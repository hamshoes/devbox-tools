// Command put-vm is the VM half of the put pair: it saves files the Mac sends
// (`puthq <file> <dest>`) as <dest>/<name>, never replacing an existing file.
// The Mac user starts every transfer, so there is no approval step.
//
// Pieces are assembled under <dir of TETHER_APP_SOCKET>/partial and then
// linked into place, or copied when dest is on another file system.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	putv1 "github.com/hamshoes/devbox-tools/pairs/put/gen/put/v1"
	"github.com/hamshoes/devbox-tools/pkg/tetherapp"
	"github.com/hamshoes/devbox-tools/pkg/tetherapp/chunk"
)

var version = "dev"

const maxFileSize = 1 << 30

// destTTL drops a remembered dest whose transfer has gone quiet (the
// receiver itself gives up after two minutes).
const destTTL = 10 * time.Minute

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "put-vm:", err)
		os.Exit(1)
	}
}

func run() error {
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return nil
	}
	_, appSock, err := tetherapp.FromEnv()
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	p := newPutter(log, home, filepath.Join(filepath.Dir(appSock), "partial"))
	app := tetherapp.NewServer()
	tetherapp.Handle(app, "/put.v1.Put/Put", p.put)
	log.Info("serving", "socket", appSock, "version", version)
	return app.Serve(ctx, appSock)
}

type putter struct {
	log  *slog.Logger
	home string
	recv *chunk.Receiver

	mu    sync.Mutex
	dests map[string]destEntry // by transfer id
}

type destEntry struct {
	dir  string
	seen time.Time
}

func newPutter(log *slog.Logger, home, partialDir string) *putter {
	return &putter{
		log:  log,
		home: home,
		recv: &chunk.Receiver{
			Dir: partialDir, MaxSize: maxFileSize,
			Approve: func(context.Context, string, int64) (bool, error) { return true, nil },
		},
		dests: map[string]destEntry{},
	}
}

func (p *putter) put(ctx context.Context, r *putv1.FilePiece) (*putv1.PutResponse, error) {
	if r.Offset == 0 {
		dir, err := resolveDest(p.home, r.Dest)
		if err != nil {
			return nil, tetherapp.Errorf(tetherapp.CodeInvalidArgument, "%v", err)
		}
		p.setDest(r.TransferId, dir)
	}
	done, err := p.recv.Put(ctx, chunk.Piece{
		ID: r.TransferId, Name: r.Name, Size: r.Size, Offset: r.Offset, Data: r.Data, Last: r.Last, SHA256: r.Sha256,
	})
	if err != nil {
		p.dropDest(r.TransferId)
		return nil, err
	}
	if done == nil {
		p.touchDest(r.TransferId)
		return &putv1.PutResponse{}, nil
	}
	dir, ok := p.dropDest(r.TransferId)
	if !ok {
		os.Remove(done.Path)
		return nil, tetherapp.Errorf(tetherapp.CodeFailedPrecondition, "no destination for this transfer")
	}
	// The partial file is 0600; a put file should look like any other new file.
	os.Chmod(done.Path, 0o644)
	path, err := keep(done.Path, dir, done.Name)
	if err != nil {
		os.Remove(done.Path)
		return nil, tetherapp.Errorf(tetherapp.CodeInternal, "%v", err)
	}
	p.log.Info("file saved", "path", path, "size", done.Size)
	return &putv1.PutResponse{SavedAs: path}, nil
}

func (p *putter) setDest(id, dir string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, e := range p.dests {
		if time.Since(e.seen) > destTTL {
			delete(p.dests, k)
		}
	}
	p.dests[id] = destEntry{dir: dir, seen: time.Now()}
}

func (p *putter) touchDest(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.dests[id]; ok {
		e.seen = time.Now()
		p.dests[id] = e
	}
}

func (p *putter) dropDest(id string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.dests[id]
	delete(p.dests, id)
	return e.dir, ok
}

// resolveDest turns the dest a Mac user typed into an existing, writable
// directory: "~" and "~/x" are under home, a relative path is relative to home.
func resolveDest(home, dest string) (string, error) {
	d := dest
	switch {
	case d == "" || d == "~":
		d = home
	case strings.HasPrefix(d, "~/"):
		d = filepath.Join(home, d[2:])
	case strings.HasPrefix(d, "~"):
		return "", fmt.Errorf("destination %q: ~user is not supported; use ~/ or an absolute path", dest)
	case !filepath.IsAbs(d):
		d = filepath.Join(home, d)
	}
	d = filepath.Clean(d)
	fi, err := os.Stat(d)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("destination %s does not exist on the VM", d)
		}
		return "", fmt.Errorf("destination %s: %v", d, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("destination %s is not a directory", d)
	}
	if err := syscall.Access(d, 0x2 /* W_OK */); err != nil {
		return "", fmt.Errorf("destination %s is not writable", d)
	}
	return d, nil
}

// keep moves tmp into dir as name without replacing anything (see
// chunk.Keep). When dir is on another file system it copies tmp into dir
// first. tmp is gone afterwards either way, except on error.
func keep(tmp, dir, name string) (string, error) {
	return keepWith(chunk.Keep, tmp, dir, name)
}

// keepWith is keep with chunk.Keep swappable, so the EXDEV path can be tested.
func keepWith(keepFn func(tmp, dir, name string) (string, error), tmp, dir, name string) (string, error) {
	path, err := keepFn(tmp, dir, name)
	if err == nil || !errors.Is(err, syscall.EXDEV) {
		return path, err
	}
	local, err := copyInto(tmp, dir)
	if err != nil {
		return "", err
	}
	path, err = keepFn(local, dir, name)
	if err != nil {
		os.Remove(local)
		return "", err
	}
	os.Remove(tmp)
	return path, nil
}

// copyInto copies src to a new hidden temporary file in dir and returns its path.
func copyInto(src, dir string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.CreateTemp(dir, ".put-*")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(out.Name())
		return "", err
	}
	if err := out.Close(); err != nil {
		os.Remove(out.Name())
		return "", err
	}
	return out.Name(), nil
}

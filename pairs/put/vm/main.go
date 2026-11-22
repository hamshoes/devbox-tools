// Command put-vm is the box half of the put pair: it takes one file from the
// Mac in pieces and puts it in a directory under the box user's home. It never
// replaces an existing file unless the caller forces it, and never writes
// through a symlink. The Mac is the trusted side, so there is no approval step.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"hash"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	putv1 "github.com/hamshoes/devbox-tools/pairs/put/gen/put/v1"
	"github.com/hamshoes/devbox-tools/pkg/tetherapp"
	"github.com/hamshoes/devbox-tools/pkg/tetherapp/chunk"
)

var version = "dev"

const (
	maxFileSize   = 1 << 30
	maxUploads    = 4
	idleTimeout   = 10 * time.Minute
	sweepInterval = time.Minute

	// Codes the tetherapp package has no constant for.
	codeAlreadyExists = "already_exists"
	codeDataLoss      = "data_loss"
)

var sha256RE = regexp.MustCompile(`^[0-9a-f]{64}$`)

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
	// Symlinks in the home path itself are fine; what matters is where it really is.
	realHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		return fmt.Errorf("resolve home: %w", err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	p := &putter{log: log, home: home, realHome: realHome, uploads: map[string]*upload{}}
	go p.sweepLoop(ctx)
	app := tetherapp.NewServer()
	tetherapp.Handle(app, "/put.v1.Put/Begin", p.begin)
	tetherapp.Handle(app, "/put.v1.Put/Chunk", p.chunk)
	tetherapp.Handle(app, "/put.v1.Put/Commit", p.commit)
	tetherapp.Handle(app, "/put.v1.Put/Abort", p.abort)
	err = app.Serve(ctx, appSock)
	p.abortAll()
	return err
}

type putter struct {
	log      *slog.Logger
	home     string
	realHome string

	mu      sync.Mutex
	uploads map[string]*upload
}

// upload is one file on its way in. mu serialises the calls for one upload.
type upload struct {
	id     string
	dir    string
	target string
	tmp    string
	size   int64
	sha256 string
	mode   os.FileMode
	force  bool

	mu      sync.Mutex
	f       *os.File
	h       hash.Hash
	written int64
	seen    time.Time
	closed  bool
}

func (p *putter) begin(_ context.Context, r *putv1.BeginRequest) (*putv1.BeginResponse, error) {
	name, err := chunk.CleanName(r.Name)
	if err != nil {
		return nil, tetherapp.Errorf(tetherapp.CodeInvalidArgument, "%v", err)
	}
	if r.Size < 0 || r.Size > maxFileSize {
		return nil, tetherapp.Errorf(tetherapp.CodeInvalidArgument, "too large: %d bytes, limit %d", r.Size, int64(maxFileSize))
	}
	if !sha256RE.MatchString(r.Sha256) {
		return nil, tetherapp.Errorf(tetherapp.CodeInvalidArgument, "sha256 must be 64 lower-case hex digits")
	}
	mode := os.FileMode(0o644)
	if r.Mode&0o111 != 0 {
		mode = 0o755
	}

	dir, err := p.resolveDir(r.Dir)
	if err != nil {
		return nil, err
	}
	target := filepath.Join(dir, name)
	if err := checkTarget(target, r.Force); err != nil {
		return nil, err
	}

	p.sweep()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.uploads) >= maxUploads {
		return nil, tetherapp.Errorf(tetherapp.CodeResourceExhausted, "at most %d uploads at once", maxUploads)
	}
	id := chunk.NewID()
	tmp := filepath.Join(dir, ".put-"+id+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, tetherapp.Errorf(tetherapp.CodeInternal, "create temporary file: %v", err)
	}
	p.uploads[id] = &upload{
		id: id, dir: dir, target: target, tmp: tmp, size: r.Size, sha256: r.Sha256, mode: mode, force: r.Force,
		f: f, h: sha256.New(), seen: time.Now(),
	}
	p.log.Info("upload started", "path", target, "size", r.Size)
	return &putv1.BeginResponse{UploadId: id, Path: target}, nil
}

// resolveDir turns the caller's dir into an absolute, symlink-free path under
// the home and creates whatever is missing.
func (p *putter) resolveDir(dir string) (string, error) {
	switch {
	case dir == "~":
		dir = p.home
	case strings.HasPrefix(dir, "~/"):
		dir = filepath.Join(p.home, dir[2:])
	case !filepath.IsAbs(dir):
		dir = filepath.Join(p.home, dir)
	}
	dir = filepath.Clean(dir)

	// Resolve the longest part that exists, then append what is still to be made.
	existing, rest := dir, ""
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
			return "", tetherapp.Errorf(tetherapp.CodeInternal, "stat directory: %v", err)
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = parent
	}
	real, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", tetherapp.Errorf(tetherapp.CodeInvalidArgument, "resolve directory: %v", err)
	}
	if !within(p.realHome, real) {
		return "", tetherapp.Errorf(tetherapp.CodePermissionDenied, "outside home")
	}
	if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
		return "", tetherapp.Errorf(tetherapp.CodeInvalidArgument, "%s is not a directory", existing)
	}
	full := filepath.Join(real, rest)
	if err := os.MkdirAll(full, 0o755); err != nil {
		if errors.Is(err, syscall.ENOTDIR) {
			return "", tetherapp.Errorf(tetherapp.CodeInvalidArgument, "a part of the directory is not a directory")
		}
		return "", tetherapp.Errorf(tetherapp.CodeInternal, "create directory: %v", err)
	}
	return full, nil
}

// within reports whether path is root or below it; both must be clean and absolute.
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// checkTarget applies the rules for the final path; Commit repeats it just before the rename.
func checkTarget(target string, force bool) error {
	fi, err := os.Lstat(target)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return tetherapp.Errorf(tetherapp.CodeInternal, "stat target: %v", err)
	case fi.Mode()&os.ModeSymlink != 0:
		return tetherapp.Errorf(tetherapp.CodePermissionDenied, "target is a symlink")
	case fi.IsDir():
		return tetherapp.Errorf(tetherapp.CodeInvalidArgument, "target is a directory")
	case !force:
		return tetherapp.Errorf(codeAlreadyExists, "target exists")
	}
	return nil
}

// get returns the upload with its mutex held; the caller must unlock it.
func (p *putter) get(id string) (*upload, error) {
	p.mu.Lock()
	u := p.uploads[id]
	p.mu.Unlock()
	if u == nil {
		return nil, tetherapp.Errorf(tetherapp.CodeFailedPrecondition, "no such upload")
	}
	u.mu.Lock()
	if u.closed {
		u.mu.Unlock()
		return nil, tetherapp.Errorf(tetherapp.CodeFailedPrecondition, "no such upload")
	}
	return u, nil
}

func (p *putter) chunk(_ context.Context, r *putv1.ChunkRequest) (*putv1.ChunkResponse, error) {
	u, err := p.get(r.UploadId)
	if err != nil {
		return nil, err
	}
	defer u.mu.Unlock()
	switch {
	case len(r.Data) > chunk.DataSize:
		p.drop(u)
		return nil, tetherapp.Errorf(tetherapp.CodeInvalidArgument, "chunk larger than %d bytes", chunk.DataSize)
	case r.Offset != u.written:
		p.drop(u)
		return nil, tetherapp.Errorf(tetherapp.CodeFailedPrecondition, "offset %d, expected %d", r.Offset, u.written)
	case u.written+int64(len(r.Data)) > u.size:
		p.drop(u)
		return nil, tetherapp.Errorf(tetherapp.CodeFailedPrecondition, "more data than the declared %d bytes", u.size)
	}
	if _, err := u.f.Write(r.Data); err != nil {
		p.drop(u)
		return nil, tetherapp.Errorf(tetherapp.CodeInternal, "write: %v", err)
	}
	u.h.Write(r.Data)
	u.written += int64(len(r.Data))
	u.seen = time.Now()
	return &putv1.ChunkResponse{}, nil
}

func (p *putter) commit(_ context.Context, r *putv1.CommitRequest) (*putv1.CommitResponse, error) {
	u, err := p.get(r.UploadId)
	if err != nil {
		return nil, err
	}
	defer u.mu.Unlock()
	if u.written != u.size {
		p.drop(u)
		return nil, tetherapp.Errorf(codeDataLoss, "got %d of %d bytes", u.written, u.size)
	}
	if hex.EncodeToString(u.h.Sum(nil)) != u.sha256 {
		p.drop(u)
		return nil, tetherapp.Errorf(codeDataLoss, "sha256 mismatch")
	}
	if err := u.f.Chmod(u.mode); err != nil {
		p.drop(u)
		return nil, tetherapp.Errorf(tetherapp.CodeInternal, "chmod: %v", err)
	}
	if err := u.f.Sync(); err != nil {
		p.drop(u)
		return nil, tetherapp.Errorf(tetherapp.CodeInternal, "sync: %v", err)
	}
	if err := checkTarget(u.target, u.force); err != nil {
		p.drop(u)
		return nil, err
	}
	if err := os.Rename(u.tmp, u.target); err != nil {
		p.drop(u)
		return nil, tetherapp.Errorf(tetherapp.CodeInternal, "rename: %v", err)
	}
	u.f.Close()
	u.closed = true
	p.forget(u)
	if err := syncDir(u.dir); err != nil {
		// The file is in place; only its durability is in doubt.
		p.log.Warn("sync directory", "path", u.dir, "err", err)
	}
	p.log.Info("upload committed", "path", u.target, "size", u.size)
	return &putv1.CommitResponse{Path: u.target, Bytes: u.written}, nil
}

func (p *putter) abort(_ context.Context, r *putv1.AbortRequest) (*putv1.AbortResponse, error) {
	if u, err := p.get(r.UploadId); err == nil {
		p.drop(u)
		u.mu.Unlock()
	}
	return &putv1.AbortResponse{}, nil
}

// drop discards u and its temporary file. The caller holds u.mu.
func (p *putter) drop(u *upload) {
	if u.closed {
		return
	}
	u.closed = true
	u.f.Close()
	if err := os.Remove(u.tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
		p.log.Warn("remove temporary file", "path", u.tmp, "err", err)
	}
	p.forget(u)
	p.log.Info("upload aborted", "path", u.target, "written", u.written)
}

func (p *putter) forget(u *upload) {
	p.mu.Lock()
	delete(p.uploads, u.id)
	p.mu.Unlock()
}

// sweep aborts uploads that have had no call for idleTimeout.
func (p *putter) sweep() {
	p.mu.Lock()
	all := make([]*upload, 0, len(p.uploads))
	for _, u := range p.uploads {
		all = append(all, u)
	}
	p.mu.Unlock()
	for _, u := range all {
		u.mu.Lock()
		if time.Since(u.seen) > idleTimeout {
			p.drop(u)
		}
		u.mu.Unlock()
	}
}

func (p *putter) sweepLoop(ctx context.Context) {
	t := time.NewTicker(sweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.sweep()
		}
	}
}

// abortAll removes every temporary file on the way out.
func (p *putter) abortAll() {
	p.mu.Lock()
	all := make([]*upload, 0, len(p.uploads))
	for _, u := range p.uploads {
		all = append(all, u)
	}
	p.mu.Unlock()
	for _, u := range all {
		u.mu.Lock()
		p.drop(u)
		u.mu.Unlock()
	}
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Package chunk moves a file over unary tether calls, which carry at most
// tetherapp.MaxBody each: the sender splits it into Pieces, the receiver
// writes them to a temporary file and asks for approval on the first one.
package chunk

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/hamshoes/devbox-tools/pkg/tetherapp"
)

// DataSize is the most file data in one Piece. protojson sends bytes as
// base64, so a Piece stays well under the 1 MiB message limit.
const DataSize = 512 << 10

// idleTimeout drops a transfer that has had no piece for this long.
const idleTimeout = 2 * time.Minute

var idRE = regexp.MustCompile(`^[a-z0-9]{16,64}$`)

// Piece is one call's worth of a transfer. Name and Size travel on every
// piece but only the first one's count; SHA256 travels on the last.
type Piece struct {
	ID     string
	Name   string
	Size   int64
	Offset int64
	Data   []byte
	Last   bool
	SHA256 string // hex, of the whole file
}

// NewID returns a random transfer ID.
func NewID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Send reads size bytes from r and passes them to send as pieces, in order.
// The first call to send is the one that waits for approval.
func Send(r io.Reader, name string, size int64, send func(Piece) error) error {
	id, h := NewID(), sha256.New()
	buf := make([]byte, DataSize)
	var off int64
	for {
		n, err := io.ReadFull(r, buf)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
			return err
		}
		h.Write(buf[:n])
		p := Piece{ID: id, Name: name, Size: size, Offset: off, Data: buf[:n]}
		off += int64(n)
		if off > size {
			return fmt.Errorf("%s grew while it was being sent", name)
		}
		if p.Last = off == size; p.Last {
			p.SHA256 = hex.EncodeToString(h.Sum(nil))
		}
		if err := send(p); err != nil {
			return err
		}
		if p.Last {
			return nil
		}
		if n == 0 {
			return fmt.Errorf("%s shrank while it was being sent", name)
		}
	}
}

// FormatSize is n bytes for people: "512 B", "1.5 KiB", "20.0 MiB".
func FormatSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// CleanName checks that name is a plain file name a sender may choose.
func CleanName(name string) (string, error) {
	switch {
	case name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) || len(name) > 255:
		return "", fmt.Errorf("bad file name %q", name)
	case strings.IndexFunc(name, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0:
		return "", fmt.Errorf("file name has control characters")
	}
	return name, nil
}

// Receiver assembles transfers into temporary files in Dir.
type Receiver struct {
	Dir     string
	MaxSize int64
	// Approve is asked once per transfer, on its first piece; anything but
	// (true, nil) refuses it.
	Approve func(ctx context.Context, name string, size int64) (bool, error)

	mu   sync.Mutex
	open map[string]*transfer
}

type transfer struct {
	name    string
	size    int64
	written int64
	f       *os.File
	h       hash.Hash
	seen    time.Time
}

// Done is a finished transfer: the file is at Path, which the caller now owns.
type Done struct {
	Name string
	Path string
	Size int64
}

// Put takes one piece. It returns a Done once the last piece is in and checked.
// Every error is a *tetherapp.Error, and aborts the transfer.
func (r *Receiver) Put(ctx context.Context, p Piece) (*Done, error) {
	if !idRE.MatchString(p.ID) {
		return nil, tetherapp.Errorf(tetherapp.CodeInvalidArgument, "bad transfer id")
	}
	if p.Offset == 0 {
		if err := r.begin(ctx, p); err != nil {
			return nil, err
		}
	}
	r.mu.Lock()
	t, ok := r.open[p.ID]
	if ok {
		delete(r.open, p.ID) // held by this call until it is put back
	}
	r.mu.Unlock()
	if !ok {
		return nil, tetherapp.Errorf(tetherapp.CodeFailedPrecondition, "unknown or expired transfer")
	}
	done, err := r.write(t, p)
	if err != nil {
		t.f.Close()
		os.Remove(t.f.Name())
		return nil, err
	}
	if done == nil {
		t.seen = time.Now()
		r.mu.Lock()
		r.open[p.ID] = t
		r.mu.Unlock()
	}
	return done, nil
}

// begin checks the first piece, asks for approval and opens the temporary file.
func (r *Receiver) begin(ctx context.Context, p Piece) error {
	name, err := CleanName(p.Name)
	if err != nil {
		return tetherapp.Errorf(tetherapp.CodeInvalidArgument, "%v", err)
	}
	if p.Size < 0 || p.Size > r.MaxSize {
		return tetherapp.Errorf(tetherapp.CodeInvalidArgument, "size %d is over the %d byte limit", p.Size, r.MaxSize)
	}
	r.sweep()
	ok, err := r.Approve(ctx, name, p.Size)
	if err != nil || !ok {
		return tetherapp.Errorf(tetherapp.CodePermissionDenied, "denied on the Mac")
	}
	if err := os.MkdirAll(r.Dir, 0o700); err != nil {
		return tetherapp.Errorf(tetherapp.CodeInternal, "%v", err)
	}
	f, err := os.CreateTemp(r.Dir, ".part-*")
	if err != nil {
		return tetherapp.Errorf(tetherapp.CodeInternal, "%v", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.open == nil {
		r.open = map[string]*transfer{}
	}
	if old, ok := r.open[p.ID]; ok {
		old.f.Close()
		os.Remove(old.f.Name())
	}
	r.open[p.ID] = &transfer{name: name, size: p.Size, f: f, h: sha256.New(), seen: time.Now()}
	return nil
}

func (r *Receiver) write(t *transfer, p Piece) (*Done, error) {
	if p.Offset != t.written {
		return nil, tetherapp.Errorf(tetherapp.CodeFailedPrecondition, "piece at %d, expected %d", p.Offset, t.written)
	}
	if t.written+int64(len(p.Data)) > t.size {
		return nil, tetherapp.Errorf(tetherapp.CodeInvalidArgument, "more data than the announced size")
	}
	if _, err := t.f.Write(p.Data); err != nil {
		return nil, tetherapp.Errorf(tetherapp.CodeInternal, "write: %v", err)
	}
	t.h.Write(p.Data)
	t.written += int64(len(p.Data))
	if !p.Last {
		return nil, nil
	}
	if t.written != t.size {
		return nil, tetherapp.Errorf(tetherapp.CodeInvalidArgument, "got %d bytes of %d", t.written, t.size)
	}
	if hex.EncodeToString(t.h.Sum(nil)) != p.SHA256 {
		return nil, tetherapp.Errorf(tetherapp.CodeInvalidArgument, "checksum mismatch")
	}
	if err := t.f.Close(); err != nil {
		return nil, tetherapp.Errorf(tetherapp.CodeInternal, "close: %v", err)
	}
	return &Done{Name: t.name, Path: t.f.Name(), Size: t.size}, nil
}

// sweep drops transfers that have gone quiet.
func (r *Receiver) sweep() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, t := range r.open {
		if time.Since(t.seen) > idleTimeout {
			t.f.Close()
			os.Remove(t.f.Name())
			delete(r.open, id)
		}
	}
}

// Keep moves the finished file at tmp into dir as name, never replacing a
// file: on a clash it tries "name (1).ext", "name (2).ext", ... tmp must be on
// the same file system as dir. It returns the final path.
func Keep(tmp, dir, name string) (string, error) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	p := filepath.Join(dir, name)
	for i := 1; i <= 1000; i++ {
		err := os.Link(tmp, p) // unlike rename, link fails if p exists
		if err == nil {
			os.Remove(tmp)
			return p, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
		p = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
	}
	return "", fmt.Errorf("no free name for %s in %s", name, dir)
}

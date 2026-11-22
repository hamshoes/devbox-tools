package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	putv1 "github.com/hamshoes/devbox-tools/pairs/put/gen/put/v1"
	"github.com/hamshoes/devbox-tools/pkg/tetherapp"
	"github.com/hamshoes/devbox-tools/pkg/tetherapp/chunk"
)

func TestResolveDest(t *testing.T) {
	home := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(home, "a", "b"), 0o755))
	must(t, os.WriteFile(filepath.Join(home, "file"), nil, 0o644))
	abs := t.TempDir()

	ok := map[string]string{
		"":           home,
		"~":          home,
		"~/":         home,
		"~/a":        filepath.Join(home, "a"),
		"~/a/b/../b": filepath.Join(home, "a", "b"),
		"a":          filepath.Join(home, "a"),
		"a/b/":       filepath.Join(home, "a", "b"),
		"./a":        filepath.Join(home, "a"),
		abs:          abs,
		abs + "/.":   abs,
	}
	for in, want := range ok {
		got, err := resolveDest(home, in)
		if err != nil || got != want {
			t.Errorf("resolveDest(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := map[string]string{
		"~/missing":    "does not exist",
		"missing":      "does not exist",
		"~/file":       "not a directory",
		"~root":        "~user",
		abs + "/nope/": "does not exist",
	}
	for in, want := range bad {
		if _, err := resolveDest(home, in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("resolveDest(%q) err = %v; want it to mention %q", in, err, want)
		}
	}

	if os.Geteuid() != 0 {
		ro := filepath.Join(home, "ro")
		must(t, os.Mkdir(ro, 0o555))
		if _, err := resolveDest(home, "ro"); err == nil || !strings.Contains(err.Error(), "not writable") {
			t.Errorf("read-only dir: err = %v", err)
		}
	}
}

func TestKeepCrossDevice(t *testing.T) {
	src, dest := t.TempDir(), t.TempDir()
	tmp := filepath.Join(src, ".part-1")
	must(t, os.WriteFile(tmp, []byte("hello"), 0o600))
	must(t, os.WriteFile(filepath.Join(dest, "f.txt"), []byte("old"), 0o644))

	// Fail like a cross-device link whenever tmp is outside dest.
	calls := 0
	fake := func(tmp, dir, name string) (string, error) {
		calls++
		if filepath.Dir(tmp) != dir {
			return "", &os.LinkError{Op: "link", Old: tmp, New: filepath.Join(dir, name), Err: syscall.EXDEV}
		}
		return chunk.Keep(tmp, dir, name)
	}
	path, err := keepWith(fake, tmp, dest, "f.txt")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("keep called %d times, want 2", calls)
	}
	if want := filepath.Join(dest, "f (1).txt"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	if b, _ := os.ReadFile(path); string(b) != "hello" {
		t.Errorf("content = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "f.txt")); string(b) != "old" {
		t.Errorf("existing file changed: %q", b)
	}
	if _, err := os.Stat(tmp); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("tmp left behind: %v", err)
	}
	ents, _ := os.ReadDir(dest)
	if len(ents) != 2 {
		t.Errorf("dest has %d entries, want 2 (no leftover temp files)", len(ents))
	}

	// Other errors are not retried.
	other := errors.New("boom")
	if _, err := keepWith(func(string, string, string) (string, error) { return "", other }, tmp, dest, "x"); err != other {
		t.Errorf("err = %v, want boom", err)
	}
}

func TestRoundTrip(t *testing.T) {
	home := t.TempDir()
	must(t, os.Mkdir(filepath.Join(home, "in"), 0o755))
	// Unix socket paths are short; keep the socket dir short too.
	sockDir, err := os.MkdirTemp("", "put")
	must(t, err)
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "app.sock")

	p := newPutter(slog.New(slog.NewTextHandler(io.Discard, nil)), home, filepath.Join(sockDir, "partial"))
	app := tetherapp.NewServer()
	tetherapp.Handle(app, "/put.v1.Put/Put", p.put)
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- app.Serve(ctx, sock) }()
	t.Cleanup(func() { cancel(); <-served })
	waitSocket(t, sock)

	data := make([]byte, chunk.DataSize*2+12345) // three pieces
	rand.Read(data)

	putFile := func(dest string) (string, error) {
		var saved string
		err := chunk.Send(bytes.NewReader(data), "blob.bin", int64(len(data)), func(pc chunk.Piece) error {
			var resp putv1.PutResponse
			req := &putv1.FilePiece{TransferId: pc.ID, Name: pc.Name, Size: pc.Size, Offset: pc.Offset,
				Data: pc.Data, Last: pc.Last, Sha256: pc.SHA256, Dest: dest}
			if err := tetherapp.Call(context.Background(), sock, "/put.v1.Put/Put", req, &resp); err != nil {
				return err
			}
			saved = resp.SavedAs
			return nil
		})
		return saved, err
	}

	for i, want := range []string{"blob.bin", "blob (1).bin"} {
		saved, err := putFile("~/in")
		if err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
		if w := filepath.Join(home, "in", want); saved != w {
			t.Fatalf("put %d saved as %q, want %q", i, saved, w)
		}
		got, err := os.ReadFile(saved)
		must(t, err)
		if !bytes.Equal(got, data) {
			t.Fatalf("put %d: content differs", i)
		}
		if fi, _ := os.Stat(saved); fi.Mode().Perm() != 0o644 {
			t.Errorf("put %d: mode %v, want 0644", i, fi.Mode().Perm())
		}
	}

	_, err = putFile("~/nowhere")
	var te *tetherapp.Error
	if !errors.As(err, &te) || te.Code != tetherapp.CodeInvalidArgument || !strings.Contains(te.Message, "does not exist") {
		t.Fatalf("bad dest: err = %v", err)
	}
	if n := len(p.dests); n != 0 {
		t.Errorf("%d dests still remembered", n)
	}
}

func waitSocket(t *testing.T, path string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if fi, err := os.Stat(path); err == nil && fi.Mode().Type() == os.ModeSocket {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket %s never appeared", path)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

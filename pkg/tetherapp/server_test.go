package tetherapp

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func sockPath(t *testing.T) string {
	t.Helper()
	// Short dir: unix socket paths are limited to ~104 bytes on macOS.
	dir, err := os.MkdirTemp("", "ta")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "s.sock")
}

func TestListenUnixLiveSocket(t *testing.T) {
	path := sockPath(t)
	first, err := ListenUnix(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	accepted := make(chan error, 1)
	go func() {
		c, err := first.Accept()
		if err == nil {
			c.Close()
		}
		accepted <- err
	}()

	// The liveness dial is accepted by the goroutine above; start another for the check below.
	if _, err := ListenUnix(path); !errors.Is(err, ErrInUse) {
		t.Fatalf("second ListenUnix: got %v, want ErrInUse", err)
	}
	if err := <-accepted; err != nil {
		t.Fatal(err)
	}

	go func() {
		c, err := first.Accept()
		if err == nil {
			c.Close()
		}
		accepted <- err
	}()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("first listener no longer reachable: %v", err)
	}
	c.Close()
	if err := <-accepted; err != nil {
		t.Fatalf("first listener: %v", err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket after failed second listen: %v %v", fi, err)
	}
}

func TestListenUnixStaleSocket(t *testing.T) {
	path := sockPath(t)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close() // leaves the file behind, like a crashed process
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	ln2, err := ListenUnix(path)
	if err != nil {
		t.Fatalf("stale socket not replaced: %v", err)
	}
	ln2.Close()
}

func TestListenUnixNotASocket(t *testing.T) {
	path := sockPath(t)
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ln, err := ListenUnix(path)
	if err != nil {
		t.Fatalf("regular file not replaced: %v", err)
	}
	ln.Close()
}

func TestListenUnixMissing(t *testing.T) {
	ln, err := ListenUnix(sockPath(t))
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
}

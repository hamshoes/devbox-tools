package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// errLocked means another controld holds the instance lock.
var errLocked = errors.New("another controld is already running")

// lockPath is the instance lock, kept next to the local socket.
func lockPath(controldSocket string) string {
	return filepath.Join(filepath.Dir(controldSocket), "controld.lock")
}

// acquireLock takes an exclusive flock on path for the life of the process.
// The returned file must stay open; the kernel drops the lock when the
// process exits, however it exits, so a crash never leaves a stale lock.
// The lock file itself is never removed: unlinking it would let a later
// controld lock a fresh inode while an older one still holds the old one.
// The descriptor is close-on-exec, so supervised children don't inherit it.
func acquireLock(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w (lock %s is held)", errLocked, path)
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return f, nil
}

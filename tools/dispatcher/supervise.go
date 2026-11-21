package main

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/hamshoes/devbox-tools/pkg/dispatch"
)

const (
	minBackoff  = time.Second
	maxBackoff  = 30 * time.Second
	stableAfter = time.Minute
	maxLogBytes = 10 << 20
)

// supervised runs one child process and restarts it, with backoff, whenever it exits.
type supervised struct {
	log  *slog.Logger
	argv []string
	env  []string
	logf string // stdout and stderr go here
	// onExit is called after every exit; returning false stops the supervision.
	onExit func() bool
}

// childEnv is the dispatcher's environment minus its own token, plus extra.
func childEnv(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, dispatch.TokenEnv+"=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, extra...)
}

func openLog(path string) *os.File {
	if st, err := os.Stat(path); err == nil && st.Size() > maxLogBytes {
		os.Remove(path)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil
	}
	return f
}

// run blocks until ctx ends or onExit says stop. The child gets SIGTERM, then SIGKILL after 5 seconds.
func (s *supervised) run(ctx context.Context) {
	backoff := minBackoff
	for ctx.Err() == nil {
		cmd := exec.CommandContext(ctx, s.argv[0], s.argv[1:]...)
		cmd.Env = s.env
		cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
		cmd.WaitDelay = 5 * time.Second
		lf := openLog(s.logf)
		if lf != nil {
			cmd.Stdout, cmd.Stderr = lf, lf
		}
		began := time.Now()
		err := cmd.Run()
		if lf != nil {
			lf.Close()
		}
		if ctx.Err() != nil {
			return
		}
		ran := time.Since(began).Round(time.Millisecond)
		s.log.Warn("child exited", "err", err, "ran", ran)
		if s.onExit != nil && !s.onExit() {
			return
		}
		if ran > stableAfter {
			backoff = minBackoff
		}
		t := time.NewTimer(backoff)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

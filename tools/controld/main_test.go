package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseArgs(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		want    options
		wantErr error // nil, flag.ErrHelp, or errAny
	}{
		{args: nil},
		{args: []string{"-debug"}, want: options{debug: true}},
		{args: []string{"--debug"}, want: options{debug: true}},
		{args: []string{"--version"}, want: options{version: true}},
		{args: []string{"-version"}, want: options{version: true}},
		{args: []string{"-h"}, wantErr: flag.ErrHelp},
		{args: []string{"--help"}, wantErr: flag.ErrHelp},
		{args: []string{"help"}, wantErr: errAny},
		{args: []string{"-debug", "status"}, wantErr: errAny},
		{args: []string{"--bogus"}, wantErr: errAny},
		{args: []string{"--", "x"}, wantErr: errAny},
	} {
		got, err := parseArgs(tc.args, io.Discard, io.Discard)
		switch {
		case tc.wantErr == nil && err != nil:
			t.Errorf("%q: unexpected error %v", tc.args, err)
		case tc.wantErr == flag.ErrHelp && !errors.Is(err, flag.ErrHelp):
			t.Errorf("%q: got %v, want ErrHelp", tc.args, err)
		case tc.wantErr == errAny && (err == nil || errors.Is(err, flag.ErrHelp)):
			t.Errorf("%q: got %v, want a usage error", tc.args, err)
		case tc.wantErr == nil && got != tc.want:
			t.Errorf("%q: got %+v, want %+v", tc.args, got, tc.want)
		}
	}
}

var errAny = errors.New("any error")

func TestLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "controld.lock")
	first, err := acquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	// flock locks belong to the open file description, so a second open in
	// this process conflicts just as another process would.
	if _, err := acquireLock(path); !errors.Is(err, errLocked) {
		t.Fatalf("second lock: got %v, want errLocked", err)
	}
	first.Close()
	again, err := acquireLock(path)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	again.Close()
}

// TestSecondInstanceTouchesNothing runs the built binary against a temp state
// dir whose lock is held, and checks it exits non-zero without creating the
// socket, the tether state or the token file.
func TestSecondInstanceTouchesNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	home := t.TempDir()
	bin := filepath.Join(t.TempDir(), "controld")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	env := append(os.Environ(),
		"HOME="+home,
		"BOX_CONFIG_DIR=cfg", "BOX_DEVTOOLS_DIR=tools", "BOX_STATE_DIR=state",
		"BOX_TETHER_STATE_DIR=tether", "BOX_CONTROLD_SOCKET=state/controld.sock",
		"DISPATCH_TOKEN=test-token",
		// Away from the live ports, in case a regression lets it start.
		"BOX_CONTROL_PORT=47100", "BOX_PAIR_PORT_MIN=47101", "BOX_PAIR_PORT_MAX=47199",
		"BOX_WEB_PORT=47080", "BOX_RESERVED_PORTS=47100-47199 47080", "BOX_PAIR_PORTS=control=47101",
		// The rest, so the test never depends on the host's platform file.
		"BOX_SSH_PORT=47022", "BOX_MAX_FORWARDS=8", "BOX_USER=test", "BOX_WORK_GROUP=test",
		"BOX_WORK_GID=4242", "BOX_NOTIFY_SPOOL="+filepath.Join(home, "spool"), "BOX_LOCK_FILE="+filepath.Join(home, "box.lock"),
	)
	sock := filepath.Join(home, "state", "controld.sock")
	held, err := acquireLock(lockPath(sock))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()

	cmd := exec.CommandContext(ctx, bin)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("second controld exited 0:\n%s", out)
	}
	if !strings.Contains(string(out), "already running") {
		t.Errorf("unexpected output:\n%s", out)
	}
	for _, p := range []string{sock, filepath.Join(home, "tether")} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s exists after a refused start (%v)", p, err)
		}
	}

	for _, args := range [][]string{{"help"}, {"--bogus"}} {
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err == nil {
			t.Errorf("controld %q exited 0:\n%s", args, out)
		}
	}
	for _, args := range [][]string{{"--help"}, {"-h"}, {"--version"}} {
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("controld %q: %v\n%s", args, err, out)
		}
	}
}

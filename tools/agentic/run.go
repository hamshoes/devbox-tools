package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

const preHookTimeout = 7 * 24 * 60 * 60 // seconds: a push waits as long as Sam takes

// shellQuote makes s safe as one word of the shell command a hook runs.
func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-+:@%") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// claudeSettings is the settings overlay passed to claude with --settings. Hooks merge with the user's and
// the project's own.
func claudeSettings(self string) ([]byte, error) {
	hook := func(sub string, timeout int) []any {
		return []any{map[string]any{"matcher": "Bash", "hooks": []any{map[string]any{
			"type": "command", "command": shellQuote(self) + " hook " + sub, "timeout": timeout,
		}}}}
	}
	return json.MarshalIndent(map[string]any{"hooks": map[string]any{
		"PreToolUse":         hook("pre", preHookTimeout),
		"PostToolUse":        hook("post", 10),
		"PostToolUseFailure": hook("post", 10),
	}}, "", "  ")
}

// writeSettings makes sure ~/.config/agentic/claude-settings.json holds the current settings and returns its path.
func writeSettings() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	want, err := claudeSettings(self)
	if err != nil {
		return "", err
	}
	want = append(want, '\n')
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".config", "agentic")
	path := filepath.Join(dir, "claude-settings.json")
	if have, err := os.ReadFile(path); err == nil && bytes.Equal(have, want) {
		return path, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".settings-")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	_, werr := tmp.Write(want)
	if err := errors.Join(werr, tmp.Close(), os.Chmod(tmp.Name(), 0o644), os.Rename(tmp.Name(), path)); err != nil {
		return "", err
	}
	return path, nil
}

func runCmd(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	name := fs.String("name", "", "run name (default: the current directory's name)")
	if err := fs.Parse(args); err != nil || fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: agentic run [--name N] -- <cmd...>")
		return 2
	}
	argv := fs.Args()
	cwd, _ := os.Getwd()
	runName := *name
	if runName == "" {
		runName = filepath.Base(cwd)
	}
	runID := newID()
	os.Setenv("AGENTIC", "1")
	os.Setenv("AGENTIC_RUN_ID", runID)
	os.Setenv("AGENTIC_RUN_NAME", runName)

	if filepath.Base(argv[0]) == "claude" {
		// Without the hooks the push gate would silently be missing, so a failure here stops the run.
		path, err := writeSettings()
		if err != nil {
			fmt.Fprintln(os.Stderr, "agentic: writing claude settings:", err)
			return 1
		}
		argv = append([]string{argv[0], "--settings", path}, argv[1:]...)
	}

	logEvent(event{Event: "run_start", Cwd: cwd, Command: strings.Join(argv, " ")})
	pruneRuns()

	c := exec.Command(argv[0], argv[1:]...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	var code int
	if err := c.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "agentic:", err)
		code = 127
		if errors.Is(err, os.ErrPermission) {
			code = 126
		}
	} else {
		go func() {
			for s := range sigs { // the terminal sends ^C to the child as well; this covers kill
				c.Process.Signal(s)
			}
		}()
		err := c.Wait()
		var ee *exec.ExitError
		switch {
		case err == nil:
		case errors.As(err, &ee):
			code = ee.ExitCode()
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				code = 128 + int(ws.Signal())
			}
		default:
			code = 1
		}
	}
	signal.Stop(sigs)
	logEvent(event{Event: "run_end", Exit: &code})
	return code
}

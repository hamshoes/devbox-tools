// Package project implements `proj start` and `proj work`.
package project

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/hamshoes/devbox-tools/tools/proj/internal/tmux"
)

// ErrCancelled means the user picked nothing; the caller exits 1 without a message.
var ErrCancelled = errors.New("cancelled")

// Start creates a project under root and returns its absolute path.
func Start(root string, args []string) (string, error) {
	var rel, module string
	var goInit, ghCreate bool
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--go":
			goInit = true
		case a == "--gh":
			ghCreate = true
		case a == "--module":
			i++
			if i == len(args) {
				return "", errors.New("--module needs a value")
			}
			module = args[i]
		case strings.HasPrefix(a, "--module="):
			module = strings.TrimPrefix(a, "--module=")
		case strings.HasPrefix(a, "--"):
			return "", fmt.Errorf("unknown flag %s", a)
		case rel == "":
			rel = a
		default:
			return "", errors.New("usage: proj start <rel/path> [--go] [--module <path>] [--gh]")
		}
	}
	if rel == "" {
		return "", errors.New("usage: proj start <rel/path> [--go] [--module <path>] [--gh]")
	}
	if filepath.IsAbs(rel) {
		return "", errors.New("path must be relative to the projects root")
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".." {
			return "", errors.New("path must not contain ..")
		}
	}
	rel = filepath.Clean(rel)
	dir := filepath.Join(root, rel)
	name := filepath.Base(dir)
	if _, err := os.Lstat(dir); err == nil {
		return "", fmt.Errorf("%s already exists", dir)
	}

	if goInit && module == "" {
		out, err := exec.Command("gh", "api", "user", "-q", ".login").Output()
		login := strings.TrimSpace(string(out))
		if err != nil || login == "" {
			return "", errors.New("could not get GitHub login from gh; pass --module <path>")
		}
		module = "github.com/" + login + "/" + name
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := run(dir, "git", "init", "-q"); err != nil {
		return "", err
	}
	if goInit {
		if err := run(dir, "go", "mod", "init", module); err != nil {
			return "", err
		}
	}
	if ghCreate {
		if err := run(dir, "gh", "repo", "create", name, "--private", "--source", ".", "--remote", "origin"); err != nil {
			fmt.Fprintln(os.Stderr, "proj: gh repo create failed; the token may not be allowed to create repos. Continuing.")
		}
	}
	tmux.Follow(dir)
	return dir, nil
}

// Work picks a git repo under root with fzf and returns its absolute path.
func Work(root string, args []string) (string, error) {
	if len(args) > 1 {
		return "", errors.New("usage: proj work [query]")
	}
	query := ""
	if len(args) == 1 {
		query = args[0]
	}
	repos, err := FindRepos(root)
	if err != nil {
		return "", err
	}
	if len(repos) == 0 {
		fmt.Fprintln(os.Stderr, "proj: no repositories under", root)
		return "", ErrCancelled
	}
	cmd := exec.Command("fzf", "--query", query, "--select-1", "--exit-0", "--height", "40%", "--reverse")
	cmd.Stdin = strings.NewReader(strings.Join(repos, "\n") + "\n")
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	pick := strings.TrimSpace(string(out))
	if err != nil || pick == "" {
		return "", ErrCancelled
	}
	dir := filepath.Join(root, pick)
	tmux.Follow(dir)
	return dir, nil
}

// run executes a command in dir with its stdout sent to stderr, so stdout stays clean.
func run(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Command proj creates and jumps between projects and scratch folders.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hamshoes/devbox-tools/tools/proj/internal/project"
	"github.com/hamshoes/devbox-tools/tools/proj/internal/scratch"
	"github.com/hamshoes/devbox-tools/tools/proj/internal/shell"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		if !errors.Is(err, project.ErrCancelled) {
			fmt.Fprintln(os.Stderr, "proj:", err)
		}
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: proj start|work|scratch|init|--version")
	}
	rest := args[1:]
	switch args[0] {
	case "--version":
		fmt.Println(version)
	case "init":
		if len(rest) != 1 || rest[0] != "zsh" {
			return errors.New("usage: proj init zsh")
		}
		fmt.Print(shell.Zsh)
	case "start":
		return printPath(project.Start(root("PROJECTS_ROOT", "projects"), rest))
	case "work":
		return printPath(project.Work(root("PROJECTS_ROOT", "projects"), rest))
	case "scratch":
		sroot := root("SCRATCH_ROOT", "scratch")
		if len(rest) > 0 && rest[0] == "ls" {
			return scratch.List(sroot)
		}
		if len(rest) > 0 && rest[0] == "clean" {
			return scratch.Clean(sroot, rest[1:])
		}
		return printPath(scratch.New(sroot, rest))
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
	return nil
}

// printPath writes the one path the shell wrapper will cd to.
func printPath(dir string, err error) error {
	if err != nil {
		return err
	}
	fmt.Println(dir)
	return nil
}

func root(env, def string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, def)
}

// Command proj creates projects and jumps between them.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hamshoes/devbox-tools/tools/proj/internal/project"
	"github.com/hamshoes/devbox-tools/tools/proj/internal/shell"
)

var version = "dev"

const usage = `usage: proj start <rel/path> [--go] [--module <path>] [--gh]
       proj work [query]
       proj init zsh
       proj --version

  start   create $PROJECTS_ROOT/<rel/path>, run git init, print its path
          --go      run go mod init (module: --module, else github.com/<gh login>/<name>)
          --gh      create a private GitHub repo with gh
  work    pick a git repo under $PROJECTS_ROOT with fzf, print its path
  init    print the zsh wrapper that cd's into the printed paths

PROJECTS_ROOT defaults to $HOME/projects.
`

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
		return errors.New("usage: proj start|work|init|--version (-h for help)")
	}
	for _, a := range args {
		if a == "-h" || a == "--help" {
			fmt.Print(usage)
			return nil
		}
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
		return printPath(project.Start(projectsRoot(), rest))
	case "work":
		return printPath(project.Work(projectsRoot(), rest))
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

func projectsRoot() string {
	if v := os.Getenv("PROJECTS_ROOT"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "projects")
}

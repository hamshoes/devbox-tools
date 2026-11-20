// Command scratch creates and manages throwaway folders.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hamshoes/devbox-tools/tools/scratch/internal/scratch"
	"github.com/hamshoes/devbox-tools/tools/scratch/internal/shell"
)

var version = "dev"

const usage = `usage: scratch [name] [--go]    create $SCRATCH_ROOT/<YYYYMMDD>-<name> (default name "tmp") and print its path
       scratch ls               list scratch folders, newest first
       scratch clean [days]     delete scratch folders older than days (default 30), after confirming
       scratch init zsh         print the zsh wrapper that cd's into new folders
       scratch --version

  --go    also run go mod init and write a minimal main.go

SCRATCH_ROOT defaults to $HOME/scratch.
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "scratch:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			fmt.Print(usage)
			return nil
		}
	}
	root := os.Getenv("SCRATCH_ROOT")
	if root == "" {
		home, _ := os.UserHomeDir()
		root = filepath.Join(home, "scratch")
	}
	if len(args) > 0 {
		switch args[0] {
		case "--version":
			fmt.Println(version)
			return nil
		case "init":
			if len(args) != 2 || args[1] != "zsh" {
				return errors.New("usage: scratch init zsh")
			}
			fmt.Print(shell.Zsh)
			return nil
		case "ls":
			return scratch.List(root)
		case "clean":
			return scratch.Clean(root, args[1:])
		}
	}
	dir, err := scratch.New(root, args)
	if err != nil {
		return err
	}
	fmt.Println(dir)
	return nil
}

// Command agentic wraps agent runs: it logs every Bash command Claude Code runs, and holds each
// `git push` until it is approved on the Mac (through the desk pair's spool).
package main

import (
	"fmt"
	"os"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	rest := os.Args[2:]
	switch os.Args[1] {
	case "--version", "-version":
		fmt.Println(version)
	case "run":
		os.Exit(runCmd(rest))
	case "hook":
		if len(rest) != 1 {
			usage()
		}
		switch rest[0] {
		case "pre":
			os.Exit(hookPre())
		case "post":
			os.Exit(hookPost())
		}
		usage()
	case "ask":
		os.Exit(askCmd(rest))
	case "pending":
		os.Exit(pendingCmd())
	case "cancel":
		os.Exit(cancelCmd(rest))
	case "log":
		os.Exit(logCmd(rest))
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  agentic run [--name N] -- <cmd...>
  agentic ask --title T [--detail D] [--kind generic]
  agentic pending
  agentic cancel <ask_id> | --all | --run <run_id>
  agentic log [--run R] [-f]
  agentic hook pre|post
  agentic --version`)
	os.Exit(2)
}

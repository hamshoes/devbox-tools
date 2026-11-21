// Command dispatch asks the local dispatcher to run an action on the Mac. Invoked as xdg-open, it opens a URL there.
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/hamshoes/devbox-tools/pkg/dispatch"
)

var version = "dev"

const (
	exitUnreachable = 1
	exitInvalid     = 2
	exitNotMac      = 3
)

func main() {
	if filepath.Base(os.Args[0]) == "xdg-open" {
		if len(os.Args) != 2 {
			fmt.Fprintln(os.Stderr, "usage: xdg-open <url>")
			os.Exit(exitInvalid)
		}
		os.Exit(openOrPrint(os.Args[1]))
	}

	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "--version", "-version":
		fmt.Println(version)
	case "open":
		if len(os.Args) != 3 {
			usage()
		}
		os.Exit(open(os.Args[2]))
	case "open-or-print":
		if len(os.Args) != 3 {
			usage()
		}
		os.Exit(openOrPrint(os.Args[2]))
	case "status":
		os.Exit(status())
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: dispatch open <url> | open-or-print <url> | status | --version")
	os.Exit(exitInvalid)
}

func open(rawURL string) int {
	exec, err := dispatch.NewOpenURL(rawURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitInvalid
	}
	resp, err := call(dispatch.Request{Action: dispatch.ActionOpenURL, Args: exec.Args})
	switch {
	case err != nil:
		fmt.Fprintln(os.Stderr, "dispatch: dispatcher unreachable:", err)
		return exitUnreachable
	case !resp.OK:
		fmt.Fprintln(os.Stderr, "dispatch:", resp.Error)
		return exitUnreachable
	case !resp.Delivered:
		fmt.Fprintln(os.Stderr, "dispatch:", resp.Error)
		return exitNotMac
	}
	return 0
}

func openOrPrint(rawURL string) int {
	code := open(rawURL)
	switch code {
	case 0, exitInvalid:
		return code
	}
	fmt.Fprintf(os.Stderr, "Open this URL on your Mac: %s\n", rawURL)
	return 0
}

func status() int {
	resp, err := call(dispatch.Request{Action: dispatch.ActionStatus})
	if err != nil || !resp.OK {
		fmt.Println("dispatcher: down")
		return 1
	}
	fmt.Println("dispatcher: up")
	if resp.Delivered {
		fmt.Println("mac: connected")
	} else {
		fmt.Println("mac: not connected")
	}
	return 0
}

func call(req dispatch.Request) (dispatch.Response, error) {
	var resp dispatch.Response
	conn, err := net.DialTimeout("unix", dispatch.SocketPath(), 2*time.Second)
	if err != nil {
		return resp, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return resp, err
	}
	err = json.NewDecoder(conn).Decode(&resp)
	return resp, err
}

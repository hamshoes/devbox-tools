package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/hamshoes/devbox-tools/pkg/dispatch"
)

// deliver sends one action to the dispatcher and maps the outcome to an exit code and a message.
func deliver(action string, args json.RawMessage) (int, string) {
	resp, err := call(dispatch.Request{Action: action, Args: args})
	switch {
	case err != nil:
		return exitUnreachable, "dispatch: dispatcher unreachable: " + err.Error()
	case !resp.OK:
		return exitUnreachable, "dispatch: " + resp.Error
	case !resp.Delivered:
		return exitNotMac, "dispatch: " + resp.Error
	}
	return 0, ""
}

// finish prints msg and returns code, unless quiet: then it prints nothing and always succeeds.
func finish(quiet bool, code int, msg string) int {
	if quiet {
		return 0
	}
	if msg != "" {
		fmt.Fprintln(os.Stderr, msg)
	}
	return code
}

// quietFlag reports whether --quiet appears before any "--", so even a bad invocation can stay silent.
func quietFlag(argv []string) bool {
	for _, a := range argv {
		switch a {
		case "--":
			return false
		case "--quiet":
			return true
		}
	}
	return false
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// textArg joins the words, or reads stdin when there are none. At most limit+1 bytes are read from stdin.
func textArg(words []string, limit int64) (string, error) {
	if len(words) > 0 {
		return strings.Join(words, " "), nil
	}
	if isTerminal(os.Stdin) {
		return "", errors.New("no text: pass it as arguments or on stdin")
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, limit+1))
	return string(data), err
}

func notify(argv []string) int {
	quiet := quietFlag(argv)
	args := dispatch.NotifyArgs{Title: "devbox"}
	var words []string
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		next := func() (string, bool) {
			i++
			if i >= len(argv) {
				return "", false
			}
			return argv[i], true
		}
		var ok bool
		switch {
		case a == "--":
			words = append(words, argv[i+1:]...)
			i = len(argv)
		case a == "--quiet":
		case a == "-t":
			args.Title, ok = next()
		case a == "-s":
			args.Subtitle, ok = next()
		case a == "--url":
			args.URL, ok = next()
		case strings.HasPrefix(a, "-") && len(a) > 1:
			return finish(quiet, exitInvalid, "usage: dispatch notify [-t title] [-s subtitle] [--url u] [--quiet] [message…]")
		default:
			words = append(words, a)
		}
		if (a == "-t" || a == "-s" || a == "--url") && !ok {
			return finish(quiet, exitInvalid, "dispatch: "+a+" needs a value")
		}
	}
	msg, err := textArg(words, 64<<10)
	if err != nil {
		return finish(quiet, exitInvalid, "dispatch: "+err.Error())
	}
	args.Message = msg
	exec, err := dispatch.NewNotify(args)
	if err != nil {
		return finish(quiet, exitInvalid, err.Error())
	}
	code, m := deliver(dispatch.ActionNotify, exec.Args)
	return finish(quiet, code, m)
}

func clip(argv []string) int {
	quiet := quietFlag(argv)
	var words []string
	for i, a := range argv {
		if a == "--" {
			words = append(words, argv[i+1:]...)
			break
		}
		if a == "--quiet" {
			continue
		}
		if strings.HasPrefix(a, "-") && len(a) > 1 {
			return finish(quiet, exitInvalid, "usage: dispatch clip [--quiet] [text…]")
		}
		words = append(words, a)
	}
	text, err := textArg(words, dispatch.MaxClipBytes)
	if err != nil {
		return finish(quiet, exitInvalid, "dispatch: "+err.Error())
	}
	exec, err := dispatch.NewClip(text)
	if err != nil {
		return finish(quiet, exitInvalid, err.Error())
	}
	code, msg := deliver(dispatch.ActionClip, exec.Args)
	if code == exitNotMac && !quiet && isTerminal(os.Stdout) {
		// tmux has set-clipboard on, so the terminal carries the text to the Mac clipboard.
		fmt.Fprintf(os.Stdout, "\x1b]52;c;%s\a", base64.StdEncoding.EncodeToString([]byte(text)))
		fmt.Fprintln(os.Stderr, "copied via terminal (mac not connected)")
		return 0
	}
	return finish(quiet, code, msg)
}

package main

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxCommandBytes = 4 << 10
	keepRuns        = 200
)

// event is one line of a run's audit log.
type event struct {
	At          string `json:"at"`
	RunID       string `json:"run_id"`
	RunName     string `json:"run_name"`
	Event       string `json:"event"` // run_start|cmd|result|ask|decision|run_end
	Cwd         string `json:"cwd,omitempty"`
	Command     string `json:"command,omitempty"`
	Exit        *int   `json:"exit,omitempty"`
	AskID       string `json:"ask_id,omitempty"`
	Decision    string `json:"decision,omitempty"` // allow|deny|cancelled
	Note        string `json:"note,omitempty"`
	ToolUseID   string `json:"tool_use_id,omitempty"`
	Interrupted bool   `json:"interrupted,omitempty"`
	Failed      bool   `json:"failed,omitempty"`
}

// newID is a time-sortable lower-case ID: milliseconds in base 36, then random characters.
func newID() string {
	var b [8]byte
	rand.Read(b[:])
	id := []byte(strconv.FormatInt(time.Now().UnixMilli(), 36))
	for _, c := range b[:6] {
		id = strconv.AppendInt(id, int64(c%36), 36)
	}
	return string(id)
}

func runsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "agentic", "runs"), nil
}

// logEvent appends e to the current run's log (the run in $AGENTIC_RUN_ID). Auditing is best effort: a
// failure to write is reported on stderr and never stops the command being logged.
func logEvent(e event) {
	if e.RunID == "" {
		e.RunID = os.Getenv("AGENTIC_RUN_ID")
	}
	if e.RunName == "" {
		e.RunName = os.Getenv("AGENTIC_RUN_NAME")
	}
	e.At = time.Now().UTC().Format(time.RFC3339Nano)
	e.Command = truncUTF8(e.Command, maxCommandBytes)
	name := e.RunID
	if name == "" {
		name = "adhoc"
	}
	dir, err := runsDir()
	if err == nil {
		err = os.MkdirAll(dir, 0o700)
	}
	var f *os.File
	if err == nil {
		f, err = os.OpenFile(filepath.Join(dir, name+".jsonl"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	}
	if err == nil {
		defer f.Close()
		var line []byte
		if line, err = json.Marshal(e); err == nil {
			_, err = f.Write(append(line, '\n'))
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentic: audit log:", err)
	}
}

// truncUTF8 cuts s to at most n bytes without splitting a character.
func truncUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// pruneRuns deletes the oldest run logs beyond keepRuns.
func pruneRuns() {
	dir, err := runsDir()
	if err != nil {
		return
	}
	files := runFiles(dir)
	for i := 0; i < len(files)-keepRuns; i++ {
		os.Remove(filepath.Join(dir, files[i]))
	}
}

// runFiles lists run log names, oldest first.
func runFiles(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	type f struct {
		name string
		at   time.Time
	}
	var fs []f
	for _, e := range ents {
		if fi, err := e.Info(); err == nil && strings.HasSuffix(e.Name(), ".jsonl") && fi.Mode().IsRegular() {
			fs = append(fs, f{e.Name(), fi.ModTime()})
		}
	}
	sort.Slice(fs, func(i, j int) bool { return fs[i].at.Before(fs[j].at) })
	names := make([]string, len(fs))
	for i, f := range fs {
		names[i] = f.name
	}
	return names
}

// ---- agentic log ----

func logCmd(args []string) int {
	var run string
	var follow bool
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-f":
			follow = true
		case a == "--run" && i+1 < len(args):
			i++
			run = args[i]
		case strings.HasPrefix(a, "--run="):
			run = strings.TrimPrefix(a, "--run=")
		default:
			fmt.Fprintln(os.Stderr, "usage: agentic log [--run R] [-f]")
			return 2
		}
	}
	dir, err := runsDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentic:", err)
		return 1
	}
	pick := func() string { // the named run (an ID prefix), else the newest
		files := runFiles(dir)
		if run == "" {
			if len(files) > 0 {
				return files[len(files)-1]
			}
			return ""
		}
		for i := len(files) - 1; i >= 0; i-- {
			if strings.HasPrefix(files[i], run) {
				return files[i]
			}
		}
		return ""
	}
	name := pick()
	if name == "" && !follow {
		fmt.Fprintln(os.Stderr, "agentic: no matching run")
		return 1
	}
	var off int64
	for {
		if name != "" {
			off = printFrom(filepath.Join(dir, name), off)
		}
		if !follow {
			return 0
		}
		time.Sleep(500 * time.Millisecond)
		if next := pick(); next != name {
			name, off = next, 0
		}
	}
}

// printFrom pretty-prints the complete lines of path after offset off, and returns the new offset.
func printFrom(path string, off int64) int64 {
	f, err := os.Open(path)
	if err != nil {
		return off
	}
	defer f.Close()
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return off
	}
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil { // a partial last line is left for the next pass
			return off
		}
		off += int64(len(line))
		var e event
		if json.Unmarshal(line, &e) == nil {
			fmt.Println(pretty(e))
		}
	}
}

func pretty(e event) string {
	at := e.At
	if t, err := time.Parse(time.RFC3339Nano, e.At); err == nil {
		at = t.Local().Format("01-02 15:04:05")
	}
	var detail string
	switch e.Event {
	case "cmd":
		detail = e.Command
	case "result":
		detail = "done"
		switch {
		case e.Interrupted:
			detail = "interrupted"
		case e.Failed:
			detail = "failed"
		}
		if e.Exit != nil {
			detail += " exit " + strconv.Itoa(*e.Exit)
		}
	case "ask":
		detail = e.AskID + "  " + e.Command
	case "decision":
		detail = e.Decision + "  " + e.AskID
	case "run_start", "run_end":
		detail = e.Command
		if e.Exit != nil {
			detail = "exit " + strconv.Itoa(*e.Exit)
		}
	}
	if e.Note != "" {
		detail += "  (" + e.Note + ")"
	}
	return fmt.Sprintf("%s  %-9s %-10s %s", at, e.Event, e.RunName, strings.TrimSpace(detail))
}

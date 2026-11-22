package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/hamshoes/devbox-tools/pkg/platform"
)

// The spool at DEVBOX_NOTIFY_SPOOL is shared with the notify pair's VM app by file contract only;
// nothing here imports the pair.

var idPattern = regexp.MustCompile(`^[a-z0-9]{8,40}$`)

var errSpool = errors.New("approval system unavailable (spool missing)")

type spool struct{ asks, answers, cancel string }

func openSpool() (spool, error) {
	cfg, err := platform.Load()
	if err != nil {
		return spool{}, fmt.Errorf("approval system unavailable: %w", err)
	}
	base := cfg.NotifySpool
	s := spool{filepath.Join(base, "asks"), filepath.Join(base, "answers"), filepath.Join(base, "cancel")}
	for _, c := range []struct {
		dir  string
		mode uint32
	}{{s.asks, 2}, {s.answers, 4}, {s.cancel, 2}} { // write, read, write
		if fi, err := os.Stat(c.dir); err != nil || !fi.IsDir() || syscall.Access(c.dir, c.mode) != nil {
			return s, errSpool
		}
	}
	return s, nil
}

type askFile struct {
	AskID     string `json:"ask_id"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Detail    string `json:"detail"`
	Cwd       string `json:"cwd"`
	PID       int    `json:"pid"`
	RunID     string `json:"run_id"`
	RunName   string `json:"run_name"`
	CreatedAt string `json:"created_at"`
}

// submit writes an ask to the spool, atomically, with group access (the spool directories are setgid).
func (s spool) submit(kind, title, detail, cwd string) (string, time.Time, error) {
	id := newID()
	raw, _ := json.Marshal(askFile{
		AskID: id, Kind: kind, Title: title, Detail: detail, Cwd: cwd, PID: os.Getpid(),
		RunID: os.Getenv("AGENTIC_RUN_ID"), RunName: os.Getenv("AGENTIC_RUN_NAME"),
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	})
	tmp := filepath.Join(s.asks, ".tmp-"+id)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o660)
	if err != nil {
		return "", time.Time{}, errSpool
	}
	_, werr := f.Write(raw)
	cerr := f.Close()
	if err := errors.Join(werr, cerr, os.Chmod(tmp, 0o660)); err != nil {
		os.Remove(tmp)
		return "", time.Time{}, errSpool
	}
	at := time.Now() // a cancel made before this is not about this ask
	if err := os.Rename(tmp, filepath.Join(s.asks, id+".json")); err != nil {
		os.Remove(tmp)
		return "", time.Time{}, errSpool
	}
	return id, at, nil
}

// wait blocks until the ask is answered or cancelled, or ctx ends (""). Anything but an explicit
// "allow" comes back as "deny" or "cancelled". A cancel file works without notify-vm running.
func (s spool) wait(ctx context.Context, id string, since time.Time) string {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		if raw, err := os.ReadFile(filepath.Join(s.answers, id+".json")); err == nil {
			var a struct {
				Decision string `json:"decision"`
			}
			if json.Unmarshal(raw, &a) != nil {
				return "deny"
			}
			switch a.Decision {
			case "allow", "cancelled":
				return a.Decision
			}
			return "deny"
		}
		for _, name := range []string{id, "all"} {
			if fi, err := os.Stat(filepath.Join(s.cancel, name)); err == nil && !fi.ModTime().Before(since) {
				return "cancelled"
			}
		}
		select {
		case <-ctx.Done():
			return ""
		case <-t.C:
		}
	}
}

// withdrawWait bounds how long withdraw waits for notify-vm to take a cancel.
const withdrawWait = 3 * time.Second

// withdraw gives up on an ask: a cancel file makes notify-vm answer it and
// withdraw the dialog from the Mac; once that is done, or after withdrawWait,
// the ask file is removed.
func (s spool) withdraw(id string) {
	touchCancel(s, id)
	deadline := time.Now().Add(withdrawWait)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(s.answers, id+".json")); err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	os.Remove(filepath.Join(s.asks, id+".json"))
}

// touchCancel creates or refreshes cancel/<name>; a file already there must look new to the waiters.
func touchCancel(s spool, name string) error {
	f, err := os.OpenFile(filepath.Join(s.cancel, name), os.O_WRONLY|os.O_CREATE, 0o660)
	if err != nil {
		return err
	}
	now := time.Now()
	return errors.Join(os.Chmod(f.Name(), 0o660), os.Chtimes(f.Name(), now, now), f.Close())
}

// ask submits, logs, waits and logs the decision. A spool error is returned with decision "deny".
// If ctx ends first the ask is withdrawn and the decision is "timeout" (deadline) or "cancelled".
func (s spool) ask(ctx context.Context, kind, title, detail, cwd, shown string) (decision, id string, err error) {
	id, since, err := s.submit(kind, title, detail, cwd)
	if err != nil {
		logEvent(event{Event: "decision", Cwd: cwd, Command: shown, Decision: "deny", Note: "unavailable: " + err.Error()})
		return "deny", "", err
	}
	logEvent(event{Event: "ask", Cwd: cwd, Command: shown, AskID: id})
	decision = s.wait(ctx, id, since)
	if decision == "" {
		s.withdraw(id)
		decision = "cancelled"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			decision = "timeout"
		}
	}
	logEvent(event{Event: "decision", AskID: id, Decision: decision})
	return decision, id, nil
}

// ---- agentic pending / cancel ----

type pendingAsk struct {
	askFile
	age      time.Duration
	decision string // "" while waiting
}

// listAsks reads every ask in the spool, oldest first.
func (s spool) listAsks() []pendingAsk {
	ents, _ := os.ReadDir(s.asks)
	var out []pendingAsk
	for _, e := range ents {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !idPattern.MatchString(id) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(s.asks, e.Name()))
		fi, err2 := e.Info()
		if err != nil || err2 != nil {
			continue
		}
		p := pendingAsk{age: time.Since(fi.ModTime())}
		if json.Unmarshal(raw, &p.askFile) != nil {
			p.Title = "(unreadable ask)"
		}
		p.AskID = id
		if raw, err := os.ReadFile(filepath.Join(s.answers, id+".json")); err == nil {
			var a struct{ Decision string }
			json.Unmarshal(raw, &a)
			p.decision = a.Decision
			if p.decision == "" {
				p.decision = "deny"
			}
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].age > out[j].age })
	return out
}

func pendingCmd() int {
	s, err := openSpool()
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentic:", err)
		return 1
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tAGE\tRUN\tTITLE\tSTATE")
	for _, p := range s.listAsks() {
		state := "waiting"
		if p.decision != "" {
			state = "answered: " + p.decision
		} else if _, err := os.Stat(filepath.Join(s.cancel, p.AskID)); err == nil {
			state = "waiting (cancel requested)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", p.AskID, p.age.Round(time.Second), p.RunName, oneLine(p.Title, 70), state)
	}
	w.Flush()
	return 0
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func cancelCmd(args []string) int {
	usage := func() int {
		fmt.Fprintln(os.Stderr, "usage: agentic cancel <ask_id> | --all | --run <run_id>")
		return 2
	}
	if len(args) == 0 || len(args) > 2 {
		return usage()
	}
	s, err := openSpool()
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentic:", err)
		return 1
	}
	touch := func(name string) error { return touchCancel(s, name) }
	var ids []string
	switch {
	case args[0] == "--all" && len(args) == 1:
		if err := touch("all"); err != nil {
			fmt.Fprintln(os.Stderr, "agentic:", err)
			return 1
		}
		n := 0
		for _, p := range s.listAsks() {
			if p.decision == "" {
				fmt.Println("cancelled", p.AskID)
				n++
			}
		}
		fmt.Printf("cancelled all pending asks (%d)\n", n)
	case args[0] == "--run" && len(args) == 2:
		for _, p := range s.listAsks() {
			if p.decision == "" && p.RunID == args[1] {
				ids = append(ids, p.AskID)
			}
		}
	case len(args) == 1 && idPattern.MatchString(args[0]):
		ids = []string{args[0]}
	default:
		return usage()
	}
	rc := 0
	for _, id := range ids {
		if err := touch(id); err != nil {
			fmt.Fprintln(os.Stderr, "agentic:", err)
			rc = 1
			continue
		}
		fmt.Println("cancelled", id)
	}
	if len(ids) == 0 && args[0] != "--all" {
		fmt.Println("nothing to cancel")
	}
	return rc
}

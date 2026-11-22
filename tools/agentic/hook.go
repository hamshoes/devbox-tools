package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

// hookInput is the part of Claude Code's hook JSON that these handlers read.
type hookInput struct {
	HookEventName string                   `json:"hook_event_name"`
	ToolName      string                   `json:"tool_name"`
	ToolUseID     string                   `json:"tool_use_id"`
	Cwd           string                   `json:"cwd"`
	ToolInput     struct{ Command string } `json:"tool_input"`
	ToolResponse  json.RawMessage          `json:"tool_response"` // an object on success, error text on failure
	Error         string                   `json:"error"`
	IsInterrupt   bool                     `json:"is_interrupt"`
}

func readHook() (hookInput, error) {
	var in hookInput
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
	if err != nil {
		return in, err
	}
	return in, json.Unmarshal(raw, &in)
}

// ---- PreToolUse ----

// unavailable is the reason given when no approval can be asked for.
const unavailable = "approval system unavailable. Do not retry the push; commit locally and report back."

func hookPre() int {
	syscall.Umask(0o007) // anything written to the spool is group-readable and not world-readable
	in, err := readHook()
	if err != nil {
		// Claude Code's blocking-error path: exit 2 blocks the tool call and shows stderr to the agent.
		fmt.Fprintf(os.Stderr, "agentic: blocked: the hook could not read Claude Code's input (%v), so it can't tell whether this runs git push.\n", err)
		return 2
	}
	cmd := in.ToolInput.Command
	if (in.ToolName != "" && in.ToolName != "Bash") || cmd == "" {
		return 0
	}
	cwd := in.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	logEvent(event{Event: "cmd", Cwd: cwd, Command: cmd, ToolUseID: in.ToolUseID})

	pushes := findPushes(cmd, 0)
	if len(pushes) == 0 {
		return 0
	}
	// Decide before Claude Code's own timeout, which would let the call through.
	ctx, cancel := context.WithTimeout(context.Background(), preHookTimeout-preHookMargin)
	defer cancel()
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	title, detail, dir := buildPushAsk(cmd, cwd, pushes[0])
	if n := len(pushes); n > 1 {
		detail += fmt.Sprintf("\n(%d pushes in this command; the first is described)", n)
	}
	s, err := openSpool()
	if err != nil {
		logEvent(event{Event: "decision", Cwd: dir, Command: cmd, Decision: "deny", Note: "unavailable: " + err.Error(), ToolUseID: in.ToolUseID})
		return decide("deny", unavailable)
	}
	if out, ok := askedDeny(s.ask(ctx, "git-push", title, detail, dir, cmd)); !ok {
		return out
	}

	// C5: a second dialog when the pushed commits touch env-like files, or when that can't be told.
	files, ferr := pushedEnvFiles(dir, pushes[0])
	switch {
	case ferr != nil:
		detail = "Could not list the files these commits change (" + ferr.Error() + "), so they may include env files.\n\n" + capRunes(cmd, 300)
	case len(files) > 0:
		shown := files
		if len(shown) > 40 {
			shown = append(shown[:40:40], fmt.Sprintf("… and %d more", len(files)-40))
		}
		detail = "The pushed commits add or change these env-like files:\n  " + strings.Join(shown, "\n  ")
	default:
		return decide("allow", "git push approved on the Mac")
	}
	if out, ok := askedDeny(s.ask(ctx, "git-push-env", "git push includes env files: "+filepath.Base(dir), detail, dir, cmd)); !ok {
		return out
	}
	return decide("allow", "git push and its env files approved on the Mac")
}

// askedDeny turns an ask's outcome into the hook's answer when it is not an
// allow; ok is true only for an explicit allow.
func askedDeny(decision, _ string, err error) (int, bool) {
	switch {
	case err != nil:
		return decide("deny", unavailable), false
	case decision == "allow":
		return 0, true
	case decision == "timeout":
		return decide("deny", "approval timed out. Do not retry the push; commit locally and report back."), false
	case decision == "cancelled":
		return decide("deny", "git push approval was cancelled. Do not retry; report back."), false
	}
	return decide("deny", "git push denied on the Mac. Do not retry the push; commit locally and report back."), false
}

// decide prints a PreToolUse decision for Claude Code.
func decide(permission, reason string) int {
	out := map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName":            "PreToolUse",
		"permissionDecision":       permission,
		"permissionDecisionReason": reason,
	}}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		return 1
	}
	return 0
}

// ---- PostToolUse / PostToolUseFailure ----

var exitCode = regexp.MustCompile(`(?i)exit code:? (\d+)`)

func hookPost() int {
	in, err := readHook()
	if err != nil {
		return 0
	}
	if (in.ToolName != "" && in.ToolName != "Bash") || in.ToolInput.Command == "" {
		return 0
	}
	e := event{Event: "result", Cwd: in.Cwd, ToolUseID: in.ToolUseID, Interrupted: in.IsInterrupt}
	var resp struct {
		Interrupted bool `json:"interrupted"`
	}
	if json.Unmarshal(in.ToolResponse, &resp) == nil {
		e.Interrupted = e.Interrupted || resp.Interrupted
	}
	// The docs give no exit code field; a failed call carries error text that starts "Exit code N".
	text := in.Error
	if text == "" {
		json.Unmarshal(in.ToolResponse, &text)
	}
	e.Failed = in.HookEventName == "PostToolUseFailure" || text != ""
	if m := exitCode.FindStringSubmatch(text); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			e.Exit = &n
		}
	}
	logEvent(e)
	return 0
}

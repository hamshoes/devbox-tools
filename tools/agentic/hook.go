package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
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

func hookPre() int {
	syscall.Umask(0o007) // anything written to the spool is group-readable and not world-readable
	in, err := readHook()
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentic: hook input:", err)
		return 0 // no decision: Claude Code's normal permissions apply
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
	title, detail, dir := buildPushAsk(cmd, cwd, pushes[0])
	if n := len(pushes); n > 1 {
		detail += fmt.Sprintf("\n(%d pushes in this command; the first is described)", n)
	}
	s, err := openSpool()
	if err != nil {
		logEvent(event{Event: "decision", Cwd: dir, Command: cmd, Decision: "deny", Note: "unavailable: " + err.Error(), ToolUseID: in.ToolUseID})
		return decide("deny", "approval system unavailable (spool missing). Do not retry the push; commit locally and report back.")
	}
	decision, _, err := s.ask("git-push", title, detail, dir, cmd)
	switch {
	case err != nil:
		return decide("deny", "approval system unavailable (spool missing). Do not retry the push; commit locally and report back.")
	case decision == "allow":
		return decide("allow", "git push approved on Mac")
	case decision == "cancelled":
		return decide("deny", "git push approval was cancelled in the VM. Do not retry; report back.")
	}
	return decide("deny", "git push denied by Sam. Do not retry the push; commit locally and report back.")
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

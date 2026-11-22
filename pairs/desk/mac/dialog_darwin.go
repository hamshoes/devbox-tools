//go:build darwin

package main

import (
	"context"
	"os/exec"
	"strings"
)

func checkFlags() error { return nil }

// decide shows the dialog and blocks until it is answered. Only an explicit Allow is true: Deny, an
// error, a killed process or a cancelled ctx are all false. The text travels as argv after "--", so
// it can't become part of the script or be read as an osascript option.
func decide(ctx context.Context, title, body string) bool {
	out, err := exec.CommandContext(ctx, "osascript",
		"-e", "on run argv",
		"-e", `display dialog (item 1 of argv) with title (item 2 of argv) buttons {"Deny","Allow"} default button "Deny" cancel button "Deny" with icon caution`,
		"-e", "end run",
		"--", body, title,
	).Output()
	return err == nil && strings.TrimSpace(string(out)) == "button returned:Allow"
}

// Package tmux renames the current window and moves idle zsh panes to a directory.
package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Follow renames the current window after dir and sends `cd` to every other
// pane in the window that is sitting at a zsh prompt. It does nothing outside tmux.
func Follow(dir string) {
	self := os.Getenv("TMUX_PANE")
	if os.Getenv("TMUX") == "" || self == "" {
		return
	}
	if err := exec.Command("tmux", "rename-window", "-t", self, filepath.Base(dir)).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "proj: tmux rename-window:", err)
	}
	out, err := exec.Command("tmux", "list-panes", "-t", self, "-F", "#{pane_id} #{pane_current_command}").Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "proj: tmux list-panes:", err)
		return
	}
	keys := fmt.Sprintf("cd -- %s && clear", quote(dir))
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		id, cmd, _ := strings.Cut(line, " ")
		if id == "" || id == self || cmd != "zsh" {
			continue
		}
		if err := exec.Command("tmux", "send-keys", "-t", id, keys, "Enter").Run(); err != nil {
			fmt.Fprintln(os.Stderr, "proj: tmux send-keys:", err)
		}
	}
}

func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

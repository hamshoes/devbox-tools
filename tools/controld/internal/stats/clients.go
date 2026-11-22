package stats

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Client is one attached tmux client.
type Client struct {
	TTY           string    `json:"tty"`
	Session       string    `json:"session"`
	AttachedSince time.Time `json:"attached_since"`
	LastActivity  time.Time `json:"last_activity"`
}

const clientsFormat = "#{client_tty}\t#{session_name}\t#{client_created}\t#{client_activity}"

// listClients returns the attached tmux clients; no tmux server means none.
func listClients(ctx context.Context) ([]Client, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tmux", "list-clients", "-F", clientsFormat).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			msg := string(ee.Stderr)
			if strings.Contains(msg, "no server running") || strings.Contains(msg, "error connecting to") {
				return []Client{}, nil
			}
		}
		return nil, err
	}
	clients := []Client{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 4 {
			continue
		}
		created, _ := strconv.ParseInt(f[2], 10, 64)
		activity, _ := strconv.ParseInt(f[3], 10, 64)
		clients = append(clients, Client{f[0], f[1], time.Unix(created, 0), time.Unix(activity, 0)})
	}
	return clients, nil
}

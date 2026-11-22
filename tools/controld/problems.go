package main

import (
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	controlv1 "github.com/hamshoes/devbox-tools/pairs/control/gen/control/v1"
)

const maxProblems = 20

// problems keeps the most recent failures controld saw, for `bb status` and
// the status page, and hands each to report. Adding one never blocks.
type problems struct {
	log    *slog.Logger
	report func(*controlv1.Problem) // must not block; nil drops reports

	mu   sync.Mutex
	list []*controlv1.Problem
}

// add records a failure from source (e.g. "service/x", "pair/notify").
func (p *problems) add(source, format string, args ...any) {
	pr := &controlv1.Problem{At: time.Now().UTC().Format(time.RFC3339), Source: source, Message: fmt.Sprintf(format, args...)}
	p.log.Warn("problem", "source", pr.Source, "msg", pr.Message)
	p.mu.Lock()
	p.list = append(p.list, pr)
	if n := len(p.list); n > maxProblems {
		p.list = slices.Delete(p.list, 0, n-maxProblems)
	}
	p.mu.Unlock()
	if p.report != nil {
		p.report(pr)
	}
}

// recent returns the recorded problems, oldest first.
func (p *problems) recent() []*controlv1.Problem {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.list)
}

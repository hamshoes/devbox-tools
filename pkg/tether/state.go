package tether

import (
	"slices"
	"sync"
)

type State int

const (
	Connecting State = iota
	Open
	Reconnecting
	Closed
)

func (s State) String() string {
	switch s {
	case Connecting:
		return "connecting"
	case Open:
		return "open"
	case Reconnecting:
		return "reconnecting"
	case Closed:
		return "closed"
	}
	return "unknown"
}

type stateEvent struct {
	state   State
	targets []func(State)
}

// bus holds the current state and delivers changes to subscribers, in order,
// from one goroutine. Closed is terminal.
type bus struct {
	mu     sync.Mutex
	cur    State
	subs   []func(State)
	queue  []stateEvent
	exited bool
	wake   chan struct{}
	done   chan struct{}
}

func newBus() *bus {
	b := &bus{wake: make(chan struct{}, 1), done: make(chan struct{})}
	go b.run()
	return b
}

func (b *bus) get() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cur
}

func (b *bus) set(s State) {
	b.mu.Lock()
	if b.cur == Closed || b.cur == s {
		b.mu.Unlock()
		return
	}
	b.cur = s
	b.queue = append(b.queue, stateEvent{s, slices.Clone(b.subs)})
	b.mu.Unlock()
	b.signal()
}

// subscribe registers fn and replays the current state to it first, so a
// subscriber added right after Listen or Dial cannot miss an early change.
func (b *bus) subscribe(fn func(State)) {
	b.mu.Lock()
	b.subs = append(b.subs, fn)
	if b.exited {
		cur := b.cur
		b.mu.Unlock()
		fn(cur)
		return
	}
	b.queue = append(b.queue, stateEvent{b.cur, []func(State){fn}})
	b.mu.Unlock()
	b.signal()
}

func (b *bus) signal() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

func (b *bus) run() {
	defer close(b.done)
	for {
		<-b.wake
		for {
			b.mu.Lock()
			if len(b.queue) == 0 {
				if b.cur == Closed {
					b.exited = true
					b.mu.Unlock()
					return
				}
				b.mu.Unlock()
				break
			}
			ev := b.queue[0]
			b.queue = b.queue[1:]
			b.mu.Unlock()
			for _, fn := range ev.targets {
				fn(ev.state)
			}
		}
	}
}

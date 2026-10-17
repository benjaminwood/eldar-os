package server

import (
	"sort"
	"sync"

	"github.com/benjaminwood/eldar-os/supervisor/api"
)

// Approvals are prompts waiting on Ben, answered from the admin API (TUI, web
// UI, phone). Reachable only through the admin socket, never the agent's port.
type Approvals struct {
	mu      sync.Mutex
	entries map[int]*approvalEntry
	n       *Notifier
}

type approvalEntry struct {
	info api.Pending
	ch   chan api.Decision
}

func NewApprovals(n *Notifier) *Approvals {
	return &Approvals{entries: map[int]*approvalEntry{}, n: n}
}

// Add registers a prompt; the channel receives Ben's decision.
func (a *Approvals) Add(info api.Pending) <-chan api.Decision {
	ch := make(chan api.Decision, 1)
	a.mu.Lock()
	a.entries[info.ID] = &approvalEntry{info: info, ch: ch}
	a.mu.Unlock()
	a.n.Changed()
	return ch
}

// Resolve answers a prompt. False if it was already answered or withdrawn.
func (a *Approvals) Resolve(id int, d api.Decision) bool {
	a.mu.Lock()
	e := a.entries[id]
	delete(a.entries, id)
	a.mu.Unlock()
	if e == nil {
		return false
	}
	e.ch <- d
	a.n.Changed()
	return true
}

// Remove withdraws a prompt without answering it (timed out).
func (a *Approvals) Remove(id int) {
	a.mu.Lock()
	_, ok := a.entries[id]
	delete(a.entries, id)
	a.mu.Unlock()
	if ok {
		a.n.Changed()
	}
}

func (a *Approvals) Get(id int) (api.Pending, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e := a.entries[id]
	if e == nil {
		return api.Pending{}, false
	}
	return e.info, true
}

// List returns waiting prompts, oldest first.
func (a *Approvals) List() []api.Pending {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []api.Pending{}
	for _, e := range a.entries {
		out = append(out, e.info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out
}

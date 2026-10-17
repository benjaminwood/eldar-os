// Package server is the supervisor: it holds the credentials, decides what an
// agent may run (policy, grants, Ben's approvals), runs it in the agent's
// environment, and serves the admin API the TUI and web UI use.
package server

import "sync"

// Notifier signals "something the admin UIs show has changed". Subscribers get
// a coalesced wake-up, so a burst of changes costs one snapshot.
type Notifier struct {
	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

func NewNotifier() *Notifier { return &Notifier{subs: map[chan struct{}]struct{}{}} }

// Changed wakes every subscriber that isn't already pending.
func (n *Notifier) Changed() {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for c := range n.subs {
		select {
		case c <- struct{}{}:
		default:
		}
	}
}

// Subscribe returns a channel that receives a value after changes, and a
// cancel func.
func (n *Notifier) Subscribe() (<-chan struct{}, func()) {
	c := make(chan struct{}, 1)
	n.mu.Lock()
	n.subs[c] = struct{}{}
	n.mu.Unlock()
	return c, func() {
		n.mu.Lock()
		delete(n.subs, c)
		n.mu.Unlock()
	}
}

// Subscribers is how many are listening (the admin event streams).
func (n *Notifier) Subscribers() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.subs)
}

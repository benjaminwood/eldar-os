package server

import (
	"sync"
	"time"

	"github.com/benjaminwood/eldar-os/supervisor/api"
)

const maxBypass = 2 * time.Hour

// Modes is the global mode: normal, bypass (everything not denied runs,
// optionally for a while) or lockdown (nothing runs).
type Modes struct {
	mu        sync.Mutex
	mode      api.Mode
	expiresAt *time.Time
	n         *Notifier
}

func NewModes(n *Notifier) *Modes { return &Modes{mode: api.ModeNormal, n: n} }

func (m *Modes) Current() api.Mode {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.expiresAt != nil && !time.Now().Before(*m.expiresAt) {
		m.mode, m.expiresAt = api.ModeNormal, nil
		m.n.Changed()
	}
	return m.mode
}

// Bypass for d (capped at two hours), or until changed when d is nil.
func (m *Modes) Bypass(d *time.Duration) {
	m.mu.Lock()
	m.mode, m.expiresAt = api.ModeBypass, nil
	if d != nil {
		if *d > maxBypass {
			*d = maxBypass
		}
		t := time.Now().Add(*d)
		m.expiresAt = &t
	}
	m.mu.Unlock()
	m.n.Changed()
}

func (m *Modes) Lockdown() { m.set(api.ModeLockdown) }
func (m *Modes) Revert()   { m.set(api.ModeNormal) }

func (m *Modes) set(mode api.Mode) {
	m.mu.Lock()
	m.mode, m.expiresAt = mode, nil
	m.mu.Unlock()
	m.n.Changed()
}

// Status is the mode and, for a timed bypass, when it ends (ms since epoch).
func (m *Modes) Status() (api.Mode, *int64) {
	mode := m.Current()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.expiresAt == nil {
		return mode, nil
	}
	ms := m.expiresAt.UnixMilli()
	return mode, &ms
}

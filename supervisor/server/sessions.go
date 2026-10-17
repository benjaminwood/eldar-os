package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/benjaminwood/eldar-os/supervisor/api"
)

// Sessions tracks the Claude sessions that call the supervisor.
//
// A request carries sessionId (CLAUDE_CODE_SESSION_ID) and sessionKey, a random
// value the session's client generates at startup and keeps only in memory. The
// ID alone proves nothing (every session's ID is visible to the agent), so the
// first key seen for an ID is pinned, and only requests presenting it are
// "verified". Session-specific grants apply only to verified requests, so one
// session can't borrow another's grant by sending its ID. Pins are in memory:
// a restart re-pins on each session's next request.
type Sessions struct {
	mu      sync.Mutex
	entries map[string]*sessionEntry
	n       *Notifier
}

type sessionEntry struct {
	info    api.Session
	keyHash string
}

func NewSessions(n *Notifier) *Sessions {
	return &Sessions{entries: map[string]*sessionEntry{}, n: n}
}

// TouchMeta is what a request tells us about its session.
type TouchMeta struct {
	Cwd, HerdrPane, Action string
}

func hashKey(k string) string {
	s := sha256.Sum256([]byte(k))
	return hex.EncodeToString(s[:])
}

// Touch records a request and reports whether it's verified as the pinned
// session.
func (s *Sessions) Touch(id, key string, meta TouchMeta) bool {
	if id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.n.Changed()
	now := time.Now().UnixMilli()
	e := s.entries[id]
	if e == nil {
		e = &sessionEntry{info: api.Session{ID: id, FirstSeen: now, LastSeen: now}}
		s.entries[id] = e
	}
	if key != "" && e.keyHash == "" {
		e.keyHash = hashKey(key)
		e.info.Pinned = true
	}
	verified := key != "" && e.keyHash != "" &&
		subtle.ConstantTimeCompare([]byte(hashKey(key)), []byte(e.keyHash)) == 1
	if key != "" && !verified {
		// Someone presented this ID with the wrong key. Count it, and don't
		// let the impostor's metadata overwrite the real session's.
		e.info.KeyMismatches++
		return false
	}
	e.info.LastSeen = now
	e.info.Requests++
	if meta.Cwd != "" {
		e.info.Cwd = meta.Cwd
	}
	if meta.HerdrPane != "" {
		e.info.HerdrPane = meta.HerdrPane
	}
	if meta.Action != "" {
		line := strings.SplitN(meta.Action, "\n", 2)[0]
		if r := []rune(line); len(r) > 160 {
			line = string(r[:160])
		}
		e.info.LastAction = line
	}
	return verified
}

// Unpin forgets a session's pinned key so its next request re-pins: for when
// its client restarted (new key, same ID) and Ben has confirmed that's what the
// mismatches were.
func (s *Sessions) Unpin(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[id]
	if e == nil {
		return false
	}
	e.keyHash, e.info.Pinned, e.info.KeyMismatches = "", false, 0
	s.n.Changed()
	return true
}

// List returns sessions, most recent first, forgetting any idle for a day.
func (s *Sessions) List() []api.Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-24 * time.Hour).UnixMilli()
	out := []api.Session{}
	for id, e := range s.entries {
		if e.info.LastSeen < cutoff {
			delete(s.entries, id)
			continue
		}
		out = append(out, e.info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen > out[j].LastSeen })
	return out
}

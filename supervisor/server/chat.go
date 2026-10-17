package server

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/benjaminwood/eldar-os/supervisor/api"
)

// Ben's chat with Eldar, the chief-of-staff Claude session. Ben writes from the
// web UI (admin API); Eldar reads and replies through the `chat` channel MCP
// server in its session (agent API). The supervisor is the only path between
// them: a message from "ben" can only come through the admin socket, and only
// the chief-of-staff session (the Claude pane in herdr workspace ChatWorkspace,
// tab ChatTab) presenting its pinned chat key can read Ben's messages or reply.
//
// Files (supervisor state, invisible to the agent): chat.jsonl (one message
// per line, append-only) and chat-state.json ({readUpTo, deliveredUpTo}).

const (
	chatMaxText = 20_000
	chatKeep    = 5000
)

type chatState struct {
	ReadUpTo      int `json:"readUpTo"`      // Eldar's messages Ben has seen
	DeliveredUpTo int `json:"deliveredUpTo"` // Ben's messages handed to Eldar's channel
}

type Chat struct {
	path, statePath string
	n               *Notifier

	mu       sync.Mutex
	messages []api.ChatMessage
	state    chatState
	nextID   int
	fromBen  map[chan api.ChatMessage]struct{}
	stream   *chatStream // Eldar's live channel, at most one
}

type chatStream struct {
	sessionID string
	cancel    context.CancelFunc
}

func NewChat(path, statePath string, n *Notifier) *Chat {
	c := &Chat{path: path, statePath: statePath, n: n, nextID: 1, fromBen: map[chan api.ChatMessage]struct{}{}}
	if f, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			var m api.ChatMessage
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				c.messages = append(c.messages, m)
			}
		}
		f.Close()
		if len(c.messages) > chatKeep {
			c.messages = c.messages[len(c.messages)-chatKeep:]
		}
		if len(c.messages) > 0 {
			c.nextID = c.messages[len(c.messages)-1].ID + 1
		}
	}
	if b, err := os.ReadFile(statePath); err == nil {
		json.Unmarshal(b, &c.state)
	}
	return c
}

// chatLine is what's stored: the API message plus the session that wrote it.
type chatLine struct {
	api.ChatMessage
	SessionID string `json:"sessionId,omitempty"`
}

func (c *Chat) Add(from, text, sessionID string) (api.ChatMessage, error) {
	body := strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if body == "" {
		return api.ChatMessage{}, errors.New("empty message")
	}
	if len([]rune(body)) > chatMaxText {
		return api.ChatMessage{}, fmt.Errorf("message too long (max %d characters)", chatMaxText)
	}
	c.mu.Lock()
	m := api.ChatMessage{ID: c.nextID, From: from, Text: body, TS: time.Now().UnixMilli()}
	c.nextID++
	c.messages = append(c.messages, m)
	if len(c.messages) > chatKeep {
		c.messages = c.messages[1:]
	}
	if b, err := json.Marshal(chatLine{m, sessionID}); err == nil {
		if f, err := os.OpenFile(c.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600); err == nil {
			f.Write(append(b, '\n'))
			f.Close()
		} else {
			logf("chat: append failed: %v", err)
		}
	}
	if from == "ben" {
		for ch := range c.fromBen {
			select {
			case ch <- m:
			default:
			}
		}
	}
	c.mu.Unlock()
	c.n.Changed()
	return m, nil
}

// List: messages after `after`, oldest first, the newest `limit` of them.
func (c *Chat) List(after, limit int) []api.ChatMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []api.ChatMessage{}
	for _, m := range c.messages {
		if m.ID > after {
			out = append(out, m)
		}
	}
	return tail(out, limit)
}

// Before: the last `limit` messages before `before`, oldest first.
func (c *Chat) Before(before, limit int) []api.ChatMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []api.ChatMessage{}
	for _, m := range c.messages {
		if m.ID < before {
			out = append(out, m)
		}
	}
	return tail(out, limit)
}

func tail(s []api.ChatMessage, n int) []api.ChatMessage {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func (c *Chat) saveState() {
	b, _ := json.Marshal(c.state)
	tmp := c.statePath + ".tmp"
	if os.WriteFile(tmp, append(b, '\n'), 0o600) == nil {
		os.Rename(tmp, c.statePath)
	}
}

func (c *Chat) markDelivered(id int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if id > c.state.DeliveredUpTo {
		c.state.DeliveredUpTo = id
		c.saveState()
	}
}

func (c *Chat) MarkRead(upTo int) {
	c.mu.Lock()
	last := 0
	if len(c.messages) > 0 {
		last = c.messages[len(c.messages)-1].ID
	}
	n := min(max(upTo, 0), last)
	changed := n > c.state.ReadUpTo
	if changed {
		c.state.ReadUpTo = n
		c.saveState()
	}
	c.mu.Unlock()
	if changed {
		c.n.Changed()
	}
}

func (c *Chat) ReadUpTo() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state.ReadUpTo
}

// Summary for the snapshot (the last message cut to 300 characters).
func (c *Chat) Summary() api.ChatSummary {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := api.ChatSummary{ReadUpTo: c.state.ReadUpTo, Connected: c.stream != nil}
	for _, m := range c.messages {
		if m.From == "eldar" && m.ID > c.state.ReadUpTo {
			s.Unread++
		}
	}
	if n := len(c.messages); n > 0 {
		last := c.messages[n-1]
		s.LastID = last.ID
		last.Text = truncRunes(last.Text, 300)
		s.Last = &last
	}
	return s
}

// KeyPins: trust-on-first-use key per session for MCP servers other than the
// supervisor client (the chat channel): each holds its own random key. Memory
// only; a restart re-pins.
type KeyPins struct {
	mu   sync.Mutex
	pins map[string]string
}

// Repin replaces a session's pin: a channel connecting its stream while no
// other connection holds it (the previous process ended, e.g. Eldar resumed).
func (k *KeyPins) Repin(sessionID, key string) {
	h := sha256.Sum256([]byte(key))
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.pins == nil {
		k.pins = map[string]string{}
	}
	k.pins[sessionID] = hex.EncodeToString(h[:])
}

func (k *KeyPins) Check(sessionID, key string) bool {
	h := sha256.Sum256([]byte(key))
	hx := hex.EncodeToString(h[:])
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.pins == nil {
		k.pins = map[string]string{}
	}
	if p, ok := k.pins[sessionID]; ok {
		return p == hx
	}
	k.pins[sessionID] = hx
	return true
}

// chiefOfStaff is Eldar: the Claude pane in workspace ChatWorkspace, tab ChatTab.
func (s *Supervisor) chiefOfStaff(ctx context.Context, maxAge time.Duration) *api.ChatEldar {
	for _, sp := range s.herdr.View(ctx, "", maxAge).Spaces {
		if sp.Label != s.cfg.ChatWorkspace {
			continue
		}
		for _, t := range sp.Tabs {
			if t.Label != s.cfg.ChatTab {
				continue
			}
			for _, p := range t.Panes {
				if p.Agent != nil && p.Agent.Kind == "claude" {
					return &api.ChatEldar{SessionID: p.Agent.SessionID, PaneID: p.PaneID, Status: p.Agent.Status}
				}
			}
		}
	}
	return nil
}

// chatAuth: only the chief-of-staff session, with its pinned chat key.
func (s *Supervisor) chatAuth(r *http.Request) (string, int, string) {
	return s.chiefOfStaffAuth(r, &s.chatPins, "chat", false)
}

// chiefOfStaffAuth: only the chief-of-staff session, presenting the key its
// `what` channel pinned (each channel process has its own).
// With repin (a stream connecting while none is open), the caller's key
// replaces the pin: a new channel process in the same session, after the old
// one ended. While a stream is open, only its key is accepted.
func (s *Supervisor) chiefOfStaffAuth(r *http.Request, pins *KeyPins, what string, repin bool) (string, int, string) {
	sessionID, key := r.Header.Get("X-Session-Id"), r.Header.Get("X-Chat-Key")
	if sessionID == "" || key == "" {
		return "", 401, "need X-Session-Id and X-Chat-Key"
	}
	cos := s.chiefOfStaff(r.Context(), 3*time.Second)
	if cos == nil || cos.SessionID != sessionID {
		cos = s.chiefOfStaff(r.Context(), 0)
	}
	if cos == nil {
		return "", 409, fmt.Sprintf("no Claude session in herdr workspace %q, tab %q", s.cfg.ChatWorkspace, s.cfg.ChatTab)
	}
	if cos.SessionID != sessionID {
		return "", 403, "only the chief-of-staff session (Eldar) can use the " + what
	}
	if repin {
		pins.Repin(sessionID, key)
	} else if !pins.Check(sessionID, key) {
		logf("%s: key mismatch for session %s", what, sessionID)
		return "", 403, what + " key mismatch for this session"
	}
	return sessionID, 0, ""
}

func (s *Supervisor) chatRoutes(mux *http.ServeMux) {
	auth := func(h func(w http.ResponseWriter, r *http.Request, session string)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			session, status, msg := s.chatAuth(r)
			if status != 0 {
				sendJSON(w, status, map[string]any{"error": msg})
				return
			}
			h(w, r, session)
		}
	}
	// Ben's undelivered messages, then each new one, as server-sent events.
	mux.HandleFunc("GET /chat/stream", func(w http.ResponseWriter, r *http.Request) {
		s.chat.mu.Lock()
		idle := s.chat.stream == nil
		s.chat.mu.Unlock()
		session, status, msg := s.chiefOfStaffAuth(r, &s.chatPins, "chat", idle)
		if status != 0 {
			sendJSON(w, status, map[string]any{"error": msg})
			return
		}
		fl, ok := w.(http.Flusher)
		if !ok {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		ch := make(chan api.ChatMessage, 64)
		c := s.chat
		c.mu.Lock()
		if c.stream != nil {
			c.stream.cancel() // a newer connection replaces the old one
		}
		me := &chatStream{sessionID: session, cancel: cancel}
		c.stream = me
		c.fromBen[ch] = struct{}{}
		var pending []api.ChatMessage
		for _, m := range c.messages {
			if m.From == "ben" && m.ID > c.state.DeliveredUpTo {
				pending = append(pending, m)
			}
		}
		c.mu.Unlock()
		s.n.Changed()
		defer func() {
			c.mu.Lock()
			delete(c.fromBen, ch)
			if c.stream == me {
				c.stream = nil
			}
			c.mu.Unlock()
			s.n.Changed()
		}()
		send := func(m api.ChatMessage) bool {
			b, _ := json.Marshal(map[string]any{"id": m.ID, "text": m.Text, "ts": m.TS})
			if _, err := fmt.Fprintf(w, "event: message\ndata: %s\n\n", b); err != nil {
				return false
			}
			fl.Flush()
			c.markDelivered(m.ID)
			return true
		}
		for _, m := range pending {
			if !send(m) {
				return
			}
		}
		fl.Flush()
		beat := time.NewTicker(20 * time.Second)
		defer beat.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case m := <-ch:
				if !send(m) {
					return
				}
			case <-beat.C:
				if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
					return
				}
				fl.Flush()
			}
		}
	})
	mux.HandleFunc("POST /chat/reply", auth(func(w http.ResponseWriter, r *http.Request, session string) {
		var b struct {
			Text string `json:"text"`
		}
		readJSON(r, &b)
		m, err := s.chat.Add("eldar", b.Text, session)
		if err != nil {
			sendJSON(w, 400, map[string]any{"error": err.Error()})
			return
		}
		sendJSON(w, 200, map[string]any{"id": m.ID})
	}))
	mux.HandleFunc("GET /chat/history", auth(func(w http.ResponseWriter, r *http.Request, _ string) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 {
			limit = 30
		}
		sendJSON(w, 200, map[string]any{"messages": s.chat.List(0, min(limit, 200))})
	}))
}

// chatStatus: whether Eldar's channel is connected, and Eldar's pane.
func (s *Supervisor) chatStatus(ctx context.Context) (bool, *api.ChatEldar) {
	s.chat.mu.Lock()
	connected := s.chat.stream != nil
	s.chat.mu.Unlock()
	return connected, s.chiefOfStaff(ctx, 3*time.Second)
}

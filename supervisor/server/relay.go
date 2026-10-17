package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// The relay: other agents (notify_ben), scripts and cron reach Ben through
// Eldar, the chief-of-staff session. Anything in Eldar's VM may POST /relay;
// the supervisor keeps each event until Eldar's `relay` channel takes it from
// GET /relay/stream (only Eldar's session, with its pinned key, like the chat),
// so events sent while Eldar restarts are delivered when it's back. Eldar
// treats them as data: it tells Ben, and acts on his answer.
//
// Files (supervisor state): relay.jsonl (append-only) and relay-state.json
// ({deliveredUpTo}).

const (
	relayMaxText = 20_000
	relayKeep    = 1000
)

type RelayEvent struct {
	ID   int               `json:"id"`
	TS   int64             `json:"ts"`
	Text string            `json:"text"`
	Meta map[string]string `json:"meta"`
}

type Relay struct {
	path, statePath string

	mu        sync.Mutex
	events    []RelayEvent
	delivered int
	nextID    int
	subs      map[chan RelayEvent]struct{}
	stream    *chatStream
}

func NewRelay(path, statePath string) *Relay {
	r := &Relay{path: path, statePath: statePath, nextID: 1, subs: map[chan RelayEvent]struct{}{}}
	if f, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			var e RelayEvent
			if json.Unmarshal(sc.Bytes(), &e) == nil {
				r.events = append(r.events, e)
			}
		}
		f.Close()
		if len(r.events) > relayKeep {
			r.events = r.events[len(r.events)-relayKeep:]
		}
		if len(r.events) > 0 {
			r.nextID = r.events[len(r.events)-1].ID + 1
		}
	}
	var st struct {
		DeliveredUpTo int `json:"deliveredUpTo"`
	}
	if b, err := os.ReadFile(statePath); err == nil && json.Unmarshal(b, &st) == nil {
		r.delivered = st.DeliveredUpTo
	}
	return r
}

// relayMeta sanitises a value for the <channel> tag: one safe line.
func relayMeta(v string) string {
	v = strings.Map(func(c rune) rune {
		if strings.ContainsRune("<>\"\r\n", c) {
			return '_'
		}
		return c
	}, v)
	return truncRunes(v, 200)
}

func (r *Relay) Add(text string, meta map[string]string) (RelayEvent, error) {
	body := strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if body == "" {
		return RelayEvent{}, errors.New("empty message")
	}
	if len([]rune(body)) > relayMaxText {
		return RelayEvent{}, fmt.Errorf("message too long (max %d characters)", relayMaxText)
	}
	clean := map[string]string{}
	for k, v := range meta {
		if v = relayMeta(v); v != "" {
			clean[k] = v
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e := RelayEvent{ID: r.nextID, TS: time.Now().UnixMilli(), Text: body, Meta: clean}
	r.nextID++
	r.events = append(r.events, e)
	if len(r.events) > relayKeep {
		r.events = r.events[1:]
	}
	if b, err := json.Marshal(e); err == nil {
		if f, err := os.OpenFile(r.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600); err == nil {
			f.Write(append(b, '\n'))
			f.Close()
		} else {
			logf("relay: append failed: %v", err)
		}
	}
	for ch := range r.subs {
		select {
		case ch <- e:
		default:
		}
	}
	return e, nil
}

func (r *Relay) markDelivered(id int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id <= r.delivered {
		return
	}
	r.delivered = id
	b, _ := json.Marshal(map[string]int{"deliveredUpTo": id})
	if err := os.WriteFile(r.statePath, b, 0o600); err != nil {
		logf("relay: state: %v", err)
	}
}

func (s *Supervisor) relayRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /relay", func(w http.ResponseWriter, req *http.Request) {
		var b struct {
			Text         string `json:"text"`
			Origin       string `json:"origin"`
			SessionID    string `json:"sessionId"`
			PaneID       string `json:"paneId"`
			SessionName  string `json:"sessionName"`
			Cwd          string `json:"cwd"`
			ExpectsReply bool   `json:"expectsReply"`
		}
		if err := readJSON(req, &b); err != nil {
			sendJSON(w, 400, map[string]any{"error": "invalid JSON: " + err.Error()})
			return
		}
		meta := map[string]string{"origin": b.Origin, "session_id": b.SessionID, "pane_id": b.PaneID,
			"session_name": b.SessionName, "cwd": b.Cwd}
		if b.ExpectsReply {
			meta["expects_reply"] = "true"
		}
		e, err := s.relay.Add(b.Text, meta)
		if err != nil {
			sendJSON(w, 400, map[string]any{"error": err.Error()})
			return
		}
		s.audit.Log(AuditEntry{RequestID: s.nextID(), Action: "relay to Eldar: " + truncRunes(e.Text, 200),
			Decision: "allow", DecisionReason: "from " + e.Meta["origin"], SessionID: b.SessionID, Cwd: b.Cwd, Mode: "normal"})
		s.relay.mu.Lock()
		connected := s.relay.stream != nil
		s.relay.mu.Unlock()
		sendJSON(w, 200, map[string]any{"id": e.ID, "delivered": connected})
	})
	// Undelivered events, then each new one, as server-sent events. Only
	// Eldar's session, with the relay channel's own pinned key.
	mux.HandleFunc("GET /relay/stream", func(w http.ResponseWriter, req *http.Request) {
		s.relay.mu.Lock()
		idle := s.relay.stream == nil
		s.relay.mu.Unlock()
		session, status, msg := s.chiefOfStaffAuth(req, &s.relayPins, "relay", idle)
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
		ctx, cancel := context.WithCancel(req.Context())
		defer cancel()
		ch := make(chan RelayEvent, 64)
		r := s.relay
		r.mu.Lock()
		if r.stream != nil {
			r.stream.cancel() // a newer connection replaces the old one
		}
		me := &chatStream{sessionID: session, cancel: cancel}
		r.stream = me
		r.subs[ch] = struct{}{}
		var pending []RelayEvent
		for _, e := range r.events {
			if e.ID > r.delivered {
				pending = append(pending, e)
			}
		}
		r.mu.Unlock()
		defer func() {
			r.mu.Lock()
			delete(r.subs, ch)
			if r.stream == me {
				r.stream = nil
			}
			r.mu.Unlock()
		}()
		send := func(e RelayEvent) bool {
			b, _ := json.Marshal(e)
			if _, err := fmt.Fprintf(w, "event: relay\ndata: %s\n\n", b); err != nil {
				return false
			}
			fl.Flush()
			r.markDelivered(e.ID)
			return true
		}
		for _, e := range pending {
			if !send(e) {
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
			case e := <-ch:
				if !send(e) {
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
}

package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// iMessage, through a BlueBubbles server on a Mac: the chat interface of an
// agent with its own Apple ID (a family agent, not Eldar). The supervisor is
// the only path between them, as with Ben's chat:
//
//   - In: it polls BlueBubbles for new messages and keeps those from allowed
//     contacts until the agent's `imessage` channel takes them from
//     GET /imessage/stream. Messages from anyone else are dropped, and only
//     their sender is audited.
//   - Out: POST /imessage/send and GET /imessage/history, to and with allowed
//     contacts only. The supervisor adds BlueBubbles' password; the agent
//     never sees it.
//
// Only the Claude session in the configured herdr workspace and tab may use
// any of it, with its channel's pinned key. A private file names the server,
// that tab and the contacts (re-read on every use, so edits apply at once):
//
//	server: http://192.168.1.27:1234
//	session: { workspace: primary, tab: Family }
//	contacts:
//	  - { name: Ben, address: "+15555550100" }   # a phone number or an Apple ID email
//
// Direct chats only: group chats are neither delivered nor sent to.
//
// Files (supervisor state): imessage.jsonl (delivered-or-pending messages,
// append-only) and imessage-state.json ({lastRowID, deliveredUpTo}).

const (
	imessageMaxText = 5_000
	imessageKeep    = 2000
	imessagePoll    = 3 * time.Second
)

type imessageContact struct {
	Name    string `yaml:"name"`
	Address string `yaml:"address"`
}

type imessageFile struct {
	Server  string `yaml:"server"`
	Session struct {
		Workspace string `yaml:"workspace"`
		Tab       string `yaml:"tab"`
	} `yaml:"session"`
	Contacts []imessageContact `yaml:"contacts"`
}

// IMessageEvent is one incoming message from an allowed contact.
type IMessageEvent struct {
	ID          int    `json:"id"`
	TS          int64  `json:"ts"`
	From        string `json:"from"` // the sender's address, normalised
	Name        string `json:"name"`
	Chat        string `json:"chat"` // BlueBubbles chat GUID
	GUID        string `json:"guid"` // the message's GUID
	Text        string `json:"text"`
	Attachments int    `json:"attachments,omitempty"`
}

type imessageState struct {
	LastRowID     int64 `json:"lastRowID"`     // newest BlueBubbles message seen
	DeliveredUpTo int   `json:"deliveredUpTo"` // newest event handed to the channel
}

type IMessage struct {
	path, passwordFile, eventsPath, statePath string
	http                                      *http.Client

	mu      sync.Mutex
	events  []IMessageEvent
	state   imessageState
	nextID  int
	subs    map[chan IMessageEvent]struct{}
	stream  *chatStream
	chats   map[string]string // address -> chat GUID, as last seen
	lastErr string
}

func NewIMessage(path, passwordFile, eventsPath, statePath string) *IMessage {
	m := &IMessage{path: path, passwordFile: passwordFile, eventsPath: eventsPath, statePath: statePath,
		http: &http.Client{Timeout: 30 * time.Second}, nextID: 1,
		subs: map[chan IMessageEvent]struct{}{}, chats: map[string]string{}}
	if f, err := os.Open(eventsPath); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			var e IMessageEvent
			if json.Unmarshal(sc.Bytes(), &e) == nil {
				m.events = append(m.events, e)
				m.chats[e.From] = e.Chat
			}
		}
		f.Close()
		if len(m.events) > imessageKeep {
			m.events = m.events[len(m.events)-imessageKeep:]
		}
		if n := len(m.events); n > 0 {
			m.nextID = m.events[n-1].ID + 1
		}
	}
	if b, err := os.ReadFile(statePath); err == nil {
		json.Unmarshal(b, &m.state)
	}
	return m
}

// load reads the config; none configured, or none there yet, means iMessage
// is off.
func (m *IMessage) load() (imessageFile, bool, error) {
	var f imessageFile
	if m.path == "" {
		return f, false, nil
	}
	b, err := os.ReadFile(m.path)
	if os.IsNotExist(err) {
		return f, false, nil
	}
	if err != nil {
		return f, false, err
	}
	if err := yaml.Unmarshal(b, &f); err != nil {
		return f, false, fmt.Errorf("%s: %w", m.path, err)
	}
	if f.Server == "" || f.Session.Workspace == "" || f.Session.Tab == "" {
		return f, false, fmt.Errorf("%s: needs server, session.workspace and session.tab", m.path)
	}
	return f, true, nil
}

// normAddress: phone numbers to E.164 (US numbers without a country code get
// +1), emails lower-cased.
func normAddress(a string) string {
	a = strings.TrimSpace(a)
	if strings.Contains(a, "@") {
		return strings.ToLower(a)
	}
	var d strings.Builder
	for _, c := range a {
		if c >= '0' && c <= '9' {
			d.WriteRune(c)
		}
	}
	n := d.String()
	switch {
	case n == "":
		return a
	case strings.HasPrefix(a, "+"):
		return "+" + n
	case len(n) == 10:
		return "+1" + n
	case len(n) == 11 && n[0] == '1':
		return "+" + n
	}
	return "+" + n
}

// contact returns the allowed contact for an address, if any.
func (f imessageFile) contact(addr string) (imessageContact, bool) {
	addr = normAddress(addr)
	for _, c := range f.Contacts {
		if normAddress(c.Address) == addr {
			return imessageContact{Name: c.Name, Address: addr}, true
		}
	}
	return imessageContact{}, false
}

func (m *IMessage) saveState() {
	b, _ := json.Marshal(m.state)
	tmp := m.statePath + ".tmp"
	if os.WriteFile(tmp, append(b, '\n'), 0o600) == nil {
		os.Rename(tmp, m.statePath)
	}
}

func (m *IMessage) add(e IMessageEvent) IMessageEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	e.ID = m.nextID
	m.nextID++
	m.events = append(m.events, e)
	if len(m.events) > imessageKeep {
		m.events = m.events[1:]
	}
	m.chats[e.From] = e.Chat
	if b, err := json.Marshal(e); err == nil {
		if f, err := os.OpenFile(m.eventsPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600); err == nil {
			f.Write(append(b, '\n'))
			f.Close()
		} else {
			logf("imessage: append failed: %v", err)
		}
	}
	for ch := range m.subs {
		select {
		case ch <- e:
		default:
		}
	}
	return e
}

func (m *IMessage) markDelivered(id int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id > m.state.DeliveredUpTo {
		m.state.DeliveredUpTo = id
		m.saveState()
	}
}

// call makes one BlueBubbles API request. Errors never include the URL, which
// carries the password.
func (m *IMessage) call(ctx context.Context, f imessageFile, method, path string, query url.Values, body any) (json.RawMessage, error) {
	pw, err := os.ReadFile(m.passwordFile)
	if err != nil {
		return nil, errors.New("BlueBubbles password unavailable")
	}
	if query == nil {
		query = url.Values{}
	}
	query.Set("password", strings.TrimSpace(string(pw)))
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(f.Server, "/")+"/api/v1/"+path+"?"+query.Encode(), rd)
	if err != nil {
		return nil, errors.New("bad BlueBubbles server address")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := m.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("BlueBubbles unreachable: %v", err)
	}
	defer res.Body.Close()
	var out struct {
		Status  int             `json:"status"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
		Error   *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("BlueBubbles answered %s with non-JSON", res.Status)
	}
	if res.StatusCode >= 300 {
		msg := out.Message
		if out.Error != nil && out.Error.Message != "" {
			msg = out.Error.Message
		}
		return nil, fmt.Errorf("BlueBubbles: %s: %s", res.Status, msg)
	}
	return out.Data, nil
}

// bbMessage is the part of a BlueBubbles message we use.
type bbMessage struct {
	ROWID                 int64  `json:"originalROWID"`
	GUID                  string `json:"guid"`
	Text                  string `json:"text"`
	IsFromMe              bool   `json:"isFromMe"`
	DateCreated           int64  `json:"dateCreated"`
	AssociatedMessageType any    `json:"associatedMessageType"`
	Handle                *struct {
		Address string `json:"address"`
	} `json:"handle"`
	Chats []struct {
		GUID  string `json:"guid"`
		Style int    `json:"style"`
	} `json:"chats"`
	Attachments []json.RawMessage `json:"attachments"`
}

// A tapback or other message about a message, rather than one of its own.
func (b bbMessage) isAssociated() bool {
	switch v := b.AssociatedMessageType.(type) {
	case nil:
		return false
	case float64:
		return v != 0
	case string:
		return v != "" && v != "0"
	}
	return true
}

const bbGroupStyle = 43

// poll fetches messages newer than the last one seen, and adds the ones from
// allowed contacts. The first poll only records where history ends.
func (s *Supervisor) pollIMessage(ctx context.Context) error {
	m := s.imessage
	f, on, err := m.load()
	if !on || err != nil {
		return err
	}
	m.mu.Lock()
	last := m.state.LastRowID
	m.mu.Unlock()
	if last == 0 {
		data, err := m.call(ctx, f, http.MethodPost, "message/query", nil, map[string]any{"limit": 1, "sort": "DESC"})
		if err != nil {
			return err
		}
		var msgs []bbMessage
		json.Unmarshal(data, &msgs)
		m.mu.Lock()
		m.state.LastRowID = 1 // an empty history starts from the first message
		if len(msgs) > 0 {
			m.state.LastRowID = msgs[0].ROWID
		}
		m.saveState()
		m.mu.Unlock()
		logf("imessage: connected; new messages after #%d", m.state.LastRowID)
		return nil
	}
	data, err := m.call(ctx, f, http.MethodPost, "message/query", nil, map[string]any{
		"limit": 100, "sort": "ASC", "with": []string{"chat", "handle", "attachment"},
		"where": []map[string]any{{"statement": "message.ROWID > :rowid", "args": map[string]any{"rowid": last}}},
	})
	if err != nil {
		return err
	}
	var msgs []bbMessage
	if err := json.Unmarshal(data, &msgs); err != nil {
		return fmt.Errorf("BlueBubbles messages: %v", err)
	}
	for _, b := range msgs {
		if b.ROWID > last {
			last = b.ROWID
		}
		if b.IsFromMe || b.isAssociated() || b.Handle == nil || len(b.Chats) == 0 {
			continue
		}
		text := strings.TrimSpace(b.Text)
		if text == "" && len(b.Attachments) == 0 {
			continue
		}
		from := normAddress(b.Handle.Address)
		c, ok := f.contact(from)
		group := b.Chats[0].Style == bbGroupStyle
		if !ok || group {
			why := "not an allowed contact"
			if group {
				why = "a group chat"
			}
			s.audit.Log(AuditEntry{RequestID: s.nextID(), Action: "imessage from " + from + " ignored",
				Decision: "deny", DecisionReason: why + " (the text was dropped)", Mode: "normal"})
			continue
		}
		e := m.add(IMessageEvent{TS: b.DateCreated, From: from, Name: c.Name, Chat: b.Chats[0].GUID,
			GUID: b.GUID, Text: truncRunes(text, imessageMaxText), Attachments: len(b.Attachments)})
		s.audit.Log(AuditEntry{RequestID: s.nextID(), Action: "imessage from " + c.Name + ": " + truncRunes(e.Text, 200),
			Decision: "allow", DecisionReason: "allowed contact " + from, Mode: "normal"})
	}
	m.mu.Lock()
	if last > m.state.LastRowID {
		m.state.LastRowID = last
		m.saveState()
	}
	m.mu.Unlock()
	return nil
}

// runIMessage polls until ctx ends, logging each change of error state once.
func (s *Supervisor) runIMessage(ctx context.Context) {
	t := time.NewTicker(imessagePoll)
	defer t.Stop()
	for {
		err := s.pollIMessage(ctx)
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		s.imessage.mu.Lock()
		changed := msg != s.imessage.lastErr
		s.imessage.lastErr = msg
		s.imessage.mu.Unlock()
		if changed {
			if msg != "" {
				logf("imessage: %s", msg)
			} else {
				logf("imessage: polling again")
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// imessageAuth: only the Claude session in the configured tab, with its
// channel's pinned key.
func (s *Supervisor) imessageAuth(r *http.Request, repin bool) (imessageFile, string, int, string) {
	f, on, err := s.imessage.load()
	if err != nil {
		return f, "", 500, err.Error()
	}
	if !on {
		return f, "", 404, "iMessage isn't configured"
	}
	session, status, msg := s.tabSessionAuth(r, &s.imessagePins, "iMessage", f.Session.Workspace, f.Session.Tab, repin)
	return f, session, status, msg
}

func randomGUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Supervisor) imessageRoutes(mux *http.ServeMux) {
	m := s.imessage
	// Undelivered messages, then each new one, as server-sent events.
	mux.HandleFunc("GET /imessage/stream", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		idle := m.stream == nil
		m.mu.Unlock()
		_, session, status, msg := s.imessageAuth(r, idle)
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
		ch := make(chan IMessageEvent, 64)
		m.mu.Lock()
		if m.stream != nil {
			m.stream.cancel() // a newer connection replaces the old one
		}
		me := &chatStream{sessionID: session, cancel: cancel}
		m.stream = me
		m.subs[ch] = struct{}{}
		var pending []IMessageEvent
		for _, e := range m.events {
			if e.ID > m.state.DeliveredUpTo {
				pending = append(pending, e)
			}
		}
		m.mu.Unlock()
		defer func() {
			m.mu.Lock()
			delete(m.subs, ch)
			if m.stream == me {
				m.stream = nil
			}
			m.mu.Unlock()
		}()
		send := func(e IMessageEvent) bool {
			b, _ := json.Marshal(e)
			if _, err := fmt.Fprintf(w, "event: imessage\ndata: %s\n\n", b); err != nil {
				return false
			}
			fl.Flush()
			m.markDelivered(e.ID)
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
	mux.HandleFunc("POST /imessage/send", func(w http.ResponseWriter, r *http.Request) {
		f, session, status, msg := s.imessageAuth(r, false)
		if status != 0 {
			sendJSON(w, status, map[string]any{"error": msg})
			return
		}
		var b struct {
			To   string `json:"to"`
			Text string `json:"text"`
		}
		readJSON(r, &b)
		text := strings.TrimSpace(strings.ReplaceAll(b.Text, "\r\n", "\n"))
		if text == "" {
			sendJSON(w, 400, map[string]any{"error": "empty message"})
			return
		}
		if len([]rune(text)) > imessageMaxText {
			sendJSON(w, 400, map[string]any{"error": fmt.Sprintf("message too long (max %d characters)", imessageMaxText)})
			return
		}
		c, ok := f.contact(b.To)
		if !ok {
			s.audit.Log(AuditEntry{RequestID: s.nextID(), Action: "imessage to " + normAddress(b.To) + ": " + truncRunes(text, 200),
				Decision: "deny", DecisionReason: "not an allowed contact", SessionID: session, Mode: "normal"})
			sendJSON(w, 403, map[string]any{"error": "not an allowed contact: " + normAddress(b.To)})
			return
		}
		m.mu.Lock()
		chat := m.chats[c.Address]
		m.mu.Unlock()
		if chat == "" {
			chat = "iMessage;-;" + c.Address
		}
		_, err := m.call(r.Context(), f, http.MethodPost, "message/text", nil,
			map[string]any{"chatGuid": chat, "tempGuid": randomGUID(), "message": text, "method": "apple-script"})
		decision, reason := "allow", "allowed contact "+c.Address
		if err != nil {
			decision, reason = "error", err.Error()
		}
		s.audit.Log(AuditEntry{RequestID: s.nextID(), Action: "imessage to " + c.Name + ": " + truncRunes(text, 200),
			Decision: decision, DecisionReason: reason, SessionID: session, Mode: "normal"})
		if err != nil {
			sendJSON(w, 502, map[string]any{"error": err.Error()})
			return
		}
		sendJSON(w, 200, map[string]any{"sent": true, "to": c.Address, "name": c.Name})
	})
	// Recent messages with one allowed contact, newest last.
	mux.HandleFunc("GET /imessage/history", func(w http.ResponseWriter, r *http.Request) {
		f, _, status, msg := s.imessageAuth(r, false)
		if status != 0 {
			sendJSON(w, status, map[string]any{"error": msg})
			return
		}
		c, ok := f.contact(r.URL.Query().Get("with"))
		if !ok {
			sendJSON(w, 403, map[string]any{"error": "not an allowed contact"})
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		limit = min(max(limit, 1), 100)
		if r.URL.Query().Get("limit") == "" {
			limit = 20
		}
		m.mu.Lock()
		chat := m.chats[c.Address]
		m.mu.Unlock()
		if chat == "" {
			chat = "iMessage;-;" + c.Address
		}
		data, err := m.call(r.Context(), f, http.MethodGet, "chat/"+url.PathEscape(chat)+"/message",
			url.Values{"limit": {strconv.Itoa(limit)}, "sort": {"DESC"}, "with": {"handle"}}, nil)
		if err != nil {
			sendJSON(w, 502, map[string]any{"error": err.Error()})
			return
		}
		var msgs []bbMessage
		json.Unmarshal(data, &msgs)
		out := []map[string]any{}
		for i := len(msgs) - 1; i >= 0; i-- {
			b := msgs[i]
			if b.isAssociated() {
				continue
			}
			from := c.Name
			if b.IsFromMe {
				from = "me"
			}
			out = append(out, map[string]any{"from": from, "text": b.Text, "ts": b.DateCreated, "attachments": len(b.Attachments)})
		}
		sendJSON(w, 200, map[string]any{"with": c.Address, "name": c.Name, "messages": out})
	})
	// The allowed contacts, so the agent knows whom it can write to.
	mux.HandleFunc("GET /imessage/contacts", func(w http.ResponseWriter, r *http.Request) {
		f, _, status, msg := s.imessageAuth(r, false)
		if status != 0 {
			sendJSON(w, status, map[string]any{"error": msg})
			return
		}
		out := []map[string]string{}
		for _, c := range f.Contacts {
			out = append(out, map[string]string{"name": c.Name, "address": normAddress(c.Address)})
		}
		sendJSON(w, 200, map[string]any{"contacts": out})
	})
}

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
//   - In: it polls BlueBubbles for new messages and keeps those in allowed
//     chats until the agent's `imessage` channel takes them from
//     GET /imessage/stream. Messages from anywhere else are dropped, and only
//     their sender is audited.
//   - Out: POST /imessage/send and GET /imessage/history, in allowed chats
//     only. The supervisor adds BlueBubbles' password; the agent never sees
//     it.
//
// Only the Claude session in the configured herdr workspace and tab may use
// any of it, with its channel's pinned key. A private file names the server,
// that tab and the contacts (re-read on every use, so edits apply at once):
//
//	server: http://192.168.1.27:1234
//	session: { workspace: primary, tab: Family }
//	contacts:
//	  - { name: Ben, address: "+15555550100" }   # a phone number or an Apple ID email
//	  - { name: Sam, address: "sam@example.com", direct: false }
//
// Allowed chats: a direct chat with a contact (unless `direct: false`), and a
// group chat whose members are all contacts, at least one of them direct. So
// a `direct: false` contact is reached only in a group with a direct one.
//
// With a webhook listener (Config.IMessageWebhook), BlueBubbles' new-message
// webhook makes the supervisor poll at once, and the regular poll slows to a
// backstop. The webhook's body is ignored: messages are only ever read from
// BlueBubbles' API, so a forged webhook can cause an extra poll and nothing
// else, and one lost while the supervisor restarts is caught by the next poll.
//
// Files (supervisor state): imessage.jsonl (delivered-or-pending messages,
// append-only) and imessage-state.json ({lastRowID, deliveredUpTo}).

const (
	imessageMaxText = 5_000
	imessageKeep    = 2000
	imessagePoll    = 3 * time.Second  // without a webhook
	imessageBackup  = 30 * time.Second // with one: a backstop for lost webhooks
)

type imessageContact struct {
	Name    string `yaml:"name"`
	Address string `yaml:"address"`
	Direct  *bool  `yaml:"direct"` // may chat one-on-one (default true)
}

func (c imessageContact) direct() bool { return c.Direct == nil || *c.Direct }

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
	ID          int      `json:"id"`
	TS          int64    `json:"ts"`
	From        string   `json:"from"` // the sender's address, normalised
	Name        string   `json:"name"`
	Chat        string   `json:"chat"` // BlueBubbles chat GUID
	ChatName    string   `json:"chatName,omitempty"`
	Group       bool     `json:"group,omitempty"`
	Members     []string `json:"members,omitempty"` // a group's members, by name
	GUID        string   `json:"guid"`              // the message's GUID
	Text        string   `json:"text"`
	Attachments int      `json:"attachments,omitempty"`
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
	chats   map[string]string     // address -> direct chat GUID, as last seen
	groups  map[string]bbChatInfo // chat GUID -> members, briefly cached
	lastErr string
	nudge   chan struct{} // poll now (the webhook)
}

func NewIMessage(path, passwordFile, eventsPath, statePath string) *IMessage {
	m := &IMessage{path: path, passwordFile: passwordFile, eventsPath: eventsPath, statePath: statePath,
		http: &http.Client{Timeout: 30 * time.Second}, nextID: 1,
		subs: map[chan IMessageEvent]struct{}{}, chats: map[string]string{}, groups: map[string]bbChatInfo{},
		nudge: make(chan struct{}, 1)}
	if f, err := os.Open(eventsPath); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			var e IMessageEvent
			if json.Unmarshal(sc.Bytes(), &e) == nil {
				m.events = append(m.events, e)
				if !e.Group {
					m.chats[e.From] = e.Chat
				}
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
			c.Address = addr
			return c, true
		}
	}
	return imessageContact{}, false
}

// bbChatInfo: a chat's kind, name and members (addresses, normalised).
type bbChatInfo struct {
	at      time.Time
	Group   bool
	Name    string
	Members []string
}

// chatInfo fetches a chat's members from BlueBubbles, cached for a minute.
func (m *IMessage) chatInfo(ctx context.Context, f imessageFile, guid string) (bbChatInfo, error) {
	m.mu.Lock()
	ci, ok := m.groups[guid]
	m.mu.Unlock()
	if ok && time.Since(ci.at) < time.Minute {
		return ci, nil
	}
	data, err := m.call(ctx, f, http.MethodGet, "chat/"+url.PathEscape(guid), url.Values{"with": {"participants"}}, nil)
	if err != nil {
		return ci, err
	}
	var c struct {
		Style        int    `json:"style"`
		DisplayName  string `json:"displayName"`
		Participants []struct {
			Address string `json:"address"`
		} `json:"participants"`
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return ci, fmt.Errorf("BlueBubbles chat: %v", err)
	}
	ci = bbChatInfo{at: time.Now(), Group: c.Style == bbGroupStyle, Name: c.DisplayName}
	for _, p := range c.Participants {
		ci.Members = append(ci.Members, normAddress(p.Address))
	}
	m.mu.Lock()
	m.groups[guid] = ci
	m.mu.Unlock()
	return ci, nil
}

// allowedChat decides whether a chat is one the agent may use, and names its
// members. Direct: its one member is a direct contact. Group: every member is
// a contact and at least one is direct.
func (f imessageFile) allowedChat(ci bbChatInfo) ([]string, string) {
	var names []string
	direct := false
	for _, a := range ci.Members {
		c, ok := f.contact(a)
		if !ok {
			return nil, a + " isn't an allowed contact"
		}
		names = append(names, c.Name)
		direct = direct || c.direct()
	}
	switch {
	case len(names) == 0:
		return nil, "the chat has no members"
	case !ci.Group && len(names) != 1:
		return nil, "a direct chat with more than one member"
	case !direct && ci.Group:
		return nil, "no member of the group may chat directly"
	case !direct:
		return nil, names[0] + " may only be reached in a group"
	}
	return names, ""
}

// resolveChat turns a reply target (an address, or a chat GUID from a
// message) into an allowed chat's GUID, label and members.
func (m *IMessage) resolveChat(ctx context.Context, f imessageFile, target string) (guid, label string, members []string, err error) {
	target = strings.TrimSpace(target)
	if strings.Contains(target, ";") { // a chat GUID: "iMessage;-;…" or "iMessage;+;chat…"
		ci, err := m.chatInfo(ctx, f, target)
		if err != nil {
			return "", "", nil, err
		}
		names, why := f.allowedChat(ci)
		if why != "" {
			return "", "", nil, errors.New("not an allowed chat: " + why)
		}
		label = names[0]
		if ci.Group {
			label = "group " + strings.Join(names, ", ")
			if ci.Name != "" {
				label = "group \"" + ci.Name + "\" (" + strings.Join(names, ", ") + ")"
			}
		}
		return target, label, names, nil
	}
	c, ok := f.contact(target)
	if !ok {
		return "", "", nil, errors.New("not an allowed contact: " + normAddress(target))
	}
	if !c.direct() {
		return "", "", nil, errors.New(c.Name + " may only be reached in a group chat (use its chat)")
	}
	m.mu.Lock()
	guid = m.chats[c.Address]
	m.mu.Unlock()
	if guid == "" {
		guid = "iMessage;-;" + c.Address
	}
	return guid, c.Name, []string{c.Name}, nil
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
	if !e.Group {
		m.chats[e.From] = e.Chat
	}
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
	done := func(upTo int64) { // record progress, so nothing is delivered twice
		m.mu.Lock()
		if upTo > m.state.LastRowID {
			m.state.LastRowID = upTo
			m.saveState()
		}
		m.mu.Unlock()
	}
	for _, b := range msgs {
		prev := last
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
		chat := b.Chats[0]
		ci := bbChatInfo{Group: chat.Style == bbGroupStyle, Members: []string{from}}
		var names []string
		why := "not an allowed contact"
		if ok && ci.Group {
			if ci, err = m.chatInfo(ctx, f, chat.GUID); err != nil {
				done(prev) // try this message again on the next poll
				return err
			}
		}
		if ok {
			names, why = f.allowedChat(ci)
		}
		if why != "" {
			s.audit.Log(AuditEntry{RequestID: s.nextID(), Action: "imessage from " + from + " ignored",
				Decision: "deny", DecisionReason: why + " (the text was dropped)", Mode: "normal"})
			continue
		}
		ev := IMessageEvent{TS: b.DateCreated, From: from, Name: c.Name, Chat: chat.GUID,
			GUID: b.GUID, Text: truncRunes(text, imessageMaxText), Attachments: len(b.Attachments)}
		where := "direct"
		if ci.Group {
			ev.Group, ev.ChatName, ev.Members = true, ci.Name, names
			where = "group with " + strings.Join(names, ", ")
		}
		e := m.add(ev)
		s.audit.Log(AuditEntry{RequestID: s.nextID(), Action: "imessage from " + c.Name + ": " + truncRunes(e.Text, 200),
			Decision: "allow", DecisionReason: "allowed chat (" + where + ")", Mode: "normal"})
	}
	done(last)
	return nil
}

// Nudge asks for a poll now; nudges while one is pending coalesce.
func (m *IMessage) Nudge() {
	select {
	case m.nudge <- struct{}{}:
	default:
	}
}

// runIMessage polls until ctx ends, logging each change of error state once.
func (s *Supervisor) runIMessage(ctx context.Context) {
	every := imessagePoll
	if s.cfg.IMessageWebhook != "" {
		every = imessageBackup
	}
	t := time.NewTicker(every)
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
		case <-s.imessage.nudge:
		}
	}
}

// imessageWebhook serves BlueBubbles' webhook: any POST is a nudge to poll.
func (s *Supervisor) imessageWebhook() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /bluebubbles/webhook", func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20))
		s.imessage.Nudge()
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
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
			To   string `json:"to"` // an address, or a chat GUID
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
		chat, label, _, err := m.resolveChat(r.Context(), f, b.To)
		if err != nil {
			s.audit.Log(AuditEntry{RequestID: s.nextID(), Action: "imessage to " + truncRunes(b.To, 80) + ": " + truncRunes(text, 200),
				Decision: "deny", DecisionReason: err.Error(), SessionID: session, Mode: "normal"})
			sendJSON(w, 403, map[string]any{"error": err.Error()})
			return
		}
		_, err = m.call(r.Context(), f, http.MethodPost, "message/text", nil,
			map[string]any{"chatGuid": chat, "tempGuid": randomGUID(), "message": text, "method": "apple-script"})
		decision, reason := "allow", "allowed chat"
		if err != nil {
			decision, reason = "error", err.Error()
		}
		s.audit.Log(AuditEntry{RequestID: s.nextID(), Action: "imessage to " + label + ": " + truncRunes(text, 200),
			Decision: decision, DecisionReason: reason, SessionID: session, Mode: "normal"})
		if err != nil {
			sendJSON(w, 502, map[string]any{"error": err.Error()})
			return
		}
		sendJSON(w, 200, map[string]any{"sent": true, "to": label, "name": label})
	})
	// Recent messages in one allowed chat (`with`: an address, or a chat GUID),
	// newest last.
	mux.HandleFunc("GET /imessage/history", func(w http.ResponseWriter, r *http.Request) {
		f, _, status, msg := s.imessageAuth(r, false)
		if status != 0 {
			sendJSON(w, status, map[string]any{"error": msg})
			return
		}
		chat, label, _, err := m.resolveChat(r.Context(), f, r.URL.Query().Get("with"))
		if err != nil {
			sendJSON(w, 403, map[string]any{"error": err.Error()})
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		limit = min(max(limit, 1), 100)
		if r.URL.Query().Get("limit") == "" {
			limit = 20
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
			from := "?"
			if b.Handle != nil {
				from = normAddress(b.Handle.Address)
				if c, ok := f.contact(from); ok {
					from = c.Name
				}
			}
			if b.IsFromMe {
				from = "me"
			}
			out = append(out, map[string]any{"from": from, "text": b.Text, "ts": b.DateCreated, "attachments": len(b.Attachments)})
		}
		sendJSON(w, 200, map[string]any{"chat": chat, "name": label, "messages": out})
	})
	// The contacts, so the agent knows whom it can write to (direct: false
	// ones only in a group chat).
	mux.HandleFunc("GET /imessage/contacts", func(w http.ResponseWriter, r *http.Request) {
		f, _, status, msg := s.imessageAuth(r, false)
		if status != 0 {
			sendJSON(w, status, map[string]any{"error": msg})
			return
		}
		out := []map[string]any{}
		for _, c := range f.Contacts {
			out = append(out, map[string]any{"name": c.Name, "address": normAddress(c.Address), "direct": c.direct()})
		}
		sendJSON(w, 200, map[string]any{"contacts": out})
	})
}

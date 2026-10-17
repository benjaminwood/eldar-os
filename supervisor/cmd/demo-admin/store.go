package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/benjaminwood/eldar-os/supervisor/api"
)

// demo is the mock supervisor's state. It answers the admin API the way the
// real one does for what the screenshots and the promo show: a decision
// resolves its approval and is audited, a grant is matched against later
// requests (target, session, profile, credentials, AWS, as server/grants.go
// does), chat messages arrive. The control API (control.go) plays the agents:
// it files requests and has Eldar reply.
type demo struct {
	mu       sync.Mutex
	st       api.State
	chat     []api.ChatMessage
	readUpTo int
	audit    []api.AuditEntry
	nextID   int
	subs     map[chan struct{}]struct{}
	// The terminal's story (terminal.go): which approvals it waits on.
	pushID, checkID int
}

func newDemo() *demo {
	d := &demo{st: state(), chat: slices.Clone(chat), readUpTo: 6, audit: audit(), nextID: 44,
		subs: map[chan struct{}]struct{}{}, pushID: 41, checkID: 43}
	return d
}

// changed wakes every subscriber (the event stream, terminals).
func (d *demo) changed() {
	for c := range d.subs {
		select {
		case c <- struct{}{}:
		default:
		}
	}
}

func (d *demo) subscribe() (<-chan struct{}, func()) {
	c := make(chan struct{}, 1)
	d.mu.Lock()
	d.subs[c] = struct{}{}
	d.mu.Unlock()
	return c, func() { d.mu.Lock(); delete(d.subs, c); d.mu.Unlock() }
}

// snapshot is the state as JSON, marshalled under the lock.
func (d *demo) snapshot() []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	st := d.st
	st.Now = time.Now().UnixMilli()
	last := d.chat[len(d.chat)-1]
	unread := 0
	for _, m := range d.chat {
		if m.From == "eldar" && m.ID > d.readUpTo {
			unread++
		}
	}
	st.Chat.LastID, st.Chat.ReadUpTo, st.Chat.Unread, st.Chat.Last = last.ID, d.readUpTo, unread, &last
	b, _ := json.Marshal(st)
	return b
}

func (d *demo) chatJSON() []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	b, _ := json.Marshal(map[string]any{"messages": d.chat, "readUpTo": d.readUpTo, "connected": true, "eldar": d.st.Chat.Eldar})
	return b
}

func (d *demo) auditJSON() []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	b, _ := json.Marshal(map[string]any{"entries": d.audit})
	return b
}

func stamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func credsJSON(c *[]string) json.RawMessage {
	if c == nil {
		return nil
	}
	b, _ := json.Marshal(*c)
	return b
}

// touch records a session's activity and sets its herdr pane's status:
// blocked while it has an approval waiting, otherwise working.
func (d *demo) touch(session, action string) {
	for i := range d.st.Sessions {
		if s := &d.st.Sessions[i]; s.ID == session {
			s.LastSeen, s.Requests = time.Now().UnixMilli(), s.Requests+1
			if action != "" {
				s.LastAction = action
			}
		}
	}
	status := "working"
	for _, p := range d.st.Pending {
		if p.SessionID == session {
			status = "blocked"
		}
	}
	d.setStatusLocked(session, status)
}

func (d *demo) setStatusLocked(session, status string) {
	for i := range d.st.HerdrPanes {
		if p := &d.st.HerdrPanes[i]; p.SessionID == session && p.Title != "Eldar" {
			p.Status = status
		}
	}
	for si := range d.st.Herdr.Spaces {
		sp := &d.st.Herdr.Spaces[si]
		for ti := range sp.Tabs {
			tb := &sp.Tabs[ti]
			for pi := range tb.Panes {
				if a := tb.Panes[pi].Agent; a != nil && a.SessionID == session && a.Name != "Eldar" {
					a.Status, tb.Status = status, status
				}
			}
		}
	}
}

// decide answers an approval as POST /admin/decide does: it's gone from the
// queue, and the audit log says who decided and what ran.
func (d *demo) decide(id int, dec api.Decision) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	i := slices.IndexFunc(d.st.Pending, func(p api.Pending) bool { return p.ID == id })
	if i < 0 {
		return false
	}
	p := d.st.Pending[i]
	d.st.Pending = slices.Delete(d.st.Pending, i, i+1)
	e := api.AuditEntry{Timestamp: stamp(), RequestID: p.ID, Action: p.Command, Reason: p.Reason, Profile: p.Profile,
		Decision: "denied", DecisionReason: "Denied by Ben", Mode: "normal", Credentials: credsJSON(p.Credentials),
		AwsProfile: p.AwsProfile, SessionID: p.SessionID, Target: "eldar"}
	if p.Target != "" {
		e.Target, e.Action = p.Target, p.Target+": "+p.Command
	}
	if dec != api.Deny {
		zero, ms := 0, int64(1200)
		e.Decision, e.DecisionReason, e.ExitCode, e.DurationMs = "allow", "Approved by Ben", &zero, &ms
	}
	if p.Kind == "browser" {
		e.Action, e.Target = fmt.Sprintf("BROWSER WINDOW #%d: Arc (%s) on %s", p.ID, p.ArcProfileName, p.Host), "browser"
		e.ExitCode, e.DurationMs = nil, nil
	}
	d.audit = append(d.audit, e)
	d.touch(p.SessionID, "")
	d.changed()
	return true
}

// grantBody is POST /admin/grants: absent credentials mean any, absent AWS
// none (the editors' defaults, as in server/admin.go).
type grantBody struct {
	Target      string     `json:"target"`
	Session     string     `json:"session"`
	Profile     string     `json:"profile"`
	Credentials *api.Creds `json:"credentials"`
	Aws         *api.Creds `json:"aws"`
	Minutes     *float64   `json:"minutes"`
	Note        string     `json:"note"`
}

func (d *demo) addGrant(b grantBody, client string) api.Grant {
	d.mu.Lock()
	defer d.mu.Unlock()
	g := api.Grant{Target: b.Target, Session: b.Session, Profile: b.Profile, Credentials: api.AnyCreds,
		Aws: api.Creds{Names: []string{}}, CreatedAt: time.Now().UnixMilli(), Note: b.Note}
	if g.Target == "" {
		g.Target = "eldar"
	}
	if g.Session == "" {
		g.Session = "*"
	}
	if g.Profile == "" {
		g.Profile = "*"
	}
	if b.Credentials != nil {
		g.Credentials = *b.Credentials
	}
	if b.Aws != nil {
		g.Aws = *b.Aws
	}
	until := "until revoked"
	if b.Minutes != nil && *b.Minutes > 0 {
		exp := time.Now().Add(time.Duration(*b.Minutes) * time.Minute).UnixMilli()
		g.ExpiresAt, until = &exp, fmt.Sprintf("%dm", int(*b.Minutes))
	}
	for _, x := range d.st.Grants {
		g.ID = max(g.ID, x.ID)
	}
	g.ID++
	d.st.Grants = append(d.st.Grants, g)
	who := "all"
	if g.Session != "*" {
		who = g.Session[:8]
	}
	d.audit = append(d.audit, api.AuditEntry{Timestamp: stamp(), Action: fmt.Sprintf("GRANT #%d", g.ID), Decision: "allow", Mode: "normal",
		DecisionReason: fmt.Sprintf("Bypass grant session=%s profile=%s creds=%s aws=%s, %s (via %s)", who, g.Profile, g.Credentials, g.Aws, until, client)})
	d.changed()
	return g
}

// match is server/grants.go's Grants.Match for the demo's requests (every
// demo session presents its pinned key).
func (d *demo) match(r request) *api.Grant {
	for i := range d.st.Grants {
		g := &d.st.Grants[i]
		if g.Target != r.Target || (g.Session != "*" && g.Session != r.Session) {
			continue
		}
		if g.ExpiresAt != nil && *g.ExpiresAt <= time.Now().UnixMilli() {
			continue
		}
		if g.Profile != "*" && g.Profile != r.Profile {
			continue
		}
		if !g.Credentials.Any && slices.ContainsFunc(r.Credentials, func(c string) bool { return !slices.Contains(g.Credentials.Names, c) }) {
			continue
		}
		if r.AwsProfile != "" && !g.Aws.Any && !slices.Contains(g.Aws.Names, r.AwsProfile) {
			continue
		}
		return g
	}
	return nil
}

// request is an agent asking to run a command (the control API's stand-in for
// supervisor_exec / supervisor_host_exec).
type request struct {
	Session     string   `json:"session"`
	Command     string   `json:"command"`
	Reason      string   `json:"reason"`
	Profile     string   `json:"profile"`
	Credentials []string `json:"credentials"`
	AwsProfile  string   `json:"awsProfile"`
	Target      string   `json:"target"` // eldar (default) or a host
	Cwd         string   `json:"cwd"`
}

// file decides a request as HandleExec does after the policy (none of the
// demo's commands match a rule): a grant runs it, otherwise it waits for Ben.
// It returns the request ID and whether it ran.
func (d *demo) file(r request) (int, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if r.Target == "" {
		r.Target = "eldar"
	}
	if r.Profile == "" && r.Target == "eldar" {
		r.Profile = "work"
	}
	d.nextID++
	id := d.nextID
	creds := slices.Clone(r.Credentials)
	if creds == nil {
		creds = []string{}
	}
	if g := d.match(r); g != nil {
		g.Uses++
		zero, ms := 0, int64(2300)
		why := fmt.Sprintf("Bypass grant #%d", g.ID)
		if g.Session != "*" {
			why += fmt.Sprintf(" (session %.8s)", g.Session)
		}
		d.audit = append(d.audit, api.AuditEntry{Timestamp: stamp(), RequestID: id, Action: r.Command, Reason: r.Reason, Profile: r.Profile,
			Decision: "allow", DecisionReason: why, Mode: "normal", ExitCode: &zero, DurationMs: &ms, Credentials: credsJSON(&creds),
			AwsProfile: r.AwsProfile, SessionID: r.Session, Target: r.Target})
		d.touch(r.Session, r.Command)
		d.changed()
		return id, true
	}
	p := api.Pending{ID: id, Kind: "exec", Title: strings.SplitN(r.Command, "\n", 2)[0], Command: r.Command, Reason: r.Reason,
		Profile: r.Profile, Credentials: &creds, AwsProfile: r.AwsProfile, TimeoutSeconds: 300, SessionID: r.Session,
		SessionVerified: true, Cwd: r.Cwd, Channel: "none", AllowAlways: strings.Fields(r.Command)[0] + " *",
		CreatedAt: time.Now().UnixMilli(), ExpiresAt: time.Now().Add(3 * time.Minute).UnixMilli()}
	if r.Target != "eldar" {
		p.Target, p.Host = r.Target, "Ben's Mac"
	}
	d.st.Pending = append(d.st.Pending, p)
	d.touch(r.Session, r.Command)
	d.changed()
	return id, false
}

func (d *demo) addChat(from, text string) api.ChatMessage {
	d.mu.Lock()
	defer d.mu.Unlock()
	m := api.ChatMessage{ID: d.chat[len(d.chat)-1].ID + 1, From: from, Text: text, TS: time.Now().UnixMilli()}
	d.chat = append(d.chat, m)
	if from == "ben" {
		d.readUpTo = max(d.readUpTo, m.ID)
	}
	d.changed()
	return m
}

func (d *demo) markRead(upTo int) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.readUpTo = max(d.readUpTo, upTo)
	d.changed()
	return d.readUpTo
}

func (d *demo) setStatus(session, status string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.setStatusLocked(session, status)
	d.changed()
}

// story is where the terminal's Claude session is: waiting on the push
// (#41), pushed and waiting on its log check (#43), or done.
func (d *demo) story() (stage, checkID int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	waiting := func(id int) bool {
		return slices.ContainsFunc(d.st.Pending, func(p api.Pending) bool { return p.ID == id })
	}
	switch {
	case waiting(d.pushID):
		return 0, 0
	case waiting(d.checkID):
		return 1, d.checkID
	}
	return 2, d.checkID
}

// Package view derives what the UIs display from a raw api.State: sessions
// merged with their herdr panes, each session's effective bypass, and labels.
// Both the TUI and the web UI render these structs, so they can't disagree
// about what "this session is bypassed" means.
package view

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/benjaminwood/eldar-os/supervisor/api"
)

// Level is how much of a session's traffic skips approval.
type Level string

const (
	LevelNone    Level = "none"    // everything asks per policy
	LevelPartial Level = "partial" // some profiles/credentials skip approval
	LevelFull    Level = "full"    // everything skips approval
	LevelLocked  Level = "locked"  // lockdown: everything is refused
)

// Cover is one thing that lets a session skip approval.
type Cover struct {
	Source    string `json:"source"` // global | grant | agent
	ID        int    `json:"id,omitempty"`
	Session   string `json:"session"`       // "*" = applies to all sessions
	Profile   string `json:"profile"`       // "*" = any profile
	Creds     string `json:"creds"`         // human summary
	Aws       string `json:"aws,omitempty"` // human summary of the AWS roles covered
	Full      bool   `json:"full"`
	ExpiresAt *int64 `json:"expiresAt"`
	Note      string `json:"note,omitempty"`
}

func (c Cover) Label() string {
	who := ""
	switch c.Source {
	case "global":
		return "global bypass"
	case "agent":
		who = "agent-held"
	case "grant":
		who = "grant"
	}
	if c.Full {
		return who + ": everything"
	}
	prof := c.Profile
	if prof == "*" {
		prof = "any profile"
	}
	if c.Aws != "" {
		return fmt.Sprintf("%s: %s · %s · %s", who, prof, c.Creds, c.Aws)
	}
	return fmt.Sprintf("%s: %s · %s", who, prof, c.Creds)
}

// Session is a Claude session as the UIs show it.
type Session struct {
	ID            string  `json:"id"`
	Title         string  `json:"title"`
	Cwd           string  `json:"cwd,omitempty"`
	Pane          string  `json:"pane,omitempty"`
	Status        string  `json:"status,omitempty"` // herdr agent status
	InHerdr       bool    `json:"inHerdr"`
	Focused       bool    `json:"focused"`
	SeenByProxy   bool    `json:"seenByProxy"`
	Requests      int     `json:"requests"`
	LastSeen      int64   `json:"lastSeen,omitempty"`
	LastAction    string  `json:"lastAction,omitempty"`
	Pinned        bool    `json:"pinned"`
	KeyMismatches int     `json:"keyMismatches"`
	Pending       int     `json:"pending"`
	Level         Level   `json:"level"`
	Covers        []Cover `json:"covers"`
}

// Short is the first block of a session ID.
func Short(id string) string {
	if i := strings.IndexByte(id, '-'); i > 0 {
		return id[:i]
	}
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func grantCover(g api.Grant) Cover {
	return Cover{
		Source: "grant", ID: g.ID, Session: g.Session, Profile: g.Profile,
		Creds: g.Credentials.String(), Aws: AwsScope(g.Aws), Full: FullGrant(g),
		ExpiresAt: g.ExpiresAt, Note: g.Note,
	}
}

// FullGrant reports whether g covers everything its target can ask for.
func FullGrant(g api.Grant) bool {
	switch g.Target {
	case "browser":
		return g.Arc.Any
	case "", "eldar":
		return g.Profile == "*" && g.Credentials.Any && g.Aws.Any
	default: // a remote host: no AWS there
		return g.Profile == "*" && g.Credentials.Any
	}
}

// EntryHost is the remote host an audit entry ran on ("" for Eldar's VM and
// everything else), and EntryCommand its command without the "<host>: " prefix.
func EntryHost(e api.AuditEntry) string {
	if e.Target == "" || e.Target == "eldar" || e.Target == "browser" {
		return ""
	}
	return e.Target
}

func EntryCommand(e api.AuditEntry) string {
	if h := EntryHost(e); h != "" {
		return strings.TrimPrefix(e.Action, h+": ")
	}
	return e.Action
}

// IsEldarGrant: a grant for commands in Eldar's VM.
func IsEldarGrant(g api.Grant) bool { return g.Target == "" || g.Target == "eldar" }

// AwsScope describes the AWS roles a grant covers.
func AwsScope(c api.Creds) string {
	if c.Any {
		return "any AWS role"
	}
	if len(c.Names) == 0 {
		return "no AWS"
	}
	labels := make([]string, len(c.Names))
	for i, n := range c.Names {
		labels[i] = AwsLabel(n)
	}
	return "AWS " + strings.Join(labels, ", ")
}

// AwsLabel splits an AWS profile "<account>.<role>" into "account · role".
func AwsLabel(profile string) string {
	if i := strings.LastIndexByte(profile, '.'); i > 0 {
		return profile[:i] + " · " + profile[i+1:]
	}
	return profile
}

// AwsRisky flags admin roles and production-looking accounts.
func AwsRisky(profile string) bool {
	p := strings.ToLower(profile)
	return strings.Contains(p, "admin") || strings.Contains(p, "prod") || strings.Contains(p, "security")
}

// CoversFor lists everything that lets session id skip approval, narrowest
// first. Agent-held bypasses are matched by the session ID the agent reported
// when it asked; the token, not the ID, is what actually gates them.
func CoversFor(st api.State, id string) []Cover {
	var out []Cover
	if st.Mode == api.ModeBypass {
		out = append(out, Cover{Source: "global", Session: "*", Profile: "*", Creds: "any credentials", Full: true, ExpiresAt: st.ModeExpiresAt})
	}
	var specific, global []Cover
	for _, g := range st.Grants {
		if !IsEldarGrant(g) {
			continue // Mac and browser grants don't change what a session's eldar commands do
		}
		switch g.Session {
		case id:
			specific = append(specific, grantCover(g))
		case "*":
			global = append(global, grantCover(g))
		}
	}
	return append(append(specific, global...), out...)
}

func levelOf(st api.State, covers []Cover) Level {
	if st.Mode == api.ModeLockdown {
		return LevelLocked
	}
	if len(covers) == 0 {
		return LevelNone
	}
	for _, c := range covers {
		if c.Full {
			return LevelFull
		}
	}
	return LevelPartial
}

// Sessions merges proxy sessions with herdr panes. Order: sessions with
// pending approvals, then herdr's focused pane, then most recently active.
func Sessions(st api.State) []Session {
	byID := map[string]*Session{}
	var order []string
	get := func(id string) *Session {
		if s, ok := byID[id]; ok {
			return s
		}
		s := &Session{ID: id}
		byID[id] = s
		order = append(order, id)
		return s
	}
	for _, p := range st.HerdrPanes {
		if p.SessionID == "" {
			continue
		}
		s := get(p.SessionID)
		s.InHerdr, s.Pane, s.Title, s.Cwd, s.Status, s.Focused = true, p.PaneID, p.Title, p.Cwd, p.Status, p.Focused
	}
	for _, ps := range st.Sessions {
		s := get(ps.ID)
		s.SeenByProxy = true
		s.Requests, s.LastSeen, s.LastAction = ps.Requests, ps.LastSeen, ps.LastAction
		s.Pinned, s.KeyMismatches = ps.Pinned, ps.KeyMismatches
		if s.Cwd == "" {
			s.Cwd = ps.Cwd
		}
		if s.Pane == "" {
			s.Pane = ps.HerdrPane
		}
	}
	for _, p := range st.Pending {
		if p.SessionID != "" {
			get(p.SessionID).Pending++
		}
	}
	out := make([]Session, 0, len(order))
	for _, id := range order {
		s := byID[id]
		if s.Title == "" {
			if s.Cwd != "" {
				s.Title = filepath.Base(s.Cwd)
			} else {
				s.Title = "session " + Short(id)
			}
		}
		s.Covers = CoversFor(st, id)
		s.Level = levelOf(st, s.Covers)
		out = append(out, *s)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.Pending > 0) != (b.Pending > 0) {
			return a.Pending > 0
		}
		if a.Focused != b.Focused {
			return a.Focused
		}
		if a.InHerdr != b.InHerdr {
			return a.InHerdr
		}
		return a.LastSeen > b.LastSeen
	})
	return out
}

// SessionTitle finds a display name for a session ID.
func SessionTitle(sessions []Session, id string) string {
	if id == "" {
		return "unknown session"
	}
	for _, s := range sessions {
		if s.ID == id {
			return s.Title
		}
	}
	return "session " + Short(id)
}

// Remaining formats the time until ms (epoch millis), relative to now.
func Remaining(ms int64, now time.Time) string {
	d := time.UnixMilli(ms).Sub(now)
	if d <= 0 {
		return "expired"
	}
	return Duration(d)
}

// Ago formats how long ago ms was.
func Ago(ms int64, now time.Time) string {
	if ms == 0 {
		return "never"
	}
	d := now.Sub(time.UnixMilli(ms))
	if d < 5*time.Second {
		return "just now"
	}
	return Duration(d) + " ago"
}

func Duration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		m := int(d.Minutes())
		s := int(d.Seconds()) % 60
		if m < 10 && s > 0 {
			return fmt.Sprintf("%dm%02ds", m, s)
		}
		return fmt.Sprintf("%dm", m)
	case d < 48*time.Hour:
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%02dm", h, m)
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// CredsOf summarizes a pending request's credential scope.
func CredsOf(p api.Pending) string {
	if p.Credentials == nil {
		if p.Kind != "exec" {
			return ""
		}
		return "ALL in profile"
	}
	if len(*p.Credentials) == 0 {
		if p.AwsProfile != "" {
			return "none from 1Password"
		}
		return "none"
	}
	return strings.Join(*p.Credentials, ", ")
}

// KindLabel names a pending request's kind.
func KindLabel(kind string) string {
	return KindLabelFor(kind, "", "")
}

// KindLabelFor names a request, marking ones that run on the Mac.
// KindLabelFor labels a pending request; host is the remote host's label
// (Pending.Host) for requests that run on one.
func KindLabelFor(kind, target, host string) string {
	if host == "" {
		host = target
	}
	if kind == "browser" {
		return "BROWSER ON " + strings.ToUpper(host)
	}
	if target != "" && target != "eldar" {
		return "ON " + strings.ToUpper(host)
	}
	if kind == "grant_request" {
		return "GRANT REQUEST"
	}
	return "COMMAND"
}

var boilerplate = regexp.MustCompile(`^\s*(#.*|set\s+[-+][A-Za-z]+.*|set\s+-o\s+\S+.*|)\s*$`)

// CommandSummary is the line that best describes a command — the first one
// that isn't blank, a comment or a `set -e`-style shell option — and how many
// more lines there are.
func CommandSummary(cmd string) (string, int) {
	lines := strings.Split(strings.TrimSpace(cmd), "\n")
	for _, l := range lines {
		if !boilerplate.MatchString(l) {
			return strings.TrimSpace(l), len(lines) - 1
		}
	}
	return strings.TrimSpace(lines[0]), len(lines) - 1
}

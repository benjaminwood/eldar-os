package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/benjaminwood/eldar-os/supervisor/api"
)

// The admin API: served only on the admin socket (see listenAdmin), never on
// the agent's port. The TUI talks to it directly; the web UI relays to it.

// Snapshot is everything the admin UIs show.
func (s *Supervisor) Snapshot() api.State {
	mode, exp := s.modes.Status()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	herdr := s.herdr.View(ctx, "", 3*time.Second)
	return api.State{
		Now: time.Now().UnixMilli(), Mode: mode, ModeExpiresAt: exp,
		Pending:       s.approvals.List(),
		Sessions:      s.sessions.List(),
		HerdrPanes:    ClaudePanes(herdr),
		Grants:        s.grants.List(),
		Profiles:      s.profiles.Names(),
		Cache:         s.profiles.Cache(),
		AwsProfiles:   []string{},
		ArcProfiles:   []api.ArcProfile{},
		Settings:      s.settings.Get(),
		MCP:           api.MCPSummary{Servers: []api.MCPServer{}, Pending: []api.MCPSignIn{}},
		Herdr:         herdr,
		Mac:           api.MacStatus{Allow: []string{}},
		BrowserGrants: []api.BrowserGrant{},
		Crons:         s.crons.List(),
		Viewers:       s.n.Subscribers(),
	}
}

func clientName(r *http.Request) string {
	v := regexp.MustCompile(`[^\w .:·@-]`).ReplaceAllString(r.Header.Get("X-Admin-Client"), "")
	if v = truncRunes(v, 80); v == "" {
		return "admin API"
	}
	return v
}

// adminLog audits a change made from an admin UI.
func (s *Supervisor) adminLog(r *http.Request, action, why string) {
	s.audit.Log(AuditEntry{Action: action, Decision: "allow", DecisionReason: fmt.Sprintf("%s (via %s)", why, clientName(r)), Mode: string(s.modes.Current())})
}

// adminGrantBody is a grant from the admin UIs; scopes stay raw so absent
// fields get the editor's defaults (credentials any, aws none, arc any).
type adminGrantBody struct {
	Target      string          `json:"target"`
	Session     string          `json:"session"`
	Profile     string          `json:"profile"`
	Credentials json.RawMessage `json:"credentials"`
	Aws         json.RawMessage `json:"aws"`
	Arc         json.RawMessage `json:"arc"`
	ArcNames    []string        `json:"arcNames"`
	Minutes     *float64        `json:"minutes"`
	Note        string          `json:"note"`
}

func (s *Supervisor) parseAdminGrant(b adminGrantBody) (api.GrantInput, string) {
	target := b.Target
	if target == "" {
		target = "eldar"
	}
	switch target {
	case "eldar", "mac":
	case "browser":
		return api.GrantInput{}, "Arc grants aren't available on this supervisor yet"
	default:
		return api.GrantInput{}, "target must be eldar, mac or browser"
	}
	profile := b.Profile
	if profile == "" {
		profile = "*"
	}
	if profile != "*" && !slices.Contains(s.profiles.Names(), profile) {
		return api.GrantInput{}, fmt.Sprintf("unknown profile '%s'", profile)
	}
	creds, ok := parseScope(b.Credentials, api.AnyCreds)
	if !ok {
		return api.GrantInput{}, `credentials must be "*" or an array of names`
	}
	aws, ok := parseScope(b.Aws, api.Creds{Names: []string{}})
	if !ok {
		return api.GrantInput{}, `aws must be "*" or an array of AWS profile names`
	}
	if len(aws.Names) > 0 {
		return api.GrantInput{}, "unknown AWS profiles: " + strings.Join(aws.Names, ", ")
	}
	in := api.GrantInput{Target: target, Session: b.Session, Profile: profile, Credentials: creds, Aws: aws, Note: truncRunes(b.Note, 200)}
	if in.Session == "" {
		in.Session = "*"
	}
	if b.Minutes != nil && *b.Minutes > 0 {
		m := int(*b.Minutes)
		in.Minutes = &m
	}
	return in, ""
}

var notYet = map[string]string{
	"/admin/mac": "Mac access", "/admin/mcp": "MCP sign-ins",
	"/admin/chat": "chat", "/admin/browser-grants": "Arc",
}

func (s *Supervisor) adminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/state", func(w http.ResponseWriter, r *http.Request) { sendJSON(w, 200, s.Snapshot()) })

	// Server-sent snapshots: one on connect, one per burst of changes
	// (debounced), and every 5 seconds as a heartbeat.
	mux.HandleFunc("GET /admin/events", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			sendJSON(w, 500, map[string]any{"error": "streaming unsupported"})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		changed, cancel := s.n.Subscribe()
		defer cancel()
		send := func() bool {
			b, _ := json.Marshal(s.Snapshot())
			if _, err := fmt.Fprintf(w, "event: state\ndata: %s\n\n", b); err != nil {
				return false
			}
			fl.Flush()
			return true
		}
		if !send() {
			return
		}
		tick := time.NewTicker(5 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-changed:
				time.Sleep(100 * time.Millisecond) // coalesce a burst
				select {
				case <-changed:
				default:
				}
			case <-tick.C:
			}
			if !send() {
				return
			}
		}
	})

	mux.HandleFunc("GET /admin/credentials", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("profile")
		p, ok := s.profiles.Get(name)
		if !ok || !slices.Contains(s.profiles.Names(), name) {
			sendJSON(w, 400, map[string]any{"error": fmt.Sprintf("unknown profile '%s'", name)})
			return
		}
		env, err := s.profiles.Read(r.Context(), p)
		if err != nil {
			sendJSON(w, 502, map[string]any{"error": err.Error()})
			return
		}
		sendJSON(w, 200, map[string]any{"profile": name, "credentials": sortedKeys(env)})
	})
	mux.HandleFunc("GET /admin/audit", func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 {
			limit = 100
		}
		limit = min(limit, 500)
		entries := []api.AuditEntry{}
		for _, e := range s.audit.Tail(limit) {
			a := toAPIAudit(e)
			a.Result = nil // output can hold secrets a command printed; the feed doesn't need it
			entries = append(entries, a)
		}
		sendJSON(w, 200, map[string]any{"entries": entries})
	})
	mux.HandleFunc("GET /admin/audit/entry", func(w http.ResponseWriter, r *http.Request) {
		ts := r.URL.Query().Get("ts")
		rid, _ := strconv.Atoi(r.URL.Query().Get("requestId"))
		all := s.audit.Tail(5000)
		for i := len(all) - 1; i >= 0; i-- {
			if all[i].Timestamp == ts && all[i].RequestID == rid {
				sendJSON(w, 200, toAPIAudit(all[i]))
				return
			}
		}
		sendJSON(w, 404, map[string]any{"error": "not in the recent audit log"})
	})
	mux.HandleFunc("POST /admin/decide", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			ID       *int         `json:"id"`
			Decision api.Decision `json:"decision"`
		}
		readJSON(r, &b)
		if b.ID == nil || !slices.Contains([]api.Decision{api.Approve, api.Deny, api.AllowAlways}, b.Decision) {
			sendJSON(w, 400, map[string]any{"error": "need {id, decision}"})
			return
		}
		if !s.approvals.Resolve(*b.ID, b.Decision) {
			sendJSON(w, 409, map[string]any{"error": "already answered or expired"})
			return
		}
		sendJSON(w, 200, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /admin/mode", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Mode    string   `json:"mode"`
			Minutes *float64 `json:"minutes"`
		}
		readJSON(r, &b)
		switch b.Mode {
		case "normal":
			s.modes.Revert()
			s.adminLog(r, "MODE normal", "Bypass/lockdown cleared")
		case "bypass":
			if b.Minutes != nil && *b.Minutes > 0 {
				d := time.Duration(*b.Minutes * float64(time.Minute))
				s.modes.Bypass(&d)
				s.adminLog(r, "MODE bypass", fmt.Sprintf("Global bypass %v minutes", *b.Minutes))
			} else {
				s.modes.Bypass(nil)
				s.adminLog(r, "MODE bypass", "Global bypass indefinitely")
			}
		case "lockdown":
			s.modes.Lockdown()
			s.adminLog(r, "MODE lockdown", fmt.Sprintf("Lockdown; revoked %d bypasses/grants", s.grants.Clear()))
		default:
			sendJSON(w, 400, map[string]any{"error": "mode must be normal | bypass | lockdown"})
			return
		}
		mode, exp := s.modes.Status()
		sendJSON(w, 200, map[string]any{"mode": mode, "expiresAt": exp})
	})
	mux.HandleFunc("POST /admin/settings", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			MobilePush *bool `json:"mobilePush"`
		}
		readJSON(r, &b)
		before := s.settings.Get()
		after := before
		if b.MobilePush != nil {
			after = s.settings.SetMobilePush(*b.MobilePush)
		}
		if after.MobilePush != before.MobilePush {
			state := "off"
			if after.MobilePush {
				state = "on"
			}
			s.adminLog(r, "SETTINGS mobilePush="+state, "Supervisor settings changed")
		}
		sendJSON(w, 200, after)
	})
	mux.HandleFunc("POST /admin/cache/clear", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Name *string `json:"name"`
		}
		readJSON(r, &b)
		name := ""
		if b.Name != nil {
			name = *b.Name
			if !slices.ContainsFunc(s.profiles.Cache(), func(e api.CacheEntry) bool { return e.Name == name }) {
				sendJSON(w, 404, map[string]any{"error": "nothing cached under that name"})
				return
			}
		}
		removed := s.profiles.ClearCache(name)
		if len(removed) > 0 {
			label := name
			if label == "" {
				label = "all"
			}
			s.adminLog(r, "CACHE CLEAR "+label, "Cleared cached credentials: "+strings.Join(removed, ", "))
			s.n.Changed()
		}
		sendJSON(w, 200, map[string]any{"removed": removed})
	})
	cronID := regexp.MustCompile(`^[A-Za-z0-9-]{4,80}$`)
	mux.HandleFunc("POST /admin/crons", func(w http.ResponseWriter, r *http.Request) {
		var b struct{ ID, Name string }
		readJSON(r, &b)
		if !cronID.MatchString(b.ID) {
			sendJSON(w, 400, map[string]any{"error": "need {id, name}"})
			return
		}
		name := b.Name
		if name == "" {
			name = b.ID
		}
		if err := s.crons.Add(b.ID, truncRunes(name, 120)); err != nil {
			sendJSON(w, 500, map[string]any{"error": err.Error()})
			return
		}
		s.adminLog(r, "CRON ALLOW "+b.ID, fmt.Sprintf("Cron %q may auto-approve while running", name))
		s.n.Changed()
		sendJSON(w, 200, map[string]any{"ok": true})
	})
	mux.HandleFunc("DELETE /admin/crons/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		ok, err := s.crons.Remove(id)
		if err != nil || !ok {
			sendJSON(w, 404, map[string]any{"error": "no such cron"})
			return
		}
		s.adminLog(r, "CRON REVOKE "+id, "Cron auto-approval removed")
		s.n.Changed()
		sendJSON(w, 200, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /admin/grants", func(w http.ResponseWriter, r *http.Request) {
		if s.modes.Current() == api.ModeLockdown {
			sendJSON(w, 409, map[string]any{"error": "in lockdown — leave lockdown first"})
			return
		}
		var b adminGrantBody
		if err := readJSON(r, &b); err != nil {
			sendJSON(w, 400, map[string]any{"error": err.Error()})
			return
		}
		in, why := s.parseAdminGrant(b)
		if why != "" {
			sendJSON(w, 400, map[string]any{"error": why})
			return
		}
		g := s.grants.Add(in)
		who := "all"
		if g.Session != "*" {
			who = truncRunes(g.Session, 8)
		}
		scope := fmt.Sprintf("session=%s profile=%s creds=%s", who, g.Profile, listOf(g.Credentials, "any", "none"))
		if g.Target == "mac" {
			scope = "MAC " + scope
		} else {
			scope += " aws=" + listOf(g.Aws, "any", "none")
		}
		until := "until revoked"
		if g.ExpiresAt != nil {
			until = fmt.Sprintf("%dm", (*g.ExpiresAt-time.Now().UnixMilli())/60000)
		}
		s.adminLog(r, fmt.Sprintf("GRANT #%d", g.ID), fmt.Sprintf("Bypass grant %s, %s", scope, until))
		sendJSON(w, 200, g)
	})
	mux.HandleFunc("POST /admin/grant-requests/{id}/approve", func(w http.ResponseWriter, r *http.Request) {
		var b adminGrantBody
		readJSON(r, &b)
		in, why := s.parseAdminGrant(b)
		if why != "" {
			sendJSON(w, 400, map[string]any{"error": why})
			return
		}
		id, _ := strconv.Atoi(r.PathValue("id"))
		if !s.ApproveGrantRequest(id, in) {
			sendJSON(w, 409, map[string]any{"error": "not a pending grant request (already answered or expired?)"})
			return
		}
		s.adminLog(r, fmt.Sprintf("GRANT REQUEST #%d APPROVED", id), "Grant request approved with the scope chosen in the editor")
		sendJSON(w, 200, map[string]any{"ok": true})
	})
	mux.HandleFunc("DELETE /admin/grants/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.Atoi(r.PathValue("id"))
		if !s.grants.Revoke(id) {
			sendJSON(w, 404, map[string]any{"error": "no such grant"})
			return
		}
		s.adminLog(r, fmt.Sprintf("REVOKE GRANT #%d", id), "Bypass grant revoked")
		sendJSON(w, 200, map[string]any{"ok": true})
	})
	sessionID := regexp.MustCompile(`^[A-Za-z0-9-]{1,80}$`)
	mux.HandleFunc("POST /admin/sessions/{id}/unpin", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !sessionID.MatchString(id) || !s.sessions.Unpin(id) {
			sendJSON(w, 404, map[string]any{"error": "no such session"})
			return
		}
		s.adminLog(r, "UNPIN SESSION "+id, "Session key unpinned; next request re-pins")
		sendJSON(w, 200, map[string]any{"ok": true})
	})
	// herdr: Ben's workspaces in Eldar's VM.
	herdrSession := func(v string) string {
		if v == "" {
			return s.herdr.DefaultSession
		}
		return v
	}
	mux.HandleFunc("GET /admin/herdr", func(w http.ResponseWriter, r *http.Request) {
		sendJSON(w, 200, s.herdr.View(r.Context(), herdrSession(r.URL.Query().Get("session")), 0))
	})
	mux.HandleFunc("POST /admin/herdr/launch", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			WorkspaceID, Name, Cwd, Session string
			SkipPermissions             *bool
		}
		readJSON(r, &b)
		in := api.LaunchInput{WorkspaceID: b.WorkspaceID, Name: b.Name, Cwd: b.Cwd, SkipPermissions: b.SkipPermissions == nil || *b.SkipPermissions}
		out, err := s.herdr.Launch(r.Context(), herdrSession(b.Session), in)
		if err != nil {
			sendJSON(w, 400, map[string]any{"error": err.Error()})
			return
		}
		cwd := in.Cwd
		if cwd == "" {
			cwd = defaultCwd
		}
		skip := ""
		if in.SkipPermissions {
			skip = ", skip permissions"
		}
		s.adminLog(r, "HERDR LAUNCH "+in.Name, fmt.Sprintf("Claude session %q started in %s (%s, %s%s)", in.Name, in.WorkspaceID, out["paneId"], cwd, skip))
		sendJSON(w, 200, out)
	})
	mux.HandleFunc("POST /admin/herdr/shell", func(w http.ResponseWriter, r *http.Request) {
		var b struct{ WorkspaceID, Name, Cwd, Session string }
		readJSON(r, &b)
		out, err := s.herdr.OpenShell(r.Context(), herdrSession(b.Session), b.WorkspaceID, b.Name, b.Cwd)
		if err != nil {
			sendJSON(w, 400, map[string]any{"error": err.Error()})
			return
		}
		s.adminLog(r, "HERDR SHELL "+b.Name, fmt.Sprintf("Terminal %q opened in %s (%s)", b.Name, b.WorkspaceID, out["paneId"]))
		sendJSON(w, 200, out)
	})
	mux.HandleFunc("POST /admin/herdr/close", func(w http.ResponseWriter, r *http.Request) {
		var b struct{ PaneID, Session string }
		readJSON(r, &b)
		out, err := s.herdr.Close(r.Context(), herdrSession(b.Session), b.PaneID)
		if err != nil {
			sendJSON(w, 400, map[string]any{"error": err.Error()})
			return
		}
		s.adminLog(r, "HERDR CLOSE "+b.PaneID, fmt.Sprintf("Closed %s %s", out["closed"], out["id"]))
		sendJSON(w, 200, out)
	})
	mux.HandleFunc("GET /admin/terminal", s.handleTerminal)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		paths := make([]string, 0, len(notYet))
		for p := range notYet {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			if strings.HasPrefix(r.URL.Path, p) {
				sendJSON(w, 501, map[string]any{"error": notYet[p] + " isn't available on this supervisor yet"})
				return
			}
		}
		sendJSON(w, 404, map[string]any{"error": "not found"})
	})
	return mux
}

func toAPIAudit(e AuditEntry) api.AuditEntry {
	a := api.AuditEntry{
		Timestamp: e.Timestamp, RequestID: e.RequestID, Action: e.Action, Reason: e.Reason,
		Profile: e.Profile, Decision: e.Decision, DecisionReason: e.DecisionReason, Mode: e.Mode,
		Credentials: e.Credentials, AwsProfile: e.AwsProfile, Cwd: e.Cwd, SessionID: e.SessionID,
		Target: e.Target, DurationMs: e.DurationMs, TimedOut: e.TimedOut, Truncated: e.Truncated,
	}
	if e.Result != nil {
		code := e.Result.ExitCode
		a.ExitCode = &code
		a.Result = &api.AuditResult{ExitCode: code, Stdout: e.Result.Stdout, Stderr: e.Result.Stderr}
	}
	return a
}

func sortStrings(s []string) { sort.Strings(s) }

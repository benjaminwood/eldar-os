// demo-admin serves made-up supervisor state on an admin socket, so the TUI
// and web UI can be shown (scripts/screenshots) without anyone's real data.
// Everything here is invented; times are relative to now so countdowns move.
//
//	demo-admin <socket path>
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/benjaminwood/eldar-os/supervisor/api"
)

// Times are relative to each request, so countdowns never run out.
func ms(d time.Duration) int64 { return time.Now().Add(d).UnixMilli() }
func ptr[T any](v T) *T        { return &v }
func names(n ...string) api.Creds {
	return api.Creds{Names: n}
}

const (
	sEldar   = "5c5990d6-e046-44e0-b4a5-25466ed8b40e"
	sBilling = "1b262412-8554-4305-89aa-020b76e8c838"
	sDocs    = "a94f0faa-a53d-4502-b856-5c4e7b455bab"
	sInfra   = "7d01c2e9-3f4a-4b8e-9a11-2c5e8f0d9b37"
)

func agent(name, status, session string) *api.HerdrAgent {
	return &api.HerdrAgent{Kind: "claude", Name: name, Status: status, SessionID: session, Title: name}
}

func state() api.State {
	pane := func(id, title, cwd, status, session string) api.HerdrPaneView {
		return api.HerdrPaneView{PaneID: id, TerminalID: "term_" + strings.ReplaceAll(id, ":", ""), Cwd: cwd, Title: title,
			Agent: agent(title, status, session)}
	}
	return api.State{
		Now:  time.Now().UnixMilli(),
		Mode: api.ModeNormal,
		Pending: []api.Pending{
			{ID: 41, Kind: "exec", Title: "git push origin fix/billing-retries", Command: "git push origin fix/billing-retries",
				Reason: "Push the retry fix so it can be reviewed", Profile: "work", Credentials: ptr([]string{"GITHUB_TOKEN"}),
				TimeoutSeconds: 300, SessionID: sBilling, SessionVerified: true, Cwd: "/home/eldar/workspace/projects/api",
				Channel: "none", AllowAlways: "git *", CreatedAt: ms(-20 * time.Second), ExpiresAt: ms(160 * time.Second)},
			{ID: 42, Kind: "exec", Target: "mac", Host: "Ben's Mac", Title: "brew upgrade gh", Command: "brew upgrade gh",
				Reason: "gh on the Mac is too old for `gh pr checks --watch`", Credentials: ptr([]string{}), TimeoutSeconds: 300,
				SessionID: sInfra, SessionVerified: true, Cwd: "~", Channel: "none", AllowAlways: "brew *",
				CreatedAt: ms(-8 * time.Second), ExpiresAt: ms(172 * time.Second)},
			{ID: 43, Kind: "exec", Title: "aws logs tail /ecs/api --since 1h --filter-pattern ERROR",
				Command: "aws logs tail /ecs/api --since 1h --filter-pattern ERROR", Reason: "Find the 500s behind the retry bug",
				Profile: "work", Credentials: ptr([]string{}), AwsProfile: "staging.ReadOnly", TimeoutSeconds: 120,
				SessionID: sBilling, SessionVerified: true, Cwd: "/home/eldar/workspace/projects/api", Channel: "none",
				AllowAlways: "aws *", CreatedAt: ms(-3 * time.Second), ExpiresAt: ms(177 * time.Second)},
			{ID: 44, Kind: "browser", Target: "mac", Host: "Ben's Mac", Title: "Control Arc (Personal) on Ben's Mac for 30 minutes",
				Reason: "browser_navigate https://www.macrumors.com", SessionID: sDocs, SessionVerified: true,
				TimeoutSeconds: 1800, ArcProfile: "mac:Profile 6", ArcProfileName: "Personal", Channel: "none",
				CreatedAt: ms(-1 * time.Second), ExpiresAt: ms(179 * time.Second)},
		},
		Sessions: []api.Session{
			{ID: sEldar, FirstSeen: ms(-6 * time.Hour), LastSeen: ms(-2 * time.Minute), Requests: 37, Cwd: "/home/eldar/workspace", HerdrPane: "w1:p2", LastAction: "relay: deploy finished", Pinned: true},
			{ID: sBilling, FirstSeen: ms(-50 * time.Minute), LastSeen: ms(-3 * time.Second), Requests: 12, Cwd: "/home/eldar/workspace/projects/api", HerdrPane: "w1:p3", LastAction: "aws logs tail /ecs/api …", Pinned: true},
			{ID: sDocs, FirstSeen: ms(-20 * time.Minute), LastSeen: ms(-1 * time.Second), Requests: 3, Cwd: "/home/eldar/workspace/projects/docs", HerdrPane: "w1:p4", LastAction: "browser: Arc (Personal)", Pinned: true},
			{ID: sInfra, FirstSeen: ms(-90 * time.Minute), LastSeen: ms(-8 * time.Second), Requests: 21, Cwd: "/home/eldar/workspace/projects/eldar-os-staging", HerdrPane: "w2:p1", LastAction: "mac: brew upgrade gh", Pinned: true},
		},
		HerdrPanes: []api.HerdrPane{
			{PaneID: "w1:p2", Title: "Eldar", Cwd: "/home/eldar/workspace", SessionID: sEldar, Status: "idle"},
			{PaneID: "w1:p3", Title: "billing-retries", Cwd: "/home/eldar/workspace/projects/api", SessionID: sBilling, Status: "working", Focused: true},
			{PaneID: "w1:p4", Title: "docs-cleanup", Cwd: "/home/eldar/workspace/projects/docs", SessionID: sDocs, Status: "blocked"},
			{PaneID: "w2:p1", Title: "eldar-os", Cwd: "/home/eldar/workspace/projects/eldar-os-staging", SessionID: sInfra, Status: "blocked"},
		},
		Grants: []api.Grant{
			{ID: 7, Target: "eldar", Session: sBilling, Profile: "work", Credentials: names("NPM_TOKEN"), Aws: names(),
				CreatedAt: ms(-30 * time.Minute), ExpiresAt: ptr(ms(25 * time.Minute)), Note: "publish the client", Uses: 4},
			{ID: 8, Target: "mac", Session: "*", Profile: "*", Credentials: names(), Aws: names(),
				CreatedAt: ms(-5 * time.Minute), ExpiresAt: ptr(ms(55 * time.Minute)), Note: "Ben's Mac: read-only checks", Uses: 2},
		},
		Profiles:    []string{"work", "personal"},
		Cache:       []api.CacheEntry{{Name: "work", Kind: "1password", CachedAt: ms(-42 * time.Minute)}},
		AwsProfiles: []string{"staging.ReadOnly", "staging.Admin", "prod.ReadOnly"},
		ArcProfiles: []api.ArcProfile{{ID: "mac:Default", Name: "Work", Ready: true}, {ID: "mac:Profile 6", Name: "Personal", Ready: true}},
		Chat: api.ChatSummary{LastID: 6, ReadUpTo: 6, Connected: true,
			Last:  &api.ChatMessage{ID: 6, From: "eldar", Text: chat[5].Text, TS: chat[5].TS},
			Eldar: &api.ChatEldar{SessionID: sEldar, PaneID: "w1:p2", Status: "idle"}},
		Settings: api.Settings{MobilePush: true},
		MCP:      api.MCPSummary{Servers: []api.MCPServer{}, Pending: []api.MCPSignIn{}},
		Herdr: api.HerdrView{Session: "default", OK: true, Spaces: []api.HerdrSpace{
			{WorkspaceID: "w1", Label: "primary", Focused: true, Status: "working", Cwds: []string{"/home/eldar/workspace"}, Tabs: []api.HerdrTab{
				{TabID: "w1:t2", Label: "Eldar", Status: "idle", Panes: []api.HerdrPaneView{pane("w1:p2", "Eldar", "/home/eldar/workspace", "idle", sEldar)}},
				{TabID: "w1:t3", Label: "billing-retries", Focused: true, Status: "working", Panes: []api.HerdrPaneView{pane("w1:p3", "billing-retries", "/home/eldar/workspace/projects/api", "working", sBilling)}},
				{TabID: "w1:t4", Label: "docs-cleanup", Status: "blocked", Panes: []api.HerdrPaneView{pane("w1:p4", "docs-cleanup", "/home/eldar/workspace/projects/docs", "blocked", sDocs)}},
			}},
			{WorkspaceID: "w2", Label: "infra", Status: "blocked", Cwds: []string{"/home/eldar/workspace/projects/eldar-os-staging"}, Tabs: []api.HerdrTab{
				{TabID: "w2:t1", Label: "eldar-os", Status: "blocked", Panes: []api.HerdrPaneView{pane("w2:p1", "eldar-os", "/home/eldar/workspace/projects/eldar-os-staging", "blocked", sInfra)}},
				{TabID: "w2:t2", Label: "shell", Panes: []api.HerdrPaneView{{PaneID: "w2:p2", TerminalID: "term_w2p2", Cwd: "/home/eldar", Title: "zsh"}}},
			}},
		}},
		Mac: api.MacStatus{Configured: true, Host: "203.0.113.10", Allow: []string{"brew list*", "git status*"},
			LastTest: &api.MacTest{OK: true, At: ms(-15 * time.Minute), Detail: "MacBook Pro · macOS 26.5"}},
		Hosts: []api.HostStatus{{Name: "mac", Label: "Ben's Mac", Description: "Ben's MacBook Pro (macOS, zsh, Homebrew).",
			Configured: true, Host: "203.0.113.10", Allow: []string{"brew list*", "git status*"},
			LastTest: &api.MacTest{OK: true, At: ms(-15 * time.Minute), Detail: "MacBook Pro · macOS 26.5"}}},
		BrowserGrants: []api.BrowserGrant{{ID: 3, SessionID: sEldar, Client: "Eldar · workspace", Profile: "mac:Default",
			ProfileName: "Work", Host: "mac", Space: "Work", CreatedAt: ms(-4 * time.Minute), ExpiresAt: ms(26 * time.Minute), Channels: 1}},
		Crons:   []api.Cron{},
		Viewers: 1,
	}
}

var chat = []api.ChatMessage{
	{ID: 1, From: "ben", Text: "Morning. Anything from overnight?", TS: ms(-95 * time.Minute)},
	{ID: 2, From: "eldar", Text: "Morning! Two things:\n\n- **The nightly deploy finished green.** 3 PRs went out.\n- **Billing retries are failing** on ~2% of renewals since yesterday. I started a session (`billing-retries`) to dig in.", TS: ms(-94 * time.Minute)},
	{ID: 3, From: "ben", Text: "Nice. Have it open a PR when it has a fix, I'll review from my phone.", TS: ms(-60 * time.Minute)},
	{ID: 4, From: "eldar", Text: "Will do. It's asking for staging logs now (read-only) — approve #43 if that looks right.", TS: ms(-2 * time.Minute)},
	{ID: 5, From: "ben", Text: "Approved. Also, can you add `yq` to your VM?", TS: ms(-90 * time.Second)},
	{ID: 6, From: "eldar", Text: "Opened **PR #12: Eldar VM: add yq**. Merge it and it's live in about 30 seconds; I'll confirm here when it is.", TS: ms(-60 * time.Second)},
}

func audit() []api.AuditEntry {
	ts := func(d time.Duration) string { return time.Now().Add(d).UTC().Format(time.RFC3339Nano) }
	exit := func(n int) *int { return &n }
	dur := func(n int64) *int64 { return &n }
	creds := func(n ...string) json.RawMessage { b, _ := json.Marshal(n); return b }
	return []api.AuditEntry{
		{Timestamp: ts(-50 * time.Minute), RequestID: 31, Action: "gh pr list --state open", Reason: "Find the open billing PRs", Profile: "work", Decision: "allow", DecisionReason: "Allowed by policy rule", Mode: "normal", ExitCode: exit(0), Credentials: creds("GITHUB_TOKEN"), SessionID: sBilling, Target: "eldar", DurationMs: dur(1840)},
		{Timestamp: ts(-41 * time.Minute), RequestID: 32, Action: "npm publish --tag next", Reason: "Publish the client with the retry fix", Profile: "work", Decision: "allow", DecisionReason: "Bypass grant #7 (session 1b262412)", Mode: "normal", ExitCode: exit(0), Credentials: creds("NPM_TOKEN"), SessionID: sBilling, Target: "eldar", DurationMs: dur(9120)},
		{Timestamp: ts(-30 * time.Minute), RequestID: 33, Action: "mac: brew list --versions gh", Reason: "Check gh on the Mac", Decision: "allow", DecisionReason: "Ben's Mac allow list: brew list*", Mode: "normal", ExitCode: exit(0), SessionID: sInfra, Target: "mac", DurationMs: dur(650)},
		{Timestamp: ts(-22 * time.Minute), RequestID: 34, Action: "kubectl delete namespace billing", Reason: "Clean up", Profile: "work", Decision: "deny", DecisionReason: "Denied by policy rule", Mode: "normal", SessionID: sBilling, Target: "eldar"},
		{Timestamp: ts(-12 * time.Minute), RequestID: 35, Action: "BROWSER WINDOW #3: Arc (Work) on Ben's Mac", Reason: "Check the staging dashboard", Decision: "allow", DecisionReason: "Approved by Ben; 30m for Eldar · workspace", Mode: "normal", SessionID: sEldar, Target: "browser"},
		{Timestamp: ts(-6 * time.Minute), RequestID: 36, Action: "git push origin docs/readme", Reason: "Push the README draft", Profile: "work", Decision: "denied", DecisionReason: "Denied by Ben", Mode: "normal", Credentials: creds("GITHUB_TOKEN"), SessionID: sDocs, Target: "eldar"},
		{Timestamp: ts(-2 * time.Minute), RequestID: 37, Action: "PR watch benjaminwood/eldar-os-staging#12: [eldar-os] PR #12 (Eldar VM: add yq) is merged and applied", Decision: "allow", DecisionReason: "told the session in w2:p1", Mode: "normal", SessionID: sInfra},
	}
}

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: demo-admin <socket path>")
	}
	os.Remove(os.Args[1])
	ln, err := net.Listen("unix", os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("demo admin API on %s", os.Args[1])
	log.Fatal(http.Serve(ln, handler()))
}

// handler answers the admin API's read endpoints with the demo state; every
// write succeeds and changes nothing.
func handler() http.Handler {
	send := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/state", func(w http.ResponseWriter, r *http.Request) { send(w, state()) })
	mux.HandleFunc("GET /admin/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for {
			b, _ := json.Marshal(state())
			if _, err := fmt.Fprintf(w, "event: state\ndata: %s\n\n", b); err != nil {
				return
			}
			fl.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
	})
	mux.HandleFunc("GET /admin/chat", func(w http.ResponseWriter, r *http.Request) {
		send(w, map[string]any{"messages": chat, "readUpTo": 6, "connected": true, "eldar": state().Chat.Eldar})
	})
	mux.HandleFunc("GET /admin/audit", func(w http.ResponseWriter, r *http.Request) { send(w, map[string]any{"entries": audit()}) })
	mux.HandleFunc("GET /admin/herdr", func(w http.ResponseWriter, r *http.Request) { send(w, state().Herdr) })
	mux.HandleFunc("GET /admin/aws", func(w http.ResponseWriter, r *http.Request) {
		send(w, map[string]any{"sessions": []any{map[string]any{"session": "corp", "signedIn": true}}, "profiles": state().AwsProfiles})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { send(w, map[string]any{"ok": true}) })
	return mux
}

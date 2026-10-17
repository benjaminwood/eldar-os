package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/benjaminwood/eldar-os/supervisor/api"
)

// Herdr reads and drives herdr in Eldar's VM (as the agent user, who runs the
// herdr server) for the admin UIs: Ben's workspaces, starting Claude sessions
// and shells, closing them. Only reachable through the admin socket. Commands
// name explicit IDs and pass --no-focus, so they never move whatever a desk
// client is looking at.
type Herdr struct {
	runner         Runner
	user           string
	DefaultSession string
	n              *Notifier

	mu    sync.Mutex
	cache map[string]herdrCached
}

type herdrCached struct {
	at   time.Time
	view api.HerdrView
}

func NewHerdr(r Runner, user, defaultSession string, n *Notifier) *Herdr {
	return &Herdr{runner: r, user: user, DefaultSession: defaultSession, n: n, cache: map[string]herdrCached{}}
}

var (
	validHerdrSession = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	validWorkspace    = regexp.MustCompile(`^w\d+$`)
	validTab          = regexp.MustCompile(`^w\d+:t[A-Za-z0-9]+$`)
	validPane         = regexp.MustCompile(`^w\d+:p[A-Za-z0-9]+$`)
	validTerminal     = regexp.MustCompile(`^term_[A-Za-z0-9]+$`)
)

func sessionArgs(session string) ([]string, error) {
	if !validHerdrSession.MatchString(session) {
		return nil, errors.New("invalid herdr session name")
	}
	if session == "default" {
		return nil, nil
	}
	return []string{"--session", session}, nil
}

// run runs a herdr command and returns its JSON `result`.
func (h *Herdr) run(ctx context.Context, session string, timeout time.Duration, args ...string) (json.RawMessage, error) {
	sa, err := sessionArgs(session)
	if err != nil {
		return nil, err
	}
	res, err := h.runner.Exec(ctx, ExecSpec{Argv: append(append([]string{"herdr"}, sa...), args...), User: h.user, Timeout: timeout, MaxOutput: 8 << 20})
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		msg := strings.TrimSpace(res.Stderr)
		if msg == "" {
			msg = strings.TrimSpace(res.Stdout)
		}
		var j struct {
			Message string `json:"message"`
			Error   struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(msg), &j) == nil {
			if j.Error.Message != "" {
				msg = j.Error.Message
			} else if j.Message != "" {
				msg = j.Message
			}
		}
		if msg == "" {
			msg = fmt.Sprintf("herdr exited %d", res.ExitCode)
		}
		return nil, errors.New(truncRunes(msg, 400))
	}
	var out struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil {
		return nil, fmt.Errorf("herdr: unexpected output: %w", err)
	}
	return out.Result, nil
}

func (h *Herdr) Invalidate() {
	h.mu.Lock()
	clear(h.cache)
	h.mu.Unlock()
	h.n.Changed()
}

type herdrSnapshot struct {
	Snapshot struct {
		Workspaces []struct {
			ID, Label, Status string
			Focused           bool
		} `json:"workspaces"`
		Tabs []struct {
			ID, Workspace, Label, Status string
			Focused                      bool
		} `json:"tabs"`
		Panes []struct {
			ID, Tab, Workspace, Terminal, FgCwd, Cwd, Title string
			Focused                                         bool
		} `json:"panes"`
		Agents []struct {
			Pane, Agent, Name, Status, Title string
			Session                          *struct{ Value string } `json:"agent_session"`
		} `json:"agents"`
	} `json:"snapshot"`
}

// herdr's JSON uses snake_case; map it explicitly.
func (s *herdrSnapshot) UnmarshalJSON(b []byte) error {
	var raw struct {
		Snapshot struct {
			Workspaces []map[string]any `json:"workspaces"`
			Tabs       []map[string]any `json:"tabs"`
			Panes      []map[string]any `json:"panes"`
			Agents     []map[string]any `json:"agents"`
		} `json:"snapshot"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	str := func(m map[string]any, k string) string { v, _ := m[k].(string); return v }
	bl := func(m map[string]any, k string) bool { v, _ := m[k].(bool); return v }
	for _, w := range raw.Snapshot.Workspaces {
		s.Snapshot.Workspaces = append(s.Snapshot.Workspaces, struct {
			ID, Label, Status string
			Focused           bool
		}{str(w, "workspace_id"), str(w, "label"), str(w, "agent_status"), bl(w, "focused")})
	}
	for _, t := range raw.Snapshot.Tabs {
		s.Snapshot.Tabs = append(s.Snapshot.Tabs, struct {
			ID, Workspace, Label, Status string
			Focused                      bool
		}{str(t, "tab_id"), str(t, "workspace_id"), str(t, "label"), str(t, "agent_status"), bl(t, "focused")})
	}
	for _, p := range raw.Snapshot.Panes {
		s.Snapshot.Panes = append(s.Snapshot.Panes, struct {
			ID, Tab, Workspace, Terminal, FgCwd, Cwd, Title string
			Focused                                         bool
		}{str(p, "pane_id"), str(p, "tab_id"), str(p, "workspace_id"), str(p, "terminal_id"), str(p, "foreground_cwd"), str(p, "cwd"), str(p, "terminal_title_stripped"), bl(p, "focused")})
	}
	for _, a := range raw.Snapshot.Agents {
		ag := struct {
			Pane, Agent, Name, Status, Title string
			Session                          *struct{ Value string } `json:"agent_session"`
		}{Pane: str(a, "pane_id"), Agent: str(a, "agent"), Name: str(a, "name"), Status: str(a, "agent_status"), Title: str(a, "terminal_title_stripped")}
		if sess, ok := a["agent_session"].(map[string]any); ok {
			ag.Session = &struct{ Value string }{str(sess, "value")}
		}
		s.Snapshot.Agents = append(s.Snapshot.Agents, ag)
	}
	return nil
}

// View returns workspaces → tabs → panes with the agent in each, cached for
// maxAge. On failure it keeps the last known spaces and reports the error.
func (h *Herdr) View(ctx context.Context, session string, maxAge time.Duration) api.HerdrView {
	if session == "" {
		session = h.DefaultSession
	}
	h.mu.Lock()
	hit, ok := h.cache[session]
	h.mu.Unlock()
	if ok && time.Since(hit.at) < maxAge {
		return hit.view
	}
	view := api.HerdrView{Session: session, OK: true, Spaces: []api.HerdrSpace{}}
	raw, err := h.run(ctx, session, 10*time.Second, "api", "snapshot")
	var s herdrSnapshot
	if err == nil {
		err = json.Unmarshal(raw, &s)
	}
	if err != nil {
		view.OK, view.Error = false, err.Error()
		if ok {
			view.Spaces = hit.view.Spaces
		}
	} else {
		agents := map[string]*api.HerdrAgent{}
		for _, a := range s.Snapshot.Agents {
			ag := &api.HerdrAgent{Kind: a.Agent, Name: a.Name, Status: a.Status, Title: a.Title}
			if a.Session != nil {
				ag.SessionID = a.Session.Value
			}
			agents[a.Pane] = ag
		}
		panes := map[string][]api.HerdrPaneView{}
		cwds := map[string]map[string]int{}
		for _, p := range s.Snapshot.Panes {
			cwd := p.FgCwd
			if cwd == "" {
				cwd = p.Cwd
			}
			panes[p.Tab] = append(panes[p.Tab], api.HerdrPaneView{PaneID: p.ID, TerminalID: p.Terminal, Cwd: cwd, Title: p.Title, Focused: p.Focused, Agent: agents[p.ID]})
			if cwd != "" {
				if cwds[p.Workspace] == nil {
					cwds[p.Workspace] = map[string]int{}
				}
				cwds[p.Workspace][cwd]++
			}
		}
		tabs := map[string][]api.HerdrTab{}
		for _, t := range s.Snapshot.Tabs {
			ps := panes[t.ID]
			if ps == nil {
				ps = []api.HerdrPaneView{}
			}
			tabs[t.Workspace] = append(tabs[t.Workspace], api.HerdrTab{TabID: t.ID, Label: t.Label, Focused: t.Focused, Status: t.Status, Panes: ps})
		}
		for _, w := range s.Snapshot.Workspaces {
			ts := tabs[w.ID]
			if ts == nil {
				ts = []api.HerdrTab{}
			}
			view.Spaces = append(view.Spaces, api.HerdrSpace{WorkspaceID: w.ID, Label: w.Label, Focused: w.Focused, Status: w.Status, Tabs: ts, Cwds: byCount(cwds[w.ID])})
		}
	}
	h.mu.Lock()
	h.cache[session] = herdrCached{at: time.Now(), view: view}
	h.mu.Unlock()
	return view
}

func byCount(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.SliceStable(out, func(i, j int) bool { return m[out[i]] > m[out[j]] })
	return out
}

// ClaudePanes are the Claude sessions open in herdr.
func ClaudePanes(v api.HerdrView) []api.HerdrPane {
	out := []api.HerdrPane{}
	for _, sp := range v.Spaces {
		for _, t := range sp.Tabs {
			for _, p := range t.Panes {
				if p.Agent == nil || p.Agent.Kind != "claude" {
					continue
				}
				title := p.Agent.Title
				if title == "" {
					title = p.Title
				}
				out = append(out, api.HerdrPane{PaneID: p.PaneID, Title: title, Cwd: p.Cwd, SessionID: p.Agent.SessionID, Status: p.Agent.Status, Focused: p.Focused})
			}
		}
	}
	return out
}

var slugBad = regexp.MustCompile(`[^a-z0-9_-]+`)

// agentSlug: herdr agent names are [a-z][a-z0-9_-]{0,31}, unique among live agents.
func agentSlug(name string, taken map[string]bool) string {
	base := slugBad.ReplaceAllString(strings.ToLower(name), "-")
	base = strings.TrimLeftFunc(base, func(r rune) bool { return r < 'a' || r > 'z' })
	base = strings.TrimRight(base, "-")
	if len(base) > 28 {
		base = base[:28]
	}
	if base == "" {
		base = "claude"
	}
	slug := base
	for i := 2; taken[slug]; i++ {
		slug = fmt.Sprintf("%s-%d", base, i)
	}
	return slug
}

func cleanName(name string) (string, error) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" || len([]rune(name)) > 80 || strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 }) {
		return "", errors.New("name must be 1–80 printable characters")
	}
	return name, nil
}

func cleanCwd(cwd, def string) (string, error) {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		cwd = def
	}
	if !strings.HasPrefix(cwd, "/") || strings.ContainsRune(cwd, 0) {
		return "", errors.New("cwd must be an absolute path")
	}
	return cwd, nil
}

const defaultCwd = "/home/eldar/workspace"

func (h *Herdr) newTab(ctx context.Context, session, workspace, name, cwd string) (tabID, paneID string, err error) {
	raw, err := h.run(ctx, session, 20*time.Second, "tab", "create", "--workspace", workspace, "--cwd", cwd, "--label", name, "--no-focus")
	if err != nil {
		return "", "", err
	}
	var out struct {
		Tab struct {
			TabID string `json:"tab_id"`
		} `json:"tab"`
		RootPane struct {
			PaneID string `json:"pane_id"`
		} `json:"root_pane"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", "", err
	}
	return out.Tab.TabID, out.RootPane.PaneID, nil
}

// Launch opens a tab and starts Claude in it, named. Sends no input: typing
// into a session is what the passkey-gated terminal is for, so launching can't
// be used to slip a prompt in.
func (h *Herdr) Launch(ctx context.Context, session string, in api.LaunchInput) (map[string]string, error) {
	if !validWorkspace.MatchString(in.WorkspaceID) {
		return nil, errors.New("invalid workspace")
	}
	name, err := cleanName(in.Name)
	if err != nil {
		return nil, err
	}
	cwd, err := cleanCwd(in.Cwd, defaultCwd)
	if err != nil {
		return nil, err
	}
	taken := map[string]bool{}
	for _, sp := range h.View(ctx, session, 0).Spaces {
		for _, t := range sp.Tabs {
			for _, p := range t.Panes {
				if p.Agent != nil && p.Agent.Name != "" {
					taken[p.Agent.Name] = true
				}
			}
		}
	}
	slug := agentSlug(name, taken)
	tabID, paneID, err := h.newTab(ctx, session, in.WorkspaceID, name, cwd)
	if err != nil {
		return nil, err
	}
	h.Invalidate()
	args := []string{"agent", "start", slug, "--kind", "claude", "--pane", paneID, "--timeout", "90000", "--"}
	if in.SkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	args = append(args, "-n", name)
	time.Sleep(800 * time.Millisecond) // the new shell needs to reach its prompt
	_, err = h.run(ctx, session, 120*time.Second, args...)
	h.Invalidate()
	if err != nil {
		return nil, fmt.Errorf("tab %s opened, but Claude didn't start: %w", tabID, err)
	}
	return map[string]string{"tabId": tabID, "paneId": paneID, "agent": slug}, nil
}

// OpenShell opens a labelled tab with a plain shell.
func (h *Herdr) OpenShell(ctx context.Context, session, workspace, name, cwd string) (map[string]string, error) {
	if !validWorkspace.MatchString(workspace) {
		return nil, errors.New("invalid workspace")
	}
	name, err := cleanName(name)
	if err != nil {
		return nil, err
	}
	if cwd, err = cleanCwd(cwd, defaultCwd); err != nil {
		return nil, err
	}
	tabID, paneID, err := h.newTab(ctx, session, workspace, name, cwd)
	h.Invalidate()
	if err != nil {
		return nil, err
	}
	return map[string]string{"tabId": tabID, "paneId": paneID}, nil
}

// Close closes a pane: its whole tab when it's the tab's only pane.
func (h *Herdr) Close(ctx context.Context, session, paneID string) (map[string]string, error) {
	if !validPane.MatchString(paneID) {
		return nil, errors.New("invalid pane")
	}
	for _, sp := range h.View(ctx, session, 0).Spaces {
		for _, t := range sp.Tabs {
			for _, p := range t.Panes {
				if p.PaneID != paneID {
					continue
				}
				var err error
				out := map[string]string{"closed": "pane", "id": paneID}
				if len(t.Panes) == 1 && validTab.MatchString(t.TabID) {
					_, err = h.run(ctx, session, 20*time.Second, "tab", "close", t.TabID)
					out = map[string]string{"closed": "tab", "id": t.TabID}
				} else {
					_, err = h.run(ctx, session, 20*time.Second, "pane", "close", paneID)
				}
				h.Invalidate()
				if err != nil {
					return nil, err
				}
				return out, nil
			}
		}
	}
	return nil, errors.New("no such pane")
}

// paneTerminal finds a pane's terminal ID and a label for it.
func (h *Herdr) paneTerminal(ctx context.Context, session, paneID string) (terminal, label string, err error) {
	for _, sp := range h.View(ctx, session, 0).Spaces {
		for _, t := range sp.Tabs {
			for _, p := range t.Panes {
				if p.PaneID == paneID {
					if !validTerminal.MatchString(p.TerminalID) {
						return "", "", errors.New("that pane has no terminal to attach to")
					}
					return p.TerminalID, sp.Label + " / " + t.Label, nil
				}
			}
		}
	}
	return "", "", errors.New("no such pane")
}

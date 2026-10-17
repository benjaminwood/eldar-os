package server

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/benjaminwood/eldar-os/supervisor/api"
	"github.com/coder/websocket"
)

// Browsers on remote hosts: agents drive a real, logged-in browser profile
// (Arc on Ben's Mac first) through Playwright MCP running on that host, one
// approved window per session, each window bound to one profile.
//
// The host runs a browser bridge (github.com/benjaminwood/arc-playwright-bridge
// for Arc), which the supervisor drives over SSH with the host's key:
//
//	<bridge> profiles                                     JSON profiles + readiness
//	<bridge> mcp '<profile>' <space|-> --blocked-origins  Playwright MCP on stdio
//	<bridge> new-tab '<profile>' <space> <base64url url>  open a tab in a space
//
// A session's browser MCP server (`supervisor browser`, in the VM) makes a
// random secret, holds it in memory only, and presents it to ask
// (POST /browser/request, which puts an approval in front of Ben) and then to
// open its channel (GET /browser/channel, a WebSocket the supervisor pipes to
// the bridge's stdio over SSH). Only the secret's hash is kept here. When the
// window ends (expiry, revoke, lockdown) the channel closes and Playwright
// exits; the next browser call asks again.
//
// Invariants: every window is an explicit decision by Ben (an approval, or a
// browser grant he made for that session and profile); global bypass, eldar
// and host grants and allow lists never cover it. A window's channel always
// opens the approved profile. browser_run_code_unsafe is withheld (it could
// lift the blocked origins). While a host has an open window, the web UI
// refuses that host's tailnet device, so a driven page can't approve anything.

// HostBrowser is a host's browser, from hosts.json.
type HostBrowser struct {
	Name          string   `json:"name"`          // e.g. "Arc"
	Bridge        string   `json:"bridge"`        // the bridge command on the host
	HiddenOrigins []string `json:"hiddenOrigins"` // never driven or opened (approval pages, tailnet admin)
	Device        string   `json:"device"`        // the host's tailnet machine name, refused by the web UI during a window
}

var (
	validBrowserProfile = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,63}$`)
	validBrowserSpace   = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	validSecret         = regexp.MustCompile(`^[A-Za-z0-9_-]{32,128}$`)
	withheldTools       = []string{"browser_run_code_unsafe"}
)

const maxBrowserMinutes = 480

// BrowserSpace is a section of a profile (Arc's spaces).
type BrowserSpace struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// BrowserProfile is one profile as the bridge reports it (never a token).
type BrowserProfile struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Spaces    []string       `json:"spaces"`
	SpaceIDs  []BrowserSpace `json:"spaceIds"`
	OpensIn   string         `json:"opensIn"`
	Extension string         `json:"extension"`
	Token     bool           `json:"token"`
	Ready     bool           `json:"ready"`
}

func (p BrowserProfile) space(id string) *BrowserSpace {
	for _, sp := range p.SpaceIDs {
		if strings.EqualFold(sp.ID, id) {
			return &sp
		}
	}
	return nil
}

// browserKey is how a host's profile is named in grants and approvals.
func browserKey(host, profile string) string { return host + ":" + profile }

type browserWindow struct {
	id                   int
	hash                 string
	host                 string
	sessionID, client    string
	profile, profileName string
	space                *BrowserSpace
	created, expires     time.Time
	sockets              map[*websocket.Conn]struct{}
}

type cached[T any] struct {
	at  time.Time
	val T
}

// Browsers holds open windows and per-host caches.
type Browsers struct {
	mu       sync.Mutex
	windows  map[string]*browserWindow // secret hash → window
	next     int
	tools    map[string]cached[[]json.RawMessage]
	profiles map[string]cached[[]BrowserProfile]
}

func NewBrowsers() *Browsers {
	return &Browsers{windows: map[string]*browserWindow{}, tools: map[string]cached[[]json.RawMessage]{}, profiles: map[string]cached[[]BrowserProfile]{}}
}

func secretHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// browserHost: a configured host with a browser.
func (s *Supervisor) browserHost(name string) (*RemoteHost, error) {
	h := s.host(name)
	if h == nil || h.Browser == nil {
		var with []string
		for _, n := range s.hostNames() {
			if s.host(n).Browser != nil {
				with = append(with, n)
			}
		}
		return nil, fmt.Errorf("no browser on host %q (hosts with one: %s)", name, strings.Join(with, ", "))
	}
	if !h.Configured() {
		return nil, fmt.Errorf("%s isn't reachable from this supervisor (no key)", h.Label)
	}
	return h, nil
}

// remote runs a command on the host over SSH (the bridge's commands).
func (h *RemoteHost) remote(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "ssh",
		"-i", h.KeyPath, "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
		"-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile="+h.KnownHosts,
		"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=4", "-o", "LogLevel=ERROR",
		h.Target, command)
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return truncRunes(s, 200)
}

// browserProfiles: the host's profiles, cached for maxAge.
func (s *Supervisor) browserProfiles(ctx context.Context, h *RemoteHost, maxAge time.Duration) ([]BrowserProfile, error) {
	b := s.browsers
	b.mu.Lock()
	c, ok := b.profiles[h.Name]
	b.mu.Unlock()
	if ok && time.Since(c.at) < maxAge {
		return c.val, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := h.remote(ctx, h.Browser.Bridge+" profiles")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("listing %s profiles on %s failed: %v %s", h.Browser.Name, h.Label, err, lastLine(stderr.String()))
	}
	var r struct {
		Profiles []BrowserProfile `json:"profiles"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return nil, fmt.Errorf("%s's bridge answered non-JSON: %v", h.Label, err)
	}
	r.Profiles = slices.DeleteFunc(r.Profiles, func(p BrowserProfile) bool { return !validBrowserProfile.MatchString(p.ID) })
	b.mu.Lock()
	b.profiles[h.Name] = cached[[]BrowserProfile]{time.Now(), r.Profiles}
	b.mu.Unlock()
	return r.Profiles, nil
}

// knownBrowserProfiles: every host's last profile list (no SSH), for the grant editors.
func (s *Supervisor) knownBrowserProfiles() []api.ArcProfile {
	b := s.browsers
	b.mu.Lock()
	defer b.mu.Unlock()
	out := []api.ArcProfile{}
	multi := len(b.profiles) > 1
	for _, host := range slices.Sorted(func(yield func(string) bool) {
		for k := range b.profiles {
			if !yield(k) {
				return
			}
		}
	}) {
		for _, p := range b.profiles[host].val {
			name := p.Name
			if multi {
				name += " (" + s.host(host).Label + ")"
			}
			out = append(out, api.ArcProfile{ID: browserKey(host, p.ID), Name: name, Ready: p.Ready})
		}
	}
	return out
}

// browserTools: Playwright MCP's tool list from the host (no browser contact),
// minus withheld tools; cached 6h.
func (s *Supervisor) browserTools(ctx context.Context, h *RemoteHost) ([]json.RawMessage, error) {
	b := s.browsers
	b.mu.Lock()
	c, ok := b.tools[h.Name]
	b.mu.Unlock()
	if ok && time.Since(c.at) < 6*time.Hour {
		return c.val, nil
	}
	profiles, err := s.browserProfiles(ctx, h, 30*time.Second)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(profiles, func(p BrowserProfile) bool { return p.Ready })
	if i < 0 {
		return nil, fmt.Errorf("no %s profile on %s is set up for agents yet", h.Browser.Name, h.Label)
	}
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	cmd := h.remote(ctx, fmt.Sprintf("%s mcp %s -", h.Browser.Bridge, shellQuote(profiles[i].ID)))
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() { stdin.Close(); cmd.Process.Kill(); cmd.Wait() }()
	for _, m := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"supervisor","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	} {
		io.WriteString(stdin, m+"\n")
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 8<<20)
	for sc.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Result struct {
				Tools []json.RawMessage `json:"tools"`
			} `json:"result"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil || string(m.ID) != "2" {
			continue
		}
		tools := slices.DeleteFunc(m.Result.Tools, func(t json.RawMessage) bool {
			var n struct{ Name string }
			json.Unmarshal(t, &n)
			return slices.Contains(withheldTools, n.Name)
		})
		b.mu.Lock()
		b.tools[h.Name] = cached[[]json.RawMessage]{time.Now(), tools}
		b.mu.Unlock()
		return tools, nil
	}
	return nil, fmt.Errorf("Playwright MCP on %s didn't list its tools", h.Label)
}

// window: the live window for a secret, if any.
func (s *Supervisor) window(secret string) *browserWindow {
	b := s.browsers
	b.mu.Lock()
	defer b.mu.Unlock()
	w := b.windows[secretHash(secret)]
	if w == nil || time.Now().After(w.expires) {
		return nil
	}
	return w
}

func (s *Supervisor) browserLog(w *browserWindow, action, why string) {
	s.audit.Log(AuditEntry{RequestID: s.nextID(), Action: action, Decision: "allow", DecisionReason: why,
		SessionID: w.sessionID, Target: "browser", Mode: string(s.modes.Current())})
}

// endWindow closes a window's channels (4001) and forgets it.
func (s *Supervisor) endWindow(w *browserWindow, why string) {
	b := s.browsers
	b.mu.Lock()
	if b.windows[w.hash] != w {
		b.mu.Unlock()
		return
	}
	delete(b.windows, w.hash)
	socks := make([]*websocket.Conn, 0, len(w.sockets))
	for c := range w.sockets {
		socks = append(socks, c)
	}
	b.mu.Unlock()
	for _, c := range socks {
		c.Close(4001, why)
	}
	s.browserLog(w, fmt.Sprintf("BROWSER END #%d", w.id), fmt.Sprintf("%s window on %s ended: %s", w.profileName, w.host, why))
	s.n.Changed()
}

// endWindows ends every window matching f; returns how many.
func (s *Supervisor) endWindows(why string, f func(*browserWindow) bool) int {
	b := s.browsers
	b.mu.Lock()
	var ws []*browserWindow
	for _, w := range b.windows {
		if f(w) {
			ws = append(ws, w)
		}
	}
	b.mu.Unlock()
	for _, w := range ws {
		s.endWindow(w, why)
	}
	return len(ws)
}

// sweepBrowsers ends expired windows every 5s.
func (s *Supervisor) sweepBrowsers(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			now := time.Now()
			s.endWindows("time window ended", func(w *browserWindow) bool { return now.After(w.expires) })
		}
	}
}

func (s *Supervisor) browserGrants() []api.BrowserGrant {
	b := s.browsers
	b.mu.Lock()
	defer b.mu.Unlock()
	out := []api.BrowserGrant{}
	for _, w := range b.windows {
		g := api.BrowserGrant{ID: w.id, SessionID: w.sessionID, Client: w.client, Profile: browserKey(w.host, w.profile),
			ProfileName: w.profileName, Host: w.host, CreatedAt: w.created.UnixMilli(), ExpiresAt: w.expires.UnixMilli(), Channels: len(w.sockets)}
		if h := s.host(w.host); h != nil && h.Browser != nil {
			g.Device = h.Browser.Device
		}
		if w.space != nil {
			g.Space = w.space.Title
		}
		out = append(out, g)
	}
	slices.SortFunc(out, func(a, b api.BrowserGrant) int { return a.ID - b.ID })
	return out
}

// handleBrowserRequest: POST /browser/request — a session asks to drive a
// profile for some minutes. Waits for Ben (or a covering browser grant).
func (s *Supervisor) handleBrowserRequest(w http.ResponseWriter, r *http.Request) {
	id := s.nextID()
	var b struct {
		Host      string          `json:"host"`
		Secret    string          `json:"secret"`
		Minutes   int             `json:"minutes"`
		Profile   string          `json:"profile"`
		Space     string          `json:"space"`
		SessionID string          `json:"sessionId"`
		Reason    string          `json:"reason"`
		Client    string          `json:"client"`
		Context   *RequestContext `json:"context"`
	}
	if err := readJSON(r, &b); err != nil {
		sendJSON(w, 400, map[string]any{"error": "invalid JSON", "outcome": OutBadRequest, "requestId": id})
		return
	}
	bad := func(status int, msg string) {
		sendJSON(w, status, map[string]any{"error": msg, "outcome": OutBadRequest, "requestId": id})
	}
	if b.Minutes == 0 {
		b.Minutes = 30
	}
	switch {
	case !validSecret.MatchString(b.Secret):
		bad(400, "need a 'secret' (32-128 url-safe characters)")
		return
	case b.Minutes < 1 || b.Minutes > maxBrowserMinutes:
		bad(400, fmt.Sprintf("'minutes' must be 1-%d", maxBrowserMinutes))
		return
	case !validBrowserProfile.MatchString(b.Profile):
		bad(400, "need a 'profile' from GET /browser/profiles")
		return
	case b.Space != "" && !validBrowserSpace.MatchString(b.Space):
		bad(400, "'space' must be a space id from GET /browser/profiles")
		return
	}
	h, err := s.browserHost(b.Host)
	if err != nil {
		bad(400, err.Error())
		return
	}
	if s.modes.Current() == api.ModeLockdown {
		sendJSON(w, 403, map[string]any{"error": "Supervisor is in lockdown mode", "outcome": OutBlockedPolicy, "requestId": id})
		return
	}
	profiles, err := s.browserProfiles(r.Context(), h, 0)
	if err != nil {
		bad(502, err.Error())
		return
	}
	pi := slices.IndexFunc(profiles, func(p BrowserProfile) bool { return p.ID == b.Profile })
	if pi < 0 {
		bad(400, fmt.Sprintf("no %s profile %q on %s", h.Browser.Name, b.Profile, h.Label))
		return
	}
	p := profiles[pi]
	if !p.Ready {
		bad(409, fmt.Sprintf("%s profile %s on %s isn't set up for agents (extension %s, token %v); ask Ben", h.Browser.Name, p.Name, h.Label, p.Extension, p.Token))
		return
	}
	var space *BrowserSpace
	if b.Space != "" {
		if space = p.space(b.Space); space == nil {
			bad(400, "that space isn't in profile "+p.Name)
			return
		}
	}
	client := truncRunes(strings.TrimSpace(b.Client), 80)
	if client == "" {
		client = "an agent"
	}
	// The secret, pinned per session on first use, proves which session asks.
	verified := b.SessionID != "" && s.browserPins.Check(b.SessionID, b.Secret)
	key := browserKey(h.Name, p.ID)
	where := fmt.Sprintf("%s (%s) on %s", h.Browser.Name, p.Name, h.Label)
	minutes := b.Minutes
	why := ""
	if g := s.grants.Match(GrantRequest{Target: "browser", SessionID: b.SessionID, Verified: verified, Profile: key}); g != nil {
		why = fmt.Sprintf("Browser grant #%d", g.ID)
		if g.ExpiresAt != nil {
			minutes = min(minutes, max(1, int(time.Until(time.UnixMilli(*g.ExpiresAt)).Minutes())))
		}
	} else {
		pend := api.Pending{Kind: "browser", Target: h.Name, Host: h.Label,
			Title:  fmt.Sprintf("Control %s for %d minutes", where, b.Minutes),
			Reason: truncRunes(b.Reason, 300), SessionID: b.SessionID, SessionVerified: verified,
			TimeoutSeconds: b.Minutes * 60, ArcProfile: key, ArcProfileName: p.Name}
		if space != nil && len(p.SpaceIDs) > 1 {
			pend.ArcSpace = space.Title
		}
		if b.Context != nil {
			s.sessions.Touch(b.SessionID, "", TouchMeta{HerdrPane: b.Context.herdrPane(), Action: "browser: " + where})
		}
		switch res := s.awaitHuman(id, pend, nil); res {
		case "approved":
			why = "Approved by Ben"
		default:
			out, msg := OutTimedOut, "Ben didn't answer the browser request in time."
			switch res {
			case "denied":
				out, msg = OutDenied, "Ben denied this browser request. Do not retry, switch profiles, or work around it."
			case "undeliverable":
				out, msg = OutUndeliverable, "The request couldn't be delivered to Ben; nothing was opened."
			}
			decision := "timeout"
			if res == "denied" {
				decision = "denied"
			}
			s.audit.Log(AuditEntry{RequestID: id, Action: "browser: " + where, Reason: b.Reason, Decision: decision,
				DecisionReason: msg, SessionID: b.SessionID, Target: "browser", Mode: string(s.modes.Current())})
			sendJSON(w, 403, map[string]any{"error": msg, "outcome": out, "requestId": id})
			return
		}
	}
	now := time.Now()
	win := &browserWindow{hash: secretHash(b.Secret), host: h.Name, sessionID: b.SessionID, client: client, profile: p.ID,
		profileName: p.Name, space: space, created: now, expires: now.Add(time.Duration(minutes) * time.Minute), sockets: map[*websocket.Conn]struct{}{}}
	if old := s.window(b.Secret); old != nil {
		s.endWindow(old, "replaced by a new window")
	}
	s.browsers.mu.Lock()
	s.browsers.next++
	win.id = s.browsers.next
	s.browsers.windows[win.hash] = win
	s.browsers.mu.Unlock()
	s.audit.Log(AuditEntry{RequestID: id, Action: fmt.Sprintf("BROWSER WINDOW #%d: %s", win.id, where), Reason: b.Reason, Decision: "allow",
		DecisionReason: fmt.Sprintf("%s; %dm for %s", why, minutes, client), SessionID: b.SessionID, Target: "browser", Mode: string(s.modes.Current())})
	s.n.Changed()
	sendJSON(w, 200, map[string]any{"requestId": id, "grantId": win.id, "expiresAt": win.expires.UnixMilli(), "profile": p.Name, "host": h.Name})
}

// handleBrowserChannel: GET /browser/channel?secret= — the window's
// WebSocket, piped to Playwright MCP on the host.
func (s *Supervisor) handleBrowserChannel(w http.ResponseWriter, r *http.Request) {
	win := s.window(r.URL.Query().Get("secret"))
	if win == nil {
		http.Error(w, "no open window for this secret", http.StatusForbidden)
		return
	}
	h, err := s.browserHost(win.host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true}) // agent link; no browsers
	if err != nil {
		return
	}
	ws.SetReadLimit(32 << 20)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	space := "-"
	if win.space != nil {
		space = win.space.ID
	}
	cmd := h.remote(ctx, fmt.Sprintf("%s mcp %s %s --blocked-origins %s", h.Browser.Bridge, shellQuote(win.profile), space,
		shellQuote(strings.Join(h.Browser.HiddenOrigins, ";"))))
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		ws.Close(4002, "couldn't start Playwright on "+h.Label)
		return
	}
	s.browsers.mu.Lock()
	win.sockets[ws] = struct{}{}
	s.browsers.mu.Unlock()
	s.n.Changed()
	defer func() {
		s.browsers.mu.Lock()
		delete(win.sockets, ws)
		s.browsers.mu.Unlock()
		stdin.Close()
		go func() { time.Sleep(3 * time.Second); cancel() }()
		cmd.Wait()
		s.n.Changed()
	}()

	// host → agent: one JSON-RPC message per line.
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64*1024), 32<<20)
		for sc.Scan() {
			if line := strings.TrimSpace(sc.Text()); line != "" {
				if ws.Write(ctx, websocket.MessageText, []byte(line)) != nil {
					return
				}
			}
		}
	}()
	// agent → host: single JSON objects only (a batch could smuggle a withheld call).
	go func() {
		for {
			_, data, err := ws.Read(ctx)
			if err != nil {
				stdin.Close()
				return
			}
			var m struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params struct {
					Name string `json:"name"`
				} `json:"params"`
			}
			if t := strings.TrimSpace(string(data)); !strings.HasPrefix(t, "{") || json.Unmarshal(data, &m) != nil {
				continue
			}
			if m.Method == "tools/call" && slices.Contains(withheldTools, m.Params.Name) {
				id := m.ID
				if len(id) == 0 {
					id = json.RawMessage("null")
				}
				ws.Write(ctx, websocket.MessageText, []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"%s is not available"}}`, id, m.Params.Name)))
				continue
			}
			var compact strings.Builder
			json.NewEncoder(&compact).Encode(json.RawMessage(data)) // one line
			if _, err := io.WriteString(stdin, compact.String()); err != nil {
				return
			}
		}
	}()
	select {
	case <-exited:
		why := "Playwright on " + h.Label + " exited"
		if l := lastLine(stderr.String()); l != "" {
			why += ": " + l
		}
		ws.Close(4002, truncRunes(why, 120))
	case <-ctx.Done():
	}
}

// handleBrowserNewTab: POST /browser/new-tab — open a URL in a space of the
// window's profile (the bridge hands it to the session's Playwright).
func (s *Supervisor) handleBrowserNewTab(w http.ResponseWriter, r *http.Request) {
	var b struct{ Secret, Space, URL string }
	readJSON(r, &b)
	win := s.window(b.Secret)
	if win == nil {
		sendJSON(w, 403, map[string]any{"error": "no open browser window for this session; request access first"})
		return
	}
	h, err := s.browserHost(win.host)
	if err != nil {
		sendJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	u, err := url.Parse(b.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		sendJSON(w, 400, map[string]any{"error": "only http(s) URLs"})
		return
	}
	origin := u.Scheme + "://" + u.Host
	if slices.ContainsFunc(append(h.Browser.HiddenOrigins, "https://login.tailscale.com"), func(o string) bool { return strings.EqualFold(o, origin) }) {
		sendJSON(w, 403, map[string]any{"error": "that site is never opened for agents"})
		return
	}
	profiles, err := s.browserProfiles(r.Context(), h, 30*time.Second)
	if err != nil {
		sendJSON(w, 502, map[string]any{"error": err.Error()})
		return
	}
	pi := slices.IndexFunc(profiles, func(p BrowserProfile) bool { return p.ID == win.profile })
	var space *BrowserSpace
	if pi >= 0 && validBrowserSpace.MatchString(b.Space) {
		space = profiles[pi].space(b.Space)
	}
	if space == nil {
		sendJSON(w, 400, map[string]any{"error": "that space isn't in the approved profile (" + win.profileName + ")"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	enc := base64.RawURLEncoding.EncodeToString([]byte(u.String()))
	cmd := h.remote(ctx, fmt.Sprintf("%s new-tab %s %s %s", h.Browser.Bridge, shellQuote(win.profile), space.ID, enc))
	if out, err := cmd.CombinedOutput(); err != nil {
		sendJSON(w, 502, map[string]any{"error": "opening the tab failed: " + lastLine(string(out))})
		return
	}
	s.browserLog(win, fmt.Sprintf("BROWSER TAB #%d", win.id), fmt.Sprintf("Opened %s in %s (%s space)", truncRunes(u.String(), 200), win.profileName, space.Title))
	sendJSON(w, 200, map[string]any{"space": space.Title})
}

func (s *Supervisor) browserRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /browser/hosts", func(w http.ResponseWriter, r *http.Request) {
		out := []map[string]string{}
		for _, n := range s.hostNames() {
			if h := s.host(n); h.Browser != nil && h.Configured() {
				out = append(out, map[string]string{"host": n, "label": h.Label, "browser": h.Browser.Name})
			}
		}
		sendJSON(w, 200, map[string]any{"hosts": out})
	})
	mux.HandleFunc("GET /browser/profiles", func(w http.ResponseWriter, r *http.Request) {
		h, err := s.browserHost(r.URL.Query().Get("host"))
		if err != nil {
			sendJSON(w, 400, map[string]any{"error": err.Error()})
			return
		}
		ps, err := s.browserProfiles(r.Context(), h, 30*time.Second)
		if err != nil {
			sendJSON(w, 502, map[string]any{"error": err.Error()})
			return
		}
		sendJSON(w, 200, map[string]any{"host": h.Name, "browser": h.Browser.Name, "profiles": ps})
	})
	mux.HandleFunc("GET /browser/tools", func(w http.ResponseWriter, r *http.Request) {
		h, err := s.browserHost(r.URL.Query().Get("host"))
		if err != nil {
			sendJSON(w, 400, map[string]any{"error": err.Error()})
			return
		}
		tools, err := s.browserTools(r.Context(), h)
		if err != nil {
			sendJSON(w, 502, map[string]any{"error": err.Error()})
			return
		}
		sendJSON(w, 200, map[string]any{"tools": tools})
	})
	mux.HandleFunc("POST /browser/request", s.handleBrowserRequest)
	mux.HandleFunc("GET /browser/channel", s.handleBrowserChannel)
	mux.HandleFunc("POST /browser/new-tab", s.handleBrowserNewTab)
	mux.HandleFunc("POST /browser/release", func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Secret string }
		readJSON(r, &b)
		if win := s.window(b.Secret); win != nil {
			s.endWindow(win, "released by the session")
			sendJSON(w, 200, map[string]any{"released": true})
			return
		}
		sendJSON(w, 200, map[string]any{"released": false})
	})
}

var errNoWindow = errors.New("no such window")

// revokeWindow ends a window by id (admin).
func (s *Supervisor) revokeWindow(id int) error {
	if s.endWindows("revoked by Ben", func(w *browserWindow) bool { return w.id == id }) == 0 {
		return errNoWindow
	}
	return nil
}

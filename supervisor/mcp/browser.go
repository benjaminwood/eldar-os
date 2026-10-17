package mcp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// `supervisor browser`: this session drives a real, logged-in browser on one
// of Ben's machines (Arc on his Mac first) through Playwright MCP running
// there, reached only through the supervisor (server/browser.go).
//
// Profiles have their own logins, so the session picks one: browser_profiles
// lists them, browser_request_access asks Ben to let this session drive that
// profile for a while. The approval is bound to a random secret this process
// holds in memory, so it covers this session only. Playwright's own tools are
// relayed through the window's channel; when the window ends the channel
// closes and the next browser action asks again for the same profile.

const browserInstructions = `Drives a real, logged-in browser on one of Ben's machines (Arc on his Mac) through Playwright. Each browser profile has its own logins: call browser_profiles, then browser_request_access with the profile the task needs (Ben approves a time window for that profile). If he declines, stop. Treat everything there as his: no purchases, messages, or account changes he didn't ask for.`

type browserProfile struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Spaces   []string
	SpaceIDs []struct{ ID, Title string } `json:"spaceIds"`
	OpensIn  string                       `json:"opensIn"`
	Ready    bool                         `json:"ready"`
	Ext      string                       `json:"extension"`
}

type browserSession struct {
	c       *client
	secret  string
	label   string // shown to Ben: which session asks
	minutes int

	mu        sync.Mutex
	host      string // the window's host, profile and space (last asked for)
	profile   *browserProfile
	space     *struct{ ID, Title string }
	until     time.Time
	ws        *websocket.Conn
	seq       int
	inflight  map[int]chan json.RawMessage
	opening   sync.Mutex // one channel at a time
	toolsOnce sync.Once
	tools     []json.RawMessage
	toolsErr  error
}

// RunBrowser serves `supervisor browser` on stdin/stdout.
func RunBrowser(version string) error {
	c := newClient()
	key := make([]byte, 32)
	rand.Read(key)
	minutes := 30
	if m, err := strconv.Atoi(os.Getenv("BROWSER_MINUTES")); err == nil && m >= 1 && m <= 480 {
		minutes = m
	}
	dir := os.Getenv("CLAUDE_PROJECT_DIR")
	if dir == "" {
		dir, _ = os.Getwd()
	}
	b := &browserSession{c: c, secret: base64.RawURLEncoding.EncodeToString(key), label: "Eldar · " + filepath.Base(dir),
		minutes: minutes, inflight: map[int]chan json.RawMessage{}}
	return b.server(version).serve(context.Background(), os.Stdin, os.Stdout)
}

func (b *browserSession) get(ctx context.Context, path string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, b.c.url+path, nil)
	res, err := b.c.http.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't reach the supervisor: %w", err)
	}
	defer res.Body.Close()
	raw := map[string]json.RawMessage{}
	json.NewDecoder(res.Body).Decode(&raw)
	if res.StatusCode != 200 {
		var e string
		json.Unmarshal(raw["error"], &e)
		return errors.New(e)
	}
	b2, _ := json.Marshal(raw)
	return json.Unmarshal(b2, out)
}

type browserHost struct{ Host, Label, Browser string }

func (b *browserSession) hosts(ctx context.Context) ([]browserHost, error) {
	var r struct{ Hosts []browserHost }
	err := b.get(ctx, "/browser/hosts", &r)
	return r.Hosts, err
}

func (b *browserSession) profiles(ctx context.Context, host string) ([]browserProfile, error) {
	var r struct{ Profiles []browserProfile }
	err := b.get(ctx, "/browser/profiles?host="+host, &r)
	return r.Profiles, err
}

// pickHost: the named host, or the only one with a browser.
func (b *browserSession) pickHost(ctx context.Context, want string) (browserHost, error) {
	hs, err := b.hosts(ctx)
	if err != nil {
		return browserHost{}, err
	}
	for _, h := range hs {
		if want == "" && len(hs) == 1 || strings.EqualFold(want, h.Host) {
			return h, nil
		}
	}
	if len(hs) == 0 {
		return browserHost{}, errors.New("no machine with a browser is configured")
	}
	var names []string
	for _, h := range hs {
		names = append(names, fmt.Sprintf("%s (%s on %s)", h.Host, h.Browser, h.Label))
	}
	return browserHost{}, fmt.Errorf("say which host: %s", strings.Join(names, ", "))
}

func (b *browserSession) remoteTools(ctx context.Context) ([]json.RawMessage, error) {
	b.toolsOnce.Do(func() {
		hs, err := b.hosts(ctx)
		if err != nil || len(hs) == 0 {
			b.toolsErr = fmt.Errorf("no browser available: %v", err)
			return
		}
		var r struct{ Tools []json.RawMessage }
		if b.toolsErr = b.get(ctx, "/browser/tools?host="+hs[0].Host, &r); b.toolsErr == nil {
			b.tools = r.Tools
		}
	})
	return b.tools, b.toolsErr
}

func (b *browserSession) server(version string) *server {
	own := []tool{
		{Name: "browser_profiles", Description: "[Ben's browsers] List the browser profiles on Ben's machines (Arc on his Mac) and their spaces. A profile has its own logins; a space is a sidebar section within a profile (several can share a profile and its logins). Shows which profiles are set up for you and what this session currently has. Call this before browser_request_access.",
			Schema: func([]string, string) map[string]any {
				return object(map[string]any{"host": map[string]any{"type": "string", "description": "Only this machine (default: all)."}})
			}, run: b.profilesTool},
		{Name: "browser_request_access", Description: "[Ben's browsers] Ask Ben to let this session drive one browser profile for a time window. Waits for his answer. Pick the profile whose logins the task needs; if unsure, ask him. Switching profiles ends the current window. If he declines, stop.",
			Schema: func([]string, string) map[string]any {
				return object(map[string]any{
					"host":    map[string]any{"type": "string", "description": "Which machine (default: the only one with a browser)."},
					"profile": map[string]any{"type": "string", "description": "Profile id or name from browser_profiles, or a space name (then that space is used)."},
					"space":   map[string]any{"type": "string", "description": "Space your first tab opens in, within that profile (default: the profile's first space). Use the space Ben named."},
					"reason":  map[string]any{"type": "string", "description": "What you'll do in his browser, shown to Ben in the approval."},
					"minutes": map[string]any{"type": "integer", "minimum": 1, "maximum": 480, "description": fmt.Sprintf("Window length (default %d).", b.minutes)},
				}, "profile", "reason")
			}, run: b.requestTool},
		{Name: "browser_open_tab", Description: "[Ben's browsers] Open a URL in a new tab in a specific space of the approved profile. Use this instead of browser_tabs 'new' when Ben wants a tab in a particular space, since the browser decides where Playwright's own new tabs land. The tab joins your session; select it with browser_tabs.",
			Schema: func([]string, string) map[string]any {
				return object(map[string]any{
					"url":   map[string]any{"type": "string", "description": "http(s) URL to open."},
					"space": map[string]any{"type": "string", "description": "Space name in the approved profile (default: the window's space)."},
				}, "url")
			}, run: b.openTabTool},
		{Name: "browser_release", Description: "[Ben's browsers] Give the current browser window back early when you're done. The next browser action asks Ben again.",
			Schema: func([]string, string) map[string]any { return object(map[string]any{}) }, run: b.releaseTool},
	}
	s := &server{c: b.c, name: "browser", version: version, instructions: browserInstructions, tools: map[string]tool{}}
	for _, t := range own {
		s.tools[t.Name] = t
	}
	s.describe = func(ctx context.Context) []map[string]any {
		var out []map[string]any
		for _, t := range own {
			out = append(out, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": t.Schema(nil, "")})
		}
		remote, err := b.remoteTools(ctx)
		if err != nil {
			logStderr("browser", err.Error())
		}
		for _, raw := range remote {
			var t map[string]any
			if json.Unmarshal(raw, &t) == nil {
				t["description"] = "[Ben's browser] " + fmt.Sprint(t["description"])
				out = append(out, t)
			}
		}
		return out
	}
	s.fallback = b.relay
	return s
}

func (b *browserSession) live() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.profile != nil && time.Now().Before(b.until)
}

func (b *browserSession) profilesTool(ctx context.Context, args map[string]any) map[string]any {
	hs, err := b.hosts(ctx)
	if err != nil {
		return errorText(err.Error())
	}
	var out []string
	for _, h := range hs {
		if want := str(args, "host"); want != "" && !strings.EqualFold(want, h.Host) {
			continue
		}
		ps, err := b.profiles(ctx, h.Host)
		if err != nil {
			out = append(out, fmt.Sprintf("%s on %s [host: %s]: %v", h.Browser, h.Label, h.Host, err))
			continue
		}
		out = append(out, fmt.Sprintf("%s on %s [host: %s]:", h.Browser, h.Label, h.Host))
		for _, p := range ps {
			state := "ready"
			if !p.Ready {
				state = "not set up for you (extension " + p.Ext + ")"
			}
			spaces := "space: " + strings.Join(p.Spaces, "")
			if len(p.Spaces) > 1 {
				spaces = fmt.Sprintf("spaces: %s (default for new tabs: %s)", strings.Join(p.Spaces, ", "), p.OpensIn)
			}
			mine := ""
			b.mu.Lock()
			if b.profile != nil && b.host == h.Host && b.profile.ID == p.ID && time.Now().Before(b.until) {
				mine = fmt.Sprintf("  ← this session's window, %d min left", int(time.Until(b.until).Minutes()))
			}
			b.mu.Unlock()
			out = append(out, fmt.Sprintf("- profile [id: %s] — %s — %s%s", p.ID, spaces, state, mine))
		}
	}
	if len(out) == 0 {
		return textResult("No machine with a browser is configured.")
	}
	return textResult(strings.Join(out, "\n") + "\n\nOnly ready profiles can be used; if the one you need isn't, ask Ben to set it up. Spaces in a profile share its logins, and browser_tabs lists tabs across them.")
}

func matchProfile(ps []browserProfile, want string) *browserProfile {
	w := strings.ToLower(strings.TrimSpace(want))
	for _, pass := range []func(browserProfile) bool{
		func(p browserProfile) bool { return strings.ToLower(p.ID) == w },
		func(p browserProfile) bool { return strings.ToLower(p.Name) == w },
		func(p browserProfile) bool {
			for _, s := range p.Spaces {
				if strings.ToLower(s) == w {
					return true
				}
			}
			return false
		},
	} {
		for i := range ps {
			if pass(ps[i]) {
				return &ps[i]
			}
		}
	}
	return nil
}

func (b *browserSession) requestTool(ctx context.Context, args map[string]any) map[string]any {
	h, err := b.pickHost(ctx, str(args, "host"))
	if err != nil {
		return errorText(err.Error())
	}
	ps, err := b.profiles(ctx, h.Host)
	if err != nil {
		return errorText(err.Error())
	}
	p := matchProfile(ps, str(args, "profile"))
	if p == nil {
		var names []string
		for _, x := range ps {
			names = append(names, fmt.Sprintf("%s [%s]", x.Name, x.ID))
		}
		return errorText(fmt.Sprintf("No profile matches %q on %s. Profiles: %s", str(args, "profile"), h.Label, strings.Join(names, ", ")))
	}
	want := strings.ToLower(strings.TrimSpace(str(args, "space")))
	if want == "" {
		want = strings.ToLower(strings.TrimSpace(str(args, "profile")))
	}
	var space *struct{ ID, Title string }
	for i := range p.SpaceIDs {
		if strings.ToLower(p.SpaceIDs[i].Title) == want {
			space = &p.SpaceIDs[i]
		}
	}
	if str(args, "space") != "" && space == nil {
		return errorText(fmt.Sprintf("%q isn't a space in that profile. Its spaces: %s", str(args, "space"), strings.Join(p.Spaces, ", ")))
	}
	minutes := b.minutes
	if m, ok := args["minutes"].(float64); ok {
		minutes = min(max(int(m), 1), 480)
	}
	sameSpace := (b.space == nil) == (space == nil) && (space == nil || b.space.ID == space.ID)
	b.mu.Lock()
	if b.host != h.Host || b.profile == nil || b.profile.ID != p.ID || !sameSpace {
		b.closeLocked()
		b.until = time.Time{}
	}
	b.host, b.profile, b.space = h.Host, p, space
	b.mu.Unlock()
	if err := b.request(ctx, truncate(str(args, "reason"), 300), minutes); err != nil {
		return errorText(err.Error())
	}
	opens := p.OpensIn
	if space != nil {
		opens = space.Title
	}
	return textResult(fmt.Sprintf("Ben allowed this session to drive %s (%s) on %s until %s. New tabs open in its %q space. browser_tabs lists every tab in the profile, across its spaces.",
		h.Browser, p.Name, h.Label, b.until.In(pacific()).Format("3:04 PM MST"), opens))
}

func pacific() *time.Location {
	if l, err := time.LoadLocation("America/Los_Angeles"); err == nil {
		return l
	}
	return time.Local
}

// request asks the supervisor (Ben) for a window on the chosen profile.
func (b *browserSession) request(ctx context.Context, reason string, minutes int) error {
	b.mu.Lock()
	host, p, space := b.host, b.profile, b.space
	b.mu.Unlock()
	if p == nil {
		return errors.New("No browser profile chosen yet. Call browser_profiles, then browser_request_access with the profile this task needs.")
	}
	payload := map[string]any{"host": host, "secret": b.secret, "minutes": minutes, "profile": p.ID, "sessionId": b.c.sessionID,
		"reason": reason, "client": b.label}
	if space != nil {
		payload["space"] = space.ID
	}
	if c := approvalContext(); c != nil {
		payload["context"] = c
	}
	data, err := b.c.post(ctx, "/browser/request", payload, 6*time.Minute)
	if err != nil {
		return fmt.Errorf("couldn't reach the supervisor (%v); nothing was opened", err)
	}
	if e, ok := data["error"]; ok {
		return fmt.Errorf("%v", e)
	}
	exp, _ := data["expiresAt"].(float64)
	b.mu.Lock()
	b.until = time.UnixMilli(int64(exp))
	b.mu.Unlock()
	return nil
}

func (b *browserSession) closeLocked() {
	if b.ws != nil {
		b.ws.Close(websocket.StatusNormalClosure, "")
		b.ws = nil
	}
}

// channel opens (or reuses) the window's channel, asking for a window first if needed.
func (b *browserSession) channel(ctx context.Context, why string) (*websocket.Conn, error) {
	b.opening.Lock()
	defer b.opening.Unlock()
	if !b.live() {
		if err := b.request(ctx, why, b.minutes); err != nil {
			return nil, err
		}
	}
	b.mu.Lock()
	if b.ws != nil {
		ws := b.ws
		b.mu.Unlock()
		return ws, nil
	}
	b.mu.Unlock()
	u := strings.Replace(b.c.url, "http", "ws", 1) + "/browser/channel?secret=" + b.secret
	ws, _, err := websocket.Dial(ctx, u, nil)
	if err != nil {
		return nil, fmt.Errorf("couldn't open the browser channel: %w", err)
	}
	ws.SetReadLimit(32 << 20)
	// MCP handshake with Playwright, then a reader that routes responses.
	conn := context.Background()
	init, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "init", "method": "initialize", "params": map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": b.label, "version": "1"}}})
	ws.Write(ctx, websocket.MessageText, init)
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return nil, fmt.Errorf("browser channel closed during setup: %v", err)
		}
		if strings.Contains(string(data), `"id":"init"`) {
			break
		}
	}
	ws.Write(ctx, websocket.MessageText, []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	b.mu.Lock()
	b.ws = ws
	b.mu.Unlock()
	go func() {
		for {
			_, data, err := ws.Read(conn)
			if err != nil {
				b.mu.Lock()
				if b.ws == ws {
					b.ws = nil
				}
				if websocket.CloseStatus(err) == 4001 {
					b.until = time.Time{} // the window ended; the next call asks again
				}
				for id, ch := range b.inflight {
					reason := err.Error()
					ch <- json.RawMessage(fmt.Sprintf(`{"error":{"code":-32000,"message":%q}}`, "Browser access ended: "+reason+". The next browser action asks Ben again."))
					delete(b.inflight, id)
				}
				b.mu.Unlock()
				return
			}
			var m struct {
				ID json.RawMessage `json:"id"`
			}
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			id, err := strconv.Atoi(string(m.ID))
			if err != nil {
				continue // notifications from Playwright aren't forwarded
			}
			b.mu.Lock()
			ch := b.inflight[id]
			delete(b.inflight, id)
			b.mu.Unlock()
			if ch != nil {
				ch <- data
			}
		}
	}()
	return ws, nil
}

// relay sends a Playwright tool call through the window's channel.
func (b *browserSession) relay(ctx context.Context, name string, params json.RawMessage) (any, *rpcError) {
	var p struct {
		Arguments map[string]any `json:"arguments"`
	}
	json.Unmarshal(params, &p)
	detail := ""
	for _, k := range []string{"url", "element", "text", "key"} {
		if v, ok := p.Arguments[k]; ok {
			detail = fmt.Sprint(v)
			break
		}
	}
	ws, err := b.channel(ctx, truncate(strings.TrimSpace(name+" "+detail), 300))
	if err != nil {
		return errorText(err.Error()), nil
	}
	b.mu.Lock()
	b.seq++
	id := b.seq
	ch := make(chan json.RawMessage, 1)
	b.inflight[id] = ch
	b.mu.Unlock()
	msg, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": json.RawMessage(params)})
	if err := ws.Write(ctx, websocket.MessageText, msg); err != nil {
		return errorText("the browser channel closed: " + err.Error()), nil
	}
	select {
	case data := <-ch:
		var r struct {
			Result json.RawMessage `json:"result"`
			Error  *rpcError       `json:"error"`
		}
		json.Unmarshal(data, &r)
		if r.Error != nil {
			return errorText(r.Error.Message), nil
		}
		return r.Result, nil
	case <-ctx.Done():
		return nil, &rpcError{-32000, "cancelled"}
	}
}

func (b *browserSession) openTabTool(ctx context.Context, args map[string]any) map[string]any {
	if !b.live() {
		return errorText("No open browser window. Call browser_request_access first.")
	}
	b.mu.Lock()
	host, p, space := b.host, b.profile, b.space
	b.mu.Unlock()
	ps, err := b.profiles(ctx, host)
	if err != nil {
		return errorText(err.Error())
	}
	cur := matchProfile(ps, p.ID)
	if cur == nil {
		return errorText("The approved profile is no longer listed.")
	}
	var target *struct{ ID, Title string }
	if want := strings.ToLower(strings.TrimSpace(str(args, "space"))); want != "" {
		for i := range cur.SpaceIDs {
			if strings.ToLower(cur.SpaceIDs[i].Title) == want {
				target = &cur.SpaceIDs[i]
			}
		}
	} else if space != nil {
		target = space
	} else {
		for i := range cur.SpaceIDs {
			if cur.SpaceIDs[i].Title == cur.OpensIn {
				target = &cur.SpaceIDs[i]
			}
		}
	}
	if target == nil {
		return errorText(fmt.Sprintf("%q isn't a space in this profile. Its spaces: %s", str(args, "space"), strings.Join(cur.Spaces, ", ")))
	}
	data, err := b.c.post(ctx, "/browser/new-tab", map[string]any{"secret": b.secret, "space": target.ID, "url": str(args, "url")}, 30*time.Second)
	if err != nil {
		return errorText(err.Error())
	}
	if e, ok := data["error"]; ok {
		return errorText(fmt.Sprint(e))
	}
	return textResult(fmt.Sprintf("Opened %s in the %v space. It joins your session in a moment; use browser_tabs to list and select it.", str(args, "url"), data["space"]))
}

func (b *browserSession) releaseTool(ctx context.Context, _ map[string]any) map[string]any {
	b.mu.Lock()
	had := b.profile != nil && time.Now().Before(b.until)
	b.closeLocked()
	b.until = time.Time{}
	b.mu.Unlock()
	b.c.post(ctx, "/browser/release", map[string]any{"secret": b.secret}, 15*time.Second)
	if had {
		return textResult("Browser window ended.")
	}
	return textResult("There was no open browser window.")
}

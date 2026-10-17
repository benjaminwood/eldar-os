package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benjaminwood/eldar-os/supervisor/api"
	"github.com/coder/websocket"
)

const testSpace = "11111111-1111-1111-1111-111111111111"

// browserTest: a supervisor whose "mac" host has a browser, with ssh and the
// bridge faked: ssh runs the remote command locally; the bridge lists two
// profiles (one ready) and its mcp echoes every line back.
func browserTest(t *testing.T) (*Supervisor, *httptest.Server, string) {
	t.Helper()
	s, _ := newTest(t)
	dir := t.TempDir()
	write := func(name, body string) {
		os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755)
	}
	write("ssh", "#!/bin/sh\nfor a; do last=$a; done\nexec sh -c \"$last\"\n")
	write("bridge", `#!/bin/sh
case $1 in
profiles) echo '{"profiles":[{"id":"Default","name":"Work","spaces":["Work"],"spaceIds":[{"id":"`+testSpace+`","title":"Work"}],"opensIn":"Work","extension":"patched","token":true,"ready":true},{"id":"Profile 2","name":"Off","spaces":["Off"],"spaceIds":[],"extension":"missing","ready":false}]}' ;;
mcp) echo "args: $*" >&2; while read -r line; do echo "$line"; done ;;
new-tab) echo "$*" > "$(dirname "$0")/new-tab.args" ;;
esac
`)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	os.WriteFile(filepath.Join(dir, "key"), []byte("k"), 0o600)
	s.cfg.Hosts = map[string]*RemoteHost{"mac": {Name: "mac", Label: "Ben's Mac", Target: "ben@mac", KeyPath: filepath.Join(dir, "key"),
		Browser: &HostBrowser{Name: "Arc", Bridge: filepath.Join(dir, "bridge"), HiddenOrigins: []string{"https://approve.example"}, Device: "macbook"}}}
	srv := httptest.NewServer(s.agentHandler())
	t.Cleanup(srv.Close)
	return s, srv, dir
}

func postJSON(t *testing.T, url string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	res, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func TestBrowserRequestChecks(t *testing.T) {
	s, srv, _ := browserTest(t)
	secret := strings.Repeat("a", 40)
	for _, c := range []struct {
		body map[string]any
		want int
		msg  string
	}{
		{map[string]any{"host": "mac", "secret": "short", "profile": "Default"}, 400, "secret"},
		{map[string]any{"host": "nas", "secret": secret, "profile": "Default"}, 400, "hosts with one: mac"},
		{map[string]any{"host": "mac", "secret": secret, "profile": "Nope"}, 400, "no Arc profile"},
		{map[string]any{"host": "mac", "secret": secret, "profile": "Profile 2"}, 409, "isn't set up"},
		{map[string]any{"host": "mac", "secret": secret, "profile": "Default", "space": "22222222-2222-2222-2222-222222222222"}, 400, "isn't in profile"},
		{map[string]any{"host": "mac", "secret": secret, "profile": "Default", "minutes": 999}, 400, "minutes"},
	} {
		code, out := postJSON(t, srv.URL+"/browser/request", c.body)
		if code != c.want || !strings.Contains(out["error"].(string), c.msg) {
			t.Errorf("%v: %d %v", c.body, code, out)
		}
	}
	s.modes.Lockdown()
	if code, _ := postJSON(t, srv.URL+"/browser/request", map[string]any{"host": "mac", "secret": secret, "profile": "Default"}); code != 403 {
		t.Fatalf("lockdown: %d", code)
	}
}

func TestBrowserWindowEndToEnd(t *testing.T) {
	s, srv, dir := browserTest(t)
	ctx := context.Background()
	secret := strings.Repeat("b", 40)
	// Host grants and global bypass never cover a browser window.
	s.grants.Add(api.GrantInput{Target: "mac", Session: "*", Profile: "*", Credentials: api.AnyCreds})
	s.modes.Bypass(nil)
	done := make(chan int)
	go func() {
		code, _ := postJSON(t, srv.URL+"/browser/request", map[string]any{"host": "mac", "secret": secret, "profile": "Default", "sessionId": "sess", "reason": "test"})
		done <- code
	}()
	if code := <-done; code != 403 { // nobody answered the approval (200ms in tests)
		t.Fatalf("an approval must be asked: %d", code)
	}
	if pend := s.approvals.List(); len(pend) != 0 {
		t.Fatalf("leftover approvals: %v", pend)
	}
	s.modes.Revert()

	// A browser grant Ben made for this profile opens it without asking.
	s.grants.Add(api.GrantInput{Target: "browser", Session: "*", Profile: "*", Arc: api.Creds{Names: []string{"mac:Default"}}})
	code, out := postJSON(t, srv.URL+"/browser/request", map[string]any{"host": "mac", "secret": secret, "profile": "Default", "space": testSpace, "sessionId": "sess", "reason": "test"})
	if code != 200 || out["grantId"] == nil {
		t.Fatalf("grant-covered request: %d %v", code, out)
	}
	gs := s.browserGrants()
	if len(gs) != 1 || gs[0].Profile != "mac:Default" || gs[0].Device != "macbook" || gs[0].Space != "Work" {
		t.Fatalf("windows: %+v", gs)
	}
	if code, _ := postJSON(t, srv.URL+"/browser/new-tab", map[string]any{"secret": secret, "space": testSpace, "url": "https://approve.example/x"}); code != 403 {
		t.Fatalf("hidden origin opened: %d", code)
	}
	if code, out := postJSON(t, srv.URL+"/browser/new-tab", map[string]any{"secret": secret, "space": testSpace, "url": "https://example.com/"}); code != 200 || out["space"] != "Work" {
		t.Fatalf("new tab: %d %v", code, out)
	}
	if args, _ := os.ReadFile(filepath.Join(dir, "new-tab.args")); !strings.HasPrefix(string(args), "new-tab Default "+testSpace+" ") {
		t.Fatalf("bridge new-tab args: %q", args)
	}

	// The channel: Playwright's stdio, with withheld tools refused here.
	if _, res, err := websocket.Dial(ctx, strings.Replace(srv.URL, "http", "ws", 1)+"/browser/channel?secret="+strings.Repeat("c", 40), nil); err == nil || res.StatusCode != 403 {
		t.Fatal("a channel needs a live window's secret")
	}
	ws, _, err := websocket.Dial(ctx, strings.Replace(srv.URL, "http", "ws", 1)+"/browser/channel?secret="+secret, nil)
	if err != nil {
		t.Fatal(err)
	}
	send := func(m string) string {
		t.Helper()
		ws.Write(ctx, websocket.MessageText, []byte(m))
		rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_, data, err := ws.Read(rctx)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	if got := send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"browser_navigate","arguments":{"url":"https://example.com"}}}`); !strings.Contains(got, "browser_navigate") {
		t.Fatalf("relayed: %s", got)
	}
	if got := send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"browser_run_code_unsafe","arguments":{}}}`); !strings.Contains(got, `"error"`) || !strings.Contains(got, `"id":2`) {
		t.Fatalf("withheld tool: %s", got)
	}
	ws.Write(ctx, websocket.MessageText, []byte(`[{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"browser_run_code_unsafe"}}]`))
	if got := send(`{"jsonrpc":"2.0","id":4,"method":"ping"}`); !strings.Contains(got, `"id":4`) {
		t.Fatalf("a batch must be dropped, not relayed: %s", got)
	}

	// Revoking ends the window and closes the channel with 4001.
	if err := s.revokeWindow(gs[0].ID); err != nil {
		t.Fatal(err)
	}
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, _, err := ws.Read(rctx); websocket.CloseStatus(err) != 4001 {
		t.Fatalf("close status: %v", err)
	}
	if len(s.browserGrants()) != 0 || s.window(secret) != nil {
		t.Fatal("window still open")
	}
}

func TestBrowserAdminGrant(t *testing.T) {
	s, _, _ := browserTest(t)
	in, msg := s.parseAdminGrant(adminGrantBody{Target: "browser", Session: "*", Arc: json.RawMessage(`["mac:Default"]`), Minutes: ptr(30.0)})
	if msg != "" || in.Target != "browser" || len(in.Arc.Names) != 1 {
		t.Fatalf("%v %q", in, msg)
	}
	if _, msg := s.parseAdminGrant(adminGrantBody{Target: "browser", Session: "*", Arc: json.RawMessage(`[]`)}); msg == "" {
		t.Fatal("a browser grant must name profiles (or *)")
	}
}

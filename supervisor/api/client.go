package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Client talks to the admin API over its unix socket.
type Client struct {
	Socket string
	Name   string // sent as X-Admin-Client, recorded in the audit log
	http   *http.Client
	stream *http.Client
}

// SocketCandidates lists where the admin socket lives, most specific first:
// $SUPERVISOR_SOCKET, then the supervisor on this host.
func SocketCandidates() []string {
	var out []string
	if s := os.Getenv("SUPERVISOR_SOCKET"); s != "" {
		out = append(out, s)
	}
	out = append(out, "/run/supervisor/admin.sock")
	return out
}

// FindSocket returns the first socket that exists.
func FindSocket() (string, error) {
	cands := SocketCandidates()
	for _, s := range cands {
		if fi, err := os.Stat(s); err == nil && fi.Mode()&os.ModeSocket != 0 {
			return s, nil
		}
	}
	return "", fmt.Errorf("no admin socket found (looked in %s)", strings.Join(cands, ", "))
}

func New(socket string) *Client {
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socket)
	}
	return &Client{
		Socket: socket,
		// No client-wide timeout: some calls (starting Claude) take a minute.
		// Callers bound each request with their context.
		http:   &http.Client{Transport: &http.Transport{DialContext: dial}},
		stream: &http.Client{Transport: &http.Transport{DialContext: dial}},
	}
}

type clientNameKey struct{}

// WithClientName overrides the X-Admin-Client name for calls made with ctx
// (the web UI uses it to record which device acted).
func WithClientName(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, clientNameKey{}, name)
}

// APIError is a non-2xx response from the proxy.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return e.Message }

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://admin"+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	name := c.Name
	if n, ok := ctx.Value(clientNameKey{}).(string); ok {
		name = n
	}
	if name != "" {
		req.Header.Set("X-Admin-Client", name)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return friendly(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		var e struct{ Error string }
		_ = json.Unmarshal(raw, &e)
		if e.Error == "" {
			e.Error = res.Status
		}
		return &APIError{Status: res.StatusCode, Message: e.Error}
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// friendly turns socket errors into something a person can act on.
func friendly(err error) error {
	switch {
	case errors.Is(err, os.ErrPermission):
		return errors.New("permission denied on the admin socket — run as a member of the supervisor-admin group (ben)")
	case errors.Is(err, os.ErrNotExist):
		return errors.New("admin socket missing — is the supervisor running?")
	}
	var op *net.OpError
	if errors.As(err, &op) {
		if strings.Contains(op.Err.Error(), "permission denied") {
			return errors.New("permission denied on the admin socket — run as a member of the supervisor-admin group (ben)")
		}
		if strings.Contains(op.Err.Error(), "connection refused") || strings.Contains(op.Err.Error(), "no such file") {
			return errors.New("supervisor not reachable — is it running?")
		}
	}
	return err
}

func (c *Client) State(ctx context.Context) (State, error) {
	var s State
	err := c.do(ctx, "GET", "/admin/state", nil, &s)
	return s, err
}

func (c *Client) Decide(ctx context.Context, id int, d Decision) error {
	return c.do(ctx, "POST", "/admin/decide", map[string]any{"id": id, "decision": d}, nil)
}

// SetMode changes the global mode. minutes applies to bypass; nil = indefinite.
func (c *Client) SetMode(ctx context.Context, m Mode, minutes *int) error {
	return c.do(ctx, "POST", "/admin/mode", map[string]any{"mode": m, "minutes": minutes}, nil)
}

func (c *Client) AddGrant(ctx context.Context, in GrantInput) (Grant, error) {
	var g Grant
	err := c.do(ctx, "POST", "/admin/grants", in, &g)
	return g, err
}

func (c *Client) RevokeGrant(ctx context.Context, id int) error {
	return c.do(ctx, "DELETE", fmt.Sprintf("/admin/grants/%d", id), nil, nil)
}

// ApproveGrantRequest approves a session's grant request with the scope Ben chose.
func (c *Client) ApproveGrantRequest(ctx context.Context, id int, in GrantInput) error {
	return c.do(ctx, "POST", fmt.Sprintf("/admin/grant-requests/%d/approve", id), in, nil)
}

// RevokeBrowserGrant ends a session's Arc access window.
func (c *Client) RevokeBrowserGrant(ctx context.Context, id int) error {
	return c.do(ctx, "DELETE", fmt.Sprintf("/admin/browser-grants/%d", id), nil, nil)
}

func (c *Client) UnpinSession(ctx context.Context, id string) error {
	return c.do(ctx, "POST", "/admin/sessions/"+url.PathEscape(id)+"/unpin", nil, nil)
}

// Credentials lists the credential NAMES in a profile.
func (c *Client) Credentials(ctx context.Context, profile string) ([]string, error) {
	var out struct{ Credentials []string }
	err := c.do(ctx, "GET", "/admin/credentials?profile="+url.QueryEscape(profile), nil, &out)
	return out.Credentials, err
}

// SetMobilePush turns push notifications for new approvals on or off.
func (c *Client) SetMobilePush(ctx context.Context, on bool) (Settings, error) {
	var s Settings
	err := c.do(ctx, "POST", "/admin/settings", map[string]any{"mobilePush": on}, &s)
	return s, err
}

// RegisterMCP starts an OAuth sign-in for an MCP server and returns its link.
func (c *Client) RegisterMCP(ctx context.Context, name, serverURL string) (string, error) {
	var out struct {
		AuthURL string `json:"authUrl"`
	}
	err := c.do(ctx, "POST", "/admin/mcp/register", map[string]string{"name": name, "url": serverURL}, &out)
	return out.AuthURL, err
}

// CompleteMCP finishes a sign-in from the redirect URL the browser landed on.
func (c *Client) CompleteMCP(ctx context.Context, redirectURL string) (string, error) {
	var out struct{ Name string }
	err := c.do(ctx, "POST", "/admin/mcp/callback", map[string]string{"url": redirectURL}, &out)
	return out.Name, err
}

func (c *Client) RemoveMCP(ctx context.Context, name string) error {
	return c.do(ctx, "DELETE", "/admin/mcp/"+url.PathEscape(name), nil, nil)
}

// LaunchClaude opens a new tab in a herdr workspace and starts Claude in it.
func (c *Client) LaunchClaude(ctx context.Context, in LaunchInput) (map[string]string, error) {
	out := map[string]string{}
	err := c.do(ctx, "POST", "/admin/herdr/launch", in, &out)
	return out, err
}

// OpenShell opens a new tab with a plain shell in a herdr workspace.
func (c *Client) OpenShell(ctx context.Context, workspaceID, name, cwd string) (map[string]string, error) {
	out := map[string]string{}
	err := c.do(ctx, "POST", "/admin/herdr/shell", map[string]string{"workspaceId": workspaceID, "name": name, "cwd": cwd}, &out)
	return out, err
}

// CloseClaude closes the Claude session in a pane (its tab if it's alone there).
func (c *Client) CloseClaude(ctx context.Context, paneID, session string) error {
	body := map[string]string{"paneId": paneID}
	if session != "" {
		body["session"] = session
	}
	return c.do(ctx, "POST", "/admin/herdr/close", body, nil)
}

// TestMac checks the SSH connection to Ben's Mac.
func (c *Client) TestMac(ctx context.Context) (MacTest, error) {
	var t MacTest
	err := c.do(ctx, "POST", "/admin/mac/test", map[string]any{}, &t)
	return t, err
}

// MacAllow adds (or, with remove, deletes) a Mac allow-list pattern.
func (c *Client) MacAllow(ctx context.Context, pattern string, remove bool) error {
	method := "POST"
	if remove {
		method = "DELETE"
	}
	return c.do(ctx, method, "/admin/mac/allow", map[string]string{"pattern": pattern}, nil)
}

func (c *Client) AllowCron(ctx context.Context, id, name string) error {
	return c.do(ctx, "POST", "/admin/crons", map[string]string{"id": id, "name": name}, nil)
}

func (c *Client) RevokeCron(ctx context.Context, id string) error {
	return c.do(ctx, "DELETE", "/admin/crons/"+url.PathEscape(id), nil, nil)
}

// Chat returns chat messages as raw JSON, passing the query (after, before,
// limit) through.
func (c *Client) Chat(ctx context.Context, q url.Values) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.do(ctx, "GET", "/admin/chat?"+q.Encode(), nil, &out)
	return out, err
}

// SendChat posts a message from Ben to Eldar.
func (c *Client) SendChat(ctx context.Context, text string) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.do(ctx, "POST", "/admin/chat", map[string]string{"text": text}, &out)
	return out, err
}

// ChatRead marks Eldar's messages read up to and including id.
func (c *Client) ChatRead(ctx context.Context, upTo int) error {
	return c.do(ctx, "POST", "/admin/chat/read", map[string]int{"upTo": upTo}, nil)
}

// ClearCache deletes one cached credential set (a CacheEntry name), or all of
// them when name is empty. The next request for it refetches.
func (c *Client) ClearCache(ctx context.Context, name string) ([]string, error) {
	var body any = map[string]string{}
	if name != "" {
		body = map[string]string{"name": name}
	}
	var out struct{ Removed []string }
	err := c.do(ctx, "POST", "/admin/cache/clear", body, &out)
	return out.Removed, err
}

// Audit returns recent audit entries, oldest first.
func (c *Client) Audit(ctx context.Context, limit int) ([]AuditEntry, error) {
	var out struct{ Entries []AuditEntry }
	err := c.do(ctx, "GET", fmt.Sprintf("/admin/audit?limit=%d", limit), nil, &out)
	return out.Entries, err
}

// AuditEntryDetail fetches one audit entry in full, output included.
func (c *Client) AuditEntryDetail(ctx context.Context, ts string, requestID int) (AuditEntry, error) {
	var e AuditEntry
	q := url.Values{}
	q.Set("ts", ts)
	q.Set("requestId", fmt.Sprint(requestID))
	err := c.do(ctx, "GET", "/admin/audit/entry?"+q.Encode(), nil, &e)
	return e, err
}

// Event is one item from Subscribe: a new snapshot, or a connection change.
type Event struct {
	State     *State
	Connected bool
	Err       error
}

// Subscribe streams snapshots until ctx ends, reconnecting with backoff. The
// proxy sends one on connect, one per change, and a heartbeat every 5s.
func (c *Client) Subscribe(ctx context.Context) <-chan Event {
	ch := make(chan Event, 4)
	go func() {
		defer close(ch)
		backoff := 500 * time.Millisecond
		for ctx.Err() == nil {
			err := c.streamOnce(ctx, ch)
			if ctx.Err() != nil {
				return
			}
			select {
			case ch <- Event{Connected: false, Err: err}:
			case <-ctx.Done():
				return
			}
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			if backoff < 5*time.Second {
				backoff *= 2
			}
		}
	}()
	return ch
}

func (c *Client) streamOnce(ctx context.Context, ch chan<- Event) error {
	req, err := http.NewRequestWithContext(ctx, "GET", "http://admin/admin/events", nil)
	if err != nil {
		return err
	}
	res, err := c.stream.Do(req)
	if err != nil {
		return friendly(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("event stream: %s", res.Status)
	}
	// A heartbeat arrives every 5s; silence for much longer means a dead link.
	alive := make(chan struct{}, 1)
	go func() {
		t := time.NewTimer(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-alive:
				t.Reset(15 * time.Second)
			case <-t.C:
				res.Body.Close()
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var s State
		if err := json.Unmarshal([]byte(line[6:]), &s); err != nil {
			continue
		}
		select {
		case alive <- struct{}{}:
		default:
		}
		select {
		case ch <- Event{State: &s, Connected: true}:
		case <-ctx.Done():
			return nil
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return errors.New("event stream closed")
}

// AwsLogin is an AWS SSO sign-in in progress (or its outcome).
type AwsLogin struct {
	Session         string    `json:"session"`
	VerificationURL string    `json:"verificationUrl"`
	UserCode        string    `json:"userCode"`
	ExpiresAt       time.Time `json:"expiresAt"`
	Status          string    `json:"status"`
	Detail          string    `json:"detail,omitempty"`
}

// AwsLogin starts signing the supervisor in to an AWS SSO session ("" = the only one).
func (c *Client) AwsLogin(ctx context.Context, session string) (AwsLogin, error) {
	var l AwsLogin
	err := c.do(ctx, "POST", "/admin/aws/login", map[string]string{"session": session}, &l)
	return l, err
}

// AwsStatus: the supervisor's AWS SSO sessions, signed in or not, with any sign-in in progress.
func (c *Client) AwsStatus(ctx context.Context) ([]struct {
	Session  string    `json:"session"`
	SignedIn bool      `json:"signedIn"`
	Login    *AwsLogin `json:"login"`
}, error) {
	var r struct {
		Sessions []struct {
			Session  string    `json:"session"`
			SignedIn bool      `json:"signedIn"`
			Login    *AwsLogin `json:"login"`
		} `json:"sessions"`
	}
	err := c.do(ctx, "GET", "/admin/aws", nil, &r)
	return r.Sessions, err
}

package mcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Command wall clock, enforced by the supervisor. Live values are in
// GET /status under `limits`.
const (
	defaultTimeoutS = 300
	maxTimeoutS     = 900
	// The HTTP request has to outlast the approval wait plus the command
	// itself, or it gives up on a response the supervisor was about to send.
	approvalHeadroom = 210 * time.Second
)

func httpTimeoutFor(commandTimeoutS int) time.Duration {
	return time.Duration(commandTimeoutS)*time.Second + approvalHeadroom
}

// errUnreachable: the request never got an answer, so nothing was decided.
type errUnreachable struct{ msg string }

func (e errUnreachable) Error() string { return e.msg }

// errNoResponse: the request went out but no answer came in time; the
// command may still be waiting for Ben or running.
type errNoResponse struct{ msg string }

func (e errNoResponse) Error() string { return e.msg }

// client talks to the supervisor's agent API on behalf of one Claude session.
type client struct {
	url        string
	sessionID  string
	sessionKey string // proves requests claiming sessionID come from this process
	http       *http.Client
}

func newClient() *client {
	url := os.Getenv("SUPERVISOR_URL")
	if url == "" {
		url = "http://10.42.0.1:9100"
	}
	// The supervisor pins the first key it sees per session ID; grants
	// scoped to a session only apply to requests presenting it. Memory only,
	// never written anywhere: every agent session can read every file the
	// agent can, so a stored key would prove nothing.
	key := make([]byte, 32)
	rand.Read(key)
	return &client{
		url: url, sessionID: os.Getenv("CLAUDE_CODE_SESSION_ID"),
		sessionKey: base64.RawURLEncoding.EncodeToString(key), http: &http.Client{},
	}
}

func (c *client) do(ctx context.Context, method, path string, body any, timeout time.Duration) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url+path, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, errNoResponse{fmt.Sprintf("No response from the supervisor within %ds.", int(timeout.Seconds()))}
		}
		return nil, errUnreachable{err.Error()}
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, errNoResponse{fmt.Sprintf("No response from the supervisor within %ds.", int(timeout.Seconds()))}
		}
		return nil, errUnreachable{err.Error()}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, errUnreachable{"The supervisor returned non-JSON: " + truncate(string(raw), 300)}
	}
	return out, nil
}

func (c *client) get(ctx context.Context, path string) (map[string]any, error) {
	return c.do(ctx, http.MethodGet, path, nil, 30*time.Second)
}

func (c *client) post(ctx context.Context, path string, body any, timeout time.Duration) (map[string]any, error) {
	return c.do(ctx, http.MethodPost, path, body, timeout)
}

// approvalContext tells the supervisor which herdr pane the session runs in,
// so the approval can point Ben at it.
func approvalContext() map[string]any {
	pane, socket := os.Getenv("HERDR_PANE_ID"), os.Getenv("HERDR_SOCKET_PATH")
	if pane == "" || socket == "" {
		return nil
	}
	return map[string]any{"herdr": map[string]string{"pane": pane, "socket": socket}}
}

// openScratchpadToBen lets commands the supervisor runs as ben read files
// staged in this session's scratchpad. Claude creates it 0700 under the
// agent's group, which ben is in; only the group bits are missing. Group
// r-x lets ben read what's staged there without writing to it.
func openScratchpadToBen(sessionID string) {
	if sessionID == "" {
		return
	}
	root := fmt.Sprintf("/tmp/claude-%d", os.Getuid())
	widen := func(dir string) {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			os.Chmod(dir, st.Mode().Perm()|0o050)
		}
	}
	projects, _ := os.ReadDir(root)
	for _, p := range projects {
		session := filepath.Join(root, p.Name(), sessionID)
		scratch := filepath.Join(session, "scratchpad")
		if _, err := os.Stat(scratch); err != nil {
			continue
		}
		// Every component has to be traversable, root-most first.
		for _, d := range []string{root, filepath.Join(root, p.Name()), session, scratch} {
			widen(d)
		}
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const snapshotJSON = `{"snapshot": {
  "workspaces": [
    {"workspace_id": "w1", "label": "~", "focused": true},
    {"workspace_id": "w2", "label": "work", "focused": false}],
  "tabs": [
    {"tab_id": "w1:t1", "workspace_id": "w1", "label": "Supervisor"},
    {"tab_id": "w1:t2", "workspace_id": "w1", "label": "shell"},
    {"tab_id": "w2:t1", "workspace_id": "w2", "label": "supervisor"},
    {"tab_id": "w2:t2", "workspace_id": "w2", "label": "notes"},
    {"tab_id": "w2:t3", "workspace_id": "w2", "label": "notes"}]}}`

func TestFindHerdrTab(t *testing.T) {
	for _, c := range []struct{ ws, tab, want string }{
		{"", "supervisor", "w1:t1"},     // two match: the focused workspace wins
		{"work", "SUPERVISOR", "w2:t1"}, // by workspace label, any case
		{"w2", "supervisor", "w2:t1"},   // by workspace ID
		{"", "w1:t2", "w1:t2"},          // by tab ID
		{"", "notes", ""},               // two in an unfocused workspace: ambiguous
		{"", "missing", ""},
		{"nope", "shell", ""},
	} {
		got, err := FindHerdrTab(json.RawMessage(snapshotJSON), c.ws, c.tab)
		if got != c.want || (c.want == "") != (err != nil) {
			t.Errorf("FindHerdrTab(%q, %q) = %q, %v; want %q", c.ws, c.tab, got, err, c.want)
		}
	}
}

func TestHostAgentRoundTrip(t *testing.T) {
	h := NewHostAgents()
	if err := h.Do(context.Background(), HostJob{Kind: "herdr-focus", Tab: "x"}); err == nil {
		t.Fatal("job done with no agent connected")
	}
	mux := http.NewServeMux()
	h.routes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// A fake agent: fails jobs for tab "bad", does the rest.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/admin/host-agent", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			data, ok := strings.CutPrefix(sc.Text(), "data: ")
			if !ok {
				continue
			}
			var job HostJob
			json.Unmarshal([]byte(data), &job)
			msg := ""
			if job.Tab == "bad" {
				msg = "no herdr tab"
			}
			b, _ := json.Marshal(map[string]any{"id": job.ID, "error": msg})
			r, err := http.Post(srv.URL+"/admin/host-agent/done", "application/json", strings.NewReader(string(b)))
			if err == nil {
				r.Body.Close()
			}
		}
	}()
	for i := 0; i < 100; i++ { // wait for the agent to register
		h.mu.Lock()
		n := len(h.conns)
		h.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := h.Do(context.Background(), HostJob{Kind: "herdr-focus", Tab: "supervisor"}); err != nil {
		t.Errorf("good job: %v", err)
	}
	if err := h.Do(context.Background(), HostJob{Kind: "herdr-focus", Tab: "bad"}); err == nil || err.Error() != "no herdr tab" {
		t.Errorf("bad job: %v", err)
	}
}

func TestDeviceHerdrFocusNeedsAgentAndTarget(t *testing.T) {
	s, c := pairTest(t)
	if status, msg := s.DeviceAction(context.Background(), c, "herdr-focus", 0, map[string]string{"herdr": "host", "tab": "supervisor"}); status != 502 || !strings.Contains(msg, "host agent") {
		t.Errorf("no agent: %d %s", status, msg)
	}
	if status, _ := s.DeviceAction(context.Background(), c, "herdr-focus", 0, map[string]string{"herdr": "elsewhere", "tab": "x"}); status != 502 {
		t.Errorf("unknown herdr: %d", status)
	}
	bad := c
	bad.Token = "nope"
	if status, _ := s.DeviceAction(context.Background(), bad, "herdr-focus", 0, map[string]string{"herdr": "host", "tab": "x"}); status != 401 {
		t.Errorf("unpaired device: %d", status)
	}
}

func TestIsSupervisorTUI(t *testing.T) {
	for argv, want := range map[string]bool{
		"supervisor":                            true,
		"/run/current-system/sw/bin/supervisor": true,
		"supervisor --socket /x":                true,
		"supervisor serve":                      false,
		"supervisor host-agent":                 false,
		"supervisor web --socket /x":            false,
		"zsh":                                   false,
		"":                                      false,
	} {
		if got := IsSupervisorTUI(strings.Fields(argv)); got != want {
			t.Errorf("IsSupervisorTUI(%q) = %v", argv, got)
		}
	}
}

func TestHerdrPanesFocusedFirst(t *testing.T) {
	snap := `{"snapshot": {
	  "workspaces": [{"workspace_id": "w1", "focused": false}, {"workspace_id": "w2", "focused": true}],
	  "panes": [{"pane_id": "w1:p1", "tab_id": "w1:t1", "workspace_id": "w1"},
	            {"pane_id": "w2:p1", "tab_id": "w2:t1", "workspace_id": "w2"}]}}`
	panes, focused, err := HerdrPanes(json.RawMessage(snap))
	if err != nil || focused != "w2" || len(panes) != 2 || panes[0].ID != "w2:p1" {
		t.Errorf("HerdrPanes: %v %q %v", panes, focused, err)
	}
}

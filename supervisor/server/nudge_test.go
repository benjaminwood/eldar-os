package server

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestApprovalNudgesHerdr(t *testing.T) {
	s, r := newTest(t)
	req := ExecRequest{Command: "git", Args: []string{"push"}, Profile: "work", Credentials: creds(), SessionID: "sess", SessionKey: "k",
		Context: &RequestContext{Herdr: &struct {
			Pane   string `json:"pane"`
			Socket string `json:"socket"`
		}{Pane: "w1:p2"}}}
	s.HandleExec(context.Background(), req) // no answer: times out (200ms in tests)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, sp := range r.snapshot() {
			if len(sp.Argv) > 3 && sp.Argv[0] == "herdr" && sp.Argv[1] == "notification" {
				if !strings.HasPrefix(sp.Argv[3], "Approval #") || !strings.Contains(strings.Join(sp.Argv, " "), "git push") || sp.User != "eldar" {
					t.Fatalf("nudge: %v as %s", sp.Argv, sp.User)
				}
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no herdr nudge; runner saw %v", r.snapshot())
}

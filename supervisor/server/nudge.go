package server

import (
	"context"
	"fmt"
	"time"
)

// Approval nudges: when a session in herdr is waiting on Ben, herdr shows a
// notification (with its "request" sound) saying which approval and what for,
// so at the desk he sees a stalled session without opening the approval UI.
// Answering still happens in the supervisor's TUI or web UI. Fire-and-forget:
// a failed nudge never affects the approval.

// nudgeHerdr notifies herdr about approval id, if the asking session is in a
// herdr pane (as it last told the supervisor).
func (s *Supervisor) nudgeHerdr(id int, sessionID, what string) {
	if sessionID == "" || s.runner == nil {
		return
	}
	pane := ""
	for _, sess := range s.sessions.List() {
		if sess.ID == sessionID {
			pane = sess.HerdrPane
		}
	}
	if pane == "" {
		return
	}
	name := pane
	for _, p := range ClaudePanes(s.herdr.View(context.Background(), "", 30*time.Second)) {
		if p.PaneID == pane && p.Title != "" {
			name = p.Title
		}
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		res, err := s.runner.Exec(ctx, ExecSpec{User: s.cfg.AgentUser, Timeout: 10 * time.Second, MaxOutput: 4096,
			Argv: []string{"herdr", "notification", "show", fmt.Sprintf("Approval #%d · %s", id, truncRunes(name, 40)),
				"--body", truncRunes(what, 160) + " — answer in the supervisor", "--sound", "request"}})
		if err != nil || res.ExitCode != 0 {
			logf("herdr nudge #%d: %v %s", id, err, truncRunes(res.Stderr, 200))
		}
	}()
}

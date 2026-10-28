package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The host agent does small things as Ben on the supervisor's own machine,
// for actions that need his login there: the supervisor service itself is
// sandboxed (no sudo, no home directory), which it should stay. It's
// `supervisor host-agent`, a user service of Ben's that connects to the admin
// socket (only the supervisor-admin group can) and takes jobs from
// GET /admin/host-agent. It runs only the fixed jobs it knows, never a command
// it's sent. Today that's focusing a tab in Ben's herdr on the host.

// HostJob is one job for the host agent.
type HostJob struct {
	ID        int    `json:"id"`
	Kind      string `json:"kind"`                // "herdr-focus"
	Workspace string `json:"workspace,omitempty"` // herdr-focus: workspace label or ID ("" = any)
	Tab       string `json:"tab,omitempty"`       // herdr-focus: tab label or ID
}

type HostAgents struct {
	mu      sync.Mutex
	conns   []chan HostJob
	waiting map[int]chan string
	next    int
}

func NewHostAgents() *HostAgents { return &HostAgents{waiting: map[int]chan string{}} }

// Do hands job to the most recently connected agent and waits for its answer.
func (h *HostAgents) Do(ctx context.Context, job HostJob) error {
	h.mu.Lock()
	if len(h.conns) == 0 {
		h.mu.Unlock()
		return errors.New("the host agent isn't running")
	}
	h.next++
	job.ID = h.next
	done := make(chan string, 1)
	h.waiting[job.ID] = done
	conn := h.conns[len(h.conns)-1]
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.waiting, job.ID)
		h.mu.Unlock()
	}()
	select {
	case conn <- job:
	case <-time.After(2 * time.Second):
		return errors.New("the host agent isn't taking jobs")
	}
	select {
	case msg := <-done:
		if msg != "" {
			return errors.New(msg)
		}
		return nil
	case <-time.After(10 * time.Second):
		return errors.New("the host agent didn't answer")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *HostAgents) finish(id int, msg string) {
	h.mu.Lock()
	done := h.waiting[id]
	h.mu.Unlock()
	if done != nil {
		done <- msg
	}
}

// routes: the agent's job stream, and its answers.
func (h *HostAgents) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/host-agent", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			sendJSON(w, 500, map[string]any{"error": "streaming unsupported"})
			return
		}
		jobs := make(chan HostJob)
		h.mu.Lock()
		h.conns = append(h.conns, jobs)
		h.mu.Unlock()
		defer func() {
			h.mu.Lock()
			for i, c := range h.conns {
				if c == jobs {
					h.conns = append(h.conns[:i], h.conns[i+1:]...)
					break
				}
			}
			h.mu.Unlock()
		}()
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(200)
		fl.Flush()
		tick := time.NewTicker(15 * time.Second)
		defer tick.Stop()
		for {
			var frame string
			select {
			case <-r.Context().Done():
				return
			case <-tick.C:
				frame = ": keepalive\n\n"
			case job := <-jobs:
				b, _ := json.Marshal(job)
				frame = "event: job\ndata: " + string(b) + "\n\n"
			}
			if _, err := fmt.Fprint(w, frame); err != nil {
				return
			}
			fl.Flush()
		}
	})
	mux.HandleFunc("POST /admin/host-agent/done", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			ID    int    `json:"id"`
			Error string `json:"error"`
		}
		readJSON(r, &b)
		h.finish(b.ID, truncRunes(b.Error, 300))
		sendJSON(w, 200, map[string]any{"ok": true})
	})
}

// FindHerdrTab finds a tab in a herdr `api snapshot` result by label or ID,
// optionally within one workspace (label or ID). Labels match without regard
// to case; if several tabs match, the one in the focused workspace wins.
func FindHerdrTab(snapshot json.RawMessage, workspace, tab string) (string, error) {
	var s herdrSnapshot
	if err := json.Unmarshal(snapshot, &s); err != nil {
		return "", fmt.Errorf("herdr snapshot: %w", err)
	}
	spaces := map[string]bool{} // workspace ID → focused, for the ones that qualify
	for _, w := range s.Snapshot.Workspaces {
		if workspace == "" || w.ID == workspace || strings.EqualFold(w.Label, workspace) {
			spaces[w.ID] = w.Focused
		}
	}
	if len(spaces) == 0 {
		return "", fmt.Errorf("no herdr workspace %q", workspace)
	}
	var found []string
	for _, t := range s.Snapshot.Tabs {
		if _, ok := spaces[t.Workspace]; ok && (t.ID == tab || strings.EqualFold(t.Label, tab)) {
			if spaces[t.Workspace] {
				return t.ID, nil
			}
			found = append(found, t.ID)
		}
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("no herdr tab %q", tab)
	case 1:
		return found[0], nil
	}
	return "", fmt.Errorf("%d herdr tabs are called %q; name the workspace", len(found), tab)
}

// focusHerdrTab focuses a tab in one of the herdrs a device can steer:
// "host" (Ben's, through the host agent) or "eldar" (the one in Eldar's VM,
// which the supervisor drives itself).
func (s *Supervisor) focusHerdrTab(ctx context.Context, which, workspace, tab string) error {
	if tab == "" || len(tab) > 80 || len(workspace) > 80 {
		return errors.New("need a tab (label or ID)")
	}
	switch which {
	case "host":
		return s.hostAgents.Do(ctx, HostJob{Kind: "herdr-focus", Workspace: workspace, Tab: tab})
	case "eldar":
		session := s.herdr.DefaultSession
		raw, err := s.herdr.run(ctx, session, 10*time.Second, "api", "snapshot")
		if err != nil {
			return err
		}
		id, err := FindHerdrTab(raw, workspace, tab)
		if err != nil {
			return err
		}
		if _, err := s.herdr.run(ctx, session, 10*time.Second, "tab", "focus", id); err != nil {
			return err
		}
		s.herdr.Invalidate()
		return nil
	}
	return fmt.Errorf("unknown herdr %q (host or eldar)", which)
}

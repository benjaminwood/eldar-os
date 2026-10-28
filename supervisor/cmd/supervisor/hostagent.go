package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/benjaminwood/eldar-os/supervisor/api"
	"github.com/benjaminwood/eldar-os/supervisor/server"
)

// runHostAgent is `supervisor host-agent`: Ben's user service on the
// supervisor's machine, doing the fixed jobs that need his login there
// (server/hostagent.go). It takes jobs over the admin socket, so only a member
// of supervisor-admin can run it, and runs no command it's sent: each kind of
// job is code here.
func runHostAgent(args []string) {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "usage: supervisor host-agent")
		os.Exit(2)
	}
	log.SetFlags(0)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	backoff := time.Second
	for ctx.Err() == nil {
		sock, err := api.FindSocket()
		if err == nil {
			start := time.Now()
			err = hostAgentOnce(ctx, sock)
			if time.Since(start) > time.Minute {
				backoff = time.Second
			}
		}
		if ctx.Err() != nil {
			return
		}
		log.Printf("host-agent: %v; reconnecting in %s", err, backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func hostAgentOnce(ctx context.Context, sock string) error {
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}}
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://admin/admin/host-agent", nil)
	req.Header.Set("X-Admin-Client", "host-agent")
	res, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("job stream: %s", res.Status)
	}
	log.Printf("host-agent: connected to %s", sock)
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var job server.HostJob
		if json.Unmarshal([]byte(data), &job) != nil {
			continue
		}
		msg := ""
		if err := doHostJob(ctx, job); err != nil {
			msg = err.Error()
			log.Printf("host-agent: job %d (%s %s): %v", job.ID, job.Kind, job.Tab, err)
		}
		b, _ := json.Marshal(map[string]any{"id": job.ID, "error": msg})
		done, _ := http.NewRequestWithContext(ctx, "POST", "http://admin/admin/host-agent/done", bytes.NewReader(b))
		done.Header.Set("Content-Type", "application/json")
		if r, err := hc.Do(done); err == nil {
			r.Body.Close()
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return errors.New("job stream closed")
}

func doHostJob(ctx context.Context, job server.HostJob) error {
	switch job.Kind {
	case "herdr-focus":
		snap, err := herdrJSON(ctx, "api", "snapshot")
		if err != nil {
			return err
		}
		id, err := server.FindHerdrTab(snap, job.Workspace, job.Tab)
		if err != nil {
			return err
		}
		_, err = herdrJSON(ctx, "tab", "focus", id)
		return err
	}
	return fmt.Errorf("unknown job %q", job.Kind)
}

// herdrJSON runs Ben's herdr and returns its JSON result.
func herdrJSON(ctx context.Context, args ...string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "herdr", args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String() + string(out)); msg != "" {
			return nil, fmt.Errorf("herdr %s: %s", args[0], msg)
		}
		return nil, fmt.Errorf("herdr %s: %w", args[0], err)
	}
	var r struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return nil, fmt.Errorf("herdr %s: unexpected output", args[0])
	}
	return r.Result, nil
}

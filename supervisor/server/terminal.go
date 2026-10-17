package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Live terminals onto herdr panes (Claude sessions or shells) for the web
// UI's takeover view: WebSocket on the admin socket at
// /admin/terminal?pane=<id>[&cols&rows&session]. Binary frames are terminal
// bytes both ways; text frames are JSON control messages from the client
// ({"type":"resize","cols":N,"rows":N}).
//
// Each one is `herdr terminal attach <terminal> --takeover` in a real PTY in
// Eldar's VM, as the agent user, over SSH: just that pane's terminal, no herdr
// chrome, and Ben's desk layout is left alone. Closing the socket closes the
// SSH session, which hangs up the attach client.

// TerminalSSH is how the supervisor reaches the VM for terminals.
type TerminalSSH struct {
	Host, KeyPath, KnownHosts, User string
}

func (t TerminalSSH) dial(ctx context.Context) (*ssh.Client, error) {
	key, err := os.ReadFile(t.KeyPath)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, err
	}
	hostKeys, err := knownhosts.New(t.KnownHosts)
	if err != nil {
		return nil, err
	}
	addr := net.JoinHostPort(t.Host, "22")
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, &ssh.ClientConfig{
		User: t.User, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: hostKeys, Timeout: 10 * time.Second,
	})
	if err != nil {
		conn.Close()
		return nil, err
	}
	return ssh.NewClient(c, chans, reqs), nil
}

func clampInt(s string, lo, hi, def int) int {
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return min(max(v, lo), hi)
}

func (s *Supervisor) handleTerminal(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	paneID, session := q.Get("pane"), q.Get("session")
	if session == "" {
		session = s.herdr.DefaultSession
	}
	cols, rows := clampInt(q.Get("cols"), 20, 400, 80), clampInt(q.Get("rows"), 5, 200, 24)
	if !validPane.MatchString(paneID) || !validHerdrSession.MatchString(session) {
		sendJSON(w, 400, map[string]any{"error": "bad pane or session"})
		return
	}
	terminal, label, err := s.herdr.paneTerminal(r.Context(), session, paneID)
	if err != nil {
		sendJSON(w, 404, map[string]any{"error": err.Error()})
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true}) // unix socket; the web UI relays
	if err != nil {
		return
	}
	ws.SetReadLimit(1 << 20)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fail := func(msg string) { ws.Close(websocket.StatusInternalError, truncRunes(msg, 120)) }

	client, err := s.termSSH.dial(ctx)
	if err != nil {
		fail("can't reach Eldar's VM: " + err.Error())
		return
	}
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		fail(err.Error())
		return
	}
	defer sess.Close()
	if err := sess.RequestPty("xterm-256color", rows, cols, ssh.TerminalModes{ssh.ECHO: 1}); err != nil {
		fail(err.Error())
		return
	}
	stdin, _ := sess.StdinPipe()
	stdout, _ := sess.StdoutPipe()
	sess.Stderr = sess.Stdout
	sa, _ := sessionArgs(session)
	cmd := "herdr"
	for _, a := range append(sa, "terminal", "attach", terminal, "--takeover") {
		cmd += " " + shellQuote(a)
	}
	if err := sess.Start("COLORTERM=truecolor exec " + cmd); err != nil {
		fail(err.Error())
		return
	}
	started := time.Now()
	s.adminLog(r, "TERMINAL OPEN "+paneID, "Terminal attached to "+label)
	why := "client disconnected"

	// VM → browser
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 32*1024)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				if ws.Write(ctx, websocket.MessageBinary, buf[:n]) != nil {
					return
				}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					why = "stream error"
				} else {
					why = "session ended"
				}
				return
			}
		}
	}()
	// Keep NAT and proxies from idling the socket out.
	go func() {
		t := time.NewTicker(25 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pctx, c := context.WithTimeout(ctx, 10*time.Second)
				ws.Ping(pctx)
				c()
			}
		}
	}()
	// browser → VM
	go func() {
		defer cancel()
		for {
			typ, data, err := ws.Read(ctx)
			if err != nil {
				return
			}
			if typ == websocket.MessageBinary {
				stdin.Write(data)
				continue
			}
			var msg struct {
				Type       string
				Cols, Rows int
			}
			if json.Unmarshal(data, &msg) == nil && msg.Type == "resize" {
				sess.WindowChange(min(max(msg.Rows, 1), 500), min(max(msg.Cols, 1), 1000))
			}
		}
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
	ws.Close(websocket.StatusNormalClosure, "")
	s.adminLog(r, "TERMINAL CLOSE "+paneID, fmt.Sprintf("Terminal detached from %s after %ds (%s)", label, int(time.Since(started).Seconds()), why))
}

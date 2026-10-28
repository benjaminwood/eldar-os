// Package tools lends an agent's commands to a sandboxed agent.
//
// The owner (Eldar) lists commands in a grants file; a relay running as the
// owner listens on a Unix socket, the one thing the sandbox may reach. In the
// sandbox, each granted command is a stub that sends its arguments (and its
// stdin) to the relay, which runs the owner's real command, as the owner and
// with the owner's environment, and streams its output and exit status back.
// So the sandboxed agent uses the owner's tools without seeing their source,
// their credentials or anything else of the owner's, and a grant takes effect
// (or is revoked) the moment the file changes.
//
//	commands:
//	  budget:
//	    run: ~/workspace/projects/budget/bin/budget
//	    description: The household budget: accounts, transactions, spending, ...
//	    allow: [accounts, transactions]  # optional: permitted first arguments
//	    timeout: 10m                     # optional; default 10m
package tools

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

// Grant is one command lent to the sandbox.
type Grant struct {
	Run         string   `yaml:"run"`
	Description string   `yaml:"description"`
	Allow       []string `yaml:"allow"`
	Timeout     string   `yaml:"timeout"`
}

type grantsFile struct {
	Commands map[string]Grant `yaml:"commands"`
}

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// LoadGrants reads the grants file; a missing file grants nothing. A `~/` in
// run is the owner's home.
func LoadGrants(path string) (map[string]Grant, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Grant{}, nil
	} else if err != nil {
		return nil, err
	}
	var f grantsFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	home, _ := os.UserHomeDir()
	out := map[string]Grant{}
	for name, g := range f.Commands {
		if !validName.MatchString(name) {
			return nil, fmt.Errorf("%s: bad command name %q", path, name)
		}
		if strings.HasPrefix(g.Run, "~/") {
			g.Run = filepath.Join(home, g.Run[2:])
		}
		if !filepath.IsAbs(g.Run) {
			return nil, fmt.Errorf("%s: %s: run must be an absolute path (or ~/...)", path, name)
		}
		if g.Timeout != "" {
			if _, err := time.ParseDuration(g.Timeout); err != nil {
				return nil, fmt.Errorf("%s: %s: bad timeout %q", path, name, g.Timeout)
			}
		}
		out[name] = g
	}
	return out, nil
}

// Relay serves the socket and keeps the stubs in StubDir in step with the grants.
type Relay struct {
	GrantsPath string
	StubDir    string // where the sandbox finds the granted commands
	Client     string // the program a stub runs: `<Client> tool <name> "$@"`
	Socket     string
	Log        *log.Logger
	// The sandboxed agent's herdr tab ("workspace/tab"), and herdr's Claude
	// hook: the agent can't reach herdr's socket (it controls every pane),
	// so the relay passes its session report on, for that tab's pane only.
	HerdrTab, HerdrHook string

	mu     sync.Mutex
	raw    []byte
	grants map[string]Grant
}

// current returns the grants as the file says now, re-reading it when it
// changed; a file that no longer parses keeps the previous grants.
func (r *Relay) current() map[string]Grant {
	r.mu.Lock()
	defer r.mu.Unlock()
	raw, err := os.ReadFile(r.GrantsPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return r.grants
	}
	if r.grants != nil && string(raw) == string(r.raw) {
		return r.grants
	}
	r.raw = raw
	g, err := LoadGrants(r.GrantsPath)
	if err != nil {
		r.Log.Printf("grants: %v; keeping the previous grants", err)
		if r.grants == nil {
			r.grants = map[string]Grant{}
		}
		return r.grants
	}
	r.grants = g
	names := make([]string, 0, len(g))
	for n := range g {
		names = append(names, n)
	}
	sort.Strings(names)
	r.Log.Printf("grants: %s", strings.Join(names, ", "))
	if err := r.writeStubs(g); err != nil {
		r.Log.Printf("stubs: %v", err)
	}
	return g
}

const stubMark = "# tool stub (supervisor tools relay)"

// writeStubs makes StubDir hold exactly one stub per granted command.
func (r *Relay) writeStubs(g map[string]Grant) error {
	if r.StubDir == "" {
		return nil
	}
	if err := os.MkdirAll(r.StubDir, 0o755); err != nil {
		return err
	}
	entries, _ := os.ReadDir(r.StubDir)
	for _, e := range entries {
		p := filepath.Join(r.StubDir, e.Name())
		if _, ok := g[e.Name()]; !ok {
			if b, err := os.ReadFile(p); err == nil && strings.Contains(string(b), stubMark) {
				os.Remove(p)
			}
		}
	}
	for name := range g {
		stub := fmt.Sprintf("#!/bin/sh\n%s\nexec %s tool %s \"$@\"\n", stubMark, r.Client, name)
		p := filepath.Join(r.StubDir, name)
		if b, err := os.ReadFile(p); err == nil && string(b) == stub {
			continue
		}
		tmp := p + ".tmp"
		if err := os.WriteFile(tmp, []byte(stub), 0o755); err != nil {
			return err
		}
		if err := os.Rename(tmp, p); err != nil {
			return err
		}
	}
	return nil
}

// Request is the first line a client sends; its stdin follows, up to the
// client closing its write side.
type Request struct {
	Tool string   `json:"tool"`
	Args []string `json:"args"`
}

// Frames from the relay: a byte for the stream, a 4-byte length, the data.
const (
	frameStdout = 1
	frameStderr = 2
	frameExit   = 3 // data: the exit status, in decimal
)

func writeFrame(w io.Writer, mu *sync.Mutex, kind byte, data []byte) error {
	mu.Lock()
	defer mu.Unlock()
	var hdr [5]byte
	hdr[0] = kind
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(data)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(data)
	return err
}

type frameWriter struct {
	w    io.Writer
	mu   *sync.Mutex
	kind byte
	n    int64
}

func (f *frameWriter) Write(p []byte) (int, error) {
	n := len(p)
	f.n += int64(n)
	for len(p) > 0 {
		chunk := p[:min(len(p), 1<<16)]
		if err := writeFrame(f.w, f.mu, f.kind, chunk); err != nil {
			return 0, err
		}
		p = p[len(chunk):]
	}
	return n, nil
}

// Serve listens on the socket until ctx ends.
func (r *Relay) Serve(ctx context.Context) error {
	os.Remove(r.Socket)
	ln, err := net.Listen("unix", r.Socket)
	if err != nil {
		return err
	}
	// Owner and group: the socket's directory (setgid) gives it the
	// sandboxed agent's group, and its mode keeps everyone else out.
	os.Chmod(r.Socket, 0o660)
	go func() { <-ctx.Done(); ln.Close() }()
	r.current() // load the grants and write the stubs now
	go func() {
		for t := time.NewTicker(2 * time.Second); ; {
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
				r.current() // stubs follow the file without a request
			}
		}
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go r.handle(ctx, c.(*net.UnixConn))
	}
}

func (r *Relay) handle(ctx context.Context, c *net.UnixConn) {
	defer c.Close()
	var mu sync.Mutex
	fail := func(status int, format string, a ...any) {
		writeFrame(c, &mu, frameStderr, []byte(fmt.Sprintf(format, a...)+"\n"))
		writeFrame(c, &mu, frameExit, []byte(strconv.Itoa(status)))
	}
	br := bufio.NewReader(c)
	line, err := br.ReadBytes('\n')
	if err != nil {
		return
	}
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		fail(2, "tools: bad request")
		return
	}
	if req.Tool == "@herdr-session" {
		if err := r.reportSession(ctx, br); err != nil {
			r.Log.Printf("herdr session report: %v", err)
			fail(1, "herdr session report: %v", err)
			return
		}
		writeFrame(c, &mu, frameExit, []byte("0"))
		return
	}
	if req.Tool == "" { // the list: what's granted, and what each is for
		grants := r.current()
		names := make([]string, 0, len(grants))
		for n := range grants {
			names = append(names, n)
		}
		sort.Strings(names)
		var b strings.Builder
		for _, n := range names {
			fmt.Fprintf(&b, "%s: %s\n", n, strings.TrimSpace(grants[n].Description))
			if a := grants[n].Allow; len(a) > 0 {
				fmt.Fprintf(&b, "  subcommands: %s\n", strings.Join(a, ", "))
			}
		}
		if len(names) == 0 {
			b.WriteString("(no tools granted)\n")
		}
		writeFrame(c, &mu, frameStdout, []byte(b.String()))
		writeFrame(c, &mu, frameExit, []byte("0"))
		return
	}
	g, ok := r.current()[req.Tool]
	if !ok {
		r.Log.Printf("refused %s: not granted", req.Tool)
		fail(126, "%s: not a granted tool", req.Tool)
		return
	}
	if len(g.Allow) > 0 && (len(req.Args) == 0 || !slices.Contains(g.Allow, req.Args[0])) {
		r.Log.Printf("refused %s %s: not an allowed subcommand", req.Tool, firstArg(req.Args))
		fail(126, "%s: allowed subcommands are: %s", req.Tool, strings.Join(g.Allow, ", "))
		return
	}
	timeout := 10 * time.Minute
	if g.Timeout != "" {
		timeout, _ = time.ParseDuration(g.Timeout)
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, g.Run, req.Args...)
	cmd.Dir = filepath.Dir(g.Run)
	cmd.Stdin = br // the rest of the connection: the client's stdin
	stdout := &frameWriter{w: c, mu: &mu, kind: frameStdout}
	stderr := &frameWriter{w: c, mu: &mu, kind: frameStderr}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	start := time.Now()
	err = cmd.Run()
	status := 0
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		status = ee.ExitCode()
		if status < 0 {
			status = 128 + 9
		}
	default:
		status = 127
		fmt.Fprintf(stderr, "%s: %v\n", req.Tool, err)
	}
	if cctx.Err() == context.DeadlineExceeded {
		fmt.Fprintf(stderr, "%s: stopped after %s\n", req.Tool, timeout)
	}
	writeFrame(c, &mu, frameExit, []byte(strconv.Itoa(status)))
	r.Log.Printf("ran %s %s: exit %d in %s, %d bytes out", req.Tool, summarize(req.Args), status,
		time.Since(start).Round(time.Millisecond), stdout.n)
}

func firstArg(a []string) string {
	if len(a) == 0 {
		return "(none)"
	}
	return a[0]
}

// summarize: the arguments for the log, shortened.
func summarize(args []string) string {
	s := strings.Join(args, " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// Call runs a granted tool through the relay: stdin goes to it (when stdin
// isn't a terminal), its output to stdout and stderr; it returns the exit status.
func Call(socket, tool string, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	c, err := net.Dial("unix", socket)
	if err != nil {
		return 0, fmt.Errorf("tools relay unavailable: %w", err)
	}
	defer c.Close()
	uc := c.(*net.UnixConn)
	b, _ := json.Marshal(Request{Tool: tool, Args: args})
	if _, err := uc.Write(append(b, '\n')); err != nil {
		return 0, err
	}
	go func() {
		if stdin != nil {
			io.Copy(uc, stdin)
		}
		uc.CloseWrite()
	}()
	br := bufio.NewReader(uc)
	for {
		var hdr [5]byte
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			return 0, fmt.Errorf("tools relay: connection lost")
		}
		data := make([]byte, binary.BigEndian.Uint32(hdr[1:]))
		if _, err := io.ReadFull(br, data); err != nil {
			return 0, fmt.Errorf("tools relay: connection lost")
		}
		switch hdr[0] {
		case frameStdout:
			stdout.Write(data)
		case frameStderr:
			stderr.Write(data)
		case frameExit:
			return strconv.Atoi(string(data))
		}
	}
}

// reportSession runs herdr's Claude hook for the sandboxed agent's tab, with
// the hook input the agent's Claude gave its own SessionStart hook.
func (r *Relay) reportSession(ctx context.Context, hookInput io.Reader) error {
	if r.HerdrTab == "" || r.HerdrHook == "" {
		return errors.New("not configured")
	}
	ws, tab, _ := strings.Cut(r.HerdrTab, "/")
	herdr := func(args ...string) (map[string]any, error) {
		out, err := exec.CommandContext(ctx, "herdr", args...).Output()
		if err != nil {
			return nil, fmt.Errorf("herdr %s: %w", args[0], err)
		}
		var v struct {
			Result map[string]any `json:"result"`
		}
		return v.Result, json.Unmarshal(out, &v)
	}
	find := func(res map[string]any, key, field, want, id string) string {
		items, _ := res[key].([]any)
		for _, x := range items {
			m, _ := x.(map[string]any)
			if fmt.Sprint(m[field]) == want {
				return fmt.Sprint(m[id])
			}
		}
		return ""
	}
	res, err := herdr("workspace", "list")
	if err != nil {
		return err
	}
	wsID := find(res, "workspaces", "label", ws, "workspace_id")
	if wsID == "" {
		return fmt.Errorf("no herdr workspace %q", ws)
	}
	if res, err = herdr("tab", "list", "--workspace", wsID); err != nil {
		return err
	}
	tabID := find(res, "tabs", "label", tab, "tab_id")
	if tabID == "" {
		return fmt.Errorf("no tab %q in %s", tab, ws)
	}
	if res, err = herdr("pane", "list", "--workspace", wsID); err != nil {
		return err
	}
	pane := find(res, "panes", "tab_id", tabID, "pane_id")
	if pane == "" {
		return fmt.Errorf("no pane in tab %s", tabID)
	}
	sock := os.Getenv("HERDR_SOCKET_PATH")
	if sock == "" {
		home, _ := os.UserHomeDir()
		sock = filepath.Join(home, ".config/herdr/herdr.sock")
	}
	cmd := exec.CommandContext(ctx, "sh", r.HerdrHook, "session")
	cmd.Stdin = io.LimitReader(hookInput, 1<<20)
	cmd.Env = append(os.Environ(), "HERDR_ENV=1", "HERDR_PANE_ID="+pane, "HERDR_SOCKET_PATH="+sock)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	r.Log.Printf("passed on the session report for %s (%s)", r.HerdrTab, pane)
	return nil
}

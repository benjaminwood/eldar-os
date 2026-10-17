package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ExecSpec is one command to run in the agent's environment.
type ExecSpec struct {
	// Exactly one of Argv (quoted for the shell) or Shell (run verbatim).
	Argv  []string
	Shell string

	Env       map[string]string // credentials, exported only inside the script
	Cwd       string
	User      string        // "" = the runner's default (ben)
	Timeout   time.Duration // 0 = none; enforced in the environment with `timeout`
	MaxOutput int           // bytes kept per stream; 0 = unlimited
}

type ExecResult struct {
	ExitCode                 int
	Stdout, Stderr           string
	TimedOut, Truncated      bool
	StdoutBytes, StderrBytes int64
}

// Runner runs commands where the agent works.
type Runner interface {
	Exec(ctx context.Context, s ExecSpec) (ExecResult, error)
}

// SSHRunner runs commands in Eldar's VM over SSH. The command and any
// credentials travel in a script on stdin, so they never appear in a process
// list on either side.
type SSHRunner struct {
	Host        string // e.g. 10.42.0.2
	KeyPath     string
	KnownHosts  string
	DefaultUser string
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Script is the shell script the runner sends (exported for tests).
func (r *SSHRunner) Script(s ExecSpec) (string, error) {
	cmd := s.Shell
	if cmd == "" {
		if len(s.Argv) == 0 {
			return "", errors.New("empty command")
		}
		q := make([]string, len(s.Argv))
		for i, a := range s.Argv {
			q[i] = shellQuote(a)
		}
		cmd = strings.Join(q, " ")
	}
	var b strings.Builder
	// Group-writable files: the workspace is shared between eldar and ben.
	b.WriteString("umask 002\n")
	keys := make([]string, 0, len(s.Env))
	for k := range s.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !envName.MatchString(k) {
			return "", fmt.Errorf("credential name %q isn't a valid environment variable name", k)
		}
		fmt.Fprintf(&b, "export %s=%s\n", k, shellQuote(s.Env[k]))
	}
	if s.Cwd != "" {
		fmt.Fprintf(&b, "cd -- %s || exit 1\n", shellQuote(s.Cwd))
	}
	// bash, not sh: callers write bash (pipefail, [[ ]], arrays). `timeout`
	// signals the whole process group; -k 5 escalates to SIGKILL; 124 = fired.
	if secs := int(s.Timeout / time.Second); secs > 0 {
		fmt.Fprintf(&b, "exec timeout -k 5 %d bash -c %s\n", secs, shellQuote(cmd))
	} else {
		fmt.Fprintf(&b, "exec bash -c %s\n", shellQuote(cmd))
	}
	return b.String(), nil
}

func (r *SSHRunner) sshArgs(user string) []string {
	return []string{
		"-i", r.KeyPath,
		"-o", "BatchMode=yes",
		"-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + r.KnownHosts,
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=15",
		"-o", "LogLevel=ERROR",
		user + "@" + r.Host,
		"bash -s",
	}
}

func (r *SSHRunner) Exec(ctx context.Context, s ExecSpec) (ExecResult, error) {
	script, err := r.Script(s)
	if err != nil {
		return ExecResult{}, err
	}
	user := s.User
	if user == "" {
		user = r.DefaultUser
	}
	// Backstop if the connection itself hangs; the command's own clock runs
	// inside the VM.
	if s.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.Timeout+30*time.Second)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, "ssh", r.sshArgs(user)...)
	cmd.Stdin = strings.NewReader(script)
	out, errw := &capped{limit: s.MaxOutput}, &capped{limit: s.MaxOutput}
	cmd.Stdout, cmd.Stderr = out, errw
	err = cmd.Run()
	res := ExecResult{
		Stdout: out.buf.String(), Stderr: errw.buf.String(),
		StdoutBytes: out.total, StderrBytes: errw.total,
		Truncated: out.truncated() || errw.truncated(),
	}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		res.ExitCode = ee.ExitCode()
		if res.ExitCode == -1 { // killed by our backstop
			res.ExitCode, res.TimedOut = 124, true
		}
	default:
		return res, err
	}
	if s.Timeout > 0 && (res.ExitCode == 124 || res.ExitCode == 137) {
		res.TimedOut = true
	}
	return res, nil
}

// ReadFile returns a file's contents from the agent's environment (the
// file-based /exec mode).
func (r *SSHRunner) ReadFile(ctx context.Context, path string) (string, error) {
	res, err := r.Exec(ctx, ExecSpec{Argv: []string{"cat", "--", path}, Timeout: 30 * time.Second, MaxOutput: 1 << 20})
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", errors.New(strings.TrimSpace(res.Stderr))
	}
	return res.Stdout, nil
}

// capped keeps at most limit bytes (0 = unlimited) but counts everything, so
// callers can report what was dropped.
type capped struct {
	buf   bytes.Buffer
	limit int
	total int64
}

func (c *capped) Write(p []byte) (int, error) {
	c.total += int64(len(p))
	if c.limit <= 0 {
		return c.buf.Write(p)
	}
	if room := c.limit - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

func (c *capped) truncated() bool { return c.limit > 0 && c.total > int64(c.buf.Len()) }

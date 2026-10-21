package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/benjaminwood/eldar-os/supervisor/api"
	"github.com/benjaminwood/eldar-os/supervisor/view"
)

// File copies between Eldar's VM and the remote hosts (POST /copy). The
// supervisor streams the file itself: `cat` at the source over one SSH
// connection, into a temp file at the destination over another, hashing and
// counting the bytes as they pass. Neither end gets a key to the other, the
// file never passes through an agent's context, and the audit records paths,
// size and hash, never contents.
//
// Ben approves a file by size and sha256, and the destination keeps it only if
// what arrived hashes the same, so a file that changes after approval isn't
// copied. The VM end runs as the agent user: a copy reads and writes only what
// Eldar could. A host's grants cover sending files to it; pulling a file off a
// host always asks, and copies have no allow list.

// CopyRequest is the body of POST /copy.
type CopyRequest struct {
	From           string          `json:"from"` // "<target>:<path>", e.g. "eldar:/tmp/x.csv"
	To             string          `json:"to"`   // a file, or a directory ending in "/"
	Overwrite      bool            `json:"overwrite,omitempty"`
	Reason         string          `json:"reason,omitempty"`
	TimeoutSeconds float64         `json:"timeoutSeconds,omitempty"`
	SessionID      string          `json:"sessionId,omitempty"`
	SessionKey     string          `json:"sessionKey,omitempty"`
	Context        *RequestContext `json:"context,omitempty"`
}

// copyEnd is one side of a copy: Eldar's VM or a host, and a path there.
type copyEnd struct {
	target, label, path string
}

func (e copyEnd) String() string { return e.target + ":" + e.path }

// sh quotes the path for a POSIX shell there, expanding a leading ~.
func (e copyEnd) sh() string {
	if e.path == "~" {
		return `"$HOME"`
	}
	if rest, ok := strings.CutPrefix(e.path, "~/"); ok {
		return `"$HOME"/` + shellQuote(rest)
	}
	return shellQuote(e.path)
}

var (
	copyTarget = regexp.MustCompile(`^([a-z][a-z0-9-]{0,30}):(.+)$`)
	sha256Hex  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func (s *Supervisor) parseCopyEnd(spec string) (copyEnd, error) {
	m := copyTarget.FindStringSubmatch(spec)
	if m == nil {
		return copyEnd{}, fmt.Errorf("%q isn't <target>:<path>, e.g. eldar:/tmp/report.csv or mac:~/Downloads/", spec)
	}
	e := copyEnd{target: m[1], path: m[2], label: "Eldar's VM"}
	if e.target != "eldar" {
		h := s.host(e.target)
		if h == nil {
			return e, fmt.Errorf("unknown target %q; use eldar or a configured host (%s)", e.target, strings.Join(s.hostNames(), ", "))
		}
		if !h.Configured() {
			return e, fmt.Errorf("%s isn't reachable from this supervisor (no key)", h.Label)
		}
		e.label = h.Label
	}
	if !strings.HasPrefix(e.path, "/") && e.path != "~" && !strings.HasPrefix(e.path, "~/") {
		return e, fmt.Errorf("%q: paths are absolute or start with ~/", spec)
	}
	if strings.ContainsAny(e.path, "\x00\n") {
		return e, fmt.Errorf("%q: the path has a newline or NUL in it", spec)
	}
	return e, nil
}

// hostCommand runs script under sh on a host, with stdin and stdout left to
// the caller (a variable so tests can stand in for SSH).
var hostCommand = func(ctx context.Context, h *RemoteHost, script string) *exec.Cmd {
	return exec.CommandContext(ctx, "ssh",
		"-i", h.KeyPath, "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
		"-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile="+h.KnownHosts,
		"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=4", "-o", "LogLevel=ERROR",
		h.Target, "sh -c "+shellQuote(script))
}

// commander is a runner that can run a script with the caller's stdin and
// stdout (SSHRunner).
type commander interface {
	Command(ctx context.Context, user, script string) *exec.Cmd
}

func (s *Supervisor) copyCmd(ctx context.Context, target, script string) (*exec.Cmd, error) {
	if target != "eldar" {
		return hostCommand(ctx, s.host(target), script), nil
	}
	r, ok := s.runner.(commander)
	if !ok {
		return nil, errors.New("this supervisor's runner can't stream files")
	}
	return r.Command(ctx, s.cfg.AgentUser, script), nil
}

// shaFn defines __h FILE: the file's sha256 in hex, with whichever tool the
// system has (sha256sum on Linux, shasum on macOS).
const shaFn = `__h() { if command -v sha256sum >/dev/null 2>&1; then sha256sum < "$1"; else shasum -a 256 < "$1"; fi | cut -d' ' -f1; }
`

// probe runs a short script at one end and returns its stdout lines.
func (s *Supervisor) probe(ctx context.Context, target, script string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd, err := s.copyCmd(ctx, target, script)
	if err != nil {
		return nil, err
	}
	var out, errw bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errw
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(errw.String()); msg != "" {
			return nil, errors.New(msg)
		}
		return nil, err
	}
	return strings.Split(strings.TrimSpace(out.String()), "\n"), nil
}

// copySource: the source's size, sha256 and file type.
func (s *Supervisor) copySource(ctx context.Context, from copyEnd) (size int64, sum, kind string, err error) {
	lines, err := s.probe(ctx, from.target, shaFn+fmt.Sprintf(`p=%s
[ -f "$p" ] || { echo "not a file: $p" >&2; exit 3; }
[ -r "$p" ] || { echo "can't read $p" >&2; exit 3; }
wc -c < "$p" | tr -d ' '
__h "$p"
file -b "$p" 2>/dev/null | head -c 200 || true
`, from.sh()))
	if err != nil {
		return 0, "", "", err
	}
	if len(lines) < 2 {
		return 0, "", "", fmt.Errorf("couldn't read %s's size and hash", from)
	}
	size, err = strconv.ParseInt(lines[0], 10, 64)
	if err != nil || !sha256Hex.MatchString(lines[1]) {
		return 0, "", "", fmt.Errorf("couldn't read %s's size and hash", from)
	}
	if len(lines) > 2 {
		kind = lines[2]
	}
	return size, lines[1], kind, nil
}

// copyDest resolves the destination (a directory gets the source's name) and
// checks it can be written: its directory exists and, unless overwriting,
// nothing is there yet. Returns the absolute path.
func (s *Supervisor) copyDest(ctx context.Context, to copyEnd, name string, overwrite bool) (string, error) {
	dir := "0"
	if strings.HasSuffix(to.path, "/") || to.path == "~" {
		dir = "1"
	}
	lines, err := s.probe(ctx, to.target, fmt.Sprintf(`d=%s
if [ -d "$d" ]; then d="${d%%/}"/%s
elif [ %s = 1 ]; then echo "no such directory: $d" >&2; exit 3
fi
p=$(dirname -- "$d")
[ -d "$p" ] || { echo "no such directory: $p" >&2; exit 3; }
[ -w "$p" ] || { echo "can't write to $p" >&2; exit 3; }
[ %t = true ] || [ ! -e "$d" ] || { echo "$d exists (set overwrite to replace it)" >&2; exit 4; }
[ ! -d "$d" ] || { echo "$d is a directory" >&2; exit 4; }
case $d in /*) ;; *) d="$PWD/$d" ;; esac
printf '%%s\n' "$d"
`, to.sh(), shellQuote(name), dir, overwrite))
	if err != nil {
		return "", err
	}
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "/") {
		return "", fmt.Errorf("couldn't resolve %s", to)
	}
	return lines[0], nil
}

// stream copies the approved file: the source's bytes into a temp file next
// to dest, kept only if they hash to sum. Returns the bytes and hash that
// passed through.
func (s *Supervisor) stream(ctx context.Context, from copyEnd, toTarget, dest string, size int64, sum string, overwrite bool) (int64, string, error) {
	src, err := s.copyCmd(ctx, from.target, "exec cat -- "+from.sh())
	if err != nil {
		return 0, "", err
	}
	dst, err := s.copyCmd(ctx, toTarget, shaFn+fmt.Sprintf(`d=%s
t=$(mktemp "$(dirname -- "$d")/.supervisor-copy.XXXXXX") || exit 3
trap 'rm -f "$t"' EXIT
cat > "$t" || exit 3
h=$(__h "$t")
[ "$h" = %s ] || { echo "what arrived doesn't match the approved file (sha256 $h)" >&2; exit 5; }
[ %t = true ] || [ ! -e "$d" ] || { echo "$d appeared while copying; not replaced" >&2; exit 4; }
mv -f "$t" "$d" || exit 3
trap - EXIT
echo "$h"
`, shellQuote(dest), sum, overwrite))
	if err != nil {
		return 0, "", err
	}
	out, err := src.StdoutPipe()
	if err != nil {
		return 0, "", err
	}
	var srcErr, dstErr, dstOut capped
	srcErr.limit, dstErr.limit, dstOut.limit = 4096, 4096, 4096
	src.Stderr = &srcErr
	hash := sha256.New()
	// One byte past the approved size is enough to know the file grew.
	n := &counter{r: io.TeeReader(io.LimitReader(out, size+1), hash)}
	dst.Stdin, dst.Stdout, dst.Stderr = n, &dstOut, &dstErr
	if err := src.Start(); err != nil {
		return 0, "", err
	}
	dstRun := dst.Run()
	if dstRun != nil || n.n > size {
		src.Process.Kill()
	}
	srcRun := src.Wait()
	got := hex.EncodeToString(hash.Sum(nil))
	reason := func(err error, stderr *capped) string {
		if msg := strings.TrimSpace(stderr.buf.String()); msg != "" {
			return msg
		}
		return err.Error()
	}
	switch {
	case ctx.Err() != nil:
		return n.n, got, errors.New("the copy timed out; nothing was kept")
	case dstRun != nil:
		return n.n, got, errors.New(reason(dstRun, &dstErr))
	case srcRun != nil:
		return n.n, got, fmt.Errorf("reading %s failed: %s", from, reason(srcRun, &srcErr))
	case n.n != size || got != sum || strings.TrimSpace(dstOut.buf.String()) != sum:
		return n.n, got, errors.New("the bytes copied don't match the approved file")
	}
	return n.n, got, nil
}

type counter struct {
	r io.Reader
	n int64
}

func (c *counter) Read(p []byte) (int, error) {
	k, err := c.r.Read(p)
	c.n += int64(k)
	return k, err
}

// HandleCopy runs POST /copy: check both ends, decide, stream, verify.
func (s *Supervisor) HandleCopy(ctx context.Context, req CopyRequest) response {
	id := s.nextID()
	mode := s.modes.Current()
	bad := func(msg string) response { return fail(400, id, OutBadRequest, msg) }
	from, err := s.parseCopyEnd(req.From)
	if err != nil {
		return bad("from: " + err.Error())
	}
	to, err := s.parseCopyEnd(req.To)
	if err != nil {
		return bad("to: " + err.Error())
	}
	if from.target == to.target {
		return bad("Both ends are on " + from.label + "; copy with a command there.")
	}
	// The side that isn't the VM; for a host-to-host copy, the source.
	host := to
	if from.target != "eldar" {
		host = from
	}
	action := fmt.Sprintf("copy %s → %s", from, to)
	verified := s.sessions.Touch(req.SessionID, req.SessionKey, TouchMeta{HerdrPane: req.Context.herdrPane(), Action: action})
	base := AuditEntry{RequestID: id, Action: action, Reason: req.Reason, Mode: string(mode), SessionID: req.SessionID, Target: host.target}
	deny := func(decision, why string, out Outcome, msg string) response {
		e := base
		e.Decision, e.DecisionReason = decision, why
		s.audit.Log(e)
		return fail(403, id, out, msg)
	}
	if mode == api.ModeLockdown {
		return deny("lockdown", "Supervisor is in lockdown mode", OutBlockedPolicy, "Supervisor is in lockdown mode")
	}
	if a, err := s.policy.Evaluate(action, ""); err != nil {
		return fail(500, id, OutInternal, "Policy unreadable: "+err.Error())
	} else if a == Deny {
		return deny("deny", "Denied by policy rule", OutBlockedPolicy, "Action denied by policy")
	}

	size, sum, kind, err := s.copySource(ctx, from)
	if err != nil {
		return bad("from: " + err.Error())
	}
	if limit := s.cfg.MaxCopyBytes; limit > 0 && size > limit {
		return bad(fmt.Sprintf("%s is %s; copies are limited to %s.", from, view.Bytes(size), view.Bytes(limit)))
	}
	dest, err := s.copyDest(ctx, to, path.Base(strings.TrimSuffix(from.path, "/")), req.Overwrite)
	if err != nil {
		return bad("to: " + err.Error())
	}
	final := copyEnd{target: to.target, label: to.label, path: dest}
	base.Action = fmt.Sprintf("copy %s → %s (%s, sha256 %s)", from, final, view.Bytes(size), sum)

	why := ""
	if from.target == "eldar" {
		if g := s.grants.Match(GrantRequest{Target: to.target, SessionID: req.SessionID, Verified: verified, Creds: []string{}}); g != nil {
			why = fmt.Sprintf("%s grant #%d", to.label, g.ID)
		}
	}
	if why == "" {
		result := s.awaitHuman(id, api.Pending{
			Kind: "copy", Target: host.target, Host: host.label,
			Title:  truncRunes(fmt.Sprintf("%s → %s", path.Base(from.path), final.label), 200),
			Reason: req.Reason, SessionID: req.SessionID, SessionVerified: verified,
			Copy: &api.CopyInfo{From: from.label + ": " + from.path, To: final.label + ": " + dest,
				Bytes: size, SHA256: sum, Type: kind, Pull: to.target == "eldar", Overwrite: req.Overwrite},
		}, nil)
		switch result {
		case "approved":
			why = "Approved by Ben"
		case "denied":
			return deny("denied", "Denied by Ben", OutDenied, "Request denied by Ben. Do NOT retry or attempt to work around this.")
		case "undeliverable":
			return deny("timeout", "Approval prompt could not be delivered to any channel", OutUndeliverable,
				"Approval could not be delivered on any channel — nobody saw this request and nothing was copied. This is NOT a refusal; it is safe to retry once.")
		default:
			return deny("timeout", "No response within timeout", OutTimedOut,
				"Request timed out — Ben did not approve within the timeout window. Do NOT retry. Inform Ben that the request was not approved.")
		}
	}

	timeout := s.cfg.MaxCommandTimeout
	if req.TimeoutSeconds > 0 {
		timeout = min(time.Duration(req.TimeoutSeconds)*time.Second, s.cfg.MaxCommandTimeout)
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := time.Now()
	n, got, err := s.stream(cctx, from, to.target, dest, size, sum, req.Overwrite)
	dur := time.Since(started).Milliseconds()
	e := base
	e.Decision, e.DecisionReason, e.DurationMs = "allow", why, &dur
	if err != nil {
		e.Result = &AuditResult{ExitCode: 1, Stderr: truncBytes(err.Error(), 1000)}
		e.TimedOut = cctx.Err() != nil
		s.audit.Log(e)
		return fail(422, id, OutInternal, "The copy failed and nothing was kept at the destination: "+err.Error(),
			"bytesSent", n)
	}
	e.Result = &AuditResult{ExitCode: 0, Stdout: fmt.Sprintf("%d bytes, sha256 %s", n, got)}
	s.audit.Log(e)
	return response{200, map[string]any{"requestId": id, "from": from.String(), "to": final.String(),
		"bytes": n, "sha256": got, "type": kind}}
}

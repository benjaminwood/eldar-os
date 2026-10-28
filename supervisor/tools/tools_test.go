package tools

import (
	"bytes"
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func startRelay(t *testing.T, grants string) (*Relay, string) {
	t.Helper()
	dir := t.TempDir()
	gp := filepath.Join(dir, "tools.yaml")
	os.WriteFile(gp, []byte(grants), 0o600)
	r := &Relay{GrantsPath: gp, StubDir: filepath.Join(dir, "bin"), Client: "/x/supervisor",
		Socket: filepath.Join(dir, "tools.sock"), Log: log.New(io.Discard, "", 0)}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go r.Serve(ctx)
	for range 100 {
		if _, err := os.Stat(r.Socket); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return r, dir
}

func call(t *testing.T, r *Relay, stdin string, tool string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	var in io.Reader
	if stdin != "" {
		in = strings.NewReader(stdin)
	}
	status, err := Call(r.Socket, tool, args, in, &out, &errb)
	if err != nil {
		t.Fatal(err)
	}
	return status, out.String(), errb.String()
}

func TestRelayRunsGrantedCommands(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "budget")
	os.WriteFile(script, []byte("#!/bin/sh\necho \"args: $*\"\necho oops >&2\n[ \"$1\" = plan ] && cat\nexit 3\n"), 0o755)
	r, rdir := startRelay(t, "commands:\n  budget:\n    run: "+script+"\n    description: The budget\n    allow: [balances, plan]\n")

	status, out, errs := call(t, r, "", "budget", "balances", "--month", "2026-10; rm -rf /")
	if status != 3 || out != "args: balances --month 2026-10; rm -rf /\n" || errs != "oops\n" {
		t.Fatalf("run: %d %q %q", status, out, errs)
	}
	if _, out, _ := call(t, r, `{"x":1}`, "budget", "plan"); !strings.HasSuffix(out, `{"x":1}`) {
		t.Fatalf("stdin not passed: %q", out)
	}
	if status, _, errs := call(t, r, "", "budget", "delete"); status != 126 || !strings.Contains(errs, "allowed subcommands") {
		t.Fatalf("subcommand outside allow: %d %q", status, errs)
	}
	if status, _, _ := call(t, r, "", "shell"); status != 126 {
		t.Fatalf("ungranted tool: %d", status)
	}
	if _, out, _ := call(t, r, "", ""); out != "budget: The budget\n  subcommands: balances, plan\n" {
		t.Fatalf("list %q", out)
	}
	if b, err := os.ReadFile(filepath.Join(rdir, "bin", "budget")); err != nil || !strings.Contains(string(b), "exec /x/supervisor tool budget \"$@\"") {
		t.Fatalf("stub: %v %q", err, b)
	}

	// Revoking it removes the stub and refuses calls at once.
	os.WriteFile(r.GrantsPath, []byte("commands: {}\n"), 0o600)
	if status, _, _ := call(t, r, "", "budget", "balances"); status != 126 {
		t.Fatalf("revoked: %d", status)
	}
	if _, err := os.Stat(filepath.Join(rdir, "bin", "budget")); !os.IsNotExist(err) {
		t.Fatalf("stub left behind: %v", err)
	}
}

func TestRelayStopsACommandAtItsTimeout(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "slow")
	os.WriteFile(script, []byte("#!/bin/sh\nsleep 30\n"), 0o755)
	r, _ := startRelay(t, "commands:\n  slow:\n    run: "+script+"\n    timeout: 200ms\n")
	start := time.Now()
	status, _, errs := call(t, r, "", "slow")
	if status == 0 || !strings.Contains(errs, "stopped after") || time.Since(start) > 10*time.Second {
		t.Fatalf("timeout: %d %q after %s", status, errs, time.Since(start))
	}
}

func TestLoadGrantsRejectsRelativeAndBadNames(t *testing.T) {
	dir := t.TempDir()
	for _, bad := range []string{"commands:\n  x:\n    run: bin/x\n", "commands:\n  ../x:\n    run: /bin/true\n"} {
		p := filepath.Join(dir, "g.yaml")
		os.WriteFile(p, []byte(bad), 0o600)
		if _, err := LoadGrants(p); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

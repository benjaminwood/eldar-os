package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/benjaminwood/eldar-os/supervisor/tools"
)

// The socket a sandboxed agent reaches the tools relay on.
func toolsSocket() string {
	if s := os.Getenv("SUPERVISOR_TOOLS_SOCKET"); s != "" {
		return s
	}
	return "/var/lib/quickbeam/run/tools.sock"
}

// runToolsRelay: the owner's side (a user service as Eldar).
func runToolsRelay(args []string) {
	fs := flag.NewFlagSet("tools-relay", flag.ExitOnError)
	grants := fs.String("grants", "", "the grants file (the owner's)")
	stubs := fs.String("stubs", "", "directory the stubs go in (on the sandbox's PATH)")
	socket := fs.String("socket", toolsSocket(), "Unix socket to serve")
	client := fs.String("client", "/run/current-system/sw/bin/supervisor", "program the stubs run")
	herdrTab := fs.String("herdr-tab", "", "WORKSPACE/TAB of the sandboxed agent, whose session reports to pass on to herdr")
	herdrHook := fs.String("herdr-hook", "", "herdr's Claude hook script")
	fs.Parse(args)
	if *grants == "" {
		fmt.Fprintln(os.Stderr, "usage: supervisor tools-relay --grants FILE --stubs DIR [--socket PATH]")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	r := &tools.Relay{GrantsPath: *grants, StubDir: *stubs, Client: *client, Socket: *socket,
		Log: log.New(os.Stderr, "tools: ", 0), HerdrTab: *herdrTab, HerdrHook: *herdrHook}
	if err := r.Serve(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "supervisor tools-relay:", err)
		os.Exit(1)
	}
}

// runTool: the sandbox's side, what a stub runs. `supervisor tool --list`
// shows what's granted; `--herdr-session` passes a Claude SessionStart hook's
// input to herdr through the relay.
func runTool(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: supervisor tool NAME [ARGS...] | supervisor tool --list")
		os.Exit(2)
	}
	name, rest := args[0], args[1:]
	switch name {
	case "--list":
		name, rest = "", nil
	case "--herdr-session": // the sandboxed agent's SessionStart hook (input on stdin)
		name, rest = "@herdr-session", nil
	}
	var stdin io.Reader // a terminal isn't passed on, only piped input
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice == 0 {
		stdin = os.Stdin
	}
	status, err := tools.Call(toolsSocket(), name, rest, stdin, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(127)
	}
	os.Exit(status)
}

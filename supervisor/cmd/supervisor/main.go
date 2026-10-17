// supervisor holds the agent's credentials and decides what it may run.
//
//	supervisor serve   the supervisor itself (a system service)
//	supervisor         Ben's TUI: approvals, sessions, grants, the global mode
//	supervisor web     the web UI (on the tailnet, with mobile push)
//	supervisor mcp     the MCP server agents in Eldar's VM use to reach it (stdio)
//	supervisor git-credential, supervisor github-token
//	                   GitHub as Eldar's App, for git and gh in the VM
//
// The UIs talk to the admin socket, which only the supervisor-admin group can
// open; the agent isn't in it.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"os/user"
	"syscall"

	"github.com/benjaminwood/eldar-os/supervisor/api"
	"github.com/benjaminwood/eldar-os/supervisor/mcp"
	"github.com/benjaminwood/eldar-os/supervisor/tui"
	"github.com/benjaminwood/eldar-os/supervisor/web"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "web" {
		runWeb(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		runServe(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "git-credential" {
		runGitCredential(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "github-token" {
		runGitHubToken()
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		if err := mcp.Run(version); err != nil {
			fmt.Fprintln(os.Stderr, "supervisor mcp:", err)
			os.Exit(1)
		}
		return
	}
	socket := flag.String("socket", "", "admin socket path (default: auto-detect)")
	showVersion := flag.Bool("version", false, "print version")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: supervisor [--socket PATH]   Ben's TUI\n       supervisor web [flags]       the web UI (supervisor web -h)\n       supervisor serve             the supervisor itself\n       supervisor mcp               MCP server for agents (stdio)\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVersion {
		fmt.Println("supervisor", version)
		return
	}

	if u, err := user.Current(); err == nil && (u.Username == "eldar" || u.Uid == "1500") {
		fmt.Fprintln(os.Stderr, "The supervisor TUI is Ben's tool; the agent can't use it.")
		os.Exit(1)
	}

	path := *socket
	if path == "" {
		var err error
		if path, err = api.FindSocket(); err != nil {
			fmt.Fprintln(os.Stderr, "supervisor:", err)
			os.Exit(1)
		}
	}
	client := api.New(path)
	client.Name = "TUI"
	if u, err := os.Hostname(); err == nil {
		client.Name = "TUI@" + u
	}
	if err := tui.Run(client); err != nil {
		fmt.Fprintln(os.Stderr, "supervisor:", err)
		os.Exit(1)
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func runWeb(args []string) {
	fs := flag.NewFlagSet("web", flag.ExitOnError)
	socket := fs.String("socket", os.Getenv("SUPERVISOR_SOCKET"), "admin socket path (default: auto-detect)")
	hostname := fs.String("hostname", env("WEB_HOSTNAME", "supervisor"), "tailnet machine name")
	state := fs.String("state", env("WEB_STATE_DIR", "/state"), "state directory (tailnet identity, push keys)")
	authKey := fs.String("authkey-file", env("TS_AUTHKEY_FILE", "/state/authkey"), "one-time tailnet auth key")
	user := fs.String("allow-user", os.Getenv("WEB_ALLOWED_USER"), "tailnet login allowed to use the UI")
	listen := fs.String("dev-listen", "", "serve plain HTTP here with NO auth (development; loopback only)")
	browserDevice := fs.String("browser-device", os.Getenv("WEB_BROWSER_DEVICE"), "tailnet machine Eldar's browser runs on; refused while it's driven")
	static := fs.String("static-dir", os.Getenv("WEB_STATIC_DIR"), "serve UI files from this dir instead of the embedded copy")
	_ = fs.Parse(args)

	path := *socket
	if path == "" {
		var err error
		if path, err = api.FindSocket(); err != nil {
			fmt.Fprintln(os.Stderr, "supervisor web:", err)
			os.Exit(1)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := web.Run(ctx, web.Config{
		Client: api.New(path), Hostname: *hostname, StateDir: *state, AuthKeyFile: *authKey,
		AllowedUser: *user, Listen: *listen, StaticDir: *static, BrowserDevice: *browserDevice,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "supervisor web:", err)
		os.Exit(1)
	}
}

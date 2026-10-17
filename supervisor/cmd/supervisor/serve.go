package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/benjaminwood/eldar-os/supervisor/server"
)

// runServe starts the supervisor. Configuration is environment variables (the
// systemd unit sets them; credentials come from an EnvironmentFile):
//
//	SUPERVISOR_LISTEN           agent-facing address (default 10.42.0.1:9100)
//	SUPERVISOR_ADMIN_SOCKET     admin socket (default /run/supervisor/admin.sock)
//	SUPERVISOR_ADMIN_GROUP      group that may open it (default supervisor-admin)
//	SUPERVISOR_STATE_DIR        audit log, settings, crons, credential cache
//	SUPERVISOR_POLICY           policy file
//	SUPERVISOR_AGENT_HOST       Eldar's VM (default 10.42.0.2)
//	SUPERVISOR_RUN_USER         user commands run as there (default ben)
//	SUPERVISOR_AGENT_USER       the unprivileged agent user (default eldar)
//	SUPERVISOR_SSH_KEY          key for the VM
//	SUPERVISOR_KNOWN_HOSTS      the VM's pinned host key
//	SUPERVISOR_DEFAULT_PROFILE  the profile agents use unless told otherwise
//	SUPERVISOR_GITHUB_APP_ID, SUPERVISOR_GITHUB_INSTALLATION_ID,
//	SUPERVISOR_GITHUB_APP_KEY (key file), SUPERVISOR_GITHUB_REPOS (comma-separated names)
//	                            Eldar's GitHub App (see server.GitHubApp)
//	APPROVAL_TIMEOUT_MS, COMMAND_TIMEOUT_S, MAX_COMMAND_TIMEOUT_S,
//	MAX_OUTPUT_BYTES, OP_PATH, OP_TIMEOUT_MS
//	OP_ACCOUNT_*_TOKEN, OP_PROFILE_*_ACCOUNT, OP_PROFILE_*_ENV  (see server.Profiles)
func runServe(args []string) {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "supervisor serve takes no arguments; it's configured by environment variables")
		os.Exit(2)
	}
	state := env("SUPERVISOR_STATE_DIR", "/var/lib/supervisor")
	cfg := server.Config{
		Listen:                env("SUPERVISOR_LISTEN", "10.42.0.1:9100"),
		AdminSocket:           env("SUPERVISOR_ADMIN_SOCKET", "/run/supervisor/admin.sock"),
		AdminGroup:            env("SUPERVISOR_ADMIN_GROUP", "supervisor-admin"),
		PolicyPath:            env("SUPERVISOR_POLICY", state+"/policy.yaml"),
		AuditPath:             state + "/audit.jsonl",
		SettingsPath:          state + "/settings.json",
		CronsPath:             state + "/authorized-crons.json",
		CacheDir:              state + "/cache",
		ApprovalTimeout:       ms("APPROVAL_TIMEOUT_MS", 180_000),
		DefaultCommandTimeout: secs("COMMAND_TIMEOUT_S", 300),
		MaxCommandTimeout:     secs("MAX_COMMAND_TIMEOUT_S", 900),
		MaxOutputBytes:        num("MAX_OUTPUT_BYTES", 5<<20),
		OpBin:                 env("OP_PATH", "op"),
		OpTimeout:             ms("OP_TIMEOUT_MS", 15_000),
		AgentUser:             env("SUPERVISOR_AGENT_USER", "eldar"),
		DefaultProfile:        os.Getenv("SUPERVISOR_DEFAULT_PROFILE"),
		HerdrSession:          env("HERDR_SESSION", "default"),
		ChatWorkspace:         env("CHAT_WORKSPACE", "primary"),
		ChatTab:               env("CHAT_TAB", "Eldar"),
		ChatPath:              state + "/chat.jsonl",
		ChatStatePath:         state + "/chat-state.json",
		RelayPath:             state + "/relay.jsonl",
		RelayStatePath:        state + "/relay-state.json",
		WatchesPath:           state + "/watches.json",
	}
	runner := &server.SSHRunner{
		Host:        env("SUPERVISOR_AGENT_HOST", "10.42.0.2"),
		KeyPath:     env("SUPERVISOR_SSH_KEY", state+"/ssh/id_ed25519"),
		KnownHosts:  env("SUPERVISOR_KNOWN_HOSTS", state+"/ssh/known_hosts"),
		DefaultUser: env("SUPERVISOR_RUN_USER", "ben"),
	}
	cfg.GitHub = &server.GitHubApp{
		AppID:          os.Getenv("SUPERVISOR_GITHUB_APP_ID"),
		InstallationID: os.Getenv("SUPERVISOR_GITHUB_INSTALLATION_ID"),
		KeyPath:        os.Getenv("SUPERVISOR_GITHUB_APP_KEY"),
		Repos:          strings.Fields(strings.ReplaceAll(os.Getenv("SUPERVISOR_GITHUB_REPOS"), ",", " ")),
	}
	cfg.TerminalSSH = server.TerminalSSH{Host: runner.Host, KeyPath: runner.KeyPath, KnownHosts: runner.KnownHosts, User: cfg.AgentUser}
	profiles := server.LoadProfiles(os.Environ(), cfg.OpBin, cfg.OpTimeout, cfg.CacheDir)
	s, err := server.New(cfg, profiles, runner)
	if err != nil {
		fmt.Fprintln(os.Stderr, "supervisor:", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := s.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "supervisor:", err)
		os.Exit(1)
	}
}

func num(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil && v > 0 {
		return v
	}
	return def
}

func ms(k string, def int) time.Duration   { return time.Duration(num(k, def)) * time.Millisecond }
func secs(k string, def int) time.Duration { return time.Duration(num(k, def)) * time.Second }

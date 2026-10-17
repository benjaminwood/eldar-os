package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/user"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/benjaminwood/eldar-os/supervisor/api"
)

func logf(format string, a ...any) { log.Printf(format, a...) }

// Config is everything the supervisor needs, from the environment (see
// cmd/supervisor) or tests.
type Config struct {
	Listen      string // agent-facing HTTP, e.g. 10.42.0.1:9100
	AdminSocket string // admin API (TUI, web UI)
	AdminGroup  string // group allowed to connect to the admin socket

	PolicyPath, AuditPath, SettingsPath, CronsPath, CacheDir string

	ApprovalTimeout       time.Duration
	DefaultCommandTimeout time.Duration
	MaxCommandTimeout     time.Duration
	MaxOutputBytes        int

	OpBin     string
	OpTimeout time.Duration

	AgentUser      string // the unprivileged agent user (cron checks, herdr)
	DefaultProfile string // the profile agents use unless told otherwise

	HerdrSession              string // herdr session the UIs show by default
	ChatWorkspace, ChatTab    string // where Eldar, the chief-of-staff session, lives in herdr
	ChatPath, ChatStatePath   string
	RelayPath, RelayStatePath string
	WatchesPath               string
	TerminalSSH               TerminalSSH            // live terminals into the VM
	GitHub                    *GitHubApp             // Eldar's GitHub App; nil = none
	Hosts                     map[string]*RemoteHost // remote hosts by name (hosts.go); "mac" is Ben's Mac
	HostAllowPath             string
}

// Supervisor holds all state and serves both APIs.
type Supervisor struct {
	cfg         Config
	n           *Notifier
	policy      *Policy
	modes       *Modes
	sessions    *Sessions
	grants      *Grants
	approvals   *Approvals
	audit       *Audit
	settings    *Settings
	crons       *Crons
	profiles    *Profiles
	herdr       *Herdr
	chat        *Chat
	chatPins    KeyPins
	relay       *Relay
	relayPins   KeyPins
	watches     *Watches
	hostAllow   *HostAllow
	browsers    *Browsers
	browserPins KeyPins
	termSSH     TerminalSSH
	runner      interface {
		Runner
		ReadFile(ctx context.Context, path string) (string, error)
	}

	reqID          atomic.Int64
	mu             sync.Mutex
	grantOverrides map[int]api.GrantInput
}

// FileRunner is a Runner that can also read files (SSHRunner).
type FileRunner interface {
	Runner
	ReadFile(ctx context.Context, path string) (string, error)
}

func New(cfg Config, profiles *Profiles, runner FileRunner) (*Supervisor, error) {
	n := NewNotifier()
	audit, err := NewAudit(cfg.AuditPath)
	if err != nil {
		return nil, err
	}
	return &Supervisor{
		cfg: cfg, n: n,
		policy:         NewPolicy(cfg.PolicyPath),
		modes:          NewModes(n),
		sessions:       NewSessions(n),
		grants:         NewGrants(n),
		approvals:      NewApprovals(n),
		audit:          audit,
		settings:       NewSettings(cfg.SettingsPath, n),
		crons:          NewCrons(cfg.CronsPath),
		profiles:       profiles,
		herdr:          NewHerdr(runner, cfg.AgentUser, cfg.HerdrSession, n),
		termSSH:        cfg.TerminalSSH,
		chat:           NewChat(cfg.ChatPath, cfg.ChatStatePath, n),
		relay:          NewRelay(cfg.RelayPath, cfg.RelayStatePath),
		watches:        NewWatches(cfg.WatchesPath),
		hostAllow:      NewHostAllow(cfg.HostAllowPath),
		browsers:       NewBrowsers(),
		runner:         runner,
		grantOverrides: map[int]api.GrantInput{},
	}, nil
}

func (s *Supervisor) nextID() int { return int(s.reqID.Add(1)) }

// Run serves until ctx ends.
func (s *Supervisor) Run(ctx context.Context) error {
	for _, w := range s.profiles.Warnings {
		logf("op: %s", w)
	}
	logf("profiles: %v", s.profiles.Names())
	go s.sweepBrowsers(ctx)

	// The agent's port. Bound to the VM link only; IP_FREEBIND so the
	// supervisor can start before the link is up.
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		c.Control(func(fd uintptr) { serr = syscall.SetsockoptInt(int(fd), syscall.SOL_IP, syscall.IP_FREEBIND, 1) })
		return serr
	}}
	agentLn, err := lc.Listen(ctx, "tcp", s.cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.cfg.Listen, err)
	}
	adminLn, err := listenAdmin(s.cfg.AdminSocket, s.cfg.AdminGroup)
	if err != nil {
		agentLn.Close()
		return err
	}
	agentSrv := &http.Server{Handler: s.agentHandler(),
		// Above the longest command plus the approval window: requests can
		// legitimately wait on Ben and then run.
		WriteTimeout:      s.cfg.MaxCommandTimeout + s.cfg.ApprovalTimeout + time.Minute,
		ReadHeaderTimeout: 30 * time.Second}
	adminSrv := &http.Server{Handler: s.adminHandler(), ReadHeaderTimeout: 30 * time.Second}

	errc := make(chan error, 2)
	go func() { errc <- agentSrv.Serve(agentLn) }()
	go func() { errc <- adminSrv.Serve(adminLn) }()
	logf("agent API on %s; admin API on %s (group %s)", s.cfg.Listen, s.cfg.AdminSocket, s.cfg.AdminGroup)

	select {
	case <-ctx.Done():
	case err = <-errc:
	}
	shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	agentSrv.Shutdown(shut)
	adminSrv.Shutdown(shut)
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}

// listenAdmin creates the admin socket, mode 0660, owned by group. The socket's
// permissions are the authentication: the agent isn't in the group.
func listenAdmin(path, group string) (net.Listener, error) {
	os.Remove(path)
	old := syscall.Umask(0o177)
	ln, err := net.Listen("unix", path)
	syscall.Umask(old)
	if err != nil {
		return nil, fmt.Errorf("admin socket %s: %w", path, err)
	}
	if group != "" {
		g, err := user.LookupGroup(group)
		if err != nil {
			ln.Close()
			return nil, fmt.Errorf("admin group %s: %w", group, err)
		}
		gid, _ := strconv.Atoi(g.Gid)
		if err := os.Chown(path, -1, gid); err != nil {
			ln.Close()
			return nil, fmt.Errorf("chown admin socket: %w", err)
		}
		if err := os.Chmod(path, 0o660); err != nil {
			ln.Close()
			return nil, err
		}
	}
	return ln, nil
}

// Package api is the typed client for the supervisor's admin API.
//
// The admin API is the single interface layer for managing the proxy: the TUI
// uses this package directly, and the web UI's server relays through it. See
// credential-proxy/src/admin.ts for the server side.
package api

import "encoding/json"

// Decision answers a pending approval.
type Decision string

const (
	Approve     Decision = "approved"
	Deny        Decision = "denied"
	AllowAlways Decision = "allow_always"
)

// Mode is the proxy's global mode.
type Mode string

const (
	ModeNormal   Mode = "normal"
	ModeBypass   Mode = "bypass"
	ModeLockdown Mode = "lockdown"
)

// Pending is an approval waiting on Ben.
type Pending struct {
	ID              int         `json:"id"`
	Kind            string      `json:"kind"` // exec | browser | grant_request
	Title           string      `json:"title"`
	Command         string      `json:"command,omitempty"`
	Reason          string      `json:"reason,omitempty"`
	Profile         string      `json:"profile,omitempty"`
	Credentials     *[]string   `json:"credentials"` // nil = whole profile
	AwsProfile      string      `json:"awsProfile,omitempty"`
	TimeoutSeconds  int         `json:"timeoutSeconds,omitempty"`
	SessionID       string      `json:"sessionId,omitempty"`
	SessionVerified bool        `json:"sessionVerified,omitempty"`
	Cwd             string      `json:"cwd,omitempty"`
	Channel         string      `json:"channel"`          // tmux | none
	Target          string      `json:"target,omitempty"` // "" / eldar, or a remote host's name ("mac")
	Host            string      `json:"host,omitempty"`   // remote host requests: the host's label
	AllowAlways     string      `json:"allowAlwaysPattern,omitempty"`
	ArcProfile      string      `json:"arcProfile,omitempty"` // browser: Arc profile directory
	ArcProfileName  string      `json:"arcProfileName,omitempty"`
	ArcSpace        string      `json:"arcSpace,omitempty"`
	Grant           *GrantInput `json:"grant,omitempty"` // grant requests: the proposed grant
	CreatedAt       int64       `json:"createdAt"`
	ExpiresAt       int64       `json:"expiresAt"`
}

// Session is a Claude Code session that has called the proxy.
type Session struct {
	ID            string `json:"id"`
	FirstSeen     int64  `json:"firstSeen"`
	LastSeen      int64  `json:"lastSeen"`
	Requests      int    `json:"requests"`
	Cwd           string `json:"cwd,omitempty"`
	HerdrPane     string `json:"herdrPane,omitempty"`
	LastAction    string `json:"lastAction,omitempty"`
	Pinned        bool   `json:"pinned"`
	KeyMismatches int    `json:"keyMismatches"`
}

// HerdrPane is a Claude session open in herdr, whether or not it has called
// the proxy yet.
type HerdrPane struct {
	PaneID    string `json:"paneId"`
	Title     string `json:"title"`
	Cwd       string `json:"cwd"`
	SessionID string `json:"sessionId,omitempty"`
	Status    string `json:"status,omitempty"`
	Focused   bool   `json:"focused"`
}

// Grant is a bypass grant Ben created.
type Grant struct {
	ID          int      `json:"id"`
	Target      string   `json:"target"`  // eldar | mac | browser
	Session     string   `json:"session"` // "*" or session ID
	Profile     string   `json:"profile"` // "*" or a 1Password profile (browser: always "*")
	Credentials Creds    `json:"credentials"`
	Aws         Creds    `json:"aws"`                // eldar: AWS profiles covered; none unless set
	Arc         Creds    `json:"arc"`                // browser: Arc profile directories covered
	ArcNames    []string `json:"arcNames,omitempty"` // browser: their names
	CreatedAt   int64    `json:"createdAt"`
	ExpiresAt   *int64   `json:"expiresAt"` // nil = until revoked
	Note        string   `json:"note,omitempty"`
	Uses        int      `json:"uses"`
}

// State is one snapshot of everything the UIs show.
type State struct {
	Now           int64          `json:"now"`
	Mode          Mode           `json:"mode"`
	ModeExpiresAt *int64         `json:"modeExpiresAt"`
	Pending       []Pending      `json:"pending"`
	Sessions      []Session      `json:"sessions"`
	HerdrPanes    []HerdrPane    `json:"herdrPanes"`
	Grants        []Grant        `json:"grants"`
	Profiles      []string       `json:"profiles"`
	Cache         []CacheEntry   `json:"cache"`
	AwsProfiles   []string       `json:"awsProfiles"`
	ArcProfiles   []ArcProfile   `json:"arcProfiles"`
	Chat          ChatSummary    `json:"chat"`
	Settings      Settings       `json:"settings"`
	MCP           MCPSummary     `json:"mcp"`
	Herdr         HerdrView      `json:"herdr"`
	Mac           MacStatus      `json:"mac"`
	Hosts         []HostStatus   `json:"hosts"`
	BrowserGrants []BrowserGrant `json:"browserGrants"`
	Crons         []Cron         `json:"crons"`
	Viewers       int            `json:"viewers"`
}

// ArcProfile is an Arc profile on Ben's Mac, as last read by the proxy.
type ArcProfile struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Ready bool   `json:"ready"`
}

// ChatMessage is one message in Ben's chat with Eldar, the chief-of-staff session.
type ChatMessage struct {
	ID   int    `json:"id"`
	From string `json:"from"` // "ben" | "eldar"
	Text string `json:"text"`
	TS   int64  `json:"ts"`
}

// ChatEldar is the chief-of-staff Claude session as herdr sees it.
type ChatEldar struct {
	SessionID string `json:"sessionId"`
	PaneID    string `json:"paneId"`
	Status    string `json:"status"`
}

// ChatSummary is the chat's state in a snapshot (Last.Text is cut to 300 characters).
type ChatSummary struct {
	LastID    int          `json:"lastId"`
	Unread    int          `json:"unread"`
	ReadUpTo  int          `json:"readUpTo"`
	Last      *ChatMessage `json:"last"`
	Connected bool         `json:"connected"` // Eldar's chat channel is listening
	Eldar     *ChatEldar   `json:"eldar"`
}

// CacheEntry is one cached credential set on the proxy: a 1Password profile's
// Environment, or minted AWS STS creds (Name "aws-<aws profile>").
type CacheEntry struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"` // "1password" | "aws"
	CachedAt int64  `json:"cachedAt"`
}

// Settings are Ben's persisted proxy preferences.
type Settings struct {
	MobilePush bool `json:"mobilePush"` // web UI pushes new approvals and sign-ins to subscribed devices
}

// MCPServer is a registered MCP server the proxy holds OAuth tokens for.
type MCPServer struct {
	Name      string `json:"name"`
	ServerURL string `json:"serverUrl"`
	HasToken  bool   `json:"hasToken"`
	ExpiresAt *int64 `json:"expiresAt"`
}

// MCPSignIn is an MCP OAuth authorization waiting for Ben to sign in.
type MCPSignIn struct {
	Name      string `json:"name"`
	ServerURL string `json:"serverUrl"`
	AuthURL   string `json:"authUrl"`
	StartedAt string `json:"startedAt"`
}

type MCPSummary struct {
	Servers []MCPServer `json:"servers"`
	Pending []MCPSignIn `json:"pending"`
}

// HerdrView is Ben's herdr session: workspaces ("spaces") → tabs → panes.
type HerdrView struct {
	Session string       `json:"session"`
	OK      bool         `json:"ok"`
	Error   string       `json:"error,omitempty"`
	Spaces  []HerdrSpace `json:"spaces"`
}

type HerdrSpace struct {
	WorkspaceID string     `json:"workspaceId"`
	Label       string     `json:"label"`
	Focused     bool       `json:"focused"`
	Status      string     `json:"status"`
	Tabs        []HerdrTab `json:"tabs"`
	Cwds        []string   `json:"cwds"`
}

type HerdrTab struct {
	TabID   string          `json:"tabId"`
	Label   string          `json:"label"`
	Focused bool            `json:"focused"`
	Status  string          `json:"status"`
	Panes   []HerdrPaneView `json:"panes"`
}

type HerdrPaneView struct {
	PaneID     string      `json:"paneId"`
	TerminalID string      `json:"terminalId"`
	Cwd        string      `json:"cwd"`
	Title      string      `json:"title"`
	Focused    bool        `json:"focused"`
	Agent      *HerdrAgent `json:"agent,omitempty"`
}

type HerdrAgent struct {
	Kind      string `json:"kind"`
	Name      string `json:"name,omitempty"`
	Status    string `json:"status"`
	SessionID string `json:"sessionId,omitempty"`
	Title     string `json:"title"`
}

// BrowserGrant is a Claude session's approved window to drive Arc on the Mac.
type BrowserGrant struct {
	ID        int    `json:"id"`
	SessionID string `json:"sessionId,omitempty"`
	Client    string `json:"client"`
	// Profile is the Arc profile directory; ProfileName is how Ben knows it (its spaces).
	Profile     string `json:"profile"`
	ProfileName string `json:"profileName"`
	CreatedAt   int64  `json:"createdAt"`
	ExpiresAt   int64  `json:"expiresAt"`
	Channels    int    `json:"channels"`
}

// HostStatus is a remote host agents may run commands on (request target =
// Name). The Mac is the host named "mac".
type HostStatus struct {
	Name        string   `json:"name"`
	Label       string   `json:"label"`
	Description string   `json:"description,omitempty"`
	Configured  bool     `json:"configured"`
	Host        string   `json:"host"`
	LastTest    *MacTest `json:"lastTest"`
	Allow       []string `json:"allow"`
}

// MacStatus is the supervisor's view of Ben's Mac (the host "mac").
type MacStatus struct {
	Configured bool     `json:"configured"`
	Host       string   `json:"host"`
	LastTest   *MacTest `json:"lastTest"`
	Allow      []string `json:"allow"`
}

type MacTest struct {
	OK     bool   `json:"ok"`
	At     int64  `json:"at"`
	Detail string `json:"detail"`
}

// LaunchInput starts a named Claude session in a new tab of a herdr workspace.
type LaunchInput struct {
	WorkspaceID     string `json:"workspaceId"`
	Name            string `json:"name"`
	Cwd             string `json:"cwd,omitempty"`
	SkipPermissions bool   `json:"skipPermissions"`
	Session         string `json:"session,omitempty"` // herdr session; default is Ben's
}

// Cron is a cron job allowed to auto-approve its requests while it runs.
type Cron struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	AuthorizedAt string `json:"authorizedAt"`
}

// GrantInput creates a grant. Empty Session/Profile mean "*".
type GrantInput struct {
	Target      string   `json:"target,omitempty"` // eldar (default) | mac | browser
	Session     string   `json:"session"`
	Profile     string   `json:"profile"`
	Credentials Creds    `json:"credentials"`
	Aws         Creds    `json:"aws"`
	Arc         Creds    `json:"arc"`
	ArcNames    []string `json:"arcNames,omitempty"`
	Minutes     *int     `json:"minutes"` // nil = until revoked
	Note        string   `json:"note,omitempty"`
}

// AuditEntry is one record from the proxy's audit log (command output omitted).
type AuditEntry struct {
	Timestamp      string `json:"timestamp"`
	RequestID      int    `json:"requestId"`
	Action         string `json:"action"`
	Reason         string `json:"reason,omitempty"`
	Profile        string `json:"profile,omitempty"`
	Decision       string `json:"decision"`
	DecisionReason string `json:"decisionReason"`
	Mode           string `json:"mode"`
	ExitCode       *int   `json:"exitCode,omitempty"`

	// Raw so "absent" (older entries), null (whole profile) and [] (none) stay distinct.
	Credentials json.RawMessage `json:"credentials,omitempty"`
	AwsProfile  string          `json:"awsProfile,omitempty"`
	Cwd         string          `json:"cwd,omitempty"`
	SessionID   string          `json:"sessionId,omitempty"`
	Target      string          `json:"target,omitempty"`
	DurationMs  *int64          `json:"durationMs,omitempty"`
	TimedOut    bool            `json:"timedOut,omitempty"`
	Truncated   bool            `json:"truncated,omitempty"`
	Result      *AuditResult    `json:"result,omitempty"` // only from AuditEntry (single-entry fetch)
}

// AuditResult is the output the audit log keeps (first 1000 bytes per stream).
type AuditResult struct {
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
}

// CredsSummary describes what credentials an audited command got.
func (e AuditEntry) CredsSummary() string {
	switch raw := string(e.Credentials); raw {
	case "":
		return ""
	case "null":
		return "ALL in profile"
	default:
		var names []string
		if json.Unmarshal(e.Credentials, &names) != nil {
			return ""
		}
		if len(names) == 0 {
			return "none"
		}
		out := names[0]
		for _, n := range names[1:] {
			out += ", " + n
		}
		return out
	}
}

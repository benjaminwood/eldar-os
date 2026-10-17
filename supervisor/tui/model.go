// Package tui is the terminal front end for the proxy admin API.
package tui

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	zone "github.com/lrstanley/bubblezone"

	"github.com/benjaminwood/eldar-os/supervisor/api"
	"github.com/benjaminwood/eldar-os/supervisor/view"
)

type tab int

const (
	tabApprovals tab = iota
	tabSessions
	tabGrants
	tabSettings
	tabActivity
	tabCount
)

var tabNames = []string{"Approvals", "Sessions", "Grants", "Settings", "Activity"}

type (
	eventMsg  api.Event
	tickMsg   time.Time
	resultMsg struct {
		ok  string
		err error
	}
	credsMsg struct {
		profile string
		names   []string
		err     error
	}
)

type model struct {
	client *api.Client
	events <-chan api.Event
	cancel context.CancelFunc

	st        api.State
	have      bool
	connected bool
	connErr   string
	sessions  []view.Session
	now       time.Time

	w, h int
	tab  tab

	// approvals
	pSel    int // selected pending ID
	marks   map[int]bool
	seen    map[int]bool
	seenSignIn map[string]bool
	primed  bool
	pOff    int
	cmdOff  int // command scroll in the detail pane
	btnHot  int // keyboard-highlighted button in the detail pane (-1 none)

	// sessions
	sSel string
	sOff int

	// grants
	gSel string // "g<id>" or "a<id>"
	gOff int

	// settings
	cSel string

	// activity
	audit        []api.AuditEntry
	auditErr     string
	auditFilter  int
	auditQuery   string
	aSel         string
	aOff         int
	auditDetail  *auditDetail
	auditPending bool

	toast      string
	toastColor string
	toastUntil time.Time

	popup  *popup
	editor *editor
	prompt *prompt
	help   bool
}

// Run starts the TUI against the admin socket.
func Run(client *api.Client) error {
	zone.NewGlobal()
	ctx, cancel := context.WithCancel(context.Background())
	m := &model{
		client: client,
		events: client.Subscribe(ctx),
		cancel: cancel,
		marks:  map[int]bool{},
		seen:   map[int]bool{},
		seenSignIn: map[string]bool{},
		now:    time.Now(),
		btnHot: -1,
	}
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
	cancel()
	return err
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(waitEvent(m.events), tick(), tea.SetWindowTitle("supervisor"))
}

func waitEvent(ch <-chan api.Event) tea.Cmd {
	return func() tea.Msg {
		e, ok := <-ch
		if !ok {
			return nil
		}
		return eventMsg(e)
	}
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// call runs an API action off the UI goroutine and reports the outcome.
func (m *model) call(ok string, f func(context.Context) error) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return resultMsg{ok: ok, err: f(ctx)}
	}
}

func (m *model) flash(text, color string) {
	m.toast, m.toastColor, m.toastUntil = text, color, time.Now().Add(4*time.Second)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil

	case tickMsg:
		m.now = time.Time(msg)
		return m, tick()

	case eventMsg:
		cmds := []tea.Cmd{waitEvent(m.events)}
		if msg.State != nil {
			cmds = append(cmds, m.applyState(*msg.State))
		}
		m.connected = msg.Connected
		if msg.Err != nil {
			m.connErr = msg.Err.Error()
		} else if msg.Connected {
			m.connErr = ""
		}
		return m, tea.Batch(cmds...)

	case resultMsg:
		if msg.err != nil {
			m.flash("✗ "+msg.err.Error(), t.Red)
		} else if msg.ok != "" {
			m.flash(msg.ok, t.Mint)
		}
		return m, nil

	case credsMsg:
		if m.editor != nil {
			m.editor.setCreds(msg)
		}
		return m, nil

	case auditMsg:
		if msg.err != nil {
			m.auditErr = msg.err.Error()
		} else {
			m.audit, m.auditErr = msg.entries, ""
		}
		return m, nil

	case auditRefreshMsg:
		m.auditPending = false
		if m.tab == tabActivity {
			return m, m.loadAudit()
		}
		return m, nil

	case auditDetailMsg:
		if d := m.auditDetail; d != nil && auditKey(d.entry) == auditKey(msg.entry) || (d != nil && msg.err != nil) {
			d.loading = false
			if msg.err != nil {
				d.err = "output unavailable: " + msg.err.Error()
			} else {
				d.entry = msg.entry
			}
		}
		return m, nil

	case tea.MouseMsg:
		return m, m.mouse(msg)

	case tea.KeyMsg:
		return m, m.key(msg)
	}
	if m.prompt != nil {
		return m, m.prompt.update(m, msg)
	}
	if m.editor != nil {
		return m, m.editor.update(m, msg)
	}
	return m, nil
}

// applyState installs a new snapshot, keeping selections stable by ID.
func (m *model) applyState(st api.State) tea.Cmd {
	m.st, m.have = st, true
	m.sessions = view.Sessions(st)
	m.now = time.Now()

	live := map[int]bool{}
	var fresh []int
	for _, p := range st.Pending {
		live[p.ID] = true
		if !m.seen[p.ID] {
			m.seen[p.ID] = true
			fresh = append(fresh, p.ID)
		}
	}
	for id := range m.marks {
		if !live[id] {
			delete(m.marks, id)
		}
	}
	if !live[m.pSel] {
		m.pSel = 0
		m.cmdOff = 0
		if len(st.Pending) > 0 {
			m.pSel = st.Pending[0].ID
		}
	}
	var cmds []tea.Cmd
	for _, si := range st.MCP.Pending {
		if key := "mcp:" + si.Name + si.StartedAt; !m.seenSignIn[key] {
			m.seenSignIn[key] = true
			if m.primed && m.tab != tabSettings {
				m.flash("🔗 "+si.Name+" is waiting for you to sign in — press 4", t.Orange)
			}
		}
	}
	m.primed = true
	if len(fresh) > 0 {
		// Ring the terminal bell; most terminals turn that into an attention cue.
		fmt.Fprint(os.Stderr, "\a")
		if m.tab != tabApprovals && m.editor == nil && m.popup == nil {
			m.flash(fmt.Sprintf("● New approval #%d — press 1", fresh[len(fresh)-1]), t.Orange)
		}
	}
	title := "supervisor"
	if n := len(st.Pending); n > 0 {
		title = fmt.Sprintf("(%d) supervisor", n)
	}
	cmds = append(cmds, tea.SetWindowTitle(title))
	if m.tab == tabActivity {
		cmds = append(cmds, m.auditSoon())
	}
	return tea.Batch(cmds...)
}

func (m *model) pendingIndex() int {
	for i, p := range m.st.Pending {
		if p.ID == m.pSel {
			return i
		}
	}
	return -1
}

func (m *model) selectedPending() *api.Pending {
	if i := m.pendingIndex(); i >= 0 {
		return &m.st.Pending[i]
	}
	return nil
}

func (m *model) sessionIndex() int {
	for i, s := range m.sessions {
		if s.ID == m.sSel {
			return i
		}
	}
	if len(m.sessions) > 0 {
		m.sSel = m.sessions[0].ID
		return 0
	}
	return -1
}

// grantRows lists grants then open Arc windows with stable keys.
type grantRow struct {
	key     string
	grant   *api.Grant
	browser *api.BrowserGrant
}

func (m *model) grantRows() []grantRow {
	var rows []grantRow
	for i := range m.st.Grants {
		g := &m.st.Grants[i]
		rows = append(rows, grantRow{key: fmt.Sprintf("g%d", g.ID), grant: g})
	}
	for i := range m.st.BrowserGrants {
		b := &m.st.BrowserGrants[i]
		rows = append(rows, grantRow{key: fmt.Sprintf("b%d", b.ID), browser: b})
	}
	return rows
}

func (m *model) grantIndex(rows []grantRow) int {
	for i, r := range rows {
		if r.key == m.gSel {
			return i
		}
	}
	if len(rows) > 0 {
		m.gSel = rows[0].key
		return 0
	}
	return -1
}

// ── actions ────────────────────────────────────────────────────────────────

// decideTargets is the marked set, or the selected request when none are marked.
func (m *model) decideTargets() []int {
	var ids []int
	for id := range m.marks {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	if len(ids) == 0 && m.pSel != 0 {
		ids = []int{m.pSel}
	}
	return ids
}

func (m *model) decide(d api.Decision) tea.Cmd {
	ids := m.decideTargets()
	if len(ids) == 0 {
		return nil
	}
	if d == api.AllowAlways {
		// "Always" adds an allow rule per command; only meaningful for commands.
		for _, id := range ids {
			for _, p := range m.st.Pending {
				if p.ID == id && p.Kind != "exec" {
					m.flash("“Always” only applies to commands — approve bypass requests instead", t.Orange)
					return nil
				}
			}
		}
	}
	verb := map[api.Decision]string{api.Approve: "Approved", api.Deny: "Denied", api.AllowAlways: "Approved + allowlisted"}[d]
	labels := make([]string, len(ids))
	for i, id := range ids {
		labels[i] = fmt.Sprintf("#%d", id)
	}
	m.marks = map[int]bool{}
	client := m.client
	return m.call(fmt.Sprintf("✓ %s %s", verb, strings.Join(labels, ", ")), func(ctx context.Context) error {
		var errs []string
		for _, id := range ids {
			if err := client.Decide(ctx, id, d); err != nil {
				errs = append(errs, fmt.Sprintf("#%d: %s", id, err))
			}
		}
		if len(errs) > 0 {
			return fmt.Errorf("%s", strings.Join(errs, "; "))
		}
		return nil
	})
}

func (m *model) setMode(mode api.Mode, minutes *int) tea.Cmd {
	client := m.client
	label := map[api.Mode]string{api.ModeNormal: "Normal mode", api.ModeBypass: "Global bypass on", api.ModeLockdown: "Lockdown engaged"}[mode]
	return m.call("✓ "+label, func(ctx context.Context) error { return client.SetMode(ctx, mode, minutes) })
}

func (m *model) addGrant(in api.GrantInput, approve int) tea.Cmd {
	client := m.client
	ok := "✓ Grant created"
	if approve != 0 {
		ok = fmt.Sprintf("✓ Grant created and #%d approved", approve)
	}
	return m.call(ok, func(ctx context.Context) error {
		if _, err := client.AddGrant(ctx, in); err != nil {
			return err
		}
		if approve != 0 {
			return client.Decide(ctx, approve, api.Approve)
		}
		return nil
	})
}

func (m *model) approveGrantRequest(id int, in api.GrantInput) tea.Cmd {
	client := m.client
	return m.call(fmt.Sprintf("✓ Request #%d approved with your scope", id), func(ctx context.Context) error {
		return client.ApproveGrantRequest(ctx, id, in)
	})
}

func (m *model) revoke(r grantRow) tea.Cmd {
	client := m.client
	if r.grant != nil {
		id := r.grant.ID
		return m.call(fmt.Sprintf("✓ Grant #%d revoked", id), func(ctx context.Context) error { return client.RevokeGrant(ctx, id) })
	}
	id := r.browser.ID
	return m.call(fmt.Sprintf("✓ Arc access #%d ended", id), func(ctx context.Context) error { return client.RevokeBrowserGrant(ctx, id) })
}

// clearSession revokes everything that targets one session specifically.
func (m *model) clearSession(s view.Session) tea.Cmd {
	var rows []grantRow
	for _, r := range m.grantRows() {
		if r.grant != nil && r.grant.Session == s.ID {
			rows = append(rows, r)
		}
	}
	if len(rows) == 0 {
		m.flash("Nothing session-specific to revoke for "+s.Title, t.Dim)
		return nil
	}
	client := m.client
	return m.call(fmt.Sprintf("✓ Cleared %d bypass%s for %s", len(rows), plural(len(rows), "es"), s.Title), func(ctx context.Context) error {
		for _, r := range rows {
			if err := client.RevokeGrant(ctx, r.grant.ID); err != nil {
				return err
			}
		}
		return nil
	})
}

// toggleSessionBypass turns a session's full bypass off, or offers durations.
func (m *model) toggleSessionBypass(s view.Session) tea.Cmd {
	if m.st.Mode == api.ModeLockdown {
		m.flash("In lockdown — leave lockdown first (m)", t.Red)
		return nil
	}
	var full []int
	for _, g := range m.st.Grants {
		if view.IsEldarGrant(g) && g.Session == s.ID && view.FullGrant(g) {
			full = append(full, g.ID)
		}
	}
	if len(full) > 0 {
		client := m.client
		return m.call("✓ Bypass off for "+s.Title, func(ctx context.Context) error {
			for _, id := range full {
				if err := client.RevokeGrant(ctx, id); err != nil {
					return err
				}
			}
			return nil
		})
	}
	m.popup = sessionBypassPopup(m, s)
	return nil
}

func (m *model) unpin(s view.Session) tea.Cmd {
	client := m.client
	return m.call("✓ Key unpinned — re-pins on the session's next request", func(ctx context.Context) error {
		return client.UnpinSession(ctx, s.ID)
	})
}

// setPush turns mobile push on or off ("go mobile").
func (m *model) setPush(on bool) tea.Cmd {
	client := m.client
	ok := "📱 Mobile push off"
	if on {
		ok = "📱 Mobile push on — new approvals go to your phone"
	}
	return m.call(ok, func(ctx context.Context) error {
		_, err := client.SetMobilePush(ctx, on)
		return err
	})
}


func (m *model) openEditor(e *editor) tea.Cmd {
	m.editor = e
	return e.loadCreds(m)
}

func plural(n int, suffix string) string {
	if n == 1 {
		return ""
	}
	return suffix
}

func intp(n int) *int { return &n }

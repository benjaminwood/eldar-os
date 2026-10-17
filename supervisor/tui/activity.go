package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	zone "github.com/lrstanley/bubblezone"

	"github.com/benjaminwood/eldar-os/supervisor/api"
	"github.com/benjaminwood/eldar-os/supervisor/view"
)

type (
	auditMsg struct {
		entries []api.AuditEntry
		err     error
	}
	auditDetailMsg struct {
		entry api.AuditEntry
		err   error
	}
	auditRefreshMsg struct{}
)

var auditFilters = []struct {
	name string
	keep func(api.AuditEntry) bool
}{
	{"all", func(api.AuditEntry) bool { return true }},
	{"asked you", func(e api.AuditEntry) bool { return e.Decision == "approved" || e.Decision == "denied" || e.Decision == "timeout" }},
	{"auto-approved", func(e api.AuditEntry) bool { return e.Decision == "allow" || e.Decision == "bypass" }},
	{"blocked", func(e api.AuditEntry) bool { return e.Decision == "deny" || e.Decision == "lockdown" || e.Decision == "denied" }},
}

func decisionStyle(d string) (string, lipgloss.Style) {
	switch d {
	case "approved":
		return "✓", sMint
	case "denied":
		return "✗", sRed
	case "bypass":
		return "⚡", sOrange
	case "timeout":
		return "⌛", sYellow
	case "deny", "lockdown":
		return "⛔", sRed
	default:
		return "•", sDim
	}
}

var decisionLabels = map[string]string{
	"approved": "Approved by you", "denied": "Denied", "allow": "Allowed", "bypass": "Bypass",
	"timeout": "Timed out", "deny": "Blocked by policy", "lockdown": "Blocked (lockdown)", "ask": "Asked",
}

// loadAudit fetches the recent audit log.
func (m *model) loadAudit() tea.Cmd {
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		es, err := client.Audit(ctx, 400)
		return auditMsg{entries: es, err: err}
	}
}

// auditSoon debounces refreshes driven by state changes.
func (m *model) auditSoon() tea.Cmd {
	if m.auditPending {
		return nil
	}
	m.auditPending = true
	return tea.Tick(800*time.Millisecond, func(time.Time) tea.Msg { return auditRefreshMsg{} })
}

// visibleAudit is the filtered log, newest first.
func (m *model) visibleAudit() []api.AuditEntry {
	q := strings.ToLower(strings.TrimSpace(m.auditQuery))
	var out []api.AuditEntry
	for i := len(m.audit) - 1; i >= 0; i-- {
		e := m.audit[i]
		if !auditFilters[m.auditFilter].keep(e) {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(e.Action+" "+e.Reason+" "+e.DecisionReason+" "+e.Profile+" "+e.Cwd), q) {
			continue
		}
		out = append(out, e)
	}
	return out
}

func auditKey(e api.AuditEntry) string { return e.Timestamp + "#" + strconv.Itoa(e.RequestID) }

func (m *model) auditIndex(list []api.AuditEntry) int {
	for i, e := range list {
		if auditKey(e) == m.aSel {
			return i
		}
	}
	if len(list) > 0 {
		m.aSel = auditKey(list[0])
		return 0
	}
	return -1
}

func (m *model) openAuditDetail(e api.AuditEntry) tea.Cmd {
	m.auditDetail = &auditDetail{entry: e, loading: true}
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		full, err := client.AuditEntryDetail(ctx, e.Timestamp, e.RequestID)
		return auditDetailMsg{entry: full, err: err}
	}
}

func (m *model) activityKey(k string) tea.Cmd {
	list := m.visibleAudit()
	i := m.auditIndex(list)
	move := func(d int) {
		if i >= 0 {
			m.aSel = auditKey(list[max(0, min(len(list)-1, i+d))])
		}
	}
	switch k {
	case "j", "down":
		move(1)
	case "k", "up":
		move(-1)
	case "pgdown":
		move(10)
	case "pgup":
		move(-10)
	case "home", "g":
		move(-len(list))
	case "end", "G":
		move(len(list))
	case "enter":
		if i >= 0 {
			return m.openAuditDetail(list[i])
		}
	case "f":
		m.auditFilter = (m.auditFilter + 1) % len(auditFilters)
	case "/":
		m.prompt = newPrompt("Search activity", "Matches commands, reasons, profiles and directories.",
			[]string{"Search"}, []string{"e.g. gh pr  or  personal"},
			func(m *model, v []string) (tea.Cmd, string) {
				m.auditQuery = v[0]
				return nil, ""
			})
		m.prompt.inputs[0].SetValue(m.auditQuery)
	case "esc":
		m.auditQuery = ""
	case "r":
		return m.loadAudit()
	}
	return nil
}

func (m *model) activityView(w, h int) string {
	list := m.visibleAudit()
	head := " " + sAccent.Bold(true).Render("Activity") + "  " + sDim.Render("showing ") + sBright.Render(auditFilters[m.auditFilter].name)
	if m.auditQuery != "" {
		head += sDim.Render("  matching ") + sCyan.Render("“"+m.auditQuery+"”")
	}
	head += sFaint.Render(fmt.Sprintf("  · %d", len(list)))
	if m.auditErr != "" {
		head += "  " + sRed.Render("✗ "+m.auditErr)
	}
	lines := []string{head, ""}

	const rowH = 2
	fitN := max(1, (h-2)/rowH)
	idx := m.auditIndex(list)
	if idx < m.aOff {
		m.aOff = idx
	}
	if idx >= m.aOff+fitN {
		m.aOff = idx - fitN + 1
	}
	m.aOff = max(0, min(m.aOff, len(list)-fitN))

	if len(list) == 0 {
		msg := "Nothing here yet."
		if m.auditQuery != "" {
			msg = "No matches. esc clears the search."
		}
		lines = append(lines, "  "+sDim.Render(msg))
	}
	for i := m.aOff; i < min(len(list), m.aOff+fitN); i++ {
		e := list[i]
		ico, st := decisionStyle(e.Decision)
		when := ""
		if ts, err := time.Parse(time.RFC3339Nano, e.Timestamp); err == nil {
			when = ts.Local().Format("Jan 2 15:04:05")
		}
		mac := strings.HasPrefix(e.Action, "mac: ") || e.Target == "mac"
		action, more := view.CommandSummary(strings.TrimPrefix(e.Action, "mac: "))
		if more > 0 {
			action += fmt.Sprintf("  (+%d lines)", more)
		}
		tag := ""
		if mac {
			tag = sLav.Bold(true).Render("MAC ")
		}
		var bits []string
		if e.ExitCode != nil {
			if *e.ExitCode == 0 {
				bits = append(bits, sMint.Render("exit 0"))
			} else {
				bits = append(bits, sRed.Render(fmt.Sprintf("exit %d", *e.ExitCode)))
			}
		}
		if e.DurationMs != nil {
			bits = append(bits, sDim.Render(view.Duration(time.Duration(*e.DurationMs)*time.Millisecond)))
		}
		right := strings.Join(bits, sFaint.Render(" · ")) + "  " + sFaint.Render(when)
		line1 := st.Render(ico) + " " + tag + sBright.Render(trunc(action, w-lipgloss.Width(right)-12))
		line1 = fit(line1, w-4-lipgloss.Width(right)) + right
		why := e.DecisionReason
		if e.Profile != "" {
			why += " · " + e.Profile
		}
		if e.Reason != "" {
			why += " — " + e.Reason
		}
		line2 := "  " + sDim.Render(trunc(why, w-8))
		bar := "  "
		if auditKey(e) == m.aSel {
			bar = sAccent.Render("▌ ")
		}
		row := bar + line1 + "\n" + bar + line2
		if auditKey(e) == m.aSel {
			row = lipgloss.NewStyle().Background(c(t.Surface)).Width(w).Render(row)
		}
		lines = append(lines, zone.Mark("au:"+auditKey(e), row))
	}
	return lipgloss.NewStyle().Width(w).Height(h).MaxHeight(h).Render(strings.Join(lines, "\n"))
}

// auditDetail is the full view of one entry.
type auditDetail struct {
	entry   api.AuditEntry
	loading bool
	err     string
	off     int
}

func (d *auditDetail) key(m *model, k string) {
	switch k {
	case "esc", "q", "enter":
		m.auditDetail = nil
	case "j", "down":
		d.off++
	case "k", "up":
		d.off = max(0, d.off-1)
	case "pgdown", " ":
		d.off += 10
	case "pgup":
		d.off = max(0, d.off-10)
	case "g", "home":
		d.off = 0
	}
}

func (d *auditDetail) render(m *model, maxW, maxH int) string {
	e := d.entry
	w := min(maxW-6, 110)
	inner := w - 4
	ico, st := decisionStyle(e.Decision)
	label := decisionLabels[e.Decision]
	if label == "" {
		label = e.Decision
	}
	when := e.Timestamp
	if ts, err := time.Parse(time.RFC3339Nano, e.Timestamp); err == nil {
		when = ts.Local().Format("Mon Jan 2 15:04:05") + " · " + view.Ago(ts.UnixMilli(), m.now)
	}
	command := strings.TrimPrefix(e.Action, "mac: ")
	mac := command != e.Action || e.Target == "mac"

	var body []string
	add := func(k, v string) {
		if v != "" {
			wrapped := lipgloss.NewStyle().Width(inner - 11).Render(v)
			for i, l := range strings.Split(wrapped, "\n") {
				kk := ""
				if i == 0 {
					kk = k
				}
				body = append(body, sLabel.Width(11).Render(kk)+l)
			}
		}
	}
	section := func(title string) { body = append(body, "", sLav.Bold(true).Render(title)) }

	cmdTitle := "Command"
	if mac {
		cmdTitle = "Command — on your Mac"
	}
	section(cmdTitle)
	for _, l := range strings.Split(lipgloss.NewStyle().Width(inner).Render(command), "\n") {
		body = append(body, sCyan.Render(l))
	}
	section("Details")
	add("Why", e.DecisionReason)
	add("Reason", sYellow.Render(e.Reason))
	add("Profile", e.Profile)
	creds := e.CredsSummary()
	if creds == "ALL in profile" {
		creds = sOrange.Bold(true).Render("⚠ " + creds)
	}
	add("Creds", creds)
	add("AWS", e.AwsProfile)
	if e.SessionID != "" {
		add("Session", view.SessionTitle(m.sessions, e.SessionID)+sFaint.Render("  "+view.Short(e.SessionID)))
	}
	add("Cwd", strings.Replace(e.Cwd, "/home/eldar/", "~/", 1))
	if e.ExitCode != nil {
		if *e.ExitCode == 0 {
			add("Exit", sMint.Render("0"))
		} else {
			add("Exit", sRed.Render(strconv.Itoa(*e.ExitCode)))
		}
	}
	if e.DurationMs != nil {
		add("Took", view.Duration(time.Duration(*e.DurationMs)*time.Millisecond))
	}
	var flags []string
	if e.TimedOut {
		flags = append(flags, sOrange.Render("timed out"))
	}
	if e.Truncated {
		flags = append(flags, sOrange.Render("output truncated"))
	}
	add("Flags", strings.Join(flags, " · "))
	if e.Mode != "" && e.Mode != "normal" {
		add("Mode", e.Mode)
	}
	if e.RequestID > 0 {
		add("Request", "#"+strconv.Itoa(e.RequestID))
	}

	switch {
	case d.loading:
		section("Output")
		body = append(body, sDim.Render("loading…"))
	case d.err != "":
		section("Output")
		body = append(body, sDim.Render(d.err))
	case e.Result != nil:
		if e.Result.Stdout != "" {
			section("Output")
			for _, l := range strings.Split(lipgloss.NewStyle().Width(inner).Render(strings.TrimRight(e.Result.Stdout, "\n")), "\n") {
				body = append(body, sBase.Render(l))
			}
		}
		if e.Result.Stderr != "" {
			section("Errors (stderr)")
			for _, l := range strings.Split(lipgloss.NewStyle().Width(inner).Render(strings.TrimRight(e.Result.Stderr, "\n")), "\n") {
				body = append(body, sRed.Render(l))
			}
		}
		if e.Result.Stdout != "" || e.Result.Stderr != "" {
			body = append(body, "", sFaint.Render("The audit log keeps the first 1000 characters of each stream."))
		}
	}

	viewH := max(5, maxH-10)
	d.off = max(0, min(d.off, len(body)-viewH))
	shown := body[d.off:min(len(body), d.off+viewH)]
	scroll := ""
	if len(body) > viewH {
		scroll = sFaint.Render(fmt.Sprintf("  %d–%d of %d", d.off+1, d.off+len(shown), len(body)))
	}
	head := st.Bold(true).Render(ico+" "+label) + "  " + sDim.Render(when)
	rows := append([]string{head}, shown...)
	rows = append(rows, "", sFaint.Render("j/k scroll · esc close")+scroll)
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c(t.Accent)).Padding(1, 2).Width(w).
		Render(strings.Join(rows, "\n"))
}

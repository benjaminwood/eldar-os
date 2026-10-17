package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	zone "github.com/lrstanley/bubblezone"

	"github.com/benjaminwood/eldar-os/supervisor/api"
	"github.com/benjaminwood/eldar-os/supervisor/view"
)

const (
	headerLines = 3
	footerLines = 2
)

func (m *model) View() string {
	if m.w == 0 {
		return ""
	}
	if m.w < 50 || m.h < 16 {
		return lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center,
			gradient("ELDAR", t.Accent, t.Cyan, true)+"\n"+sDim.Render("make the window a bit bigger"))
	}
	bodyH := m.h - headerLines - footerLines
	var body string
	switch {
	case !m.have:
		body = m.connecting(bodyH)
	case m.tab == tabApprovals:
		body = m.approvalsView(m.w, bodyH)
	case m.tab == tabSessions:
		body = m.sessionsView(m.w, bodyH)
	case m.tab == tabGrants:
		body = m.grantsView(m.w, bodyH)
	case m.tab == tabSettings:
		body = m.settingsView(m.w, bodyH)
	default:
		body = m.activityView(m.w, bodyH)
	}
	body = lipgloss.NewStyle().Width(m.w).Height(bodyH).MaxHeight(bodyH).Render(body)

	screen := strings.Join([]string{m.header(), body, m.footer()}, "\n")

	switch {
	case m.prompt != nil:
		screen = overlay(screen, m.prompt.render(), m.w, m.h)
	case m.auditDetail != nil:
		screen = overlay(screen, m.auditDetail.render(m, m.w, m.h), m.w, m.h)
	case m.editor != nil:
		screen = overlay(screen, m.editor.render(m, m.w, m.h), m.w, m.h)
	case m.popup != nil:
		screen = overlay(screen, m.popup.render(), m.w, m.h)
	case m.help:
		screen = overlay(screen, m.helpView(), m.w, m.h)
	}
	return zone.Scan(screen)
}

func (m *model) header() string {
	brand := gradient("✦ E L D A R", t.Accent, t.Cyan, true) + sDim.Render("  supervisor")

	var mode string
	switch m.st.Mode {
	case api.ModeBypass:
		txt := "⚡ BYPASS"
		if m.st.ModeExpiresAt != nil {
			txt += " " + view.Remaining(*m.st.ModeExpiresAt, m.now)
		} else {
			txt += " ∞"
		}
		mode = badge(txt, t.Bg, t.Orange)
	case api.ModeLockdown:
		mode = badge("■ LOCKDOWN", t.Bright, t.Red)
	default:
		mode = badge("● NORMAL", t.Bg, t.Mint)
	}
	mode = zone.Mark("mode", mode)

	push := sFaint.Render("📱 off")
	if m.st.Settings.MobilePush {
		push = badge("📱 PUSH", t.Bg, t.Cyan)
	}
	push = zone.Mark("push", push)

	conn := sMint.Render("● live")
	if !m.connected {
		conn = sRed.Render("○ reconnecting")
	}
	right := push + "  " + mode + "  " + conn + " "
	gap := m.w - lipgloss.Width(brand) - lipgloss.Width(right) - 1
	line1 := " " + brand + strings.Repeat(" ", max(1, gap)) + right

	var tabs []string
	for i, name := range tabNames {
		count := ""
		switch tab(i) {
		case tabApprovals:
			if n := len(m.st.Pending); n > 0 {
				count = " " + badge(strconv.Itoa(n), t.Bg, t.Orange)
			}
		case tabSessions:
			count = " " + sFaint.Render(strconv.Itoa(len(m.sessions)))
		case tabGrants:
			if n := len(m.st.Grants) + len(m.st.BrowserGrants); n > 0 {
				count = " " + sFaint.Render(strconv.Itoa(n))
			}
		case tabSettings:
			if n := len(m.st.MCP.Pending); n > 0 {
				count = " " + badge(strconv.Itoa(n), t.Bg, t.Orange)
			}
		}
		label := sDim.Render(fmt.Sprintf("%d ", i+1)) + sBase.Render(name)
		if tab(i) == m.tab {
			label = sAccent.Render(fmt.Sprintf("%d ", i+1)) + sBold.Underline(true).Foreground(c(t.Accent)).Render(name)
		}
		tabs = append(tabs, zone.Mark("tab:"+strconv.Itoa(i), " "+label+count+" "))
	}
	line2 := " " + strings.Join(tabs, sFaint.Render("│"))
	return line1 + "\n" + line2 + "\n" + rule(m.w)
}

type hint struct{ key, desc string }

func (m *model) hints() []hint {
	switch m.tab {
	case tabApprovals:
		if len(m.st.Pending) == 0 {
			return []hint{{"p", "go mobile"}, {"m", "mode"}, {"tab", "switch"}, {"?", "help"}, {"q", "quit"}}
		}
		h := []hint{{"a", "approve"}, {"d", "deny"}, {"A", "always"}, {"g", "grant…"}, {"space", "mark"}, {"*", "mark all"}}
		if len(m.marks) > 0 {
			h = append([]hint{{"", fmt.Sprintf("%d marked", len(m.marks))}}, h...)
		}
		return append(h, hint{"m", "mode"}, hint{"?", "help"})
	case tabSessions:
		return []hint{{"b", "bypass on/off"}, {"g", "fine-grained…"}, {"x", "clear"}, {"u", "re-pin"}, {"m", "mode"}, {"?", "help"}}
	case tabGrants:
		return []hint{{"n", "new grant"}, {"x", "revoke"}, {"m", "mode"}, {"?", "help"}, {"q", "quit"}}
	case tabActivity:
		return []hint{{"enter", "details"}, {"f", "filter"}, {"/", "search"}, {"esc", "clear search"}, {"r", "refresh"}, {"?", "help"}}
	default:
		return []hint{{"y", "copy sign-in link"}, {"n", "connect MCP"}, {"v", "paste redirect"}, {"c", "allow cron"}, {"t", "test Mac"}, {"b", "Mac bypass"}, {"a", "Mac allow"}, {"x", "remove"}}
	}
}

func (m *model) footer() string {
	var line string
	if m.toast != "" && m.now.Before(m.toastUntil) {
		line = " " + lipgloss.NewStyle().Foreground(c(m.toastColor)).Render(m.toast)
	} else if !m.connected && m.connErr != "" {
		line = " " + sRed.Render("✗ "+m.connErr)
	} else {
		var parts []string
		for _, h := range m.hints() {
			if h.key == "" {
				parts = append(parts, sOrange.Render(h.desc))
				continue
			}
			parts = append(parts, sAccent.Render(h.key)+" "+sDim.Render(h.desc))
		}
		line = " " + strings.Join(parts, sFaint.Render("  ·  "))
	}
	help := zone.Mark("help", sFaint.Render("? "))
	line = fit(line, m.w-2) + help
	return rule(m.w) + "\n" + line
}

func (m *model) connecting(h int) string {
	msg := sDim.Render("connecting to " + "the proxy…")
	if m.connErr != "" {
		msg = sRed.Render(m.connErr) + "\n\n" + sDim.Render("retrying…")
	}
	return lipgloss.Place(m.w, h, lipgloss.Center, lipgloss.Center, sigil()+"\n\n"+msg)
}

// sigil is the Eldar mark used on empty screens.
func sigil() string {
	lines := []string{
		"      ·  ✦  ·      ",
		"   ✧     ᛖ     ✧   ",
		"      ·  ✦  ·      ",
	}
	var out []string
	for _, l := range lines {
		out = append(out, gradient(l, t.Violet, t.Cyan, false))
	}
	return lipgloss.JoinVertical(lipgloss.Center, out...)
}

func (m *model) helpView() string {
	sec := func(title string, rows [][2]string) string {
		var b strings.Builder
		b.WriteString(sAccent.Bold(true).Render(title) + "\n")
		for _, r := range rows {
			b.WriteString("  " + sBright.Width(12).Render(r[0]) + sDim.Render(r[1]) + "\n")
		}
		return b.String()
	}
	left := sec("Everywhere", [][2]string{
		{"1-5 / tab", "switch view"}, {"m", "proxy mode (bypass, lockdown)"}, {"p", "mobile push on/off (go mobile)"},
		{"?", "this help"}, {"q", "quit"},
	}) + "\n" + sec("Approvals", [][2]string{
		{"j k / ↑ ↓", "select"}, {"a  y", "approve"}, {"d  n", "deny"}, {"A", "approve + allowlist command"},
		{"g", "grant a bypass, then approve"}, {"space x", "mark (act on many)"}, {"* ctrl+a", "mark all / none"},
		{"J K", "scroll command"}, {"← → enter", "focus + press a button"},
	})
	right := sec("Sessions", [][2]string{
		{"b  enter", "bypass this session on/off"}, {"g", "fine-grained bypass…"}, {"x", "clear its bypasses"},
		{"u", "re-pin its key"},
	}) + "\n" + sec("Grants", [][2]string{
		{"n", "new grant"}, {"x  del", "revoke"},
	}) + "\n" + sec("Activity", [][2]string{
		{"enter / click", "full details + output"}, {"f", "filter"}, {"/", "search"},
	}) + "\n" + sec("Settings", [][2]string{
		{"y", "copy MCP sign-in link"}, {"n", "connect an MCP server"}, {"v", "paste a sign-in redirect"},
		{"c", "allow a cron"}, {"X", "clear all cached credentials"}, {"t", "test the Mac"}, {"b", "Mac bypass on/off"}, {"a", "always allow on Mac"}, {"x", "remove"},
	}) + "\n" + sec("Mouse", [][2]string{
		{"click", "tabs, rows, buttons, mode badge"}, {"wheel", "move / scroll"},
	})
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, "    ", right)
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c(t.Accent)).Padding(1, 2).
		Render(gradient("✦ supervisor", t.Accent, t.Cyan, true) + "\n\n" + body + sFaint.Render("any key to close"))
}

// overlay centers box over base, dimming everything underneath.
func overlay(base, box string, w, h int) string {
	baseLines := strings.Split(base, "\n")
	for len(baseLines) < h {
		baseLines = append(baseLines, "")
	}
	for i, l := range baseLines {
		baseLines[i] = sFaint.Render(fit(ansi.Strip(l), w))
	}
	boxLines := strings.Split(box, "\n")
	bw := lipgloss.Width(box)
	x := max(0, (w-bw)/2)
	y := max(0, (h-len(boxLines))/2)
	for i, bl := range boxLines {
		row := y + i
		if row >= len(baseLines) {
			break
		}
		plain := fit(ansi.Strip(baseLines[row]), w)
		left := ansi.Truncate(plain, x, "")
		right := ansi.TruncateLeft(plain, x+bw, "")
		baseLines[row] = sFaint.Render(left) + bl + strings.Repeat(" ", max(0, bw-lipgloss.Width(bl))) + sFaint.Render(right)
	}
	return strings.Join(baseLines[:h], "\n")
}

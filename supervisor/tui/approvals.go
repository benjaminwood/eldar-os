package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	zone "github.com/lrstanley/bubblezone"

	"github.com/benjaminwood/eldar-os/supervisor/api"
	"github.com/benjaminwood/eldar-os/supervisor/view"
)

const pendingRowH = 3

func (m *model) approvalsView(w, h int) string {
	if len(m.st.Pending) == 0 {
		msg := sBright.Render("All quiet.") + "\n" +
			sDim.Render("Approvals land here the moment an agent asks.")
		if m.st.Mode == api.ModeBypass {
			msg += "\n\n" + sOrange.Render("Global bypass is on — nothing will ask until it's off (m).")
		}
		return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center,
			lipgloss.JoinVertical(lipgloss.Center, sigil(), "", msg))
	}
	if w >= 110 {
		listW := max(38, w*2/5)
		list := m.pendingList(listW, h)
		sep := sRule.Render(strings.Repeat("│\n", h-1) + "│")
		return lipgloss.JoinHorizontal(lipgloss.Top, list, sep, m.pendingDetail(w-listW-1, h))
	}
	// Stacked: list on top (compact), detail below.
	listH := min(len(m.st.Pending)*pendingRowH, max(pendingRowH, h/3))
	return m.pendingList(w, listH) + "\n" + rule(w) + "\n" + m.pendingDetail(w, h-listH-1)
}

func (m *model) pendingList(w, h int) string {
	rowsFit := max(1, h/pendingRowH)
	idx := m.pendingIndex()
	if idx < m.pOff {
		m.pOff = idx
	}
	if idx >= m.pOff+rowsFit {
		m.pOff = idx - rowsFit + 1
	}
	m.pOff = max(0, min(m.pOff, len(m.st.Pending)-rowsFit))

	var out []string
	for i := m.pOff; i < min(len(m.st.Pending), m.pOff+rowsFit); i++ {
		out = append(out, m.pendingRow(m.st.Pending[i], w))
	}
	return lipgloss.NewStyle().Width(w).Height(h).MaxHeight(h).Render(strings.Join(out, "\n"))
}

func kindColor(kind, target string) string {
	if kind == "browser" {
		return t.Orange
	}
	if target == "mac" {
		return t.Lavender
	}
	if kind == "grant_request" {
		return t.Yellow
	}
	return t.Cyan
}

func (m *model) pendingRow(p api.Pending, w int) string {
	sel := p.ID == m.pSel
	id := strconv.Itoa(p.ID)

	box := sFaint.Render("○")
	if m.marks[p.ID] {
		box = sOrange.Render("◉")
	}
	box = zone.Mark("pm:"+id, box)

	secs := time.UnixMilli(p.ExpiresAt).Sub(m.now).Seconds()
	left := lipgloss.NewStyle().Foreground(c(urgency(secs))).Render(view.Remaining(p.ExpiresAt, m.now))
	kind := lipgloss.NewStyle().Foreground(c(kindColor(p.Kind, p.Target))).Render(view.KindLabelFor(p.Kind, p.Target))
	head := box + " " + sBold.Render("#"+id) + " " + kind
	head += strings.Repeat(" ", max(1, w-3-lipgloss.Width(head)-lipgloss.Width(left))) + left

	title := sBright.Render(trunc(strings.ReplaceAll(p.Title, "\n", " "), w-5))
	who := view.SessionTitle(m.sessions, p.SessionID)
	meta := sDim.Render(trunc(who, w/2))
	if p.Profile != "" {
		meta += sFaint.Render(" · ") + sLav.Render(p.Profile)
	}
	if cr := view.CredsOf(p); cr != "" {
		meta += sFaint.Render(" · ") + sDim.Render(cr)
	}
	if p.AwsProfile != "" {
		st := sYellow
		if view.AwsRisky(p.AwsProfile) {
			st = sOrange.Bold(true)
		}
		meta += sFaint.Render(" · ") + st.Render("AWS "+view.AwsLabel(p.AwsProfile))
	}
	if p.ArcProfileName != "" {
		meta += sFaint.Render(" · ") + sLav.Render("Arc "+p.ArcProfileName)
	}

	bar := "  "
	if sel {
		bar = sAccent.Render("▌ ")
	}
	lines := []string{
		bar + fit(head, w-3),
		bar + "  " + fit(title, w-5),
		bar + "  " + fit(meta, w-5),
	}
	row := strings.Join(lines, "\n")
	if sel {
		row = lipgloss.NewStyle().Background(c(t.Surface)).Width(w).Render(row)
	}
	return zone.Mark("p:"+id, row)
}

func (m *model) pendingDetail(w, h int) string {
	p := m.selectedPending()
	if p == nil {
		return ""
	}
	pad := lipgloss.NewStyle().Padding(0, 2)
	inner := w - 4

	secsLeft := time.UnixMilli(p.ExpiresAt).Sub(m.now).Seconds()
	total := float64(p.ExpiresAt-p.CreatedAt) / 1000
	color := urgency(secsLeft)

	kind := lipgloss.NewStyle().Foreground(c(kindColor(p.Kind, p.Target))).Bold(true).Render(view.KindLabelFor(p.Kind, p.Target))
	head := sBold.Render(fmt.Sprintf("#%d", p.ID)) + "  " + kind
	exp := lipgloss.NewStyle().Foreground(c(color)).Render("expires in " + view.Remaining(p.ExpiresAt, m.now))
	head += strings.Repeat(" ", max(1, inner-lipgloss.Width(head)-lipgloss.Width(exp))) + exp

	var rows []string
	rows = append(rows, head, progress(secsLeft/total, inner, color), "")

	field := func(k, v string) {
		if ansi.Strip(v) != "" {
			rows = append(rows, sLabel.Render(k)+trunc(v, inner-10))
		}
	}

	sess := sDim.Render("not reported")
	if p.SessionID != "" {
		sess = sBright.Render(view.SessionTitle(m.sessions, p.SessionID))
		if p.SessionVerified {
			sess += "  " + sMint.Render("✓ verified")
		} else {
			sess += "  " + sOrange.Render("⚠ unverified")
		}
		sess += "  " + sFaint.Render(view.Short(p.SessionID))
	}
	field("Session", sess)
	if p.Target == "mac" {
		field("Runs on", sLav.Bold(true).Render(p.Host))
	}
	if g := p.Grant; p.Kind == "grant_request" && g != nil {
		where := map[string]string{"mac": "your Mac (commands)", "browser": "Arc on your Mac"}[g.Target]
		if where == "" {
			where = "Eldar (commands in the container)"
		}
		field("Covers", sBright.Render(where))
		any := func(c api.Creds, all, none string, label func(string) string) string {
			if c.Any {
				return sOrange.Bold(true).Render("⚠ " + all)
			}
			if len(c.Names) == 0 {
				return sDim.Render(none)
			}
			out := make([]string, len(c.Names))
			for i, n := range c.Names {
				out[i] = label(n)
			}
			return sBright.Render(strings.Join(out, ", "))
		}
		same := func(s string) string { return s }
		if g.Target == "browser" {
			names := api.Creds{Any: g.Arc.Any, Names: g.ArcNames}
			if len(names.Names) == 0 {
				names.Names = g.Arc.Names
			}
			field("Arc", any(names, "ANY Arc profile", "none", same))
		} else {
			prof := g.Profile
			if prof == "*" {
				prof = sOrange.Render("ANY profile")
			}
			field("Profile", sLav.Render(prof))
			field("Creds", any(g.Credentials, "ANY credentials", "none", same))
			if g.Target != "mac" {
				aws := any(g.Aws, "ANY AWS role", "no AWS", view.AwsLabel)
				for _, a := range g.Aws.Names {
					if view.AwsRisky(a) {
						aws += "  " + sOrange.Render("⚠ admin/production")
						break
					}
				}
				field("AWS", aws)
			}
		}
		field("For", sOrange.Render(view.Duration(time.Duration(p.TimeoutSeconds)*time.Second))+sFaint.Render("  this session only"))
		field("Answer", sDim.Render("a approve as asked · g narrow it first · d deny"))
	} else if p.Kind == "browser" {
		arc := p.ArcProfileName
		if p.ArcSpace != "" {
			arc += " · " + p.ArcSpace + " space"
		}
		field("Arc", sLav.Bold(true).Render(arc))
	} else {
		field("Profile", sLav.Render(p.Profile))
	}
	if p.Kind != "grant_request" && (p.Kind == "exec" || p.Credentials != nil) {
		cr := view.CredsOf(*p)
		st := sBase
		if p.Credentials == nil {
			st = sOrange.Bold(true)
			cr = "⚠ " + cr
		}
		field("Creds", st.Render(cr))
	}
	if p.AwsProfile != "" {
		v := sYellow.Render(view.AwsLabel(p.AwsProfile))
		if view.AwsRisky(p.AwsProfile) {
			v = sOrange.Bold(true).Render("⚠ " + view.AwsLabel(p.AwsProfile))
		}
		field("AWS", v+sFaint.Render("  minted STS credentials"))
	}
	switch {
	case p.Kind == "grant_request":
	case p.Kind == "exec" && p.TimeoutSeconds > 0:
		field("Timeout", sBase.Render(view.Duration(time.Duration(p.TimeoutSeconds)*time.Second)))
	case p.TimeoutSeconds > 0:
		field("Duration", sOrange.Render(view.Duration(time.Duration(p.TimeoutSeconds)*time.Second)))
	}
	field("Cwd", sDim.Render(strings.Replace(p.Cwd, "/home/eldar/", "~/", 1)))
	if p.AllowAlways != "" {
		field("Always", sDim.Render("A adds “"+p.AllowAlways+"” to the Mac allow list"))
	}
	if p.Channel == "tmux" {
		field("Also on", sDim.Render("tmux popup in the caller's pane"))
	}
	if p.Reason != "" {
		rows = append(rows, "")
		for i, l := range strings.Split(lipgloss.NewStyle().Width(inner-10).Render(p.Reason), "\n") {
			k := ""
			if i == 0 {
				k = "Reason"
			}
			rows = append(rows, sLabel.Render(k)+sYellow.Render(l))
		}
	}

	// Buttons take 3 lines; the command gets whatever is left.
	btns := m.pendingButtons(*p, inner)
	btnH := lipgloss.Height(btns)

	if p.Command != "" {
		rows = append(rows, "", sLav.Render("Command")+" "+sRule.Render(strings.Repeat("─", max(0, inner-8))))
		avail := h - len(rows) - btnH - 2
		cmdLines := strings.Split(lipgloss.NewStyle().Width(inner).Render(p.Command), "\n")
		if avail > 0 {
			m.cmdOff = max(0, min(m.cmdOff, len(cmdLines)-avail))
			end := min(len(cmdLines), m.cmdOff+avail)
			shown := cmdLines[m.cmdOff:end]
			for i := range shown {
				shown[i] = sCyan.Render(shown[i])
			}
			if hidden := len(cmdLines) - end; hidden > 0 && len(shown) > 0 {
				shown[len(shown)-1] = sFaint.Render(fmt.Sprintf("… %d more lines — J/K or wheel to scroll", hidden))
			}
			rows = append(rows, zone.Mark("cmd", strings.Join(shown, "\n")))
		}
	}

	return pad.Render(strings.Join(rows, "\n") + "\n\n" + btns)
}

func (m *model) pendingButtons(p api.Pending, w int) string {
	n := len(m.marks)
	suffix := ""
	if n > 1 {
		suffix = fmt.Sprintf(" %d", n)
	}
	defs := []struct{ name, label, key, color string }{
		{"approve", "✓ Approve" + suffix, "a", t.Mint},
		{"deny", "✗ Deny" + suffix, "d", t.Red},
		{"always", "⏭ Always", "A", t.Cyan},
		{"grant", "⚡ Grant…", "g", t.Orange},
	}
	var out []string
	for i, d := range defs {
		if d.name == "always" && p.Kind != "exec" {
			continue
		}
		out = append(out, zone.Mark("btn:"+d.name, button(d.label, d.key, d.color, m.btnHot == i)))
	}
	row := lipgloss.JoinHorizontal(lipgloss.Top, interleave(out, " ")...)
	if lipgloss.Width(row) > w {
		// Narrow: two per line.
		var lines []string
		for i := 0; i < len(out); i += 2 {
			lines = append(lines, lipgloss.JoinHorizontal(lipgloss.Top, interleave(out[i:min(len(out), i+2)], " ")...))
		}
		row = lipgloss.JoinVertical(lipgloss.Left, lines...)
	}
	return row
}

func interleave(xs []string, sep string) []string {
	var out []string
	for i, x := range xs {
		if i > 0 {
			out = append(out, sep)
		}
		out = append(out, x)
	}
	return out
}

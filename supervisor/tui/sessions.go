package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	zone "github.com/lrstanley/bubblezone"

	"github.com/benjaminwood/eldar-os/supervisor/api"
	"github.com/benjaminwood/eldar-os/supervisor/view"
)

func (m *model) sessionsView(w, h int) string {
	if len(m.sessions) == 0 {
		return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, lipgloss.JoinVertical(lipgloss.Center,
			sigil(), "", sBright.Render("No sessions yet."),
			sDim.Render("Claude sessions appear when they open in herdr or call the proxy.")))
	}
	detailH := min(14, h/2)
	if w >= 130 {
		listW := w * 3 / 5
		sep := sRule.Render(strings.Repeat("│\n", h-1) + "│")
		return lipgloss.JoinHorizontal(lipgloss.Top, m.sessionTable(listW, h), sep, m.sessionDetail(w-listW-1, h))
	}
	return m.sessionTable(w, h-detailH-1) + "\n" + rule(w) + "\n" + m.sessionDetail(w, detailH)
}

func (m *model) sessionTable(w, h int) string {
	cols := []struct {
		name string
		w    int
	}{{"", 3}, {"SESSION", 0}, {"PANE", 8}, {"STATE", 9}, {"LAST CALL", 11}, {"CALLS", 6}, {"APPROVAL", 9}}
	fixed := 0
	for _, c := range cols {
		fixed += c.w
	}
	titleW := max(12, w-fixed-2)
	cols[1].w = titleW

	var hdr strings.Builder
	hdr.WriteString(" ")
	for _, c := range cols {
		hdr.WriteString(fit(sFaint.Render(c.name), c.w))
	}
	lines := []string{hdr.String()}

	idx := m.sessionIndex()
	rowsFit := max(1, h-1)
	if idx < m.sOff {
		m.sOff = idx
	}
	if idx >= m.sOff+rowsFit {
		m.sOff = idx - rowsFit + 1
	}
	m.sOff = max(0, min(m.sOff, len(m.sessions)-rowsFit))

	for i := m.sOff; i < min(len(m.sessions), m.sOff+rowsFit); i++ {
		s := m.sessions[i]
		title := sBright.Render(s.Title)
		if s.Pending > 0 {
			title = badge(strconv.Itoa(s.Pending), t.Bg, t.Orange) + " " + title
		}
		if s.KeyMismatches > 0 {
			title = sRed.Render("⚠ ") + title
		}
		pane := sDim.Render(s.Pane)
		if s.Focused {
			pane = sCyan.Render(s.Pane)
		}
		state := sDim.Render(s.Status)
		switch s.Status {
		case "working":
			state = sCyan.Render("working")
		case "blocked":
			state = sOrange.Render("blocked")
		case "":
			if !s.InHerdr {
				state = sFaint.Render("closed?")
			}
		}
		last := sFaint.Render("—")
		if s.SeenByProxy {
			last = sDim.Render(view.Ago(s.LastSeen, m.now))
		}
		calls := sFaint.Render("—")
		if s.Requests > 0 {
			calls = sBase.Render(strconv.Itoa(s.Requests))
		}
		cells := []string{" " + levelGlyph(s.Level), title, pane, state, last, calls, levelText(s.Level)}
		var row strings.Builder
		row.WriteString(" ")
		for j, cell := range cells {
			row.WriteString(fit(cell, cols[j].w))
		}
		line := row.String()
		if s.ID == m.sSel {
			line = lipgloss.NewStyle().Background(c(t.Surface)).Render(sAccent.Render("▌") + fit(line[1:], w-1))
		}
		lines = append(lines, zone.Mark("s:"+s.ID, line))
	}
	return lipgloss.NewStyle().Width(w).Height(h).MaxHeight(h).Render(strings.Join(lines, "\n"))
}

func (m *model) sessionDetail(w, h int) string {
	i := m.sessionIndex()
	if i < 0 {
		return ""
	}
	s := m.sessions[i]
	inner := w - 4
	var rows []string

	head := sBold.Render(trunc(s.Title, inner-12))
	lvl := levelGlyph(s.Level) + " " + levelText(s.Level)
	head += strings.Repeat(" ", max(1, inner-lipgloss.Width(head)-lipgloss.Width(lvl))) + lvl
	rows = append(rows, head)

	meta := []string{sFaint.Render(s.ID)}
	if s.Pane != "" {
		meta = append(meta, sCyan.Render(s.Pane))
	}
	if s.Cwd != "" {
		meta = append(meta, sDim.Render(strings.Replace(s.Cwd, "/home/eldar/", "~/", 1)))
	}
	rows = append(rows, trunc(strings.Join(meta, sFaint.Render(" · ")), inner))

	switch {
	case s.KeyMismatches > 0:
		rows = append(rows, sRed.Render(fmt.Sprintf("⚠ %d request%s claimed this session with a different key", s.KeyMismatches, plural(s.KeyMismatches, "s")))+
			sDim.Render(" — a restarted proxy-mcp, or an impostor. ")+sAccent.Render("u")+sDim.Render(" re-pins."))
	case s.Pinned:
		rows = append(rows, sMint.Render("✓ key pinned")+sDim.Render(" — session-specific grants apply only to this session"))
	case s.SeenByProxy:
		rows = append(rows, sOrange.Render("○ no key")+sDim.Render(" — this session's proxy-mcp predates session keys; session grants won't match until it restarts"))
	default:
		rows = append(rows, sDim.Render("○ hasn't called the proxy yet — its key pins on the first call"))
	}
	if s.LastAction != "" {
		rows = append(rows, sLav.Render("last  ")+sCyan.Render(trunc(s.LastAction, inner-6)))
	}

	rows = append(rows, "")
	if len(s.Covers) == 0 {
		if m.st.Mode == api.ModeLockdown {
			rows = append(rows, sRed.Render("Lockdown — every request is refused."))
		} else {
			rows = append(rows, sDim.Render("Every credentialed call asks for approval (per policy)."))
		}
	}
	for _, cv := range s.Covers {
		exp := "until revoked"
		if cv.ExpiresAt != nil {
			exp = view.Remaining(*cv.ExpiresAt, m.now) + " left"
		}
		color := t.Yellow
		if cv.Full {
			color = t.Orange
		}
		scope := ""
		if cv.Session == "*" && cv.Source != "global" {
			scope = sFaint.Render(" (all sessions)")
		}
		line := lipgloss.NewStyle().Foreground(c(color)).Render("• "+cv.Label()) + scope + "  " + sDim.Render(exp)
		if cv.Source == "grant" && cv.Session == s.ID {
			line += "  " + zone.Mark("sx:"+strconv.Itoa(cv.ID), sRed.Render("[✕]"))
		}
		rows = append(rows, trunc(line, inner))
	}

	btnLabel := "⚡ Bypass session"
	for _, g := range m.st.Grants {
		if view.IsEldarGrant(g) && g.Session == s.ID && view.FullGrant(g) {
			btnLabel = "○ Bypass off"
		}
	}
	btns := []string{
		zone.Mark("sb:b", button(btnLabel, "b", t.Orange, false)),
		zone.Mark("sb:g", button("◐ Fine-grained…", "g", t.Yellow, false)),
		zone.Mark("sb:x", button("✕ Clear", "x", t.Red, false)),
	}
	if s.Pinned || s.KeyMismatches > 0 {
		btns = append(btns, zone.Mark("sb:u", button("↻ Re-pin", "u", t.Lavender, false)))
	}
	bar := lipgloss.JoinHorizontal(lipgloss.Top, interleave(btns, " ")...)

	top := lipgloss.NewStyle().Width(inner).Render(strings.Join(rows, "\n"))
	gap := max(1, h-lipgloss.Height(top)-lipgloss.Height(bar))
	return lipgloss.NewStyle().Padding(0, 2).MaxHeight(h).Render(top + strings.Repeat("\n", gap) + bar)
}

package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	zone "github.com/lrstanley/bubblezone"

	"github.com/benjaminwood/eldar-os/supervisor/view"
)

func (m *model) grantsView(w, h int) string {
	rows := m.grantRows()
	newBtn := zone.Mark("gnew", button("+ New grant", "n", t.Orange, false))
	if len(rows) == 0 {
		return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, lipgloss.JoinVertical(lipgloss.Center,
			sigil(), "", sBright.Render("No bypasses active."),
			sDim.Render("Grants you create and bypasses agents were granted show up here."), "", newBtn))
	}

	idx := m.grantIndex(rows)
	const rowH = 2
	listH := h - 4
	fitN := max(1, listH/rowH)
	if idx < m.gOff {
		m.gOff = idx
	}
	if idx >= m.gOff+fitN {
		m.gOff = idx - fitN + 1
	}
	m.gOff = max(0, min(m.gOff, len(rows)-fitN))

	var out []string
	for i := m.gOff; i < min(len(rows), m.gOff+fitN); i++ {
		out = append(out, m.grantRowView(rows[i], w))
	}
	list := lipgloss.NewStyle().Height(listH).MaxHeight(listH).Render(strings.Join(out, "\n"))
	// Indent the whole (multi-line) button, not just its first line.
	return list + "\n" + rule(w) + "\n" + lipgloss.NewStyle().PaddingLeft(1).Render(newBtn)
}

func (m *model) grantRowView(r grantRow, w int) string {
	var kind, who, scope, exp, extra string
	var color string
	if g := r.grant; g != nil {
		kind = sAccent.Render(fmt.Sprintf("grant #%d", g.ID))
		switch g.Target {
		case "eldar", "":
		case "browser":
			kind = sLav.Bold(true).Render(fmt.Sprintf("BROWSER grant #%d", g.ID))
		default: // a remote host
			kind = sLav.Bold(true).Render(fmt.Sprintf("%s grant #%d", strings.ToUpper(g.Target), g.ID))
		}
		who = "every session"
		if g.Session != "*" {
			who = view.SessionTitle(m.sessions, g.Session)
		}
		prof := g.Profile
		if prof == "*" {
			prof = "any profile"
		}
		scope = prof + " · " + g.Credentials.String()
		if view.IsEldarGrant(*g) {
			scope += " · " + view.AwsScope(g.Aws)
		}
		if g.Target == "browser" {
			scope = "open Arc windows in any profile"
			if !g.Arc.Any {
				names := g.ArcNames
				if len(names) == 0 {
					names = g.Arc.Names
				}
				scope = "open Arc windows in " + strings.Join(names, ", ")
			}
		} else if view.FullGrant(*g) {
			scope = "everything"
		}
		exp = "until revoked"
		if g.ExpiresAt != nil {
			exp = view.Remaining(*g.ExpiresAt, m.now) + " left"
		}
		extra = fmt.Sprintf("%d use%s", g.Uses, plural(g.Uses, "s"))
		if g.Note != "" {
			extra += " · " + g.Note
		}
		color = t.Yellow
		if scope == "everything" {
			color = t.Orange
		}
	} else if b := r.browser; b != nil {
		kind = sLav.Bold(true).Render(fmt.Sprintf("BROWSER #%d", b.ID))
		who = view.SessionTitle(m.sessions, b.SessionID)
		on := m.hostLabel(b.Host)
		if on == "" {
			on = "a remote host"
		}
		scope = "drive a browser on " + on
		if b.ProfileName != "" {
			scope = "drive " + b.ProfileName + " on " + on
		}
		color = t.Orange
		exp = view.Remaining(b.ExpiresAt, m.now) + " left"
		extra = b.Client
		if b.Channels > 0 {
			extra += " · connected"
		}
	}
	sel := r.key == m.gSel
	bar := "  "
	if sel {
		bar = sAccent.Render("▌ ")
	}
	x := zone.Mark("gx:"+r.key, sRed.Render("[✕ revoke]"))
	head := kind + "  " + sBright.Render(trunc(who, w/3)) + "  " + lipgloss.NewStyle().Foreground(c(color)).Render(scope)
	right := sDim.Render(exp) + "  " + x
	line1 := fit(head, w-4-lipgloss.Width(right)) + right
	line2 := "  " + sFaint.Render(trunc(extra, w-6))
	row := bar + line1 + "\n" + bar + line2
	if sel {
		row = lipgloss.NewStyle().Background(c(t.Surface)).Width(w).Render(row)
	}
	return zone.Mark("gr:"+r.key, row)
}

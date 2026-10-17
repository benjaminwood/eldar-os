package tui

import (
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	zone "github.com/lrstanley/bubblezone"

	"github.com/benjaminwood/eldar-os/supervisor/api"
	"github.com/benjaminwood/eldar-os/supervisor/view"
)

type popupOpt struct {
	key, label, hint string
	color            string
	current          bool
	run              func(m *model) tea.Cmd
}

// popup is a small menu of choices, keyboard- and mouse-driven.
type popup struct {
	title, subtitle string
	opts            []popupOpt
	sel             int
}

func (p *popup) move(d int) {
	p.sel = (p.sel + d + len(p.opts)) % len(p.opts)
}

func (p *popup) choose(m *model, i int) tea.Cmd {
	m.popup = nil
	return p.opts[i].run(m)
}

func (p *popup) key(m *model, k string) tea.Cmd {
	switch k {
	case "esc", "q":
		m.popup = nil
	case "j", "down", "tab":
		p.move(1)
	case "k", "up", "shift+tab":
		p.move(-1)
	case "enter", " ":
		return p.choose(m, p.sel)
	default:
		for i, o := range p.opts {
			if o.key == k {
				return p.choose(m, i)
			}
		}
	}
	return nil
}

func (p *popup) render() string {
	var rows []string
	rows = append(rows, sBold.Render(p.title))
	if p.subtitle != "" {
		rows = append(rows, sDim.Render(p.subtitle))
	}
	rows = append(rows, "")
	w := 0
	for _, o := range p.opts {
		w = max(w, lipgloss.Width(o.label)+lipgloss.Width(o.hint)+10)
	}
	for i, o := range p.opts {
		k := sDim.Render("[" + o.key + "]")
		label := lipgloss.NewStyle().Foreground(c(o.color)).Render(o.label)
		if o.current {
			label += sDim.Render("  · current")
		}
		line := " " + k + "  " + label
		if o.hint != "" {
			line += "  " + sFaint.Render(o.hint)
		}
		line = fit(line, w)
		if i == p.sel {
			line = lipgloss.NewStyle().Background(c(t.Border)).Render(line)
		}
		rows = append(rows, zone.Mark("pop:"+strconv.Itoa(i), line))
	}
	rows = append(rows, "", sFaint.Render("↑↓ move · enter choose · esc close"))
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).BorderForeground(c(t.Accent)).
		Padding(1, 2).
		Render(strings.Join(rows, "\n"))
	return zone.Mark("popup", box)
}

func modePopup(m *model) *popup {
	cur := m.st.Mode
	bypass := func(min *int) func(*model) tea.Cmd {
		return func(m *model) tea.Cmd { return m.setMode(api.ModeBypass, min) }
	}
	p := &popup{
		title:    "Proxy mode",
		subtitle: "Applies to every session",
		opts: []popupOpt{
			{key: "n", label: "● Normal", hint: "ask per policy", color: t.Mint, current: cur == api.ModeNormal,
				run: func(m *model) tea.Cmd { return m.setMode(api.ModeNormal, nil) }},
			{key: "1", label: "⚡ Bypass 15 minutes", color: t.Orange, run: bypass(intp(15))},
			{key: "2", label: "⚡ Bypass 1 hour", color: t.Orange, run: bypass(intp(60))},
			{key: "3", label: "⚡ Bypass 2 hours", color: t.Orange, run: bypass(intp(120))},
			{key: "4", label: "⚡ Bypass until I turn it off", color: t.Orange, current: cur == api.ModeBypass, run: bypass(nil)},
			{key: "L", label: "■ Lockdown", hint: "refuse all, revoke every bypass", color: t.Red, current: cur == api.ModeLockdown,
				run: func(m *model) tea.Cmd { m.popup = lockdownConfirm(); return nil }},
		},
	}
	for i, o := range p.opts {
		if o.current {
			p.sel = i
		}
	}
	return p
}

func lockdownConfirm() *popup {
	return &popup{
		title:    "Engage lockdown?",
		subtitle: "Every request is refused and all grants and agent bypasses are revoked.",
		opts: []popupOpt{
			{key: "y", label: "■ Yes, lock it down", color: t.Red, run: func(m *model) tea.Cmd { return m.setMode(api.ModeLockdown, nil) }},
			{key: "n", label: "Cancel", color: t.Fg, run: func(*model) tea.Cmd { return nil }},
		},
		sel: 1,
	}
}

func sessionBypassPopup(m *model, s view.Session) *popup {
	grant := func(min *int) func(*model) tea.Cmd {
		return func(m *model) tea.Cmd {
			return m.addGrant(api.GrantInput{Session: s.ID, Profile: "*", Credentials: api.AnyCreds, Aws: api.AnyCreds, Minutes: min, Note: "session bypass"}, 0)
		}
	}
	return &popup{
		title:    "Bypass " + trunc(s.Title, 40),
		subtitle: "Every request from this session skips approval (policy denies still apply)",
		opts: []popupOpt{
			{key: "1", label: "15 minutes", color: t.Orange, run: grant(intp(15))},
			{key: "2", label: "1 hour", color: t.Orange, run: grant(intp(60))},
			{key: "3", label: "4 hours", color: t.Orange, run: grant(intp(240))},
			{key: "4", label: "Until I turn it off", color: t.Orange, run: grant(nil)},
			{key: "g", label: "Fine-grained…", hint: "pick profile & credentials", color: t.Cyan,
				run: func(m *model) tea.Cmd { return m.openEditor(newEditor(m, s.ID)) }},
		},
		sel: 1,
	}
}

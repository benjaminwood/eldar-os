package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/lucasb-eyer/go-colorful"

	"github.com/benjaminwood/eldar-os/supervisor/view"
)

var t = view.Theme

func c(hex string) lipgloss.Color { return lipgloss.Color(hex) }

var (
	sBase   = lipgloss.NewStyle().Foreground(c(t.Fg))
	sBright = lipgloss.NewStyle().Foreground(c(t.Bright))
	sBold   = lipgloss.NewStyle().Foreground(c(t.Bright)).Bold(true)
	sDim    = lipgloss.NewStyle().Foreground(c(t.Dim))
	sFaint  = lipgloss.NewStyle().Foreground(c(t.Faint))
	sAccent = lipgloss.NewStyle().Foreground(c(t.Accent))
	sLav    = lipgloss.NewStyle().Foreground(c(t.Lavender))
	sCyan   = lipgloss.NewStyle().Foreground(c(t.Cyan))
	sMint   = lipgloss.NewStyle().Foreground(c(t.Mint))
	sRed    = lipgloss.NewStyle().Foreground(c(t.Red))
	sYellow = lipgloss.NewStyle().Foreground(c(t.Yellow))
	sOrange = lipgloss.NewStyle().Foreground(c(t.Orange))
	sLabel  = lipgloss.NewStyle().Foreground(c(t.Lavender)).Width(10)

	sRule = lipgloss.NewStyle().Foreground(c(t.Border))
)

// badge is a solid pill.
func badge(text, fg, bg string) string {
	return lipgloss.NewStyle().Foreground(c(fg)).Background(c(bg)).Bold(true).Padding(0, 1).Render(text)
}

// button renders a clickable-looking button; hot = keyboard-focused.
func button(label, key, color string, hot bool) string {
	st := lipgloss.NewStyle().Padding(0, 1).Border(lipgloss.RoundedBorder()).BorderForeground(c(color))
	k := lipgloss.NewStyle().Foreground(c(t.Dim)).Render(" " + key)
	if hot {
		st = st.BorderForeground(c(t.Bright)).Background(c(color)).Foreground(c(t.Bg)).Bold(true)
		k = lipgloss.NewStyle().Foreground(c(t.Overlay)).Background(c(color)).Render(" " + key)
	} else {
		st = st.Foreground(c(color))
	}
	return st.Render(label + k)
}

// gradient colors each rune of s along the theme's accent ramp.
func gradient(s string, from, to string, bold bool) string {
	a, _ := colorful.Hex(from)
	b, _ := colorful.Hex(to)
	runes := []rune(s)
	var out strings.Builder
	for i, r := range runes {
		f := 0.0
		if len(runes) > 1 {
			f = float64(i) / float64(len(runes)-1)
		}
		st := lipgloss.NewStyle().Foreground(lipgloss.Color(a.BlendLab(b, f).Hex()))
		if bold {
			st = st.Bold(true)
		}
		out.WriteString(st.Render(string(r)))
	}
	return out.String()
}

func rule(w int) string { return sRule.Render(strings.Repeat("─", max(0, w))) }

// fit truncates (ANSI-aware) and pads s to exactly w cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "…")
	if pad := w - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

func trunc(s string, w int) string { return ansi.Truncate(s, max(0, w), "…") }

// progress renders a thin bar for the fraction left.
func progress(frac float64, w int, color string) string {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	full := int(frac*float64(w) + 0.5)
	return lipgloss.NewStyle().Foreground(c(color)).Render(strings.Repeat("━", full)) +
		sFaint.Render(strings.Repeat("━", w-full))
}

// urgency colors a countdown by seconds left.
func urgency(secs float64) string {
	switch {
	case secs < 20:
		return t.Red
	case secs < 60:
		return t.Orange
	default:
		return t.Mint
	}
}

func levelGlyph(l view.Level) string {
	switch l {
	case view.LevelFull:
		return sOrange.Render("⚡")
	case view.LevelPartial:
		return sYellow.Render("◐")
	case view.LevelLocked:
		return sRed.Render("■")
	default:
		return sFaint.Render("○")
	}
}

func levelText(l view.Level) string {
	switch l {
	case view.LevelFull:
		return sOrange.Bold(true).Render("BYPASS")
	case view.LevelPartial:
		return sYellow.Render("partial")
	case view.LevelLocked:
		return sRed.Render("locked")
	default:
		return sDim.Render("asks")
	}
}

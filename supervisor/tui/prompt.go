package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// prompt is a small text-entry dialog: one or more labelled fields.
type prompt struct {
	title, hint string
	labels      []string
	inputs      []textinput.Model
	focus       int
	err         string
	submit      func(m *model, values []string) (tea.Cmd, string) // returns an error message to keep it open
}

func newPrompt(title, hint string, labels, placeholders []string, submit func(*model, []string) (tea.Cmd, string)) *prompt {
	p := &prompt{title: title, hint: hint, labels: labels, submit: submit}
	for i := range labels {
		in := textinput.New()
		in.Prompt = ""
		in.Placeholder = placeholders[i]
		in.CharLimit = 2000
		in.Width = 52
		in.TextStyle = sBright
		in.PlaceholderStyle = sFaint
		in.Cursor.Style = sAccent
		p.inputs = append(p.inputs, in)
	}
	p.inputs[0].Focus()
	return p
}

func (p *prompt) setFocus(i int) {
	p.inputs[p.focus].Blur()
	p.focus = (i + len(p.inputs)) % len(p.inputs)
	p.inputs[p.focus].Focus()
}

func (p *prompt) update(m *model, msg tea.Msg) tea.Cmd {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "esc":
			m.prompt = nil
			return nil
		case "tab", "down":
			p.setFocus(p.focus + 1)
			return nil
		case "shift+tab", "up":
			p.setFocus(p.focus - 1)
			return nil
		case "enter":
			if p.focus < len(p.inputs)-1 {
				p.setFocus(p.focus + 1)
				return nil
			}
			vals := make([]string, len(p.inputs))
			for i, in := range p.inputs {
				vals[i] = strings.TrimSpace(in.Value())
			}
			cmd, errMsg := p.submit(m, vals)
			if errMsg != "" {
				p.err = errMsg
				return nil
			}
			m.prompt = nil
			return cmd
		}
	}
	var cmd tea.Cmd
	p.inputs[p.focus], cmd = p.inputs[p.focus].Update(msg)
	return cmd
}

func (p *prompt) render() string {
	rows := []string{sBold.Render(p.title)}
	if p.hint != "" {
		rows = append(rows, lipgloss.NewStyle().Width(66).Foreground(c(t.Dim)).Render(p.hint))
	}
	rows = append(rows, "")
	for i, l := range p.labels {
		mark, st := "  ", sLav
		if i == p.focus {
			mark, st = sAccent.Render("▸ "), sAccent.Bold(true)
		}
		rows = append(rows, mark+st.Width(12).Render(l)+p.inputs[i].View())
	}
	if p.err != "" {
		rows = append(rows, "", sRed.Render("✗ "+p.err))
	}
	rows = append(rows, "", sFaint.Render("enter next/submit · tab move · esc cancel"))
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c(t.Cyan)).Padding(1, 2).
		Render(strings.Join(rows, "\n"))
}

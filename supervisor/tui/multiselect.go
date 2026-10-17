package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	zone "github.com/lrstanley/bubblezone"
)

// msItem is one choice in a multiSelect. Items with a group are listed under
// a header for it (e.g. AWS roles under their account).
type msItem struct {
	value, label, group string
	warn                bool // shown with a ⚠ (admin or production AWS roles)
}

// msRow is one visible line: a group header or an item.
type msRow struct {
	group string
	item  *msItem
}

// multiSelect is a filterable checklist: type to filter, ↑↓ to move, space to
// toggle (a header toggles its whole group), ctrl+a to toggle everything
// visible. The grant editor uses it for credentials, AWS roles and Arc profiles.
type multiSelect struct {
	name    string // zone prefix and what the items are called ("credentials")
	items   []msItem
	checked map[string]bool
	filter  string
	cur     int
	off     int
	rows    int
}

func newMultiSelect(name string, rows int) *multiSelect {
	return &multiSelect{name: name, checked: map[string]bool{}, rows: rows}
}

// setItems replaces the choices, keeping checked values that disappeared so a
// prefilled grant never silently widens or narrows.
func (s *multiSelect) setItems(items []msItem) {
	have := map[string]bool{}
	for _, it := range items {
		have[it.value] = true
	}
	for v := range s.checked {
		if !have[v] {
			items = append(items, msItem{value: v, label: v})
		}
	}
	// Picks (and their groups) first, so a prefilled list shows what's ticked.
	picked := map[string]bool{}
	for _, it := range items {
		if s.checked[it.value] {
			picked[it.group] = true
		}
	}
	first := func(it msItem) bool {
		if it.group != "" {
			return picked[it.group]
		}
		return s.checked[it.value]
	}
	sort.SliceStable(items, func(i, j int) bool {
		if a, b := first(items[i]), first(items[j]); a != b {
			return a
		}
		if items[i].group != items[j].group {
			return strings.ToLower(items[i].group) < strings.ToLower(items[j].group)
		}
		return strings.ToLower(items[i].label) < strings.ToLower(items[j].label)
	})
	s.items = items
	s.cur, s.off = 0, 0
}

func (s *multiSelect) visible() []msRow {
	f := strings.ToLower(s.filter)
	var out []msRow
	last := "\x00"
	for i := range s.items {
		it := &s.items[i]
		if f != "" && !strings.Contains(strings.ToLower(it.label), f) && !strings.Contains(strings.ToLower(it.group), f) {
			continue
		}
		if it.group != "" && it.group != last {
			out = append(out, msRow{group: it.group})
		}
		last = it.group
		out = append(out, msRow{item: it})
	}
	return out
}

func (s *multiSelect) selected() []string {
	var out []string
	for v := range s.checked {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func (s *multiSelect) labelOf(v string) string {
	for _, it := range s.items {
		if it.value == v {
			return it.label
		}
	}
	return v
}

// groupItems: the visible items under a header.
func (s *multiSelect) groupItems(rows []msRow, group string) []*msItem {
	var out []*msItem
	for _, r := range rows {
		if r.item != nil && r.item.group == group {
			out = append(out, r.item)
		}
	}
	return out
}

func (s *multiSelect) toggle(rows []msRow, i int) {
	if i < 0 || i >= len(rows) {
		return
	}
	r := rows[i]
	if r.item != nil {
		if s.checked[r.item.value] {
			delete(s.checked, r.item.value)
		} else {
			s.checked[r.item.value] = true
		}
		return
	}
	items := s.groupItems(rows, r.group)
	all := len(items) > 0
	for _, it := range items {
		all = all && s.checked[it.value]
	}
	for _, it := range items {
		if all {
			delete(s.checked, it.value)
		} else {
			s.checked[it.value] = true
		}
	}
}

// key handles a key press while the list has focus. It returns -1 or +1 when
// the cursor runs off the top or bottom, so the form can move focus.
func (s *multiSelect) key(km tea.KeyMsg) (leave int) {
	rows := s.visible()
	switch k := km.String(); k {
	case "up":
		if s.cur == 0 {
			return -1
		}
		s.cur--
	case "down":
		if s.cur >= len(rows)-1 {
			return 1
		}
		s.cur++
	case " ":
		s.toggle(rows, s.cur)
	case "backspace":
		if s.filter != "" {
			s.filter = s.filter[:len(s.filter)-1]
			s.cur, s.off = 0, 0
		}
	case "ctrl+a":
		all := true
		for _, r := range rows {
			if r.item != nil {
				all = all && s.checked[r.item.value]
			}
		}
		for _, r := range rows {
			if r.item == nil {
				continue
			}
			if all {
				delete(s.checked, r.item.value)
			} else {
				s.checked[r.item.value] = true
			}
		}
	default:
		if km.Type == tea.KeyRunes && !km.Alt {
			s.filter += strings.TrimSpace(string(km.Runes))
			s.cur, s.off = 0, 0
		}
	}
	return 0
}

// mouse: a click toggles the row under it; the wheel moves the cursor.
func (s *multiSelect) mouse(msg tea.MouseMsg, click bool, dir int) bool {
	rows := s.visible()
	if !click {
		if zone.Get("ms:" + s.name + ":list").InBounds(msg) {
			s.cur = max(0, min(len(rows)-1, s.cur+dir))
			return true
		}
		return false
	}
	for i := s.off; i < min(len(rows), s.off+s.rows); i++ {
		if zone.Get("ms:" + s.name + ":" + strconv.Itoa(i)).InBounds(msg) {
			s.cur = i
			s.toggle(rows, i)
			return true
		}
	}
	return false
}

// render returns the list's lines: a filter/status line, then the rows.
func (s *multiSelect) render(width int, focused bool, loading bool, err string) []string {
	status := sDim.Render(fmt.Sprintf("%d selected", len(s.checked)))
	filter := sFaint.Render("type to filter " + s.name)
	if s.filter != "" {
		filter = sCyan.Render("/" + s.filter)
	}
	out := []string{filter + "  " + status}
	var list []string
	switch {
	case loading:
		list = append(list, sDim.Render("loading "+s.name+"…"))
	case err != "":
		list = append(list, sRed.Render(trunc(err, width)))
	default:
		rows := s.visible()
		if s.cur >= len(rows) {
			s.cur = max(0, len(rows)-1)
		}
		if s.cur < s.off {
			s.off = s.cur
		}
		if s.cur >= s.off+s.rows {
			s.off = s.cur - s.rows + 1
		}
		if len(rows) == 0 {
			list = append(list, sDim.Render("no matches"))
		}
		for i := s.off; i < min(len(rows), s.off+s.rows); i++ {
			r := rows[i]
			var line string
			if r.item == nil {
				items := s.groupItems(rows, r.group)
				n := 0
				for _, it := range items {
					if s.checked[it.value] {
						n++
					}
				}
				box := sFaint.Render("[ ]")
				switch {
				case n == len(items) && n > 0:
					box = sMint.Render("[✓]")
				case n > 0:
					box = sYellow.Render("[–]")
				}
				line = box + " " + sLav.Bold(true).Render(trunc(r.group, width-8))
			} else {
				box := sFaint.Render("[ ]")
				st := sBase
				if s.checked[r.item.value] {
					box = sMint.Render("[✓]")
					st = sBright
				}
				indent := ""
				if r.item.group != "" {
					indent = "  "
				}
				label := r.item.label
				if r.item.warn {
					label = "⚠ " + label
				}
				line = indent + box + " " + st.Render(trunc(label, width-8-len(indent)))
			}
			if focused && i == s.cur {
				line = lipgloss.NewStyle().Background(c(t.Border)).Render(fit(line, width))
			}
			list = append(list, zone.Mark("ms:"+s.name+":"+strconv.Itoa(i), line))
		}
		if more := len(rows) - (s.off + s.rows); more > 0 {
			list = append(list, sFaint.Render(fmt.Sprintf("  … %d more", more)))
		}
	}
	block := zone.Mark("ms:"+s.name+":list", strings.Join(list, "\n"))
	return append(out, strings.Split(block, "\n")...)
}

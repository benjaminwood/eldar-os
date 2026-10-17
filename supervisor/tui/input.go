package tui

import (
	"strconv"

	tea "github.com/charmbracelet/bubbletea"
	zone "github.com/lrstanley/bubblezone"

	"github.com/benjaminwood/eldar-os/supervisor/api"
)

// Detail-pane buttons for a pending request, in display order.
var pendingButtons = []string{"approve", "deny", "always", "grant"}

func (m *model) key(msg tea.KeyMsg) tea.Cmd {
	k := msg.String()
	if k == "ctrl+c" {
		return tea.Quit
	}
	if m.prompt != nil {
		return m.prompt.update(m, msg)
	}
	if m.editor != nil {
		return m.editor.update(m, msg)
	}
	if m.popup != nil {
		return m.popup.key(m, k)
	}
	if m.auditDetail != nil {
		m.auditDetail.key(m, k)
		return nil
	}
	if m.help {
		m.help = false
		return nil
	}

	switch k {
	case "q":
		return tea.Quit
	case "?":
		m.help = true
		return nil
	case "1", "2", "3", "4", "5":
		return m.setTab(tab(k[0] - '1'))
	case "tab":
		return m.setTab((m.tab + 1) % tabCount)
	case "shift+tab":
		return m.setTab((m.tab + tabCount - 1) % tabCount)
	case "m":
		m.popup = modePopup(m)
		return nil
	case "p":
		return m.setPush(!m.st.Settings.MobilePush)
	}

	switch m.tab {
	case tabApprovals:
		return m.approvalsKey(k)
	case tabSessions:
		return m.sessionsKey(k)
	case tabGrants:
		return m.grantsKey(k)
	case tabSettings:
		return m.settingsKey(k)
	case tabActivity:
		return m.activityKey(k)
	}
	return nil
}

func (m *model) setTab(tb tab) tea.Cmd {
	m.tab = tb
	if tb == tabActivity {
		return m.loadAudit()
	}
	return nil
}

func (m *model) movePending(delta int) {
	n := len(m.st.Pending)
	if n == 0 {
		return
	}
	i := m.pendingIndex() + delta
	i = max(0, min(n-1, i))
	if m.st.Pending[i].ID != m.pSel {
		m.pSel = m.st.Pending[i].ID
		m.cmdOff = 0
	}
}

func (m *model) approvalsKey(k string) tea.Cmd {
	switch k {
	case "j", "down":
		m.movePending(1)
	case "k", "up":
		m.movePending(-1)
	case "g", "G":
		if k == "G" {
			m.movePending(len(m.st.Pending))
			return nil
		}
		if p := m.selectedPending(); p != nil {
			return m.openEditor(editorFromPending(m, *p))
		}
	case "home":
		m.movePending(-len(m.st.Pending))
	case "end":
		m.movePending(len(m.st.Pending))
	case " ", "x":
		if m.pSel != 0 {
			if m.marks[m.pSel] {
				delete(m.marks, m.pSel)
			} else {
				m.marks[m.pSel] = true
			}
			m.movePending(1)
		}
	case "*", "ctrl+a":
		if len(m.marks) == len(m.st.Pending) {
			m.marks = map[int]bool{}
		} else {
			for _, p := range m.st.Pending {
				m.marks[p.ID] = true
			}
		}
	case "esc":
		m.marks = map[int]bool{}
		m.btnHot = -1
	case "a", "y":
		return m.decide(api.Approve)
	case "d", "n":
		return m.decide(api.Deny)
	case "A":
		return m.decide(api.AllowAlways)
	case "J", "pgdown":
		m.cmdOff += 5
	case "K", "pgup":
		m.cmdOff = max(0, m.cmdOff-5)
	case "left", "h":
		if m.btnHot <= 0 {
			m.btnHot = len(pendingButtons) - 1
		} else {
			m.btnHot--
		}
	case "right", "l":
		m.btnHot = (m.btnHot + 1) % len(pendingButtons)
	case "enter":
		if m.btnHot >= 0 {
			return m.pendingButton(pendingButtons[m.btnHot])
		}
	}
	return nil
}

func (m *model) pendingButton(name string) tea.Cmd {
	switch name {
	case "approve":
		return m.decide(api.Approve)
	case "deny":
		return m.decide(api.Deny)
	case "always":
		return m.decide(api.AllowAlways)
	case "grant":
		if p := m.selectedPending(); p != nil {
			return m.openEditor(editorFromPending(m, *p))
		}
	}
	return nil
}

func (m *model) moveSession(delta int) {
	i := m.sessionIndex()
	if i < 0 {
		return
	}
	i = max(0, min(len(m.sessions)-1, i+delta))
	m.sSel = m.sessions[i].ID
}

func (m *model) sessionsKey(k string) tea.Cmd {
	i := m.sessionIndex()
	switch k {
	case "j", "down":
		m.moveSession(1)
	case "k", "up":
		m.moveSession(-1)
	case "home":
		m.moveSession(-len(m.sessions))
	case "end", "G":
		m.moveSession(len(m.sessions))
	case "b", "enter":
		if i >= 0 {
			return m.toggleSessionBypass(m.sessions[i])
		}
	case "g":
		if i >= 0 {
			return m.openEditor(newEditor(m, m.sessions[i].ID))
		}
	case "x", "delete":
		if i >= 0 {
			return m.clearSession(m.sessions[i])
		}
	case "u":
		if i >= 0 && m.sessions[i].Pinned {
			return m.unpin(m.sessions[i])
		}
	case "B":
		m.popup = modePopup(m)
	}
	return nil
}

func (m *model) grantsKey(k string) tea.Cmd {
	rows := m.grantRows()
	i := m.grantIndex(rows)
	move := func(d int) {
		if i >= 0 {
			m.gSel = rows[max(0, min(len(rows)-1, i+d))].key
		}
	}
	switch k {
	case "j", "down":
		move(1)
	case "k", "up":
		move(-1)
	case "n", "g":
		return m.openEditor(newEditor(m, "*"))
	case "x", "delete", "backspace":
		if i >= 0 {
			return m.revoke(rows[i])
		}
	}
	return nil
}

// ── mouse ──────────────────────────────────────────────────────────────────

func (m *model) mouse(msg tea.MouseMsg) tea.Cmd {
	wheel := msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown
	click := msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft
	if !wheel && !click {
		return nil
	}
	dir := 1
	if msg.Button == tea.MouseButtonWheelUp {
		dir = -1
	}

	if m.prompt != nil {
		return nil
	}
	if m.auditDetail != nil {
		if click {
			m.auditDetail = nil
		} else if dir > 0 {
			m.auditDetail.off += 3
		} else {
			m.auditDetail.off = max(0, m.auditDetail.off-3)
		}
		return nil
	}
	if m.editor != nil {
		return m.editor.mouse(m, msg, click, dir)
	}
	if m.popup != nil {
		if click {
			for i := range m.popup.opts {
				if zone.Get("pop:" + strconv.Itoa(i)).InBounds(msg) {
					return m.popup.choose(m, i)
				}
			}
			if !zone.Get("popup").InBounds(msg) {
				m.popup = nil
			}
		} else {
			m.popup.move(dir)
		}
		return nil
	}
	if m.help {
		if click {
			m.help = false
		}
		return nil
	}

	if click {
		for i := range tabNames {
			if zone.Get("tab:" + strconv.Itoa(i)).InBounds(msg) {
				return m.setTab(tab(i))
			}
		}
		if zone.Get("mode").InBounds(msg) {
			m.popup = modePopup(m)
			return nil
		}
		if zone.Get("push").InBounds(msg) {
			return m.setPush(!m.st.Settings.MobilePush)
		}
		if zone.Get("help").InBounds(msg) {
			m.help = true
			return nil
		}
	}

	switch m.tab {
	case tabApprovals:
		if wheel {
			if zone.Get("cmd").InBounds(msg) {
				m.cmdOff = max(0, m.cmdOff+dir*2)
			} else {
				m.movePending(dir)
			}
			return nil
		}
		for _, p := range m.st.Pending {
			id := strconv.Itoa(p.ID)
			if zone.Get("pm:" + id).InBounds(msg) {
				if m.marks[p.ID] {
					delete(m.marks, p.ID)
				} else {
					m.marks[p.ID] = true
				}
				return nil
			}
			if zone.Get("p:" + id).InBounds(msg) {
				if m.pSel != p.ID {
					m.pSel, m.cmdOff = p.ID, 0
				}
				return nil
			}
		}
		for _, b := range pendingButtons {
			if zone.Get("btn:" + b).InBounds(msg) {
				return m.pendingButton(b)
			}
		}
	case tabSessions:
		if wheel {
			m.moveSession(dir)
			return nil
		}
		for _, s := range m.sessions {
			if zone.Get("s:" + s.ID).InBounds(msg) {
				m.sSel = s.ID
				return nil
			}
		}
		for _, a := range []string{"b", "g", "x", "u"} {
			if zone.Get("sb:" + a).InBounds(msg) {
				return m.sessionsKey(a)
			}
		}
		for _, g := range m.st.Grants {
			if zone.Get("sx:" + strconv.Itoa(g.ID)).InBounds(msg) {
				return m.revoke(grantRow{grant: &g})
			}
		}
	case tabGrants:
		rows := m.grantRows()
		if wheel {
			if i := m.grantIndex(rows); i >= 0 {
				m.gSel = rows[max(0, min(len(rows)-1, i+dir))].key
			}
			return nil
		}
		for _, r := range rows {
			if zone.Get("gx:" + r.key).InBounds(msg) {
				return m.revoke(r)
			}
			if zone.Get("gr:" + r.key).InBounds(msg) {
				m.gSel = r.key
				return nil
			}
		}
		if zone.Get("gnew").InBounds(msg) {
			return m.openEditor(newEditor(m, "*"))
		}
	case tabActivity:
		list := m.visibleAudit()
		if wheel {
			if i := m.auditIndex(list); i >= 0 {
				m.aSel = auditKey(list[max(0, min(len(list)-1, i+dir))])
			}
			return nil
		}
		for _, e := range list {
			if zone.Get("au:" + auditKey(e)).InBounds(msg) {
				if m.aSel == auditKey(e) {
					return m.openAuditDetail(e)
				}
				m.aSel = auditKey(e)
				return nil
			}
		}
	case tabSettings:
		rows := m.settingRows()
		if wheel {
			if i := m.settingIndex(rows); i >= 0 {
				m.cSel = rows[max(0, min(len(rows)-1, i+dir))].key
			}
			return nil
		}
		for _, r := range rows {
			if zone.Get("cn:" + r.key).InBounds(msg) {
				m.cSel = r.key
				return nil
			}
		}
	}
	return nil
}

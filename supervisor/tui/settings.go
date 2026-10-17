package tui

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	zone "github.com/lrstanley/bubblezone"

	"github.com/benjaminwood/eldar-os/supervisor/api"
	"github.com/benjaminwood/eldar-os/supervisor/view"
)

// settingRow is one selectable line on the Settings tab.
type settingRow struct {
	key  string // "p:<name>" sign-in, "s:<name>" server, "c:<id>" cron, "k:<name>" cached creds, "h:<host>" a remote host, "ha:<host>:<pattern>" its allow list, "hg:<host>:<id>" its grant
	kind byte
	id   string
	host string // remote host rows: which host
}

func (m *model) settingRows() []settingRow {
	var rows []settingRow
	for _, p := range m.st.MCP.Pending {
		rows = append(rows, settingRow{"p:" + p.Name, 'p', p.Name, ""})
	}
	for _, e := range m.st.Cache {
		rows = append(rows, settingRow{"k:" + e.Name, 'k', e.Name, ""})
	}
	for _, h := range m.hosts() {
		rows = append(rows, settingRow{"h:" + h.Name, 'h', h.Name, h.Name})
		for _, g := range m.hostGrants(h.Name) {
			rows = append(rows, settingRow{fmt.Sprintf("hg:%s:%d", h.Name, g.ID), 'g', fmt.Sprint(g.ID), h.Name})
		}
		for _, p := range h.Allow {
			rows = append(rows, settingRow{"ha:" + h.Name + ":" + p, 'a', p, h.Name})
		}
	}
	for _, s := range m.st.MCP.Servers {
		rows = append(rows, settingRow{"s:" + s.Name, 's', s.Name, ""})
	}
	for _, c := range m.st.Crons {
		rows = append(rows, settingRow{"c:" + c.ID, 'c', c.ID, ""})
	}
	return rows
}

func (m *model) settingIndex(rows []settingRow) int {
	for i, r := range rows {
		if r.key == m.cSel {
			return i
		}
	}
	if len(rows) > 0 {
		m.cSel = rows[0].key
		return 0
	}
	return -1
}

// hosts: remote hosts agents may run commands on (Ben's Mac is "mac").
func (m *model) hosts() []api.HostStatus {
	var out []api.HostStatus
	for _, h := range m.st.Hosts {
		if h.Configured {
			out = append(out, h)
		}
	}
	return out
}

func (m *model) hostLabel(name string) string {
	for _, h := range m.st.Hosts {
		if h.Name == name {
			return h.Label
		}
	}
	return name
}

// selectedHost: the host whose section the cursor is in, else the first.
func (m *model) selectedHost() string {
	rows := m.settingRows()
	if i := m.settingIndex(rows); i >= 0 && rows[i].host != "" {
		return rows[i].host
	}
	if hs := m.hosts(); len(hs) > 0 {
		return hs[0].Name
	}
	return ""
}

func (m *model) hostGrants(host string) []api.Grant {
	var out []api.Grant
	for _, g := range m.st.Grants {
		if g.Target == host {
			out = append(out, g)
		}
	}
	return out
}

// hostBypass returns the IDs of a host's grants that cover everything.
func (m *model) hostBypass(host string) []int {
	var ids []int
	for _, g := range m.hostGrants(host) {
		if g.Session == "*" && g.Profile == "*" && g.Credentials.Any {
			ids = append(ids, g.ID)
		}
	}
	return ids
}

func hostBypassPopup(host, label string) *popup {
	grant := func(min *int) func(*model) tea.Cmd {
		return func(m *model) tea.Cmd {
			return m.addGrant(api.GrantInput{Target: host, Session: "*", Profile: "*", Credentials: api.AnyCreds, Minutes: min, Note: label + " bypass"}, 0)
		}
	}
	return &popup{
		title:    "Bypass " + label,
		subtitle: "Every command there runs without asking (policy denies and lockdown still apply)",
		opts: []popupOpt{
			{key: "1", label: "15 minutes", color: t.Lavender, run: grant(intp(15))},
			{key: "2", label: "1 hour", color: t.Lavender, run: grant(intp(60))},
			{key: "3", label: "4 hours", color: t.Lavender, run: grant(intp(240))},
			{key: "4", label: "Until I turn it off", color: t.Lavender, run: grant(nil)},
		},
		sel: 1,
	}
}

// copyToClipboard asks the terminal to set the clipboard (OSC 52). Works in
// most modern terminals, and through tmux with set-clipboard on.
func copyToClipboard(s string) {
	fmt.Fprintf(os.Stderr, "\x1b]52;c;%s\a", base64.StdEncoding.EncodeToString([]byte(s)))
}

var (
	mcpName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	cronID  = regexp.MustCompile(`^[A-Za-z0-9-]{4,80}$`)
)

func (m *model) settingsKey(k string) tea.Cmd {
	rows := m.settingRows()
	i := m.settingIndex(rows)
	client := m.client
	switch k {
	case "j", "down":
		if i >= 0 {
			m.cSel = rows[min(len(rows)-1, i+1)].key
		}
	case "k", "up":
		if i >= 0 {
			m.cSel = rows[max(0, i-1)].key
		}
	case "y", "enter":
		if i >= 0 && rows[i].kind == 'p' {
			for _, p := range m.st.MCP.Pending {
				if p.Name == rows[i].id && p.AuthURL != "" {
					copyToClipboard(p.AuthURL)
					m.flash("✓ Sign-in link for "+p.Name+" copied — paste it into your browser", t.Mint)
				}
			}
		}
	case "t":
		host := m.selectedHost()
		if host == "" {
			return nil
		}
		label := m.hostLabel(host)
		m.flash("Testing the connection to "+label+"…", t.Dim)
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			r, err := client.HostTest(ctx, host)
			if err != nil {
				return resultMsg{err: err}
			}
			if !r.OK {
				return resultMsg{err: fmt.Errorf("%s unreachable: %s", label, r.Detail)}
			}
			return resultMsg{ok: "✓ " + label + " reachable — " + r.Detail}
		}
	case "b":
		host := m.selectedHost()
		if host == "" {
			return nil
		}
		label := m.hostLabel(host)
		if ids := m.hostBypass(host); len(ids) > 0 {
			return m.call("✓ "+label+" bypass off", func(ctx context.Context) error {
				for _, id := range ids {
					if err := client.RevokeGrant(ctx, id); err != nil {
						return err
					}
				}
				return nil
			})
		}
		m.popup = hostBypassPopup(host, label)
	case "a":
		host := m.selectedHost()
		if host == "" {
			return nil
		}
		label := m.hostLabel(host)
		m.prompt = newPrompt("Always allow on "+label,
			"A glob over the whole command; * matches anything. Commands that match run there without asking.",
			[]string{"Pattern"}, []string{"e.g. git status*  or  brew list*"},
			func(m *model, v []string) (tea.Cmd, string) {
				if v[0] == "" || v[0] == "*" {
					return nil, "give a specific pattern — for everything, use a bypass (b) so it can expire"
				}
				return m.call("✓ Added to the "+label+" allow list", func(ctx context.Context) error { return client.HostAllow(ctx, host, v[0], false) }), ""
			})
	case "X":
		if len(m.st.Cache) == 0 {
			return nil
		}
		return m.call("✓ Credential cache cleared — next requests refetch", func(ctx context.Context) error {
			_, err := client.ClearCache(ctx, "")
			return err
		})
	case "x", "delete":
		if i < 0 {
			return nil
		}
		r := rows[i]
		if r.kind == 'k' {
			return m.call("✓ Cleared "+r.id+" — next request refetches", func(ctx context.Context) error {
				_, err := client.ClearCache(ctx, r.id)
				return err
			})
		}
		if r.kind == 'h' {
			return nil
		}
		if r.kind == 'a' {
			return m.call("✓ Removed from the "+m.hostLabel(r.host)+" allow list", func(ctx context.Context) error { return client.HostAllow(ctx, r.host, r.id, true) })
		}
		if r.kind == 'g' {
			id, _ := strconv.Atoi(r.id)
			return m.call(fmt.Sprintf("✓ %s grant #%d revoked", m.hostLabel(r.host), id), func(ctx context.Context) error { return client.RevokeGrant(ctx, id) })
		}
		if r.kind == 'c' {
			return m.call("✓ Cron removed from auto-approval", func(ctx context.Context) error { return client.RevokeCron(ctx, r.id) })
		}
		return m.call("✓ MCP server "+r.id+" removed", func(ctx context.Context) error { return client.RemoveMCP(ctx, r.id) })
	case "n":
		m.prompt = newPrompt("Connect an MCP server", "Starts an OAuth sign-in; the link appears here and in the web app.",
			[]string{"Name", "Server URL"}, []string{"e.g. linear", "https://mcp.example.com/mcp"},
			func(m *model, v []string) (tea.Cmd, string) {
				if !mcpName.MatchString(v[0]) {
					return nil, "name: letters, digits, - and _ only"
				}
				if !strings.HasPrefix(v[1], "http://") && !strings.HasPrefix(v[1], "https://") {
					return nil, "server URL must start with https://"
				}
				return func() tea.Msg {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					link, err := client.RegisterMCP(ctx, v[0], v[1])
					if err != nil {
						return resultMsg{err: err}
					}
					copyToClipboard(link)
					return resultMsg{ok: "✓ Sign-in started for " + v[0] + " — link copied to your clipboard"}
				}, ""
			})
	case "v":
		m.prompt = newPrompt("Finish a sign-in from its redirect",
			"If the browser couldn't reach the proxy after you signed in, paste the address it landed on.",
			[]string{"Redirect URL"}, []string{"https://…/oauth/callback?code=…&state=…"},
			func(m *model, v []string) (tea.Cmd, string) {
				if !strings.Contains(v[0], "code=") || !strings.Contains(v[0], "state=") {
					return nil, "that URL has no code/state in it"
				}
				return func() tea.Msg {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					name, err := client.CompleteMCP(ctx, v[0])
					if err != nil {
						return resultMsg{err: err}
					}
					return resultMsg{ok: "✓ " + name + " is connected"}
				}, ""
			})
	case "c":
		m.prompt = newPrompt("Allow a cron to auto-approve",
			"While that cron is running, its requests are approved without asking.",
			[]string{"Cron ID", "Name"}, []string{"the job's id from jobs.json", "what it does"},
			func(m *model, v []string) (tea.Cmd, string) {
				if !cronID.MatchString(v[0]) {
					return nil, "cron ID: 4–80 letters, digits or dashes"
				}
				return m.call("✓ Cron allowed", func(ctx context.Context) error { return client.AllowCron(ctx, v[0], v[1]) }), ""
			})
	}
	return nil
}

func (m *model) settingsView(w, h int) string {
	rows := m.settingRows()
	idx := m.settingIndex(rows)
	sel := func(key string) bool { return idx >= 0 && rows[idx].key == key }
	line := func(key, s string) string {
		bar := "  "
		if sel(key) {
			bar = sAccent.Render("▌ ")
			s = lipgloss.NewStyle().Background(c(t.Surface)).Render(fit(s, w-4))
		}
		return zone.Mark("cn:"+key, bar+s)
	}
	section := func(title, hint string) string {
		return " " + sAccent.Bold(true).Render(title) + "  " + sFaint.Render(hint)
	}
	var out []string

	if len(m.st.MCP.Pending) > 0 {
		out = append(out, section("Waiting for you to sign in", "y copy link · v paste redirect · x cancel"), "")
		for _, p := range m.st.MCP.Pending {
			out = append(out, line("p:"+p.Name, sOrange.Render("🔗 ")+sBold.Render(p.Name)+"  "+sDim.Render(p.ServerURL)))
			if p.AuthURL != "" {
				for _, l := range strings.Split(lipgloss.NewStyle().Width(w-8).Render(p.AuthURL), "\n") {
					out = append(out, "      "+sFaint.Render(l))
				}
			} else {
				out = append(out, "      "+sDim.Render("link lost — x, then n to start again"))
			}
		}
		out = append(out, "")
	}

	out = append(out, section("Credential cache", "x clear · X clear all"), "")
	if len(m.st.Cache) == 0 {
		out = append(out, "  "+sDim.Render("empty — the next request for a profile fetches from 1Password"))
	}
	for _, e := range m.st.Cache {
		src := sCyan.Render("1Password")
		name := e.Name
		if e.Kind == "aws" {
			src, name = sOrange.Render("AWS STS"), strings.TrimPrefix(e.Name, "aws-")
		}
		out = append(out, line("k:"+e.Name, fit(src, 11)+fit(sBright.Render(name), 48)+sDim.Render("cached "+view.Ago(e.CachedAt, m.now))))
	}
	for _, h := range m.hosts() {
		out = append(out, "", section(h.Label+" · "+h.Host, "t test · b bypass · a always allow · x remove"), "")
		status := sDim.Render("not tested yet — press t")
		if lt := h.LastTest; lt != nil {
			if lt.OK {
				status = sMint.Render("● reachable") + sDim.Render("  "+lt.Detail+" · checked "+view.Ago(lt.At, m.now))
			} else {
				status = sRed.Render("○ unreachable") + sDim.Render("  "+trunc(lt.Detail, w-40))
			}
		}
		bypass := sDim.Render("   every command there asks")
		if len(m.hostBypass(h.Name)) > 0 {
			bypass = sOrange.Bold(true).Render("   ⚡ bypass on") + sDim.Render(" — every command there runs without asking")
		}
		out = append(out, line("h:"+h.Name, status+bypass))
		for _, g := range m.hostGrants(h.Name) {
			who := "every session"
			if g.Session != "*" {
				who = view.SessionTitle(m.sessions, g.Session)
			}
			exp := "until revoked"
			if g.ExpiresAt != nil {
				exp = view.Remaining(*g.ExpiresAt, m.now) + " left"
			}
			out = append(out, line(fmt.Sprintf("hg:%s:%d", h.Name, g.ID), sLav.Render(fmt.Sprintf("grant #%d ", g.ID))+sBright.Render(trunc(who, 30))+sDim.Render("  "+g.Profile+" · "+g.Credentials.String()+" · "+exp)))
		}
		out = append(out, "", "  "+sLav.Render("Always allowed:"))
		if len(h.Allow) == 0 {
			out = append(out, "  "+sDim.Render("nothing yet — approve one of its requests with A, or add a pattern with a"))
		}
		for _, p := range h.Allow {
			out = append(out, line("ha:"+h.Name+":"+p, sCyan.Render(p)))
		}
	}
	out = append(out, "", section("MCP servers", "n connect · x remove"), "")
	if len(m.st.MCP.Servers) == 0 {
		out = append(out, "  "+sDim.Render("none registered"))
	}
	for _, s := range m.st.MCP.Servers {
		status := sMint.Render("● connected")
		if !s.HasToken {
			status = sRed.Render("○ no token")
		} else if s.ExpiresAt != nil {
			if left := view.Remaining(*s.ExpiresAt, m.now); left == "expired" {
				status += sDim.Render("  token expired — refreshes on next use")
			} else {
				status += sDim.Render("  token refreshes in " + left)
			}
		}
		out = append(out, line("s:"+s.Name, fit(sBright.Render(s.Name), 22)+fit(sDim.Render(s.ServerURL), max(10, min(60, w-70)))+"  "+status))
	}

	out = append(out, "", section("Cron auto-approval", "c allow · x remove"), "")
	if len(m.st.Crons) == 0 {
		out = append(out, "  "+sDim.Render("no crons allowed"))
	}
	for _, cr := range m.st.Crons {
		when := ""
		if t, err := time.Parse(time.RFC3339, cr.AuthorizedAt); err == nil {
			when = "allowed " + view.Ago(t.UnixMilli(), m.now)
		}
		name := sBright.Render(cr.Name)
		if cr.Name == "" || cr.Name == cr.ID {
			name = sDim.Render("(unnamed)")
		}
		out = append(out, line("c:"+cr.ID, fit(name, 36)+fit(sFaint.Render(cr.ID), 40)+sDim.Render(when)))
	}
	return lipgloss.NewStyle().Width(w).Height(h).MaxHeight(h).Render(strings.Join(out, "\n"))
}

package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	zone "github.com/lrstanley/bubblezone"

	"github.com/benjaminwood/eldar-os/supervisor/api"
	"github.com/benjaminwood/eldar-os/supervisor/view"
)

type field int

const (
	fTarget field = iota
	fSession
	fProfile
	fCredMode
	fCredList
	fAwsMode
	fAwsList
	fArcMode
	fArcList
	fDuration
	fNote
	fSubmit
	fieldCount
)

// Scope modes, shared by credentials, AWS roles and Arc profiles.
const (
	modeAny = iota
	modeNone
	modeOnly
)

var modeLabels = []string{"Any", "None", "Only these"}

var (
	threeModes = []int{modeAny, modeNone, modeOnly}
	arcModes   = []int{modeAny, modeOnly} // an Arc grant covering no profile means nothing
)

type duration struct {
	label string
	min   *int
}

var durations = []duration{
	{"15m", intp(15)}, {"1h", intp(60)}, {"4h", intp(240)}, {"8h", intp(480)}, {"until revoked", nil},
}

type opt struct{ value, label string }

// grantTargets: what a grant can cover, for this editor: Eldar, each remote
// host agents may run commands on, and browser windows.
func grantTargets(m *model) []opt {
	out := []opt{{"eldar", "Eldar (commands in its VM)"}}
	for _, h := range m.hosts() {
		out = append(out, opt{h.Name, h.Label + " (commands over SSH)"})
	}
	return append(out, opt{"browser", "Browser windows"})
}

// The target kinds the editor's fields depend on.
func (e *editor) isEldar() bool   { return e.targets[e.target].value == "eldar" }
func (e *editor) isBrowser() bool { return e.targets[e.target].value == "browser" }
func (e *editor) isHost() bool    { return !e.isEldar() && !e.isBrowser() }
func (e *editor) setTarget(v string) {
	for i, o := range e.targets {
		if o.value == v {
			e.target = i
		}
	}
}

const listRows = 7

// editor is the fine-grained bypass grant form.
type editor struct {
	approveID int  // approve this pending request once the grant exists
	request   bool // approveID is a grant request: approve it with this form's scope
	targets   []opt
	target    int // index into targets
	durs      []duration

	sessOpts []opt
	sessIdx  int
	profOpts []string
	profIdx  int

	credMode int
	creds    *multiSelect
	credsFor string
	credsErr string
	loading  bool

	awsMode int
	aws     *multiSelect

	arcMode  int
	arc      *multiSelect
	arcNames map[string]string

	durIdx int
	note   textinput.Model
	focus  field
	err    string
}

func newEditor(m *model, session string) *editor {
	e := &editor{
		targets: grantTargets(m),
		durIdx:  1, awsMode: modeNone, durs: durations,
		creds: newMultiSelect("credentials", listRows), aws: newMultiSelect("AWS roles", listRows),
		arc: newMultiSelect("browser profiles", listRows), arcNames: map[string]string{},
	}
	e.sessOpts = []opt{{"*", "All sessions"}}
	found := session == "*"
	for _, s := range m.sessions {
		label := s.Title
		if s.Pane != "" {
			label += "  " + s.Pane
		}
		e.sessOpts = append(e.sessOpts, opt{s.ID, label})
		if s.ID == session {
			e.sessIdx = len(e.sessOpts) - 1
			found = true
		}
	}
	if !found && session != "" {
		e.sessOpts = append(e.sessOpts, opt{session, "session " + view.Short(session)})
		e.sessIdx = len(e.sessOpts) - 1
	}
	e.profOpts = append([]string{"*"}, m.st.Profiles...)

	var aws []msItem
	for _, p := range m.st.AwsProfiles {
		aws = append(aws, awsItem(p))
	}
	e.aws.setItems(aws)
	var arc []msItem
	for _, a := range m.st.ArcProfiles {
		e.arcNames[a.ID] = a.Name
		label := a.Name
		if !a.Ready {
			label += " (not set up)"
		}
		arc = append(arc, msItem{value: a.ID, label: label})
	}
	e.arc.setItems(arc)

	e.note = textinput.New()
	e.note.Placeholder = "optional — why this grant exists"
	e.note.CharLimit = 200
	e.note.Prompt = ""
	e.note.TextStyle = sBright
	e.note.PlaceholderStyle = sFaint
	e.note.Cursor.Style = sAccent
	if session != "*" {
		e.focus = fProfile
	}
	return e
}

// awsItem lists an AWS profile "<account>.<role>" as a role under its account.
func awsItem(p string) msItem {
	it := msItem{value: p, label: p, warn: view.AwsRisky(p)}
	if i := strings.LastIndexByte(p, '.'); i > 0 {
		it.group, it.label = p[:i], p[i+1:]
	}
	return it
}

// editorFromPending prefills the form with exactly what the request asked for.
func editorFromPending(m *model, p api.Pending) *editor {
	sess := p.SessionID
	if sess == "" {
		sess = "*"
	}
	if p.Kind == "grant_request" && p.Grant != nil {
		return editorFromGrantRequest(m, p, *p.Grant)
	}
	e := newEditor(m, sess)
	if p.Kind == "exec" || p.Kind == "browser" {
		e.approveID = p.ID
	}
	e.focus = fDuration
	if p.Kind == "browser" {
		e.setTarget("browser")
		if p.ArcProfile != "" {
			e.arcMode = modeOnly
			e.arc.checked[p.ArcProfile] = true
			if _, ok := e.arcNames[p.ArcProfile]; !ok {
				e.arcNames[p.ArcProfile] = p.ArcProfileName
				e.arc.setItems(append(e.arc.items, msItem{value: p.ArcProfile, label: p.ArcProfileName}))
			}
		}
		return e
	}
	if p.Target != "" {
		e.setTarget(p.Target)
	}
	for i, pr := range e.profOpts {
		if pr == p.Profile {
			e.profIdx = i
		}
	}
	if p.Credentials != nil && e.profile() != "*" {
		if len(*p.Credentials) == 0 {
			e.credMode = modeNone
		} else {
			e.credMode = modeOnly
			for _, n := range *p.Credentials {
				e.creds.checked[n] = true
			}
		}
	}
	if p.AwsProfile != "" {
		e.awsMode = modeOnly
		e.aws.checked[p.AwsProfile] = true
		e.aws.setItems(e.aws.items) // adds it if ~/.aws/config no longer lists it
	}
	return e
}

// editorFromGrantRequest opens a session's proposed grant for Ben to narrow
// before approving it.
func editorFromGrantRequest(m *model, p api.Pending, g api.GrantInput) *editor {
	e := newEditor(m, g.Session)
	e.approveID, e.request, e.focus = p.ID, true, fDuration
	e.setTarget(g.Target)
	for i, pr := range e.profOpts {
		if pr == g.Profile {
			e.profIdx = i
		}
	}
	pick := func(c api.Creds, l *multiSelect) int {
		if c.Any {
			return modeAny
		}
		if len(c.Names) == 0 {
			return modeNone
		}
		for _, n := range c.Names {
			l.checked[n] = true
		}
		l.setItems(l.items)
		return modeOnly
	}
	e.credMode = pick(g.Credentials, e.creds)
	e.awsMode = pick(g.Aws, e.aws)
	if e.arcMode = pick(g.Arc, e.arc); e.arcMode == modeNone {
		e.arcMode = modeAny
	}
	for i, id := range g.Arc.Names {
		if i < len(g.ArcNames) && e.arcNames[id] == "" {
			e.arcNames[id] = g.ArcNames[i]
		}
	}
	if g.Minutes != nil {
		e.durIdx = -1
		for i, d := range e.durs {
			if d.min != nil && *d.min == *g.Minutes {
				e.durIdx = i
			}
		}
		if e.durIdx < 0 {
			e.durs = append([]duration{{fmt.Sprintf("%dm (asked)", *g.Minutes), intp(*g.Minutes)}}, durations...)
			e.durIdx = 0
		}
	}
	return e
}

func (e *editor) profile() string { return e.profOpts[e.profIdx] }

func (e *editor) loadCreds(m *model) tea.Cmd {
	prof := e.profile()
	if e.isBrowser() || prof == "*" || e.credsFor == prof {
		return nil
	}
	e.loading, e.credsErr = true, ""
	e.creds.setItems(nil)
	e.credsFor = prof
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		names, err := client.Credentials(ctx, prof)
		return credsMsg{profile: prof, names: names, err: err}
	}
}

func (e *editor) setCreds(msg credsMsg) {
	if msg.profile != e.credsFor {
		return
	}
	e.loading = false
	if msg.err != nil {
		e.credsErr = msg.err.Error()
		return
	}
	items := make([]msItem, len(msg.names))
	for i, n := range msg.names {
		items[i] = msItem{value: n, label: n}
	}
	e.creds.setItems(items)
}

func (e *editor) fieldEnabled(f field) bool {
	switch f {
	case fProfile, fCredMode:
		return !e.isBrowser()
	case fCredList:
		return !e.isBrowser() && e.credMode == modeOnly
	case fAwsMode:
		return e.isEldar()
	case fAwsList:
		return e.isEldar() && e.awsMode == modeOnly
	case fArcMode:
		return e.isBrowser()
	case fArcList:
		return e.isBrowser() && e.arcMode == modeOnly
	}
	return true
}

func (e *editor) step(d int) {
	f := e.focus
	for i := 0; i < int(fieldCount); i++ {
		f = (f + field(d) + fieldCount) % fieldCount
		if e.fieldEnabled(f) {
			break
		}
	}
	e.setFocus(f)
}

func (e *editor) setFocus(f field) {
	e.focus = f
	if f == fNote {
		e.note.Focus()
	} else {
		e.note.Blur()
	}
}

// nextMode steps through the allowed modes.
func nextMode(modes []int, cur, d int) int {
	for i, m := range modes {
		if m == cur {
			return modes[(i+d+len(modes))%len(modes)]
		}
	}
	return modes[0]
}

// cycle changes the focused field's value.
func (e *editor) cycle(m *model, d int) tea.Cmd {
	switch e.focus {
	case fTarget:
		e.target = (e.target + d + len(e.targets)) % len(e.targets)
		return e.loadCreds(m)
	case fSession:
		e.sessIdx = (e.sessIdx + d + len(e.sessOpts)) % len(e.sessOpts)
	case fProfile:
		e.profIdx = (e.profIdx + d + len(e.profOpts)) % len(e.profOpts)
		if e.profile() == "*" {
			if e.credMode == modeOnly {
				e.credMode = modeAny
			}
		} else {
			// A new profile has different credentials; drop picks from the old one.
			e.creds.checked = map[string]bool{}
		}
		return e.loadCreds(m)
	case fCredMode:
		e.setMode(fCredMode, nextMode(threeModes, e.credMode, d))
		return e.loadCreds(m)
	case fAwsMode:
		e.setMode(fAwsMode, nextMode(threeModes, e.awsMode, d))
	case fArcMode:
		e.setMode(fArcMode, nextMode(arcModes, e.arcMode, d))
	case fDuration:
		e.durIdx = (e.durIdx + d + len(e.durs)) % len(e.durs)
	}
	return nil
}

func (e *editor) setMode(f field, mode int) {
	switch f {
	case fCredMode:
		if mode == modeOnly && e.profile() == "*" {
			e.err = "Pick a profile first — “Only these” lists that profile's credentials"
			return
		}
		e.credMode = mode
	case fAwsMode:
		e.awsMode = mode
	case fArcMode:
		if mode == modeNone {
			return
		}
		e.arcMode = mode
	}
	e.err = ""
}

func (e *editor) list(f field) *multiSelect {
	switch f {
	case fCredList:
		return e.creds
	case fAwsList:
		return e.aws
	case fArcList:
		return e.arc
	}
	return nil
}

func scope(mode int, sel []string) api.Creds {
	switch mode {
	case modeAny:
		return api.AnyCreds
	case modeOnly:
		return api.Creds{Names: sel}
	}
	return api.Creds{Names: []string{}}
}

func (e *editor) input() (api.GrantInput, error) {
	in := api.GrantInput{
		Target:      e.targets[e.target].value,
		Session:     e.sessOpts[e.sessIdx].value,
		Profile:     e.profile(),
		Minutes:     e.durs[e.durIdx].min,
		Note:        strings.TrimSpace(e.note.Value()),
		Credentials: api.Creds{Names: []string{}},
		Aws:         api.Creds{Names: []string{}},
		Arc:         api.Creds{Names: []string{}},
	}
	if e.isBrowser() {
		in.Profile = "*"
		in.Arc = scope(e.arcMode, e.arc.selected())
		if e.arcMode == modeOnly {
			if len(in.Arc.Names) == 0 {
				return in, fmt.Errorf("pick at least one Arc profile, or choose Any")
			}
			for _, id := range in.Arc.Names {
				name := e.arcNames[id]
				if name == "" {
					name = id
				}
				in.ArcNames = append(in.ArcNames, name)
			}
		}
		return in, nil
	}
	in.Credentials = scope(e.credMode, e.creds.selected())
	if e.credMode == modeOnly && len(in.Credentials.Names) == 0 {
		return in, fmt.Errorf("pick at least one credential, or choose None")
	}
	if e.isEldar() {
		in.Aws = scope(e.awsMode, e.aws.selected())
		if e.awsMode == modeOnly && len(in.Aws.Names) == 0 {
			return in, fmt.Errorf("pick at least one AWS role, or choose None")
		}
	}
	return in, nil
}

func (e *editor) submit(m *model) tea.Cmd {
	in, err := e.input()
	if err != nil {
		e.err = err.Error()
		return nil
	}
	m.editor = nil
	if e.request {
		return m.approveGrantRequest(e.approveID, in)
	}
	return m.addGrant(in, e.approveID)
}

func (e *editor) update(m *model, msg tea.Msg) tea.Cmd {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		if e.focus == fNote {
			var cmd tea.Cmd
			e.note, cmd = e.note.Update(msg)
			return cmd
		}
		return nil
	}
	k := km.String()
	switch k {
	case "esc":
		m.editor = nil
		return nil
	case "ctrl+s":
		return e.submit(m)
	case "tab":
		e.step(1)
		return nil
	case "shift+tab":
		e.step(-1)
		return nil
	}

	if l := e.list(e.focus); l != nil {
		if k == "enter" {
			return e.submit(m)
		}
		if leave := l.key(km); leave != 0 {
			e.step(leave)
		}
		return nil
	}

	switch e.focus {
	case fNote:
		switch k {
		case "enter":
			return e.submit(m)
		case "up":
			e.step(-1)
			return nil
		case "down":
			e.step(1)
			return nil
		}
		var cmd tea.Cmd
		e.note, cmd = e.note.Update(msg)
		return cmd

	case fSubmit:
		switch k {
		case "enter", " ":
			return e.submit(m)
		case "up", "k":
			e.step(-1)
		}
		return nil
	}

	switch k {
	case "left", "h":
		return e.cycle(m, -1)
	case "right", "l", " ":
		return e.cycle(m, 1)
	case "up", "k":
		e.step(-1)
	case "down", "j":
		e.step(1)
	case "enter":
		return e.submit(m)
	case "a", "n", "o":
		if f := e.focus; f == fCredMode || f == fAwsMode || f == fArcMode {
			e.setMode(f, map[string]int{"a": modeAny, "n": modeNone, "o": modeOnly}[k])
			return e.loadCreds(m)
		}
	}
	return nil
}

func (e *editor) mouse(m *model, msg tea.MouseMsg, click bool, dir int) tea.Cmd {
	lists := []field{fCredList, fAwsList, fArcList}
	if !click {
		for _, f := range lists {
			if e.fieldEnabled(f) && e.list(f).mouse(msg, false, dir) {
				return nil
			}
		}
		return nil
	}
	if zone.Get("ed:cancel").InBounds(msg) {
		m.editor = nil
		return nil
	}
	if zone.Get("ed:submit").InBounds(msg) {
		return e.submit(m)
	}
	arrows := []struct {
		f    field
		name string
	}{{fTarget, "tgt"}, {fSession, "sess"}, {fProfile, "prof"}}
	for _, a := range arrows {
		if zone.Get("ed:" + a.name + ":prev").InBounds(msg) {
			e.setFocus(a.f)
			return e.cycle(m, -1)
		}
		if zone.Get("ed:" + a.name + ":next").InBounds(msg) {
			e.setFocus(a.f)
			return e.cycle(m, 1)
		}
	}
	for _, f := range []field{fCredMode, fAwsMode, fArcMode} {
		for i := range modeLabels {
			if zone.Get("ed:m:" + strconv.Itoa(int(f)) + ":" + strconv.Itoa(i)).InBounds(msg) {
				e.setFocus(f)
				e.setMode(f, i)
				return e.loadCreds(m)
			}
		}
	}
	for _, f := range lists {
		if e.fieldEnabled(f) && e.list(f).mouse(msg, true, 0) {
			e.setFocus(f)
			return nil
		}
	}
	for i := range e.durs {
		if zone.Get("ed:d:" + strconv.Itoa(i)).InBounds(msg) {
			e.setFocus(fDuration)
			e.durIdx = i
			return nil
		}
	}
	for f := fSession; f < fieldCount; f++ {
		if zone.Get("ed:f:" + strconv.Itoa(int(f))).InBounds(msg) {
			e.setFocus(f)
			return nil
		}
	}
	if !zone.Get("editor").InBounds(msg) {
		m.editor = nil
	}
	return nil
}

// pickedSummary names up to three picks, else counts them.
func pickedSummary(l *multiSelect, label func(string) string, noun string) string {
	sel := l.selected()
	if len(sel) == 0 {
		return "(pick " + noun + ")"
	}
	if len(sel) > 3 {
		return fmt.Sprintf("%d %s", len(sel), noun)
	}
	names := make([]string, len(sel))
	for i, v := range sel {
		names[i] = label(v)
	}
	return strings.Join(names, ", ")
}

// summary says in plain words what the grant will do.
func (e *editor) summary(m *model) string {
	in, _ := e.input()
	who := "every session"
	if in.Session != "*" {
		who = trunc(view.SessionTitle(m.sessions, in.Session), 40)
	}
	dur := "for " + e.durs[e.durIdx].label
	if e.durs[e.durIdx].min == nil {
		dur = "until you revoke it"
	}
	b := sBright.Render
	var s string
	if e.isBrowser() {
		prof := "any browser profile"
		if e.arcMode == modeOnly {
			prof = pickedSummary(e.arc, func(v string) string { return e.arcNames[v] }, "browser profiles")
		}
		s = fmt.Sprintf("Open browser windows for %s in %s without asking, %s. Each window lasts no longer than the grant.", b(who), b(prof), b(dur))
	} else {
		prof := "any profile"
		if in.Profile != "*" {
			prof = "the " + in.Profile + " profile"
		}
		creds := map[int]string{modeAny: "any credentials", modeNone: "no credentials"}[e.credMode]
		if e.credMode == modeOnly {
			creds = pickedSummary(e.creds, func(v string) string { return v }, "credentials")
		}
		where, aws := "", ""
		if e.isHost() {
			where = " on " + m.hostLabel(e.targets[e.target].value)
		} else {
			aws = map[int]string{modeAny: "any AWS role", modeNone: "no AWS"}[e.awsMode]
			if e.awsMode == modeOnly {
				aws = "AWS " + pickedSummary(e.aws, view.AwsLabel, "AWS roles")
			}
			aws = ", " + aws
		}
		s = fmt.Sprintf("Auto-approve commands"+where+" from %s using %s with %s%s, %s.", b(who), b(prof), b(creds), b(aws), b(dur))
	}
	if e.request {
		s += " " + sMint.Render(fmt.Sprintf("Approves request #%d with this scope.", e.approveID))
	} else if e.approveID != 0 {
		s += " " + sMint.Render(fmt.Sprintf("Then approve #%d.", e.approveID))
	}
	return s
}

func (e *editor) render(m *model, maxW, maxH int) string {
	w := min(maxW-4, 84)
	inner := w - 6
	labelW := 10
	pad := strings.Repeat(" ", labelW+2)

	label := func(f field, text string) string {
		mark := "  "
		st := sLav
		if e.focus == f {
			mark = sAccent.Render("▸ ")
			st = sAccent.Bold(true)
		}
		return zone.Mark("ed:f:"+strconv.Itoa(int(f)), mark+st.Width(labelW).Render(text))
	}
	cycler := func(f field, name, value string) string {
		left := zone.Mark("ed:"+name+":prev", sDim.Render("‹"))
		right := zone.Mark("ed:"+name+":next", sDim.Render("›"))
		st := sBright
		if e.focus == f {
			st = sBright.Bold(true)
		}
		return left + " " + st.Render(trunc(value, inner-labelW-8)) + " " + right
	}
	radios := func(f field, modes []int, cur int, dim func(int) bool) string {
		var out []string
		for _, i := range modes {
			dot := sFaint.Render("○")
			st := sDim
			if i == cur {
				dot = sAccent.Render("●")
				st = sBright
			}
			if dim != nil && dim(i) {
				st = sFaint
			}
			out = append(out, zone.Mark("ed:m:"+strconv.Itoa(int(f))+":"+strconv.Itoa(i), dot+" "+st.Render(modeLabels[i])))
		}
		return strings.Join(out, "   ")
	}
	var rows []string
	addList := func(f field, l *multiSelect, loading bool, err string) {
		for _, line := range l.render(inner-labelW-2, e.focus == f, loading, err) {
			rows = append(rows, pad+line)
		}
	}

	title := "New bypass grant"
	if e.request {
		title = fmt.Sprintf("Review grant request #%d", e.approveID)
	} else if e.approveID != 0 {
		title = fmt.Sprintf("Grant + approve #%d", e.approveID)
	}
	rows = append(rows, gradient("⚡ "+title, t.Orange, t.Accent, true), "")
	rows = append(rows, label(fTarget, "Target")+cycler(fTarget, "tgt", e.targets[e.target].label))
	rows = append(rows, label(fSession, "Session")+cycler(fSession, "sess", e.sessOpts[e.sessIdx].label))

	if e.isBrowser() {
		rows = append(rows, label(fArcMode, "Arc")+radios(fArcMode, arcModes, e.arcMode, nil))
		if e.arcMode == modeOnly {
			err := ""
			if len(e.arc.items) == 0 {
				err = "no browser profiles known yet — the supervisor reads them when a session asks"
			}
			addList(fArcList, e.arc, false, err)
		}
	} else {
		profLabel := e.profile()
		if profLabel == "*" {
			profLabel = "Any profile"
		}
		rows = append(rows, label(fProfile, "Profile")+cycler(fProfile, "prof", profLabel))
		rows = append(rows, label(fCredMode, "Creds")+radios(fCredMode, threeModes, e.credMode, func(i int) bool { return i == modeOnly && e.profile() == "*" }))
		if e.credMode == modeOnly {
			addList(fCredList, e.creds, e.loading, e.credsErr)
		}
		if e.isEldar() {
			r := radios(fAwsMode, threeModes, e.awsMode, nil)
			if e.awsMode == modeAny {
				r += "  " + sOrange.Render("⚠ every role, admin and production included")
			}
			rows = append(rows, label(fAwsMode, "AWS")+r)
			if e.awsMode == modeOnly {
				addList(fAwsList, e.aws, false, "")
			}
		}
	}

	var chips []string
	for i, d := range e.durs {
		st := lipgloss.NewStyle().Foreground(c(t.Dim)).Padding(0, 1)
		if i == e.durIdx {
			st = lipgloss.NewStyle().Foreground(c(t.Bg)).Background(c(t.Orange)).Bold(true).Padding(0, 1)
		}
		chips = append(chips, zone.Mark("ed:d:"+strconv.Itoa(i), st.Render(d.label)))
	}
	rows = append(rows, label(fDuration, "For")+strings.Join(chips, " "))

	e.note.Width = inner - labelW - 4
	rows = append(rows, label(fNote, "Note")+e.note.View())

	rows = append(rows, "", lipgloss.NewStyle().Width(inner).Foreground(c(t.Fg)).Render(e.summary(m)))
	if in, err := e.input(); err == nil && in.Session == "*" && view.FullGrant(api.Grant{Target: in.Target, Profile: in.Profile, Credentials: in.Credentials, Aws: in.Aws, Arc: in.Arc}) {
		what := "everything here — same as global bypass"
		switch {
		case e.isHost():
			what = "every command on " + m.hostLabel(e.targets[e.target].value)
		case e.isBrowser():
			what = "every browser profile, for every session"
		}
		rows = append(rows, sOrange.Render("This covers "+what+"."))
	} else if e.isEldar() && e.awsMode == modeOnly {
		for _, a := range e.aws.selected() {
			if view.AwsRisky(a) {
				rows = append(rows, sOrange.Render("⚠ Includes admin or production AWS roles."))
				break
			}
		}
	}
	if e.err != "" {
		rows = append(rows, sRed.Render("✗ "+e.err))
	}
	submitLabel := "Create grant"
	if e.request {
		submitLabel = "Approve with this scope"
	} else if e.approveID != 0 {
		submitLabel = "Grant + approve"
	}
	rows = append(rows, "", lipgloss.JoinHorizontal(lipgloss.Top,
		zone.Mark("ed:cancel", button("Cancel", "esc", t.Dim, false)), " ",
		zone.Mark("ed:submit", button(submitLabel, "ctrl+s", t.Mint, e.focus == fSubmit)),
	))
	hint := "tab next · ←→ change · a/n/o any/none/only · enter create · esc cancel"
	if e.list(e.focus) != nil {
		hint = "type to filter · ↑↓ move · space toggle (a heading toggles its group) · ctrl+a all shown · enter create"
	}
	rows = append(rows, sFaint.Render(hint))

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).BorderForeground(c(t.Orange)).
		Padding(1, 2).Width(w).
		Render(strings.Join(rows, "\n"))
	return zone.Mark("editor", box)
}

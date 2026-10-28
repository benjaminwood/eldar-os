package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/benjaminwood/eldar-os/supervisor/api"
)

// Devices are physical controls on Ben's own machines, such as a Stream Deck
// on his Mac. A device reaches the supervisor through the web UI's tailnet node
// (which checks the caller is one of Ben's untagged machines) and presents its
// own token; it never sees the admin API. The supervisor decides what each
// device shows and what a press does (DeviceView, DeviceAction), so a device
// is a thin client and new controls are added here.
//
// A device can answer approvals, which decision 0013 otherwise reserves for
// the admin socket and the passkey-locked web UI. The risk is an agent that
// can run commands on the device's machine: it could read the token and
// approve its own requests. So the token only works from the tailnet machine
// it was paired on, and Approve turns off while an agent has a way onto that
// machine (a grant or allow-list entry for its host, or a browser window
// there). Deny is always available: an agent denying itself gains nothing.
//
// Pairing is itself an approval, which a device can never answer.
type Devices struct {
	path string
	mu   sync.Mutex
	list []deviceRecord
	next int
	n    *Notifier
}

type deviceRecord struct {
	api.Device
	TokenHash string `json:"tokenHash"`
}

type devicesFile struct {
	Devices []deviceRecord `json:"devices"`
}

// pairKind is the pending kind for a pairing request.
const pairKind = "device_pair"

// deviceActions is what a device may send.
var deviceActions = []string{"approve", "deny", "herdr-focus"}

func NewDevices(path string, n *Notifier) *Devices {
	d := &Devices{path: path, n: n, next: 1}
	if b, err := os.ReadFile(path); err == nil {
		var f devicesFile
		if json.Unmarshal(b, &f) != nil {
			logf("devices: unreadable %s; no devices paired", path)
		}
		d.list = f.Devices
		for _, r := range d.list {
			d.next = max(d.next, r.ID+1)
		}
	}
	return d
}

func tokenHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func (d *Devices) saveLocked() {
	if d.path == "" {
		return
	}
	if err := writeJSONAtomic(d.path, devicesFile{Devices: d.list}); err != nil {
		logf("devices: save failed: %v", err)
	}
}

// Add pairs a device and returns its token, which is stored only as a hash.
func (d *Devices) Add(name, node, host string) (api.Device, string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token := base64.RawURLEncoding.EncodeToString(b)
	d.mu.Lock()
	dev := api.Device{ID: d.next, Name: name, Node: node, Host: host, CreatedAt: time.Now().UnixMilli()}
	d.next++
	d.list = append(d.list, deviceRecord{Device: dev, TokenHash: tokenHash(token)})
	d.saveLocked()
	d.mu.Unlock()
	d.n.Changed()
	return dev, token
}

func (d *Devices) Remove(id int) bool {
	d.mu.Lock()
	n := len(d.list)
	d.list = slices.DeleteFunc(d.list, func(r deviceRecord) bool { return r.ID == id })
	removed := len(d.list) != n
	if removed {
		d.saveLocked()
	}
	d.mu.Unlock()
	if removed {
		d.n.Changed()
	}
	return removed
}

func (d *Devices) List() []api.Device {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := []api.Device{}
	for _, r := range d.list {
		out = append(out, r.Device)
	}
	return out
}

// lastSeenEvery limits how often a device's LastSeen is written to disk.
const lastSeenEvery = time.Minute

// Authenticate finds the device a token belongs to; it must come from the
// tailnet machine the device was paired on.
func (d *Devices) Authenticate(token, node string) (api.Device, bool) {
	if token == "" {
		return api.Device{}, false
	}
	want := tokenHash(token)
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.list {
		r := &d.list[i]
		if subtle.ConstantTimeCompare([]byte(r.TokenHash), []byte(want)) != 1 {
			continue
		}
		if !sameNode(r.Node, node) {
			return api.Device{}, false
		}
		if now := time.Now(); now.Sub(time.UnixMilli(r.LastSeen)) > lastSeenEvery {
			r.LastSeen = now.UnixMilli()
			d.saveLocked()
		}
		return r.Device, true
	}
	return api.Device{}, false
}

// sameNode compares tailnet machine names, ignoring case and the tailnet suffix.
func sameNode(a, b string) bool {
	a, _, _ = strings.Cut(strings.TrimSuffix(a, "."), ".")
	b, _, _ = strings.Cut(strings.TrimSuffix(b, "."), ".")
	return a != "" && strings.EqualFold(a, b)
}

// hostOnNode is the supervisor host that runs on a tailnet machine: the one
// whose ssh address is one of the machine's addresses or its name.
func (s *Supervisor) hostOnNode(node string, ips []string) string {
	for _, name := range s.hostNames() {
		h := s.host(name)
		if h == nil {
			continue
		}
		addr := h.Addr()
		if slices.Contains(ips, addr) || sameNode(addr, node) && net.ParseIP(addr) == nil {
			return name
		}
	}
	return ""
}

// agentReach says why an agent may currently be able to act on a device's
// machine, or "" if none can.
func (s *Supervisor) agentReach(dev api.Device, browserDriven bool) string {
	if browserDriven {
		return "an agent is driving the browser on this machine"
	}
	for _, g := range s.browserGrants() {
		if sameNode(g.Device, dev.Node) || (dev.Host != "" && g.Host == dev.Host) {
			return "an agent has a browser window on this machine"
		}
	}
	if dev.Host == "" {
		return ""
	}
	for _, g := range s.grants.List() {
		if g.Target == dev.Host {
			return fmt.Sprintf("grant #%d covers commands on %s", g.ID, dev.Host)
		}
	}
	if len(s.hostAllow.List(dev.Host)) > 0 {
		return "the allow list auto-approves commands on " + dev.Host
	}
	return ""
}

// DeviceView is what a device shows now.
func (s *Supervisor) DeviceView(c api.DeviceCaller) (api.DeviceView, bool) {
	dev, ok := s.devices.Authenticate(c.Token, c.Node)
	if !ok {
		return api.DeviceView{}, false
	}
	return s.deviceView(dev, c.BrowserDriven), true
}

func (s *Supervisor) deviceView(dev api.Device, browserDriven bool) api.DeviceView {
	pending := s.approvals.List()
	answerable := slices.DeleteFunc(slices.Clone(pending), func(p api.Pending) bool { return p.Kind == pairKind })
	v := api.DeviceView{Device: dev, Pending: len(pending), Controls: map[string]api.DeviceControl{}}

	approve := api.DeviceControl{Badge: len(answerable), Detail: "Nothing waiting"}
	if len(answerable) > 0 {
		oldest := answerable[0]
		approve.Ref, approve.Detail = oldest.ID, fmt.Sprintf("#%d %s", oldest.ID, oldest.Title)
		if why := s.agentReach(dev, browserDriven); why != "" {
			approve.Detail = "Off: " + why
		} else {
			approve.Enabled = true
		}
	}
	v.Controls["approve"] = approve

	deny := api.DeviceControl{Badge: len(pending), Detail: "Nothing waiting"}
	if len(pending) > 0 {
		deny.Enabled, deny.Ref, deny.Detail = true, pending[0].ID, fmt.Sprintf("#%d %s", pending[0].ID, pending[0].Title)
	}
	v.Controls["deny"] = deny
	return v
}

// DeviceAction runs a press. For approve and deny, ref is the request the
// device showed, so one that arrived since isn't answered by mistake;
// herdr-focus takes args herdr (host | eldar), tab and optionally workspace.
func (s *Supervisor) DeviceAction(ctx context.Context, c api.DeviceCaller, action string, ref int, args map[string]string) (int, string) {
	dev, ok := s.devices.Authenticate(c.Token, c.Node)
	if !ok {
		return 401, "unknown device"
	}
	if !slices.Contains(deviceActions, action) {
		return 400, fmt.Sprintf("unknown action %q", action)
	}
	if action == "herdr-focus" {
		// Only changes what Ben is looking at, so it's always allowed.
		if err := s.focusHerdrTab(ctx, args["herdr"], args["workspace"], args["tab"]); err != nil {
			return 502, err.Error()
		}
		return 200, ""
	}
	p, ok := s.approvals.Get(ref)
	if !ok {
		return 409, "already answered or expired"
	}
	decision := api.Deny
	if action == "approve" {
		if p.Kind == pairKind {
			return 403, "a device can't approve pairing"
		}
		if why := s.agentReach(dev, c.BrowserDriven); why != "" {
			return 403, "approve is off: " + why
		}
		decision = api.Approve
	}
	if !s.approvals.Resolve(ref, decision) {
		return 409, "already answered or expired"
	}
	s.audit.Log(AuditEntry{RequestID: ref, Action: fmt.Sprintf("DEVICE %s #%d: %s", strings.ToUpper(action), ref, truncRunes(p.Title, 120)),
		Decision: string(decision), DecisionReason: fmt.Sprintf("Pressed on %s (%s)", dev.Name, dev.Node), Mode: string(s.modes.Current())})
	return 200, ""
}

// PairDevice asks Ben to pair a device and waits for his answer. On approval
// it returns the device and its token, which the caller hands to the device.
func (s *Supervisor) PairDevice(name string, c api.DeviceCaller) (api.Device, string, int, string) {
	name = strings.TrimSpace(truncRunes(name, 60))
	if name == "" || c.Node == "" {
		return api.Device{}, "", 400, "need a device name and its tailnet machine"
	}
	if s.modes.Current() == api.ModeLockdown {
		return api.Device{}, "", 403, "the supervisor is in lockdown"
	}
	for _, p := range s.approvals.List() {
		if p.Kind == pairKind {
			return api.Device{}, "", 409, "another pairing is already waiting"
		}
	}
	node, _, _ := strings.Cut(c.Node, ".")
	host := s.hostOnNode(c.Node, c.IPs)
	where := node
	if host != "" {
		where += " (supervisor host " + host + ")"
	}
	id := s.nextID()
	reason := fmt.Sprintf("A device on %s asks to pair. Once paired it can deny any request, and approve requests "+
		"(not pairings) while no agent can act on that machine. Approve only if you just asked it to pair.", where)
	result := s.awaitHuman(id, api.Pending{Kind: pairKind, Title: fmt.Sprintf("Pair %q on %s", name, node), Reason: reason, Target: "", Host: host}, nil)
	if result != string(api.Approve) && result != string(api.AllowAlways) {
		s.audit.Log(AuditEntry{RequestID: id, Action: fmt.Sprintf("PAIR DEVICE %q on %s", name, node), Decision: result, DecisionReason: "Not approved", Mode: string(s.modes.Current())})
		return api.Device{}, "", 403, "pairing was not approved (" + result + ")"
	}
	dev, token := s.devices.Add(name, node, host)
	s.audit.Log(AuditEntry{RequestID: id, Action: fmt.Sprintf("PAIR DEVICE #%d %q on %s", dev.ID, name, where), Decision: "approved", DecisionReason: "Approved by Ben", Mode: string(s.modes.Current())})
	return dev, token, 200, ""
}

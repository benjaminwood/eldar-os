package server

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/benjaminwood/eldar-os/supervisor/api"
)

// pairTest pairs a device on the Mac's tailnet machine and returns its caller.
func pairTest(t *testing.T) (*Supervisor, api.DeviceCaller) {
	t.Helper()
	s, _ := newTest(t)
	s.cfg.ApprovalTimeout = 2 * time.Second
	s.devices = NewDevices(filepath.Join(t.TempDir(), "devices.json"), s.n)
	s.cfg.Hosts = map[string]*RemoteHost{"mac": {Name: "mac", Label: "Ben's Mac", Target: "ben@100.64.0.5"}}
	c := api.DeviceCaller{Node: "bens-mbp.example.ts.net.", IPs: []string{"100.64.0.5", "fd7a::5"}}
	answer(s, api.Approve)
	dev, token, status, msg := s.PairDevice("Stream Deck", c)
	if status != 200 || token == "" {
		t.Fatalf("pair: %d %s", status, msg)
	}
	if dev.Node != "bens-mbp" || dev.Host != "mac" {
		t.Fatalf("paired %+v", dev)
	}
	c.Token = token
	return s, c
}

// waiting adds a pending request and returns its ID.
func waiting(s *Supervisor, kind, title string) int {
	id := s.nextID()
	s.approvals.Add(api.Pending{ID: id, Kind: kind, Title: title, CreatedAt: time.Now().UnixMilli() + int64(id)})
	return id
}

func TestDevicePairing(t *testing.T) {
	s, c := pairTest(t)
	if _, ok := s.DeviceView(c); !ok {
		t.Fatal("paired device not recognised")
	}
	// The token is only good from the machine it was paired on.
	other := c
	other.Node = "someone-else.example.ts.net."
	if _, ok := s.DeviceView(other); ok {
		t.Error("token accepted from another machine")
	}
	bad := c
	bad.Token = "nope"
	if _, ok := s.DeviceView(bad); ok {
		t.Error("wrong token accepted")
	}
	// Pairings survive a restart; only the token's hash is stored.
	reloaded := NewDevices(s.devices.path, s.n)
	if _, ok := reloaded.Authenticate(c.Token, c.Node); !ok {
		t.Error("pairing lost on reload")
	}
	// Denied pairing: no device.
	answer(s, api.Deny)
	if _, _, status, _ := s.PairDevice("Other", c); status != 403 {
		t.Errorf("denied pairing: %d", status)
	}
	if n := len(s.devices.List()); n != 1 {
		t.Errorf("%d devices after a denied pairing", n)
	}
}

func TestDeviceApproveAndDeny(t *testing.T) {
	s, c := pairTest(t)
	v, _ := s.DeviceView(c)
	if v.Controls["approve"].Enabled || v.Controls["deny"].Enabled {
		t.Fatalf("controls on with nothing waiting: %+v", v.Controls)
	}
	first := waiting(s, "exec", "first")
	second := waiting(s, "exec", "second")
	v, _ = s.DeviceView(c)
	ap := v.Controls["approve"]
	if !ap.Enabled || ap.Badge != 2 || ap.Ref != first {
		t.Fatalf("approve control: %+v", ap)
	}
	entry := s.approvals.entries[first]
	if status, msg := s.DeviceAction(c, "approve", first); status != 200 {
		t.Fatalf("approve: %d %s", status, msg)
	}
	if d := <-entry.ch; d != api.Approve {
		t.Errorf("decision %s", d)
	}
	if status, _ := s.DeviceAction(c, "approve", first); status != 409 {
		t.Errorf("second answer to the same request: %d", status)
	}
	if status, _ := s.DeviceAction(c, "deny", second); status != 200 {
		t.Errorf("deny: %d", status)
	}
	if status, _ := s.DeviceAction(c, "launch", second); status != 400 {
		t.Errorf("unknown action: %d", status)
	}
}

func TestDeviceCantApprovePairing(t *testing.T) {
	s, c := pairTest(t)
	id := waiting(s, pairKind, "Pair another")
	v, _ := s.DeviceView(c)
	if ap := v.Controls["approve"]; ap.Enabled || ap.Badge != 0 {
		t.Errorf("pairing offered to approve: %+v", ap)
	}
	if status, _ := s.DeviceAction(c, "approve", id); status != 403 {
		t.Errorf("device approved a pairing: %d", status)
	}
	if status, _ := s.DeviceAction(c, "deny", id); status != 200 {
		t.Errorf("device couldn't deny a pairing: %d", status)
	}
}

// Approve turns off while an agent could act on the device's machine.
func TestDeviceApproveOffWhileAgentsReachMachine(t *testing.T) {
	cases := map[string]func(s *Supervisor, c *api.DeviceCaller){
		"grant for its host": func(s *Supervisor, _ *api.DeviceCaller) {
			s.grants.Add(api.GrantInput{Target: "mac", Session: "*", Profile: "*", Credentials: api.AnyCreds})
		},
		"allow list for its host": func(s *Supervisor, _ *api.DeviceCaller) { s.hostAllow.Add("mac", "ls *") },
		"browser on it":           func(_ *Supervisor, c *api.DeviceCaller) { c.BrowserDriven = true },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			s, c := pairTest(t)
			s.hostAllow = NewHostAllow(filepath.Join(t.TempDir(), "allow.json"))
			id := waiting(s, "exec", "x")
			setup(s, &c)
			v, _ := s.DeviceView(c)
			if ap := v.Controls["approve"]; ap.Enabled || ap.Badge != 1 {
				t.Errorf("approve: %+v", ap)
			}
			if status, _ := s.DeviceAction(c, "approve", id); status != 403 {
				t.Errorf("approved anyway: %d", status)
			}
			if status, _ := s.DeviceAction(c, "deny", id); status != 200 {
				t.Errorf("deny: %d", status)
			}
		})
	}
	// A grant for another host doesn't matter.
	s, c := pairTest(t)
	s.grants.Add(api.GrantInput{Target: "eldar", Session: "*", Profile: "*", Credentials: api.AnyCreds})
	waiting(s, "exec", "x")
	if v, _ := s.DeviceView(c); !v.Controls["approve"].Enabled {
		t.Errorf("approve off for an Eldar grant: %+v", v.Controls["approve"])
	}
}

func TestDeviceUnpair(t *testing.T) {
	s, c := pairTest(t)
	dev := s.devices.List()[0]
	if !s.devices.Remove(dev.ID) {
		t.Fatal("remove")
	}
	if _, ok := s.DeviceView(c); ok {
		t.Error("unpaired device still works")
	}
}

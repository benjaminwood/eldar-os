package server

import (
	"path/filepath"
	"testing"
)

func TestRelayAddSanitisesAndPersists(t *testing.T) {
	dir := t.TempDir()
	r := NewRelay(filepath.Join(dir, "relay.jsonl"), filepath.Join(dir, "relay-state.json"))
	e, err := r.Add("  deploy failed  ", map[string]string{"origin": `cron" evil="x<y>`, "pane_id": "", "cwd": "a\nb"})
	if err != nil {
		t.Fatal(err)
	}
	if e.Text != "deploy failed" || e.Meta["origin"] != "cron_ evil=_x_y_" || e.Meta["cwd"] != "a_b" {
		t.Fatalf("event %+v", e)
	}
	if _, ok := e.Meta["pane_id"]; ok {
		t.Fatal("empty meta should be dropped")
	}
	if _, err := r.Add("   ", nil); err == nil {
		t.Fatal("empty message accepted")
	}
	r.markDelivered(e.ID)
	r.Add("second", nil)
	again := NewRelay(filepath.Join(dir, "relay.jsonl"), filepath.Join(dir, "relay-state.json"))
	if len(again.events) != 2 || again.delivered != e.ID || again.nextID != 3 {
		t.Fatalf("reload: %d events, delivered %d, next %d", len(again.events), again.delivered, again.nextID)
	}
}

func TestKeyPinsRepin(t *testing.T) {
	var k KeyPins
	if !k.Check("s", "a") || k.Check("s", "b") {
		t.Fatal("first key pins, a second is refused")
	}
	k.Repin("s", "b") // a new channel process, the old stream gone
	if k.Check("s", "a") || !k.Check("s", "b") {
		t.Fatal("repin replaces the key")
	}
}

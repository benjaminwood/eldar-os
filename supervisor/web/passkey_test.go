package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// After a rename, keys made for the old domain are set aside so the first
// device can enrol again (they could never sign in, and would block it).
func TestPasskeysForAnotherDomainAreSetAside(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "passkeys.json"), []byte(`{"rpId":"old.example","userId":"AAAA","keys":[{}]}`), 0o600)
	p, err := newPasskeys(dir, "new.example")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.data.Keys) != 0 || p.data.RPID != "new.example" {
		t.Fatalf("keys %d, rpId %q", len(p.data.Keys), p.data.RPID)
	}
	if _, err := os.Stat(filepath.Join(dir, "passkeys.old.example.json")); err != nil {
		t.Fatal("old keys not kept:", err)
	}
	var f passkeyFile
	b, _ := os.ReadFile(filepath.Join(dir, "passkeys.json"))
	if json.Unmarshal(b, &f) != nil || f.RPID != "new.example" {
		t.Fatalf("new file: %s", b)
	}
}

// Files from before the domain was recorded belong to the current one.
func TestPasskeysWithoutDomainAreAdopted(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "passkeys.json"), []byte(`{"userId":"AAAA","keys":[{}]}`), 0o600)
	p, err := newPasskeys(dir, "same.example")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.data.Keys) != 1 || p.data.RPID != "same.example" {
		t.Fatalf("keys %d, rpId %q", len(p.data.Keys), p.data.RPID)
	}
}

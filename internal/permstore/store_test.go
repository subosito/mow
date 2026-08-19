package permstore

import (
	"path/filepath"
	"testing"
)

func TestRememberLookupRevoke(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	ws := filepath.Join(t.TempDir(), "proj")

	rule, err := Remember(ws, "bash", `{"command":"ls"}`, Allow)
	if err != nil {
		t.Fatal(err)
	}
	if rule.ID == "" || rule.Tool != "bash" {
		t.Fatalf("rule: %+v", rule)
	}
	d, ok, err := Lookup(ws, "bash", `{"command":"ls"}`)
	if err != nil || !ok || d != Allow {
		t.Fatalf("lookup allow: d=%q ok=%v err=%v", d, ok, err)
	}
	if _, ok, _ := Lookup(ws, "bash", `{"command":"rm -rf /"}`); ok {
		t.Fatal("different args must not match")
	}

	if _, err := Remember(ws, "bash", `{"command":"ls"}`, Deny); err != nil {
		t.Fatal(err)
	}
	d, ok, err = Lookup(ws, "bash", `{"command":"ls"}`)
	if err != nil || !ok || d != Deny {
		t.Fatalf("lookup deny: d=%q ok=%v err=%v", d, ok, err)
	}

	ok, err = Revoke(ws, rule.ID)
	if err != nil || !ok {
		t.Fatalf("revoke: ok=%v err=%v", ok, err)
	}
	if _, ok, _ = Lookup(ws, "bash", `{"command":"ls"}`); ok {
		t.Fatal("revoked rule still matches")
	}
	rules, err := Load(ws)
	if err != nil || len(rules) != 0 {
		t.Fatalf("load after revoke: %+v err=%v", rules, err)
	}
}

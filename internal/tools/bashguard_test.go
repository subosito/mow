package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/subosito/mow/internal/policy"
)

func TestIsDestructiveBash(t *testing.T) {
	bad := []string{
		"git checkout -- internal/foo.go",
		"git restore internal/foo.go",
		"git reset --hard HEAD",
		"rm -rf internal/analytics",
		"git clean -fd",
	}
	for _, c := range bad {
		if !isDestructiveBash(c) {
			t.Errorf("want destructive: %q", c)
		}
	}
	ok := []string{
		"git checkout -b feature",
		"go test ./...",
		"rm /tmp/scratch.txt",
		"git status",
	}
	for _, c := range ok {
		if isDestructiveBash(c) {
			t.Errorf("want allow: %q", c)
		}
	}
}

func TestIsDiscoverBash(t *testing.T) {
	refuse := []string{"ls -la", "find . -name '*.go'", "rg foo", "grep -n foo .", "fd bar"}
	for _, c := range refuse {
		if !isDiscoverBash(c) {
			t.Errorf("want discover refuse: %q", c)
		}
	}
	allow := []string{"go test ./... | rg FAIL", "git status", "just verify"}
	for _, c := range allow {
		if isDiscoverBash(c) {
			t.Errorf("want allow: %q", c)
		}
	}
}

func TestBashRefusesDestructiveAndDiscover(t *testing.T) {
	p := &policy.Policy{Workspace: t.TempDir(), AllowShell: true}
	reg := Registry(p, []string{"bash"})
	out, err := reg[0].Exec(t.Context(), json.RawMessage(`{"command":"git reset --hard"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "discarding uncommitted work") {
		t.Fatalf("want WIP block, got %q", out)
	}
	out, err = reg[0].Exec(t.Context(), json.RawMessage(`{"command":"ls -la"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "use the grep tool") {
		t.Fatalf("want discover block, got %q", out)
	}
}

package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/subosito/mow/internal/llm"
)

func TestIsProductiveBash(t *testing.T) {
	prod := []string{
		"go test ./...",
		"GOWORK=off go build ./cmd/x",
		"just verify",
		"git commit -m 'x'",
		"git add -A && git commit -m y",
	}
	for _, c := range prod {
		if isExploreBash(c) {
			t.Errorf("want productive (not explore): %q", c)
		}
	}
	explore := []string{
		"git status --short",
		"git log --oneline -12",
		"ls -la internal/adminplane",
		"find . -name '*.go'",
		"sed -n '1,80p' internal/adminplane/admin/server.go",
		"cat internal/foo.go",
		"cd /x && git status && ls",
	}
	for _, c := range explore {
		if !isExploreBash(c) {
			t.Errorf("want explore: %q", c)
		}
	}
}

func TestBashReadPaths(t *testing.T) {
	cases := []struct {
		cmd  string
		want []string
	}{
		{`cat internal/adminplane/admin/server.go`, []string{"internal/adminplane/admin/server.go"}},
		{`sed -n '1,80p' internal/adminplane/admin/server.go`, []string{"internal/adminplane/admin/server.go"}},
		{`cd "$(pwd)" && head -n 40 docs-internal/x.md`, []string{"docs-internal/x.md"}},
		{`git status`, nil},
		{`go test ./...`, nil},
		{`cat a.go && cat b.go`, []string{"a.go", "b.go"}},
	}
	for _, c := range cases {
		got := bashReadPaths(c.cmd)
		if len(got) != len(c.want) {
			t.Errorf("cmd=%q got=%v want=%v", c.cmd, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("cmd=%q got[%d]=%q want %q", c.cmd, i, got[i], c.want[i])
			}
		}
	}
}

func TestMaybeDedupeBash(t *testing.T) {
	s := newThrashState()
	args1, _ := json.Marshal(map[string]string{"command": "cat foo.go"})
	if stub, ok := s.maybeDedupeBash(args1); ok {
		t.Fatalf("first cat should run: stub=%q", stub)
	}
	args2, _ := json.Marshal(map[string]string{"command": "sed -n '1,20p' foo.go"})
	stub, ok := s.maybeDedupeBash(args2)
	if !ok || !strings.Contains(stub, "already viewed") {
		t.Fatalf("second view should stub: ok=%v stub=%q", ok, stub)
	}
	args3, _ := json.Marshal(map[string]string{"command": "go test ./..."})
	if _, ok := s.maybeDedupeBash(args3); ok {
		t.Fatal("go test should not path-stub")
	}
}

func TestBatchExploreOnly_productiveBashResets(t *testing.T) {
	s := newThrashState()
	bashCall := func(cmd string) []llm.ToolCall {
		args, _ := json.Marshal(map[string]string{"command": cmd})
		return []llm.ToolCall{{
			ID: "c", Type: "function",
			Function: llm.FunctionCall{Name: "bash", Arguments: string(args)},
		}}
	}
	for i := 0; i < 5; i++ {
		if s.noteTurn(bashCall(`git status`)) {
			t.Fatalf("unexpected warn at explore %d", i+1)
		}
	}
	if s.noteTurn(bashCall(`go test ./...`)) {
		t.Fatal("productive should not warn")
	}
	if s.exploreStreak != 0 {
		t.Fatalf("streak=%d want 0 after productive bash", s.exploreStreak)
	}
	for i := 0; i < 5; i++ {
		if s.noteTurn(bashCall(`ls`)) {
			t.Fatalf("warn too early at %d", i+1)
		}
	}
	if !s.noteTurn(bashCall(`ls`)) {
		t.Fatal("want warn on 6th explore")
	}
}

func TestReadAndBashSharePathDedupe(t *testing.T) {
	s := newThrashState()
	readArgs, _ := json.Marshal(map[string]string{"path": "pkg/x.go"})
	if _, ok := s.maybeDedupeRead(readArgs); ok {
		t.Fatal("first read should run")
	}
	bashArgs, _ := json.Marshal(map[string]string{"command": "cat pkg/x.go"})
	stub, ok := s.maybeDedupeBash(bashArgs)
	if !ok || !strings.Contains(stub, "already viewed") {
		t.Fatalf("bash cat after read should stub: ok=%v %q", ok, stub)
	}
}

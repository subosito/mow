package agent

import (
	"encoding/json"
	"path/filepath"
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

func TestBashReadPaths(t *testing.T) {
	cases := []struct {
		cmd  string
		want []string
	}{
		{`cat internal/adminplane/admin/server.go`, []string{"internal/adminplane/admin/server.go"}},
		{`sed -n '1,80p' internal/adminplane/admin/server.go`, []string{"internal/adminplane/admin/server.go"}},
		{`cd /proj && head -n 40 docs-internal/x.md`, []string{filepath.Join("/proj", "docs-internal/x.md")}},
		{`git status`, nil},
		{`go test ./...`, nil},
		{`cat a.go && cat b.go`, []string{"a.go", "b.go"}},
		{`grep -n "func" internal/foo.go`, []string{"internal/foo.go"}},
		{`awk '{print}' internal/foo.go`, []string{"internal/foo.go"}},
		{`rg context_search`, nil},
	}
	for _, c := range cases {
		got := bashReadPaths(c.cmd)
		if len(got) != len(c.want) {
			t.Errorf("cmd=%q got=%v want=%v", c.cmd, got, c.want)
			continue
		}
		for i := range got {
			if filepath.Clean(got[i]) != filepath.Clean(c.want[i]) {
				t.Errorf("cmd=%q got[%d]=%q want %q", c.cmd, i, got[i], c.want[i])
			}
		}
	}
}

func TestGuardBash(t *testing.T) {
	s := newThrashState("")
	args1, _ := json.Marshal(map[string]string{"command": "cat foo.go"})
	if g := s.guardBash(args1); g.blocked() || g.Notice != "" {
		t.Fatalf("first cat should run clean: %+v", g)
	}
	args2, _ := json.Marshal(map[string]string{"command": "sed -n '1,20p' foo.go"})
	g := s.guardBash(args2)
	if g.blocked() {
		t.Fatalf("second view must still run, not block: %q", g.Block)
	}
	if !strings.Contains(g.Notice, "already viewed") {
		t.Fatalf("second view should degrade: %q", g.Notice)
	}
	args3, _ := json.Marshal(map[string]string{"command": "go test ./..."})
	if g := s.guardBash(args3); g.blocked() || g.Notice != "" {
		t.Fatalf("go test should not path-degrade: %+v", g)
	}
}

func TestGuardBash_inventory(t *testing.T) {
	s := newThrashState("")
	for i := 0; i < inventoryLimit; i++ {
		args, _ := json.Marshal(map[string]string{"command": "git status --short"})
		if g := s.guardBash(args); g.blocked() || g.Notice != "" {
			t.Fatalf("status %d should run clean: %+v", i+1, g)
		}
	}
	args, _ := json.Marshal(map[string]string{"command": `cd "$(pwd)" && git status`})
	g := s.guardBash(args)
	if g.blocked() {
		t.Fatalf("third status must degrade, not refuse: %q", g.Block)
	}
	if !strings.Contains(g.Notice, "inventory") {
		t.Fatalf("third status should carry an inventory notice: %q", g.Notice)
	}
}

// Past hardInventoryLimit the model has been warned repeatedly and kept
// listing; only then does the guard refuse outright.
func TestGuardBash_inventoryHardLimitBlocks(t *testing.T) {
	s := newThrashState("")
	args, _ := json.Marshal(map[string]string{"command": "git status --short"})
	for i := 0; i < hardInventoryLimit; i++ {
		if g := s.guardBash(args); g.blocked() {
			t.Fatalf("call %d should not block yet: %q", i+1, g.Block)
		}
	}
	g := s.guardBash(args)
	if !g.blocked() || !strings.Contains(g.Block, "refusing") {
		t.Fatalf("want hard block past limit: %+v", g)
	}
}

func TestGuardBash_destructive(t *testing.T) {
	s := newThrashState("")
	args, _ := json.Marshal(map[string]string{"command": "git checkout -- foo.go && rm -rf internal/analytics"})
	g := s.guardBash(args)
	if !g.blocked() || !strings.Contains(g.Block, "blocked") {
		t.Fatalf("want blocked: %+v", g)
	}
}

// degradeToolResult must keep the real output, capped, behind the notice.
func TestDegradeToolResult(t *testing.T) {
	if got := degradeToolResult("", "body"); got != "body" {
		t.Fatalf("no notice should pass through: %q", got)
	}
	got := degradeToolResult("(note: capped)", "real output")
	if !strings.HasPrefix(got, "(note: capped)") {
		t.Fatalf("notice should lead: %q", got)
	}
	if !strings.Contains(got, "real output") {
		t.Fatalf("degraded result must keep the body: %q", got)
	}
	big := strings.Repeat("x", degradedResultLimit*3)
	if n := len(degradeToolResult("(note)", big)); n > degradedResultLimit*2 {
		t.Fatalf("degraded body not capped: %d", n)
	}
}

func TestPathKeyUnifiesAbsRel(t *testing.T) {
	ws := t.TempDir()
	s := newThrashState(ws)
	rel := "internal/foo.go"
	abs := filepath.Join(ws, rel)
	if k1, k2 := s.pathKey(rel), s.pathKey(abs); k1 != k2 {
		t.Fatalf("path keys differ: rel=%q abs=%q", k1, k2)
	}
	// Access via abs then relative bash should degrade.
	args1, _ := json.Marshal(map[string]string{"command": "cat " + abs})
	if g := s.guardBash(args1); g.blocked() || g.Notice != "" {
		t.Fatal("first should run clean")
	}
	args2, _ := json.Marshal(map[string]string{"command": "cat " + rel})
	g := s.guardBash(args2)
	if !strings.Contains(g.Notice, "already viewed") {
		t.Fatalf("want notice: %+v", g)
	}
}

func TestBatchExploreOnly_productiveBashResets(t *testing.T) {
	s := newThrashState("")
	bashCall := func(cmd string) []llm.ToolCall {
		args, _ := json.Marshal(map[string]string{"command": cmd})
		return []llm.ToolCall{{
			ID: "c", Type: "function",
			Function: llm.FunctionCall{Name: "bash", Arguments: string(args)},
		}}
	}
	for i := 0; i < exploreWarnEvery-1; i++ {
		if s.noteTurn(bashCall(`git status`)) {
			t.Fatalf("unexpected warn at explore %d", i+1)
		}
	}
	if !s.noteTurn(bashCall(`git status`)) {
		t.Fatal("want warn at exploreWarnEvery")
	}
	// After threshold, every explore turn warns.
	if !s.noteTurn(bashCall(`ls`)) {
		t.Fatal("want warn every turn after threshold")
	}
	if s.noteTurn(bashCall(`go test ./...`)) {
		t.Fatal("productive should not warn")
	}
	if s.exploreStreak != 0 {
		t.Fatalf("streak=%d want 0 after productive bash", s.exploreStreak)
	}
}

func TestReadAndBashSharePathDedupe(t *testing.T) {
	s := newThrashState("")
	readArgs, _ := json.Marshal(map[string]string{"path": "pkg/x.go"})
	if _, ok := s.maybeDedupeRead(readArgs); ok {
		t.Fatal("first read should run")
	}
	bashArgs, _ := json.Marshal(map[string]string{"command": "cat pkg/x.go"})
	g := s.guardBash(bashArgs)
	if !strings.Contains(g.Notice, "already viewed") {
		t.Fatalf("bash cat after read should degrade: %+v", g)
	}
}

func TestInventoryKey_rgAndPythonListing(t *testing.T) {
	if got := inventoryKey(`rg recall`); got != "rg" {
		t.Fatalf("rg: %q", got)
	}
	if got := inventoryKey(`cd "$(pwd)" && rg -n foo`); got != "rg" {
		t.Fatalf("rg after cd: %q", got)
	}
	if got := inventoryKey(`grep -n foo`); got != "grep" {
		t.Fatalf("grep: %q", got)
	}
	if got := inventoryKey(`python3 -c "import os; os.walk('.')"`); got != "python-list" {
		t.Fatalf("python walk: %q", got)
	}
	if got := inventoryKey(`python3 -c "print(1+1)"`); got != "" {
		t.Fatalf("plain python must not be inventory: %q", got)
	}
	if got := inventoryKey(`go test ./... | rg FAIL`); got != "" {
		t.Fatalf("productive test pipeline must not inventory-stub: %q", got)
	}
}

func TestGuardBash_rgInventory(t *testing.T) {
	s := newThrashState("")
	for i := 0; i < inventoryLimit; i++ {
		args, _ := json.Marshal(map[string]string{"command": "rg leftover"})
		if g := s.guardBash(args); g.blocked() || g.Notice != "" {
			t.Fatalf("rg %d should run clean: %+v", i+1, g)
		}
	}
	args, _ := json.Marshal(map[string]string{"command": `rg -n leftover src`})
	g := s.guardBash(args)
	if g.blocked() {
		t.Fatalf("third rg must degrade, not refuse: %q", g.Block)
	}
	if !strings.Contains(g.Notice, "inventory") {
		t.Fatalf("third rg should carry an inventory notice: %q", g.Notice)
	}
}

func TestNormalizeBashCmd_collidesStatus(t *testing.T) {
	a := normalizeBashCmd(`cd "$(pwd)" && git status --short`)
	b := normalizeBashCmd(`git status --short`)
	if inventoryKey(a) != inventoryKey(b) || inventoryKey(a) != "git-status" {
		t.Fatalf("a=%q b=%q keyA=%q keyB=%q", a, b, inventoryKey(a), inventoryKey(b))
	}
}

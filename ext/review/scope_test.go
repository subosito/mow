package review

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// fakeGit scripts git responses by joined-arg prefix.
type fakeGit struct {
	repo      bool
	dirty     bool
	changed   []string
	untracked []string
	// noDiffFor makes `git diff -- <path>` return empty, as happens for
	// untracked files and no-op changes.
	noDiffFor map[string]bool
	calls     []string
	fail      map[string]bool
}

func (g *fakeGit) run(ctx context.Context, ws string, args ...string) (string, error) {
	key := strings.Join(args, " ")
	g.calls = append(g.calls, key)
	if g.fail[key] {
		return "", fmt.Errorf("git failed: %s", key)
	}
	switch {
	case strings.HasPrefix(key, "rev-parse --is-inside-work-tree"):
		if !g.repo {
			return "", fmt.Errorf("not a git repository")
		}
		return "true", nil
	case strings.HasPrefix(key, "rev-parse --short HEAD"):
		return "abc1234", nil
	case strings.HasPrefix(key, "rev-parse --abbrev-ref HEAD"):
		return "feature/x", nil
	case strings.HasPrefix(key, "status --porcelain"):
		if g.dirty {
			return " M internal/api/users.go", nil
		}
		return "", nil
	case strings.HasPrefix(key, "diff --name-only"):
		return strings.Join(g.changed, "\n"), nil
	case strings.HasPrefix(key, "ls-files --others"):
		return strings.Join(g.untracked, "\n"), nil
	case strings.HasPrefix(key, "diff --no-color"):
		for p := range g.noDiffFor {
			if strings.HasSuffix(key, " "+p) {
				return "", nil
			}
		}
		return "@@ -1,3 +1,4 @@\n+added line", nil
	}
	return "", nil
}

// memFS serves file bytes by absolute path suffix.
func memFS(files map[string]string) readFileFunc {
	return func(name string) ([]byte, error) {
		for rel, body := range files {
			if strings.HasSuffix(name, rel) {
				return []byte(body), nil
			}
		}
		return nil, fmt.Errorf("no such file: %s", name)
	}
}

func TestResolveScopeDiffMode(t *testing.T) {
	g := &fakeGit{repo: true, changed: []string{"internal/api/users.go", "vendor/x/y.go", "go.sum"}}
	fs := memFS(map[string]string{
		"internal/api/users.go": "package api\n\nfunc F() {}\n",
		"vendor/x/y.go":         "package x\n",
		"go.sum":                "hash\n",
	})
	sc, err := resolveScope(context.Background(), ScopeRequest{
		Workspace: "/ws", Diff: "main...HEAD",
	}, g.run, fs)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if sc.Mode != "diff" || sc.Selector != "main...HEAD" {
		t.Errorf("mode/selector = %q/%q", sc.Mode, sc.Selector)
	}
	if len(sc.Files) != 1 || sc.Files[0].Path != "internal/api/users.go" {
		t.Fatalf("files = %+v", sc.Paths())
	}
	if len(sc.Excluded) != 2 {
		t.Errorf("excluded = %+v", sc.Excluded)
	}
	if sc.Files[0].Diff == "" {
		t.Error("diff mode should attach a per-file diff")
	}
	if !sc.Git.Available || sc.Git.Commit != "abc1234" || sc.Git.Branch != "feature/x" {
		t.Errorf("git context = %+v", sc.Git)
	}
	if !sc.InScope("internal/api/users.go") || sc.InScope("vendor/x/y.go") {
		t.Error("InScope disagrees with resolved files")
	}
	if n, ok := sc.FileLines("internal/api/users.go"); !ok || n != 3 {
		t.Errorf("FileLines = %d,%v want 3", n, ok)
	}
}

func TestResolveScopeStagedAndBase(t *testing.T) {
	files := memFS(map[string]string{"a.go": "package a\n"})
	g := &fakeGit{repo: true, changed: []string{"a.go"}}
	sc, err := resolveScope(context.Background(), ScopeRequest{Workspace: "/ws", Staged: true}, g.run, files)
	if err != nil {
		t.Fatalf("staged: %v", err)
	}
	if sc.Mode != "staged" {
		t.Errorf("mode = %q", sc.Mode)
	}
	if !containsCall(g.calls, "--cached") {
		t.Errorf("staged scope must use --cached: %v", g.calls)
	}

	g2 := &fakeGit{repo: true, changed: []string{"a.go"}}
	sc2, err := resolveScope(context.Background(), ScopeRequest{Workspace: "/ws", Base: "origin/main"}, g2.run, files)
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	if sc2.Mode != "base" || sc2.Selector != "origin/main...HEAD" {
		t.Errorf("base mode/selector = %q/%q", sc2.Mode, sc2.Selector)
	}
}

func TestResolveScopePrecedence(t *testing.T) {
	files := memFS(map[string]string{"a.go": "package a\n"})
	g := &fakeGit{repo: true, dirty: true, changed: []string{"a.go"}}
	// --diff wins over --staged and --base.
	sc, err := resolveScope(context.Background(), ScopeRequest{
		Workspace: "/ws", Diff: "main...HEAD", Staged: true, Base: "origin/main", Paths: []string{"x"},
	}, g.run, files)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if sc.Mode != "diff" {
		t.Fatalf("mode = %q, want diff to win", sc.Mode)
	}
	// --staged wins over --base.
	sc2, _ := resolveScope(context.Background(), ScopeRequest{
		Workspace: "/ws", Staged: true, Base: "origin/main",
	}, g.run, files)
	if sc2.Mode != "staged" {
		t.Fatalf("mode = %q, want staged over base", sc2.Mode)
	}
}

func TestResolveScopeDirtyWorktreeDefault(t *testing.T) {
	g := &fakeGit{repo: true, dirty: true, changed: []string{"a.go"}}
	sc, err := resolveScope(context.Background(), ScopeRequest{Workspace: "/ws"},
		g.run, memFS(map[string]string{"a.go": "package a\n"}))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if sc.Mode != "worktree" {
		t.Fatalf("mode = %q, want worktree default on a dirty repo", sc.Mode)
	}
	if !containsCall(g.calls, "ls-files --others") {
		t.Error("worktree scope should also pick up untracked files")
	}
}

func TestResolveScopeGitRequiredForDiff(t *testing.T) {
	g := &fakeGit{repo: false}
	for _, req := range []ScopeRequest{
		{Workspace: "/ws", Diff: "main...HEAD"},
		{Workspace: "/ws", Staged: true},
		{Workspace: "/ws", Base: "origin/main"},
	} {
		if _, err := resolveScope(context.Background(), req, g.run, memFS(nil)); err == nil {
			t.Errorf("want error without git for %+v", req)
		} else if !strings.Contains(err.Error(), "git repository") {
			t.Errorf("unclear error: %v", err)
		}
	}
}

func TestResolveScopeErrors(t *testing.T) {
	g := &fakeGit{repo: true}
	if _, err := resolveScope(context.Background(), ScopeRequest{}, g.run, memFS(nil)); err == nil {
		t.Error("want error for empty workspace")
	}
	_, err := resolveScope(context.Background(), ScopeRequest{Workspace: "/ws", Budget: "gigantic"}, g.run, memFS(nil))
	if err == nil || !strings.Contains(err.Error(), "budget") {
		t.Errorf("want budget error, got %v", err)
	}
}

func containsCall(calls []string, sub string) bool {
	for _, c := range calls {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

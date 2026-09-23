package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitT runs git hermetically (no user/system config) and fails on error.
func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@localhost",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@localhost",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// newRepo makes <tmp>/app with one commit on main. Worktrees go to
// <tmp>/app-worktrees, inside the test's temp dir.
func newRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@localhost")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@localhost")
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(parent, "app")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "add", "-A")
	gitT(t, repo, "commit", "-m", "seed")
	return repo
}

func TestValidateName(t *testing.T) {
	for _, tc := range []struct {
		name string
		ok   bool
	}{
		{"fix-auth", true},
		{"v1.2_try", true},
		{"a", true},
		{"", false},
		{"-x", false},
		{"Fix", false},
		{"a/b", false},
		{"a..b", false},
		{"sp ace", false},
		{strings.Repeat("a", maxNameLen+1), false},
	} {
		if err := ValidateName(tc.name); (err == nil) != tc.ok {
			t.Errorf("ValidateName(%q) err=%v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}

func TestAddListRemove(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)

	info, err := Add(ctx, repo, "fix-auth", "")
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(filepath.Dir(repo), "app-worktrees", "fix-auth")
	if info.Path != wantPath || info.Branch != "wt/fix-auth" || info.Head == "" {
		t.Fatalf("info=%+v want path %s", info, wantPath)
	}
	if _, err := os.Stat(filepath.Join(wantPath, "a.txt")); err != nil {
		t.Fatalf("checkout missing: %v", err)
	}

	// Same name again must not reuse or clobber.
	if _, err := Add(ctx, repo, "fix-auth", ""); err == nil {
		t.Fatal("duplicate add succeeded")
	}

	// List from the main checkout and from inside the worktree agree.
	for _, from := range []string{repo, wantPath} {
		list, err := List(ctx, from)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 2 || !list[0].Main || list[0].Path != repo {
			t.Fatalf("from %s: list=%+v", from, list)
		}
		if list[1].Name != "fix-auth" || list[1].Branch != "wt/fix-auth" || list[1].Dirty {
			t.Fatalf("from %s: managed=%+v", from, list[1])
		}
	}

	// Remove from inside the worktree resolves the main repo.
	if err := Remove(ctx, wantPath, "fix-auth", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wantPath); !os.IsNotExist(err) {
		t.Fatalf("worktree dir still present: %v", err)
	}
	if out := gitT(t, repo, "branch", "--list", "wt/fix-auth"); out != "" {
		t.Fatalf("branch kept: %q", out)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(repo), "app-worktrees")); !os.IsNotExist(err) {
		t.Fatal("empty worktrees dir not cleaned up")
	}
}

func TestAddFromBase(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	first := gitT(t, repo, "rev-parse", "--short", "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "add", "-A")
	gitT(t, repo, "commit", "-m", "second")

	info, err := Add(ctx, repo, "old", first)
	if err != nil {
		t.Fatal(err)
	}
	if info.Head != first {
		t.Fatalf("head=%s want %s", info.Head, first)
	}
	if _, err := os.Stat(filepath.Join(info.Path, "b.txt")); !os.IsNotExist(err) {
		t.Fatal("worktree not at base ref")
	}
	if _, err := Add(ctx, repo, "bad", "--orphan"); err == nil {
		t.Fatal("flag-like base accepted")
	}
}

func TestRemoveRefusesToLoseWork(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	info, err := Add(ctx, repo, "wip", "")
	if err != nil {
		t.Fatal(err)
	}

	// Uncommitted change: refused without force.
	if err := os.WriteFile(filepath.Join(info.Path, "a.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Remove(ctx, repo, "wip", false); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("dirty remove err=%v", err)
	}

	// Committed but unmerged: still refused.
	gitT(t, info.Path, "commit", "-am", "work")
	if err := Remove(ctx, repo, "wip", false); err == nil || !strings.Contains(err.Error(), "not merged") {
		t.Fatalf("unmerged remove err=%v", err)
	}

	// Force discards both.
	if err := Remove(ctx, repo, "wip", true); err != nil {
		t.Fatal(err)
	}
	if out := gitT(t, repo, "branch", "--list", "wt/wip"); out != "" {
		t.Fatalf("branch kept: %q", out)
	}
}

func TestRemoveOnlyManaged(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	// A human-made worktree elsewhere is listed but never removable by name.
	other := filepath.Join(filepath.Dir(repo), "elsewhere")
	gitT(t, repo, "worktree", "add", "-b", "feature", other)
	list, err := List(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[1].Name != "" || list[1].Branch != "feature" {
		t.Fatalf("list=%+v", list)
	}
	if err := Remove(ctx, repo, "elsewhere", true); err == nil {
		t.Fatal("removed an unmanaged worktree")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("unmanaged worktree touched: %v", err)
	}
}

func TestNotARepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	if _, err := List(context.Background(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "not inside a git repository") {
		t.Fatalf("err=%v", err)
	}
}

func TestRunCommandPathAndHelp(t *testing.T) {
	repo := newRepo(t)
	if code := runCommand([]string{"help"}); code != 0 {
		t.Fatalf("help code=%d", code)
	}
	if code := runCommand([]string{"add", "--dir", repo, "cli-one"}); code != 0 {
		t.Fatalf("add code=%d", code)
	}
	if _, err := os.Stat(Path(repo, "cli-one")); err != nil {
		t.Fatal(err)
	}
	// Flags after the positional name are accepted too.
	if code := runCommand([]string{"remove", "cli-one", "--dir", repo}); code != 0 {
		t.Fatalf("remove code=%d", code)
	}
	// `--` ends flags (hosts such as mowi pass names this way), and a
	// flag-shaped name is still rejected by ValidateName, not parsed as a flag.
	if code := runCommand([]string{"add", "--dir", repo, "--", "cli-two"}); code != 0 {
		t.Fatalf("add -- code=%d", code)
	}
	if code := runCommand([]string{"path", "--dir", repo, "--", "-x"}); code != 1 {
		t.Fatalf("flag-shaped name code=%d, want 1", code)
	}
	if code := runCommand([]string{"bogus"}); code != 2 {
		t.Fatalf("unknown code=%d", code)
	}
}

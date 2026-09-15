package goal_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext/goal"
)

// gitCmd runs a git command in dir and fails the test on error.
func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// Keep the test hermetic: never read the developer's ~/.gitconfig.
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@localhost",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@localhost",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// initRepo creates a git repo with one commit on a known branch.
func initRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ws := t.TempDir()
	gitCmd(t, ws, "init", "-b", "main")
	gitCmd(t, ws, "config", "user.name", "test")
	gitCmd(t, ws, "config", "user.email", "test@localhost")
	if err := os.WriteFile(filepath.Join(ws, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, ws, "add", "-A")
	gitCmd(t, ws, "commit", "-m", "seed")
	return ws
}

// worktreeEnv wires a runner whose worktree engines write a file in whatever
// checkout they are given, so a merge has something real to carry.
func worktreeEnv(t *testing.T, ws string, write func(dir, item string) error) (*goal.Runner, string) {
	t.Helper()
	t.Setenv("MOW_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "m")
	dir := t.TempDir()

	// Chat that plans two worktree items, then reports each one done after
	// performing the side effect in its own engine's workspace.
	chatFor := func(workspace string) mow.ChatFunc {
		return func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			item := focusItem(messages)
			if item == "" {
				if planned := hasPlan(messages); !planned {
					return reportCall("p", map[string]any{
						"status": "continue",
						"plan": []map[string]string{
							{"id": "a", "title": "item a", "status": "pending", "worker": "worktree"},
							{"id": "b", "title": "item b", "status": "pending", "worker": "worktree"},
						},
						"summary": "planned",
					}), nil
				}
				return reportCall("d", map[string]any{"status": "done", "summary": "goal complete"}), nil
			}
			if write != nil {
				if err := write(workspace, item); err != nil {
					return mow.Message{}, err
				}
			}
			return reportCall("w", map[string]any{
				"status": "continue", "item_id": item, "item_status": "done",
				"summary": "did " + item,
			}), nil
		}
	}

	newEng := func() (*mow.Engine, error) {
		return mow.New(mow.Options{Workspace: ws, NoSession: true, Chat: chatFor(ws)})
	}
	eng, err := newEng()
	if err != nil {
		t.Fatal(err)
	}
	r := &goal.Runner{
		Engine: eng, Store: &goal.Store{Dir: dir},
		EngineFactory: newEng,
		WorktreeEngineFactory: func(wtDir string) (*mow.Engine, error) {
			return mow.New(mow.Options{
				Workspace: wtDir, NoSession: true,
				AllowWrite: true, Chat: chatFor(wtDir),
			})
		},
	}
	return r, ws
}

// hasPlan reports whether a checklist is already present in the prompt.
func hasPlan(messages []mow.Message) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" && strings.Contains(messages[i].Content, "item a") {
			return true
		}
	}
	return false
}

// (a) A workspace that is not a git repo must not fail the goal: the item runs
// as an ordinary step and says why isolation was skipped.
func TestWorktreeFallsBackOutsideGitRepo(t *testing.T) {
	ws := t.TempDir() // deliberately not a repo
	r, _ := worktreeEnv(t, ws, nil)

	var notes []string
	r.OnEvent = func(ev goal.Event) { notes = append(notes, ev.Text) }

	st, err := r.RunSpec(context.Background(), goal.Spec{
		ID: "wt-nogit", Goal: "two things", MaxSteps: 8,
	})
	if err != nil {
		t.Fatalf("run: %v (status=%s err=%q)", err, st.Status, st.Error)
	}
	if st.Status != goal.StatusDone {
		t.Fatalf("status=%s want done (missing git must never fail a goal)", st.Status)
	}
	if !strings.Contains(strings.Join(notes, "\n"), "not a git repository") {
		t.Fatalf("want a skip note in the event stream, got %q", notes)
	}
}

// (b) In a real repo, a worktree item's work lands as a commit in the parent.
func TestWorktreeItemMergesCommitIntoParent(t *testing.T) {
	ws := initRepo(t)
	r, _ := worktreeEnv(t, ws, func(dir, item string) error {
		return os.WriteFile(filepath.Join(dir, item+".txt"), []byte(item+"\n"), 0o644)
	})

	st, err := r.RunSpec(context.Background(), goal.Spec{
		ID: "wt-merge", Goal: "two things", MaxSteps: 10,
	})
	if err != nil {
		t.Fatalf("run: %v (status=%s err=%q)", err, st.Status, st.Error)
	}
	// Both worker files exist in the parent working tree after the merges.
	for _, name := range []string{"a.txt", "b.txt"} {
		if _, err := os.Stat(filepath.Join(ws, name)); err != nil {
			t.Fatalf("%s missing from parent after merge: %v", name, err)
		}
	}
	// The merges are real commits, not a dirty tree.
	if out := gitCmd(t, ws, "status", "--porcelain"); out != "" {
		t.Fatalf("parent workspace dirty after merge: %q", out)
	}
	if log := gitCmd(t, ws, "log", "--oneline"); !strings.Contains(log, "mow:") {
		t.Fatalf("no mow commit in parent log: %q", log)
	}
	// Cleanup removed the temporary worktrees and branches.
	if wts := gitCmd(t, ws, "worktree", "list"); strings.Count(wts, "\n") != 0 {
		t.Fatalf("worktrees left behind: %q", wts)
	}
	if br := gitCmd(t, ws, "branch", "--list", "mow-wt-*"); strings.TrimSpace(br) != "" {
		t.Fatalf("worker branches left behind: %q", br)
	}
}

// (c) A conflicting change in the parent must escalate to a human, never be
// resolved silently, and the worktree must survive for inspection.
func TestWorktreeConflictEscalates(t *testing.T) {
	ws := initRepo(t)
	// The worker edits shared.txt in its checkout; meanwhile the parent branch
	// commits a different shared.txt. The two have now genuinely diverged from
	// a common base, which is the only way to produce a real merge conflict.
	var once sync.Once
	r, _ := worktreeEnv(t, ws, func(dir, item string) error {
		if err := os.WriteFile(filepath.Join(dir, "shared.txt"),
			[]byte("from "+item+"\n"), 0o644); err != nil {
			return err
		}
		once.Do(func() {
			if err := os.WriteFile(filepath.Join(ws, "shared.txt"),
				[]byte("from parent\n"), 0o644); err != nil {
				t.Error(err)
				return
			}
			gitCmd(t, ws, "add", "-A")
			gitCmd(t, ws, "commit", "-m", "parent edit")
		})
		return nil
	})

	st, err := r.RunSpec(context.Background(), goal.Spec{
		ID: "wt-conflict", Goal: "conflicting things", MaxSteps: 10,
	})
	// A blocked goal returns an error by design (the caller must act); the
	// state is what carries the human decision.
	if err != nil && st.Status != goal.StatusBlocked {
		t.Fatalf("run: %v (status=%s err=%q)", err, st.Status, st.Error)
	}
	if st.Status != goal.StatusBlocked {
		t.Fatalf("status=%s want blocked (merge conflict must escalate)", st.Status)
	}
	if !strings.Contains(st.Question, "merge conflict") {
		t.Fatalf("question=%q want a merge conflict question", st.Question)
	}
	// The parent workspace is left clean: the merge aborted, not half-applied.
	if out := gitCmd(t, ws, "status", "--porcelain"); out != "" {
		t.Fatalf("parent left mid-merge: %q", out)
	}
	// The conflicted worktree is preserved for the human.
	if wts := gitCmd(t, ws, "worktree", "list"); !strings.Contains(wts, "mow-wt") {
		t.Fatalf("conflicted worktree was cleaned up: %q", wts)
	}
}

// (d) Worktree items compose with ParallelMax: two isolated workers run
// concurrently and both merge back.
func TestWorktreeComposesWithParallel(t *testing.T) {
	ws := initRepo(t)
	r, _ := worktreeEnv(t, ws, func(dir, item string) error {
		return os.WriteFile(filepath.Join(dir, item+".txt"), []byte(item+"\n"), 0o644)
	})

	st, err := r.RunSpec(context.Background(), goal.Spec{
		ID: "wt-par", Goal: "two things", MaxSteps: 10, ParallelMax: 2,
	})
	if err != nil {
		t.Fatalf("run: %v (status=%s err=%q)", err, st.Status, st.Error)
	}
	for _, name := range []string{"a.txt", "b.txt"} {
		if _, err := os.Stat(filepath.Join(ws, name)); err != nil {
			t.Fatalf("%s missing after parallel worktree merges: %v", name, err)
		}
	}
	if out := gitCmd(t, ws, "status", "--porcelain"); out != "" {
		t.Fatalf("parent workspace dirty: %q", out)
	}
}

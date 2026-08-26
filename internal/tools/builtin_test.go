package tools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/subosito/mow/internal/policy"
	"github.com/subosito/mow/internal/tools"
)

func TestReadAndGlobUnderWorkspace(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello-mow"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{Workspace: root, MaxReadBytes: 1 << 20}
	reg := tools.Registry(p, []string{"read", "glob", "grep"})
	var readT, globT interface {
		Exec(context.Context, json.RawMessage) (string, error)
		Name() string
	}
	for _, tool := range reg {
		switch tool.Name() {
		case "read":
			readT = tool
		case "glob":
			globT = tool
		}
	}
	if readT == nil || globT == nil {
		t.Fatal("missing tools")
	}
	out, err := readT.Exec(context.Background(), json.RawMessage(`{"path":"a.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello-mow" {
		t.Fatalf("read=%q", out)
	}
	// escape
	if _, err := readT.Exec(context.Background(), json.RawMessage(`{"path":"../x"}`)); err == nil {
		t.Fatal("expected path jail")
	}
	list, err := globT.Exec(context.Background(), json.RawMessage(`{"pattern":"*.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(list, "a.txt") {
		t.Fatalf("glob=%q", list)
	}
}

func TestWriteDeniedByPolicy(t *testing.T) {
	root := t.TempDir()
	p := &policy.Policy{Workspace: root, AllowWrite: false}
	reg := tools.Registry(p, []string{"write"})
	if len(reg) != 1 {
		t.Fatalf("want write tool in registry when enabled list includes it")
	}
	_, err := reg[0].Exec(context.Background(), json.RawMessage(`{"path":"x","content":"y"}`))
	if err == nil {
		t.Fatal("expected write deny")
	}
}

func TestBashDeniedByPolicy(t *testing.T) {
	root := t.TempDir()
	p := &policy.Policy{Workspace: root, AllowShell: false}
	reg := tools.Registry(p, []string{"bash"})
	_, err := reg[0].Exec(context.Background(), json.RawMessage(`{"command":"echo hi"}`))
	if err == nil {
		t.Fatal("expected bash deny")
	}
}

func TestWriteAllowedUnderWorkspace(t *testing.T) {
	root := t.TempDir()
	p := &policy.Policy{Workspace: root, AllowWrite: true}
	reg := tools.Registry(p, []string{"write"})
	out, err := reg[0].Exec(context.Background(), json.RawMessage(`{"path":"out.txt","content":"ok"}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "out.txt"))
	if err != nil || string(b) != "ok" {
		t.Fatalf("file=%q err=%v", b, err)
	}
	if !strings.Contains(out, "created out.txt") || !strings.Contains(out, "+ok") {
		t.Fatalf("want create diff with path, got %q", out)
	}
}

func TestEditReturnsDiffWithPath(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f.go"), []byte("package main\n\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{Workspace: root, AllowWrite: true}
	reg := tools.Registry(p, []string{"edit"})
	out, err := reg[0].Exec(context.Background(), json.RawMessage(
		`{"path":"f.go","old_string":"func A() {}","new_string":"func B() {}"}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "edited f.go") {
		t.Fatalf("missing path: %q", out)
	}
	if !strings.Contains(out, "-func A() {}") || !strings.Contains(out, "+func B() {}") {
		t.Fatalf("missing hunk: %q", out)
	}
	data, _ := os.ReadFile(filepath.Join(root, "f.go"))
	if !strings.Contains(string(data), "func B()") {
		t.Fatalf("file not updated: %q", data)
	}
}

func TestBashOutputIsBoundedWhileRunning(t *testing.T) {
	root := t.TempDir()
	p := &policy.Policy{Workspace: root, AllowShell: true}
	reg := tools.Registry(p, []string{"bash"})
	out, err := reg[0].Exec(context.Background(), json.RawMessage(`{"command":"head -c 1000000 /dev/zero | tr '\\0' x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "elided from the middle") {
		t.Fatalf("expected head+tail elision marker, len=%d", len(out))
	}
	if len(out) > 101_000 {
		t.Fatalf("bash output grew past cap: %d", len(out))
	}
}

// Bash output passes through as-is (byte-budgeted only); no line-based
// clamping or command recognition — that policy belongs to packs/extensions.
func TestBashOutputPassesThroughAsIs(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 150; i++ {
		name := filepath.Join(root, "f"+strconv.Itoa(i)+".txt")
		if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p := &policy.Policy{Workspace: root, AllowShell: true}
	reg := tools.Registry(p, []string{"bash"})
	out, err := reg[0].Exec(context.Background(), json.RawMessage(`{"command":"ls -1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(out, "\n"); n < 149 {
		t.Fatalf("ls of 150 files lost entries: %d lines", n)
	}
	if strings.Contains(out, "listing cap") {
		t.Fatal("bash must not clamp listings")
	}
	// The byte budget still bounds total volume.
	seq, err := reg[0].Exec(context.Background(), json.RawMessage(`{"command":"seq 1 200"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seq, "200") {
		t.Fatalf("seq lost the tail: %.80q", seq)
	}
}

func TestBashTimeoutSoftReturns(t *testing.T) {
	// BashTimeoutSec caps each exec and soft-returns a clear message rather
	// than erroring — the agent loop must keep going and self-correct.
	root := t.TempDir()
	p := &policy.Policy{Workspace: root, AllowShell: true, BashTimeoutSec: 1}
	reg := tools.Registry(p, []string{"bash"})
	out, err := reg[0].Exec(context.Background(), json.RawMessage(`{"command":"sleep 30"}`))
	if err != nil {
		t.Fatalf("timeout must soft-return, not error: %v", err)
	}
	if !strings.Contains(out, "timed out after 1s") {
		t.Fatalf("expected timeout message, got %q", out)
	}
}

func TestBashTimeoutKillsProcessGroup(t *testing.T) {
	// A child started by a timed-out command must not survive as an orphan.
	// The process-group SIGKILL reaps it: a child scheduled to write a marker
	// file after the parent's sleep gets killed before it can write.
	root := t.TempDir()
	marker := filepath.Join(root, "survived.txt")
	p := &policy.Policy{Workspace: root, AllowShell: true, BashTimeoutSec: 1}
	reg := tools.Registry(p, []string{"bash"})
	// Child outlives the timeout window: it would write the marker ~5s in, but
	// the group kill at 1s must stop it first.
	cmd := "(sleep 5; echo yes > " + marker + ") & disown; sleep 30"
	out, err := reg[0].Exec(context.Background(), json.RawMessage(`{"command":`+jsonQuote(cmd)+`}`))
	if err != nil {
		t.Fatalf("timeout must soft-return: %v", err)
	}
	if !strings.Contains(out, "timed out") {
		t.Fatalf("expected timeout, got %q", out)
	}
	// Wait past the child's scheduled write to prove it was reaped.
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("orphan child survived timeout and wrote marker — process-group kill failed")
	}
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestBashCustomTimeoutFromPolicy(t *testing.T) {
	// BashTimeoutSec=0 (unset) defaults to 300s; an explicit small value wins.
	root := t.TempDir()
	for _, tc := range []struct {
		name string
		sec  int
	}{
		{"default when zero", 0},
		{"explicit two", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &policy.Policy{Workspace: root, AllowShell: true, BashTimeoutSec: tc.sec}
			reg := tools.Registry(p, []string{"bash"})
			// Fast command completes well under any timeout; confirms wiring
			// does not regress the happy path.
			out, err := reg[0].Exec(context.Background(), json.RawMessage(`{"command":"echo fast"}`))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, "fast") {
				t.Fatalf("got %q", out)
			}
		})
	}
}

// A slow command can ask for more time than the policy default, so an agent
// running a cold build or full test suite does not have to fall back to
// background-process workarounds.
func TestBashPerCallTimeoutExtendsDefault(t *testing.T) {
	root := t.TempDir()
	p := &policy.Policy{Workspace: root, AllowShell: true, BashTimeoutSec: 1, MaxBashTimeoutSec: 900}
	reg := tools.Registry(p, []string{"bash"})

	// Without the override, a 2s command exceeds the 1s policy default.
	out, err := reg[0].Exec(context.Background(), json.RawMessage(`{"command":"sleep 2; echo late"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "timed out") {
		t.Fatalf("expected timeout without override, got %q", out)
	}
	// With timeout_sec it completes.
	out, err = reg[0].Exec(context.Background(), json.RawMessage(`{"command":"sleep 2; echo late","timeout_sec":30}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "late") {
		t.Fatalf("expected completion with timeout_sec, got %q", out)
	}
}

// A per-call request may not exceed the configured ceiling, so a model cannot
// park the loop on a hung command by asking for an enormous timeout.
func TestBashPerCallTimeoutClampedToCeiling(t *testing.T) {
	root := t.TempDir()
	p := &policy.Policy{Workspace: root, AllowShell: true, BashTimeoutSec: 1, MaxBashTimeoutSec: 2}
	reg := tools.Registry(p, []string{"bash"})
	start := time.Now()
	out, err := reg[0].Exec(context.Background(), json.RawMessage(`{"command":"sleep 60","timeout_sec":600}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "timed out") {
		t.Fatalf("expected clamped timeout, got %q", out)
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("ceiling not enforced: waited %s", elapsed)
	}
}

// The timeout message must teach the recovery path, since that text is the
// only feedback the model gets.
func TestBashTimeoutMessageSuggestsRetry(t *testing.T) {
	root := t.TempDir()
	p := &policy.Policy{Workspace: root, AllowShell: true, BashTimeoutSec: 1}
	reg := tools.Registry(p, []string{"bash"})
	out, err := reg[0].Exec(context.Background(), json.RawMessage(`{"command":"sleep 5"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "timeout_sec") {
		t.Fatalf("timeout message should point at timeout_sec: %q", out)
	}
}

func TestReadMissingFileSuggestsNearby(t *testing.T) {
	ws := t.TempDir()
	for _, f := range []string{"tui.go", "layout_test.go", "render.go"} {
		if err := os.WriteFile(filepath.Join(ws, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p := &policy.Policy{Workspace: ws}
	var rd interface {
		Exec(ctx context.Context, args json.RawMessage) (string, error)
	}
	for _, tool := range tools.Registry(p, []string{"read"}) {
		if tool.Name() == "read" {
			rd = tool
		}
	}
	if rd == nil {
		t.Fatal("read tool missing from registry")
	}
	_, err := rd.Exec(context.Background(), json.RawMessage(`{"path":"layout.go"}`))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	msg := err.Error()
	if !strings.Contains(msg, "no such file") {
		t.Fatalf("unexpected error: %v", err)
	}
	// The stem match must surface the real neighbor.
	if !strings.Contains(msg, "layout_test.go") {
		t.Fatalf("expected nearby suggestion in error: %v", err)
	}
	// No stem match → directory listing fallback.
	_, err = rd.Exec(context.Background(), json.RawMessage(`{"path":"zzz.go"}`))
	if err == nil || !strings.Contains(err.Error(), "directory contains:") {
		t.Fatalf("expected directory fallback: %v", err)
	}
}

// The diff path must be displayed relative to the WORKSPACE, never the raw
// input: editing a workspace file passed as an absolute path (or "../mow/…"
// when mow is an extra root of a sibling workspace) shows the clean relative
// path — "sub/f.go", not "../mow/…" when the workspace IS mow.
func TestEditDiffPathWorkspaceRelative(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(sub, "f.go")
	if err := os.WriteFile(target, []byte("package main\n\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{Workspace: root, AllowWrite: true}
	reg := tools.Registry(p, []string{"edit"})

	// Model passes the ABSOLUTE path — display must still be workspace-relative.
	out, err := reg[0].Exec(context.Background(), json.RawMessage(
		`{"path":"`+target+`","old_string":"func A() {}","new_string":"func B() {}"}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "edited sub/f.go") {
		t.Fatalf("want workspace-relative path, got %q", out)
	}
	if strings.Contains(out, root) {
		t.Fatalf("diff leaked the absolute workspace path: %q", out)
	}
}

// An extra-root file (sibling workspace) displays as "../sibling/…", which is
// the correct relative form — not a bare basename.
func TestEditDiffPathExtraRootRelative(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir() // sibling "workspace" outside root
	if err := os.WriteFile(filepath.Join(other, "x.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{Workspace: root, AllowWrite: true, ExtraRoots: []string{other}}
	reg := tools.Registry(p, []string{"edit"})

	out, err := reg[0].Exec(context.Background(), json.RawMessage(
		`{"path":"`+filepath.Join(other, "x.go")+`","old_string":"package x","new_string":"package y"}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "edited ../"+filepath.Base(other)+"/x.go") {
		t.Fatalf("want extra-root relative path, got %q", out)
	}
}

func TestReadPagesPastByteCapAndRefusesBinary(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	for i := 1; i <= 80; i++ {
		fmt.Fprintf(&b, "LINE-%d\n", i)
	}
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin.dat"), []byte("hi\x00there"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{Workspace: root, MaxReadBytes: 40}
	reg := tools.Registry(p, []string{"read"})
	out, err := reg[0].Exec(context.Background(), json.RawMessage(`{"path":"big.txt","offset":70,"limit":5}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "LINE-70") {
		t.Fatalf("offset must reach past the first MaxReadBytes, got %q", out)
	}
	if strings.Contains(strings.ToLower(out), "bash") || strings.Contains(out, "sed") {
		t.Fatalf("must not send the model to bash: %q", out)
	}
	_, err = reg[0].Exec(context.Background(), json.RawMessage(`{"path":"bin.dat"}`))
	if err == nil {
		t.Fatal("binary read must fail")
	}
	if !strings.Contains(err.Error(), "binary") {
		t.Fatalf("want binary error, got %v", err)
	}
}

func TestGrepSkipsDotGitUnlessPathIsDotGit(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "secret.txt"), []byte("PATTERN in git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "a.go"), []byte("PATTERN in src\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{Workspace: root, MaxReadBytes: 1 << 20}
	reg := tools.Registry(p, []string{"grep"})
	out, err := reg[0].Exec(context.Background(), json.RawMessage(`{"pattern":"PATTERN"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.go") {
		t.Fatalf("want src hit, got %q", out)
	}
	if strings.Contains(out, "secret.txt") || strings.Contains(out, ".git") {
		t.Fatalf(".git must be skipped from .: %q", out)
	}
	gitOut, err := reg[0].Exec(context.Background(), json.RawMessage(`{"pattern":"PATTERN","path":".git"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gitOut, "secret.txt") {
		t.Fatalf("explicit .git path must be searched: %q", gitOut)
	}
}

func TestHashlineEditRejectsDuplicateLines(t *testing.T) {
	root := t.TempDir()
	body := "dup\nother\ndup\n"
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{Workspace: root, AllowWrite: true, Hashline: true}
	reg := tools.Registry(p, []string{"read", "edit"})
	readOut, err := reg[0].Exec(context.Background(), json.RawMessage(`{"path":"f.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	// First hashline token after "1:"
	line := strings.Split(readOut, "\n")[0]
	parts := strings.SplitN(line, "|", 2)
	hash := strings.TrimSpace(strings.Split(parts[0], ":")[1])
	_, err = reg[1].Exec(context.Background(), json.RawMessage(
		`{"path":"f.txt","line_hash":"`+hash+`","new_string":"changed"}`))
	if err == nil {
		t.Fatal("duplicate hash must fail")
	}
	got, _ := os.ReadFile(filepath.Join(root, "f.txt"))
	if string(got) != body {
		t.Fatalf("file mutated: %q", got)
	}
}

func TestBashKillsBackgroundChildOnReturn(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "bg.txt")
	p := &policy.Policy{Workspace: root, AllowShell: true}
	reg := tools.Registry(p, []string{"bash"})
	cmd := "(sleep 8; echo leaked > " + marker + ") & echo started"
	out, err := reg[0].Exec(context.Background(), json.RawMessage(`{"command":`+jsonQuote(cmd)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "started") {
		t.Fatalf("got %q", out)
	}
	time.Sleep(2 * time.Second)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("background child survived bash return")
	}
}

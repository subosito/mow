package contextsink

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subosito/mow"
)

func TestContextSearchFindsArchiveHit(t *testing.T) {
	root := t.TempDir()
	adir := filepath.Join(root, "sess1.archive")
	if err := os.MkdirAll(adir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "# archive\n## [user]\nremember marker-zeta-99\n"
	if err := os.WriteFile(filepath.Join(adir, "0001-x.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := newContextSearchTool(root, "sess1")
	out, err := tool.Exec(context.Background(), json.RawMessage(`{"pattern":"marker-zeta-99"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "marker-zeta-99") {
		t.Fatalf("miss: %q", out)
	}
}

func TestContextSearchEmpty(t *testing.T) {
	tool := newContextSearchTool(t.TempDir(), "sess1")
	out, err := tool.Exec(context.Background(), json.RawMessage(`{"pattern":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no context archives") {
		t.Fatalf("got %q", out)
	}
}

// TestContextSearchGetByIDRoundTrip stores a tool result the way the context
// sink does (<sid>.tools/<NNNN>-<tool>-<hex>.txt) and recalls it.
func TestContextSearchGetByIDRoundTrip(t *testing.T) {
	root := t.TempDir()
	tdir := filepath.Join(root, "sess1.tools")
	if err := os.MkdirAll(tdir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("alpha-", 400) + "\nneedle-42\n" + strings.Repeat("omega-", 400)
	if err := os.WriteFile(filepath.Join(tdir, "0003-bash-ab12cd34.txt"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := newContextSearchTool(root, "sess1")
	out, err := tool.Exec(context.Background(), json.RawMessage(`{"id":"0003-bash-ab12cd34.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[stored id=0003-bash-ab12cd34.txt") {
		t.Fatalf("no stored header: %q", out)
	}
	if !strings.Contains(out, "needle-42") {
		t.Fatalf("body window missing needle: %q", out)
	}
	// Default window is bounded — the header must report a partial window, not
	// the full body length.
	if !strings.Contains(out, "chars=4000/4811") {
		t.Fatalf("window not bounded: %q", out)
	}
	if len(out) > contextSearchMaxOutput {
		t.Fatalf("output exceeds per-call cap: %d", len(out))
	}
}

func TestContextSearchGetByIDWindow(t *testing.T) {
	root := t.TempDir()
	tdir := filepath.Join(root, "sess1.tools")
	if err := os.MkdirAll(tdir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tdir, "0001-read-aabbccdd.txt"), []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := newContextSearchTool(root, "sess1")
	out, err := tool.Exec(context.Background(), json.RawMessage(`{"id":"0001-read-aabbccdd.txt","offset":3,"window":4}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "offset=3 chars=4/10") {
		t.Fatalf("bad window header: %q", out)
	}
	if !strings.Contains(out, "3456") {
		t.Fatalf("bad window body: %q", out)
	}
}

func TestContextSearchGetByIDInvalid(t *testing.T) {
	tool := newContextSearchTool(t.TempDir(), "sess1")
	_, err := tool.Exec(context.Background(), json.RawMessage(`{"id":"../../etc/passwd"}`))
	if err == nil || !strings.Contains(err.Error(), "invalid stored result id") {
		t.Fatalf("want invalid-id error, got %v", err)
	}
}

func TestContextSearchGetByIDMissing(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sess1.tools"), 0o700); err != nil {
		t.Fatal(err)
	}
	tool := newContextSearchTool(root, "sess1")
	_, err := tool.Exec(context.Background(), json.RawMessage(`{"id":"0001-bash-aabbccdd.txt"}`))
	if err == nil || !strings.Contains(err.Error(), "expired or missing") {
		t.Fatalf("want missing error, got %v", err)
	}
}

// TestContextSearchFindsStoredToolResult: pattern search covers .tools files
// and their snippet headers carry the "stored " prefix.
func TestContextSearchFindsStoredToolResult(t *testing.T) {
	root := t.TempDir()
	tdir := filepath.Join(root, "sess1.tools")
	if err := os.MkdirAll(tdir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tdir, "0002-bash-11223344.txt"), []byte("line one\nmarker-qwerty-77\nline three\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := newContextSearchTool(root, "sess1")
	out, err := tool.Exec(context.Background(), json.RawMessage(`{"pattern":"marker-qwerty-77"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "stored 0002-bash-11223344.txt") {
		t.Fatalf("no stored header: %q", out)
	}
	if !strings.Contains(out, "marker-qwerty-77") {
		t.Fatalf("miss: %q", out)
	}
}

func TestContextSearchPatternRequired(t *testing.T) {
	tool := newContextSearchTool(t.TempDir(), "sess1")
	if _, err := tool.Exec(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("empty args must error (no pattern, no id)")
	}
	if _, err := tool.Exec(context.Background(), json.RawMessage(`{"pattern":""}`)); err == nil {
		t.Fatal("empty pattern must error")
	}
	if _, err := tool.Exec(context.Background(), json.RawMessage(`{"pattern":[123]}`)); err == nil {
		t.Fatal("non-string pattern must error")
	}
}

func TestContextSearchPatternLimits(t *testing.T) {
	tool := newContextSearchTool(t.TempDir(), "sess1")
	var many []string
	for i := 0; i < contextSearchMaxPatterns+1; i++ {
		many = append(many, "p"+string(rune('a'+i)))
	}
	args, _ := json.Marshal(map[string]any{"pattern": many})
	if _, err := tool.Exec(context.Background(), args); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("want pattern-count error, got %v", err)
	}
	tooLong := strings.Repeat("x", contextSearchMaxPatternRunes+1)
	args2, _ := json.Marshal(map[string]any{"pattern": tooLong})
	if _, err := tool.Exec(context.Background(), args2); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("want pattern-length error, got %v", err)
	}
}

// TestContextSearchNoSessionCtx: the registered (unpinned) tool resolves the
// session from the engine in ctx; without an engine/session it must fail
// explicitly, never panic.
func TestContextSearchNoSessionCtx(t *testing.T) {
	tool := newContextSearchTool("", "")
	if _, err := tool.Exec(context.Background(), json.RawMessage(`{"pattern":"x"}`)); err == nil {
		t.Fatal("no engine in ctx must error")
	}
	eng, _ := runToolPrompt(t, strings.Repeat("a", 100), nil, nil)
	// NoSession engine: SessionDir() empty.
	nosess, err := mow.New(mow.Options{LoadUserConfig: true, Workspace: t.TempDir(), NoSession: true, Chat: func(context.Context, []mow.Message, []mow.ToolSpec) (mow.Message, error) {
		return mow.Message{Role: "assistant", Content: "ok"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if nosess.SessionDir() != "" {
		t.Fatalf("NoSession engine must have empty SessionDir, got %q", nosess.SessionDir())
	}
	_ = eng
}

// TestContextSearchResolvesSessionFromEngineCtx is the real integration path:
// the registered tool (dir "") recovers a stored result from the engine's
// active session, resolved via EngineFromContext at call time.
func TestContextSearchResolvesSessionFromEngineCtx(t *testing.T) {
	eng, _ := runToolPrompt(t, bigBlob(0), nil, nil)
	dir := filepath.Join(eng.SessionDir(), eng.SessionID()+".tools")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "line one\nneedle-ctx-77\nline three\n"
	if err := os.WriteFile(filepath.Join(dir, "0001-read-aabbccdd.txt"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := newContextSearchTool("", "")
	ctx := mow.ContextWithEngine(context.Background(), eng)

	out, err := tool.Exec(ctx, json.RawMessage(`{"id":"0001-read-aabbccdd.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "needle-ctx-77") {
		t.Fatalf("recall miss via engine ctx: %q", out)
	}

	out, err = tool.Exec(ctx, json.RawMessage(`{"pattern":"needle-ctx-77"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "stored 0001-read-aabbccdd.txt") {
		t.Fatalf("pattern search miss via engine ctx: %q", out)
	}
}

// TestContextSearchReadOnly: the tool must declare itself side-effect free so
// read-only prompts can use it.
func TestContextSearchReadOnly(t *testing.T) {
	tool := newContextSearchTool(t.TempDir(), "sess1")
	if !tool.ReadOnly() {
		t.Fatal("recall must be read-only")
	}
}

// TestContextSearchSessionIsolation: the session dir is shared by all
// sessions of a project; search, recall, and the retrieval budget must all
// be pinned to the tool's own session — never a sibling's.
func TestContextSearchSessionIsolation(t *testing.T) {
	resetBudgetRegistryForTest()
	t.Cleanup(resetBudgetRegistryForTest)
	base := t.TempDir()
	for _, sid := range []string{"sess1", "sess2"} {
		tdir := filepath.Join(base, sid+".tools")
		adir := filepath.Join(base, sid+".archive")
		if err := os.MkdirAll(tdir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(adir, 0o700); err != nil {
			t.Fatal(err)
		}
		// Colliding stored id and distinct markers per session.
		if err := os.WriteFile(filepath.Join(tdir, "0001-read-aabbccdd.txt"), []byte("stored marker-"+sid+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(adir, "0001-x.md"), []byte("archived marker-"+sid+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	tool := newContextSearchTool(base, "sess1")
	ctx := context.Background()

	out, err := tool.Exec(ctx, json.RawMessage(`{"id":"0001-read-aabbccdd.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "marker-sess1") || strings.Contains(out, "marker-sess2") {
		t.Fatalf("recall crossed sessions: %q", out)
	}

	out, err = tool.Exec(ctx, json.RawMessage(`{"pattern":"marker-sess2"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "marker-sess2") {
		t.Fatalf("pattern search crossed sessions: %q", out)
	}

	// Budget isolation: exhausting sess1 must not touch sess2.
	chargeBudgetForTest(base+"/sess1", contextSearchMaxRetrieved)
	out, err = tool.Exec(ctx, json.RawMessage(`{"pattern":"marker-sess1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "budget exhausted") {
		t.Fatalf("sess1 budget not enforced: %q", out)
	}
	tool2 := newContextSearchTool(base, "sess2")
	out, err = tool2.Exec(ctx, json.RawMessage(`{"pattern":"marker-sess2"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "marker-sess2") {
		t.Fatalf("sess2 budget affected by sess1: %q", out)
	}
}

// TestContextSearchGetByIDLargeStoredBody: stored bodies above the archive
// scan cap (1 MiB) must still be retrievable via windowed recall reads —
// a stub must never point at an unretrievable body.
func TestContextSearchGetByIDLargeStoredBody(t *testing.T) {
	root := t.TempDir()
	tdir := filepath.Join(root, "sess1.tools")
	if err := os.MkdirAll(tdir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("x", 2<<20) + "\nneedle-big-99\n" + strings.Repeat("y", 2<<20)
	if err := os.WriteFile(filepath.Join(tdir, "0001-bash-aabbccdd.txt"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := newContextSearchTool(root, "sess1")
	args, _ := json.Marshal(map[string]any{"id": "0001-bash-aabbccdd.txt", "offset": 2 << 20, "window": 4000})
	out, err := tool.Exec(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "needle-big-99") {
		t.Fatalf("windowed read of a >1 MiB stored body failed: %q", out[:200])
	}
	if len(out) > contextSearchMaxOutput {
		t.Fatalf("output exceeds per-call cap: %d", len(out))
	}
}

func TestContextSearchEmitsRecoveryEventMetadata(t *testing.T) {
	eng := testContextSinkEngine(t)
	id, err := eng.SaveToolResult("bash", "marker-recovery-body")
	if err != nil {
		t.Fatal(err)
	}
	var got mow.Event
	eng.AddOnEvent(func(ev mow.Event) {
		if ev.Type == mow.EventContextSinkRecover {
			got = ev
		}
	})
	tool := newContextSearchTool("", "")
	out, err := tool.Exec(mow.ContextWithEngine(context.Background(), eng), json.RawMessage(`{"id":"`+id+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != mow.EventContextSinkRecover || got.Tool != "recall" || got.StoredID != id {
		t.Fatalf("event identity = %#v", got)
	}
	if got.RecoveryMode != "id" || got.RecoveredBytes != len(out) {
		t.Fatalf("event metrics = %#v, output bytes = %d", got, len(out))
	}
	if got.Result != "" {
		t.Fatal("recovery event must not contain recovered body")
	}
}

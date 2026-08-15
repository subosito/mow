package contextsink

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext"
	"github.com/subosito/mow/testutil"
)

func TestMain(m *testing.M) { testutil.RunWithProvider(m) }

func testContextSinkEngine(t *testing.T) *mow.Engine {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "contextsink-enabled.yaml")
	if err := os.WriteFile(cfgPath, []byte("extensions:\n  contextsink:\n    enabled: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	eng, err := mow.New(mow.Options{LoadUserConfig: true,
		Workspace:   t.TempDir(),
		ConfigPaths: []string{cfgPath},
		Chat: func(context.Context, []mow.Message, []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return eng
}

// bigBlob returns an ASCII payload larger than defaultMaxInlineBytes.
func bigBlob(n int) string {
	if n <= 0 {
		n = defaultMaxInlineBytes + 1000
	}
	return strings.Repeat("data-line-", n/10) + strings.Repeat("x", n%10)
}

// sinkEchoTool returns a fixed body; ReadOnly so it passes the default
// read-only tool policy.
type sinkEchoTool struct{ body string }

func (sinkEchoTool) Name() string        { return "sink_echo" }
func (sinkEchoTool) Description() string { return "echo a fixed body" }
func (sinkEchoTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}
func (t sinkEchoTool) Exec(_ context.Context, _ json.RawMessage) (string, error) { return t.body, nil }
func (sinkEchoTool) ReadOnly() bool                                              { return true }

// runToolPrompt drives one two-step prompt: turn 1 asks the model to run
// sink_echo, turn 2 collects the tool message the model saw and finishes.
func runToolPrompt(t *testing.T, body string, configPaths []string, extra func(*mow.Engine)) (*mow.Engine, []string) {
	t.Helper()
	ext.Reset()
	t.Cleanup(ext.Reset)
	ext.RegisterTool(sinkEchoTool{body: body})
	// The pack registers its hook in init(); ext.Reset() wiped it, so
	// re-register to mirror the linked-binary setup.
	ext.RegisterPostTool(contextSinkHook)

	var toolResults []string
	step := 0
	opt := mow.Options{
		LoadUserConfig: true,
		Workspace:      t.TempDir(),
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			step++
			if step == 1 {
				return mow.Message{
					Role: "assistant",
					ToolCalls: []mow.ToolCall{{
						ID: "1", Type: "function",
						Function: mow.FunctionCall{Name: "sink_echo", Arguments: `{}`},
					}},
				}, nil
			}
			for _, m := range messages {
				if m.Role == "tool" {
					toolResults = append(toolResults, m.Content)
				}
			}
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	}
	if configPaths != nil {
		opt.ConfigPaths = configPaths
	} else {
		cfgPath := filepath.Join(t.TempDir(), "contextsink-enabled.yaml")
		if err := os.WriteFile(cfgPath, []byte("extensions:\n  contextsink:\n    enabled: true\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		opt.ConfigPaths = []string{cfgPath}
	}
	eng, err := mow.New(opt)
	if err != nil {
		t.Fatal(err)
	}
	if extra != nil {
		extra(eng)
	}
	if _, err := eng.Prompt(context.Background(), "run tool"); err != nil {
		t.Fatal(err)
	}
	return eng, toolResults
}

func storedIDFromStub(s string) string {
	const prefix = "[stored id="
	i := strings.Index(s, prefix)
	if i < 0 {
		return ""
	}
	rest := s[i+len(prefix):]
	j := strings.IndexAny(rest, " \t]")
	if j < 0 {
		return ""
	}
	return rest[:j]
}

func TestStoresAndStubs(t *testing.T) {
	body := bigBlob(0)
	eng, toolResults := runToolPrompt(t, body, nil, nil)
	if len(toolResults) != 1 {
		t.Fatalf("tool results=%d want 1", len(toolResults))
	}
	got := toolResults[0]
	if !strings.HasPrefix(got, "[stored id=") {
		t.Fatalf("want stored stub, got %q", got)
	}
	if strings.Contains(got, body) {
		t.Fatal("full body must not appear in stub")
	}
	if !strings.Contains(got, "use recall id=") {
		t.Fatalf("stub missing search hint: %q", got)
	}
	id := storedIDFromStub(got)
	if id == "" {
		t.Fatalf("could not parse id from stub: %q", got)
	}
	stored, err := eng.StoredToolResult(id)
	if err != nil {
		t.Fatal(err)
	}
	if stored != body {
		t.Fatalf("stored body mismatch: len=%d want %d", len(stored), len(body))
	}
}

func TestNoOpUnderThreshold(t *testing.T) {
	body := strings.Repeat("small-", 100) // well under the 8k cap
	_, toolResults := runToolPrompt(t, body, nil, nil)
	if len(toolResults) != 1 {
		t.Fatalf("tool results=%d want 1", len(toolResults))
	}
	if toolResults[0] != body {
		t.Fatalf("under-threshold result must stay inline, got %q", toolResults[0][:80])
	}
}

func TestDisabledConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mow.yaml")
	cfg := "extensions:\n  contextsink:\n    enabled: false\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	body := bigBlob(0)
	_, toolResults := runToolPrompt(t, body, []string{cfgPath}, nil)
	if len(toolResults) != 1 {
		t.Fatalf("tool results=%d want 1", len(toolResults))
	}
	if toolResults[0] != body {
		t.Fatal("disabled sink must not rewrite the result")
	}
}

func TestSessionlessNoOp(t *testing.T) {
	// No engine in ctx → no-op, no panic.
	dec, err := contextSinkHook(context.Background(), ext.PostToolEvent{
		Name: "read", Result: bigBlob(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Rewrite {
		t.Fatal("sessionless hook must not rewrite")
	}
}

func TestStoreFailureLeavesSmallResultsInline(t *testing.T) {
	// Store outage + result the loop would keep whole (≤ loopToolResultCap):
	// leave it inline — never shrink the model's view below the loop cap.
	body := bigBlob(0) // ~9k, over the 8k inline cap but under 24k
	eng, _ := runToolPrompt(t, strings.Repeat("a", 100), nil, nil)
	if _, err := eng.SaveToolResult("probe", "seed"); err != nil {
		t.Fatal(err)
	}
	dir := storedToolsDir(t, eng)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })

	dec, err := contextSinkHook(mow.ContextWithEngine(context.Background(), eng), ext.PostToolEvent{
		Name: "read", Result: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Rewrite {
		t.Fatal("store failure must leave loop-kept results inline")
	}
}

func TestStoreFailureFallbackLargeResult(t *testing.T) {
	// Store outage + result larger than the loop cap: explicit head/tail
	// marker, never a dangling stub or silent truncation.
	body := strings.Repeat("payload-", 5000) // ~40k > loopToolResultCap
	eng, _ := runToolPrompt(t, strings.Repeat("a", 100), nil, nil)
	if _, err := eng.SaveToolResult("probe", "seed"); err != nil {
		t.Fatal(err)
	}
	dir := storedToolsDir(t, eng)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })

	dec, err := contextSinkHook(mow.ContextWithEngine(context.Background(), eng), ext.PostToolEvent{
		Name: "read", Result: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Rewrite {
		t.Fatal("store failure on an oversized result must rewrite")
	}
	if !strings.Contains(dec.Result, "store unavailable") {
		t.Fatalf("fallback missing marker: %q", dec.Result[:120])
	}
	if strings.Contains(dec.Result, body) {
		t.Fatal("fallback must not inline the full body")
	}
}

func TestStoreRejectsOversizedBody(t *testing.T) {
	// Bodies over the store cap must be rejected so a stored id can never be
	// unretrievable; the sink then uses its fallback.
	eng, _ := runToolPrompt(t, strings.Repeat("a", 100), nil, nil)
	huge := strings.Repeat("x", contextSearchMaxStoredRead+1) // store cap + 1
	if _, err := eng.SaveToolResult("read", huge); err == nil {
		t.Fatal("oversized body must be rejected by the store")
	}
	// Via the hook: oversized + store-down-cap → fallback marker, not a stub.
	dec, err := contextSinkHook(mow.ContextWithEngine(context.Background(), eng), ext.PostToolEvent{
		Name: "read", Result: huge,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Rewrite || !strings.Contains(dec.Result, "store unavailable") {
		t.Fatalf("want fallback marker, got rewrite=%v result=%q", dec.Rewrite, dec.Result[:80])
	}
}

func TestContextSearchExemptFromSink(t *testing.T) {
	// Stubbing recall's own output would start a recover→store→stub
	// loop; recovery results must always pass through untouched.
	eng, _ := runToolPrompt(t, strings.Repeat("a", 100), nil, nil)
	dec, err := contextSinkHook(mow.ContextWithEngine(context.Background(), eng), ext.PostToolEvent{
		Name: "recall", Result: strings.Repeat("y", 20000),
	})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Rewrite {
		t.Fatal("recall results must never be stored+stubbed")
	}
}

// storedToolsDir finds the engine's active session .tools dir via the public
// SessionDir/SessionID accessors (engine internals are not importable from
// packs).
func storedToolsDir(t *testing.T, eng *mow.Engine) string {
	t.Helper()
	dir := eng.SessionDir()
	if dir == "" || eng.SessionID() == "" {
		t.Fatal("no active session found")
	}
	dir = filepath.Join(dir, eng.SessionID()+".tools")
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSinkIsPlainExtHookAndEventCarriesFullBody(t *testing.T) {
	// The sink is an ordinary ext PostTool hook: a hook registered after it
	// sees the stub (what the model sees), while the engine's event emitter —
	// which runs before all hooks — still delivers the full body to hosts.
	ext.Reset()
	t.Cleanup(ext.Reset)
	body := bigBlob(0)
	ext.RegisterTool(sinkEchoTool{body: body})
	ext.RegisterPostTool(contextSinkHook)
	var afterSinkSaw string
	ext.RegisterPostTool(func(ctx context.Context, ev ext.PostToolEvent) (ext.PostToolDecision, error) {
		afterSinkSaw = ev.Result
		return ext.PostToolDecision{}, nil
	})

	var toolResults []string
	var eventResult string
	step := 0
	cfgPath := filepath.Join(t.TempDir(), "contextsink-enabled.yaml")
	if err := os.WriteFile(cfgPath, []byte("extensions:\n  contextsink:\n    enabled: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	eng, err := mow.New(mow.Options{LoadUserConfig: true,
		ConfigPaths: []string{cfgPath},
		Workspace:   t.TempDir(),
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			step++
			if step == 1 {
				return mow.Message{
					Role: "assistant",
					ToolCalls: []mow.ToolCall{{
						ID: "1", Type: "function",
						Function: mow.FunctionCall{Name: "sink_echo", Arguments: `{}`},
					}},
				}, nil
			}
			for _, m := range messages {
				if m.Role == "tool" {
					toolResults = append(toolResults, m.Content)
				}
			}
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	eng.AddOnEvent(func(ev mow.Event) {
		if ev.Type == mow.EventToolEnd {
			eventResult = ev.Result
		}
	})
	if _, err := eng.Prompt(context.Background(), "run tool"); err != nil {
		t.Fatal(err)
	}

	if len(toolResults) != 1 || !strings.HasPrefix(toolResults[0], "[stored id=") {
		t.Fatalf("history must carry the stub, got %q", toolResults)
	}
	if afterSinkSaw != toolResults[0] {
		t.Fatal("a hook registered after the sink must see the stub (what the model sees)")
	}
	// EventToolEnd carries the full body (truncated at 4000 for the event).
	if !strings.HasPrefix(eventResult, body[:4000]) || !strings.Contains(eventResult, "…(truncated)") {
		t.Fatalf("event bus must carry the full body, got %q", eventResult[:80])
	}
}

func TestStoredResultsAreSessionScoped(t *testing.T) {
	// Two engines = two sessions: a stored id from one must not resolve in
	// the other (no cross-session contamination).
	eng1, _ := runToolPrompt(t, bigBlob(0), nil, nil)
	eng2, _ := runToolPrompt(t, strings.Repeat("b", 100), nil, nil)

	id, err := eng1.SaveToolResult("probe", "eng1-only-body")
	if err != nil {
		t.Fatal(err)
	}
	got, err := eng2.StoredToolResult(id)
	if err == nil {
		t.Fatalf("cross-session read must fail, got %q", got)
	}
	if !strings.Contains(err.Error(), "expired or missing") {
		t.Fatalf("want missing error, got %v", err)
	}
}

func TestFormatStubBounded(t *testing.T) {
	body := bigBlob(defaultMaxInlineBytes + 5000)
	stub := formatStub("0003-bash-ab12cd34.txt", "bash", body)
	if strings.Contains(stub, body) {
		t.Fatal("stub must not embed the body")
	}
	if len(stub) > 1200 {
		t.Fatalf("stub too large: %d", len(stub))
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg := loadConfig(nil)
	if cfg.Enabled || cfg.MaxInlineBytes != defaultMaxInlineBytes {
		t.Fatalf("defaults wrong: %+v", cfg)
	}
}

func TestSinkEmitsStoreEventMetadata(t *testing.T) {
	eng := testContextSinkEngine(t)
	var got mow.Event
	eng.AddOnEvent(func(ev mow.Event) {
		if ev.Type == mow.EventContextSinkStore {
			got = ev
		}
	})
	body := strings.Repeat("large result ", 1000)
	dec, err := contextSinkHook(mow.ContextWithEngine(context.Background(), eng), ext.PostToolEvent{
		Name: "bash", ToolCallID: "call-1", Result: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Rewrite {
		t.Fatal("expected stored result rewrite")
	}
	if got.Type != mow.EventContextSinkStore || got.Tool != "bash" || got.ToolCallID != "call-1" {
		t.Fatalf("event identity = %#v", got)
	}
	if got.StoredID == "" || got.OriginalBytes != len(body) || got.InlineBytes != len(dec.Result) {
		t.Fatalf("event metrics = %#v, inline result bytes = %d", got, len(dec.Result))
	}
	if got.Result != "" {
		t.Fatal("store event must not contain result body")
	}
}

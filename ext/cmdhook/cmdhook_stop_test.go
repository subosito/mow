package cmdhook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subosito/mow/ext"
)

// A Stop hook that blocks (with a reason) or adds context keeps the agent
// going; a silent Stop hook lets it finish. Wired through ext's
// before-finish registry exactly as the engine consumes it.
func TestStopHookContinuesViaBeforeFinish(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string // substring of Continue; "" = finish
	}{
		{"block with reason", `echo '{"decision":"block","reason":"run the test suite first"}'`, "run the test suite first"},
		{"additional context", `echo '{"hookSpecificOutput":{"hookEventName":"Stop","additionalContext":"also update the changelog"}}'`, "also update the changelog"},
		{"exit 2 stderr", `echo "tests are red" >&2; exit 2`, "tests are red"},
		{"silent", `exit 0`, ""},
		{"plain stdout is not feedback", `echo "just logging"`, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resetHooks(t)
			root := t.TempDir()
			stop := hookScript(t, root, "stop.sh", tc.body)
			writeHooksJSON(t, root, `{"hooks":{"Stop":[{"matcher":"","hooks":[{"type":"command","command":"`+stop+`"}]}]}}`)
			mustLoad(t, PluginConfig{Root: root}).register()
			hooks := ext.BeforeFinishHooksForEngine(true)
			if len(hooks) != 1 {
				t.Fatalf("before-finish hooks = %d", len(hooks))
			}
			d, err := hooks[0](t.Context(), ext.BeforeFinishEvent{AssistantText: "all done"})
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" && d.Continue != "" {
				t.Fatalf("want finish, got Continue=%q", d.Continue)
			}
			if tc.want != "" && !strings.Contains(d.Continue, tc.want) {
				t.Fatalf("Continue=%q want %q", d.Continue, tc.want)
			}
		})
	}
}

// The Stop payload carries the final answer and stop_hook_active.
func TestStopPayloadCarriesLastMessage(t *testing.T) {
	resetHooks(t)
	root := t.TempDir()
	captured := filepath.Join(root, "in.json")
	stop := hookScript(t, root, "stop.sh", "cat > "+captured)
	writeHooksJSON(t, root, `{"hooks":{"Stop":[{"matcher":"","hooks":[{"type":"command","command":"`+stop+`"}]}]}}`)
	mustLoad(t, PluginConfig{Root: root}).register()
	if _, err := ext.BeforeFinishHooksForEngine(true)[0](t.Context(),
		ext.BeforeFinishEvent{AssistantText: "final words", Continuations: 2}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(captured)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Last   string `json:"last_assistant_message"`
		Active bool   `json:"stop_hook_active"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Last != "final words" || !got.Active {
		t.Fatalf("payload: %+v", got)
	}
}

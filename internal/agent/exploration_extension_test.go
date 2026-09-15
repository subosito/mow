package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/subosito/mow/internal/llm"
)

type exploratoryFixture struct {
	name                  string
	readOnly, exploratory bool
	runs                  int
}

func (t *exploratoryFixture) Name() string              { return t.name }
func (*exploratoryFixture) Description() string         { return "fixture" }
func (*exploratoryFixture) Parameters() json.RawMessage { return json.RawMessage(`{}`) }
func (t *exploratoryFixture) ReadOnly() bool            { return t.readOnly }
func (t *exploratoryFixture) Exploratory() bool         { return t.exploratory }
func (*exploratoryFixture) Untrusted() bool             { return true }
func (t *exploratoryFixture) Exec(context.Context, json.RawMessage) (string, error) {
	t.runs++
	return fmt.Sprintf("evidence %d", t.runs), nil
}

func TestExplorationExtensionResults(t *testing.T) {
	for _, ro := range []bool{true, false} {
		tool := &exploratoryFixture{name: "mcp_index_search", readOnly: ro, exploratory: !ro}
		g := explorationGuard{tools: map[string]Tool{tool.Name(): tool}, nonce: "real"}
		call := exploreCall(tool.Name(), `{}`)
		for i := range 100 {
			g.note([]llm.ToolCall{call}, exploreResult(WrapUntrusted("real", tool.Name(), fmt.Sprintf("evidence %d", i))))
		}
	}
}

func TestExplorationFramedMissesAndErrors(t *testing.T) {
	tool := &exploratoryFixture{name: "mcp_index_search", exploratory: true}
	g := explorationGuard{tools: map[string]Tool{tool.Name(): tool}, nonce: "real"}
	call := exploreCall(tool.Name(), `{}`)
	for _, body := range []string{`{"results":[],"query":"one"}`, `{"results":[],"query":"two"}`} {
		g.note([]llm.ToolCall{call}, exploreResult(WrapUntrusted("real", tool.Name(), body)))
	}
	if !g.warned {
		t.Fatal("framed changing misses escaped checkpoint")
	}
	mutation := &exploratoryFixture{name: "mcp_index_update"}
	g.tools[mutation.Name()] = mutation
	g.note([]llm.ToolCall{exploreCall(mutation.Name(), `{}`)}, exploreResult(WrapUntrusted("real", mutation.Name(), "error: update failed")))
	if !g.warned {
		t.Fatal("framed failed mutation reset guard")
	}
	g.note([]llm.ToolCall{exploreCall(mutation.Name(), `{}`)}, exploreResult(WrapUntrusted("real", mutation.Name(), "updated")))
	if g.warned || g.rounds != 0 {
		t.Fatal("successful mutation did not reset guard")
	}
}

func TestExplorationMixedNativeAndExtensionBatch(t *testing.T) {
	tool := &exploratoryFixture{name: "mcp_index_search", exploratory: true}
	g := explorationGuard{tools: map[string]Tool{tool.Name(): tool}}
	a := exploreCall("grep", `{}`)
	a.ID = "native"
	b := exploreCall(tool.Name(), `{}`)
	b.ID = "mcp"
	for range 2 {
		g.note([]llm.ToolCall{a, b}, []llm.Message{{ToolCallID: "native", Content: "(no matches)"}, {ToolCallID: "mcp", Content: WrapUntrusted("", tool.Name(), "error: missing index")}})
	}
	if !g.warned || g.rounds != 2 {
		t.Fatal("MCP call reset mixed batch")
	}
	// Identical framed successful results must count as repeated evidence.
	g = explorationGuard{tools: map[string]Tool{tool.Name(): tool}}
	warned := false
	for range 8 {
		hint := g.note([]llm.ToolCall{b}, []llm.Message{{ToolCallID: "mcp", Content: WrapUntrusted("", tool.Name(), "same evidence")}})
		warned = warned || strings.Contains(hint, "checkpoint")
	}
	if !warned {
		t.Fatal("missing advisory checkpoint")
	}
}

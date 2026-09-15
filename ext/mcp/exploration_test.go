package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/subosito/mow"
	"strings"
	"testing"
)

func TestMCPExplorationClassification(t *testing.T) {
	for _, tc := range []struct {
		name     string
		ro, want bool
	}{
		{"zvec_grep_search", false, true}, {"search", false, true}, {"searchCode", false, true}, {"list_files", false, true}, {"read_file", false, true}, {"lookup", false, true},
		{"opaque", true, true}, {"opaque", false, false}, {"write_file", false, false}, {"delete_search_index", false, false}, {"search_and_replace", false, false}, {"execute_query", false, false},
	} {
		tool := &mcpTool{prefix: "search", name: tc.name, readOnly: tc.ro}
		if got := tool.Exploratory(); got != tc.want {
			t.Errorf("%s: %v want %v", tc.name, got, tc.want)
		}
		if tool.ReadOnly() != tc.ro {
			t.Fatal("classification changed permission")
		}
	}
}

type searchEvidenceTransport struct{ calls int }

func (*searchEvidenceTransport) listTools(context.Context) ([]toolInfo, error) { return nil, nil }
func (*searchEvidenceTransport) Close() error                                  { return nil }
func (p *searchEvidenceTransport) callTool(context.Context, string, json.RawMessage) (string, error) {
	p.calls++
	return fmt.Sprintf("fresh evidence %d", p.calls), nil
}

func TestMCPSearchContinuesWithFreshEvidenceThroughEngine(t *testing.T) {
	setupMCPTest(t)
	transport := &searchEvidenceTransport{}
	tool := &mcpTool{prefix: "fixture", name: "code_search", client: transport, readOnly: false}
	calls := 0
	warned := false
	eng, err := mow.New(mow.Options{Workspace: t.TempDir(), NoSession: true, MaxTurns: 110, Tools: []mow.Tool{tool}, Chat: func(ctx context.Context, ms []mow.Message, specs []mow.ToolSpec) (mow.Message, error) {
		calls++
		for _, m := range ms {
			if strings.Contains(m.Content, "Investigation checkpoint:") {
				warned = true
			}
		}
		if len(specs) == 0 {
			t.Fatal("fresh evidence exhausted investigation tools")
		}
		if calls == 101 {
			return mow.Message{Role: "assistant", Content: "Findings collected; cause remains unconfirmed."}, nil
		}
		return mow.Message{Role: "assistant", ToolCalls: []mow.ToolCall{{ID: fmt.Sprintf("call-%d", calls), Type: "function", Function: mow.FunctionCall{Name: tool.Name(), Arguments: fmt.Sprintf(`{"query":"pass-%d"}`, calls)}}}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	res, err := eng.Prompt(context.Background(), "investigate")
	if err != nil || !warned || !strings.Contains(res.Text, "Findings collected") {
		t.Fatalf("missing successful final answer: calls=%d result=%+v err=%v", calls, res, err)
	}
	if transport.calls != 100 || calls != 101 {
		t.Fatalf("search reset exploration: tool calls=%d model calls=%d", transport.calls, calls)
	}
}

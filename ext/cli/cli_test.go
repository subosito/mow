package cli

import (
	"testing"

	"github.com/subosito/mow/ext"
)

func TestPreferFirst(t *testing.T) {
	cmds := []ext.Command{
		{Name: "goal"}, {Name: "job"}, {Name: "mcp"}, {Name: "ops"},
	}
	got := preferFirst(cmds, "mcp")
	if len(got) != 4 || got[0].Name != "mcp" || got[1].Name != "goal" || got[3].Name != "ops" {
		t.Fatalf("%v", names(got))
	}
	if got := preferFirst(cmds[:2], "mcp"); len(got) != 2 || got[0].Name != "goal" {
		t.Fatalf("missing name: %v", names(got))
	}
}

func names(cmds []ext.Command) []string {
	out := make([]string, len(cmds))
	for i, c := range cmds {
		out[i] = c.Name
	}
	return out
}

func TestPackEnvHelpOpsOnlyWhenLinked(t *testing.T) {
	if got := packEnvHelp(); got != "" {
		t.Fatalf("lean host listed pack env:\n%s", got)
	}
	ext.RegisterCommand(ext.Command{
		Name:  "ops",
		Layer: "pack",
		Run:   func([]string) int { return 0 },
	})
	if got := packEnvHelp(); got != "  MOW_OPS" {
		t.Fatalf("ops-linked host: %q", got)
	}
}

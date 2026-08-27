package cli

import (
	"testing"

	"github.com/subosito/mow/ext"
)

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

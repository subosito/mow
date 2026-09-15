package goal

import (
	"context"
	"strings"
	"testing"

	"github.com/subosito/mow/slash"
)

func TestGoalSlashIsRegistered(t *testing.T) {
	c, ok := slash.Lookup("goal")
	if !ok {
		t.Fatal("goal slash command is not registered")
	}
	if !c.Exclusive {
		t.Fatal("goal slash command must be exclusive")
	}
	if strings.TrimSpace(c.Summary) == "" || strings.TrimSpace(c.Usage) == "" {
		t.Fatal("goal slash command needs summary and usage")
	}
}

func TestGoalSlashHelpNeedsNoEngine(t *testing.T) {
	c, ok := slash.Lookup("goal")
	if !ok {
		t.Fatal("goal slash command is not registered")
	}
	res, err := c.Run(context.Background(), slash.Request{
		Name: "goal", Invoked: "goal", Args: []string{"help"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Body, "/goal run") {
		t.Fatalf("usage missing run form: %s", res.Body)
	}
}

func TestGoalSlashListUsesSharedStore(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	c, _ := slash.Lookup("goal")
	res, err := c.Run(context.Background(), slash.Request{Name: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "goal · (none)" || !strings.Contains(res.Body, "/goal new") {
		t.Fatalf("empty list result = %+v", res)
	}
}

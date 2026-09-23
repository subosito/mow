package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/subosito/mow"
	"github.com/subosito/mow/slash"
)

func TestWorkflowSlashRegistrationAndList(t *testing.T) {
	c, ok := slash.Lookup("workflow")
	if !ok || !c.Exclusive || !strings.Contains(c.Usage, "/workflow run") {
		t.Fatalf("command=%+v ok=%v", c, ok)
	}
	t.Setenv("MOW_HOME", t.TempDir())
	res, err := c.Run(t.Context(), slash.Request{Name: "workflow", Args: []string{"list"}})
	if err != nil || res.Title != "workflow · (none)" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}
func TestWorkflowSlashRunUsesCurrentEngine(t *testing.T) {
	name := "slash-workflow"
	_ = Register(Definition{Name: name, Version: "1", Phases: []Phase{VerifyPhase("verify", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	})}})
	home := t.TempDir()
	t.Setenv("MOW_HOME", home)
	eng, err := mow.New(mow.Options{Workspace: t.TempDir(), NoSession: true, Chat: func(context.Context, []mow.Message, []mow.ToolSpec) (mow.Message, error) { return mow.Message{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	c, _ := slash.Lookup("workflow")
	res, err := c.Run(t.Context(), slash.Request{Name: "workflow", Args: []string{"run", name, "1", "slash-run", `{"x":1}`}, Engine: eng})
	if err != nil || !strings.Contains(res.Body, "succeeded") {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

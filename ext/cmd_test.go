package ext_test

import (
	"testing"

	"github.com/subosito/mow/ext"
)

func TestRegisterCommandLookup(t *testing.T) {
	ext.Reset()
	t.Cleanup(ext.Reset)

	ext.RegisterCommand(ext.Command{
		Name:    "demo",
		Summary: "demo cmd",
		Run:     func(args []string) int { return 7 },
	})
	c, ok := ext.LookupCommand("demo")
	if !ok || c.Summary != "demo cmd" {
		t.Fatalf("%+v", c)
	}
	if code := c.Run(nil); code != 7 {
		t.Fatalf("code=%d", code)
	}
	if _, ok := ext.LookupCommand("missing"); ok {
		t.Fatal("expected miss")
	}
}

func TestDefaultInteractive(t *testing.T) {
	ext.Reset()
	t.Cleanup(ext.Reset)

	ext.RegisterCommand(ext.Command{Name: "a", Run: func([]string) int { return 0 }})
	ext.RegisterCommand(ext.Command{Name: "b", DefaultInteractive: true, Run: func([]string) int { return 0 }})
	c, ok := ext.DefaultInteractiveCommand()
	if !ok || c.Name != "b" {
		t.Fatalf("%+v", c)
	}
}

func TestBeforeNew(t *testing.T) {
	ext.Reset()
	t.Cleanup(ext.Reset)

	var saw []string
	ext.RegisterBeforeNew(func(paths ...string) error {
		saw = append(saw, paths...)
		return nil
	})
	if err := ext.BeforeNew("a.yaml"); err != nil {
		t.Fatal(err)
	}
	if len(saw) != 1 || saw[0] != "a.yaml" {
		t.Fatalf("%v", saw)
	}
}

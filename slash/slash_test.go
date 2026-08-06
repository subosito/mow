package slash

import (
	"context"
	"testing"
)

func reset() {
	mu.Lock()
	defer mu.Unlock()
	commands = nil
}

func noop(context.Context, Request) (Result, error) { return Result{}, nil }

func TestRegisterAndLookup(t *testing.T) {
	reset()
	Register(Command{Name: "review", Aliases: []string{"rv"}, Run: noop})

	// The token a user types varies: leading slash, stray case, surrounding
	// space from a paste. All must reach the same command.
	for _, tok := range []string{"review", "/review", "/REVIEW", "  /review  ", "rv", "/rv"} {
		if _, ok := Lookup(tok); !ok {
			t.Errorf("Lookup(%q) = miss, want hit", tok)
		}
	}
	if _, ok := Lookup("nope"); ok {
		t.Error("Lookup(nope) = hit, want miss")
	}
	if _, ok := Lookup(""); ok {
		t.Error("Lookup(empty) = hit, want miss")
	}
}

func TestRegisterReplacesByName(t *testing.T) {
	reset()
	Register(Command{Name: "review", Summary: "first", Run: noop})
	Register(Command{Name: "review", Summary: "second", Run: noop})

	// A pack linked through two module paths must not stack duplicates: the
	// help listing would show the command twice.
	if got := len(Commands()); got != 1 {
		t.Fatalf("Commands() = %d, want 1", got)
	}
	c, _ := Lookup("review")
	if c.Summary != "second" {
		t.Errorf("Summary = %q, want the later registration", c.Summary)
	}
}

func TestRegisterIgnoresIncomplete(t *testing.T) {
	reset()
	// A command with no Run would panic at dispatch, far from this mistake.
	Register(Command{Name: "broken"})
	Register(Command{Run: noop})
	Register(Command{Name: "  /  ", Run: noop})
	if got := len(Commands()); got != 0 {
		t.Fatalf("Commands() = %d, want 0", got)
	}
}

func TestCommandsSorted(t *testing.T) {
	reset()
	Register(Command{Name: "sec", Run: noop})
	Register(Command{Name: "review", Run: noop})
	got := Commands()
	if got[0].Name != "review" || got[1].Name != "sec" {
		t.Errorf("Commands() = %v, want sorted by name", []string{got[0].Name, got[1].Name})
	}
}

func TestHelpLines(t *testing.T) {
	reset()
	Register(Command{Name: "review", Aliases: []string{"rv"}, Summary: "code review", Run: noop})
	lines := HelpLines()
	if len(lines) != 1 {
		t.Fatalf("HelpLines() = %d lines, want 1", len(lines))
	}
	want := "/review (/rv) — code review"
	if lines[0] != want {
		t.Errorf("HelpLines()[0] = %q, want %q", lines[0], want)
	}
}

func TestNamesIncludesAliases(t *testing.T) {
	reset()
	Register(Command{Name: "review", Aliases: []string{"rv"}, Run: noop})
	got := Names()
	if len(got) != 2 || got[0] != "review" || got[1] != "rv" {
		t.Errorf("Names() = %v, want [review rv]", got)
	}
}

func TestIsHelpArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{"bare help", []string{"help"}, true},
		{"dash h", []string{"-h"}, true},
		{"double dash help", []string{"--help"}, true},
		{"question", []string{"?"}, true},
		{"no args runs default scope", nil, false},
		{"flag is not help", []string{"--staged"}, false},
		// A path that merely contains "help" must still be reviewed: treating
		// it as a help request would print usage and review nothing.
		{"path named help", []string{"help.go"}, false},
		{"help plus path", []string{"help", "./pkg"}, false},
		{"path after flag", []string{"--staged", "help"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsHelpArgs(tt.args); got != tt.want {
				t.Errorf("IsHelpArgs(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

func TestLookupIsolatesRegistrations(t *testing.T) {
	reset()
	// The registry is the mechanism behind "pack linked → command present".
	// With nothing registered, every token must miss, which is what makes a
	// dropped blank import remove the command from every host.
	if _, ok := Lookup("review"); ok {
		t.Error("Lookup(review) = hit with empty registry, want miss")
	}
	if got := len(HelpLines()); got != 0 {
		t.Errorf("HelpLines() = %d, want 0 with empty registry", got)
	}
}

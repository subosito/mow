package cliutil

import (
	"flag"
	"strings"
	"testing"
)

func TestCapabilityDisableFlags(t *testing.T) {
	var f EngineFlags
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	f.Bind(fs)
	if err := fs.Parse([]string{"--disallow-shell", "--disallow-write"}); err != nil {
		t.Fatal(err)
	}
	opt := f.Options()
	if !opt.DisableShell || !opt.DisableWrite {
		t.Fatalf("disable flags not mapped: shell=%v write=%v", opt.DisableShell, opt.DisableWrite)
	}
}

func TestReadOnlyDisablesBothCapabilities(t *testing.T) {
	var f EngineFlags
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	f.Bind(fs)
	if err := fs.Parse([]string{"--read-only"}); err != nil {
		t.Fatal(err)
	}
	opt := f.Options()
	if !opt.DisableShell || !opt.DisableWrite {
		t.Fatalf("read-only not mapped: shell=%v write=%v", opt.DisableShell, opt.DisableWrite)
	}
}

func TestCapabilityFlagConflicts(t *testing.T) {
	for _, args := range [][]string{
		{"--allow-shell", "--disallow-shell"},
		{"--allow-write", "--disallow-write"},
		{"--allow-shell", "--read-only"},
		{"--allow-write", "--read-only"},
	} {
		var f EngineFlags
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		f.Bind(fs)
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		if err := f.Validate(); err == nil || !strings.Contains(err.Error(), "conflicts") {
			t.Fatalf("Validate(%v) = %v, want conflict", args, err)
		}
	}
}

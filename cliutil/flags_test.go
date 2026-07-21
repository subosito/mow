package cliutil_test

import (
	"bytes"
	"flag"
	"strings"
	"testing"

	"github.com/subosito/mow/cliutil"
)

func TestPrintDefaultsLongDash(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	var buf bytes.Buffer
	fs.SetOutput(&buf)
	fs.String("config", "", "config path")
	fs.Bool("stream", false, "stream tokens")
	fs.String("p", "", "short prompt")
	cliutil.PrintDefaults(fs)
	out := buf.String()
	if !strings.Contains(out, "--config") {
		t.Fatalf("want --config in help:\n%s", out)
	}
	if !strings.Contains(out, "--stream") {
		t.Fatalf("want --stream in help:\n%s", out)
	}
	if !strings.Contains(out, "  -p") {
		t.Fatalf("want -p for short flag:\n%s", out)
	}
	if strings.Contains(out, "  --p") {
		t.Fatalf("short flag must not use --:\n%s", out)
	}
	if strings.Contains(out, "  -config") {
		t.Fatalf("long flag should not use single-dash form in help:\n%s", out)
	}
}

func TestNewFlagSetParsesDoubleDash(t *testing.T) {
	fs := cliutil.NewFlagSet("run")
	cfg := fs.String("config", "", "")
	if err := fs.Parse([]string{"--config", "/tmp/x.yaml"}); err != nil {
		t.Fatal(err)
	}
	if *cfg != "/tmp/x.yaml" {
		t.Fatalf("config=%q", *cfg)
	}
}

func TestMaxTurnsZeroMeansUnlimited(t *testing.T) {
	var ef cliutil.EngineFlags
	fs := cliutil.NewFlagSet("run")
	ef.Bind(fs)
	if err := fs.Parse([]string{"--max-turns", "0"}); err != nil {
		t.Fatal(err)
	}
	if !ef.MaxTurnsSet || ef.MaxTurns != 0 {
		t.Fatalf("MaxTurnsSet=%v MaxTurns=%d", ef.MaxTurnsSet, ef.MaxTurns)
	}
	opt := ef.Options()
	if opt.MaxTurns != -1 {
		t.Fatalf("Options.MaxTurns=%d want -1 (unlimited sentinel)", opt.MaxTurns)
	}
}

func TestMaxTurnsOmittedLeavesConfig(t *testing.T) {
	var ef cliutil.EngineFlags
	fs := cliutil.NewFlagSet("run")
	ef.Bind(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if ef.MaxTurnsSet {
		t.Fatal("MaxTurnsSet should be false when flag omitted")
	}
	opt := ef.Options()
	if opt.MaxTurns != 0 {
		t.Fatalf("Options.MaxTurns=%d want 0 (leave config)", opt.MaxTurns)
	}
}

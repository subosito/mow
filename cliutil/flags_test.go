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

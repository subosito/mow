package tty

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"github.com/subosito/mow"
)

func TestRendererDoesNotRepeatStream(t *testing.T) {
	var out, progress bytes.Buffer
	r := newRenderer(&out, &progress, false, false, false)
	r.begin()
	r.token("Hello ")
	r.token("world")
	r.finish("Hello world")
	if got := out.String(); got != "Hello world\n" {
		t.Fatalf("output = %q", got)
	}
	if progress.Len() != 0 {
		t.Fatalf("redirected progress = %q", progress.String())
	}
}

func TestRendererNonStreamingFinalAfterTool(t *testing.T) {
	var out bytes.Buffer
	r := newRenderer(&out, &out, false, false, false)
	r.begin()
	r.token("Checking.\n")
	r.event(mow.Event{Type: mow.EventToolStart, Tool: "read"})
	r.finish("Done.")
	if got := out.String(); got != "Checking.\nDone.\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestRendererSerializesConcurrentEvents(t *testing.T) {
	var out bytes.Buffer
	r := newRenderer(&out, &out, false, false, true)
	r.begin()
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			r.event(mow.Event{Type: mow.EventToolStart, Tool: "read"})
			r.token("x")
		})
	}
	wg.Wait()
	r.finish("")
	if r.tools != 20 || strings.Count(out.String(), "read") != 20 {
		t.Fatalf("lost progress: %s", out.String())
	}
}

func TestRendererStripsTerminalControls(t *testing.T) {
	var out bytes.Buffer
	r := newRenderer(&out, &out, false, false, false)
	r.begin()
	r.token("text\x1b[2J\r\x07\u009b")
	r.finish("")
	if strings.ContainsAny(out.String(), "\x1b\r\x07\u009b") {
		t.Fatalf("unsafe output: %q", out.String())
	}
}

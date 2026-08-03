package cliutil_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/subosito/mow"
	"github.com/subosito/mow/cliutil"
)

// captureStderr swaps os.Stderr for a pipe while fn runs and returns what was
// written. Not parallel-safe: tests using it must not call t.Parallel().
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w

	var (
		wg  sync.WaitGroup
		buf bytes.Buffer
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(&buf, r)
	}()

	func() {
		defer func() {
			os.Stderr = old
			_ = w.Close()
		}()
		fn()
	}()
	wg.Wait()
	_ = r.Close()
	return buf.String()
}

func TestToolProgressStartLine(t *testing.T) {
	cases := []struct {
		name   string
		stream bool
		ev     mow.Event
		want   []string
		absent []string
	}{
		{
			name: "read start",
			ev:   mow.Event{Type: mow.EventToolStart, Tool: "read", Args: json.RawMessage(`{"path":"engine.go"}`)},
			want: []string{"→ read engine.go\n"},
		},
		{
			name:   "stream inserts blank line first",
			stream: true,
			ev:     mow.Event{Type: mow.EventToolStart, Tool: "glob", Args: json.RawMessage(`{"pattern":"**/*.go"}`)},
			want:   []string{"\n→ glob **/*.go\n"},
		},
		{
			name: "no args still names tool",
			ev:   mow.Event{Type: mow.EventToolStart, Tool: "bash"},
			want: []string{"→ bash\n"},
		},
		{
			name: "unknown tool name empty",
			ev:   mow.Event{Type: mow.EventToolStart},
			want: []string{"→ ?\n"},
		},
		{
			name:   "unrelated event silent",
			ev:     mow.Event{Type: mow.EventRunStart},
			absent: []string{"→", "✓", "✗"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := captureStderr(t, func() {
				cliutil.ToolProgressOnEvent(c.stream)(c.ev)
			})
			for _, w := range c.want {
				if !strings.Contains(out, w) {
					t.Fatalf("out=%q want substring %q", out, w)
				}
			}
			for _, a := range c.absent {
				if strings.Contains(out, a) {
					t.Fatalf("out=%q must not contain %q", out, a)
				}
			}
		})
	}
}

func TestToolProgressEndVariants(t *testing.T) {
	args := json.RawMessage(`{"path":"a.go"}`)
	cases := []struct {
		name   string
		ev     mow.Event
		want   string
		silent bool
	}{
		{
			name:   "fast success silent",
			ev:     mow.Event{Type: mow.EventToolEnd, Tool: "read", Args: args, DurationMs: 2000},
			silent: true,
		},
		{
			name: "slow success reports duration",
			ev:   mow.Event{Type: mow.EventToolEnd, Tool: "read", Args: args, DurationMs: 2001},
			want: "✓ read a.go (2.0s)",
		},
		{
			name: "denied without error message",
			ev:   mow.Event{Type: mow.EventToolEnd, Tool: "write", Args: args, Denied: true},
			want: "✗ write a.go: denied",
		},
		{
			name: "denied with error message keeps error",
			ev:   mow.Event{Type: mow.EventToolEnd, Tool: "write", Args: args, Denied: true, Error: "policy: write disabled"},
			want: "✗ write a.go: policy: write disabled",
		},
		{
			name: "error wins over duration",
			ev:   mow.Event{Type: mow.EventToolEnd, Tool: "bash", Args: json.RawMessage(`{"command":"false"}`), DurationMs: 9000, Error: "exit 1"},
			want: "✗ bash false: exit 1",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := captureStderr(t, func() {
				cliutil.ToolProgressOnEvent(false)(c.ev)
			})
			if c.silent {
				if out != "" {
					t.Fatalf("want no output, got %q", out)
				}
				return
			}
			if !strings.Contains(out, c.want) {
				t.Fatalf("out=%q want substring %q", out, c.want)
			}
		})
	}
}

func TestToolProgressDelegateProgress(t *testing.T) {
	cases := []struct {
		name   string
		stream bool
		ev     mow.Event
		want   string
		silent bool
	}{
		{
			name: "named agent",
			ev:   mow.Event{Type: mow.EventDelegateProgress, Agent: "peer-agent", Delta: "reading files"},
			want: "  ↳ peer-agent: reading files\n",
		},
		{
			name: "blank agent falls back to peer",
			ev:   mow.Event{Type: mow.EventDelegateProgress, Agent: "  ", Delta: "thinking"},
			want: "  ↳ peer: thinking\n",
		},
		{
			name:   "empty delta silent",
			ev:     mow.Event{Type: mow.EventDelegateProgress, Agent: "peer-agent", Delta: "   \n"},
			silent: true,
		},
		{
			name:   "stream prefixes newline",
			stream: true,
			ev:     mow.Event{Type: mow.EventDelegateProgress, Agent: "peer-agent", Delta: "step"},
			want:   "\n  ↳ peer-agent: step\n",
		},
		{
			name: "long delta clipped",
			ev:   mow.Event{Type: mow.EventDelegateProgress, Agent: "p", Delta: strings.Repeat("x", 400)},
			want: "…",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := captureStderr(t, func() {
				cliutil.ToolProgressOnEvent(c.stream)(c.ev)
			})
			if c.silent {
				if out != "" {
					t.Fatalf("want no output, got %q", out)
				}
				return
			}
			if !strings.Contains(out, c.want) {
				t.Fatalf("out=%q want substring %q", out, c.want)
			}
			if c.name == "long delta clipped" {
				// 96-rune clip plus the "  ↳ p: " prefix and trailing newline.
				if n := len([]rune(strings.TrimSpace(out))); n > 96+16 {
					t.Fatalf("delta not clipped: %d runes", n)
				}
			}
		})
	}
}

func TestToolProgressDelegateChunk(t *testing.T) {
	ev := mow.Event{Type: mow.EventDelegateChunk, Agent: "peer-agent", Delta: "answer text"}

	quiet := captureStderr(t, func() { cliutil.ToolProgressOnEvent(false)(ev) })
	if quiet != "" {
		t.Fatalf("non-stream chunk must be silent, got %q", quiet)
	}

	loud := captureStderr(t, func() { cliutil.ToolProgressOnEvent(true)(ev) })
	if loud != "answer text" {
		t.Fatalf("stream chunk out=%q want raw delta", loud)
	}
}

func TestEnableVerbose(t *testing.T) {
	orig := slog.Default()
	t.Cleanup(func() { slog.SetDefault(orig) })

	// off → default logger untouched.
	cliutil.EnableVerbose(false)
	if slog.Default() != orig {
		t.Fatal("EnableVerbose(false) must not replace the default logger")
	}

	out := captureStderr(t, func() {
		cliutil.EnableVerbose(true)
		slog.Debug("verbose-probe", "k", "v")
	})
	if !strings.Contains(out, "verbose-probe") {
		t.Fatalf("debug line missing after EnableVerbose(true): %q", out)
	}
	if slog.Default() == orig {
		t.Fatal("EnableVerbose(true) should install a debug logger")
	}
}

func TestOptionsCLIOnTokenWritesStderr(t *testing.T) {
	var ef cliutil.EngineFlags
	fs := cliutil.NewFlagSet("run")
	ef.Bind(fs)
	if err := fs.Parse([]string{"--stream"}); err != nil {
		t.Fatal(err)
	}
	opt := ef.OptionsCLI()
	if opt.OnToken == nil {
		t.Fatal("OnToken not wired")
	}
	out := captureStderr(t, func() { opt.OnToken("tok") })
	if out != "tok" {
		t.Fatalf("OnToken out=%q want %q", out, "tok")
	}
}

func TestNewFlagSetUsageAndErrors(t *testing.T) {
	t.Run("usage lists long flags", func(t *testing.T) {
		fs := cliutil.NewFlagSet("run")
		var ef cliutil.EngineFlags
		ef.Bind(fs)
		var buf bytes.Buffer
		fs.SetOutput(&buf)
		fs.Usage()
		out := buf.String()
		if !strings.Contains(out, "Usage of run:") {
			t.Fatalf("missing usage header:\n%s", out)
		}
		for _, want := range []string{
			"--config", "--workspace", "--extra-root", "--model", "--effort",
			"--base-url", "--system-prefix", "--allow-shell", "--allow-write",
			"--max-turns", "--no-session", "--session", "--continue",
			"--stream", "--verbose",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("usage missing %s:\n%s", want, out)
			}
		}
		if !strings.Contains(out, "enable bash") {
			t.Errorf("usage missing --allow-shell description:\n%s", out)
		}
	})

	t.Run("unknown flag is an error", func(t *testing.T) {
		fs := cliutil.NewFlagSet("run")
		fs.SetOutput(io.Discard)
		var ef cliutil.EngineFlags
		ef.Bind(fs)
		if err := fs.Parse([]string{"--nope"}); err == nil {
			t.Fatal("want error for unknown flag")
		}
	})

	t.Run("bad int value is an error", func(t *testing.T) {
		fs := cliutil.NewFlagSet("run")
		fs.SetOutput(io.Discard)
		var ef cliutil.EngineFlags
		ef.Bind(fs)
		if err := fs.Parse([]string{"--max-turns", "many"}); err == nil {
			t.Fatal("want error for non-integer --max-turns")
		}
	})

	t.Run("help returns ErrHelp", func(t *testing.T) {
		fs := cliutil.NewFlagSet("run")
		fs.SetOutput(io.Discard)
		var ef cliutil.EngineFlags
		ef.Bind(fs)
		if err := fs.Parse([]string{"--help"}); err != flag.ErrHelp {
			t.Fatalf("Parse(--help)=%v want flag.ErrHelp", err)
		}
	})

	t.Run("positional args preserved", func(t *testing.T) {
		fs := cliutil.NewFlagSet("run")
		var ef cliutil.EngineFlags
		ef.Bind(fs)
		if err := fs.Parse([]string{"--stream", "hello", "world"}); err != nil {
			t.Fatal(err)
		}
		if got := fs.Args(); len(got) != 2 || got[0] != "hello" || got[1] != "world" {
			t.Fatalf("Args()=%v", got)
		}
	})

	t.Run("single dash long form accepted", func(t *testing.T) {
		fs := cliutil.NewFlagSet("run")
		var ef cliutil.EngineFlags
		ef.Bind(fs)
		if err := fs.Parse([]string{"-model", "gpt-5-mini"}); err != nil {
			t.Fatal(err)
		}
		if ef.Model != "gpt-5-mini" {
			t.Fatalf("Model=%q", ef.Model)
		}
	})
}

func TestPrintDefaultsEdges(t *testing.T) {
	t.Parallel()
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	var buf bytes.Buffer
	fs.SetOutput(&buf)
	fs.String("s", "", "short with default")
	fs.String("with-default", "abc", "has a `VALUE` default")
	fs.Int("count", 3, "how many\nsecond line")
	fs.Bool("flagless", false, "")
	fs.Bool("v", false, "short bool: no value name, tab-aligned")
	cliutil.PrintDefaults(fs)
	out := buf.String()

	if !strings.Contains(out, `(default "abc")`) {
		t.Errorf("non-zero default not shown:\n%s", out)
	}
	if !strings.Contains(out, `(default "3")`) {
		t.Errorf("int default not shown:\n%s", out)
	}
	if strings.Contains(out, `(default "false")`) {
		t.Errorf("zero bool default should be hidden:\n%s", out)
	}
	if !strings.Contains(out, "--with-default VALUE") {
		t.Errorf("unquoted usage name missing:\n%s", out)
	}
	if !strings.Contains(out, "how many\n    \tsecond line") {
		t.Errorf("multi-line usage not indented:\n%s", out)
	}
	// Short bool flags have no value name, so usage is tab-aligned on one line.
	if !strings.Contains(out, "  -v\tshort bool") {
		t.Errorf("short flag not tab-aligned:\n%s", out)
	}
}

func TestPrintDefaultsEmptyFlagSet(t *testing.T) {
	t.Parallel()
	fs := flag.NewFlagSet("empty", flag.ContinueOnError)
	var buf bytes.Buffer
	fs.SetOutput(&buf)
	cliutil.PrintDefaults(fs)
	if buf.Len() != 0 {
		t.Fatalf("empty flag set should print nothing, got %q", buf.String())
	}
}

func TestFormatToolProgressEdges(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		tool string
		args json.RawMessage
		want string
	}{
		{"whitespace tool name", "   ", nil, "?"},
		{"tool name trimmed", "  read  ", json.RawMessage(`{"path":"a"}`), "read a"},
		{"json null args", "read", json.RawMessage(`null`), "read"},
		{"json string args", "read", json.RawMessage(`"oops"`), "read"},
		{"path with newlines collapsed", "read", json.RawMessage(`{"path":"a\n  b"}`), "read a b"},
		{"unicode command", "bash", json.RawMessage(`{"command":"echo 日本語"}`), "bash echo 日本語"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := cliutil.FormatToolProgress(c.tool, c.args); got != c.want {
				t.Fatalf("FormatToolProgress(%q,%s)=%q want %q", c.tool, c.args, got, c.want)
			}
		})
	}
}

func TestFormatToolProgressVeryLongCommand(t *testing.T) {
	t.Parallel()
	args, err := json.Marshal(map[string]string{"command": strings.Repeat("a", 5000)})
	if err != nil {
		t.Fatal(err)
	}
	got := cliutil.FormatToolProgress("bash", args)
	// "bash " + 64-rune clip.
	if n := len([]rune(got)); n != len("bash ")+64 {
		t.Fatalf("len=%d want %d (%q)", n, len("bash ")+64, got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("missing ellipsis: %q", got)
	}
}

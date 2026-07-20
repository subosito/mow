package cliutil

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/subosito/mow"
)

// EnableVerbose turns on Debug slog so demoted run/tool lifecycle lines appear.
func EnableVerbose(on bool) {
	if !on {
		return
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
}

// OptionsCLI is Options plus stock CLI UX: optional token stream and compact
// tool progress on stderr. Used by run/repl and packs that drive Prompt.
func (f *EngineFlags) OptionsCLI() mow.Options {
	EnableVerbose(f.Verbose)
	opt := f.Options()
	if f.Stream {
		opt.Stream = true
		opt.OnToken = func(d string) { fmt.Fprint(os.Stderr, d) }
	}
	opt.OnEvent = ToolProgressOnEvent(f.Stream)
	return opt
}

// NewEngineCLI is NewEngine with OptionsCLI (tool progress + stream + verbose).
func (f *EngineFlags) NewEngineCLI() (*mow.Engine, error) {
	return mow.New(f.OptionsCLI())
}

// ToolProgressOnEvent prints short tool lines on stderr (not full slog dumps).
// Includes a one-line target hint (path / pattern / command).
func ToolProgressOnEvent(stream bool) mow.EventFunc {
	return func(ev mow.Event) {
		switch ev.Type {
		case mow.EventToolStart:
			if stream {
				fmt.Fprint(os.Stderr, "\n")
			}
			fmt.Fprintf(os.Stderr, "→ %s\n", FormatToolProgress(ev.Tool, ev.Args))
		case mow.EventToolEnd:
			if ev.Denied || ev.Error != "" {
				msg := ev.Error
				if msg == "" {
					msg = "denied"
				}
				fmt.Fprintf(os.Stderr, "✗ %s: %s\n", FormatToolProgress(ev.Tool, ev.Args), msg)
			}
		}
	}
}

// FormatToolProgress → "read engine.go", "glob **/*.go", "grep foo in pkg/".
func FormatToolProgress(tool string, args json.RawMessage) string {
	tool = strings.TrimSpace(tool)
	if tool == "" {
		tool = "?"
	}
	if d := toolProgressDetail(tool, args); d != "" {
		return tool + " " + d
	}
	return tool
}

func toolProgressDetail(tool string, args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil || len(m) == 0 {
		return ""
	}
	str := func(k string) string {
		v, ok := m[k]
		if !ok || v == nil {
			return ""
		}
		s, ok := v.(string)
		if !ok {
			return ""
		}
		return strings.TrimSpace(s)
	}
	switch strings.ToLower(tool) {
	case "read", "write", "edit":
		return clipRunes(str("path"), 72)
	case "glob":
		return clipRunes(str("pattern"), 72)
	case "grep":
		pat := clipRunes(str("pattern"), 40)
		if pat == "" {
			return ""
		}
		if p := str("path"); p != "" && p != "." {
			return pat + " in " + clipRunes(p, 40)
		}
		return pat
	case "bash":
		return clipRunes(str("command"), 64)
	default:
		for _, k := range []string{"path", "pattern", "command", "query", "name", "file", "url"} {
			if v := str(k); v != "" {
				return clipRunes(v, 64)
			}
		}
		return ""
	}
}

func clipRunes(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if max <= 0 || s == "" {
		return s
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	if max < 2 {
		return string(r[:max])
	}
	return string(r[:max-1]) + "…"
}

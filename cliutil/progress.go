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

// promptCostWarnTokens is the floor for printing a pre-send cost line.
// Below this, the hint is noise on short/cold prompts.
const promptCostWarnTokens = 8_000

// PrintPromptCostEstimate writes a one-line approximate input cost to stderr
// when the next Prompt would re-send a non-trivial context. Makes token waste
// visible before the round trip. No-op when eng is nil or the estimate is small.
func PrintPromptCostEstimate(eng *mow.Engine) {
	if eng == nil {
		return
	}
	est := eng.EstimatePromptCost()
	if est.InputTokens < promptCostWarnTokens {
		return
	}
	src := "est"
	if est.FromProvider {
		src = "last"
	}
	line := fmt.Sprintf("≈%s input tokens (%s)", formatTokenCount(est.InputTokens), src)
	if est.ContextWindow > 0 {
		pct := float64(est.InputTokens) / float64(est.ContextWindow) * 100
		if pct > 100 {
			pct = 100
		}
		line += fmt.Sprintf(" · %.0f%% context", pct)
	}
	if est.InputUSD > 0 {
		line += fmt.Sprintf(" · ~$%.4f input", est.InputUSD)
	}
	fmt.Fprintf(os.Stderr, "mow: next prompt %s\n", line)
}

// formatTokenCount renders a token count for CLI (e.g. 12k, 1.2M).
func formatTokenCount(n int) string {
	if n < 0 {
		n = 0
	}
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 10_000:
		return fmt.Sprintf("%dk", n/1000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// OptionsCLI is Options plus stock CLI UX: optional token stream and compact
// tool progress on stderr. Used by run/tty and packs that drive Prompt.
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
// Includes a one-line target hint (path / pattern / command) and delegate
// peer progress (delegate.progress / answer chunks when --stream).
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
			} else if ev.DurationMs > 2000 {
				// Long tools otherwise look hung; confirm completion with the
				// wall time from tool.end.
				fmt.Fprintf(os.Stderr, "✓ %s (%0.1fs)\n", FormatToolProgress(ev.Tool, ev.Args), float64(ev.DurationMs)/1000)
			}
		case mow.EventDelegateProgress:
			// Peer tool/thought while delegate is in flight.
			agent := strings.TrimSpace(ev.Agent)
			if agent == "" {
				agent = "peer"
			}
			line := strings.TrimSpace(ev.Delta)
			if line == "" {
				return
			}
			if stream {
				fmt.Fprint(os.Stderr, "\n")
			}
			fmt.Fprintf(os.Stderr, "  ↳ %s: %s\n", agent, clipRunes(line, 96))
		case mow.EventDelegateChunk:
			// Peer answer text — only when streaming so non-stream stays quiet.
			if !stream {
				return
			}
			fmt.Fprint(os.Stderr, ev.Delta)
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
	case "delegate":
		if a := str("agent"); a != "" {
			return clipRunes(a, 40)
		}
		return clipRunes(str("subagent"), 40)
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

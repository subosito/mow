package eval

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/subosito/mow"
)

// Options configure a Run / RunFixture invocation.
type Options struct {
	// Workspace is the engine workspace (required for FS tools in live runs).
	Workspace string
	// Provider, when set, is used for live eval (ignored when Case.Script is set).
	Provider mow.Provider
	// Chat is a legacy live backend (ignored when Script or Provider is set).
	Chat mow.ChatFunc
	// Base merges into mow.New options (API key, model, base URL, …). Script /
	// Provider / Chat still win for the LLM backend. NoSession is always forced.
	Base mow.Options
	// AllowWrite / AllowShell override Base when set on this struct.
	AllowWrite bool
	AllowShell bool
	// MaxTurns overrides the engine loop budget when > 0.
	MaxTurns int
	// OnEvent is forwarded to the engine (metrics/debug).
	OnEvent mow.EventFunc
}

// Report is the outcome of one Case.
type Report struct {
	Name       string   `json:"name,omitempty"`
	OK         bool     `json:"ok"`
	Text       string   `json:"text,omitempty"`
	StopReason string   `json:"stop_reason,omitempty"`
	Tools      []string `json:"tools,omitempty"`
	Err        string   `json:"error,omitempty"`
	Failures   []string `json:"failures,omitempty"`
}

// SuiteReport aggregates fixture results.
type SuiteReport struct {
	Name    string   `json:"name,omitempty"`
	OK      bool     `json:"ok"`
	Passed  int      `json:"passed"`
	Failed  int      `json:"failed"`
	Reports []Report `json:"reports"`
}

// Run executes one Case under a disposable Engine.
func Run(ctx context.Context, c Case, opt Options) (Report, error) {
	name := strings.TrimSpace(c.Name)
	if name == "" {
		name = "case"
	}
	rep := Report{Name: name}
	prompt := strings.TrimSpace(c.Prompt)
	if prompt == "" {
		rep.Err = "empty prompt"
		rep.Failures = []string{rep.Err}
		return rep, fmt.Errorf("eval: %s: empty prompt", name)
	}

	engOpt := opt.Base
	engOpt.NoSession = true
	if ws := strings.TrimSpace(opt.Workspace); ws != "" {
		engOpt.Workspace = ws
	}
	if engOpt.Workspace == "" {
		engOpt.Workspace = "."
	}
	if opt.AllowWrite {
		engOpt.AllowWrite = true
	}
	if opt.AllowShell {
		engOpt.AllowShell = true
	}
	if opt.MaxTurns != 0 {
		engOpt.MaxTurns = opt.MaxTurns
	}
	if opt.OnEvent != nil {
		engOpt.OnEvent = opt.OnEvent
	}

	switch {
	case len(c.Script) > 0:
		// Deterministic offline replay — clear live backends from Base.
		engOpt.Provider = &scriptProvider{turns: append([]mow.Message(nil), c.Script...)}
		engOpt.Chat = nil
	case opt.Provider != nil:
		engOpt.Provider = opt.Provider
		engOpt.Chat = nil
	case opt.Chat != nil:
		engOpt.Chat = opt.Chat
		engOpt.Provider = nil
	case engOpt.Provider != nil || engOpt.Chat != nil:
		// live backend already on Base
	default:
		// Base may still build a real HTTP client (model+key from flags/env).
		// If New fails, the error is returned below.
	}

	eng, err := mow.New(engOpt)
	if err != nil {
		rep.Err = err.Error()
		rep.Failures = []string{rep.Err}
		return rep, fmt.Errorf("eval: %s: engine: %w", name, err)
	}
	defer eng.Close()

	_ = c.Prior // reserved: mid-session seed (use Script for pure turn scripts)

	popt := mow.PromptOpts{}
	if s := strings.TrimSpace(c.SystemAppend); s != "" {
		popt.SystemAppend = s
	}
	res, err := eng.PromptWith(ctx, prompt, popt)
	rep.Text = res.Text
	rep.StopReason = res.StopReason
	rep.Tools = toolsUsed(eng)

	if err != nil {
		rep.Err = err.Error()
		if !c.Expect.AllowError {
			rep.Failures = append(rep.Failures, "prompt error: "+err.Error())
		}
	}
	rep.Failures = append(rep.Failures, checkExpect(c.Expect, rep, eng)...)
	rep.OK = len(rep.Failures) == 0
	if !rep.OK {
		return rep, fmt.Errorf("eval: %s failed: %s", name, strings.Join(rep.Failures, "; "))
	}
	return rep, nil
}

// RunFixture runs every case; does not stop on first failure.
func RunFixture(ctx context.Context, f Fixture, opt Options) SuiteReport {
	sr := SuiteReport{Name: f.Name, OK: true}
	for i, c := range f.Cases {
		if strings.TrimSpace(c.Name) == "" {
			c.Name = fmt.Sprintf("case-%d", i+1)
		}
		rep, err := Run(ctx, c, opt)
		if err != nil && rep.Err == "" {
			rep.Err = err.Error()
		}
		if rep.OK {
			sr.Passed++
		} else {
			sr.Failed++
			sr.OK = false
		}
		sr.Reports = append(sr.Reports, rep)
	}
	return sr
}

// scriptProvider returns Script turns in order; exhaust → empty assistant.
type scriptProvider struct {
	mu    sync.Mutex
	turns []mow.Message
	i     int
}

func (p *scriptProvider) Chat(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec, hooks mow.ChatHooks) (mow.Message, error) {
	if err := ctx.Err(); err != nil {
		return mow.Message{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.i >= len(p.turns) {
		return mow.Message{Role: "assistant", Content: ""}, nil
	}
	msg := p.turns[p.i]
	p.i++
	if msg.Role == "" {
		msg.Role = "assistant"
	}
	if hooks.OnToken != nil && msg.Content != "" {
		hooks.OnToken(msg.Content)
	}
	return msg, nil
}

func toolsUsed(eng *mow.Engine) []string {
	if eng == nil {
		return nil
	}
	msgs := eng.Messages()
	seen := map[string]bool{}
	var out []string
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			n := tc.Function.Name
			if n == "" || seen[n] {
				continue
			}
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

func checkExpect(ex Expect, rep Report, eng *mow.Engine) []string {
	var fails []string
	text := rep.Text
	for _, s := range ex.Contains {
		if s != "" && !strings.Contains(text, s) {
			fails = append(fails, fmt.Sprintf("missing %q in text", s))
		}
	}
	for _, s := range ex.NotContains {
		if s != "" && strings.Contains(text, s) {
			fails = append(fails, fmt.Sprintf("forbidden %q in text", s))
		}
	}
	if sr := strings.TrimSpace(ex.StopReason); sr != "" && rep.StopReason != sr {
		fails = append(fails, fmt.Sprintf("stop_reason=%q want %q", rep.StopReason, sr))
	}
	if len(ex.Tools) > 0 {
		have := map[string]bool{}
		for _, n := range rep.Tools {
			have[n] = true
		}
		if eng != nil {
			for _, n := range toolsUsed(eng) {
				have[n] = true
			}
		}
		for _, want := range ex.Tools {
			if want != "" && !have[want] {
				fails = append(fails, fmt.Sprintf("tool %q not used (had %v)", want, rep.Tools))
			}
		}
	}
	if ex.MaxTurns > 0 && eng != nil {
		n := 0
		for _, m := range eng.Messages() {
			if m.Role == "assistant" {
				n++
			}
		}
		if n > ex.MaxTurns {
			fails = append(fails, fmt.Sprintf("assistant turns=%d > max_turns=%d", n, ex.MaxTurns))
		}
	}
	return fails
}

// AbsWorkspace resolves workspace to an absolute path when non-empty.
func AbsWorkspace(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", nil
	}
	return filepath.Abs(dir)
}

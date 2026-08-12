// Package eval is a lightweight, deterministic evaluation / replay harness for
// mow. Hosts and CI use it to re-run scripted agent turns (or a live Provider)
// against fixed prompts and assert behavioral expectations — without a vector
// DB, heavy graph engine, or private fleet glue.
//
// Core never imports this package. Typical use:
//
//	rep, err := eval.Run(ctx, eval.Case{
//	    Name:   "lists-go-files",
//	    Prompt: "List the Go files at the repo root",
//	    Script: []mow.Message{ /* assistant turns */ },
//	    Expect: eval.Expect{Contains: []string{".go"}, Tools: []string{"glob"}},
//	}, eval.Options{Workspace: dir})
package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/subosito/mow"
)

// Case is one evaluation scenario.
type Case struct {
	// Name is a short id for reports (optional).
	Name string `json:"name,omitempty"`
	// Prompt is the user turn fed to Engine.Prompt.
	Prompt string `json:"prompt"`
	// SystemAppend is optional per-call system text (PromptOpts.SystemAppend).
	SystemAppend string `json:"system_append,omitempty"`
	// Script is the ordered list of assistant messages a scripted Provider
	// returns on successive Chat calls. Empty means Options.Provider/Chat
	// must supply the model (live eval).
	Script []mow.Message `json:"script,omitempty"`
	// Expect is checked against the run result. Empty Expect only checks err==nil
	// unless Expect.AllowError is set.
	Expect Expect `json:"expect,omitempty"`
}

// Expect is a behavioral assertion set. All non-empty fields must hold.
type Expect struct {
	// Contains requires each substring to appear in the final assistant text.
	Contains []string `json:"contains,omitempty"`
	// NotContains forbids substrings in the final assistant text.
	NotContains []string `json:"not_contains,omitempty"`
	// Tools requires each name to appear at least once among tool calls the
	// scripted/live model issued (from the result message history).
	Tools []string `json:"tools,omitempty"`
	// StopReason, when set, must equal RunResult.StopReason.
	StopReason string `json:"stop_reason,omitempty"`
	// AllowError accepts a non-nil Prompt error (still runs other checks).
	AllowError bool `json:"allow_error,omitempty"`
	// MaxTurns fails the case when the engine reports more assistant turns
	// than N in the result history (0 = no check).
	MaxTurns int `json:"max_turns,omitempty"`
}

// Fixture is a JSON file with one or more cases (CI-friendly).
type Fixture struct {
	// Name labels the suite.
	Name  string `json:"name,omitempty"`
	Cases []Case `json:"cases"`
}

// maxFixtureBytes caps on-disk fixture size (prevents accidental huge loads).
const maxFixtureBytes = 8 << 20 // 8 MiB

// maxFixtureCases caps how many cases one fixture file may declare.
const maxFixtureCases = 500

// LoadFixture reads a JSON fixture from path.
func LoadFixture(path string) (Fixture, error) {
	st, err := os.Stat(path)
	if err != nil {
		return Fixture{}, err
	}
	if st.Size() > maxFixtureBytes {
		return Fixture{}, fmt.Errorf("eval: fixture %q too large (%d bytes; max %d)", path, st.Size(), maxFixtureBytes)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Fixture{}, err
	}
	return ParseFixture(raw)
}

// ParseFixture decodes fixture JSON from bytes.
func ParseFixture(raw []byte) (Fixture, error) {
	if len(raw) > maxFixtureBytes {
		return Fixture{}, fmt.Errorf("eval: fixture too large (%d bytes; max %d)", len(raw), maxFixtureBytes)
	}
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return Fixture{}, fmt.Errorf("eval: empty fixture")
	}
	// Allow a bare Case object or a Fixture object.
	if raw[0] == '{' {
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(raw, &probe); err != nil {
			return Fixture{}, fmt.Errorf("eval: fixture: %w", err)
		}
		if _, ok := probe["cases"]; ok {
			var f Fixture
			if err := json.Unmarshal(raw, &f); err != nil {
				return Fixture{}, fmt.Errorf("eval: fixture: %w", err)
			}
			if len(f.Cases) == 0 {
				return Fixture{}, fmt.Errorf("eval: fixture has no cases")
			}
			if len(f.Cases) > maxFixtureCases {
				return Fixture{}, fmt.Errorf("eval: fixture has %d cases (max %d)", len(f.Cases), maxFixtureCases)
			}
			return f, nil
		}
		var c Case
		if err := json.Unmarshal(raw, &c); err != nil {
			return Fixture{}, fmt.Errorf("eval: case: %w", err)
		}
		if strings.TrimSpace(c.Prompt) == "" {
			return Fixture{}, fmt.Errorf("eval: case prompt required")
		}
		return Fixture{Cases: []Case{c}}, nil
	}
	if raw[0] == '[' {
		var cases []Case
		if err := json.Unmarshal(raw, &cases); err != nil {
			return Fixture{}, fmt.Errorf("eval: cases: %w", err)
		}
		if len(cases) == 0 {
			return Fixture{}, fmt.Errorf("eval: empty case list")
		}
		return Fixture{Cases: cases}, nil
	}
	return Fixture{}, fmt.Errorf("eval: fixture must be a JSON object or array")
}

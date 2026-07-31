package config_test

import (
	"testing"

	"github.com/subosito/mow/internal/config"
)

// Defaults are the harness's answer to "what should an agentic coding session
// tolerate out of the box". They are pinned here because drifting them
// silently changes behaviour for every user who never writes a config.
func TestAgenticDefaults(t *testing.T) {
	f, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	p := f.Policy
	// A coding agent runs builds and test suites; a sub-minute default forces
	// background-process workarounds for ordinary work.
	if p.BashTimeoutSec < 180 {
		t.Errorf("bash_timeout_sec = %d, too low for builds/test suites", p.BashTimeoutSec)
	}
	// The per-call ceiling must be at least the default, or an explicit
	// default would be clamped on every call.
	if p.MaxBashTimeoutSec < p.BashTimeoutSec {
		t.Errorf("max_bash_timeout_sec=%d below bash_timeout_sec=%d", p.MaxBashTimeoutSec, p.BashTimeoutSec)
	}
	// High enough that multi-file work does not die mid-stream, low enough to
	// stop a runaway loop.
	if p.MaxTurns < 60 || p.MaxTurns > 400 {
		t.Errorf("max_turns = %d, outside the sane agentic range", p.MaxTurns)
	}
	if p.MaxParallelTools < 1 || p.MaxParallelTools > 16 {
		t.Errorf("max_parallel_tools = %d", p.MaxParallelTools)
	}
}

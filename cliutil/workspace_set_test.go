package cliutil_test

import (
	"testing"

	"github.com/subosito/mow/cliutil"
)

func TestWorkspaceFlagAcceptsNameOrPath(t *testing.T) {
	t.Parallel()
	var ef cliutil.EngineFlags
	fs := cliutil.NewFlagSet("run")
	ef.Bind(fs)
	if err := fs.Parse([]string{"--workspace", "monorepo", "--effort", "high"}); err != nil {
		t.Fatal(err)
	}
	opt := ef.Options()
	if opt.Workspace != "monorepo" {
		t.Fatalf("Workspace = %q, want monorepo (hybrid: set name or path)", opt.Workspace)
	}
	if opt.Effort != "high" || !opt.ExplicitEffort {
		t.Fatalf("Effort = %q ExplicitEffort = %v, want high,true", opt.Effort, opt.ExplicitEffort)
	}
}

func TestEffortFlagBlankNotExplicit(t *testing.T) {
	t.Parallel()
	var ef cliutil.EngineFlags
	fs := cliutil.NewFlagSet("run")
	ef.Bind(fs)
	if err := fs.Parse([]string{"--effort", ""}); err != nil {
		t.Fatal(err)
	}
	opt := ef.Options()
	if opt.Effort != "" || opt.ExplicitEffort {
		t.Fatalf("blank --effort must stay non-explicit: %q %v", opt.Effort, opt.ExplicitEffort)
	}
}

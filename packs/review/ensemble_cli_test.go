package review

import (
	"flag"
	"strings"
	"testing"

	"github.com/subosito/mow/cliutil"
)

func TestReviewerFlagsParseRepeatableAndCommaSeparated(t *testing.T) {
	fs := flag.NewFlagSet("mow review", flag.ContinueOnError)
	fs.SetOutput(new(strings.Builder))
	var rf CLIFlags
	rf.Bind(fs)
	if _, err := parseArgs(fs, []string{"--reviewer", "gpt-5-mini", "--reviewers", "claude-sonnet-4, gemini-2.5-flash", "--reviewer-parallel", "2"}); err != nil {
		t.Fatal(err)
	}
	models, err := reviewerModels(rf.Reviewers)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"gpt-5-mini", "claude-sonnet-4", "gemini-2.5-flash"}
	if strings.Join(models, ",") != strings.Join(want, ",") {
		t.Fatalf("models = %v, want %v", models, want)
	}
	if rf.ReviewerParallel != 2 {
		t.Fatalf("parallel = %d, want 2", rf.ReviewerParallel)
	}
}

func TestReviewerModelsRejectInvalidValues(t *testing.T) {
	for _, reviewers := range [][]string{{"gpt-5-mini,,claude-sonnet-4"}, {"gpt-5-mini", "gpt-5-mini"}} {
		if _, err := reviewerModels(reviewers); err == nil {
			t.Fatalf("reviewerModels(%v) unexpectedly succeeded", reviewers)
		}
	}
}

func TestEnsembleOptionsAreIsolatedAndReadOnly(t *testing.T) {
	ef := cliutil.EngineFlags{
		Workspace:  "/ignored",
		Model:      "default-model",
		AllowWrite: true,
		AllowShell: true,
		NoSession:  false,
		Stream:     true,
	}
	opts, parallel, err := ensembleOptions(ef, "/workspace", []string{"gpt-5-mini", "claude-sonnet-4"}, 0, true, Budget{MaxTurns: 42})
	if err != nil {
		t.Fatal(err)
	}
	if parallel != 2 || len(opts) != 2 {
		t.Fatalf("parallel/options = %d/%d, want 2/2", parallel, len(opts))
	}
	for i, opt := range opts {
		if opt.Model != []string{"gpt-5-mini", "claude-sonnet-4"}[i] || !opt.ExplicitModel {
			t.Errorf("option %d model = %q, explicit=%t", i, opt.Model, opt.ExplicitModel)
		}
		if opt.Workspace != "/workspace" || opt.AllowWrite || opt.AllowShell || !opt.NoSession || opt.Stream {
			t.Errorf("option %d is not isolated/read-only: %+v", i, opt)
		}
		if opt.MaxTurns != 42 || opt.OnEvent != nil {
			t.Errorf("option %d max turns/progress = %d/%v", i, opt.MaxTurns, opt.OnEvent)
		}
	}
	if ef.Model != "default-model" || !ef.AllowWrite || !ef.AllowShell || ef.NoSession || !ef.Stream {
		t.Fatalf("input EngineFlags mutated: %+v", ef)
	}
}

func TestEnsembleOptionsRejectNegativeParallel(t *testing.T) {
	if _, _, err := ensembleOptions(cliutil.EngineFlags{}, "/workspace", []string{"gpt-5-mini"}, -1, false, Budget{}); err == nil {
		t.Fatal("negative parallel unexpectedly succeeded")
	}
}

func TestVerifierEngineOptionsAreReadOnly(t *testing.T) {
	ef := cliutil.EngineFlags{Model: "default-model", AllowWrite: true, AllowShell: true}
	opt := verifierEngineOptions(ef, "/workspace", "claude-sonnet-4", true, Budget{MaxTurns: 55})
	if opt.Model != "claude-sonnet-4" || !opt.ExplicitModel || opt.AllowWrite || opt.AllowShell || !opt.NoSession || opt.Stream {
		t.Fatalf("verifier option not read-only/isolated: %+v", opt)
	}
	if opt.MaxTurns != 55 {
		t.Fatalf("MaxTurns = %d", opt.MaxTurns)
	}
}

func TestResolveRejectsVerifierModelWithNoVerify(t *testing.T) {
	var rf CLIFlags
	rf.NoVerify = true
	rf.VerifierModel = "claude-sonnet-4"
	if _, _, _, err := rf.Resolve(GeneralProfile(), "/ws", nil); err == nil || !strings.Contains(err.Error(), "--verifier-model") {
		t.Fatalf("err = %v", err)
	}
}

package goal_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext/goal"
)

func TestNormalizeAndSlug(t *testing.T) {
	s, err := goal.NormalizeSpec(goal.Spec{Goal: "Fix the CI pipeline now!"})
	if err != nil {
		t.Fatal(err)
	}
	if s.ID == "" || s.MaxSteps != goal.DefaultMaxSteps {
		t.Fatalf("%+v", s)
	}
	// The ceiling still clamps an over-large request.
	big, err := goal.NormalizeSpec(goal.Spec{Goal: "x", MaxSteps: goal.MaxMaxSteps + 100})
	if err != nil {
		t.Fatal(err)
	}
	if big.MaxSteps != goal.MaxMaxSteps {
		t.Fatalf("MaxSteps=%d want clamp to %d", big.MaxSteps, goal.MaxMaxSteps)
	}
	if _, err := goal.NormalizeSpec(goal.Spec{Goal: "x", ID: "../evil"}); err == nil {
		t.Fatal("expected bad id")
	}
}

func TestParseOutcome(t *testing.T) {
	done, fail, _ := goal.ParseOutcome("all good\nGOAL_DONE\n")
	if !done || fail {
		t.Fatal("done")
	}
	done, fail, reason := goal.ParseOutcome("nope\nGOAL_FAILED: no access\n")
	if done || !fail || reason != "no access" {
		t.Fatalf("%v %v %q", done, fail, reason)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st := goal.State{ID: "g1", Goal: "do it", Status: goal.StatusPending, MaxSteps: 3}
	s := &goal.Store{Dir: dir}
	if err := s.Save(st); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load("g1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Goal != "do it" || got.Status != goal.StatusPending {
		t.Fatalf("%+v", got)
	}
	list, err := s.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("list=%v err=%v", list, err)
	}
}

func TestRunnerCompletesOnMarker(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MOW_HOME", dir)
	var n atomic.Int32
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			i := n.Add(1)
			if i < 2 {
				return mow.Message{Role: "assistant", Content: "working on it"}, nil
			}
			return mow.Message{Role: "assistant", Content: "finished\nGOAL_DONE"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var events []goal.EventKind
	r := &goal.Runner{
		Engine: eng,
		Store:  &goal.Store{Dir: dir + "/goals"},
		OnEvent: func(e goal.Event) {
			events = append(events, e.Kind)
		},
	}
	st, err := r.RunSpec(context.Background(), goal.Spec{
		ID: "ci", Goal: "ship it", MaxSteps: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != goal.StatusDone || st.Step != 2 {
		t.Fatalf("status=%s step=%d", st.Status, st.Step)
	}
	// start, step, done (or start, step1, done without intermediate if done on step 2 fires done only)
	var kinds string
	for _, k := range events {
		kinds += string(k) + ","
	}
	if !strings.Contains(kinds, string(goal.EventStart)) || !strings.Contains(kinds, string(goal.EventDone)) {
		t.Fatalf("events=%v", events)
	}
	// Persisted
	loaded, err := r.Store.Load("ci")
	if err != nil || loaded.Status != goal.StatusDone {
		t.Fatalf("load %+v err=%v", loaded, err)
	}
}

func TestRunnerFailsOnMarker(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MOW_HOME", dir)
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "GOAL_FAILED: blocked"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &goal.Runner{Engine: eng, Store: &goal.Store{Dir: dir + "/goals"}}
	st, err := r.RunSpec(context.Background(), goal.Spec{ID: "x", Goal: "y", MaxSteps: 3})
	if err == nil || st.Status != goal.StatusFailed {
		t.Fatalf("st=%+v err=%v", st, err)
	}
	if st.Error != "blocked" {
		t.Fatalf("error=%q", st.Error)
	}
}

func TestRunnerMaxSteps(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MOW_HOME", dir)
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "still going"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &goal.Runner{Engine: eng, Store: &goal.Store{Dir: dir + "/goals"}}
	st, err := r.RunSpec(context.Background(), goal.Spec{ID: "m", Goal: "g", MaxSteps: 2})
	// Budget exhaustion is a clean partial stop, not a bare failure: the run
	// reports what exists (StatusPartial + machine-readable Partial line).
	if err == nil || st.Status != goal.StatusPartial || st.Step != 2 {
		t.Fatalf("st=%+v err=%v", st, err)
	}
	if strings.TrimSpace(st.Partial) == "" {
		t.Fatalf("partial summary missing: %+v", st)
	}
	if !strings.Contains(st.Error, "max steps") {
		t.Fatalf("error should mention max steps for resume detection: %q", st.Error)
	}
}

func TestPartialSummary(t *testing.T) {
	cases := []struct {
		name string
		st   goal.State
		want string
	}{
		{
			name: "plan progress",
			st: goal.State{
				Step: 5, MaxSteps: 10,
				Plan: goal.Plan{Items: []goal.PlanItem{
					{ID: "a", Status: "done"},
					{ID: "b", Status: "done"},
					{ID: "c", Status: "pending"},
				}},
				LastReply: "draft at report.md",
			},
			want: "done 2/3 items, 1 missing",
		},
		{
			name: "no plan items",
			st:   goal.State{Step: 3, MaxSteps: 4},
			want: "stopped at step 3/4 after budget",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := goal.PartialSummaryFor(tc.st)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("PartialSummaryFor(%+v) = %q, want contains %q", tc.st, got, tc.want)
			}
		})
	}
}

func TestSubscribe(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MOW_HOME", dir)
	var saw int
	unsub := goal.Subscribe(func(e goal.Event) {
		if e.Kind == goal.EventStart {
			saw++
		}
	})
	defer unsub()
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "GOAL_DONE"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &goal.Runner{Engine: eng, Store: &goal.Store{Dir: dir + "/goals"}}
	if _, err := r.RunSpec(context.Background(), goal.Spec{ID: "s", Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	if saw != 1 {
		t.Fatalf("subscribe saw %d", saw)
	}
}

func TestRunnerAccumulatesUsage(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MOW_HOME", dir)
	var n atomic.Int32
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			i := n.Add(1)
			// Each step reports usage; the runner must sum across steps.
			usage := mow.Usage{InputTokens: 100, OutputTokens: 20}
			if i < 3 {
				return mow.Message{Role: "assistant", Content: "working", Usage: usage}, nil
			}
			return mow.Message{Role: "assistant", Content: "done\nGOAL_DONE", Usage: usage}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var lastEventTokens int
	r := &goal.Runner{
		Engine: eng,
		Store:  &goal.Store{Dir: dir + "/goals"},
		OnEvent: func(e goal.Event) {
			lastEventTokens = e.State.InputTokens + e.State.OutputTokens
		},
	}
	st, err := r.RunSpec(context.Background(), goal.Spec{ID: "u1", Goal: "x", MaxSteps: 5})
	if err != nil {
		t.Fatal(err)
	}
	if st.Step != 3 {
		t.Fatalf("step=%d want 3", st.Step)
	}
	if st.InputTokens != 300 || st.OutputTokens != 60 {
		t.Fatalf("usage=%d/%d want 300/60", st.InputTokens, st.OutputTokens)
	}
	// Events carry the running total so hosts can display live tokens.
	if lastEventTokens == 0 {
		t.Fatal("events did not carry cumulative usage")
	}
	// Persisted for `mow goal status`.
	loaded, _ := r.Store.Load("u1")
	if loaded.InputTokens != 300 {
		t.Fatalf("persisted usage lost: %d", loaded.InputTokens)
	}
}

func TestFactsText(t *testing.T) {
	st := goal.State{Facts: []goal.Fact{
		{Claim: "pricing is $9/mo", Source: "pricing.md", Confidence: 0.9},
		{Claim: "API supports streaming"},
	}}
	got := st.FactsText()
	for _, want := range []string{"pricing is $9/mo", "(source: pricing.md)", "[90%]", "API supports streaming"} {
		if !strings.Contains(got, want) {
			t.Fatalf("FactsText missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "\\\\n") {
		t.Fatalf("FactsText leaked literal escapes: %q", got)
	}
	if (goal.State{}).FactsText() != "" {
		t.Fatal("empty facts should render empty")
	}
}

// The evidence ledger is "state outside the chat window": facts seeded in
// state are injected into the next step's prompt, and a step's goal_report
// evidence is persisted into state for later steps.
func TestRunnerEvidenceLedger(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MOW_HOME", dir)
	var prompts []string
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			if len(messages) > 0 {
				prompts = append(prompts, messages[len(messages)-1].Content)
			}
			return mow.Message{Role: "assistant", Content: "working"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &goal.Runner{Engine: eng, Store: &goal.Store{Dir: dir + "/goals"}}
	// Seed a durable fact before running; step 1's prompt must carry it.
	st0 := goal.State{
		ID: "ev", Goal: "g", Status: goal.StatusRunning, Step: 0, MaxSteps: 3,
		Facts: []goal.Fact{{Claim: "base rate is 5%", Source: "rates.md", Confidence: 0.8}},
	}
	if err := r.Store.Save(st0); err != nil {
		t.Fatal(err)
	}
	_, err = r.RunRaise(context.Background(), "ev", 0)
	_ = err // budget will stop the run; we only assert prompt propagation
	joined := strings.Join(prompts, "\n")
	if !strings.Contains(joined, "Durable evidence so far") || !strings.Contains(joined, "base rate is 5%") {
		t.Fatalf("step prompt missing seeded facts:\n%s", joined)
	}
	// The facts were not clobbered by the run (still present in persisted state).
	loaded, lerr := r.Store.Load("ev")
	if lerr != nil {
		t.Fatal(lerr)
	}
	if len(loaded.Facts) != 1 || loaded.Facts[0].Claim != "base rate is 5%" {
		t.Fatalf("persisted facts clobbered: %+v", loaded.Facts)
	}
}

// A code-owned Router edge overrides the model-decided outcome: the runner
// stops partial when routed to partial_stop, and retry_same respects the
// code-owned retry cap before giving up.
func TestRunnerRouterEdges(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MOW_HOME", dir)
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "still going"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("partial_stop", func(t *testing.T) {
		r := &goal.Runner{
			Engine: eng,
			Store:  &goal.Store{Dir: dir + "/goals"},
			Router: func(st goal.State, sr goal.StepResult) goal.StepOutcome {
				return goal.OutcomePartialStop
			},
		}
		st, err := r.RunSpec(context.Background(), goal.Spec{ID: "r1", Goal: "g", MaxSteps: 4})
		if err != nil {
			t.Fatalf("partial stop should not error: %v", err)
		}
		if st.Status != goal.StatusPartial || st.Step != 1 {
			t.Fatalf("st=%+v want partial at step 1", st)
		}
		if strings.TrimSpace(st.Partial) == "" {
			t.Fatal("partial summary missing")
		}
	})
	t.Run("retry_cap", func(t *testing.T) {
		r := &goal.Runner{
			Engine: eng,
			Store:  &goal.Store{Dir: dir + "/goals"},
			Router: func(st goal.State, sr goal.StepResult) goal.StepOutcome {
				return goal.OutcomeRetrySame
			},
		}
		st, err := r.RunSpec(context.Background(), goal.Spec{ID: "r2", Goal: "g", MaxSteps: 4})
		if err == nil {
			t.Fatal("retry cap should stop with an error")
		}
		if st.Status != goal.StatusPartial {
			t.Fatalf("st.Status=%s want partial after retry cap", st.Status)
		}
		if st.RetryCount != goal.MaxStepRetries+1 {
			t.Fatalf("retry count=%d want %d", st.RetryCount, goal.MaxStepRetries+1)
		}
	})
	t.Run("escalate", func(t *testing.T) {
		r := &goal.Runner{
			Engine: eng,
			Store:  &goal.Store{Dir: dir + "/goals"},
			Router: func(st goal.State, sr goal.StepResult) goal.StepOutcome {
				return goal.OutcomeEscalate
			},
		}
		st, err := r.RunSpec(context.Background(), goal.Spec{ID: "r3", Goal: "g", MaxSteps: 4})
		if err == nil || !strings.Contains(err.Error(), "blocked") {
			t.Fatalf("escalate should block with error: %v", err)
		}
		if st.Status != goal.StatusBlocked {
			t.Fatalf("st.Status=%s want blocked", st.Status)
		}
		if strings.TrimSpace(st.Question) == "" {
			t.Fatal("blocked goal should carry a durable question")
		}
	})
}

// ResumeAnswer unblocks an escalated goal: the human decision is recorded
// as a durable fact, the question clears, and the run continues.
func TestResumeAnswerUnblocks(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MOW_HOME", dir)
	var steps atomic.Int32
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			steps.Add(1)
			return mow.Message{Role: "assistant", Content: "continuing"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	store := &goal.Store{Dir: dir + "/goals"}
	r := &goal.Runner{
		Engine: eng,
		Store:  store,
		Router: func(st goal.State, sr goal.StepResult) goal.StepOutcome {
			// Escalate the first step only.
			if st.Step == 1 {
				return goal.OutcomeEscalate
			}
			return goal.OutcomeContinue
		},
	}
	_, err = r.RunSpec(context.Background(), goal.Spec{ID: "esc", Goal: "g", MaxSteps: 5})
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("expected blocked: %v", err)
	}
	blocked, _ := store.Load("esc")
	if blocked.Status != goal.StatusBlocked || strings.TrimSpace(blocked.Question) == "" {
		t.Fatalf("blocked=%+v", blocked)
	}

	// Answer it: run continues (the stub model never finishes, so it runs to
	// the step budget and stops partial — the important asserts are the
	// durable decision and the cleared question on the returned state).
	st, err := r.ResumeAnswer(context.Background(), "esc", "use option B")
	if err == nil {
		t.Fatalf("expected budget-stop error after resume: %+v", st)
	}
	if st.Step < 2 {
		t.Fatalf("resume did not continue: %+v", st)
	}
	if st.Status != goal.StatusPartial {
		t.Fatalf("resumed run should stop partial at budget: %s", st.Status)
	}
	found := false
	for _, f := range st.Facts {
		if strings.Contains(f.Claim, "use option B") {
			found = true
		}
	}
	if !found {
		t.Fatalf("human decision not recorded in facts: %+v", st.Facts)
	}
	if strings.TrimSpace(st.Question) != "" {
		t.Fatalf("question should clear after answer: %q", st.Question)
	}
}

package goal_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/subosito/mow"
	"github.com/subosito/mow/packs/goal"
)

// gate blocks each chat call until `want` calls are in flight at once, which
// proves real concurrency without wall-clock flakiness.
type gate struct {
	want    int
	mu      sync.Mutex
	inFlt   int
	maxSeen int
	open    bool
}

func newGate(want int) *gate { return &gate{want: want} }

// wait blocks until `want` callers are inside, then releases everyone.
func (g *gate) wait(t *testing.T) {
	g.mu.Lock()
	g.inFlt++
	if g.inFlt > g.maxSeen {
		g.maxSeen = g.inFlt
	}
	if g.inFlt >= g.want {
		g.open = true
	}
	deadline := time.Now().Add(5 * time.Second)
	for !g.open {
		g.mu.Unlock()
		time.Sleep(2 * time.Millisecond)
		g.mu.Lock()
		if time.Now().After(deadline) {
			break
		}
	}
	g.inFlt--
	g.mu.Unlock()
}

func (g *gate) peak() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.maxSeen
}

func reportCall(id string, m map[string]any) mow.Message {
	args, _ := json.Marshal(m)
	return mow.Message{Role: "assistant", ToolCalls: []mow.ToolCall{{
		ID: id, Type: "function",
		Function: mow.FunctionCall{Name: "goal_report", Arguments: string(args)},
	}}}
}

// focusItem extracts the focused checklist item id from the LAST user message
// (the current step prompt); older turns may still be in the transcript.
func focusItem(messages []mow.Message) string {
	c := ""
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			c = messages[i].Content
			break
		}
	}
	if k := strings.Index(c, "Focus: ["); k >= 0 {
		rest := c[k+len("Focus: ["):]
		if e := strings.Index(rest, "]"); e > 0 {
			return rest[:e]
		}
	}
	return ""
}

func newParallelEnv(t *testing.T) (string, func(chat mow.ChatFunc) func() (*mow.Engine, error)) {
	t.Helper()
	t.Setenv("MOW_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "m")
	ws := t.TempDir()
	dir := t.TempDir()
	factory := func(chat mow.ChatFunc) func() (*mow.Engine, error) {
		return func() (*mow.Engine, error) {
			return mow.New(mow.Options{Workspace: ws, NoSession: true, Chat: chat})
		}
	}
	return dir, factory
}

// seedPlan runs step 0 (plan creation) then hands over to per-item work.
func planThenWork(g *gate, t *testing.T, work func(item string) mow.Message) mow.ChatFunc {
	var planned atomic.Bool
	return func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
		item := focusItem(messages)
		if item == "" && !planned.Load() {
			planned.Store(true)
			return reportCall("p", map[string]any{
				"status": "continue",
				"plan": []map[string]string{
					{"id": "a", "title": "item a", "status": "pending"},
					{"id": "b", "title": "item b", "status": "pending"},
				},
				"summary": "planned",
			}), nil
		}
		if item == "" {
			// Join step: everything done → finish the goal.
			return reportCall("d", map[string]any{"status": "done", "summary": "goal complete"}), nil
		}
		if g != nil {
			g.wait(t)
		}
		return work(item), nil
	}
}

// (b) ParallelMax=2 actually overlaps two in-flight chat calls.
func TestParallelStepRunsConcurrently(t *testing.T) {
	dir, factory := newParallelEnv(t)
	g := newGate(2)
	chat := planThenWork(g, t, func(item string) mow.Message {
		return reportCall("w", map[string]any{
			"status": "continue", "item_id": item, "item_status": "done",
			"summary": "did " + item,
		})
	})
	newEng := factory(chat)
	eng, err := newEng()
	if err != nil {
		t.Fatal(err)
	}
	r := &goal.Runner{Engine: eng, Store: &goal.Store{Dir: dir}, EngineFactory: newEng}
	st, err := r.RunSpec(context.Background(), goal.Spec{ID: "par", Goal: "two things", MaxSteps: 6, ParallelMax: 2})
	if err != nil {
		t.Fatalf("run: %v (status=%s err=%q)", err, st.Status, st.Error)
	}
	if st.Status != goal.StatusDone {
		t.Fatalf("status=%s err=%q", st.Status, st.Error)
	}
	if peak := g.peak(); peak < 2 {
		t.Fatalf("peak concurrent chat calls=%d want >=2 (steps did not run in parallel)", peak)
	}
	for _, it := range st.Plan.Items {
		if it.Status != goal.ItemDone {
			t.Fatalf("item %s = %s, want done", it.ID, it.Status)
		}
	}
}

func TestRunParallelClosesFactoryEngines(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	var opened, closed atomic.Int32
	newEng := func() (*mow.Engine, error) {
		opened.Add(1)
		eng, err := mow.New(mow.Options{
			Workspace: t.TempDir(), NoSession: true,
			Chat: func(context.Context, []mow.Message, []mow.ToolSpec) (mow.Message, error) {
				return reportCall("done", map[string]any{"status": "done", "summary": "ok"}), nil
			},
		})
		if err != nil {
			return nil, err
		}
		eng.RegisterCleanup(func() { closed.Add(1) })
		return eng, nil
	}
	_, err := goal.RunParallel(context.Background(), []goal.Spec{
		{ID: "close-a", Goal: "a", MaxSteps: 1},
		{ID: "close-b", Goal: "b", MaxSteps: 1},
	}, newEng, &goal.Store{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if opened.Load() != 2 || closed.Load() != opened.Load() {
		t.Fatalf("opened=%d closed=%d", opened.Load(), closed.Load())
	}
}

// (c) evidence from both parallel sub-steps lands in the joined State.Facts.
func TestParallelEvidenceMerge(t *testing.T) {
	dir, factory := newParallelEnv(t)
	chat := planThenWork(nil, t, func(item string) mow.Message {
		return reportCall("w", map[string]any{
			"status": "continue", "item_id": item, "item_status": "done",
			"summary": "did " + item,
			"evidence": []map[string]any{
				{"claim": "fact from " + item, "source": item + ".md", "confidence": 0.9},
			},
		})
	})
	newEng := factory(chat)
	eng, err := newEng()
	if err != nil {
		t.Fatal(err)
	}
	r := &goal.Runner{Engine: eng, Store: &goal.Store{Dir: dir}, EngineFactory: newEng}
	st, err := r.RunSpec(context.Background(), goal.Spec{ID: "ev", Goal: "two things", MaxSteps: 6, ParallelMax: 2})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var seenA, seenB bool
	for _, f := range st.Facts {
		switch f.Claim {
		case "fact from a":
			seenA = true
		case "fact from b":
			seenB = true
		}
	}
	if !seenA || !seenB {
		t.Fatalf("facts=%v want both parallel claims", st.Facts)
	}
}

// (d) a failure in one parallel item must route through the parent outcome
// handling — here Router escalates on failure, so the goal blocks (no silent pass).
func TestParallelFailureRoutesToParent(t *testing.T) {
	dir, factory := newParallelEnv(t)
	chat := planThenWork(nil, t, func(item string) mow.Message {
		if item == "b" {
			return reportCall("w", map[string]any{
				"status": "failed", "reason": "item b exploded",
			})
		}
		return reportCall("w", map[string]any{
			"status": "continue", "item_id": item, "item_status": "done", "summary": "did a",
		})
	})
	newEng := factory(chat)
	eng, err := newEng()
	if err != nil {
		t.Fatal(err)
	}
	r := &goal.Runner{
		Engine: eng, Store: &goal.Store{Dir: dir}, EngineFactory: newEng,
		Router: func(st goal.State, sr goal.StepResult) goal.StepOutcome {
			if sr.Outcome == goal.OutcomeFailed {
				return goal.OutcomeEscalate
			}
			return ""
		},
	}
	st, err := r.RunSpec(context.Background(), goal.Spec{ID: "fail", Goal: "two things", MaxSteps: 6, ParallelMax: 2})
	if err == nil {
		t.Fatalf("expected blocked error, got status=%s", st.Status)
	}
	if st.Status != goal.StatusBlocked {
		t.Fatalf("status=%s want blocked (err=%v)", st.Status, err)
	}
	var bad string
	for _, it := range st.Plan.Items {
		if it.ID == "b" {
			bad = string(it.Status)
		}
	}
	if bad != string(goal.ItemFailed) {
		t.Fatalf("item b = %q want failed", bad)
	}
}

// (a) opt-in: ParallelMax=0 (or no EngineFactory) keeps the sequential path.
func TestParallelOptInOnly(t *testing.T) {
	dir, factory := newParallelEnv(t)
	// A gate would deadlock here (5s cap) if only one call is ever in flight;
	// use a plain counter instead and assert calls never overlap.
	var inFlight, peak atomic.Int32
	chat := planThenWork(nil, t, func(item string) mow.Message {
		n := inFlight.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		inFlight.Add(-1)
		return reportCall("w", map[string]any{
			"status": "continue", "item_id": item, "item_status": "done", "summary": "did " + item,
		})
	})
	newEng := factory(chat)
	eng, err := newEng()
	if err != nil {
		t.Fatal(err)
	}
	// EngineFactory present but ParallelMax unset → sequential.
	r := &goal.Runner{Engine: eng, Store: &goal.Store{Dir: dir}, EngineFactory: newEng}
	st, err := r.RunSpec(context.Background(), goal.Spec{ID: "seq", Goal: "two things", MaxSteps: 8})
	if err != nil {
		t.Fatalf("run: %v (status=%s err=%q)", err, st.Status, st.Error)
	}
	if st.Status != goal.StatusDone {
		t.Fatalf("status=%s err=%q", st.Status, st.Error)
	}
	if peak.Load() != 1 {
		t.Fatalf("peak in-flight=%d want 1 (sequential default must not fan out)", peak.Load())
	}
}

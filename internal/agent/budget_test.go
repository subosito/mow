package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/subosito/mow/internal/llm"
)

func TestBudgetGateConstruction(t *testing.T) {
	t.Parallel()
	priced := TokenPrices{InputPerMTok: 3, OutputPerMTok: 15}

	t.Run("no limit yields no gate", func(t *testing.T) {
		t.Parallel()
		g, err := NewBudgetGate(BudgetLimits{})
		if err != nil || g != nil {
			t.Fatalf("want (nil, nil), got (%v, %v)", g, err)
		}
	})

	t.Run("token limit needs no pricing", func(t *testing.T) {
		t.Parallel()
		// The whole point of the token primitive: it works on gateways that
		// publish no prices at all.
		g, err := NewBudgetGate(BudgetLimits{MaxTokens: 1000})
		if err != nil || g == nil {
			t.Fatalf("token-only limit must build without prices: (%v, %v)", g, err)
		}
	})

	t.Run("usd limit without pricing is refused", func(t *testing.T) {
		t.Parallel()
		// A ceiling that can never fire is worse than no ceiling: the operator
		// believes they are protected. Fail at construction, not silently.
		_, err := NewBudgetGate(BudgetLimits{MaxUSD: 5})
		if err == nil {
			t.Fatal("want an error for a USD limit on an unpriced model")
		}
		if !strings.Contains(err.Error(), "max_run_tokens") {
			t.Errorf("error should point at the workable alternative: %v", err)
		}
	})

	t.Run("usd limit with pricing builds", func(t *testing.T) {
		t.Parallel()
		g, err := NewBudgetGate(BudgetLimits{MaxUSD: 5, Prices: priced})
		if err != nil || g == nil {
			t.Fatalf("want a gate: (%v, %v)", g, err)
		}
	})

	t.Run("partial pricing is not pricing", func(t *testing.T) {
		t.Parallel()
		_, err := NewBudgetGate(BudgetLimits{
			MaxUSD: 5,
			Prices: TokenPrices{InputPerMTok: 3}, // output price missing
		})
		if err == nil {
			t.Fatal("half-priced model must not satisfy a USD ceiling")
		}
	})
}

func TestBudgetGateTokenCeiling(t *testing.T) {
	t.Parallel()
	gate, err := NewBudgetGate(BudgetLimits{MaxTokens: 10_000})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("allows a call that fits", func(t *testing.T) {
		t.Parallel()
		d, err := gate(context.Background(), PreModelEvent{
			Turn:            1,
			Usage:           llm.Usage{InputTokens: 1000, OutputTokens: 200},
			SentChars:       4000, // ~1000 tok at 4 chars/tok
			CharsPerToken:   4,
			MaxOutputTokens: 2000,
		})
		if err != nil {
			t.Fatal(err)
		}
		if d.Stop {
			t.Errorf("1200 used + ~3000 projected is under 10k: %q", d.Reason)
		}
	})

	t.Run("refuses BEFORE the call that would breach", func(t *testing.T) {
		t.Parallel()
		// Admission control, not a tripwire: 9k consumed is still under the
		// limit, but this call would carry it past, so it must not be made.
		d, err := gate(context.Background(), PreModelEvent{
			Turn:            9,
			Usage:           llm.Usage{InputTokens: 9000, OutputTokens: 0},
			SentChars:       4000,
			CharsPerToken:   4,
			MaxOutputTokens: 2000,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !d.Stop {
			t.Fatal("must refuse a call whose projection breaches the limit")
		}
		for _, want := range []string{"token budget", "limit 10_000", "turn 9"} {
			if !strings.Contains(d.Reason, want) {
				t.Errorf("reason missing %q: %s", want, d.Reason)
			}
		}
	})

	t.Run("a huge history is caught on its first call", func(t *testing.T) {
		t.Parallel()
		// The case post-hoc checking gets wrong: nothing consumed yet, but
		// this single call is 500k tokens. Overshoot would exceed the entire
		// budget many times over.
		d, err := gate(context.Background(), PreModelEvent{
			Turn:          1,
			SentChars:     2_000_000, // ~500k tokens
			CharsPerToken: 4,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !d.Stop {
			t.Fatal("a single oversized call must be refused up front")
		}
	})
}

func TestBudgetGateUSDCeiling(t *testing.T) {
	t.Parallel()
	gate, err := NewBudgetGate(BudgetLimits{
		MaxUSD: 1.00,
		Prices: TokenPrices{InputPerMTok: 3, OutputPerMTok: 15},
	})
	if err != nil {
		t.Fatal(err)
	}

	d, err := gate(context.Background(), PreModelEvent{
		Turn:            20,
		Usage:           llm.Usage{InputTokens: 300_000, OutputTokens: 5_000}, // $0.90 + $0.075
		SentChars:       40_000,
		CharsPerToken:   4,
		MaxOutputTokens: 4_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Stop {
		t.Fatalf("~$0.98 consumed against a $1.00 limit must stop: %+v", d)
	}
	// The number must not be mistaken for the bill: Cost ignores cache
	// discounts and prices the reply at its full allowance.
	if !strings.Contains(d.Reason, "not your actual bill") {
		t.Errorf("USD reason must disclaim precision: %s", d.Reason)
	}
	if !strings.Contains(d.Reason, "cache discounts not applied") {
		t.Errorf("USD reason must name the over-estimate: %s", d.Reason)
	}
}

// Output is charged at its full allowance, never at a historical average: a
// ceiling needs a conservative bound, not a guess about the next reply.
func TestProjectCallIsConservative(t *testing.T) {
	t.Parallel()
	in, out := projectCall(PreModelEvent{
		SentChars: 8000, CharsPerToken: 4, MaxOutputTokens: 1500,
	})
	if in != 2000 {
		t.Errorf("input projection = %d, want 2000", in)
	}
	if out != 1500 {
		t.Errorf("output must be charged at its cap, got %d", out)
	}

	// No configured cap: assume a substantial reply rather than zero.
	_, out = projectCall(PreModelEvent{SentChars: 8000, CharsPerToken: 4})
	if out != unknownOutputTokens {
		t.Errorf("unbounded reply must not be projected as free, got %d", out)
	}

	// A calibrated ratio is honoured; dense code tokenizes worse than prose.
	in, _ = projectCall(PreModelEvent{SentChars: 8000, CharsPerToken: 2.5})
	if in != 3200 {
		t.Errorf("calibrated ratio ignored: got %d, want 3200", in)
	}
}

// End-to-end: the loop must surface ErrBudget and keep partial history.
func TestRunStopsOnBudgetGate(t *testing.T) {
	t.Parallel()
	var calls int
	// Turn 1 consumes 6100 tokens; with a 2000-token reply allowance, any
	// second call is already over the 8000 ceiling before it is made.
	gate, err := NewBudgetGate(BudgetLimits{MaxTokens: 8_000})
	if err != nil {
		t.Fatal(err)
	}
	// A tool call keeps the loop going so a second turn is attempted.
	tool := &countingWriteTool{}
	turn := 0
	chatWithTools := func(ctx context.Context, m []llm.Message, ts []llm.ToolSpec) (llm.Message, error) {
		turn++
		calls++
		if turn == 1 {
			return llm.Message{
				Role:  "assistant",
				Usage: llm.Usage{InputTokens: 6000, OutputTokens: 100},
				ToolCalls: []llm.ToolCall{{
					ID: "c1", Type: "function",
					Function: llm.FunctionCall{Name: "write", Arguments: `{"path":"a"}`},
				}},
			}, nil
		}
		return llm.Message{Role: "assistant", Content: "done"}, nil
	}

	res, err := Run(context.Background(), chatWithTools, "go", Options{
		MaxTurns:        10,
		Tools:           []Tool{tool},
		MaxOutputTokens: 2000,
		Hooks:           Hooks{PreModel: []PreModelFunc{gate}},
	})
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("want ErrBudget, got %v", err)
	}
	if calls != 1 {
		t.Errorf("the second call must never be made, got %d calls", calls)
	}
	if res.StopReason != "budget" {
		t.Errorf("StopReason = %q, want budget", res.StopReason)
	}
	// Partial work must survive: the run is over, not erased.
	if len(res.Messages) == 0 {
		t.Error("partial history must be preserved")
	}
	if res.Usage.InputTokens != 6000 {
		t.Errorf("usage should report what was actually consumed, got %+v", res.Usage)
	}
}

// First stop wins; later hooks are not consulted.
func TestPreModelFirstStopWins(t *testing.T) {
	t.Parallel()
	var second int
	stop := func(ctx context.Context, e PreModelEvent) (PreModelDecision, error) {
		return PreModelDecision{Stop: true, Reason: "first"}, nil
	}
	never := func(ctx context.Context, e PreModelEvent) (PreModelDecision, error) {
		second++
		return PreModelDecision{}, nil
	}
	chat := func(ctx context.Context, _ []llm.Message, _ []llm.ToolSpec) (llm.Message, error) {
		t.Error("chat must not be called")
		return llm.Message{}, nil
	}
	_, err := Run(context.Background(), chat, "go", Options{
		MaxTurns: 3,
		Hooks:    Hooks{PreModel: []PreModelFunc{stop, never}},
	})
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("want ErrBudget, got %v", err)
	}
	if !strings.Contains(err.Error(), "first") {
		t.Errorf("reason should propagate: %v", err)
	}
	if second != 0 {
		t.Error("hooks after a Stop must not run")
	}
}

// A hook that cannot evaluate fails the run closed: failing open on the spend
// path is exactly the failure the gate exists to prevent.
func TestPreModelErrorAbortsRun(t *testing.T) {
	t.Parallel()
	boom := errors.New("policy backend down")
	chat := func(ctx context.Context, _ []llm.Message, _ []llm.ToolSpec) (llm.Message, error) {
		t.Error("chat must not be called when the gate errored")
		return llm.Message{}, nil
	}
	_, err := Run(context.Background(), chat, "go", Options{
		MaxTurns: 3,
		Hooks: Hooks{PreModel: []PreModelFunc{
			func(ctx context.Context, e PreModelEvent) (PreModelDecision, error) {
				return PreModelDecision{}, boom
			},
		}},
	})
	if !errors.Is(err, boom) {
		t.Fatalf("want the hook error, got %v", err)
	}
}

// The event must carry what a gate needs, with correct values.
func TestPreModelEventFields(t *testing.T) {
	t.Parallel()
	var got PreModelEvent
	turn := 0
	chat := func(ctx context.Context, _ []llm.Message, _ []llm.ToolSpec) (llm.Message, error) {
		turn++
		return llm.Message{
			Role: "assistant", Content: "ok",
			Usage: llm.Usage{InputTokens: 500, OutputTokens: 50},
		}, nil
	}
	_, err := Run(context.Background(), chat, "hello", Options{
		MaxTurns:        1,
		MaxOutputTokens: 4096,
		Hooks: Hooks{PreModel: []PreModelFunc{
			func(ctx context.Context, e PreModelEvent) (PreModelDecision, error) {
				got = e
				return PreModelDecision{}, nil
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Turn != 1 {
		t.Errorf("Turn = %d, want 1 (1-based)", got.Turn)
	}
	if got.SentChars <= 0 {
		t.Error("SentChars must reflect the outgoing request")
	}
	if got.MaxOutputTokens != 4096 {
		t.Errorf("MaxOutputTokens = %d, want 4096", got.MaxOutputTokens)
	}
	if got.CharsPerToken <= 0 {
		t.Error("CharsPerToken must be a usable ratio")
	}
	// First turn: nothing billed yet.
	if got.Usage.InputTokens != 0 {
		t.Errorf("first-turn usage should be zero, got %+v", got.Usage)
	}
}

func TestThousands(t *testing.T) {
	t.Parallel()
	for in, want := range map[int]string{
		0: "0", 42: "42", 999: "999", 1000: "1_000",
		10_000: "10_000", 123_456: "123_456", 2_000_000: "2_000_000",
	} {
		if got := thousands(in); got != want {
			t.Errorf("thousands(%d) = %q, want %q", in, got, want)
		}
	}
}

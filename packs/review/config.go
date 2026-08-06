package review

import (
	"fmt"
	"strings"
	"sync"

	"github.com/subosito/mow/ext"
	"github.com/subosito/mow/extcfg"
)

// Config is extensions.review.
//
// Only the budget caps are configurable. The personas, taxonomies, and the
// two-pass workflow are the product; exposing them as config would let a
// project quietly weaken its own security review, and a reviewer reading a
// report could no longer tell what `mow sec` actually did.
//
// Budgets are different: they are a resource decision about one repository,
// and the built-in sizes cannot fit every tree. A 158-file repo truncates at
// the large budget's 120-file cap, and the only honest options were to review
// a sample or to split the run by hand.
type Config struct {
	// Budgets overrides the built-in sizes by name (small, medium, large).
	// Unset fields keep the built-in value, so a config that only raises
	// max_files does not silently reset the byte and turn caps.
	Budgets map[string]BudgetOverride `yaml:"budgets"`
}

// BudgetOverride is a partial budget. Pointer fields distinguish "not set"
// from "set to zero" — a zero cap would disable a limit entirely, which must
// be something a user asks for rather than something an omitted key does.
type BudgetOverride struct {
	MaxFiles     *int `yaml:"max_files"`
	MaxBytes     *int `yaml:"max_bytes"`
	MaxFileBytes *int `yaml:"max_file_bytes"`
	MaxTurns     *int `yaml:"max_turns"`
}

var (
	budgetMu       sync.RWMutex
	budgetOverride map[string]Budget
)

func init() {
	ext.RegisterBeforeNew(func(configPaths ...string) error {
		return loadConfig(configPaths...)
	})
}

// loadConfig reads extensions.review and installs budget overrides.
//
// A bad budget config is a hard error rather than a fallback to defaults: a
// user who set a cap and got the built-in one instead would believe they had
// reviewed more than they did, and for `mow sec` that is the wrong way to be
// wrong.
func loadConfig(configPaths ...string) error {
	var c Config
	ok, err := extcfg.DecodeSection("review", configPaths, &c)
	if err != nil {
		return fmt.Errorf("review extensions: %w", err)
	}
	if !ok || len(c.Budgets) == 0 {
		return nil
	}

	base := builtinBudgets()
	out := make(map[string]Budget, len(base))
	for name, b := range base {
		out[name] = b
	}
	for rawName, ov := range c.Budgets {
		name := strings.ToLower(strings.TrimSpace(rawName))
		b, known := base[name]
		if !known {
			return fmt.Errorf("review: unknown budget %q in extensions.review.budgets (want %s)",
				rawName, strings.Join(BudgetNames(), ", "))
		}
		if err := applyOverride(&b, name, ov); err != nil {
			return err
		}
		out[name] = b
	}

	budgetMu.Lock()
	budgetOverride = out
	budgetMu.Unlock()
	return nil
}

// applyOverride folds a partial override into b, rejecting values that would
// make a review silently useless.
func applyOverride(b *Budget, name string, ov BudgetOverride) error {
	set := func(field string, dst *int, v *int) error {
		if v == nil {
			return nil
		}
		if *v <= 0 {
			// Zero or negative would either disable the cap or gather nothing.
			// Both are almost certainly a typo, and both fail far from here:
			// an unbounded review blows a token budget, an empty one reports
			// "no findings" on a scope it never read.
			return fmt.Errorf("review: extensions.review.budgets.%s.%s must be > 0 (got %d)",
				name, field, *v)
		}
		*dst = *v
		return nil
	}
	if err := set("max_files", &b.MaxFiles, ov.MaxFiles); err != nil {
		return err
	}
	if err := set("max_bytes", &b.MaxBytes, ov.MaxBytes); err != nil {
		return err
	}
	if err := set("max_file_bytes", &b.MaxFileBytes, ov.MaxFileBytes); err != nil {
		return err
	}
	if err := set("max_turns", &b.MaxTurns, ov.MaxTurns); err != nil {
		return err
	}
	return nil
}

// resetBudgetsForTest clears installed overrides. Tests only.
func resetBudgetsForTest() {
	budgetMu.Lock()
	budgetOverride = nil
	budgetMu.Unlock()
}

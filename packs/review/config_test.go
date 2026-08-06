package review

import (
	"os"
	"path/filepath"
	"testing"
)

// writeConfig writes a config file and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// loadInto applies a config and restores the built-ins afterwards, so budget
// state cannot leak between tests in this package.
func loadInto(t *testing.T, body string) error {
	t.Helper()
	t.Cleanup(resetBudgetsForTest)
	resetBudgetsForTest()
	return loadConfig(writeConfig(t, body))
}

func TestBudgetOverrideRaisesCap(t *testing.T) {
	// The case this feature exists for: a repo larger than the large budget's
	// built-in file cap.
	if err := loadInto(t, `
extensions:
  review:
    budgets:
      large:
        max_files: 200
`); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	b, ok := LookupBudget("large")
	if !ok {
		t.Fatal("large budget missing after override")
	}
	if b.MaxFiles != 200 {
		t.Errorf("MaxFiles = %d, want 200", b.MaxFiles)
	}
	// Unmentioned caps must survive. Resetting them to zero would silently
	// remove the byte and turn limits, which is how a "raise the file cap"
	// edit turns into an unbounded run.
	builtin := builtinBudgets()["large"]
	if b.MaxBytes != builtin.MaxBytes {
		t.Errorf("MaxBytes = %d, want the built-in %d", b.MaxBytes, builtin.MaxBytes)
	}
	if b.MaxFileBytes != builtin.MaxFileBytes {
		t.Errorf("MaxFileBytes = %d, want the built-in %d", b.MaxFileBytes, builtin.MaxFileBytes)
	}
	if b.MaxTurns != builtin.MaxTurns {
		t.Errorf("MaxTurns = %d, want the built-in %d", b.MaxTurns, builtin.MaxTurns)
	}
	// Name must stay usable: it is what --budget matches and what the
	// truncation message reports.
	if b.Name != "large" {
		t.Errorf("Name = %q, want large", b.Name)
	}
}

func TestBudgetOverrideLeavesOtherSizesAlone(t *testing.T) {
	if err := loadInto(t, `
extensions:
  review:
    budgets:
      large:
        max_files: 200
`); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	for _, name := range []string{"small", "medium"} {
		got, _ := LookupBudget(name)
		want := builtinBudgets()[name]
		if got != want {
			t.Errorf("%s = %+v, want untouched %+v", name, got, want)
		}
	}
}

func TestBudgetOverrideAllFields(t *testing.T) {
	if err := loadInto(t, `
extensions:
  review:
    budgets:
      small:
        max_files: 5
        max_bytes: 50000
        max_file_bytes: 10000
        max_turns: 20
`); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	b, _ := LookupBudget("small")
	want := Budget{Name: "small", MaxFiles: 5, MaxBytes: 50_000, MaxFileBytes: 10_000, MaxTurns: 20}
	if b != want {
		t.Errorf("small = %+v, want %+v", b, want)
	}
}

// A typo in a budget name must not be silently ignored. Ignoring it would let
// a user believe they had raised a cap while the run still truncated at the
// built-in one — and for `mow sec` that is the wrong way to be wrong.
func TestBudgetOverrideUnknownNameFails(t *testing.T) {
	err := loadInto(t, `
extensions:
  review:
    budgets:
      huge:
        max_files: 500
`)
	if err == nil {
		t.Fatal("unknown budget name accepted")
	}
	// Built-ins must remain intact after a rejected config.
	if got, _ := LookupBudget("large"); got != builtinBudgets()["large"] {
		t.Errorf("large was mutated by a failed load: %+v", got)
	}
}

// Zero or negative caps are almost certainly typos, and both fail far from the
// config: an unbounded review burns a token budget, an empty one reports "no
// findings" against a scope it never read.
func TestBudgetOverrideRejectsNonPositive(t *testing.T) {
	for _, field := range []string{"max_files", "max_bytes", "max_file_bytes", "max_turns"} {
		t.Run(field, func(t *testing.T) {
			for _, v := range []string{"0", "-1"} {
				err := loadInto(t, `
extensions:
  review:
    budgets:
      medium:
        `+field+`: `+v+`
`)
				if err == nil {
					t.Errorf("%s: %s accepted", field, v)
				}
			}
		})
	}
}

func TestNoConfigKeepsBuiltins(t *testing.T) {
	t.Cleanup(resetBudgetsForTest)
	resetBudgetsForTest()

	// No file at all.
	if err := loadConfig(filepath.Join(t.TempDir(), "missing.yaml")); err != nil {
		t.Fatalf("missing config: %v", err)
	}
	// A config with no review section.
	if err := loadConfig(writeConfig(t, "extensions:\n  lsp:\n    command: gopls\n")); err != nil {
		t.Fatalf("unrelated section: %v", err)
	}
	// A review section with no budgets.
	if err := loadConfig(writeConfig(t, "extensions:\n  review: {}\n")); err != nil {
		t.Fatalf("empty review section: %v", err)
	}
	for name, want := range builtinBudgets() {
		if got, _ := LookupBudget(name); got != want {
			t.Errorf("%s = %+v, want built-in %+v", name, got, want)
		}
	}
}

// Budgets() hands out a copy: a caller that mutates the returned map must not
// be able to reconfigure every later review in the process.
func TestBudgetsReturnsCopy(t *testing.T) {
	t.Cleanup(resetBudgetsForTest)
	resetBudgetsForTest()

	m := Budgets()
	m["large"] = Budget{Name: "large", MaxFiles: 9999}
	delete(m, "small")

	if got, _ := LookupBudget("large"); got.MaxFiles == 9999 {
		t.Error("mutating the returned map changed the installed budgets")
	}
	if _, ok := LookupBudget("small"); !ok {
		t.Error("deleting from the returned map removed a budget")
	}
}

// The flag surface must reflect configured budgets, not a separate hardcoded
// list: --budget validation and the help text both read from the same table.
func TestBudgetNamesStayStableUnderOverride(t *testing.T) {
	if err := loadInto(t, `
extensions:
  review:
    budgets:
      large:
        max_files: 200
`); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	for _, name := range BudgetNames() {
		if _, ok := LookupBudget(name); !ok {
			t.Errorf("BudgetNames lists %q but LookupBudget misses it", name)
		}
	}
}

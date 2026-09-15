package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/subosito/mow/internal/policy"
)

func TestGrepRipgrepAndWalkAgree(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mk := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("src/hit.go", "package src\nfunc Wanted() {}\n")
	mk("src/miss.go", "package src\nfunc Other() {}\n")
	mk("node_modules/dep/x.go", "func Wanted() {}\n")
	mk("target/out.go", "func Wanted() {}\n")
	p := &policy.Policy{Workspace: root, MaxReadBytes: 1 << 20}

	walk := &grepTool{p: p, noRipgrep: true}
	walkOut, err := walk.Exec(context.Background(), json.RawMessage(`{"pattern":"Wanted"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(walkOut, "src/hit.go") {
		t.Fatalf("walk missed source hit: %q", walkOut)
	}
	if strings.Contains(walkOut, "node_modules") || strings.Contains(walkOut, "target/") {
		t.Fatalf("walk searched junk trees: %q", walkOut)
	}

	if lookupRipgrep() == "" {
		t.Skip("rg not on PATH")
	}
	rg := &grepTool{p: p}
	rgOut, err := rg.Exec(context.Background(), json.RawMessage(`{"pattern":"Wanted"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rgOut, "src/hit.go") {
		t.Fatalf("rg missed source hit: %q", rgOut)
	}
	if strings.Contains(rgOut, "node_modules") || strings.Contains(rgOut, "target/") {
		t.Fatalf("rg searched junk trees: %q", rgOut)
	}
}

func TestGrepMissingRipgrepFallsBackToWalk(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("func Fallback() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{Workspace: root, MaxReadBytes: 1 << 20}
	gt := &grepTool{p: p, rgBin: filepath.Join(root, "rg-not-installed")}
	out, err := gt.Exec(context.Background(), json.RawMessage(`{"pattern":"Fallback"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.go") {
		t.Fatalf("missing rg must still search: %q", out)
	}
}

func TestGrepBrokenRipgrepFallsBackToWalk(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stub")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("func Broken() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(root, "rg")
	// Exit 2 is rg's "error" (not "no matches"); Start succeeds.
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexit 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{Workspace: root, MaxReadBytes: 1 << 20}
	gt := &grepTool{p: p, rgBin: stub}
	out, err := gt.Exec(context.Background(), json.RawMessage(`{"pattern":"Broken"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.go") {
		t.Fatalf("broken rg must fall back to walk: %q", out)
	}
}

func TestGrepWalkSkipsBinary(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ok.go"), []byte("func Hit() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "x.bin"), []byte{0, 1, 2, 'H', 'i', 't'}, 0o644); err != nil {
		t.Fatal(err)
	}
	gt := &grepTool{p: &policy.Policy{Workspace: root, MaxReadBytes: 1 << 20}, noRipgrep: true}
	out, err := gt.Exec(context.Background(), json.RawMessage(`{"pattern":"Hit"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "ok.go") {
		t.Fatalf("missed text hit: %q", out)
	}
	if strings.Contains(out, "x.bin") {
		t.Fatalf("binary leaked: %q", out)
	}
}

func TestGrepRegexAndLiteralModes(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("alpha\nbeta\nalpha|beta\nSessionInfo {\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, walk := range []bool{true, false} {
		if !walk && lookupRipgrep() == "" {
			continue
		}
		tool := &grepTool{p: &policy.Policy{Workspace: root}, noRipgrep: walk}
		for _, tc := range []struct{ args, want string }{
			{`{"pattern":"alpha|beta"}`, "a.go (1)"},
			{`{"pattern":"alpha|beta","regex":true}`, "a.go (3)"},
			{`{"pattern":"SessionInfo \\{","regex":true}`, "SessionInfo {"},
			{`{"pattern":"missing|absent"}`, "Search was literal"},
		} {
			out, err := tool.Exec(t.Context(), json.RawMessage(tc.args))
			if err != nil || !strings.Contains(out, tc.want) {
				t.Fatalf("walk=%v args=%s: %q %v", walk, tc.args, out, err)
			}
		}
		if _, err := tool.Exec(t.Context(), json.RawMessage(`{"pattern":"[","regex":true}`)); err == nil {
			t.Fatal("invalid regex accepted")
		}
	}
}

func TestGrepExpandedAndFilenameResults(t *testing.T) {
	for _, walk := range []bool{true, false} {
		t.Run(map[bool]string{true: "walk", false: "ripgrep"}[walk], func(t *testing.T) {
			if !walk && lookupRipgrep() == "" {
				t.Skip("rg not installed")
			}
			root := t.TempDir()
			for i := 0; i < 12; i++ {
				body := "needle\n"
				if i == 0 {
					body = strings.Repeat("needle\n", 60)
				}
				if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("%02d.go", i)), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			tool := &grepTool{p: &policy.Policy{Workspace: root, MaxReadBytes: 1 << 20}, noRipgrep: walk}
			run := func(args string) string {
				t.Helper()
				out, err := tool.Exec(t.Context(), json.RawMessage(args))
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			expanded := run(`{"pattern":"needle","max_results":100}`)
			if !strings.Contains(expanded, "60:needle") || !strings.Contains(expanded, "11.go") {
				t.Fatalf("expanded results missing: %s", expanded)
			}
			files := run(`{"pattern":"needle","files_only":true}`)
			if len(strings.Split(files, "\n")) != 12 || strings.Contains(files, "needle") {
				t.Fatalf("files=%s", files)
			}
			capped := run(`{"pattern":"needle","files_only":true,"max_results":2}`)
			if len(strings.Split(capped, "\n")) != 3 || !strings.Contains(capped, "more matching files") {
				t.Fatalf("cap=%s", capped)
			}
			exact := run(`{"pattern":"needle","files_only":true,"max_results":12}`)
			if strings.Contains(exact, "more matching") {
				t.Fatalf("false truncation: %s", exact)
			}
			for _, invalid := range []string{`{"pattern":"needle","max_results":0}`, `{"pattern":"needle","max_results":501}`, `{"pattern":"needle","max_results":-1}`} {
				if _, err := tool.Exec(t.Context(), json.RawMessage(invalid)); err == nil {
					t.Fatalf("accepted %s", invalid)
				}
			}
		})
	}
}

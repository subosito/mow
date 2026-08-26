package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subosito/mow/internal/policy"
)

func TestMatchSegments(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		pat  string
		path string
		want bool
	}{
		// The regression: ** must match ZERO directories, so a root-level
		// file is found. filepath.Glob could never do this.
		{"doublestar matches root file", "/w/**/sample.png", "/w/sample.png", true},
		{"doublestar matches one level", "/w/**/sample.png", "/w/a/sample.png", true},
		{"doublestar matches deep", "/w/**/sample.png", "/w/a/b/c/sample.png", true},
		{"doublestar with extension", "/w/**/*.go", "/w/a/b/x.go", true},
		{"doublestar root extension", "/w/**/*.go", "/w/x.go", true},
		{"extension must still match", "/w/**/*.go", "/w/a/x.md", false},
		{"trailing doublestar matches all", "/w/internal/**", "/w/internal/a/b/c.go", true},
		{"anchored prefix respected", "/w/internal/**/*.go", "/w/other/x.go", false},
		{"single star is one level only", "/w/*/x.go", "/w/a/b/x.go", false},
		{"single star one level ok", "/w/*/x.go", "/w/a/x.go", true},
		{"literal mismatch", "/w/a.go", "/w/b.go", false},
		{"literal match", "/w/a.go", "/w/a.go", true},
		{"multiple doublestars", "/w/**/test/**/*.go", "/w/a/test/b/c.go", true},
		{"char class", "/w/**/x[0-9].go", "/w/a/x1.go", true},
		{"char class no match", "/w/**/x[0-9].go", "/w/a/xa.go", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := matchSegments(strings.Split(tc.pat, "/"), strings.Split(tc.path, "/"))
			if got != tc.want {
				t.Errorf("match(%q, %q) = %v, want %v", tc.pat, tc.path, got, tc.want)
			}
		})
	}
}

func TestHasDoublestar(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		pat  string
		want bool
	}{
		{"**/*.go", true},
		{"a/**/b", true},
		{"**", true},
		{"*.go", false},
		{"a/*/b", false},
		// A bare ** inside a segment is not a recursive wildcard; it is just
		// two stars, and filepath.Match handles it fine.
		{"a**b.go", false},
	} {
		if got := hasDoublestar(tc.pat); got != tc.want {
			t.Errorf("hasDoublestar(%q) = %v, want %v", tc.pat, got, tc.want)
		}
	}
}

// End-to-end through the tool, which is where the bug was observed.
func TestGlobToolRecursive(t *testing.T) {
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
	mk("sample.png", "root-level file")
	mk("main.go", "package main")
	mk("internal/tools/builtin.go", "package tools")
	mk("internal/a/b/c/deep.go", "package deep")
	mk("docs/readme.md", "# docs")
	// Must be skipped by a recursive walk or it drowns real results.
	mk("node_modules/dep/index.go", "package dep")

	p := &policy.Policy{Workspace: root, MaxReadBytes: 1 << 20}
	tool := &globTool{p: p}
	run := func(pattern string) string {
		t.Helper()
		out, err := tool.Exec(context.Background(),
			json.RawMessage(`{"pattern":`+mustJSON(pattern)+`}`))
		if err != nil {
			t.Fatalf("glob %q: %v", pattern, err)
		}
		return out
	}

	t.Run("finds a root-level file", func(t *testing.T) {
		// This is the exact case that returned "(no matches)".
		out := run("**/sample.png")
		if !strings.Contains(out, "sample.png") {
			t.Errorf("**/sample.png missed the root file, got %q", out)
		}
	})

	t.Run("finds files at every depth", func(t *testing.T) {
		out := run("**/*.go")
		for _, want := range []string{"main.go", "internal/tools/builtin.go", "internal/a/b/c/deep.go"} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q in:\n%s", want, out)
			}
		}
		if strings.Contains(out, "readme.md") {
			t.Error("*.go matched a .md file")
		}
	})

	t.Run("skips dependency and build trees", func(t *testing.T) {
		out := run("**/*.go")
		if strings.Contains(out, "node_modules") {
			t.Errorf("node_modules must not be walked recursively:\n%s", out)
		}
	})

	t.Run("anchors at a literal prefix", func(t *testing.T) {
		out := run("internal/**/*.go")
		if !strings.Contains(out, "internal/a/b/c/deep.go") {
			t.Errorf("missing deep file:\n%s", out)
		}
		if strings.Contains(out, "main.go") {
			t.Errorf("internal/** must not match the root main.go:\n%s", out)
		}
	})

	t.Run("plain globs still work", func(t *testing.T) {
		out := run("*.go")
		if !strings.Contains(out, "main.go") {
			t.Errorf("plain glob broke: %q", out)
		}
		// A single star is one level: it must not reach into internal/.
		if strings.Contains(out, "builtin.go") {
			t.Errorf("*.go should not recurse: %q", out)
		}
	})

	t.Run("no matches is reported cleanly", func(t *testing.T) {
		if out := run("**/*.rs"); out != "(no matches)" {
			t.Errorf("want (no matches), got %q", out)
		}
	})
}

// The recursive walk must not escape the workspace, whatever the pattern says.
func TestGlobRecursiveStaysInJail(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "in.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{Workspace: root, MaxReadBytes: 1 << 20}
	tool := &globTool{p: p}
	out, err := tool.Exec(context.Background(),
		json.RawMessage(`{"pattern":`+mustJSON(outside+"/**/*.go")+`}`))
	if err == nil && strings.Contains(out, "secret.go") {
		t.Fatalf("glob escaped the workspace jail: %q", out)
	}
}

func TestGlobRecursiveExtraRoot(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	extra := t.TempDir()
	nested := filepath.Join(extra, "pkg", "foo.rs")
	if err := os.MkdirAll(filepath.Dir(nested), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nested, []byte("fn"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{Workspace: ws, ExtraRoots: []string{extra}, MaxReadBytes: 1 << 20}
	tool := &globTool{p: p}
	out, err := tool.Exec(context.Background(),
		json.RawMessage(`{"pattern":`+mustJSON(filepath.Join(extra, "**", "*.rs"))+`}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "foo.rs") {
		t.Fatalf("extra-root ** glob missed foo.rs: %q", out)
	}
	rel, err := tool.Exec(context.Background(), json.RawMessage(`{"pattern":"**/*.rs"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rel, "foo.rs") {
		t.Fatalf("relative **/*.rs must not search extra roots: %q", rel)
	}
}

func mustJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

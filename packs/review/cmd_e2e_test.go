package review

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subosito/mow/cliutil"
)

// These tests drive the real command entry point (runCommand) against a real
// git repo and a stub OpenAI-compatible server, so the paths that only exist
// in production — ResolveScope, expandPaths, runGit, engine construction, the
// two-pass workflow, rendering, exit codes — are actually executed.

// stubServer serves the chat-completions wire, returning a candidate on pass 1
// and a verdict on pass 2. It honours "stream":true, because answering an SSE
// request with a plain JSON body yields empty text.
func stubServer(t *testing.T, candidates, verdicts string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"stub-model"}]}`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)

		reply := candidates
		if strings.Contains(body, "Pass 2 of 2") {
			reply = verdicts
		}
		// Streaming is on by default, and answering an SSE request with a
		// plain JSON body silently yields empty text — the failure mode this
		// stub must not reproduce.
		if strings.Contains(body, `"stream":true`) || strings.Contains(body, `"stream": true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			fl, _ := w.(http.Flusher)
			send := func(v any) {
				b, _ := json.Marshal(v)
				_, _ = w.Write([]byte("data: " + string(b) + "\n\n"))
				if fl != nil {
					fl.Flush()
				}
			}
			send(map[string]any{
				"id": "stub", "object": "chat.completion.chunk", "model": "stub-model",
				"choices": []any{map[string]any{
					"index": 0, "delta": map[string]any{"role": "assistant", "content": reply},
				}},
			})
			send(map[string]any{
				"id": "stub", "object": "chat.completion.chunk", "model": "stub-model",
				"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
				"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
			})
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			if fl != nil {
				fl.Flush()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "stub", "object": "chat.completion", "model": "stub-model",
			"choices": []any{map[string]any{
				"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": reply},
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// gitRepo creates a real repository with one committed file and one dirty
// file, so worktree/diff scope resolution has something to find.
func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_TERMINAL_PROMPT=0",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	write("app.go", "package app\n\nfunc Handler() error {\n\treturn nil\n}\n")
	run("add", ".")
	run("commit", "-qm", "initial")
	write("dirty.go", "package app\n\nfunc Dirty() {}\n")
	return dir
}

// runCLI invokes the real command with a stub provider and captures stdout.
func runCLI(t *testing.T, cmd string, srv *httptest.Server, workspace string, args ...string) (int, string, string) {
	t.Helper()
	t.Setenv("MOW_API_KEY", "stub")
	t.Setenv("MOW_BASE_URL", srv.URL+"/v1")
	t.Setenv("MOW_MODEL", "stub-model")
	t.Setenv("MOW_WIRE", "openai-chat-completions")

	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW

	full := append([]string{"--workspace", workspace}, args...)
	code := runCommand(cmd, full)

	outW.Close()
	errW.Close()
	os.Stdout, os.Stderr = oldOut, oldErr

	ob, _ := io.ReadAll(outR)
	eb, _ := io.ReadAll(errR)
	return code, string(ob), string(eb)
}

const candidateOne = `{"findings":[{"title":"Handler swallows the error",` +
	`"severity":"high","confidence":"high","category":"error-handling",` +
	`"path":"app.go","start_line":3,"end_line":5,` +
	`"evidence":"Handler always returns nil.",` +
	`"impact":"Callers cannot detect failure.",` +
	`"recommendation":"Return the real error."}]}`

const verdictConfirm = `{"verdicts":[{"id":"review-001","status":"confirmed",` +
	`"severity":"high","confidence":"high","reason":"reachable"}]}`

const verdictReject = `{"verdicts":[{"id":"review-001","status":"rejected",` +
	`"reason":"the error is handled by the caller"}]}`

// The headline case: a confirmed finding renders, and the exit code signals it.
func TestCommandEndToEndText(t *testing.T) {
	ws := gitRepo(t)
	srv := stubServer(t, candidateOne, verdictConfirm)

	code, out, _ := runCLI(t, "review", srv, ws, "app.go", "--quiet")
	if code != ExitFindings {
		t.Fatalf("exit = %d, want %d (findings)\n%s", code, ExitFindings, out)
	}
	for _, want := range []string{"[HIGH]", "Handler swallows the error", "app.go:3-5", "review-001"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

// A rejected candidate must not appear: this is the whole point of pass 2.
func TestCommandRejectedFindingIsSuppressed(t *testing.T) {
	ws := gitRepo(t)
	srv := stubServer(t, candidateOne, verdictReject)

	code, out, _ := runCLI(t, "review", srv, ws, "app.go", "--quiet")
	if code != ExitClean {
		t.Fatalf("exit = %d, want %d (clean)\n%s", code, ExitClean, out)
	}
	if strings.Contains(out, "Handler swallows the error") {
		t.Fatalf("rejected finding leaked into output:\n%s", out)
	}
	// "No findings" must not read as a clean bill of health.
	if !strings.Contains(out, "not proof") {
		t.Fatalf("empty report is missing its caveat:\n%s", out)
	}
}

// JSON is the machine contract: it must parse and carry the promised keys.
func TestCommandEndToEndJSON(t *testing.T) {
	ws := gitRepo(t)
	srv := stubServer(t, candidateOne, verdictConfirm)

	code, out, _ := runCLI(t, "review", srv, ws, "app.go", "--quiet", "--format", "json")
	if code != ExitFindings {
		t.Fatalf("exit = %d\n%s", code, out)
	}
	var rep map[string]any
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, out)
	}
	if rep["schema_version"] != float64(SchemaVersion) {
		t.Fatalf("schema_version = %v", rep["schema_version"])
	}
	if rep["profile"] != "general" || rep["advisory"] != true {
		t.Fatalf("envelope = %v", rep)
	}
	scope, _ := rep["scope"].(map[string]any)
	if scope["mode"] != "paths" {
		t.Fatalf("scope.mode = %v, want paths", scope["mode"])
	}
	// Regression: a path-scoped run must not advertise a git range.
	if _, ok := scope["diff"]; ok {
		t.Fatalf("path scope must not report a diff range: %v", scope)
	}
	findings, _ := rep["findings"].([]any)
	if len(findings) != 1 {
		t.Fatalf("findings = %v", findings)
	}
	f, _ := findings[0].(map[string]any)
	for _, k := range []string{"id", "fingerprint", "severity", "confidence", "category", "path", "evidence"} {
		if _, ok := f[k]; !ok {
			t.Fatalf("finding missing %q: %v", k, f)
		}
	}
	if f["severity"] != "high" {
		t.Fatalf("severity = %v", f["severity"])
	}
}

// `mow sec` must use the security profile and its id prefix end to end.
func TestSecCommandEndToEnd(t *testing.T) {
	ws := gitRepo(t)
	sec := `{"findings":[{"title":"Missing authorization check",` +
		`"severity":"critical","confidence":"high","category":"authz",` +
		`"path":"app.go","start_line":3,"end_line":5,` +
		`"evidence":"Handler performs no authorization check.",` +
		`"impact":"Any caller can invoke it.",` +
		`"recommendation":"Check the caller's permissions.",` +
		`"attack_vector":"network","asset_at_risk":"user data"}]}`
	srv := stubServer(t, sec, `{"verdicts":[{"id":"sec-001","status":"confirmed","severity":"critical","confidence":"high"}]}`)

	code, out, _ := runCLI(t, "sec", srv, ws, "app.go", "--quiet", "--format", "json")
	if code != ExitFindings {
		t.Fatalf("exit = %d\n%s", code, out)
	}
	var rep map[string]any
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if rep["profile"] != "security" {
		t.Fatalf("profile = %v, want security", rep["profile"])
	}
	run, _ := rep["run"].(map[string]any)
	if run["tool"] != "mow sec" {
		t.Fatalf("run.tool = %v, want 'mow sec'", run["tool"])
	}
	findings, _ := rep["findings"].([]any)
	f, _ := findings[0].(map[string]any)
	if id, _ := f["id"].(string); !strings.HasPrefix(id, "sec-") {
		t.Fatalf("security findings should use the sec- prefix, got %q", id)
	}
	// Profile extras must be flat, not nested under "extra".
	if f["attack_vector"] != "network" {
		t.Fatalf("attack_vector missing/nested: %v", f)
	}
}

// SARIF must be ingestible by a code-scanning dashboard.
func TestCommandEndToEndSARIF(t *testing.T) {
	ws := gitRepo(t)
	srv := stubServer(t, candidateOne, verdictConfirm)

	out := filepath.Join(t.TempDir(), "r.sarif")
	code, _, _ := runCLI(t, "review", srv, ws, "app.go", "--quiet", "--format", "sarif", "--output", out)
	if code != ExitFindings {
		t.Fatalf("exit = %d", code)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read sarif: %v", err)
	}
	var doc struct {
		Version string `json:"version"`
		Runs    []struct {
			Tool struct {
				Driver struct {
					Name  string `json:"name"`
					Rules []struct {
						ID string `json:"id"`
					} `json:"rules"`
				} `json:"driver"`
			} `json:"tool"`
			Results []struct {
				RuleID    string `json:"ruleId"`
				Level     string `json:"level"`
				Locations []struct {
					PhysicalLocation struct {
						ArtifactLocation struct {
							URI string `json:"uri"`
						} `json:"artifactLocation"`
						Region struct {
							StartLine int `json:"startLine"`
						} `json:"region"`
					} `json:"physicalLocation"`
				} `json:"locations"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("sarif is not valid JSON: %v", err)
	}
	if doc.Version != "2.1.0" || len(doc.Runs) != 1 {
		t.Fatalf("sarif version=%q runs=%d", doc.Version, len(doc.Runs))
	}
	r := doc.Runs[0]
	if len(r.Results) != 1 {
		t.Fatalf("results = %d", len(r.Results))
	}
	if got := r.Results[0].RuleID; !strings.HasPrefix(got, "mow/general/") {
		t.Fatalf("ruleId = %q, want profile-namespaced", got)
	}
	if r.Results[0].Level != "error" {
		t.Fatalf("level = %q, want error for high", r.Results[0].Level)
	}
	loc := r.Results[0].Locations[0].PhysicalLocation
	if loc.ArtifactLocation.URI != "app.go" || loc.Region.StartLine != 3 {
		t.Fatalf("location = %+v", loc)
	}
}

// --exit-zero is the advisory-CI switch: findings still render, exit is 0.
func TestCommandExitZero(t *testing.T) {
	ws := gitRepo(t)
	srv := stubServer(t, candidateOne, verdictConfirm)

	code, out, _ := runCLI(t, "review", srv, ws, "app.go", "--quiet", "--exit-zero")
	if code != ExitClean {
		t.Fatalf("exit = %d, want 0 under --exit-zero", code)
	}
	if !strings.Contains(out, "Handler swallows the error") {
		t.Fatal("--exit-zero must not suppress the findings themselves")
	}
}

// --fail-on raises the bar: a high finding must not fail a critical-only gate.
func TestCommandFailOnThreshold(t *testing.T) {
	ws := gitRepo(t)
	srv := stubServer(t, candidateOne, verdictConfirm)

	code, _, _ := runCLI(t, "review", srv, ws, "app.go", "--quiet", "--fail-on", "critical")
	if code != ExitClean {
		t.Fatalf("exit = %d, want 0 (high < critical)", code)
	}
}

// A bad git ref must be an error (2), never a clean review (0).
func TestCommandBadRefIsError(t *testing.T) {
	ws := gitRepo(t)
	srv := stubServer(t, candidateOne, verdictConfirm)

	code, _, errOut := runCLI(t, "review", srv, ws, "--diff", "no-such-ref...also-none", "--quiet")
	if code != ExitError {
		t.Fatalf("exit = %d, want %d (error)", code, ExitError)
	}
	if !strings.Contains(errOut, "mow review:") {
		t.Fatalf("stderr should name the command: %q", errOut)
	}
}

// An empty scope must warn even under --quiet, or a typo'd selector looks
// exactly like a clean review in CI.
func TestCommandEmptyScopeWarnsWhenQuiet(t *testing.T) {
	ws := gitRepo(t)
	srv := stubServer(t, candidateOne, verdictConfirm)

	code, _, errOut := runCLI(t, "review", srv, ws, "--quiet", "--exclude", "**")
	if code != ExitClean {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(errOut, "no files in scope") {
		t.Fatalf("empty scope must warn on stderr, got %q", errOut)
	}
}

// Trailing flags after a path must be parsed as flags, not as filenames.
func TestCommandTrailingFlagsAfterPath(t *testing.T) {
	ws := gitRepo(t)
	srv := stubServer(t, candidateOne, verdictConfirm)

	code, out, errOut := runCLI(t, "review", srv, ws, "app.go", "--format", "json", "--quiet")
	if code != ExitFindings {
		t.Fatalf("exit = %d\nstderr: %s", code, errOut)
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Fatalf("trailing --format was not applied:\n%s", out)
	}
}

// The default (no selector) on a dirty repo reviews uncommitted work; the
// header must say so, since default scope varies with worktree state.
func TestCommandDefaultScopeIsWorktree(t *testing.T) {
	ws := gitRepo(t)
	dirty := `{"findings":[{"title":"Dirty needs a doc comment",` +
		`"severity":"low","confidence":"medium","category":"maintainability",` +
		`"path":"dirty.go","start_line":3,"end_line":3,` +
		`"evidence":"Exported function without a comment.",` +
		`"impact":"Harder to use.","recommendation":"Add a comment."}]}`
	srv := stubServer(t, dirty, `{"verdicts":[{"id":"review-001","status":"confirmed"}]}`)

	code, out, _ := runCLI(t, "review", srv, ws, "--include-low")
	if code == ExitError {
		t.Fatalf("unexpected error exit\n%s", out)
	}
	if !strings.Contains(out, "uncommitted changes") {
		t.Fatalf("scope header should disclose worktree mode:\n%s", out)
	}
}

// ResolveScope against a real repo: the production path (runGit + os.ReadFile).
func TestResolveScopeRealRepo(t *testing.T) {
	ws := gitRepo(t)

	sc, err := ResolveScope(context.Background(), ScopeRequest{Workspace: ws, Budget: "medium"})
	if err != nil {
		t.Fatalf("ResolveScope: %v", err)
	}
	if sc.Mode != "worktree" {
		t.Fatalf("mode = %q, want worktree (repo has an untracked file)", sc.Mode)
	}
	if !sc.Git.Available || sc.Git.Commit == "" {
		t.Fatalf("git context not populated: %+v", sc.Git)
	}
	// The untracked file must carry content, not an empty slot.
	var found bool
	for _, f := range sc.Files {
		if f.Path == "dirty.go" {
			found = true
			if strings.TrimSpace(f.Content) == "" && strings.TrimSpace(f.Diff) == "" {
				t.Fatal("untracked file in scope with neither diff nor content")
			}
		}
	}
	if !found {
		t.Fatalf("untracked dirty.go not in scope: %v", sc.Paths())
	}

	// Explicit paths mode resolves through expandPaths.
	sc, err = ResolveScope(context.Background(), ScopeRequest{
		Workspace: ws, Paths: []string{"app.go"}, Budget: "small",
	})
	if err != nil {
		t.Fatalf("ResolveScope(paths): %v", err)
	}
	if sc.Mode != "paths" || len(sc.Files) != 1 || sc.Files[0].Path != "app.go" {
		t.Fatalf("paths scope = %q %v", sc.Mode, sc.Paths())
	}
	if !sc.InScope("app.go") || sc.InScope("nope.go") {
		t.Fatal("InScope disagrees with the resolved file set")
	}
	if n, ok := sc.FileLines("app.go"); !ok || n < 3 {
		t.Fatalf("FileLines = %d, want the real line count", n)
	}
}

// A directory that is not a git repo must degrade, not error: reviewing loose
// files should still work.
func TestResolveScopeNonRepo(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sc, err := ResolveScope(context.Background(), ScopeRequest{
		Workspace: dir, Paths: []string{"x.go"}, Budget: "small",
	})
	if err != nil {
		t.Fatalf("non-repo workspace should not error: %v", err)
	}
	if sc.Git.Available {
		t.Fatal("git should be unavailable outside a repo")
	}
	if len(sc.Files) != 1 {
		t.Fatalf("files = %v", sc.Paths())
	}
}

// A workspace-escaping path must be refused rather than silently reviewed.
func TestResolveScopeRejectsEscapingPath(t *testing.T) {
	ws := gitRepo(t)
	_, err := ResolveScope(context.Background(), ScopeRequest{
		Workspace: ws, Paths: []string{"../outside.go"}, Budget: "small",
	})
	if err == nil {
		t.Fatal("path outside the workspace should be rejected")
	}
}

func TestScopeModeDescription(t *testing.T) {
	cases := []struct{ mode, selector, want string }{
		{"diff", "main...HEAD", "git diff main...HEAD"},
		{"staged", "staged changes", "staged changes (git diff --cached)"},
		{"base", "origin/main...HEAD", "changes relative to origin/main...HEAD"},
		{"worktree", "uncommitted changes", "uncommitted changes in the working tree"},
		{"paths", "a.go b.go", "explicit paths: a.go b.go"},
	}
	for _, tc := range cases {
		got := scopeModeDescription(&Scope{Mode: tc.mode, Selector: tc.selector})
		if got != tc.want {
			t.Fatalf("mode %q → %q, want %q", tc.mode, got, tc.want)
		}
	}
}

// Directory arguments must expand recursively, honour --exclude, and skip
// files the budget/exclusion rules reject.
func TestResolveScopeExpandsDirectories(t *testing.T) {
	ws := gitRepo(t)
	mk := func(rel, content string) {
		t.Helper()
		full := filepath.Join(ws, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("pkg/a.go", "package pkg\n\nfunc A() {}\n")
	mk("pkg/sub/b.go", "package sub\n\nfunc B() {}\n")
	mk("pkg/vendor/dep.go", "package dep\n\nfunc D() {}\n")

	sc, err := ResolveScope(context.Background(), ScopeRequest{
		Workspace: ws, Paths: []string{"pkg"}, Budget: "medium",
	})
	if err != nil {
		t.Fatalf("ResolveScope: %v", err)
	}
	got := strings.Join(sc.Paths(), " ")
	if !strings.Contains(got, "pkg/a.go") || !strings.Contains(got, "pkg/sub/b.go") {
		t.Fatalf("directory did not expand recursively: %v", sc.Paths())
	}
	// vendor is excluded by default, with a stated reason.
	if strings.Contains(got, "vendor") {
		t.Fatalf("vendor should be excluded by default: %v", sc.Paths())
	}
	var sawReason bool
	for _, e := range sc.Excluded {
		if strings.Contains(e.Path, "vendor") && strings.TrimSpace(e.Reason) != "" {
			sawReason = true
		}
	}
	if !sawReason {
		t.Fatalf("every exclusion must carry a reason: %+v", sc.Excluded)
	}

	// --include-all overrides the default skips.
	sc, err = ResolveScope(context.Background(), ScopeRequest{
		Workspace: ws, Paths: []string{"pkg"}, Budget: "medium", IncludeAll: true,
	})
	if err != nil {
		t.Fatalf("ResolveScope(include-all): %v", err)
	}
	if !strings.Contains(strings.Join(sc.Paths(), " "), "vendor") {
		t.Fatalf("--include-all should keep vendor: %v", sc.Paths())
	}

	// An explicit --exclude glob wins.
	sc, err = ResolveScope(context.Background(), ScopeRequest{
		Workspace: ws, Paths: []string{"pkg"}, Budget: "medium",
		Excludes: []string{"pkg/sub/**"},
	})
	if err != nil {
		t.Fatalf("ResolveScope(exclude): %v", err)
	}
	if strings.Contains(strings.Join(sc.Paths(), " "), "sub/b.go") {
		t.Fatalf("--exclude glob not applied: %v", sc.Paths())
	}
}

// A missing path must fail loudly: silently reviewing nothing would report
// "no findings" for a typo.
func TestResolveScopeMissingPathErrors(t *testing.T) {
	ws := gitRepo(t)
	if _, err := ResolveScope(context.Background(), ScopeRequest{
		Workspace: ws, Paths: []string{"does-not-exist.go"}, Budget: "small",
	}); err == nil {
		t.Fatal("missing path should be an error")
	}
}

// Help must document flags that actually exist — stale help is a real defect
// for a CLI whose output is meant to be scripted.
func TestUsageDocumentsRealFlags(t *testing.T) {
	for _, cmd := range []string{"review", "sec"} {
		r, w, _ := os.Pipe()
		old := os.Stderr
		os.Stderr = w
		printUsage(cmd)
		w.Close()
		os.Stderr = old
		out, _ := io.ReadAll(r)
		help := string(out)

		if !strings.Contains(help, "mow "+cmd) {
			t.Fatalf("%s help does not name the command:\n%s", cmd, help)
		}
		// Advisory framing must survive into help.
		if !strings.Contains(strings.ToLower(help), "advisory") {
			t.Fatalf("%s help must say the output is advisory", cmd)
		}
		fs := flag.NewFlagSet("mow "+cmd, flag.ContinueOnError)
		var rf CLIFlags
		rf.Bind(fs)
		var ef cliutil.EngineFlags
		ef.Bind(fs)

		for _, name := range []string{
			"diff", "staged", "base", "format", "output", "min-severity",
			"fail-on", "budget", "exclude", "include-all", "include-low",
			"include-unverified", "no-verify", "exit-zero", "no-color", "quiet",
		} {
			if !strings.Contains(help, "--"+name) {
				t.Errorf("%s help does not mention --%s", cmd, name)
			}
			if fs.Lookup(name) == nil {
				t.Errorf("%s help documents --%s but it is not bound", cmd, name)
			}
		}
		// Profile is internal: neither command documents or binds --profile.
		if strings.Contains(help, "--profile") {
			t.Errorf("%s help must not document --profile (use mow review / mow sec)", cmd)
		}
		if fs.Lookup("profile") != nil {
			t.Errorf("%s must not bind --profile", cmd)
		}
		// Exit codes are the CI contract; they must be documented.
		if !strings.Contains(help, "Exit codes") {
			t.Errorf("%s help must document exit codes", cmd)
		}
	}
}

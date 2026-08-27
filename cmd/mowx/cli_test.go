package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var mowBinary string

// TestMain builds the real command once so these tests cover process startup,
// command dispatch, exit status, and stderr output rather than calling run
// directly.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mowx-cli-test-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	mowBinary = filepath.Join(dir, "mowx")
	build := exec.Command("go", "build", "-o", mowBinary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		panic("build mowx CLI: " + err.Error() + "\n" + string(output))
	}
	os.Exit(m.Run())
}

func TestRunEphemeralFlagDoesNotCreateSessionHistory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			fmt.Fprint(w, `{"data":[{"id":"deepseek-chat"}]}`)
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
		default:
			t.Errorf("unexpected request path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	home := t.TempDir()
	cmd := exec.Command(mowBinary, "run", "--ephemeral", "-p", "hi")
	cmd.Env = append(os.Environ(),
		"MOW_HOME="+home,
		"MOW_BASE_URL="+server.URL+"/v1",
		"MOW_API_KEY=test",
		"MOW_MODEL=deepseek-chat",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mow run --ephemeral: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "hello") {
		t.Fatalf("output missing response (%q)", output)
	}
	var sessionFiles []string
	err = filepath.WalkDir(home, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".jsonl") {
			sessionFiles = append(sessionFiles, path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessionFiles) != 0 {
		t.Fatalf("ephemeral run persisted session history: %v", sessionFiles)
	}
}

func TestModelsListsCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{
			"id":"gpt-5-mini",
			"wire":"openai-responses",
			"wires":["openai-responses","openai-chat-completions"],
			"efforts":["none","low","medium","high"],
			"default_effort":"medium"
		},{"id":"deepseek-chat","wire":"openai-chat-completions"}]}`)
	}))
	defer server.Close()

	cmd := exec.Command(mowBinary, "models", "--no-session")
	cmd.Env = append(os.Environ(),
		"MOW_HOME="+t.TempDir(),
		"MOW_BASE_URL="+server.URL+"/v1",
		"MOW_API_KEY=test",
		"MOW_MODEL=gpt-5-mini",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mow models: %v\n%s", err, output)
	}
	text := string(output)
	for _, want := range []string{
		"models  2",
		"current gpt-5-mini",
		"• gpt-5-mini",
		"openai-responses",
		"medium*",
		"deepseek-chat",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

func TestMowxVersionReportsFullBinary(t *testing.T) {
	output, code := runMow(t, "version")
	if code != 0 {
		t.Fatalf("exit code = %d\n%s", code, output)
	}
	if !strings.Contains(output, "mow ") {
		t.Fatalf("version missing product name:\n%s", output)
	}
}

func TestCLIHelp(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		want     []string
		notWant  []string
	}{
		{
			name: "top level",
			args: []string{"help"},
			want: []string{
				"mow — agent harness (library + CLI)",
				"mow run",
				"mow models",
				"mow tty",
				"mow trust",
				"mow goal",
				"mow ops",
				"mow review",
				"mow sec",
				"mow job",
				"Packs (this binary):",
			},
		},
		{
			name: "models command",
			args: []string{"help", "models"},
			want: []string{
				"mow models — list catalog models",
				"--chat",
			},
			notWant: []string{"Core:"},
		},
		{
			name: "run command",
			args: []string{"help", "run"},
			want: []string{
				"mow run — one-shot prompt",
				"mow run -p",
			},
			notWant: []string{"Core:"},
		},
		{
			name: "trust command",
			args: []string{"help", "trust"},
			want: []string{
				"mow trust — allow project .mow/config and skills",
				"mow trust --list",
			},
			notWant: []string{"Core:"},
		},
		{
			name: "tty command",
			args: []string{"help", "tty"},
			want: []string{
				"mow tty — interactive line session",
				"/model [id]",
			},
			notWant: []string{"Core:"},
		},
		{
			name: "run short help flag",
			args: []string{"run", "-h"},
			want: []string{
				"mow run — one-shot prompt",
				"mow run -p",
			},
			notWant: []string{"Core:"},
		},
		{
			name:     "unknown command",
			args:     []string{"help", "nosuchcmd"},
			wantCode: 2,
			want: []string{
				`mow help: unknown command "nosuchcmd"`,
				"run `mow help` to list available commands",
			},
		},
		{
			name:     "reserved leftover is not a prompt",
			args:     []string{"repl"},
			wantCode: 2,
			want: []string{
				`mow: unknown command "repl"`,
				"mow run -p",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, code := runMow(t, tt.args...)
			if code != tt.wantCode {
				t.Fatalf("exit code = %d, want %d\noutput:\n%s", code, tt.wantCode, output)
			}
			for _, want := range tt.want {
				if !strings.Contains(output, want) {
					t.Errorf("output missing %q:\n%s", want, output)
				}
			}
			for _, notWant := range tt.notWant {
				if strings.Contains(output, notWant) {
					t.Errorf("output unexpectedly contains %q:\n%s", notWant, output)
				}
			}
		})
	}
}

// Engine flags bound in cliutil must also be discoverable from the hand-written
// help text; --sandbox and --extra-root were bound but undocumented.
func TestHelpTextListsEngineFlags(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want []string
	}{
		{"run help", []string{"run", "-h"}, []string{"--sandbox", "--extra-root", "--allow-shell"}},
		{"top-level help", []string{"help"}, []string{"--sandbox", "--extra-root", "--allow-shell"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			output, _ := runMow(t, tt.args...)
			for _, want := range tt.want {
				if !strings.Contains(output, want) {
					t.Errorf("help missing %q:\n%s", want, output)
				}
			}
		})
	}
}

func runMow(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(mowBinary, args...)
	cmd.Env = append(os.Environ(), "MOW_HOME="+t.TempDir())
	output, err := cmd.CombinedOutput()
	if err == nil {
		return string(output), 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return string(output), exitErr.ExitCode()
	}
	t.Fatalf("run mow %q: %v", args, err)
	return "", -1
}

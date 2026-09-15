package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var mowBinary string

// TestMain builds the lean binary once so these tests cover the real binary's
// linked pack set, not an in-process approximation.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mow-lean-test-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	mowBinary = filepath.Join(dir, "mow")
	build := exec.Command("go", "build", "-o", mowBinary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		panic("build lean mow: " + err.Error() + "\n" + string(output))
	}
	os.Exit(m.Run())
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

// Stock mow links acp, tty, proc, cmdhook, mcp, goal and must not
// show custom-host application packs (ops/review/job/media) in help.
func TestLeanHelpListsLeanCommands(t *testing.T) {
	output, code := runMow(t, "help")
	if code != 0 {
		t.Fatalf("exit code = %d\n%s", code, output)
	}
	for _, want := range []string{"mow acp", "mow mcp", "mow tty", "mow goal", "Extensions (this binary):"} {
		if !strings.Contains(output, want) {
			t.Errorf("lean help missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "Packs (this binary):") {
		t.Errorf("stock binary must not link application packs:\n%s", output)
	}
	for _, notWant := range []string{"mow ops", "mow review", "mow job", "mow media", "mow proc", "mow plugin", "mow chat"} {
		if strings.Contains(output, notWant) {
			t.Errorf("help unexpectedly lists %q:\n%s", notWant, output)
		}
	}
}

func TestTTYWithoutChatAlias(t *testing.T) {
	output, code := runMow(t, "tty", "help")
	if code != 0 || !strings.Contains(output, "mow tty") {
		t.Fatalf("tty help: code=%d %s", code, output)
	}
	output, code = runMow(t, "chat")
	if code != 2 || !strings.Contains(output, `unknown command "chat"`) {
		t.Fatalf("old command must not run or become a prompt: code=%d %s", code, output)
	}
}

func TestDoctorReportsUnregisteredMediaTool(t *testing.T) {
	home := t.TempDir()
	body := "llm:\n  model: gpt-5-mini\ntools:\n  enable: [read, glob, grep, understand_image]\nextensions:\n  media:\n    understand:\n      image: gpt-5-mini\n"
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(mowBinary, "doctor")
	cmd.Env = append(os.Environ(), "MOW_HOME="+home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mow doctor: %v\n%s", err, out)
	}
	text := string(out)
	if !strings.Contains(text, "understand_image") {
		t.Fatalf("want understand_image:\n%s", text)
	}
	if !strings.Contains(text, "not registered") || !strings.Contains(text, "this binary") {
		t.Fatalf("want unregistered wording:\n%s", text)
	}
}

// Full-pack command names stay reserved even when the pack is not linked:
// `mow ops` on the lean binary must be an unknown command, not a free-form
// prompt to the model.
func TestLeanReservedFullPackTokens(t *testing.T) {
	for _, tok := range []string{"ops", "review", "media", "job"} {
		output, code := runMow(t, tok)
		if code != 2 {
			t.Errorf("%s: exit code = %d, want 2\n%s", tok, code, output)
			continue
		}
		want := `mow: unknown command "` + tok + `"`
		if !strings.Contains(output, want) {
			t.Errorf("%s: missing %q:\n%s", tok, want, output)
		}
	}
}

package main

import (
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
	dir, err := os.MkdirTemp("", "mow-cli-test-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	mowBinary = filepath.Join(dir, "mow")
	build := exec.Command("go", "build", "-o", mowBinary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		panic("build mow CLI: " + err.Error() + "\n" + string(output))
	}
	os.Exit(m.Run())
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
				"mow tty",
				"mow trust",
			},
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

package sandbox

import (
	"os/exec"
	"strings"
	"testing"
)

func TestParseMode(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Mode
		bad  bool
	}{
		{in: "", want: ModeNone},
		{in: "none", want: ModeNone},
		{in: " NONE ", want: ModeNone},
		{in: "fence", want: ModeFence},
		{in: "bwrap", bad: true},
		{in: "bubblewrap", bad: true},
		{in: "landlock", bad: true},
		{in: "docker", bad: true},
		{in: "bwrapp", bad: true},
	} {
		got, err := ParseMode(tc.in)
		if tc.bad {
			if err == nil {
				t.Errorf("ParseMode(%q): want error, got %q", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseMode(%q): %v", tc.in, err)
		} else if got != tc.want {
			t.Errorf("ParseMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNewUnknownModeErrors(t *testing.T) {
	if _, err := New(Mode("landlock"), Spec{Workspace: t.TempDir()}); err == nil {
		t.Fatal("New with unknown mode: want error")
	}
}

func TestNoneIsIdentity(t *testing.T) {
	be, err := New(ModeNone, Spec{})
	if err != nil {
		t.Fatal(err)
	}
	in := exec.Command("bash", "-lc", "echo hi")
	out, err := be.Wrap(in)
	if err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Fatal("none backend must return the same command")
	}
	if be.Mode() != ModeNone {
		t.Fatalf("Mode = %q", be.Mode())
	}
}

func TestScrubEnv(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"empty", nil, []string{}},
		{"keeps ordinary vars", []string{"PATH=/bin", "HOME=/h"}, []string{"PATH=/bin", "HOME=/h"}},
		{"drops exact names", []string{"MOW_API_KEY=a", "OPENAI_API_KEY=b", "ANTHROPIC_API_KEY=c", "TERM=xterm"}, []string{"TERM=xterm"}},
		{"drops any _API_KEY suffix", []string{"GEMINI_API_KEY=x", "FOO=1"}, []string{"FOO=1"}},
		{"keeps other secrets-looking vars", []string{"AWS_SECRET_ACCESS_KEY=k", "GITHUB_TOKEN=t"}, []string{"AWS_SECRET_ACCESS_KEY=k", "GITHUB_TOKEN=t"}},
		{"value containing the name is untouched", []string{"NOTE=MOW_API_KEY=see-docs"}, []string{"NOTE=MOW_API_KEY=see-docs"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ScrubEnv(tc.in)
			if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
				t.Fatalf("ScrubEnv(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestScrubEnvAllowList(t *testing.T) {
	in := []string{"PATH=/bin", "MOW_API_KEY=a", "OPENAI_API_KEY=b", "NPM_API_KEY=t"}
	got := ScrubEnv(in, "NPM_API_KEY", " ", "")
	want := []string{"PATH=/bin", "NPM_API_KEY=t"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("ScrubEnv allow = %v, want %v", got, want)
	}
	// The allow list is exact-name only: a near miss stays scrubbed.
	if out := ScrubEnv([]string{"OPENAI_API_KEY=b"}, "OPENAI_API_KEY_2"); len(out) != 0 {
		t.Fatalf("prefix match leaked: %v", out)
	}
}

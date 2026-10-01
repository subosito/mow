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
		{"drops _TOKEN suffix", []string{"GITHUB_TOKEN=t", "NPM_TOKEN=n", "PATH=/bin"}, []string{"PATH=/bin"}},
		{"drops _SECRET suffix", []string{"APP_SECRET=s", "APP_SECRET_KEY=k", "APP_SECRET_ACCESS_KEY=a", "PATH=/bin"}, []string{"PATH=/bin"}},
		{"drops exact cloud names", []string{"AWS_SESSION_TOKEN=t", "AWS_ACCESS_KEY_ID=id", "PATH=/bin"}, []string{"AWS_ACCESS_KEY_ID=id", "PATH=/bin"}},
		{"suffix must be whole", []string{"MY_TOKENIZER=ok", "SECRETIVE=ok", "TOKEN=ok"}, []string{"MY_TOKENIZER=ok", "SECRETIVE=ok", "TOKEN=ok"}},
		{"value containing the name is untouched", []string{"NOTE=MOW_API_KEY=see-docs"}, []string{"NOTE=MOW_API_KEY=see-docs"}},
		{"drops multiplexer pane-owner vars", []string{"HERDR_ENV=1", "HERDR_PANE_ID=p1", "HERDR_SOCKET_PATH=/s", "HERDR_BIN_PATH=/b", "TERM=xterm"}, []string{"TERM=xterm"}},
		{"pane prefix must lead", []string{"MY_HERDR_NOTE=1", "NOTE=HERDR_ENV=1"}, []string{"MY_HERDR_NOTE=1", "NOTE=HERDR_ENV=1"}},
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

func TestScrubEnvWithDenyList(t *testing.T) {
	// The deny list catches credentials whose name is not secret-shaped:
	// llm.api_key_env may point at MY_GATEWAY_KEY.
	in := []string{"PATH=/bin", "MY_GATEWAY_KEY=g", "GITHUB_TOKEN=t"}
	got := ScrubEnvWith(in, []string{"MY_GATEWAY_KEY", " ", ""}, nil)
	want := []string{"PATH=/bin"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("deny = %v, want %v", got, want)
	}
	// allow (env_passthrough) is a host decision and wins over deny.
	got = ScrubEnvWith(in, []string{"MY_GATEWAY_KEY", "GITHUB_TOKEN"}, []string{"GITHUB_TOKEN"})
	want = []string{"PATH=/bin", "GITHUB_TOKEN=t"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("allow must beat deny: %v, want %v", got, want)
	}
	// nil deny keeps the plain suffix rule.
	if got := ScrubEnvWith([]string{"OPENAI_API_KEY=a", "PATH=/bin"}, nil, nil); len(got) != 1 || got[0] != "PATH=/bin" {
		t.Fatalf("nil deny: %v", got)
	}
}

func TestIsCredentialName(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"PATH", false},
		{"MOW_API_KEY", true},
		{"AWS_SESSION_TOKEN", true},
		{"GITHUB_TOKEN", true},
		{"DB_SECRET", true},
		{"FOO_SECRET_KEY", true},
		{"AWS_SECRET_ACCESS_KEY", true},
		{"TOKEN", false},
		{"MY_TOKENIZER", false},
		{"AWS_ACCESS_KEY_ID", false},
		{"openai_api_key", false}, // environment names are case-sensitive
	} {
		if got := IsCredentialName(tc.name); got != tc.want {
			t.Errorf("IsCredentialName(%q) = %v, want %v", tc.name, got, tc.want)
		}
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

// Delegate peers keep the full environment (they may need provider keys) but
// never the parent's pane binding.
func TestDropPaneOwnerEnv(t *testing.T) {
	in := []string{"HERDR_ENV=1", "PATH=/bin", "OPENAI_API_KEY=k", "HERDR_PANE_ID=p1"}
	got := DropPaneOwnerEnv(in)
	want := []string{"PATH=/bin", "OPENAI_API_KEY=k"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("DropPaneOwnerEnv = %v, want %v", got, want)
	}
}

// env_passthrough is an explicit host decision and still wins.
func TestPaneOwnerPassthroughWins(t *testing.T) {
	got := ScrubEnvWith([]string{"HERDR_PANE_ID=p1", "HERDR_ENV=1"}, nil, []string{"HERDR_PANE_ID"})
	if len(got) != 1 || got[0] != "HERDR_PANE_ID=p1" {
		t.Fatalf("passthrough = %v", got)
	}
}

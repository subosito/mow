package engine

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFallbackVersionMatchesVERSIONFile(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("no caller")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.TrimSpace(string(raw))
	if Version != want {
		t.Fatalf("engine.Version=%q VERSION file=%q — keep them in lockstep (ldflags overrides the var on release builds)", Version, want)
	}
}

func TestReleaseVersionIgnoresPseudoAndDevel(t *testing.T) {
	cases := map[string]string{
		"":                                "",
		"(devel)":                         "",
		"v0.0.0-20260817001234-abcdef123": "",
		"0.0.0-20260817001234-abcdef123":  "",
		"v1.0.0-rc.1":                     "v1.0.0-rc.1",
		"1.0.0-rc.1":                      "1.0.0-rc.1",
		"v1.0.0":                          "v1.0.0",
	}
	for in, want := range cases {
		if got := releaseVersion(in); got != want {
			t.Errorf("releaseVersion(%q)=%q want %q", in, got, want)
		}
	}
}

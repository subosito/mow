package acp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFormatDelegateResult(t *testing.T) {
	long := "1. first finding\n" + strings.Repeat("x", delegateReplyChars+5_000) + "\n13. last finding\nVerdict: fix before merge."
	tests := []struct {
		name  string
		reply string
		spill bool
		check func(t *testing.T, out string)
	}{
		{
			name:  "short reply kept verbatim",
			reply: "all good\n",
			check: func(t *testing.T, out string) {
				if out != "agent: peer\nstop: end_turn\n\nall good" {
					t.Fatalf("got %q", out)
				}
			},
		},
		{
			name:  "findings before a closing summary line are kept",
			reply: "1. critical bug A\n2. critical bug B\nSummary: two issues found, see above.\n",
			check: func(t *testing.T, out string) {
				for _, want := range []string{"critical bug A", "critical bug B", "Summary: two issues found"} {
					if !strings.Contains(out, want) {
						t.Fatalf("missing %q in %q", want, out)
					}
				}
			},
		},
		{
			name:  "long reply keeps head and tail and spills full text",
			reply: long,
			spill: true,
			check: func(t *testing.T, out string) {
				for _, want := range []string{"1. first finding", "13. last finding", "Verdict: fix before merge.", "elided from the middle", "full reply saved to "} {
					if !strings.Contains(out, want) {
						t.Fatalf("missing %q", want)
					}
				}
				if n := len([]rune(out)); n > delegateReplyChars+1_000 {
					t.Fatalf("result too long: %d runes", n)
				}
				path := strings.TrimSpace(out[strings.Index(out, "full reply saved to ")+len("full reply saved to "):])
				path = path[:strings.Index(path, ";")]
				got, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("spill file: %v", err)
				}
				if strings.TrimSpace(string(got)) != strings.TrimSpace(long) {
					t.Fatalf("spill file does not hold the full reply (%d vs %d bytes)", len(got), len(long))
				}
			},
		},
		{
			name:  "long reply without scratch still keeps the tail and says so",
			reply: long,
			check: func(t *testing.T, out string) {
				for _, want := range []string{"13. last finding", "was not saved"} {
					if !strings.Contains(out, want) {
						t.Fatalf("missing %q", want)
					}
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := ""
			if tc.spill {
				dir = t.TempDir()
			}
			tc.check(t, formatDelegateResult("peer", "end_turn", tc.reply, dir))
		})
	}
}

func TestSpillDelegateReplyStaysInDir(t *testing.T) {
	dir := t.TempDir()
	path, err := spillDelegateReply(dir, "../../etc/evil", "body")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != filepath.Join(dir, "delegate") || strings.Contains(filepath.Base(path), "..") {
		t.Fatalf("spill escaped its directory or kept a dot-dot name: %s", path)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("spill file mode: %v %v", fi, err)
	}
}

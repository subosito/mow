package review

import (
	"context"
	"strings"
	"testing"

	"github.com/subosito/mow/slash"
)

// The point of registering in init is that linking this pack is the only thing
// a host needs to do. If these commands stop registering, /review and /sec
// vanish from every interactive host at once and no host-side test would
// notice — so assert registration here, where the init lives.
func TestSlashCommandsRegistered(t *testing.T) {
	for _, name := range []string{"review", "sec"} {
		c, ok := slash.Lookup(name)
		if !ok {
			t.Fatalf("slash.Lookup(%q) = miss; the pack must register in init", name)
		}
		if c.Run == nil {
			t.Errorf("%s: nil Run", name)
		}
		if strings.TrimSpace(c.Summary) == "" {
			t.Errorf("%s: empty Summary (shows in /help)", name)
		}
		if strings.TrimSpace(c.Usage) == "" {
			t.Errorf("%s: empty Usage (shown for /%s help)", name, name)
		}
		// Both drive the session engine, so a host must refuse them mid-turn
		// rather than interleaving two conversations on one history.
		if !c.Exclusive {
			t.Errorf("%s: want Exclusive (runs against the live engine)", name)
		}
	}
}

func TestSlashHelpNeedsNoEngine(t *testing.T) {
	// Help must work before any engine exists: a user typing `/sec help` to
	// find the flags should not get "no engine in this session".
	c, ok := slash.Lookup("sec")
	if !ok {
		t.Fatal("sec not registered")
	}
	res, err := c.Run(context.Background(), slash.Request{
		Name:    "sec",
		Invoked: "sec",
		Args:    []string{"help"},
	})
	if err != nil {
		t.Fatalf("help: %v", err)
	}
	if !strings.Contains(res.Body, "--staged") {
		t.Errorf("help body missing the flag surface:\n%s", res.Body)
	}
	// Help for /sec must name /sec — a shared implementation that echoes the
	// wrong command reads as the wrong review having run.
	if !strings.Contains(res.Body, "/sec") {
		t.Errorf("help body does not name /sec:\n%s", res.Body)
	}
}

func TestSlashProfileMatchesCommand(t *testing.T) {
	// The command name is the whole product surface for persona selection.
	general, err := profileFor("review")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	sec, err := profileFor("sec")
	if err != nil {
		t.Fatalf("sec: %v", err)
	}
	if general.Name == sec.Name {
		t.Errorf("review and sec resolved to the same profile %q", general.Name)
	}
	// An unregistered command must fail loudly rather than silently inheriting
	// the general review — a security command that quietly runs the general
	// persona is the worst possible failure here.
	if _, err := profileFor("audit"); err == nil {
		t.Error("profileFor(audit) = nil error, want failure")
	}
}

func TestSlashRejectsBadFlags(t *testing.T) {
	c, _ := slash.Lookup("review")
	_, err := c.Run(context.Background(), slash.Request{
		Name: "review", Invoked: "review",
		Args: []string{"--not-a-flag"},
	})
	if err == nil {
		t.Fatal("bad flag = nil error, want failure")
	}
	// The error should carry the usage so the user can correct it in place
	// rather than typing a second command to find the flag list.
	if !strings.Contains(err.Error(), "--budget") {
		t.Errorf("error lacks usage text: %v", err)
	}
}

func TestSlashSummaryCounts(t *testing.T) {
	tests := []struct {
		name string
		rep  *Report
		want string
	}{
		{"nil report", nil, "review · report"},
		{"clean", &Report{}, "review · report · 0 findings"},
		{
			"singular",
			&Report{Findings: []Finding{{}}, Counts: Counts{High: 1}},
			"review · report · 1 finding · high 1",
		},
		{
			"plural with breakdown",
			&Report{Findings: []Finding{{}, {}}, Counts: Counts{High: 1, Low: 1}},
			"review · report · 2 findings · high 1 · low 1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SlashSummary("review", tt.rep); got != tt.want {
				t.Errorf("SlashSummary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSlashCountsSuppressedNeverReadsClean(t *testing.T) {
	// A report with nothing shown but findings suppressed must not summarize
	// as "none": that is the one wording that turns a filtered review into a
	// false all-clear.
	got := SlashCounts(&Report{Suppressed: 3})
	if !strings.Contains(got, "3") {
		t.Errorf("SlashCounts() = %q, want the suppressed count visible", got)
	}
	if got == "none" {
		t.Error("suppressed findings summarized as none")
	}
}

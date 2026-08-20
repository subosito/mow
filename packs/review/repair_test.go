package review

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type scriptedReviewer struct {
	replies []string
	errs    []error
	prompts []string
}

func (s *scriptedReviewer) Ask(_ context.Context, _, prompt string) (string, error) {
	s.prompts = append(s.prompts, prompt)
	if len(s.replies) == 0 {
		return "", errors.New("scripted reviewer exhausted")
	}
	reply := s.replies[0]
	s.replies = s.replies[1:]
	var err error
	if len(s.errs) > 0 {
		err = s.errs[0]
		s.errs = s.errs[1:]
	}
	return reply, err
}

func (s *scriptedReviewer) Model() string { return "script" }

func TestAskAndParseCandidatesAcceptsFirstJSON(t *testing.T) {
	rev := &scriptedReviewer{replies: []string{`{"findings":[]}`}}
	env, _, err := askAndParseCandidates(context.Background(), rev, "sys", "prompt")
	if err != nil {
		t.Fatal(err)
	}
	if env.Findings == nil {
		t.Fatal("expected empty findings array")
	}
	if len(rev.prompts) != 1 {
		t.Fatalf("Ask count = %d, want 1", len(rev.prompts))
	}
}

func TestAskAndParseCandidatesRepairsProse(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MOW_HOME", dir)
	rev := &scriptedReviewer{replies: []string{
		"I reviewed the change and found nothing important.",
		`{"findings":[],"summary":"clean"}`,
	}}
	env, reply, err := askAndParseCandidates(context.Background(), rev, "sys", "prompt")
	if err != nil {
		t.Fatal(err)
	}
	if reply != `{"findings":[],"summary":"clean"}` {
		t.Fatalf("reply = %q", reply)
	}
	if env.Summary != "clean" {
		t.Fatalf("summary = %q", env.Summary)
	}
	if len(rev.prompts) != 2 {
		t.Fatalf("Ask count = %d, want 2", len(rev.prompts))
	}
	if !strings.Contains(rev.prompts[1], "not a JSON object") {
		t.Fatalf("repair prompt missing contract: %q", rev.prompts[1])
	}
	saved, _ := filepath.Glob(filepath.Join(dir, "reviews", "*-candidate.txt"))
	if len(saved) != 1 {
		t.Fatalf("saved files = %v", saved)
	}
	body, err := os.ReadFile(saved[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "found nothing important") {
		t.Fatalf("persisted body missing original prose:\n%s", body)
	}
}

func TestAskAndParseCandidatesAnnotatesEmptyReply(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MOW_HOME", dir)
	rev := &scriptedReviewer{replies: []string{"", "still prose"}}
	_, _, err := askAndParseCandidates(context.Background(), rev, "sys", "prompt")
	if err == nil {
		t.Fatal("expected parse error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "empty reply") {
		t.Fatalf("error missing snippet: %v", err)
	}
	if !strings.Contains(msg, "saved:") {
		t.Fatalf("error missing saved path: %v", err)
	}
}

func TestAskAndParseVerdictsRepairsProse(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	rev := &scriptedReviewer{replies: []string{
		"All good, confirmed nothing.",
		`{"verdicts":[]}`,
	}}
	env, _, err := askAndParseVerdicts(context.Background(), rev, "sys", "prompt")
	if err != nil {
		t.Fatal(err)
	}
	if env.Verdicts == nil {
		t.Fatal("expected empty verdicts array")
	}
}

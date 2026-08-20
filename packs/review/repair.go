package review

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxRepairReplyRunes = 4000
	maxErrorSnippet     = 400
)

// persistFailedReply writes the model body that failed the JSON contract.
// Reviews run with NoSession + Ephemeral, so this is the only durable copy.
// Path: $MOW_HOME/reviews/<utc>-<pass>.txt (MOW_HOME defaults to ~/.mow).
func persistFailedReply(pass, reply string, parseErr error) string {
	home := strings.TrimSpace(os.Getenv("MOW_HOME"))
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		home = filepath.Join(userHome, ".mow")
	}
	dir := filepath.Join(home, "reviews")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	name := time.Now().UTC().Format("20060102T150405Z") + "-" + sanitizePass(pass) + ".txt"
	path := filepath.Join(dir, name)
	var b strings.Builder
	fmt.Fprintf(&b, "pass: %s\nerror: %v\nbytes: %d\n\n", pass, parseErr, len(reply))
	b.WriteString(reply)
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return ""
	}
	return path
}

func sanitizePass(pass string) string {
	pass = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, strings.TrimSpace(pass))
	if pass == "" {
		return "reply"
	}
	return pass
}

func replySnippet(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "(empty reply)"
	}
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= maxErrorSnippet {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxErrorSnippet]) + "…"
}

func annotateParseError(err error, pass, reply, saved string) error {
	if err == nil {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n  pass: %s · reply: %d bytes · snippet: %q", pass, len(reply), replySnippet(reply))
	if saved != "" {
		fmt.Fprintf(&b, "\n  saved: %s", saved)
	}
	return fmt.Errorf("%w%s", err, b.String())
}

func repairPrompt(pass, previous string) string {
	var b strings.Builder
	b.WriteString("Your previous reply was not a JSON object. This pass cannot accept prose.\n")
	b.WriteString("Re-emit ONLY the required JSON object. No markdown, no fences, no explanation.\n")
	if pass == "candidate" {
		b.WriteString("Shape: {\"findings\":[...],\"notes\":[],\"summary\":\"...\"}. ")
		b.WriteString("findings may be []. Do not invent new issues you did not already state.\n\n")
	} else {
		b.WriteString("Shape: {\"verdicts\":[...],\"notes\":[],\"summary\":\"...\"}. ")
		b.WriteString("verdicts may be []. Do not invent new verdicts you did not already state.\n\n")
	}
	b.WriteString("Previous reply (may be truncated):\n")
	prev := strings.TrimSpace(previous)
	if n := utf8.RuneCountInString(prev); n > maxRepairReplyRunes {
		prev = string([]rune(prev)[:maxRepairReplyRunes]) + "\n…[truncated]"
	}
	if prev == "" {
		prev = "(empty)"
	}
	b.WriteString(prev)
	return b.String()
}

// askAndParseCandidates runs pass 1, persists a non-JSON body, and retries
// once with a JSON-only repair prompt. Grok/Claude often answer in prose or
// leave content empty after tools; Sol usually emits the object on the first try.
func askAndParseCandidates(ctx context.Context, rev Reviewer, system, prompt string) (candidateEnvelope, string, error) {
	reply, err := rev.Ask(ctx, system, prompt)
	if err != nil {
		return candidateEnvelope{}, reply, fmt.Errorf("review: candidate pass: %w", err)
	}
	env, err := parseCandidates(reply)
	if err == nil {
		return env, reply, nil
	}
	saved := persistFailedReply("candidate", reply, err)
	retry, retryErr := rev.Ask(ctx, system, repairPrompt("candidate", reply))
	if retryErr != nil {
		return candidateEnvelope{}, reply, annotateParseError(err, "candidate", reply, saved)
	}
	env, retryParse := parseCandidates(retry)
	if retryParse != nil {
		persistFailedReply("candidate-repair", retry, retryParse)
		return candidateEnvelope{}, retry, annotateParseError(err, "candidate", reply, saved)
	}
	return env, retry, nil
}

func askAndParseVerdicts(ctx context.Context, rev Reviewer, system, prompt string) (verdictEnvelope, string, error) {
	reply, err := rev.Ask(ctx, system, prompt)
	if err != nil {
		return verdictEnvelope{}, reply, fmt.Errorf("review: verification pass: %w", err)
	}
	env, err := parseVerdicts(reply)
	if err == nil {
		return env, reply, nil
	}
	saved := persistFailedReply("verdict", reply, err)
	retry, retryErr := rev.Ask(ctx, system, repairPrompt("verdict", reply))
	if retryErr != nil {
		return verdictEnvelope{}, reply, annotateParseError(err, "verdict", reply, saved)
	}
	env, retryParse := parseVerdicts(retry)
	if retryParse != nil {
		persistFailedReply("verdict-repair", retry, retryParse)
		return verdictEnvelope{}, retry, annotateParseError(err, "verdict", reply, saved)
	}
	return env, retry, nil
}

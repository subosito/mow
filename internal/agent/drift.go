package agent

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/subosito/mow/internal/llm"
)

// Prefix-drift detection.
//
// Providers cache a prompt by exact prefix match. Anthropic bills a cache
// write above plain input, so when an already-sent message changes, the next
// call does not append a small delta — it re-uploads everything from the
// change onward as a write.
//
// This is invisible without instrumentation: the run still succeeds, the model
// still answers, and the only symptom is the bill. Measured on one 406-turn
// session, ten such events re-uploaded ~400k tokens each and cost ~$24 on
// their own, on top of ~$12 lost to ordinary TTL lapses.
//
// The detector hashes each message as it is sent and reports the first index
// whose hash changed from the previous turn. That index names the culprit:
//
//	index 0            → the system prompt is unstable (a timestamp? a
//	                     re-rendered skills block?)
//	an old tool result → something is rewriting history after the fact
//	                     (a post-hoc stub, a truncation applied late)
//	near the tail      → ordinary compaction or a synthetic insert
//
// It costs one SHA-256 per message per turn and is only computed when a
// reporter is attached, so it is free in normal operation.

// driftReport describes one prefix-drift event.
type driftReport struct {
	// Turn is the 1-based turn on which the drift was observed.
	Turn int
	// Index is the first message index whose content changed.
	Index int
	// PrevLen / NowLen are the message counts before and after. A shrink
	// alongside drift is the signature of compaction; drift with no shrink
	// means something edited history in place, which is the surprising case.
	PrevLen, NowLen int
	// Role and Name identify the changed message (tool name when present).
	Role, Name string
	// StaleChars is how many characters sat at or after Index on the previous
	// turn — the size of the prefix the provider must now re-cache.
	StaleChars int
	// Reason is a short human-readable classification.
	Reason string
}

func (d driftReport) String() string {
	who := d.Role
	if d.Name != "" {
		who += ":" + d.Name
	}
	return fmt.Sprintf(
		"turn %d: history changed at index %d (%s); %d→%d messages, ~%d chars must be re-cached (%s)",
		d.Turn, d.Index, who, d.PrevLen, d.NowLen, d.StaleChars, d.Reason)
}

// prefixTracker remembers the shape of the last request so the next one can be
// compared against it.
type prefixTracker struct {
	hashes []uint64
	chars  []int
	roles  []string
	names  []string
}

// note compares send against the previous turn and returns a report when an
// already-sent message changed. Appends alone never report: growth is the
// normal, cheap case that the cache handles by extending.
func (p *prefixTracker) note(turn int, send []llm.Message) *driftReport {
	hashes := make([]uint64, len(send))
	chars := make([]int, len(send))
	roles := make([]string, len(send))
	names := make([]string, len(send))
	for i, m := range send {
		hashes[i] = hashMessage(m)
		chars[i] = msgChars(m)
		roles[i] = m.Role
		names[i] = m.Name
	}

	prev := p.hashes
	prevChars := p.chars
	prevRoles, prevNames := p.roles, p.names
	p.hashes, p.chars, p.roles, p.names = hashes, chars, roles, names
	if prev == nil {
		return nil // first turn: nothing to compare
	}

	n := min(len(prev), len(hashes))
	for i := 0; i < n; i++ {
		if prev[i] == hashes[i] {
			continue
		}
		stale := 0
		for j := i; j < len(prevChars); j++ {
			stale += prevChars[j]
		}
		role, name := prevRoles[i], prevNames[i]
		return &driftReport{
			Turn: turn, Index: i,
			PrevLen: len(prev), NowLen: len(hashes),
			Role: role, Name: name, StaleChars: stale,
			Reason: classifyDrift(i, len(prev), len(hashes), role),
		}
	}
	// Equal on the overlap. A shrink with no content change is a clean
	// truncation from the tail — unusual, but it still invalidates nothing
	// the provider had cached beyond the new end, so it is not reported.
	return nil
}

// classifyDrift guesses the cause so the log line points somewhere useful
// instead of merely stating that something moved.
func classifyDrift(idx, prevLen, nowLen int, role string) string {
	switch {
	case idx == 0 && role == "system":
		return "system prompt is not byte-stable across turns"
	case nowLen < prevLen:
		return "history shrank — compaction or a projection rewrite"
	case role == "tool":
		return "a tool result was rewritten after it entered history"
	case nowLen > prevLen:
		return "history grew and an earlier message also changed"
	default:
		return "an already-sent message was edited in place"
	}
}

// hashMessage folds the fields that go on the wire into one value. Anything
// omitted here would be a drift the provider sees and we do not.
func hashMessage(m llm.Message) uint64 {
	h := sha256.New()
	h.Write([]byte(m.Role))
	h.Write([]byte{0})
	h.Write([]byte(m.Name))
	h.Write([]byte{0})
	h.Write([]byte(m.Content))
	h.Write([]byte{0})
	h.Write([]byte(m.ToolCallID))
	for _, tc := range m.ToolCalls {
		h.Write([]byte{0})
		h.Write([]byte(tc.ID))
		h.Write([]byte(tc.Function.Name))
		h.Write([]byte(tc.Function.Arguments))
	}
	return binary.BigEndian.Uint64(h.Sum(nil)[:8])
}

func msgChars(m llm.Message) int {
	n := len(m.Content) + len(m.Role) + len(m.Name)
	for _, tc := range m.ToolCalls {
		n += len(tc.Function.Name) + len(tc.Function.Arguments)
	}
	return n
}

// driftSummary aggregates reports for an end-of-run line.
type driftSummary struct {
	Events     int
	StaleChars int
	Reasons    map[string]int
}

func (s *driftSummary) add(d driftReport) {
	if s.Reasons == nil {
		s.Reasons = map[string]int{}
	}
	s.Events++
	s.StaleChars += d.StaleChars
	s.Reasons[d.Reason]++
}

func (s driftSummary) String() string {
	if s.Events == 0 {
		return ""
	}
	parts := make([]string, 0, len(s.Reasons))
	for r, n := range s.Reasons {
		parts = append(parts, fmt.Sprintf("%s x%d", r, n))
	}
	return fmt.Sprintf("%d prefix-drift events, ~%d chars re-cached: %s",
		s.Events, s.StaleChars, strings.Join(parts, "; "))
}

// Package session stores agent transcripts as JSONL.
package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/subosito/mow/internal/llm"
)

// Event is one JSONL line.
type Event struct {
	Type    string    `json:"type"`
	TS      time.Time `json:"ts"`
	Role    string    `json:"role,omitempty"`
	Content string    `json:"content,omitempty"`
	// Model and Wire record the runtime selected for a persisted turn. They let
	// --continue / --session restore the session instead of current defaults.
	Model string `json:"model,omitempty"`
	Wire  string `json:"wire,omitempty"`
	// Effort records the reasoning effort active for the turn so resume can
	// restore it (empty when no effort was ever set).
	Effort string `json:"effort,omitempty"`
	// Raw message for legacy full-fidelity snapshots.
	Message *llm.Message `json:"message,omitempty"`
	// Messages is one complete replay snapshot. New writers use one snapshot
	// event per turn; readers retain Message support for existing JSONL files.
	Messages []llm.Message `json:"messages,omitempty"`
}

// Store appends events under Dir/ID.jsonl
type Store struct {
	Dir string
	ID  string
}

// Path returns the session file path. ID must pass ValidateID so Join cannot
// escape Dir (filepath.Join cleans ".." segments into a path outside Dir).
func (s *Store) Path() string {
	p, err := s.resolvedPath()
	if err != nil {
		// Callers that need the error use resolvedPath via Append/Load*;
		// Path keeps a string API for listings and keeps a non-escaping
		// fallback (basename only) if ID is malformed.
		id := filepath.Base(strings.TrimSpace(s.ID))
		if id == "" || id == "." || id == ".." {
			id = "_invalid"
		}
		return filepath.Join(s.Dir, id+".jsonl")
	}
	return p
}

// resolvedPath is Path with validation (used by read/write).
func (s *Store) resolvedPath() (string, error) {
	if s == nil || s.Dir == "" {
		return "", fmt.Errorf("session: dir required")
	}
	if err := ValidateID(s.ID); err != nil {
		return "", err
	}
	// Defense in depth: never join a multi-component id even if ValidateID
	// regresses (Join("dir", "../x.jsonl") → outside dir).
	if filepath.Base(s.ID) != s.ID {
		return "", fmt.Errorf("session: invalid id %q", s.ID)
	}
	return filepath.Join(s.Dir, s.ID+".jsonl"), nil
}

// Append writes one event. Exclusive flock serializes multi-process writers
// so concurrent hosts sharing a session id cannot interleave partial lines.
func (s *Store) Append(ev Event) error {
	if s.Dir == "" || s.ID == "" {
		return fmt.Errorf("session: dir and id required")
	}
	path, err := s.resolvedPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	if ev.TS.IsZero() {
		ev.TS = time.Now().UTC()
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	// Best-effort exclusive lock for the append. Failure to lock is not fatal
	// (some FS types lack flock); the write still proceeds.
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()
	enc := json.NewEncoder(f)
	return enc.Encode(ev)
}

// AppendSnapshot writes one complete replay snapshot as a single JSONL event.
// Keeping one event per turn avoids the open/flock/write cycle per message and
// remains append-only so older snapshots preserve rewind history on disk.
func (s *Store) AppendSnapshot(messages []llm.Message) error {
	if len(messages) == 0 {
		return nil
	}
	cp := append([]llm.Message(nil), messages...)
	return s.Append(Event{Type: "snapshot", Messages: cp})
}

// Runtime is the last model/wire/effort set recorded for a session.
type Runtime struct {
	Model  string
	Wire   string
	Effort string
}

// LoadRuntime returns the last recorded runtime. Older session files have no
// runtime events and return zero values. Model and wire are carried forward
// independently so a later partial event cannot erase either field.
func (s *Store) LoadRuntime() (Runtime, error) {
	path, err := s.resolvedPath()
	if err != nil {
		return Runtime{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Runtime{}, nil
		}
		return Runtime{}, err
	}
	var last Runtime
	for len(raw) > 0 {
		line, rest, _ := bytes.Cut(raw, []byte{'\n'})
		raw = rest
		if len(line) == 0 {
			continue
		}
		var ev Event
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		if v := strings.TrimSpace(ev.Model); v != "" {
			last.Model = v
		}
		if v := strings.TrimSpace(ev.Wire); v != "" {
			last.Wire = v
		}
		if v := strings.TrimSpace(ev.Effort); v != "" {
			last.Effort = v
		}
	}
	return last, nil
}

// LoadModel is retained for callers that only need the model.
func (s *Store) LoadModel() (string, error) {
	rt, err := s.LoadRuntime()
	return rt.Model, err
}

// LoadMessages reconstructs agent prior history for session resume.
//
// Session files append (1) simple user/assistant turns for the UI and (2) full
// message dumps after each turn. Naïvely concatenating every line duplicates
// history exponentially. We take the **last system-started message snapshot**
// when present; otherwise fall back to simple user/assistant turns only.
func (s *Store) LoadMessages() ([]llm.Message, error) {
	path, err := s.resolvedPath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	// Scan the file bytes directly. Converting it to a string and building a
	// []string for every line used to add an allocation per persisted event.
	var simple []llm.Message
	var snapshot []llm.Message
	hasSystem := false
	for len(raw) > 0 {
		line, rest, _ := bytes.Cut(raw, []byte{'\n'})
		raw = rest
		if len(line) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if len(ev.Messages) > 0 {
			snapshot = append(snapshot[:0], ev.Messages...)
			hasSystem = snapshot[0].Role == "system"
			continue
		}
		if ev.Message != nil {
			m := *ev.Message
			if m.Role == "system" {
				hasSystem = true
				// A later system snapshot replaces the preceding one; retain its
				// capacity for the following messages.
				snapshot = append(snapshot[:0], m)
			} else if hasSystem {
				snapshot = append(snapshot, m)
			}
			continue
		}
		if (ev.Role == "user" || ev.Role == "assistant") && strings.TrimSpace(ev.Content) != "" {
			simple = append(simple, llm.Message{Role: ev.Role, Content: ev.Content})
		}
	}
	if hasSystem && len(snapshot) > 0 {
		return repairToolCalls(snapshot), nil
	}
	return simple, nil
}

// repairToolCalls synthesizes results for assistant tool_calls that have no
// matching tool message. A cancelled or failed tool batch can persist an
// assistant message with N calls and fewer results; replaying that verbatim is
// rejected by both wires (tool_use without tool_result → HTTP 400), bricking
// the session. Filling the gaps keeps resume valid and tells the model what
// happened.
func repairToolCalls(msgs []llm.Message) []llm.Message {
	var out []llm.Message
	for i := 0; i < len(msgs); i++ {
		m := msgs[i]
		out = append(out, m)
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			continue
		}
		// Copy the contiguous run of tool results that follows, then fill gaps.
		answered := make(map[string]bool, len(m.ToolCalls))
		for i+1 < len(msgs) && msgs[i+1].Role == "tool" {
			i++
			out = append(out, msgs[i])
			answered[msgs[i].ToolCallID] = true
		}
		for _, tc := range m.ToolCalls {
			if tc.ID == "" || answered[tc.ID] {
				continue
			}
			out = append(out, llm.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    "error: interrupted before a result was recorded (run cancelled or failed)",
			})
		}
	}
	return out
}

// LoadTranscript returns user/assistant turns for UI display (no tool dumps,
// no system prompts). Uses the simple type=user/assistant events written each turn.
func (s *Store) LoadTranscript() ([]llm.Message, error) {
	path, err := s.resolvedPath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var turns []llm.Message
	for _, line := range splitLines(string(raw)) {
		if line == "" {
			continue
		}
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		// Prefer explicit transcript events; skip full message dumps.
		if ev.Message != nil || len(ev.Messages) > 0 {
			continue
		}
		if (ev.Role == "user" || ev.Role == "assistant") && strings.TrimSpace(ev.Content) != "" {
			turns = append(turns, llm.Message{Role: ev.Role, Content: ev.Content})
		}
	}
	return turns, nil
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// NewID returns a timestamp-based session id.
func NewID() string {
	return time.Now().UTC().Format("20060102T150405")
}

// ValidateID rejects ids that could escape the session directory when joined
// into a file path (ids may arrive from CLI flags or embedding hosts).
func ValidateID(id string) error {
	if id == "" {
		return fmt.Errorf("session: empty id")
	}
	if strings.HasPrefix(id, ".") {
		return fmt.Errorf("session: invalid id %q (use letters, digits, '-', '_', '.')", id)
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
		default:
			return fmt.Errorf("session: invalid id %q (use letters, digits, '-', '_', '.')", id)
		}
	}
	return nil
}

// Info summarizes a stored session for listing/resume UIs.
type Info struct {
	ID      string
	Updated time.Time
	Preview string // first user message, trimmed
}

// List returns sessions under dir, newest first, each with its first user
// message as a preview. Missing dir is empty, not an error.
func List(dir string) ([]Info, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Info
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Info{
			ID:      strings.TrimSuffix(e.Name(), ".jsonl"),
			Updated: fi.ModTime(),
			Preview: firstUserLine(filepath.Join(dir, e.Name())),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out, nil
}

// firstUserLine reads the first user event's content (bounded).
const previewReadLimit = 64 << 10

func firstUserLine(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, previewReadLimit))
	if err != nil {
		return ""
	}
	for len(raw) > 0 {
		line, rest, _ := bytes.Cut(raw, []byte{'\n'})
		raw = rest
		if len(line) == 0 {
			continue
		}
		var ev Event
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		if ev.Role == "user" && strings.TrimSpace(ev.Content) != "" {
			return strings.Join(strings.Fields(ev.Content), " ")
		}
	}
	return ""
}

// LatestID returns the most recently modified session id under dir (basename without .jsonl).
func LatestID(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	var best string
	var bestMod time.Time
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if best == "" || info.ModTime().After(bestMod) {
			bestMod = info.ModTime()
			best = strings.TrimSuffix(name, ".jsonl")
		}
	}
	return best, nil
}


// Package session stores agent transcripts as JSONL.
package session

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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
	// AllowWrite / AllowShell / ReadOnly record the tool policy this session
	// actually ran with. Pointers so false is distinct from "not recorded"
	// (legacy JSONL). Resume restores these unless the new process passed an
	// explicit capability flag.
	AllowWrite *bool `json:"allow_write,omitempty"`
	AllowShell *bool `json:"allow_shell,omitempty"`
	ReadOnly   *bool `json:"read_only,omitempty"`
	// Raw message for legacy full-fidelity snapshots.
	Message *llm.Message `json:"message,omitempty"`
	// Messages is a full checkpoint, or the new suffix when Type is "delta".
	Messages []llm.Message `json:"messages,omitempty"`
	// Base is the byte offset of the preceding replay record for a delta.
	// A mismatched base must never be applied to a different conversation.
	Base *int64 `json:"base,omitempty"`
}

// Store appends events under Dir/ID.jsonl. Do not copy a Store after use.
type Store struct {
	Dir string
	ID  string

	mu               sync.Mutex
	file             os.FileInfo
	end              int64
	snapshot         []byte
	snapshotOffset   int64
	snapshotMessages int
	checkpointBytes  int
}

// Path returns the session file path. ID must pass ValidateID so Join cannot
// escape Dir (filepath.Join cleans ".." segments into a path outside Dir).
// Host-facing: hosts/TUIs use it to locate a session's JSONL on disk.
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
	return s.append(ev, nil)
}

// AppendSnapshot persists a replay history. Unchanged histories are skipped;
// append-only growth writes just the new messages. Full checkpoints are
// written after edits/compaction, a writer change, or a doubling in history
// size, keeping ordinary session storage linear while bounding replay work.
func (s *Store) AppendSnapshot(messages []llm.Message) error {
	if len(messages) == 0 {
		return nil
	}
	// ponytail: serialize history to compare it; use per-message hashes if
	// profiling shows encoding dominates long-session turn commits.
	raw, err := json.Marshal(messages)
	if err != nil {
		return err
	}
	return s.append(Event{Type: "snapshot", Messages: messages}, raw)
}

func (s *Store) append(ev Event, snapshot []byte) error {
	path, err := s.resolvedPath()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	// Filesystems without flock retain full-snapshot writes. Deltas require
	// a lock so another writer cannot change the base between stat and write.
	locked := syscall.Flock(int(f.Fd()), syscall.LOCK_EX) == nil
	if locked {
		defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()
	}
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if !locked || s.file == nil || !os.SameFile(s.file, st) || s.end != st.Size() {
		s.snapshot = nil
	}
	n := len(ev.Messages)
	if snapshot != nil && len(s.snapshot) > 0 {
		if bytes.Equal(snapshot, s.snapshot) {
			return nil
		}
		// The comma must follow the exact serialized previous prefix; count
		// or byte length alone misses same-length edits and tool metadata.
		prefix := len(s.snapshot) - 1
		if len(snapshot) < 2*s.checkpointBytes && n > s.snapshotMessages &&
			len(snapshot) > prefix && snapshot[prefix] == ',' &&
			bytes.Equal(snapshot[:prefix], s.snapshot[:prefix]) {
			ev.Type = "delta"
			ev.Base = new(s.snapshotOffset)
			ev.Messages = ev.Messages[s.snapshotMessages:]
		}
	}
	// A failed/partial append invalidates the cached base until a full
	// checkpoint succeeds. Keep malformed crash tails on their own line.
	previous := s.snapshot
	s.snapshot = nil
	offset := st.Size()
	if offset > 0 {
		var last [1]byte
		if _, err := f.ReadAt(last[:], offset-1); err != nil {
			return err
		}
		if last[0] != '\n' {
			if _, err := f.Write([]byte{'\n'}); err != nil {
				return err
			}
			offset++
		}
	}
	if ev.TS.IsZero() {
		ev.TS = time.Now().UTC()
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if _, err := f.Write(raw); err != nil {
		return err
	}
	s.file, s.end = st, offset+int64(len(raw))
	if snapshot != nil {
		s.snapshot, s.snapshotOffset, s.snapshotMessages = snapshot, offset, n
		if ev.Type == "snapshot" {
			s.checkpointBytes = len(snapshot)
		}
	} else if ev.Message == nil && len(ev.Messages) == 0 && ev.Type != "delta" {
		s.snapshot = previous
	}
	return nil
}

// scanLines streams JSONL without a scanner token limit: one replay record
// can be larger than 64 KiB. Memory follows record size, not session length.
func (s *Store) scanLines(visit func(int64, []byte) error) error {
	path, err := s.resolvedPath()
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	var offset int64
	for {
		line, err := r.ReadBytes('\n')
		if err != nil && err != io.EOF {
			return err
		}
		if len(line) > 0 {
			if err := visit(offset, line); err != nil {
				return err
			}
			offset += int64(len(line))
		}
		if err == io.EOF {
			return nil
		}
	}
}

// Runtime is the last model/wire/effort/policy set recorded for a session.
type Runtime struct {
	Model      string
	Wire       string
	Effort     string
	AllowWrite bool
	AllowShell bool
	ReadOnly   bool
	// HasPolicy is true when at least one capability field was persisted.
	// Older session files have none; resume then keeps config defaults.
	HasPolicy bool
}

// LoadRuntime returns the last recorded runtime. Older session files have no
// runtime events and return zero values. Model and wire are carried forward
// independently so a later partial event cannot erase either field.
func (s *Store) LoadRuntime() (Runtime, error) {
	var last Runtime
	err := s.scanLines(func(_ int64, line []byte) error {
		var ev struct {
			Model      string
			Wire       string
			Effort     string
			AllowWrite *bool `json:"allow_write"`
			AllowShell *bool `json:"allow_shell"`
			ReadOnly   *bool `json:"read_only"`
		}
		if json.Unmarshal(line, &ev) != nil {
			return nil
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
		if ev.AllowWrite != nil {
			last.AllowWrite = *ev.AllowWrite
			last.HasPolicy = true
		}
		if ev.AllowShell != nil {
			last.AllowShell = *ev.AllowShell
			last.HasPolicy = true
		}
		if ev.ReadOnly != nil {
			last.ReadOnly = *ev.ReadOnly
			last.HasPolicy = true
		}
		return nil
	})
	return last, err
}

// LoadMessages reconstructs the last checkpoint plus its deltas. Legacy
// per-message snapshots and simple user/assistant transcripts remain readable.
func (s *Store) LoadMessages() ([]llm.Message, error) {
	var simple, snapshot []llm.Message
	var pending json.RawMessage
	lastOffset := int64(-1)
	materialize := func() error {
		if len(pending) == 0 {
			return nil
		}
		err := json.Unmarshal(pending, &snapshot)
		pending = nil
		return err
	}
	err := s.scanLines(func(offset int64, line []byte) error {
		var ev struct {
			Role     string
			Content  string
			Type     string
			Message  *llm.Message
			Messages json.RawMessage
			Base     *int64
		}
		if json.Unmarshal(line, &ev) != nil {
			return nil // tolerate an incomplete final record after a crash
		}
		if ev.Type == "delta" {
			if ev.Base == nil || *ev.Base != lastOffset {
				return fmt.Errorf("session: delta at %d has no matching replay base", offset)
			}
			if err := materialize(); err != nil {
				return err
			}
			var suffix []llm.Message
			if err := json.Unmarshal(ev.Messages, &suffix); err != nil {
				return err
			}
			snapshot = append(snapshot, suffix...)
			lastOffset = offset
			return nil
		}
		if len(ev.Messages) > 0 && !bytes.Equal(ev.Messages, []byte("null")) && !bytes.Equal(ev.Messages, []byte("[]")) {
			// Superseded full checkpoints need not decode every message.
			pending, snapshot, lastOffset = ev.Messages, nil, offset
			return nil
		}
		if ev.Message != nil {
			m := *ev.Message
			if m.Role == "system" {
				pending = nil
				snapshot = []llm.Message{m}
			} else {
				if err := materialize(); err != nil {
					return err
				}
				if len(snapshot) > 0 && snapshot[0].Role == "system" {
					snapshot = append(snapshot, m)
				}
			}
			lastOffset = offset
			return nil
		}
		if (ev.Role == "user" || ev.Role == "assistant") && strings.TrimSpace(ev.Content) != "" {
			simple = append(simple, llm.Message{Role: ev.Role, Content: ev.Content})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := materialize(); err != nil {
		return nil, err
	}
	if len(snapshot) > 0 && snapshot[0].Role == "system" {
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
	// First sanitize malformed/duplicate calls and orphan results. Provider
	// streams may contain an empty terminal accumulator; replay must never send
	// a tool call without a stable ID and function name.
	cleaned := make([]llm.Message, 0, len(msgs))
	validIDs := map[string]bool{}
	for _, m := range msgs {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			seen := map[string]bool{}
			calls := m.ToolCalls[:0]
			for _, tc := range m.ToolCalls {
				id, name := strings.TrimSpace(tc.ID), strings.TrimSpace(tc.Function.Name)
				// Signature-only legacy calls are retained for round-trip fidelity.
				// Drop only wire-invalid ordinary calls; pathological valid batches
				// are refused before execution, not rewritten from durable history.
				if (id == "" || name == "") && strings.TrimSpace(tc.ThoughtSignature) == "" || seen[id] {
					continue
				}
				seen[id], validIDs[id] = true, true
				tc.ID, tc.Function.Name = id, name
				if strings.TrimSpace(tc.Function.Arguments) == "" && strings.TrimSpace(tc.ThoughtSignature) == "" {
					tc.Function.Arguments = "{}"
				}
				calls = append(calls, tc)
			}
			m.ToolCalls = calls
		}
		if m.Role == "tool" && m.ToolCallID != "" && !validIDs[m.ToolCallID] {
			continue
		}
		cleaned = append(cleaned, m)
	}
	msgs = cleaned
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

// LoadTranscriptEvents returns user/assistant turns for UI display (no tool
// dumps, no system prompts), preserving event timestamps for host resume.
func (s *Store) LoadTranscriptEvents() ([]Event, error) {
	var turns []Event
	err := s.scanLines(func(_ int64, line []byte) error {
		var role struct{ Role string }
		if json.Unmarshal(line, &role) != nil || (role.Role != "user" && role.Role != "assistant") {
			return nil
		}
		var ev Event
		if json.Unmarshal(line, &ev) == nil && ev.Message == nil && len(ev.Messages) == 0 && strings.TrimSpace(ev.Content) != "" {
			turns = append(turns, ev)
		}
		return nil
	})
	return turns, err
}

// NewID returns a timestamp-based session id with a short random suffix
// (20060102T150405-a1b2c3). The stamp keeps ids sortable by creation time;
// the suffix keeps two engines started in the same second — same workspace,
// same session dir — from appending to one JSONL file. Ids without the
// suffix (older files, Options.SessionID) stay valid.
func NewID() string {
	stamp := time.Now().UTC().Format("20060102T150405")
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return stamp
	}
	return stamp + "-" + hex.EncodeToString(b[:])
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
	slices.SortFunc(out, func(a, b Info) int { return b.Updated.Compare(a.Updated) })
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

// LatestID returns the most recently modified session id under dir (basename
// without .jsonl). Callers must pass the already workspace-scoped directory;
// this function never scans sibling project hashes.
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

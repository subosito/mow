// Package workflow provides a minimal host-owned, durable workflow runtime.
// Definitions are registered in Go; this package deliberately has no scripting,
// distributed queue, scheduler, or deployment authority.
package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/subosito/mow"
)

const (
	DefaultMaxJSONBytes = 64 << 10
	maxIDBytes          = 128
	maxPhases           = 64
	maxPromptTurns      = 64
)

type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusBlocked   Status = "blocked"
)

type PhaseStatus string

const (
	PhasePending   PhaseStatus = "pending"
	PhaseRunning   PhaseStatus = "running"
	PhaseSucceeded PhaseStatus = "succeeded"
	PhaseFailed    PhaseStatus = "failed"
	PhaseBlocked   PhaseStatus = "blocked"
)

type PhaseKind string

const (
	PhasePrompt PhaseKind = "prompt"
	PhaseVerify PhaseKind = "verify"
)

type SideEffect string

const (
	EffectReadOnly  SideEffect = "read_only"
	EffectWorkspace SideEffect = "workspace"
)

type EventType string

const (
	EventRunStart   EventType = "workflow.run.start"
	EventPhaseStart EventType = "workflow.phase.start"
	EventPhaseEnd   EventType = "workflow.phase.end"
	EventRunBlocked EventType = "workflow.run.blocked"
	EventRunDone    EventType = "workflow.run.done"
	EventRunFailed  EventType = "workflow.run.failed"
)

type Event struct {
	Type       EventType `json:"type"`
	RunID      string    `json:"run_id"`
	Definition string    `json:"definition"`
	Phase      string    `json:"phase,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	At         time.Time `json:"at"`
}
type EventFunc func(Event)

// VerifyFunc is trusted host code. It receives the prior JSON and must return a
// bounded JSON result or an error. Candidate code cannot replace it at runtime.
type VerifyFunc func(context.Context, json.RawMessage) (json.RawMessage, error)

type PromptSpec struct {
	Prompt       string
	ReadOnly     bool
	AllowedTools []string
	MaxTurns     int
	SystemAppend string
}

// PromptPhase constructs a typed model phase. The prompt must return one JSON
// value; it becomes the next phase's input.
func PromptPhase(id, prompt string, readOnly bool, allowedTools ...string) Phase {
	return Phase{ID: id, Kind: PhasePrompt, Effect: map[bool]SideEffect{true: EffectReadOnly, false: EffectWorkspace}[readOnly], Prompt: &PromptSpec{Prompt: prompt, ReadOnly: readOnly, AllowedTools: append([]string(nil), allowedTools...)}}
}

// VerifyPhase constructs a trusted deterministic verification phase.
func VerifyPhase(id string, verify VerifyFunc) Phase {
	return Phase{ID: id, Kind: PhaseVerify, Effect: EffectReadOnly, Verify: verify}
}

type Phase struct {
	ID       string
	Kind     PhaseKind
	Prompt   *PromptSpec
	Verify   VerifyFunc
	MaxBytes int
	Effect   SideEffect
}
type Definition struct {
	Name     string
	Version  string
	Phases   []Phase
	MaxBytes int
}

var (
	ErrInvalid  = errors.New("workflow: invalid definition or input")
	ErrNotFound = errors.New("workflow: not found")
	ErrBlocked  = errors.New("workflow: blocked; reconcile before retrying")
	ErrRunning  = errors.New("workflow: run is already locked")
	ErrExists   = errors.New("workflow: run already exists")
)
var registry = struct {
	sync.RWMutex
	m map[string]Definition
}{m: map[string]Definition{}}

func registryKey(name, version string) string { return name + "\x00" + version }

func Register(d Definition) error {
	if err := validateDefinition(d); err != nil {
		return err
	}
	d.Phases = clonePhases(d.Phases)
	registry.Lock()
	defer registry.Unlock()
	key := registryKey(d.Name, d.Version)
	if _, ok := registry.m[key]; ok {
		return fmt.Errorf("%w: duplicate definition", ErrInvalid)
	}
	registry.m[key] = d
	return nil
}
func Lookup(name, version string) (Definition, bool) {
	registry.RLock()
	defer registry.RUnlock()
	d, ok := registry.m[registryKey(name, version)]
	d.Phases = clonePhases(d.Phases)
	return d, ok
}
func Definitions() []Definition {
	registry.RLock()
	defer registry.RUnlock()
	out := make([]Definition, 0, len(registry.m))
	for _, d := range registry.m {
		d.Phases = clonePhases(d.Phases)
		out = append(out, d)
	}
	slices.SortFunc(out, func(a, b Definition) int {
		if n := strings.Compare(a.Name, b.Name); n != 0 {
			return n
		}
		return strings.Compare(a.Version, b.Version)
	})
	return out
}
func clonePhases(in []Phase) []Phase {
	out := append([]Phase(nil), in...)
	for i := range out {
		if out[i].Prompt != nil {
			p := *out[i].Prompt
			p.AllowedTools = append([]string(nil), p.AllowedTools...)
			out[i].Prompt = &p
		}
	}
	return out
}

func validateID(s string) error {
	if s == "" || len(s) > maxIDBytes {
		return fmt.Errorf("%w: invalid id", ErrInvalid)
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return fmt.Errorf("%w: invalid id", ErrInvalid)
		}
	}
	return nil
}
func validateDefinition(d Definition) error {
	if validateID(d.Name) != nil || validateID(d.Version) != nil || len(d.Phases) == 0 || len(d.Phases) > maxPhases {
		return fmt.Errorf("%w: definition identity or phase count", ErrInvalid)
	}
	max := d.MaxBytes
	if max == 0 {
		max = DefaultMaxJSONBytes
	}
	if max < 1 || max > 1<<20 {
		return fmt.Errorf("%w: max bytes", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, p := range d.Phases {
		if validateID(p.ID) != nil || seen[p.ID] {
			return fmt.Errorf("%w: duplicate/invalid phase", ErrInvalid)
		}
		seen[p.ID] = true
		if p.MaxBytes < 0 || p.MaxBytes > max {
			return fmt.Errorf("%w: phase limit", ErrInvalid)
		}
		switch p.Kind {
		case PhasePrompt:
			if p.Prompt == nil || p.Verify != nil || strings.TrimSpace(p.Prompt.Prompt) == "" || p.Prompt.MaxTurns < 0 || p.Prompt.MaxTurns > maxPromptTurns {
				return fmt.Errorf("%w: prompt phase", ErrInvalid)
			}
			for _, tool := range p.Prompt.AllowedTools {
				if strings.TrimSpace(tool) == "" {
					return fmt.Errorf("%w: allowed tool", ErrInvalid)
				}
			}
			if p.Effect == "" {
				p.Effect = EffectReadOnly
			}
			if p.Effect == EffectReadOnly && !p.Prompt.ReadOnly {
				return fmt.Errorf("%w: read-only effect requires read-only prompt", ErrInvalid)
			}
		case PhaseVerify:
			if p.Verify == nil || p.Prompt != nil || p.Effect != EffectReadOnly && p.Effect != "" {
				return fmt.Errorf("%w: verify phase", ErrInvalid)
			}
		default:
			return fmt.Errorf("%w: phase kind", ErrInvalid)
		}
		if p.Effect != EffectReadOnly && p.Effect != EffectWorkspace && p.Effect != "" {
			return fmt.Errorf("%w: side effect", ErrInvalid)
		}
	}
	return nil
}
func boundedJSON(raw []byte, max int) (json.RawMessage, error) {
	if max <= 0 {
		max = DefaultMaxJSONBytes
	}
	if len(raw) == 0 || len(raw) > max {
		return nil, fmt.Errorf("%w: JSON size", ErrInvalid)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("%w: JSON", ErrInvalid)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("%w: trailing JSON", ErrInvalid)
	}
	switch v.(type) {
	case map[string]any, []any, string, bool, float64, nil:
	default:
		return nil, fmt.Errorf("%w: JSON value", ErrInvalid)
	}
	return append(json.RawMessage(nil), raw...), nil
}

type Runner struct {
	Engine  *mow.Engine
	Store   *Store
	OnEvent EventFunc
}

func (r *Runner) store() *Store {
	if r.Store == nil {
		return &Store{}
	}
	return r.Store
}
func (r *Runner) emit(e Event) {
	if r.OnEvent != nil {
		r.OnEvent(e)
	}
}
func (r *Runner) Run(ctx context.Context, id, name, version string, input json.RawMessage) (State, error) {
	d, ok := Lookup(name, version)
	if !ok {
		return State{}, ErrNotFound
	}
	return r.run(ctx, id, d, input, false)
}
func (r *Runner) Resume(ctx context.Context, id string) (State, error) {
	st, err := r.store().Load(id)
	if err != nil {
		return State{}, err
	}
	d, ok := Lookup(st.Definition, st.Version)
	if !ok {
		return State{}, ErrNotFound
	}
	return r.run(ctx, id, d, st.Input, true)
}

func (r *Runner) run(ctx context.Context, id string, d Definition, input json.RawMessage, resume bool) (State, error) {
	if validateID(id) != nil || validateDefinition(d) != nil {
		return State{}, ErrInvalid
	}
	max := d.MaxBytes
	if max == 0 {
		max = DefaultMaxJSONBytes
	}
	in, err := boundedJSON(input, max)
	if err != nil {
		return State{}, err
	}
	store := r.store()
	unlock, err := store.lock(id)
	if err != nil {
		return State{}, err
	}
	defer unlock()
	st, err := store.load(id)
	if err != nil {
		if !errors.Is(err, ErrNotFound) || resume {
			return State{}, err
		}
		st = State{ID: id, Definition: d.Name, Version: d.Version, Status: StatusPending, Input: in, CreatedAt: time.Now().UTC(), Phases: make([]PhaseState, len(d.Phases))}
		for i, p := range d.Phases {
			st.Phases[i] = PhaseState{ID: p.ID, Kind: p.Kind, Status: PhasePending}
		}
		if err = store.save(st); err != nil {
			return State{}, err
		}
	} else {
		if !resume {
			return st, ErrExists
		}
		if st.Definition != d.Name || st.Version != d.Version || !bytesEqualJSON(st.Input, in) || len(st.Phases) != len(d.Phases) {
			return State{}, fmt.Errorf("%w: definition/input mismatch", ErrInvalid)
		}
		for i, p := range d.Phases {
			if st.Phases[i].ID != p.ID || st.Phases[i].Kind != p.Kind {
				return State{}, fmt.Errorf("%w: phase layout mismatch", ErrInvalid)
			}
		}
		if st.Status == StatusBlocked {
			return st, ErrBlocked
		}
		if st.Status == StatusSucceeded || st.Status == StatusFailed {
			return st, nil
		}
	}
	r.emit(Event{Type: EventRunStart, RunID: id, Definition: d.Name, At: time.Now().UTC()})
	for i, p := range d.Phases {
		ps := &st.Phases[i]
		if ps.Status == PhaseSucceeded {
			continue
		}
		if ps.Status == PhaseRunning {
			ps.Status = PhaseBlocked
			ps.ErrorCode = "interrupted_uncertain_phase"
			ps.EndedAt = time.Now().UTC()
			st.Status = StatusBlocked
			st.CurrentPhase = ps.ID
			if saveErr := store.save(st); saveErr != nil {
				return st, saveErr
			}
			r.emit(Event{Type: EventRunBlocked, RunID: id, Definition: d.Name, Phase: ps.ID, Reason: ps.ErrorCode, At: time.Now().UTC()})
			return st, ErrBlocked
		}
		ps.Status = PhaseRunning
		ps.Attempts++
		ps.StartedAt = time.Now().UTC()
		ps.EndedAt = time.Time{}
		ps.ErrorCode = ""
		st.CurrentPhase = ps.ID
		st.Status = StatusRunning
		if err = store.save(st); err != nil {
			return st, err
		}
		r.emit(Event{Type: EventPhaseStart, RunID: id, Definition: d.Name, Phase: ps.ID, At: time.Now().UTC()})
		raw := st.Input
		if i > 0 {
			raw = st.Phases[i-1].Output
		}
		limit := p.MaxBytes
		if limit == 0 {
			limit = max
		}
		if p.Kind == PhasePrompt {
			if r.Engine == nil {
				return r.fail(store, st, i, "engine_unavailable", fmt.Errorf("%w: nil engine", ErrInvalid))
			}
			maxTurns := p.Prompt.MaxTurns
			if maxTurns == 0 {
				maxTurns = 16
			}
			opts := mow.PromptOpts{ReadOnly: p.Prompt.ReadOnly, AllowedTools: append([]string(nil), p.Prompt.AllowedTools...), SystemAppend: p.Prompt.SystemAppend, MaxTurns: maxTurns}
			if p.Prompt.MaxTurns > 0 {
				_ = opts
			}
			res, runErr := r.Engine.PromptWith(ctx, p.Prompt.Prompt+"\n\nWorkflow input JSON:\n"+string(raw), opts)
			if runErr != nil {
				return r.fail(store, st, i, errorCode(runErr), runErr)
			}
			raw = json.RawMessage(res.Text)
		} else {
			raw, err = p.Verify(ctx, raw)
			if err != nil {
				return r.fail(store, st, i, "verification_failed", err)
			}
		}
		raw, err = boundedJSON(raw, limit)
		if err != nil {
			return r.fail(store, st, i, "invalid_phase_output", err)
		}
		ps.Output = raw
		ps.Status = PhaseSucceeded
		ps.EndedAt = time.Now().UTC()
		st.Status = StatusPending
		st.CurrentPhase = ""
		if err = store.save(st); err != nil {
			return st, err
		}
		r.emit(Event{Type: EventPhaseEnd, RunID: id, Definition: d.Name, Phase: ps.ID, At: time.Now().UTC()})
	}
	st.Status = StatusSucceeded
	st.CurrentPhase = ""
	if err = store.save(st); err != nil {
		return st, err
	}
	r.emit(Event{Type: EventRunDone, RunID: id, Definition: d.Name, At: time.Now().UTC()})
	return st, nil
}
func (r *Runner) fail(store *Store, st State, i int, code string, cause error) (State, error) {
	ps := &st.Phases[i]
	ps.Status = PhaseFailed
	ps.ErrorCode = code
	ps.EndedAt = time.Now().UTC()
	st.Status = StatusFailed
	st.CurrentPhase = ps.ID
	if err := store.save(st); err != nil {
		return st, err
	}
	r.emit(Event{Type: EventRunFailed, RunID: st.ID, Definition: st.Definition, Phase: ps.ID, Reason: code, At: time.Now().UTC()})
	return st, cause
}
func errorCode(err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "cancelled"
	}
	return "prompt_failed"
}
func bytesEqualJSON(a, b []byte) bool {
	var av, bv any
	if json.Unmarshal(a, &av) != nil || json.Unmarshal(b, &bv) != nil {
		return false
	}
	aa, _ := json.Marshal(av)
	bb, _ := json.Marshal(bv)
	return string(aa) == string(bb)
}

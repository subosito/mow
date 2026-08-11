package review

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// EnsembleMember is one named, read-only reviewer in an EnsembleReviewer.
// Name is recorded on findings as the backward-compatible "reviewer" extra
// field. Members must themselves enforce read-only behavior; NewEngineReviewer
// does so for mow engines.
type EnsembleMember struct {
	Name     string
	Reviewer Reviewer
}

// EnsembleReviewer runs independent candidate discovery with several reviewers.
// Candidate replies are individually parsed before their findings are combined,
// then the normal workflow validates and verifies the combined candidates. The
// first member is also used for verification, keeping the existing independent
// second pass rather than asking peers to vote on their own claims.
type EnsembleReviewer struct {
	members     []EnsembleMember
	maxParallel int
}

var _ Reviewer = (*EnsembleReviewer)(nil)

// NewEnsembleReviewer constructs a candidate-review ensemble. maxParallel
// bounds simultaneous Ask calls; zero uses the number of members. Member names
// must be unique after trimming and are used as finding provenance.
func NewEnsembleReviewer(members []EnsembleMember, maxParallel int) (*EnsembleReviewer, error) {
	if len(members) == 0 {
		return nil, fmt.Errorf("review: ensemble requires at least one member")
	}
	out := make([]EnsembleMember, len(members))
	seen := make(map[string]bool, len(members))
	for i, member := range members {
		name := strings.TrimSpace(member.Name)
		if name == "" {
			return nil, fmt.Errorf("review: ensemble member %d has an empty name", i+1)
		}
		if member.Reviewer == nil {
			return nil, fmt.Errorf("review: ensemble member %q is nil", name)
		}
		key := strings.ToLower(name)
		if seen[key] {
			return nil, fmt.Errorf("review: ensemble has duplicate member %q", name)
		}
		seen[key] = true
		out[i] = EnsembleMember{Name: name, Reviewer: member.Reviewer}
	}
	if maxParallel <= 0 || maxParallel > len(out) {
		maxParallel = len(out)
	}
	return &EnsembleReviewer{members: out, maxParallel: maxParallel}, nil
}

// Ask performs candidate discovery across the ensemble. It is public so an
// ensemble remains a Reviewer, but Run uses it only for pass 1; see verifier.
func (e *EnsembleReviewer) Ask(ctx context.Context, system, prompt string) (string, error) {
	return e.askCandidates(ctx, system, prompt)
}

func (e *EnsembleReviewer) Model() string {
	if e == nil {
		return ""
	}
	models := make([]string, 0, len(e.members))
	for _, member := range e.members {
		models = append(models, member.Name+"="+member.Reviewer.Model())
	}
	return strings.Join(models, ",")
}

// verifier returns the member that independently verifies merged candidates.
func (e *EnsembleReviewer) verifier() Reviewer { return e.members[0].Reviewer }

func (e *EnsembleReviewer) askCandidates(ctx context.Context, system, prompt string) (string, error) {
	type response struct {
		member EnsembleMember
		env    candidateEnvelope
		err    error
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	responses := make(chan response, len(e.members))
	sem := make(chan struct{}, e.maxParallel)
	var wg sync.WaitGroup
	for _, member := range e.members {
		wg.Add(1)
		go func(member EnsembleMember) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				responses <- response{member: member, err: ctx.Err()}
				return
			}
			defer func() { <-sem }()
			reply, err := member.Reviewer.Ask(ctx, system, prompt)
			if err != nil {
				responses <- response{member: member, err: err}
				return
			}
			env, err := parseCandidates(reply)
			responses <- response{member: member, env: env, err: err}
		}(member)
	}
	go func() { wg.Wait(); close(responses) }()

	byName := make(map[string]candidateEnvelope, len(e.members))
	var firstErr error
	for result := range responses {
		if result.err != nil && firstErr == nil {
			firstErr = fmt.Errorf("review: ensemble member %q: %w", result.member.Name, result.err)
			cancel()
			continue
		}
		if result.err == nil {
			byName[result.member.Name] = result.env
		}
	}
	if firstErr != nil {
		return "", firstErr
	}

	merged := candidateEnvelope{Findings: []Finding{}}
	for _, member := range e.members { // member order makes output deterministic
		env := byName[member.Name]
		for _, finding := range env.Findings {
			if finding.Extra == nil {
				finding.Extra = map[string]string{}
			}
			finding.Extra["reviewer"] = member.Name
			merged.Findings = append(merged.Findings, finding)
		}
		if summary := strings.TrimSpace(env.Summary); summary != "" {
			merged.Notes = append(merged.Notes, "ensemble "+member.Name+": "+summary)
		}
		for _, note := range env.Notes {
			if note = strings.TrimSpace(note); note != "" {
				merged.Notes = append(merged.Notes, "ensemble "+member.Name+": "+note)
			}
		}
	}
	sort.Strings(merged.Notes)
	return string(mustJSON(merged)), nil
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // candidateEnvelope/Finding are always JSON marshalable
	}
	return b
}

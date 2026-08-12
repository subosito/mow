package review

import (
	"fmt"
	"strings"

	"github.com/subosito/mow"
	"github.com/subosito/mow/cliutil"
)

// reviewerModels normalizes repeatable and comma-separated reviewer model
// flags while preserving their command-line order.
func reviewerModels(reviewers []string) ([]string, error) {
	var models []string
	seen := map[string]bool{}
	for _, raw := range reviewers {
		for _, part := range strings.Split(raw, ",") {
			model := strings.TrimSpace(part)
			if model == "" {
				return nil, fmt.Errorf("review: reviewer model must not be empty")
			}
			key := strings.ToLower(model)
			if seen[key] {
				return nil, fmt.Errorf("review: duplicate reviewer model %q", model)
			}
			seen[key] = true
			models = append(models, model)
		}
	}
	return models, nil
}

// ensembleOptions creates isolated, read-only engine options for each named
// reviewer model. The caller owns the engines made from the returned options.
func ensembleOptions(ef cliutil.EngineFlags, workspace string, models []string, parallel int, quiet bool, budget Budget) ([]mow.Options, int, error) {
	if len(models) == 0 {
		return nil, 0, fmt.Errorf("review: ensemble requires at least one reviewer model")
	}
	if parallel < 0 {
		return nil, 0, fmt.Errorf("review: --reviewer-parallel must be greater than zero")
	}
	if parallel == 0 || parallel > len(models) {
		parallel = len(models)
	}
	opts := make([]mow.Options, 0, len(models))
	for _, model := range models {
		copy := ef
		copy.Model = model
		copy.AllowWrite = false
		copy.AllowShell = false
		copy.NoSession = true
		copy.Stream = false
		opt := copy.OptionsCLI()
		opt.Workspace = workspace
		if quiet {
			opt.OnEvent = nil
		}
		if !copy.MaxTurnsSet {
			opt.MaxTurns = budget.MaxTurns
		}
		opts = append(opts, opt)
	}
	return opts, parallel, nil
}

// newEnsembleEngines builds engines for options, closing any earlier engine if
// construction fails. It is separate so CLI construction can be tested without
// contacting a provider by testing ensembleOptions.
func newEnsembleEngines(opts []mow.Options, newEngine func(mow.Options) (*mow.Engine, error)) ([]*mow.Engine, error) {
	engines := make([]*mow.Engine, 0, len(opts))
	for _, opt := range opts {
		eng, err := newEngine(opt)
		if err != nil {
			for _, prior := range engines {
				prior.Close()
			}
			return nil, err
		}
		engines = append(engines, eng)
	}
	return engines, nil
}

func closeEngines(engines []*mow.Engine) {
	for _, eng := range engines {
		eng.Close()
	}
}

func ensembleFromEngines(models []string, engines []*mow.Engine, parallel int) (*EnsembleReviewer, error) {
	if len(models) != len(engines) {
		return nil, fmt.Errorf("review: reviewer models and engines differ in length")
	}
	members := make([]EnsembleMember, len(engines))
	for i, eng := range engines {
		members[i] = EnsembleMember{Name: models[i], Reviewer: NewEngineReviewer(eng)}
	}
	return NewEnsembleReviewer(members, parallel)
}

// verifierEngineOptions builds read-only engine options for a dedicated pass-two
// verifier model.
func verifierEngineOptions(ef cliutil.EngineFlags, workspace, model string, quiet bool, budget Budget) mow.Options {
	copy := ef
	copy.Model = strings.TrimSpace(model)
	copy.AllowWrite = false
	copy.AllowShell = false
	copy.NoSession = true
	copy.Stream = false
	opt := copy.OptionsCLI()
	opt.Workspace = workspace
	if quiet {
		opt.OnEvent = nil
	}
	if !copy.MaxTurnsSet {
		opt.MaxTurns = budget.MaxTurns
	}
	return opt
}

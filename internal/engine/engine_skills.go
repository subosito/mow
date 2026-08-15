package engine

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/subosito/mow/internal/agent"
	"github.com/subosito/mow/internal/contextload"
)

func prependConfigPath(paths []string, cfgPath string) []string {
	cfgPath = strings.TrimSpace(cfgPath)
	if cfgPath == "" {
		return paths
	}
	want := filepath.Clean(cfgPath)
	for _, p := range paths {
		if filepath.Clean(strings.TrimSpace(p)) == want {
			return paths
		}
	}
	return append([]string{cfgPath}, paths...)
}

// mergeSkillNames unions two skill-name lists case-insensitively, dedupes,
// and preserves first-seen order (so CLI --skill names appear before config
// names in the merged list — useful for diagnostics, not semantics).
func mergeSkillNames(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(append([]string(nil), a...), b...) {
		k := strings.ToLower(strings.TrimSpace(s))
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, s)
	}
	return out
}

// mergeSkillText concatenates two skill-text blobs (each a sequence of
// "## skill: <name>" sections) and dedupes by skill label, preserving the
// order from a then b. A skill present in both prompt-matched and explicit
// sets appears once (first occurrence wins).
func mergeSkillText(a, b string) string {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	seen := map[string]bool{}
	var out []string
	for _, blob := range []string{a, b} {
		for _, sec := range strings.Split(blob, "\n\n") {
			sec = strings.TrimSpace(sec)
			if sec == "" {
				continue
			}
			label := skillSectionLabel(sec)
			if label != "" && seen[label] {
				continue
			}
			if label != "" {
				seen[label] = true
			}
			out = append(out, sec)
		}
	}
	return strings.Join(out, "\n\n")
}

// skillSectionLabel extracts the skill folder name from a "## skill: <name>"
// section header (case-insensitive match on the prefix). Empty when the
// section does not start with the skill marker.
func skillSectionLabel(sec string) string {
	sec = strings.TrimSpace(sec)
	if !strings.HasPrefix(sec, "## skill:") {
		return ""
	}
	rest := strings.TrimPrefix(sec, "## skill:")
	rest = strings.TrimSpace(rest)
	if idx := strings.IndexByte(rest, '\n'); idx >= 0 {
		rest = rest[:idx]
	}
	return strings.ToLower(strings.TrimSpace(rest))
}

// recomposeSystemLocked rebuilds e.sys from the current e.skillsText and the
// other immutable system segments (jail facts, agents framing, SystemAppend).
// Caller must hold e.mu. Returns the composed system string.
func (e *Engine) recomposeSystemLocked() string {
	ro := e.pol.ExtraRootsReadOnly
	return contextload.ComposeSystem(
		contextload.PathJailFacts(e.cfg.Workspace, e.pol.ExtraRoots, ro),
		agent.FramingFacts(e.untrustedNonce),
		e.agents, e.skillsText, e.opt.SystemAppend,
	)
}

// ActivateSkills loads the named skills (case-insensitive folder match) from
// the engine's already-resolved skill directories and merges them into the
// live system prompt for subsequent turns. It is safe to call mid-session:
// it acquires the prompt mutex (no concurrent Prompt), does not mutate
// committed history, and preserves the first-prompt selector and explicit
// CLI/config skills already loaded.
//
// Names not found among discoverable skills are reported in Unknown, not
// errored, so a host can surface them without aborting the good ones.
// Activated returns the folder names actually loaded (first-directory spelling,
// in input order). A name already loaded is a no-op for the prompt but is
// still reported as activated.
func (e *Engine) ActivateSkills(names ...string) (activated, unknown []string) {
	if e == nil {
		return nil, nil
	}
	available := contextload.AvailableSkillNames(e.skillDirs)
	avail := make(map[string]bool, len(available))
	for _, a := range available {
		avail[strings.ToLower(a)] = true
	}
	// Partition requested names into available (activated) vs unknown.
	for _, n := range names {
		k := strings.ToLower(strings.TrimSpace(n))
		if k == "" {
			continue
		}
		if avail[k] {
			activated = append(activated, n)
		} else {
			unknown = append(unknown, n)
		}
	}
	sort.Strings(unknown)
	if len(activated) == 0 {
		return nil, unknown
	}
	// Load the named skills that exist (unknown names are silently skipped by
	// LoadExplicitSkills). First-directory precedence is baked into the loader
	// via the engine's skillDirs.
	loaded := contextload.LoadExplicitSkills(e.skillDirs, names)
	if loaded == "" {
		return nil, unknown
	}
	e.promptMu.Lock()
	defer e.promptMu.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	// Merge into the live baseline (explicit + prompt-matched + previously
	// activated). mergeSkillText dedupes by label so re-activating an
	// already-loaded skill is a no-op for the prompt.
	e.skillsText = mergeSkillText(e.skillsText, loaded)
	e.sys = e.recomposeSystemLocked()
	e.skillsLoaded = true
	sort.Strings(activated)
	return activated, unknown
}

// AvailableSkills returns the sorted, deduplicated skill folder names the
// engine can load from its configured skill directories (global, user,
// trusted project, and programmatic SkillsDirs). It is the same set the
// /skill listing and ActivateSkills operate on. Missing dirs are skipped.
func (e *Engine) AvailableSkills() []string {
	if e == nil {
		return nil
	}
	return contextload.AvailableSkillNames(e.skillDirs)
}

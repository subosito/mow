package review

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

var verdictCorrectableFields map[string]struct{}

func init() {
	verdictCorrectableFields = make(map[string]struct{}, len(SecurityEvidenceFields))
	for _, f := range SecurityEvidenceFields {
		verdictCorrectableFields[f] = struct{}{}
	}
}

// applyVerdictEvidenceCorrections merges explicit pass-two corrections into a
// security finding. Only keys present in the verdict are changed; general review
// ignores evidence_fields entirely.
func applyVerdictEvidenceCorrections(f *Finding, v Verdict, prof *Profile) ([]string, error) {
	if prof == nil || prof.Name != "security" || len(v.EvidenceFields) == 0 {
		return nil, nil
	}
	if f.Extra == nil {
		f.Extra = map[string]string{}
	}
	// Stable key order keeps report notes deterministic across runs.
	keys := make([]string, 0, len(v.EvidenceFields))
	for rawKey := range v.EvidenceFields {
		keys = append(keys, rawKey)
	}
	sort.Strings(keys)

	var notes []string
	for _, rawKey := range keys {
		rawVal := v.EvidenceFields[rawKey]
		key := normalizeEvidenceFieldKey(rawKey)
		if key == "" {
			return nil, fmt.Errorf("review: verification pass returned empty evidence field key for %q", f.ID)
		}
		if _, ok := verdictCorrectableFields[key]; !ok {
			return nil, fmt.Errorf("review: verification pass returned unknown evidence field %q for %q", rawKey, f.ID)
		}
		if isJSONNull(rawVal) {
			if _, had := f.Extra[key]; had {
				delete(f.Extra, key)
				notes = append(notes, fmt.Sprintf("pass 2 cleared %q on %q", key, f.Title))
			}
			continue
		}
		var val string
		if err := json.Unmarshal(rawVal, &val); err != nil {
			return nil, fmt.Errorf("review: verification pass evidence field %q for %q must be a string or null: %w", key, f.ID, err)
		}
		val = clampText(redactSecrets(val))
		if val == "" {
			if _, had := f.Extra[key]; had {
				delete(f.Extra, key)
				notes = append(notes, fmt.Sprintf("pass 2 cleared %q on %q", key, f.Title))
			}
			continue
		}
		if old := f.Extra[key]; old != val {
			notes = append(notes, fmt.Sprintf("pass 2 corrected %q on %q", key, f.Title))
		}
		f.Extra[key] = val
	}
	if len(f.Extra) == 0 {
		f.Extra = nil
	}
	return notes, nil
}

func normalizeEvidenceFieldKey(k string) string {
	return strings.ToLower(strings.TrimSpace(strings.ReplaceAll(k, "-", "_")))
}

func isJSONNull(raw json.RawMessage) bool {
	return len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

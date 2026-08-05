package review

import (
	"encoding/json"
	"fmt"
	"sort"
)

// findingAlias avoids infinite recursion in Marshal/UnmarshalJSON.
type findingAlias Finding

// baseFindingKeys are the JSON keys owned by the base schema. Anything else in
// an incoming finding object is captured into Extra (profile-specific fields).
var baseFindingKeys = map[string]bool{
	"id": true, "fingerprint": true, "severity": true, "confidence": true,
	"category": true, "title": true, "path": true, "start_line": true,
	"end_line": true, "locations": true, "evidence": true, "impact": true,
	"recommendation": true, "verified": true, "verification_notes": true,
}

// MarshalJSON emits the base fields with Extra flattened alongside them, so
// consumers see one flat object (base keys always win over Extra).
func (f Finding) MarshalJSON() ([]byte, error) {
	base, err := json.Marshal(findingAlias(f))
	if err != nil {
		return nil, err
	}
	if len(f.Extra) == 0 {
		return base, nil
	}
	var merged map[string]json.RawMessage
	if err := json.Unmarshal(base, &merged); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(f.Extra))
	for k := range f.Extra {
		if !baseFindingKeys[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		v, err := json.Marshal(f.Extra[k])
		if err != nil {
			return nil, err
		}
		merged[k] = v
	}
	return json.Marshal(merged)
}

// UnmarshalJSON decodes base fields and collects unknown string fields into
// Extra. Non-string extras are stringified so the schema stays predictable.
func (f *Finding) UnmarshalJSON(b []byte) error {
	var alias findingAlias
	if err := json.Unmarshal(b, &alias); err != nil {
		return err
	}
	*f = Finding(alias)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	for k, v := range raw {
		if baseFindingKeys[k] {
			continue
		}
		s, ok := rawToString(v)
		if !ok || s == "" {
			continue
		}
		if f.Extra == nil {
			f.Extra = map[string]string{}
		}
		f.Extra[k] = s
	}
	return nil
}

// rawToString renders a JSON value as a display string (objects/arrays are
// kept verbatim so nothing the model said is silently dropped).
func rawToString(v json.RawMessage) (string, bool) {
	var s string
	if err := json.Unmarshal(v, &s); err == nil {
		return s, true
	}
	var n float64
	if err := json.Unmarshal(v, &n); err == nil {
		return fmt.Sprintf("%g", n), true
	}
	var bo bool
	if err := json.Unmarshal(v, &bo); err == nil {
		return fmt.Sprintf("%t", bo), true
	}
	if string(v) == "null" {
		return "", false
	}
	return string(v), true
}

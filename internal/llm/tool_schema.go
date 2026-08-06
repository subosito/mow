package llm

import (
	"encoding/json"
)

// Tool parameter schemas reach mow from sources it does not control — MCP
// servers, ACP peers, integrator-supplied tools — and those emit ordinary JSON
// Schema, metadata keys included. Providers disagree about what to do with the
// extras: OpenAI-compatible endpoints ignore unknown keys, while stricter
// validators (notably Google's function_declarations, reached through an
// OpenAI-compatible gateway) reject the whole request.
//
// The observed failure is worth spelling out because it is so unhelpful:
//
//	HTTP 400: Invalid JSON payload received. Unknown name "$schema" at
//	'request.tools[0].function_declarations[17].parameters': Cannot find field.
//
// One tool carrying "$schema" fails the entire call, so *every* tool becomes
// unusable and the error names an array index rather than a tool. Nothing
// points at the MCP server that supplied it.
//
// mow owns this boundary: it accepts schemas from elsewhere and emits provider
// requests, so reconciling the two is its job and not the user's.

// schemaMetaKeys are JSON Schema keys that describe the *document* rather than
// the parameters. They tell a validator where the schema came from and how to
// resolve references; they tell a model nothing about how to call the tool, so
// dropping them cannot change behaviour.
//
// $ref is deliberately absent: a schema that uses references needs them
// resolved, not deleted, and silently dropping a $ref would change a parameter
// from "this shape" to "anything". Leave those alone and let the provider
// complain — a loud failure beats a tool whose arguments stopped being
// validated.
var schemaMetaKeys = map[string]bool{
	"$schema":        true,
	"$id":            true,
	"$comment":       true,
	"$anchor":        true,
	"$vocabulary":    true,
	"$dynamicAnchor": true,
}

// SanitizeToolSchema removes JSON Schema metadata keys from a tool's parameter
// schema, recursively, so a schema written for a validator survives a provider
// that only understands function parameters.
//
// It is deliberately unconditional rather than gated on a provider or model
// id. mow cannot see what is behind an OpenAI-compatible base URL — the
// endpoint that rejected "$schema" presented itself as one — and the keys are
// inert for every provider, so there is nothing to gain by keeping them for
// some callers and a silent 400 to lose by guessing wrong.
//
// Invalid JSON is returned unchanged: this is a cleanup pass, not a validator,
// and a caller's malformed schema is theirs to hear about from the provider.
func SanitizeToolSchema(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	cleaned, changed := stripSchemaMeta(v)
	if !changed {
		// Return the original bytes when nothing was removed, so formatting
		// and key order survive for every schema that was already fine.
		return raw
	}
	out, err := json.Marshal(cleaned)
	if err != nil {
		return raw
	}
	return out
}

// stripSchemaMeta walks a decoded schema removing metadata keys, reporting
// whether anything changed.
//
// Nested objects matter as much as the root: a $schema on a property, or
// inside an array's items, fails the request exactly the same way.
func stripSchemaMeta(v any) (any, bool) {
	switch t := v.(type) {
	case map[string]any:
		changed := false
		out := make(map[string]any, len(t))
		for k, val := range t {
			if schemaMetaKeys[k] {
				changed = true
				continue
			}
			cleaned, sub := stripSchemaMeta(val)
			if sub {
				changed = true
			}
			out[k] = cleaned
		}
		return out, changed
	case []any:
		changed := false
		out := make([]any, len(t))
		for i, val := range t {
			cleaned, sub := stripSchemaMeta(val)
			if sub {
				changed = true
			}
			out[i] = cleaned
		}
		return out, changed
	default:
		return v, false
	}
}

// SanitizeToolSpecs applies SanitizeToolSchema to every spec's parameters.
// Specs are copied; the caller's slice and schemas are not modified.
func SanitizeToolSpecs(specs []ToolSpec) []ToolSpec {
	if len(specs) == 0 {
		return specs
	}
	out := make([]ToolSpec, len(specs))
	copy(out, specs)
	for i := range out {
		out[i].Function.Parameters = SanitizeToolSchema(out[i].Function.Parameters)
	}
	return out
}

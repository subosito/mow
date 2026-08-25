package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// The bug: an MCP server's tool schema carried "$schema", a strict provider
// rejected the whole request with HTTP 400, and every tool became unusable —
// the error naming an array index rather than the offending tool.

func TestSanitizeToolSchemaStripsMetaKeys(t *testing.T) {
	// The exact shape that broke: draft-07 declaration on an otherwise normal
	// object schema.
	in := json.RawMessage(`{
		"$schema": "http://json-schema.org/draft-07/schema#",
		"type": "object",
		"properties": {"path": {"type": "string"}},
		"required": ["path"]
	}`)
	got := string(SanitizeToolSchema(in))

	if strings.Contains(got, "$schema") {
		t.Errorf("$schema survived: %s", got)
	}
	// Everything the model needs must be intact.
	for _, want := range []string{`"type"`, `"properties"`, `"path"`, `"required"`} {
		if !strings.Contains(got, want) {
			t.Errorf("sanitize dropped %s: %s", want, got)
		}
	}
}

func TestSanitizeToolSchemaIsRecursive(t *testing.T) {
	// A nested $schema fails the request exactly like a root one, so depth
	// must not matter — properties, array items, and $defs-style branches.
	in := json.RawMessage(`{
		"type": "object",
		"properties": {
			"nested": {"$schema": "x", "type": "object",
				"properties": {"deep": {"$id": "y", "type": "string"}}},
			"list": {"type": "array", "items": {"$comment": "z", "type": "string"}}
		}
	}`)
	got := string(SanitizeToolSchema(in))

	for _, bad := range []string{"$schema", "$id", "$comment"} {
		if strings.Contains(got, bad) {
			t.Errorf("%s survived nesting: %s", bad, got)
		}
	}
	for _, want := range []string{`"nested"`, `"deep"`, `"list"`, `"items"`} {
		if !strings.Contains(got, want) {
			t.Errorf("sanitize dropped %s: %s", want, got)
		}
	}
}

// A schema with nothing to remove must come back byte-identical: mow's own
// tools are already clean, and rewriting them would churn every request for no
// reason (and reorder keys, which makes wire diffs unreadable).
func TestSanitizeToolSchemaLeavesCleanSchemasAlone(t *testing.T) {
	in := json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`)
	got := SanitizeToolSchema(in)
	if string(got) != string(in) {
		t.Errorf("clean schema was rewritten:\n got %s\nwant %s", got, in)
	}
}

// $ref must survive. Dropping it would turn "this shape" into "anything",
// silently widening a tool's accepted arguments — worse than the 400 it would
// avoid, because nothing would report it.
func TestSanitizeToolSchemaKeepsRefs(t *testing.T) {
	in := json.RawMessage(`{"type":"object","properties":{"a":{"$ref":"#/$defs/A"}},"$defs":{"A":{"type":"string"}}}`)
	got := string(SanitizeToolSchema(in))
	if !strings.Contains(got, "$ref") {
		t.Errorf("$ref was dropped, widening the parameter: %s", got)
	}
	if !strings.Contains(got, "$defs") {
		t.Errorf("$defs was dropped, breaking its own $ref: %s", got)
	}
}

func TestSanitizeToolSchemaEdgeCases(t *testing.T) {
	tests := []struct {
		name string
		in   json.RawMessage
	}{
		// A cleanup pass is not a validator: malformed input belongs to the
		// provider's error message, not to a silent rewrite here.
		{"invalid json", json.RawMessage(`{not json`)},
		{"empty", json.RawMessage(``)},
		{"nil", nil},
		{"non-object", json.RawMessage(`"just a string"`)},
		{"null", json.RawMessage(`null`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeToolSchema(tt.in)
			if string(got) != string(tt.in) {
				t.Errorf("input was altered:\n got %q\nwant %q", got, tt.in)
			}
		})
	}
}

// A schema that is *only* metadata must still be valid JSON afterwards, not an
// empty string the provider chokes on differently.
func TestSanitizeToolSchemaAllMeta(t *testing.T) {
	got := SanitizeToolSchema(json.RawMessage(`{"$schema":"x","$id":"y"}`))
	var v map[string]any
	if err := json.Unmarshal(got, &v); err != nil {
		t.Fatalf("result is not valid JSON (%q): %v", got, err)
	}
	if len(v) != 0 {
		t.Errorf("expected an empty object, got %s", got)
	}
}

func TestSanitizeToolSchema(t *testing.T) {
	dirty := json.RawMessage(`{"$schema":"x","type":"object"}`)
	original := string(dirty)

	out := SanitizeToolSchema(dirty)
	if strings.Contains(string(out), "$schema") {
		t.Error("schema was not sanitized")
	}
	// Sanitize returns new bytes; the caller's input must be untouched.
	if string(dirty) != original {
		t.Error("SanitizeToolSchema mutated the caller's input")
	}
	// Already-clean input passes through unchanged in content.
	clean := SanitizeToolSchema(json.RawMessage(`{"type":"object"}`))
	if !strings.Contains(string(clean), `"type":"object"`) {
		t.Errorf("clean schema altered: %s", clean)
	}
}

func TestSanitizeToolSchemaEmpty(t *testing.T) {
	if got := SanitizeToolSchema(nil); got != nil {
		t.Errorf("nil = %v, want nil", got)
	}
	if got := SanitizeToolSchema(json.RawMessage{}); len(got) != 0 {
		t.Errorf("empty = %v, want empty", got)
	}
}

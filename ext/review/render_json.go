package review

import (
	"encoding/json"
	"fmt"
	"io"
)

// RenderJSON writes the report as indented JSON. The JSON envelope is the
// source of truth: the text renderer shows the same data, and SARIF is a
// projection of it.
func RenderJSON(w io.Writer, rep *Report) error {
	if rep == nil {
		return fmt.Errorf("review: nil report")
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	// Report contains only model-derived prose and paths; keeping HTML escaping
	// off makes the output readable (and it is never embedded in a page).
	enc.SetEscapeHTML(false)
	return enc.Encode(rep)
}

// RenderJSONL writes one finding per line, for streaming consumers and for
// pipelines that would rather not parse a whole envelope (jq, log shipping).
// The first line is the envelope with an empty findings list, so scope and
// counts are still available.
func RenderJSONL(w io.Writer, rep *Report) error {
	if rep == nil {
		return fmt.Errorf("review: nil report")
	}
	head := *rep
	head.Findings = []Finding{}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(head); err != nil {
		return err
	}
	for _, f := range rep.Findings {
		if err := enc.Encode(f); err != nil {
			return err
		}
	}
	return nil
}

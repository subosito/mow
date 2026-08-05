package review

import (
	"fmt"
	"io"
	"strings"
)

// Format is an output encoding for a report.
type Format string

// Supported output formats.
const (
	FormatText  Format = "text"
	FormatJSON  Format = "json"
	FormatJSONL Format = "jsonl"
	FormatSARIF Format = "sarif"
)

// FormatNames lists supported formats for help text and flag validation.
func FormatNames() []string { return []string{"text", "json", "jsonl", "sarif"} }

// ParseFormat resolves a format name (empty → text).
func ParseFormat(s string) (Format, error) {
	switch f := Format(strings.ToLower(strings.TrimSpace(s))); f {
	case "":
		return FormatText, nil
	case FormatText, FormatJSON, FormatJSONL, FormatSARIF:
		return f, nil
	default:
		return "", fmt.Errorf("review: unknown format %q (want %s)", s, strings.Join(FormatNames(), ", "))
	}
}

// Render writes a report in the requested format.
func Render(w io.Writer, rep *Report, format Format, opt TextOptions) error {
	switch format {
	case FormatJSON:
		return RenderJSON(w, rep)
	case FormatJSONL:
		return RenderJSONL(w, rep)
	case FormatSARIF:
		return RenderSARIF(w, rep)
	case FormatText, "":
		return RenderText(w, rep, opt)
	default:
		return fmt.Errorf("review: unknown format %q", format)
	}
}

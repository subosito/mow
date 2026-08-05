package review

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// TextOptions tunes the human renderer.
type TextOptions struct {
	// Color emits ANSI severity colors (callers pass false when not a TTY).
	Color bool
	// Verbose adds validation notes and the excluded-file list.
	Verbose bool
	// NoScope omits the scope header (for embedding in a larger report).
	NoScope bool
}

// RenderText writes the human-readable report. Structure is deliberately
// stable and grep-friendly: header, then one block per finding worst-first,
// then a summary line.
func RenderText(w io.Writer, rep *Report, opt TextOptions) error {
	if rep == nil {
		return fmt.Errorf("review: nil report")
	}
	prof, _ := LookupProfile(rep.Profile)
	b := &strings.Builder{}

	b.WriteString(prof.Headline)
	b.WriteString("\n\n")
	if !opt.NoScope {
		writeScopeHeader(b, rep)
	}

	if len(rep.Findings) == 0 {
		b.WriteString("No findings.\n\n")
		// Say plainly what "no findings" does and does not mean.
		b.WriteString(emptyCaveat(rep))
		b.WriteString("\n")
	} else {
		for _, f := range rep.Findings {
			writeFinding(b, f, opt)
		}
	}

	writeSummary(b, rep, opt)
	_, err := io.WriteString(w, b.String())
	return err
}

// writeScopeHeader prints exactly what was reviewed. This is printed on every
// human run because the default scope depends on working-tree state.
func writeScopeHeader(b *strings.Builder, rep *Report) {
	b.WriteString("Scope:\n")
	fmt.Fprintf(b, "  profile:         %s\n", rep.Profile)
	if s := scopeSelectorLine(rep.Scope); s != "" {
		fmt.Fprintf(b, "  selection:       %s\n", s)
	}
	if rep.Run.Commit != "" {
		line := rep.Run.Commit
		if rep.Run.Branch != "" {
			line += " (" + rep.Run.Branch + ")"
		}
		fmt.Fprintf(b, "  commit:          %s\n", line)
	}
	fmt.Fprintf(b, "  files reviewed:  %d\n", rep.Scope.FilesReviewed)
	if rep.Scope.FilesExcluded > 0 {
		fmt.Fprintf(b, "  files excluded:  %d\n", rep.Scope.FilesExcluded)
	}
	if rep.Scope.Budget != "" {
		fmt.Fprintf(b, "  budget:          %s\n", rep.Scope.Budget)
	}
	fmt.Fprintf(b, "  truncated:       %t", rep.Run.Truncated)
	if rep.Run.Truncated && rep.Run.TruncationReason != "" {
		fmt.Fprintf(b, " (%s)", rep.Run.TruncationReason)
	}
	b.WriteString("\n")
	if rep.Run.Model != "" {
		fmt.Fprintf(b, "  model:           %s\n", rep.Run.Model)
	}
	b.WriteString("\n")
}

// scopeSelectorLine describes the selection in one line.
func scopeSelectorLine(s ScopeInfo) string {
	// Selection is the resolver's own description and is correct in every
	// mode, including "uncommitted changes" — which no selector field encodes.
	if s.Selection != "" {
		return s.Selection
	}
	switch {
	case s.Diff != "":
		return s.Diff
	case s.Staged:
		return "staged changes"
	case s.Base != "":
		return "changes vs " + s.Base
	case len(s.Paths) > 0:
		return strings.Join(s.Paths, " ")
	default:
		return ""
	}
}

// writeFinding renders one finding block.
func writeFinding(b *strings.Builder, f Finding, opt TextOptions) {
	sev := strings.ToUpper(f.Severity.String())
	if opt.Color {
		sev = colorize(f.Severity, sev)
	}
	fmt.Fprintf(b, "[%s] %s\n", sev, f.Title)
	fmt.Fprintf(b, "  id:             %s\n", f.ID)
	fmt.Fprintf(b, "  path:           %s\n", formatLocation(f.Path, f.StartLine, f.EndLine))
	fmt.Fprintf(b, "  confidence:     %s\n", f.Confidence)
	fmt.Fprintf(b, "  category:       %s\n", f.Category)
	if !f.Verified {
		b.WriteString("  verified:       no (not confirmed by the verification pass)\n")
	}
	writeField(b, "evidence", f.Evidence)
	writeField(b, "impact", f.Impact)
	writeField(b, "recommendation", f.Recommendation)
	// Profile-specific fields, in a stable order.
	if len(f.Extra) > 0 {
		keys := make([]string, 0, len(f.Extra))
		for k := range f.Extra {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			writeField(b, strings.ReplaceAll(k, "_", " "), f.Extra[k])
		}
	}
	// Secondary locations help a reader follow a data path.
	if len(f.Locations) > 1 {
		b.WriteString("  related:\n")
		for _, l := range f.Locations[1:] {
			fmt.Fprintf(b, "    %s (%s)\n", formatLocation(l.Path, l.StartLine, l.EndLine), l.Role)
		}
	}
	if opt.Verbose && f.VerificationNotes != "" {
		writeField(b, "notes", f.VerificationNotes)
	}
	b.WriteString("\n")
}

// writeField prints a wrapped, indented block for multi-line prose.
func writeField(b *strings.Builder, label, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	fmt.Fprintf(b, "  %s:\n", label)
	for _, line := range wrapText(value, 76) {
		fmt.Fprintf(b, "    %s\n", line)
	}
}

// wrapText soft-wraps prose to width, preserving explicit newlines.
func wrapText(s string, width int) []string {
	var out []string
	for _, para := range strings.Split(s, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		line := words[0]
		for _, w := range words[1:] {
			if len(line)+1+len(w) > width {
				out = append(out, line)
				line = w
				continue
			}
			line += " " + w
		}
		out = append(out, line)
	}
	return out
}

// writeSummary closes with counts and the advisory reminder.
func writeSummary(b *strings.Builder, rep *Report, opt TextOptions) {
	if opt.Verbose && len(rep.Scope.Excluded) > 0 {
		b.WriteString("Excluded files:\n")
		for _, p := range rep.Scope.Excluded {
			fmt.Fprintf(b, "  %s\n", p)
		}
		b.WriteString("\n")
	}
	if opt.Verbose && len(rep.Notes) > 0 {
		b.WriteString("Notes:\n")
		for _, n := range rep.Notes {
			fmt.Fprintf(b, "  %s\n", n)
		}
		b.WriteString("\n")
	}
	b.WriteString(strings.TrimSpace(rep.Summary))
	b.WriteString("\n")
	// The summary already states the suppressed count; only add the pointer to
	// --verbose, and only when there is something extra to show.
	if rep.Suppressed > 0 && !opt.Verbose {
		b.WriteString("Re-run with --verbose to see suppressed candidates and excluded files.\n")
	}
}

// emptyCaveat states the limits of a clean run so nobody reads it as proof.
func emptyCaveat(rep *Report) string {
	base := "This is an AI-assisted advisory review, not proof that the code is correct"
	if rep.Profile == "security" {
		base = "This is an AI-assisted advisory review, not proof that the code is secure"
	}
	if rep.Run.Truncated {
		return base + ". Scope was truncated, so coverage is partial.\n"
	}
	return base + ".\n"
}

// ANSI severity colors for terminals.
func colorize(s Severity, text string) string {
	var code string
	switch s {
	case SevCritical:
		code = "1;35" // bright magenta
	case SevHigh:
		code = "1;31" // red
	case SevMedium:
		code = "1;33" // yellow
	case SevLow:
		code = "36" // cyan
	default:
		code = "2" // dim
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

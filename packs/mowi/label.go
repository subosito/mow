package mowi

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	xansi "github.com/charmbracelet/x/ansi"
)

// toolLabel builds a short activity-band label: "verb · basename" (or peer form).
// Never mid-string-truncates a shell blob into useless noise.
func toolLabel(name, args string, maxWidth int) string {
	name = strings.TrimSpace(sanitizeDisplay(name))
	args = strings.TrimSpace(sanitizeDisplay(args))
	if name == "" {
		return ""
	}
	if maxWidth <= 0 {
		maxWidth = 48
	}

	if agent, rest, ok := strings.Cut(name, ":"); ok && !strings.Contains(agent, " ") {
		agent = strings.TrimSpace(agent)
		rest = strings.TrimSpace(rest)
		if agent != "" && rest != "" {
			detail := peerDetailLabel(rest, args)
			out := glyphArrow + " " + agent
			if detail != "" {
				out += " · " + detail
			}
			// Peer identity matters more than the band's safety floor: a
			// squeezed band must not ellipsize "→ peer-agent · read engine.go" into
			// "→ gr…". Give peer labels a sane minimum; the ellipsis stays
			// tail-safe via clampLabel.
			return clampLabel(out, max(maxWidth, minPeerLabelWidth))
		}
	}

	verb := strings.ToLower(name)
	// A bare trailing colon (e.g. "acp:" with no agent detail) would otherwise
	// paint "acp: · …" — strip it so the verb reads clean.
	verb = strings.TrimSpace(strings.TrimRight(verb, ":"))
	if verb == "" {
		return ""
	}
	if strings.Contains(verb, "/") {
		if i := strings.LastIndex(verb, "/"); i >= 0 && i+1 < len(verb) {
			verb = verb[i+1:]
		}
	}

	detail := argsDetail(verb, args)
	if detail == "" {
		return clampLabel(verb, maxWidth)
	}
	return clampLabel(verb+" · "+detail, maxWidth)
}

func peerDetailLabel(rest, args string) string {
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return argsDetail("", args)
	}
	verb := strings.ToLower(fields[0])
	if len(fields) == 1 {
		if d := argsDetail(verb, args); d != "" {
			return verb + " · " + d
		}
		return verb
	}
	tail := strings.Join(fields[1:], " ")
	// Basename only when the tail IS a path, not prose that mentions one —
	// looksLikePath alone fires on any sentence containing "a/b.go", which
	// collapsed "read engine.go and summarize the loop" to "read · loop".
	if b := usefulBasename(tail); b != "" && looksLikePath(tail) && !strings.ContainsAny(tail, " 	&|;") {
		return verb + " · " + b
	}
	// Peer detail is the substance of a delegation ("summarize the loop
	// spine in internal/agent/loop.go and report …") — keep a word window
	// instead of one token so the activity band shows the task, not just
	// its verb. The band clamp still bounds the final label (tail-safe "…").
	return verb + " · " + joinFew(fields[1:], 8)
}

func argsDetail(verb, args string) string {
	if args == "" {
		return ""
	}
	if strings.HasPrefix(args, "{") {
		var raw map[string]json.RawMessage
		if json.Unmarshal([]byte(args), &raw) == nil {
			for _, key := range []string{"path", "file", "filepath", "target", "command", "cmd", "query", "pattern", "url"} {
				if v, ok := raw[key]; ok {
					var s string
					if json.Unmarshal(v, &s) == nil && strings.TrimSpace(s) != "" {
						return summarizeArgValue(verb, key, s)
					}
				}
			}
			// Fallback: pick deterministically (sorted keys) so the same tool
			// call never flickers between labels across renders — Go map
			// iteration order is randomized, which made custom/MCP tool labels
			// jump around in the activity band.
			keys := make([]string, 0, len(raw))
			for k := range raw {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				var s string
				if json.Unmarshal(raw[k], &s) == nil && strings.TrimSpace(s) != "" {
					return summarizeArgValue(verb, k, s)
				}
			}
		}
	}
	s := strings.TrimSpace(args)
	s = strings.TrimPrefix(s, "$ ")
	return summarizeArgValue(verb, "command", s)
}

func summarizeArgValue(verb, key, s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if key == "path" || key == "file" || key == "filepath" || key == "target" ||
		(looksLikePath(s) && !strings.ContainsAny(s, " \t")) {
		if b := usefulBasename(s); b != "" {
			return b
		}
	}
	if key == "command" || key == "cmd" || verb == "bash" || verb == "shell" {
		return shellSummary(s)
	}
	if key == "query" || key == "pattern" {
		return firstUsefulToken(s)
	}
	if b := usefulBasename(s); b != "" && looksLikePath(s) && !strings.ContainsAny(s, " \t&|;") {
		return b
	}
	return firstUsefulToken(s)
}

func looksLikePath(s string) bool {
	if strings.Contains(s, "/") || strings.Contains(s, `\`) {
		return true
	}
	if strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") || strings.HasPrefix(s, "~") {
		return true
	}
	if !strings.ContainsAny(s, " \t") && strings.Contains(s, ".") {
		return true
	}
	return false
}

func usefulBasename(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	fields := strings.Fields(s)
	cand := fields[len(fields)-1]
	cand = strings.Trim(cand, `"'`+"`")
	base := filepath.Base(cand)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return ""
	}
	return base
}

func shellSummary(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "\n", " ")

	// Fast path: "... -- just verify"
	if i := strings.LastIndex(s, " -- "); i >= 0 {
		rest := strings.TrimSpace(s[i+4:])
		if rest != "" {
			return joinFew(strings.Fields(rest), 4)
		}
	}

	chunks := splitShellChunks(s)
	for i := len(chunks) - 1; i >= 0; i-- {
		toks := tokenizeShell(chunks[i])
		if len(toks) == 0 {
			continue
		}
		if strings.EqualFold(toks[0], "cd") {
			continue
		}
		if strings.EqualFold(toks[0], "devenv") {
			for j := 1; j < len(toks); j++ {
				if toks[j] == "--" && j+1 < len(toks) {
					return joinFew(toks[j+1:], 4)
				}
			}
		}
		if strings.EqualFold(toks[0], "just") || strings.EqualFold(toks[0], "go") ||
			strings.EqualFold(toks[0], "make") || strings.EqualFold(toks[0], "npm") {
			return joinFew(toks, 4)
		}
		start := 0
		for start < len(toks) && strings.Contains(toks[start], "=") && !strings.HasPrefix(toks[start], "-") {
			start++
		}
		if start < len(toks) {
			return joinFew(toks[start:], 3)
		}
	}
	return firstUsefulToken(s)
}

func splitShellChunks(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		switch {
		case i+1 < len(s) && s[i] == '&' && s[i+1] == '&':
			out = append(out, strings.TrimSpace(s[start:i]))
			start = i + 2
			i++
		case i+1 < len(s) && s[i] == '|' && s[i+1] == '|':
			out = append(out, strings.TrimSpace(s[start:i]))
			start = i + 2
			i++
		case s[i] == ';':
			out = append(out, strings.TrimSpace(s[start:i]))
			start = i + 1
		}
	}
	out = append(out, strings.TrimSpace(s[start:]))
	return out
}

func tokenizeShell(s string) []string {
	var out []string
	var b strings.Builder
	inS, inD := false, false
	flush := func() {
		if b.Len() > 0 {
			out = append(out, b.String())
			b.Reset()
		}
	}
	for _, r := range s {
		switch {
		case r == '\'' && !inD:
			inS = !inS
		case r == '"' && !inS:
			inD = !inD
		case unicode.IsSpace(r) && !inS && !inD:
			flush()
		default:
			b.WriteRune(r)
		}
	}
	flush()
	return out
}

func joinFew(toks []string, n int) string {
	if len(toks) == 0 {
		return ""
	}
	if len(toks) > n {
		toks = toks[:n]
	}
	out := make([]string, len(toks))
	copy(out, toks)
	for i := range out {
		// Basename path args only — never rewrite command verbs like "just".
		if i > 0 && looksLikePath(out[i]) {
			if b := usefulBasename(out[i]); b != "" {
				out[i] = b
			}
		}
	}
	return strings.Join(out, " ")
}

func firstUsefulToken(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !strings.ContainsAny(s, " \t") && looksLikePath(s) {
		if b := usefulBasename(s); b != "" {
			return b
		}
	}
	toks := strings.Fields(s)
	if len(toks) == 0 {
		return ""
	}
	for i, tok := range toks {
		low := strings.ToLower(tok)
		if low == "just" || low == "go" || low == "make" || low == "npm" {
			return joinFew(toks[i:], 3)
		}
	}
	for _, tok := range toks {
		if strings.HasPrefix(tok, "-") {
			continue
		}
		if looksLikePath(tok) {
			if b := usefulBasename(tok); b != "" {
				return b
			}
		}
		if utf8.RuneCountInString(tok) > 32 {
			r := []rune(tok)
			return string(r[:32]) + "…"
		}
		return tok
	}
	return short(toks[0], 24)
}

// minPeerLabelWidth is the floor a delegated peer label keeps even when the
// activity band is squeezed — "→ name · verb detail" stays readable.
const minPeerLabelWidth = 28

func clampLabel(s string, maxWidth int) string {
	s = strings.Join(strings.Fields(s), " ")
	if maxWidth < 4 {
		maxWidth = 4
	}
	if xansi.StringWidth(s) <= maxWidth {
		return s
	}
	return xansi.Truncate(s, maxWidth, "…")
}

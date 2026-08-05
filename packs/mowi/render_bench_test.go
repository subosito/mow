package mowi

import (
	"strings"
	"testing"
)

// Baseline perf harness for the render path — glamour markdown, sanitize,
// and wrapping are the per-frame hot spots.

func benchMarkdown() string {
	var b strings.Builder
	for i := 0; i < 60; i++ {
		b.WriteString("## Section ")
		b.WriteString(string(rune('a' + i%26)))
		b.WriteString("\n\nSome body text with *emphasis* and `inline code` and a [link](https://example.com/x).\n\n")
		b.WriteString("- list item one\n- list item two with a longer tail to force wrapping across lines\n\n")
		b.WriteString("```go\nfunc main() {\n\tfmt.Println(\"hello world\")\n}\n```\n\n")
	}
	return b.String()
}

// BenchmarkRenderMarkdownWarm: steady-state — same width, cached renderer.
func BenchmarkRenderMarkdownWarm(b *testing.B) {
	c := newMDCache(false)
	md := benchMarkdown() // ~12KB
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if out := renderMarkdownCached(&c, md, 80, false); out == "" {
			b.Fatal("empty render")
		}
	}
}

// BenchmarkRenderMarkdownCold: width churn (resize) — renderer rebuild each op.
func BenchmarkRenderMarkdownCold(b *testing.B) {
	c := newMDCache(false)
	md := benchMarkdown()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := 60 + i%40
		if out := renderMarkdownCached(&c, md, w, false); out == "" {
			b.Fatal("empty render")
		}
	}
}

// BenchmarkRenderMarkdownLive: streaming path with fence stabilization.
func BenchmarkRenderMarkdownLive(b *testing.B) {
	c := newMDCache(false)
	md := benchMarkdown()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if out := renderMarkdownCached(&c, md, 80, true); out == "" {
			b.Fatal("empty render")
		}
	}
}

// BenchmarkSanitizeDisplayClean: no escapes — fast path.
func BenchmarkSanitizeDisplayClean(b *testing.B) {
	s := strings.Repeat("plain text with no control sequences 0123456789\n", 100)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if out := sanitizeDisplay(s); out == "" {
			b.Fatal("empty")
		}
	}
}

// BenchmarkSanitizeDisplayDirty: ESC sequences throughout — strip path.
func BenchmarkSanitizeDisplayDirty(b *testing.B) {
	s := strings.Repeat("text \x1b[31mred\x1b[0m and \x1b]0;title\x07 more\n", 100)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if out := sanitizeDisplay(s); out == "" {
			b.Fatal("empty")
		}
	}
}

// BenchmarkWordWrapLong: 4KB line-wrapped at 80 columns.
func BenchmarkWordWrapLong(b *testing.B) {
	s := strings.Repeat("the quick brown fox jumps over the lazy dog ", 80) // ~4.3KB
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if out := wordWrap(s, 80); out == "" {
			b.Fatal("empty")
		}
	}
}

func BenchmarkCleanLinesNoAnsi(b *testing.B) {
	// Plain (no-ESC) glamour output — the fast-path trim.
	var sb strings.Builder
	for i := 0; i < 500; i++ {
		sb.WriteString("line of padded prose with trailing spaces                \n")
	}
	sb.WriteString("\n")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if out := cleanLines(sb.String()); out == "" {
			b.Fatal("empty")
		}
	}
}

func BenchmarkCleanLinesAnsi(b *testing.B) {
	var sb strings.Builder
	for i := 0; i < 500; i++ {
		sb.WriteString("\x1b[32mcolored line with trailing padding                 \x1b[0m\n")
	}
	sb.WriteString("\n")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if out := cleanLines(sb.String()); out == "" {
			b.Fatal("empty")
		}
	}
}

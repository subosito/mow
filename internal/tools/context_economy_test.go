package tools

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// A bash command whose output exceeds the cap must keep the TAIL: shell output
// puts the answer at the end (failing assertion, stack trace, exit summary).
// Head-only capping discards exactly the part worth paying for, and the model
// re-runs the command — charging us twice for the same work.
func TestCappedBufferKeepsHeadAndTail(t *testing.T) {
	t.Parallel()
	var buf cappedBuffer
	// Distinguishable start and end around a large filler middle.
	buf.Write([]byte("FIRST-LINE\n"))
	buf.Write([]byte(strings.Repeat("x", maxBashOutputBytes*2)))
	buf.Write([]byte("\nFAILED: the answer is here"))

	out := buf.String()
	if !buf.Truncated() {
		t.Fatal("want truncated")
	}
	if !strings.Contains(out, "FIRST-LINE") {
		t.Error("head was dropped; want the command's opening context kept")
	}
	if !strings.Contains(out, "FAILED: the answer is here") {
		t.Error("tail was dropped; that is the regression this guards")
	}
	if !strings.Contains(out, "elided from the middle") {
		t.Errorf("want an elision notice naming what was lost, got %.120q", out)
	}
	// The notice adds a little; the payload must still respect the cap.
	if len(out) > maxBashOutputBytes+500 {
		t.Errorf("output grew past cap: %d", len(out))
	}
}

// Exactly-at-cap output is not truncated and must survive byte-for-byte.
func TestCappedBufferUnderCapIsExact(t *testing.T) {
	t.Parallel()
	var buf cappedBuffer
	want := strings.Repeat("ab", 100)
	buf.Write([]byte(want))
	if buf.Truncated() {
		t.Fatal("small output must not be marked truncated")
	}
	if got := buf.String(); got != want {
		t.Errorf("content changed: got %d bytes, want %d", len(got), len(want))
	}
}

// Byte-at-a-time writers (the common case for a streaming child process) must
// still produce the correct tail — this is what the ring buffer is for.
func TestCappedBufferSingleByteWrites(t *testing.T) {
	t.Parallel()
	var buf cappedBuffer
	for i := 0; i < maxBashOutputBytes+64; i++ {
		buf.Write([]byte{'a'})
	}
	buf.Write([]byte("ZZEND"))
	if !strings.HasSuffix(buf.String(), "ZZEND") {
		t.Error("ring buffer lost the tail under single-byte writes")
	}
}

func TestIsListingOrSearchBash(t *testing.T) {
	t.Parallel()
	yes := []string{
		"rg recall",
		`cd . && rg -n leftover`,
		"grep -R foo .",
		"find . -name '*.go'",
		"ls -la internal",
		"awk '{print}' src/app.rs",
		`python3 -c "import os; os.walk('.')"`,
	}
	for _, c := range yes {
		if !isListingOrSearchBash(c) {
			t.Errorf("want listing/search: %q", c)
		}
	}
	no := []string{
		"go test ./...",
		"go test ./packs/contextsink | rg FAIL",
		"cargo test",
		"echo hello",
		`python3 -c "print(1+1)"`,
		"git status --short",
	}
	for _, c := range no {
		if isListingOrSearchBash(c) {
			t.Errorf("must not clamp: %q", c)
		}
	}
}

func TestClampListingOutput(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	for i := 1; i <= grepMaxMatches+40; i++ {
		b.WriteString("hit ")
		b.WriteByte(byte('0' + i%10))
		b.WriteByte('\n')
	}
	got := clampListingOutput(b.String())
	if !strings.Contains(got, "listing cap") {
		t.Fatalf("want listing cap notice, got %.200q", got)
	}
	if n := strings.Count(got, "\n"); n > grepMaxMatches+2 {
		t.Fatalf("clamped listing still has %d lines", n)
	}
	// Short listing is unchanged aside from a possible trailing newline trim.
	if got := clampListingOutput("a\nb\n"); got != "a\nb" {
		t.Fatalf("short listing altered: %q", got)
	}
	long := strings.Repeat("m", grepMaxLineChars*3)
	clipped := clampListingOutput(long)
	if !strings.Contains(clipped, "line clipped") {
		t.Fatalf("want line clip, got %d chars", len(clipped))
	}
}

func TestClampGrepLine(t *testing.T) {
	t.Parallel()
	// A match inside a minified bundle must not eat the whole result budget.
	long := strings.Repeat("m", grepMaxLineChars*10)
	got := clampGrepLine(long)
	if len(got) > grepMaxLineChars+32 {
		t.Errorf("line not clamped: %d chars", len(got))
	}
	if !strings.HasSuffix(got, "…(line clipped)") {
		t.Error("want an explicit clip marker")
	}
	// Short lines pass through untouched.
	if got := clampGrepLine("func main() {}"); got != "func main() {}" {
		t.Errorf("short line altered: %q", got)
	}
	// Multi-byte runes must not be split mid-encoding.
	multi := strings.Repeat("é", grepMaxLineChars)
	if c := clampGrepLine(multi); strings.Contains(c, "\uFFFD") || !utf8.ValidString(c) {
		t.Error("clamp split a rune")
	}
}

func TestRenderReadPaging(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	for i := 1; i <= 50; i++ {
		b.WriteString("line")
		b.WriteByte(byte('0' + i%10))
		b.WriteByte('\n')
	}
	content := b.String()

	t.Run("window and continuation hint", func(t *testing.T) {
		out := renderRead(content, 10, 5, false, false, 1<<20)
		body, notice, ok := strings.Cut(out, "\n…")
		if !ok {
			t.Fatalf("want body + notice, got:\n%s", out)
		}
		if n := len(strings.Split(body, "\n")); n != 5 {
			t.Errorf("want exactly 5 lines in the window, got %d:\n%s", n, body)
		}
		out = "…" + notice
		// The notice must name the exact next call, or the model guesses.
		if !strings.Contains(out, "offset=15") {
			t.Errorf("want a continuation offset, got %q", out)
		}
		if !strings.Contains(out, "of 50") {
			t.Errorf("want the total line count, got %q", out)
		}
	})

	t.Run("last page says end of file", func(t *testing.T) {
		out := renderRead(content, 46, 10, false, false, 1<<20)
		if !strings.Contains(out, "end of file") {
			t.Errorf("want an end-of-file marker, got %q", out)
		}
		if strings.Contains(out, "offset=") {
			t.Error("must not offer a continuation past EOF")
		}
	})

	t.Run("whole small file has no notice", func(t *testing.T) {
		out := renderRead("a\nb\nc\n", 0, 0, false, false, 1<<20)
		if strings.Contains(out, "…") {
			t.Errorf("unpaged small read must be clean, got %q", out)
		}
	})

	t.Run("hashline numbers stay absolute", func(t *testing.T) {
		// An edit made from a paged read addresses real file lines; window
		// -relative numbering would silently target the wrong line.
		out := renderRead(content, 10, 3, true, false, 1<<20)
		if !strings.Contains(out, "    10:") {
			t.Errorf("want absolute line 10 in a paged hashline read, got %q", out)
		}
	})

	t.Run("byte cap is reported honestly", func(t *testing.T) {
		out := renderRead(content, 0, 0, false, true, 4096)
		if !strings.Contains(out, "read cap") {
			t.Errorf("want the byte-cap cause named, got %q", out)
		}
		// No offset may be offered: we do not know the real total.
		if strings.Contains(out, "offset=") {
			t.Error("must not name an offset when the byte cap hid the tail")
		}
	})
}

func TestFormatGrepHitsGroupsByFile(t *testing.T) {
	t.Parallel()
	got := formatGrepHits([]grepHit{
		{Path: "a.go", Line: 3, Text: "foo one"},
		{Path: "a.go", Line: 9, Text: "foo two"},
	}, false)
	if !strings.Contains(got, "a.go (2)") {
		t.Fatalf("want file header, got %q", got)
	}
	if !strings.Contains(got, "  3:foo one") || !strings.Contains(got, "  9:foo two") {
		t.Fatalf("want indented hits, got %q", got)
	}
	if strings.Contains(got, "files shown") {
		t.Fatalf("small result should not add a footer: %q", got)
	}

	var one []grepHit
	for i := 1; i <= grepMaxPerFile+5; i++ {
		one = append(one, grepHit{Path: "hot.go", Line: i, Text: "hit"})
	}
	got = formatGrepHits(one, false)
	if !strings.Contains(got, fmt.Sprintf("hot.go (%d)", grepMaxPerFile+5)) {
		t.Fatalf("want count in header, got %q", got)
	}
	if !strings.Contains(got, fmt.Sprintf("…(+%d more in this file)", 5)) {
		t.Fatalf("want per-file remainder, got %q", got)
	}
	if n := strings.Count(got, ":hit"); n != grepMaxPerFile {
		t.Fatalf("printed %d hit lines, want %d:\n%s", n, grepMaxPerFile, got)
	}

	var many []grepHit
	for i := 0; i < grepMaxFiles+10; i++ {
		many = append(many, grepHit{Path: fmt.Sprintf("f%02d.go", i), Line: 1, Text: "x"})
	}
	got = formatGrepHits(many, true)
	if n := strings.Count(got, ".go ("); n != grepMaxFiles {
		t.Fatalf("shown files %d, want %d:\n%s", n, grepMaxFiles, got)
	}
	if !strings.Contains(got, "files shown") || !strings.Contains(got, "walk stopped") {
		t.Fatalf("want summary footer, got %q", got)
	}
	if strings.Contains(got, "f00.go:1:") {
		t.Fatalf("old path:line dump leaked:\n%s", got)
	}
	if formatGrepHits(nil, false) != "(no matches)" {
		t.Fatal("empty hits")
	}
}

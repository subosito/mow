package tty

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestPlainInputBlankAndEOF(t *testing.T) {
	in := newInput(io.NopCloser(strings.NewReader("\nhello\n")), false)
	defer in.close()
	for _, want := range []string{"", "hello"} {
		got, err := in.read()
		if got != want || err != nil {
			t.Fatalf("read = %q, %v; want %q", got, err, want)
		}
	}
	if _, err := in.read(); !errors.Is(err, io.EOF) {
		t.Fatalf("want EOF, got %v", err)
	}
}

func TestPlainInputReportsOversize(t *testing.T) {
	in := newInput(io.NopCloser(strings.NewReader(strings.Repeat("x", 8<<20))), false)
	defer in.close()
	if _, err := in.read(); err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("expected input size error, got %v", err)
	}
}

func TestStatusFitsTerminalCells(t *testing.T) {
	for _, tc := range []struct {
		text  string
		width int
		want  string
	}{
		{"abcd", 3, "abc"}, {"abcd", 0, ""}, {"界界a", 3, "界"},
	} {
		if got := fitStatus(tc.text, tc.width); got != tc.want {
			t.Errorf("fit %q in %d = %q, want %q", tc.text, tc.width, got, tc.want)
		}
	}
}

package strx

import "testing"

func TestFirstNonEmpty(t *testing.T) {
	tests := []struct {
		name       string
		in         []string
		want       string
		wantUntrim string
	}{
		{name: "none", in: nil, want: "", wantUntrim: ""},
		{name: "all blank", in: []string{"", "  ", "\t"}, want: "", wantUntrim: ""},
		{name: "first wins", in: []string{"", " ", " x ", "y"}, want: "x", wantUntrim: " x "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := FirstNonEmpty(tc.in...); got != tc.want {
				t.Errorf("FirstNonEmpty = %q, want %q", got, tc.want)
			}
			if got := FirstNonEmptyRaw(tc.in...); got != tc.wantUntrim {
				t.Errorf("FirstNonEmptyRaw = %q, want %q", got, tc.wantUntrim)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name      string
		s         string
		n         int
		wantRunes string
		wantBytes string
	}{
		{name: "short", s: "hello", n: 10, wantRunes: "hello", wantBytes: "hello"},
		{name: "zero n", s: "hello", n: 0, wantRunes: "hello", wantBytes: "hello"},
		{name: "ascii cut", s: "abcdef", n: 3, wantRunes: "abc…", wantBytes: "abc…"},
		// "é" is two bytes: TruncateBytes must back up rather than split it.
		{name: "multibyte", s: "aéb", n: 2, wantRunes: "aé…", wantBytes: "a…"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := TruncateRunes(tc.s, tc.n); got != tc.wantRunes {
				t.Errorf("TruncateRunes = %q, want %q", got, tc.wantRunes)
			}
			got := TruncateBytes(tc.s, tc.n)
			if got != tc.wantBytes {
				t.Errorf("TruncateBytes = %q, want %q", got, tc.wantBytes)
			}
		})
	}
}

package cliutil

import (
	"flag"
	"strings"
	"testing"
)

func TestParseRootSpec(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		in       string
		wantPath string
		wantRO   bool
	}{
		{"bare path", "/tmp/a", "/tmp/a", false},
		{"read only suffix", "/tmp/a:ro", "/tmp/a", true},
		{"read write suffix", "/tmp/a:rw", "/tmp/a", false},
		{"upper case mode", "/tmp/a:RO", "/tmp/a", true},
		{"mixed case mode", "/tmp/a:Rw", "/tmp/a", false},
		{"surrounding spaces", "  /tmp/a:ro  ", "/tmp/a", true},
		{"inner space preserved", "/tmp/my dir:ro", "/tmp/my dir", true},
		{"empty", "", "", false},
		{"only spaces", "   ", "", false},
		{"windows drive not a mode", `C:\work`, `C:\work`, false},
		{"colon but not mode", "/tmp/a:rox", "/tmp/a:rox", false},
		{"mode only", ":ro", "", true},
		{"trailing colon", "/tmp/a:", "/tmp/a:", false},
		{"unicode path", "/tmp/データ:ro", "/tmp/データ", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			path, ro := parseRootSpec(c.in)
			if path != c.wantPath || ro != c.wantRO {
				t.Fatalf("parseRootSpec(%q)=(%q,%v) want (%q,%v)", c.in, path, ro, c.wantPath, c.wantRO)
			}
		})
	}
}

func TestSplitRootSpecsEdges(t *testing.T) {
	t.Parallel()
	t.Run("nil input", func(t *testing.T) {
		t.Parallel()
		rw, ro := splitRootSpecs(nil)
		if rw != nil || ro != nil {
			t.Fatalf("rw=%v ro=%v want nil,nil", rw, ro)
		}
	})
	t.Run("all blank dropped", func(t *testing.T) {
		t.Parallel()
		rw, ro := splitRootSpecs([]string{"", "   ", "\t"})
		if len(rw) != 0 || len(ro) != 0 {
			t.Fatalf("rw=%v ro=%v want empty", rw, ro)
		}
	})
	t.Run("mode only spec dropped", func(t *testing.T) {
		t.Parallel()
		// ":ro" has an empty path once the mode suffix is stripped.
		rw, ro := splitRootSpecs([]string{":ro", ":rw", "/keep"})
		if len(ro) != 0 {
			t.Fatalf("ro=%v want empty", ro)
		}
		if len(rw) != 1 || rw[0] != "/keep" {
			t.Fatalf("rw=%v want [/keep]", rw)
		}
	})
	t.Run("order preserved", func(t *testing.T) {
		t.Parallel()
		rw, ro := splitRootSpecs([]string{"/1", "/2:ro", "/3", "/4:ro"})
		if strings.Join(rw, ",") != "/1,/3" {
			t.Fatalf("rw=%v", rw)
		}
		if strings.Join(ro, ",") != "/2,/4" {
			t.Fatalf("ro=%v", ro)
		}
	})
}

func TestStringListValue(t *testing.T) {
	t.Parallel()

	t.Run("nil receiver string", func(t *testing.T) {
		t.Parallel()
		var s *stringList
		if got := s.String(); got != "" {
			t.Fatalf("nil String()=%q want empty", got)
		}
	})

	t.Run("append and join", func(t *testing.T) {
		t.Parallel()
		var s stringList
		if got := s.String(); got != "" {
			t.Fatalf("zero String()=%q", got)
		}
		for _, v := range []string{"a", "  b  ", "", "   ", "c"} {
			if err := s.Set(v); err != nil {
				t.Fatalf("Set(%q): %v", v, err)
			}
		}
		// Blank values are skipped, others are trimmed.
		if got := s.String(); got != "a,b,c" {
			t.Fatalf("String()=%q want a,b,c", got)
		}
	})

	t.Run("special characters kept", func(t *testing.T) {
		t.Parallel()
		var s stringList
		vals := []string{"a,b", "line\nbreak", "quote\"x", "emoji🙂"}
		for _, v := range vals {
			if err := s.Set(v); err != nil {
				t.Fatal(err)
			}
		}
		if len(s) != len(vals) {
			t.Fatalf("len=%d want %d (%v)", len(s), len(vals), s)
		}
		if s[0] != "a,b" || s[3] != "emoji🙂" {
			t.Fatalf("values mangled: %v", s)
		}
	})

	t.Run("very long value", func(t *testing.T) {
		t.Parallel()
		var s stringList
		long := strings.Repeat("x", 10000)
		if err := s.Set(long); err != nil {
			t.Fatal(err)
		}
		if len(s) != 1 || len(s[0]) != 10000 {
			t.Fatalf("long value truncated: len=%d", len(s[0]))
		}
	})

	t.Run("implements flag.Value", func(t *testing.T) {
		t.Parallel()
		var s stringList
		var _ flag.Value = &s
	})
}

func TestMaxTurnsFlagValue(t *testing.T) {
	t.Parallel()

	t.Run("nil receiver", func(t *testing.T) {
		t.Parallel()
		var m *maxTurnsFlag
		if got := m.String(); got != "0" {
			t.Fatalf("nil String()=%q want 0", got)
		}
	})

	t.Run("nil inner flags", func(t *testing.T) {
		t.Parallel()
		m := &maxTurnsFlag{}
		if got := m.String(); got != "0" {
			t.Fatalf("String()=%q want 0", got)
		}
	})

	t.Run("set values", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			in      string
			want    int
			wantErr bool
		}{
			{in: "0", want: 0},
			{in: "1", want: 1},
			{in: "42", want: 42},
			{in: "-3", want: -3},
			{in: "abc", wantErr: true},
			{in: "", wantErr: true},
			{in: "1.5", wantErr: true},
			{in: " 7", wantErr: true}, // strconv.Atoi rejects spaces
			{in: "99999999999999999999", wantErr: true},
		}
		for _, c := range cases {
			var ef EngineFlags
			m := &maxTurnsFlag{f: &ef}
			err := m.Set(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("Set(%q) want error", c.in)
				}
				if ef.MaxTurnsSet {
					t.Fatalf("Set(%q) failed but marked MaxTurnsSet", c.in)
				}
				continue
			}
			if err != nil {
				t.Fatalf("Set(%q): %v", c.in, err)
			}
			if !ef.MaxTurnsSet || ef.MaxTurns != c.want {
				t.Fatalf("Set(%q): MaxTurns=%d set=%v want %d,true", c.in, ef.MaxTurns, ef.MaxTurnsSet, c.want)
			}
			if got := m.String(); got != strings.TrimSpace(c.in) {
				t.Fatalf("String()=%q want %q", got, c.in)
			}
		}
	})
}

func TestClipRunes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"empty", "", 10, ""},
		{"short", "abc", 10, "abc"},
		{"exact", "abcde", 5, "abcde"},
		{"clipped", "abcdef", 5, "abcd…"},
		{"whitespace collapsed", "a  \t b\nc", 10, "a b c"},
		{"zero max", "abcdef", 0, "abcdef"},
		{"negative max", "abcdef", -1, "abcdef"},
		{"max one", "abcdef", 1, "a"},
		{"max two", "abcdef", 2, "a…"},
		{"unicode counted by rune", "日本語テキスト", 4, "日本語…"},
		{"unicode exact", "日本語", 3, "日本語"},
		{"only whitespace", "   \n\t ", 5, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := clipRunes(c.in, c.max); got != c.want {
				t.Fatalf("clipRunes(%q,%d)=%q want %q", c.in, c.max, got, c.want)
			}
		})
	}
}

func TestClipRunesLongInput(t *testing.T) {
	t.Parallel()
	got := clipRunes(strings.Repeat("héllo ", 5000), 72)
	if n := len([]rune(got)); n != 72 {
		t.Fatalf("rune len=%d want 72", n)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("missing ellipsis: %q", got)
	}
}

func TestToolProgressDetailUnexported(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		tool string
		args string
		want string
	}{
		{"nil-ish empty args", "read", ``, ""},
		{"empty object", "read", `{}`, ""},
		{"non-object json", "read", `[1,2]`, ""},
		{"null value", "read", `{"path":null}`, ""},
		{"non-string value", "read", `{"path":123}`, ""},
		{"whitespace path", "read", `{"path":"   "}`, ""},
		{"case-insensitive tool", "READ", `{"path":"x.go"}`, "x.go"},
		{"write path", "write", `{"path":"a/b.go"}`, "a/b.go"},
		{"edit path", "edit", `{"path":"a/b.go"}`, "a/b.go"},
		{"grep dot path ignored", "grep", `{"pattern":"foo","path":"."}`, "foo"},
		{"grep empty pattern", "grep", `{"pattern":"","path":"pkg"}`, ""},
		{"bash command", "bash", `{"command":"echo hi"}`, "echo hi"},
		{"fallback query", "search", `{"query":"needle"}`, "needle"},
		{"fallback first match wins", "x", `{"name":"n","path":"p"}`, "p"},
		{"fallback none", "x", `{"other":"v"}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := toolProgressDetail(c.tool, []byte(c.args))
			if got != c.want {
				t.Fatalf("toolProgressDetail(%q,%s)=%q want %q", c.tool, c.args, got, c.want)
			}
		})
	}
}

// panicValue is a flag.Value whose zero value panics on String(), exercising
// the recover path in isZeroValue.
type panicValue struct{ s string }

func (p panicValue) String() string {
	if p.s == "" {
		panic("boom")
	}
	return p.s
}

func (p *panicValue) Set(v string) error { p.s = v; return nil }

func TestIsZeroValueRecovers(t *testing.T) {
	t.Parallel()
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	v := &panicValue{s: "set"}
	fs.Var(v, "boom", "panics on zero String()")
	f := fs.Lookup("boom")
	// Must not panic; a panicking zero value is reported as "not zero".
	if isZeroValue(f, "set") {
		t.Fatal("want false when zero-value String() panics")
	}
}

// mapValue is a flag.Value implemented on a non-pointer type, exercising the
// reflect.Zero branch of isZeroValue (as opposed to reflect.New for pointers).
type mapValue map[string]string

func (m mapValue) String() string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return strings.Join(keys, ",")
}

func (m mapValue) Set(v string) error { m[v] = v; return nil }

func TestIsZeroValueNonPointer(t *testing.T) {
	t.Parallel()
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.Var(mapValue{}, "m", "map flag")
	f := fs.Lookup("m")
	if !isZeroValue(f, "") {
		t.Fatal("empty default on a non-pointer flag.Value should be zero")
	}
	if isZeroValue(f, "a") {
		t.Fatal("non-empty default should not be zero")
	}
}

func TestIsZeroValueBasics(t *testing.T) {
	t.Parallel()
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.String("s", "", "")
	fs.String("s2", "def", "")
	fs.Int("n", 0, "")
	fs.Int("n2", 5, "")
	fs.Bool("b", false, "")
	fs.Bool("b2", true, "")

	want := map[string]bool{"s": true, "s2": false, "n": true, "n2": false, "b": true, "b2": false}
	for name, zero := range want {
		f := fs.Lookup(name)
		if got := isZeroValue(f, f.DefValue); got != zero {
			t.Errorf("isZeroValue(%s,%q)=%v want %v", name, f.DefValue, got, zero)
		}
	}
}

func TestFlagDash(t *testing.T) {
	t.Parallel()
	cases := map[string]string{"a": "-", "": "--", "ab": "--", "config": "--", "日": "--"}
	for in, want := range cases {
		if got := flagDash(in); got != want {
			t.Errorf("flagDash(%q)=%q want %q", in, got, want)
		}
	}
}

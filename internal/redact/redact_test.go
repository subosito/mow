package redact

import (
	"strings"
	"testing"
)

func TestSanitize(t *testing.T) {
	in := "api_key=supersecret123\nnormal line\nBearer abc.def.ghi\nkey sk-proj-Ab12_cd34-EF56gh78 and sk-ant-api03-XYZxyz0123456789"
	out := Sanitize(in)
	if strings.Contains(out, "supersecret123") || strings.Contains(out, "abc.def.ghi") {
		t.Fatalf("secrets leaked: %q", out)
	}
	if strings.Contains(out, "Ab12_cd34") || strings.Contains(out, "XYZxyz0123456789") {
		t.Fatalf("prefixed sk- keys leaked: %q", out)
	}
	if !strings.Contains(out, "normal line") {
		t.Fatalf("lost benign line: %q", out)
	}
	for _, line := range []string{
		"Authorization: Bearer abc123def456",
		"authorization=Bearer abc123def456",
		"header Bearer abc123def456 sent",
	} {
		if got := Sanitize(line); strings.Contains(got, "abc123def456") {
			t.Fatalf("%q leaked token: %q", line, got)
		}
	}
}

func TestRingCap(t *testing.T) {
	r := NewRing(32)
	if _, err := r.Write([]byte(strings.Repeat("a", 100))); err != nil {
		t.Fatal(err)
	}
	if r.Len() > 32 {
		t.Fatalf("len=%d cap=32", r.Len())
	}
}

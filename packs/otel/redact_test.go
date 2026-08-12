package otel

import (
	"strings"
	"testing"
)

func TestRedactSecrets(t *testing.T) {
	t.Parallel()
	in := "failed: authorization=Bearer secret-token sk-abcdefghijklmnopqrstuvwxyz"
	out := redactSecrets(in)
	if strings.Contains(out, "secret-token") || strings.Contains(out, "sk-abcdefghijklmnopqrstuvwxyz") {
		t.Fatalf("secrets leaked: %q", out)
	}
	if !strings.Contains(out, "[redacted]") {
		t.Fatalf("expected redaction marker: %q", out)
	}
}

func TestClampAttrTruncates(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", maxAttrRunes+50)
	out := clampAttr(long)
	if len([]rune(out)) > maxAttrRunes+1 {
		t.Fatalf("clampAttr did not truncate: runes=%d", len([]rune(out)))
	}
	if !strings.HasSuffix(out, "…") {
		t.Fatalf("expected ellipsis suffix: %q", out)
	}
}

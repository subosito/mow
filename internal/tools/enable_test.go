package tools

import (
	"strings"
	"testing"
)

func TestUnknownEnable(t *testing.T) {
	got := UnknownEnable(
		[]string{"read", "understand_image", "READ", " ", "not_a_tool"},
		[]string{"read", "glob", "grep"},
	)
	if len(got) != 2 || got[0] != "understand_image" || got[1] != "not_a_tool" {
		t.Fatalf("UnknownEnable = %v", got)
	}
	if UnknownEnable([]string{"read", "glob"}, []string{"read", "glob", "grep"}) != nil {
		t.Fatal("expected no unknown names")
	}
}

func TestFormatUnregisteredEnable(t *testing.T) {
	one := FormatUnregisteredEnable([]string{"understand_image"}, false)
	if !strings.Contains(one, "tools.enable lists understand_image, but it is not registered in this binary") {
		t.Fatalf("wording: %q", one)
	}
	if !strings.Contains(one, "lean mow has no packs/media") || !strings.Contains(one, "mowx") {
		t.Fatalf("lean hint: %q", one)
	}
	full := FormatUnregisteredEnable([]string{"understand_image"}, true)
	if strings.Contains(full, "lean mow") {
		t.Fatalf("mowx should not get the lean hint: %q", full)
	}
	many := FormatUnregisteredEnable([]string{"foo", "bar"}, false)
	if !strings.Contains(many, "lists foo, bar, but they are not registered") {
		t.Fatalf("plural: %q", many)
	}
}

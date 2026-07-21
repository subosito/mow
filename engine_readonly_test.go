package mow

import "testing"

func TestIsReadOnlyTool(t *testing.T) {
	extRO := map[string]bool{"mcp_srv_lookup": true}
	cases := []struct {
		name string
		want bool
	}{
		{"read", true},
		{"glob", true},
		{"grep", true},
		{"understand_image", true},
		{"write", false},
		{"edit", false},
		{"bash", false},
		{"generate_image", false},
		{"mcp_srv_lookup", true},   // declared readOnlyHint
		{"mcp_srv_execute", false}, // undeclared ext tool
		{"acp_delegate", false},
	}
	for _, c := range cases {
		if got := isReadOnlyTool(c.name, extRO); got != c.want {
			t.Errorf("isReadOnlyTool(%q)=%v want %v", c.name, got, c.want)
		}
	}
}

func TestIsPowerTool(t *testing.T) {
	for name, want := range map[string]bool{
		"write": true, "edit": true, "bash": true, "BASH": true,
		"read": false, "grep": false, "mcp_x_y": false,
	} {
		if got := IsPowerTool(name); got != want {
			t.Errorf("IsPowerTool(%q)=%v want %v", name, got, want)
		}
	}
}

package acp

import (
	"encoding/json"
	"testing"
)

func TestParsePermissionOutcomeRejectAlways(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw  string
		want string
	}{
		{`{"outcome":{"outcome":"selected","optionId":"reject_always"}}`, optRejectAlways},
		{`{"outcome":{"outcome":"selected","optionId":"reject-always"}}`, optRejectAlways},
		{`{"outcome":{"outcome":"selected","optionId":"allow_always"}}`, optAllowAlways},
		{`{"outcome":{"outcome":"selected","optionId":"allow_once"}}`, optAllowOnce},
		{`{"outcome":{"outcome":"cancelled"}}`, optRejectOnce},
		{``, optRejectOnce},
	}
	for _, tc := range cases {
		got := parsePermissionOutcome(json.RawMessage(tc.raw))
		if got != tc.want {
			t.Fatalf("parsePermissionOutcome(%s)=%q want %q", tc.raw, got, tc.want)
		}
	}
}

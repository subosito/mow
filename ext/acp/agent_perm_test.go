package acp

import (
	"context"
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

// "Allow/reject always for this session" must not outlive the session.
func TestSessionCloseClearsAlwaysDecisions(t *testing.T) {
	a := &agentServer{
		cancels:      map[string]context.CancelFunc{},
		sessions:     map[string]*acpSession{"s1": {mode: ModeCode}},
		alwaysAllow:  map[string]bool{"bash": true},
		alwaysReject: map[string]bool{"write": true},
		boundSID:     "s1",
	}
	a.handleSessionClose(request{ID: json.RawMessage(`1`), Params: json.RawMessage(`{"sessionId":"s1"}`)})
	if len(a.alwaysAllow) != 0 || len(a.alwaysReject) != 0 {
		t.Fatalf("always decisions survived session/close: allow=%v reject=%v", a.alwaysAllow, a.alwaysReject)
	}
}

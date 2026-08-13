package rpc

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The advertised method list is only useful if it cannot drift from what
// dispatch actually serves. Parse the case labels out of dispatch's switch and
// compare both directions: a method served but not advertised is undiscoverable,
// and one advertised but not served is a lie that costs a client a round trip.
func TestRPCCapabilitiesMatchDispatch(t *testing.T) {
	src, err := os.ReadFile("rpc.go")
	if err != nil {
		t.Fatalf("read rpc.go: %v", err)
	}
	body := string(src)
	start := strings.Index(body, "func (s *Server) dispatch(")
	if start < 0 {
		t.Fatal("dispatch not found")
	}
	end := strings.Index(body[start:], "\nfunc ")
	if end < 0 {
		end = len(body) - start
	}
	dispatchBody := body[start : start+end]

	caseRe := regexp.MustCompile(`(?m)^\tcase (.+):$`)
	strRe := regexp.MustCompile(`"([^"]+)"`)
	served := map[string]bool{}
	for _, m := range caseRe.FindAllStringSubmatch(dispatchBody, -1) {
		for _, lit := range strRe.FindAllStringSubmatch(m[1], -1) {
			served[lit[1]] = true
		}
	}
	if len(served) == 0 {
		t.Fatal("parsed no case labels from dispatch")
	}
	// Aliases exist for compatibility and need not be advertised separately.
	aliases := map[string]bool{"session_id": true}

	advertised := map[string]bool{}
	for _, m := range methodNames {
		advertised[m] = true
	}
	for m := range served {
		if !advertised[m] && !aliases[m] {
			t.Errorf("dispatch serves %q but capabilities do not advertise it", m)
		}
	}
	for m := range advertised {
		if !served[m] {
			t.Errorf("capabilities advertise %q but dispatch does not serve it", m)
		}
	}
}

// Control methods must be a subset of the served surface, or a client will
// send something mid-turn that comes back as unknown.
func TestRPCControlMethodsAreServed(t *testing.T) {
	for _, m := range methodNames {
		if isControlMethod(m) && !isControlMethod(strings.ToUpper(m)) {
			t.Errorf("isControlMethod(%q) is case sensitive", m)
		}
	}
	for _, m := range []string{"cancel", "status", "capabilities"} {
		if !isControlMethod(m) {
			t.Errorf("%q must stay answerable mid-turn", m)
		}
	}
	if isControlMethod("prompt") {
		t.Error("prompt must not be a control method (it is the turn itself)")
	}
}

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
	for _, m := range []string{"cancel", "status", "capabilities", "extension.config"} {
		if !isControlMethod(m) {
			t.Errorf("%q must stay answerable mid-turn", m)
		}
	}
	// Worker-queue methods: routed off the control channel so a full
	// prompt/compact queue cannot starve cancel/status. slash is control-
	// routed but still runs in a goroutine (see dispatch).
	for _, m := range []string{"prompt", "compact"} {
		if isControlMethod(m) {
			t.Errorf("%q must remain a worker method, not control", m)
		}
	}
	if !isControlMethod("slash") {
		t.Error("slash must stay control-routed so it never hits the worker queue-full path")
	}
}

// Epoch 1 is the first public wire contract. A wrong constant here would
// break mowi and every other external host overnight.
func TestRPCCompatibilityEpochIs1(t *testing.T) {
	if rpcProtocolVersion != "1" {
		t.Fatalf("rpcProtocolVersion=%q; epoch 1 is the public contract", rpcProtocolVersion)
	}
	// capabilitiesResult must surface the same constant clients gate on.
	srv := &Server{}
	caps := srv.capabilitiesResult()
	if got, _ := caps["rpc"].(string); got != "1" {
		t.Fatalf("capabilities rpc=%v; want \"1\"", caps["rpc"])
	}
}

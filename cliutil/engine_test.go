package cliutil

import (
	"reflect"
	"testing"
)

func TestSplitRootSpecs(t *testing.T) {
	rw, ro, _ := splitRootSpecs([]string{
		"/tmp/a",
		"/tmp/b:ro",
		"/tmp/c:RW",
		"  /tmp/d:Ro  ",
		"",
	})
	if !reflect.DeepEqual(rw, []string{"/tmp/a", "/tmp/c"}) {
		t.Fatalf("rw=%v", rw)
	}
	if !reflect.DeepEqual(ro, []string{"/tmp/b", "/tmp/d"}) {
		t.Fatalf("ro=%v", ro)
	}
}

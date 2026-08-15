package contextsink

import "testing"

func TestContextSearchIsUntrusted(t *testing.T) {
	if !newContextSearchTool(t.TempDir(), "session").Untrusted() {
		t.Fatal("recall recovery must be marked untrusted")
	}
}

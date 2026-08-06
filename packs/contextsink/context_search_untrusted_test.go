package contextsink

import "testing"

func TestContextSearchIsUntrusted(t *testing.T) {
	if !newContextSearchTool(t.TempDir(), "session").Untrusted() {
		t.Fatal("context_search recovery must be marked untrusted")
	}
}

package acp

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestPromptErrorMessageScrubsDial(t *testing.T) {
	raw := fmt.Errorf(`Post "http://127.0.0.1:9420/v1/responses": dial tcp 127.0.0.1:9420: connect: connection refused`)
	got := promptErrorMessage(raw)
	if got != "provider unavailable (connection refused)" {
		t.Fatalf("got %q", got)
	}
	if strings.Contains(got, "://") || strings.Contains(got, "9420") {
		t.Fatalf("leaked endpoint: %q", got)
	}

	wrapped := fmt.Errorf("llm: provider unavailable (connection refused): %w", raw)
	if promptErrorMessage(wrapped) != "provider unavailable (connection refused)" {
		t.Fatalf("wrapped got %q", promptErrorMessage(wrapped))
	}

	keep := errors.New("mow: empty prompt")
	if promptErrorMessage(keep) != keep.Error() {
		t.Fatalf("non-transport should pass through, got %q", promptErrorMessage(keep))
	}
}

package llm

import "testing"

// The anthropic-messages wire requires max_tokens in every request, so this
// value is always sent — there is no "let the provider decide" option.
//
// It used to be a hard-coded 8192 with no config key at all, which meant a
// model advertising 128000 was cut at 6% of its capacity. That is not merely a
// short answer: a reply cut mid tool-call makes the loop refuse the batch, and
// after a couple of retries the run fails — with an error advising the operator
// to "raise llm.max_tokens", a knob that did not exist.
func TestAnthropicMaxTokens(t *testing.T) {
	t.Parallel()

	catalog := map[string]ModelInfo{
		"claude-sonnet-5": {ID: "claude-sonnet-5", MaxOutputTokens: 128_000},
		"no-cap-model":    {ID: "no-cap-model"},
	}

	t.Run("explicit config wins", func(t *testing.T) {
		t.Parallel()
		c := &Client{Model: "claude-sonnet-5", MaxTokens: 4096, CatalogModels: catalog}
		if got := c.anthropicMaxTokens(); got != 4096 {
			t.Errorf("got %d, want the configured 4096", got)
		}
	})

	t.Run("catalog cap is used when config is silent", func(t *testing.T) {
		t.Parallel()
		c := &Client{Model: "claude-sonnet-5", CatalogModels: catalog}
		if got := c.anthropicMaxTokens(); got != 128_000 {
			t.Errorf("got %d, want the published 128000 — this is the regression", got)
		}
	})

	t.Run("floor when the catalog is silent", func(t *testing.T) {
		t.Parallel()
		c := &Client{Model: "no-cap-model", CatalogModels: catalog}
		if got := c.anthropicMaxTokens(); got != defaultAnthropicMaxTokens {
			t.Errorf("got %d, want the %d floor", got, defaultAnthropicMaxTokens)
		}
	})

	t.Run("floor with no catalog at all", func(t *testing.T) {
		t.Parallel()
		c := &Client{Model: "whatever"}
		if got := c.anthropicMaxTokens(); got != defaultAnthropicMaxTokens {
			t.Errorf("got %d, want the %d floor", got, defaultAnthropicMaxTokens)
		}
	})

	t.Run("never returns zero", func(t *testing.T) {
		t.Parallel()
		// The wire rejects a request without max_tokens, so every path must
		// yield something positive.
		for _, c := range []*Client{
			{},
			{Model: "x"},
			{Model: "no-cap-model", CatalogModels: catalog},
		} {
			if got := c.anthropicMaxTokens(); got <= 0 {
				t.Errorf("client %+v produced max_tokens=%d", c, got)
			}
		}
	})
}

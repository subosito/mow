package mow_test

import (
	"testing"

	"github.com/subosito/mow"
)

func TestFilterChatModels(t *testing.T) {
	in := []mow.ModelInfo{
		// Plain OpenAI-compatible catalog (id only) → keep all.
		{ID: "deepseek-chat"},
		{ID: "gpt-5-mini"},
		// Optional gateway wire + facet metadata.
		{ID: "chat-model", Wire: "openai-chat-completions", Facet: "chat"},
		{ID: "chat-model:image", Wire: "openai-chat-completions", Facet: "image"},
		{ID: "chat-model:search", Wire: "openai-chat-completions", Facet: "search"},
		// Colon in id is NOT a facet signal when facet is chat (or empty).
		{ID: "vendor:org/model-v1", Wire: "openai-chat-completions", Facet: "chat"},
		{ID: "tts-model", Wire: "openai-audio-speech", Facet: "chat"},
		{ID: "image-model", Wire: "openai-images-generations"},
		{ID: "claude-model", Wire: "anthropic-messages", Facet: "chat"},
		{ID: ""}, // empty id dropped
	}
	got := mow.FilterChatModels(in)
	want := map[string]bool{
		"deepseek-chat":       true,
		"gpt-5-mini":          true,
		"chat-model":          true,
		"vendor:org/model-v1": true,
		"claude-model":        true,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d: %+v", len(got), got)
	}
	for _, m := range got {
		if !want[m.ID] {
			t.Fatalf("unexpected %q", m.ID)
		}
	}
}

func TestIsChatModel(t *testing.T) {
	if !mow.IsChatModel(mow.ModelInfo{ID: "x"}) {
		t.Fatal("plain id should be chat")
	}
	if mow.IsChatModel(mow.ModelInfo{ID: "x", Facet: "image"}) {
		t.Fatal("image facet should not be chat")
	}
	if mow.IsChatModel(mow.ModelInfo{ID: "x", Wire: "openai-images-generations"}) {
		t.Fatal("image wire should not be chat")
	}
	if !mow.IsChatModel(mow.ModelInfo{ID: "x", Wire: "openai-response", Facet: "chat"}) {
		t.Fatal("openai-response alias is a known chat wire")
	}
	// Search facets are a gateway packaging choice, not a distinct endpoint:
	// they share the chat wire, and provider-executed search is really a
	// declared tool on the bare model. Verified against a live gateway: the
	// bare id with tools:[{"type":"web_search"}] searches correctly, while
	// "<model>:search" without it leaks an unexecuted tool call as text.
	// Keep them out of the chat loop so neither trap reaches the agent.
	for _, facet := range []string{"search", "search_x"} {
		m := mow.ModelInfo{ID: "grok:" + facet, Wire: "openai-responses", Facet: facet}
		if mow.IsChatModel(m) {
			t.Fatalf("%q facet must not be offered as a chat model", facet)
		}
	}
}

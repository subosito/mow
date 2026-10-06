package acp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/subosito/mow"
)

// A typo model must fail fast — at config time and before a peer spawns —
// instead of looking "ok" (running…) until the delegate timeout expires.
func TestApplyModelConfigRejectsUnknownModel(t *testing.T) {
	prov := &modelProv{
		model: "real-model",
		list: []mow.ModelInfo{
			{ID: "real-model", Wire: "openai-chat-completions", Facet: "chat"},
			{ID: "other-model", Wire: "openai-chat-completions", Facet: "chat"},
		},
	}
	eng, err := mow.New(mow.Options{NoSession: true, Model: "real-model", Provider: prov})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	a := &agentServer{eng: eng}

	if err := a.applyModelConfig(t.Context(), "typo-model"); err == nil {
		t.Fatal("typo model must be rejected")
	} else if !strings.Contains(err.Error(), "unknown model") {
		t.Fatalf("err=%q want unknown-model error", err)
	}
	if err := a.applyModelConfig(t.Context(), "other-model"); err != nil {
		t.Fatalf("known model rejected: %v", err)
	}
	// Tier suffix and provider prefix still resolve.
	if err := a.applyModelConfig(t.Context(), "other-model-high"); err != nil {
		t.Fatalf("tiered model rejected: %v", err)
	}
	if err := a.applyModelConfig(t.Context(), "gateway/other-model-high"); err != nil {
		t.Fatalf("provider-prefixed tiered model rejected: %v", err)
	}
}

type blockingListProv struct {
	model string
}

func (p *blockingListProv) Chat(context.Context, []mow.Message, []mow.ToolSpec, mow.ChatHooks) (mow.Message, error) {
	return mow.Message{Role: "assistant", Content: "ok"}, nil
}

func (p *blockingListProv) ListModels(ctx context.Context) ([]mow.ModelInfo, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (p *blockingListProv) SetModel(id string) error {
	p.model = id
	return nil
}

func TestDelegateModelCheckDoesNotUsePeerTimeout(t *testing.T) {
	prev := nativeModelCheckTimeout
	nativeModelCheckTimeout = 40 * time.Millisecond
	t.Cleanup(func() { nativeModelCheckTimeout = prev })

	eng, err := mow.New(mow.Options{
		NoSession: true, Workspace: t.TempDir(), Model: "real-model",
		Provider: &blockingListProv{model: "real-model"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })

	// An unavailable catalog is skipped, but the wait must be the short model
	// check — not the peer's timeout_sec (30s on this spec's usual budget).
	ctx, cancel := modelCheckContext(mow.ContextWithEngine(t.Context(), eng))
	defer cancel()
	start := time.Now()
	if err := validateNativePeerModel(ctx, PeerSpec{Name: "peer", Model: "other-model"}); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("catalog check took %s; peer timeout leaked into the model check", time.Since(start))
	}
}

func TestLockSlotUnblocksOnCancel(t *testing.T) {
	var mu sync.Mutex
	mu.Lock()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- lockSlot(ctx, &mu) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("lockSlot ignored cancel")
	}
	mu.Unlock()
	deadline := time.Now().Add(time.Second)
	for !mu.TryLock() {
		if time.Now().After(deadline) {
			t.Fatal("mutex left locked after cancel")
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Unlock()
}

func TestDelegateRejectsUnknownNativeModelFast(t *testing.T) {
	prov := &modelProv{
		model: "real-model",
		list: []mow.ModelInfo{
			{ID: "real-model", Wire: "openai-chat-completions", Facet: "chat"},
			{ID: "other-model", Wire: "openai-chat-completions", Facet: "chat"},
		},
	}
	eng, err := mow.New(mow.Options{NoSession: true, Workspace: t.TempDir(), Model: "real-model", Provider: prov})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })

	tool := &delegateTool{
		agents: map[string]PeerSpec{
			"peer": {Name: "peer", Model: "typo-model", TimeoutSec: 30},
		},
		workspace: eng.Workspace(),
		peers:     map[string]*peerSlot{},
	}
	ctx := mow.ContextWithEngine(t.Context(), eng)
	_, err = tool.Exec(ctx, json.RawMessage(`{"agent":"peer","prompt":"hi"}`))
	if err == nil {
		t.Fatal("typo native model must fail before spawn")
	}
	if !strings.Contains(err.Error(), "unknown model") || !strings.Contains(err.Error(), "typo-model") {
		t.Fatalf("err=%q want unknown-model error naming the typo", err)
	}
	// Nothing was spawned or pooled.
	tool.peersMu.Lock()
	n := len(tool.peers)
	tool.peersMu.Unlock()
	if n != 0 {
		t.Fatalf("peers=%d want 0 (rejected before spawn)", n)
	}
}

func TestValidateNativePeerModelAcceptsKnown(t *testing.T) {
	prov := &modelProv{
		model: "real-model",
		list: []mow.ModelInfo{
			{ID: "real-model", Wire: "openai-chat-completions", Facet: "chat"},
			{ID: "other-model", Wire: "openai-chat-completions", Facet: "chat"},
		},
	}
	eng, err := mow.New(mow.Options{NoSession: true, Model: "real-model", Provider: prov})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	ctx := mow.ContextWithEngine(t.Context(), eng)

	if err := validateNativePeerModel(ctx, PeerSpec{Name: "peer", Model: "other-model"}); err != nil {
		t.Fatalf("known model rejected: %v", err)
	}
	// A one-row catalog that only echoes the host model still rejects a typo.
	one := &modelProv{
		model: "real-model",
		list: []mow.ModelInfo{
			{ID: "real-model", Wire: "openai-chat-completions", Facet: "chat"},
		},
	}
	eng1, err := mow.New(mow.Options{NoSession: true, Model: "real-model", Provider: one})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng1.Close() })
	if err := validateNativePeerModel(mow.ContextWithEngine(t.Context(), eng1), PeerSpec{Name: "peer", Model: "typo-model"}); err == nil {
		t.Fatal("typo must be rejected even when the catalog only lists the host model")
	}
	// A provider override has a different catalog; the host cannot validate it.
	if err := validateNativePeerModel(ctx, PeerSpec{Name: "peer", Model: "provider-model", Provider: "other"}); err != nil {
		t.Fatalf("provider-specific model rejected against host catalog: %v", err)
	}
	// External command peers are untouched.
	if err := validateNativePeerModel(ctx, PeerSpec{Name: "ext", Command: []string{"whatever"}}); err != nil {
		t.Fatalf("external peer must skip validation: %v", err)
	}
}

func TestMatchCatalogModelRejectsAmbiguousBareID(t *testing.T) {
	list := []mow.ModelInfo{
		{ID: "provider-a/shared-model"},
		{ID: "provider-b/shared-model"},
	}
	if _, ok := matchCatalogModel(list, "shared-model"); ok {
		t.Fatal("bare id must not choose an arbitrary provider")
	}
	if got, ok := matchCatalogModel(list, "provider-b/shared-model-high"); !ok || got.ID != "provider-b/shared-model" {
		t.Fatalf("qualified tiered match=(%q, %v), want provider-b/shared-model", got.ID, ok)
	}
}

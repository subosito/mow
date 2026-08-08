package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// llm.prompt_cache was historically a bool. Old configs must keep working
// unchanged, and the new names must map to the right wire behavior.
func TestCacheModeUnmarshal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		yaml    string
		want    CacheMode
		enabled bool
		ttl     string
		wantErr bool
	}{
		{name: "legacy true", yaml: "prompt_cache: true", want: CacheModeShort, enabled: true},
		{name: "legacy false", yaml: "prompt_cache: false", want: CacheModeNone, enabled: false},
		{name: "short", yaml: "prompt_cache: short", want: CacheModeShort, enabled: true},
		{name: "long", yaml: "prompt_cache: long", want: CacheModeLong, enabled: true, ttl: "1h"},
		{name: "none", yaml: "prompt_cache: none", want: CacheModeNone, enabled: false},
		{name: "case insensitive", yaml: "prompt_cache: LONG", want: CacheModeLong, enabled: true, ttl: "1h"},
		// A typo'd cache setting is invisible in behavior but shows up on the
		// bill, so it must fail loudly rather than default.
		{name: "unknown rejected", yaml: "prompt_cache: forever", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got struct {
				PromptCache *CacheMode `yaml:"prompt_cache"`
			}
			err := yaml.Unmarshal([]byte(tc.yaml), &got)
			if tc.wantErr {
				if err == nil {
					t.Fatal("want an error for an unknown cache mode")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.PromptCache == nil {
				t.Fatal("prompt_cache not decoded")
			}
			if *got.PromptCache != tc.want {
				t.Errorf("mode = %q, want %q", *got.PromptCache, tc.want)
			}
			if got.PromptCache.Enabled() != tc.enabled {
				t.Errorf("Enabled() = %v, want %v", got.PromptCache.Enabled(), tc.enabled)
			}
			if got.PromptCache.TTL() != tc.ttl {
				t.Errorf("TTL() = %q, want %q", got.PromptCache.TTL(), tc.ttl)
			}
		})
	}
}

// Unset means enabled at the provider default — caching a repeated prefix is
// a pure win, so it stays opt-out.
func TestCacheModeUnsetIsDefaultOn(t *testing.T) {
	t.Parallel()
	var got struct {
		PromptCache *CacheMode `yaml:"prompt_cache"`
	}
	if err := yaml.Unmarshal([]byte("model: gpt-5-mini"), &got); err != nil {
		t.Fatal(err)
	}
	if got.PromptCache != nil {
		t.Fatalf("want nil for an unset key, got %q", *got.PromptCache)
	}
}

package config

import (
	"fmt"
	"strings"
)

// CacheMode is the prompt-cache setting for llm.prompt_cache.
//
// Historically this key was a plain bool. It stays bool-compatible while
// gaining a third state, because "on" is really two different decisions:
// whether to mark cache breakpoints at all, and how long the marked prefix
// should live. A 5-minute ephemeral window is the provider default and is a
// poor fit for interactive use — a coffee break silently re-charges the whole
// prefix as fresh input tokens.
type CacheMode string

const (
	// CacheModeNone writes no cache_control markers.
	//
	// This is the right setting for one-shot calls (a compaction summary, a
	// single review pass, a delegated sub-task): they build a cache entry that
	// nothing will ever read, and a cache *write* is billed above plain input.
	// Caching a prefix you will not reuse is a pure surcharge.
	CacheModeNone CacheMode = "none"
	// CacheModeShort uses the provider default ephemeral TTL (~5 min).
	CacheModeShort CacheMode = "short"
	// CacheModeLong requests a 1h ephemeral TTL.
	CacheModeLong CacheMode = "long"
)

// Enabled reports whether cache breakpoints should be written.
func (m CacheMode) Enabled() bool { return m != CacheModeNone }

// TTL is the wire ttl value, empty for the provider default.
func (m CacheMode) TTL() string {
	if m == CacheModeLong {
		return "1h"
	}
	return ""
}

// UnmarshalYAML accepts `true`/`false` (the historical shape) and the strings
// short|long|none. Unknown values are an error rather than a silent default:
// a typo'd cache setting is invisible in behavior but shows up on the bill.
func (m *CacheMode) UnmarshalYAML(unmarshal func(any) error) error {
	var b bool
	if err := unmarshal(&b); err == nil {
		if b {
			*m = CacheModeShort
		} else {
			*m = CacheModeNone
		}
		return nil
	}
	var s string
	if err := unmarshal(&s); err != nil {
		return fmt.Errorf("llm.prompt_cache: want a bool or one of short|long|none")
	}
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "none", "off", "false":
		*m = CacheModeNone
	case "short", "on", "true", "":
		*m = CacheModeShort
	case "long":
		*m = CacheModeLong
	default:
		return fmt.Errorf("llm.prompt_cache: unknown value %q (want short|long|none)", s)
	}
	return nil
}

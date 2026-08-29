package config

import (
	"fmt"
	"strings"
)

// LLMProviderProfile is a named endpoint overlay under llm.providers.
// Only fields that change with the vendor: URL, credentials, wire, headers,
// default model. Timeouts, prompt_cache, and system_prefix stay on live llm.*.
type LLMProviderProfile struct {
	BaseURL   string            `yaml:"base_url"`
	APIKey    string            `yaml:"api_key"`
	APIKeyEnv string            `yaml:"api_key_env"`
	Wire      string            `yaml:"wire"`
	Headers   map[string]string `yaml:"headers"`
	Model     string            `yaml:"model"`
}

// ApplyNamedProvider overlays llm.providers[name] onto the live llm.* fields.
// Empty name is a no-op. Unknown name is an error. Non-empty profile fields
// replace the corresponding live values; nil Headers leaves live headers.
func (f *File) ApplyNamedProvider(name string) error {
	if f == nil {
		return nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	p, ok := lookupProvider(f.LLM.Providers, name)
	if !ok {
		return fmt.Errorf("llm.provider %q is not defined in llm.providers", name)
	}
	applyProviderProfile(&f.LLM, p)
	f.LLM.Provider = name
	return nil
}

func lookupProvider(m map[string]LLMProviderProfile, name string) (LLMProviderProfile, bool) {
	if len(m) == 0 {
		return LLMProviderProfile{}, false
	}
	if p, ok := m[name]; ok {
		return p, true
	}
	want := strings.ToLower(name)
	for k, p := range m {
		if strings.ToLower(strings.TrimSpace(k)) == want {
			return p, true
		}
	}
	return LLMProviderProfile{}, false
}

func applyProviderProfile(dst *LLMConfig, p LLMProviderProfile) {
	if s := strings.TrimSpace(p.Wire); s != "" {
		dst.Wire = s
		dst.WireExplicit = true
	}
	if s := strings.TrimSpace(p.BaseURL); s != "" {
		dst.BaseURL = s
	}
	if s := strings.TrimSpace(p.APIKey); s != "" {
		dst.APIKey = s
	}
	if s := strings.TrimSpace(p.APIKeyEnv); s != "" {
		dst.APIKeyEnv = s
	}
	if s := strings.TrimSpace(p.Model); s != "" {
		dst.Model = s
	}
	if p.Headers != nil {
		dst.Headers = map[string]string{}
		for k, v := range p.Headers {
			dst.Headers[k] = v
		}
	}
}

func mergeLLMProviders(dst *LLMConfig, o LLMConfig) {
	if s := strings.TrimSpace(o.Provider); s != "" {
		dst.Provider = s
	}
	if len(o.Providers) == 0 {
		return
	}
	if dst.Providers == nil {
		dst.Providers = map[string]LLMProviderProfile{}
	}
	for k, v := range o.Providers {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		dst.Providers[k] = v
	}
}

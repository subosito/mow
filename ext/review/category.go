package review

import (
	"encoding/json"
	"fmt"
	"strings"
)

// marshalEnum encodes a wire name, rejecting the unset zero value so a
// half-built Report never serializes as a silently-empty enum.
func marshalEnum(name string) ([]byte, error) {
	if name == "" {
		return nil, fmt.Errorf("review: cannot marshal unset enum value")
	}
	return json.Marshal(name)
}

// unmarshalEnum accepts a JSON string (numbers and objects are rejected).
func unmarshalEnum(b []byte) (string, error) {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return "", fmt.Errorf("review: enum must be a JSON string: %w", err)
	}
	return s, nil
}

func errEnum(field, got string, allowed []string) error {
	return fmt.Errorf("review: invalid %s %q (want one of: %s)", field, got, strings.Join(allowed, ", "))
}

// Category is a finding taxonomy label. Categories are profile-scoped strings
// rather than a closed Go enum: profiles own their taxonomy, and an unknown
// label from the model is normalized to "other" instead of failing the run.
type Category string

// General review categories (profile "general").
const (
	CatCorrectness      Category = "correctness"
	CatErrorHandling    Category = "error-handling"
	CatTests            Category = "tests"
	CatAPICompatibility Category = "api-compatibility"
	CatConcurrency      Category = "concurrency"
	CatPerformance      Category = "performance"
	CatMaintainability  Category = "maintainability"
	CatReadability      Category = "readability"
	CatConfig           Category = "config"
	CatDependencies     Category = "dependencies"
	CatSecurityNote     Category = "security-note"
	CatOther            Category = "other"
)

// Security review categories (profile "security").
const (
	CatAuthn           Category = "authn"
	CatAuthz           Category = "authz"
	CatInjection       Category = "injection"
	CatSSRF            Category = "ssrf"
	CatPathTraversal   Category = "path-traversal"
	CatDeserialization Category = "deserialization"
	CatXSS             Category = "xss"
	CatSecretLeak      Category = "secret-leak"
	CatCrypto          Category = "crypto"
	CatTrustBoundary   Category = "trust-boundary"
	CatSupplyChain     Category = "supply-chain"
	CatDenialOfService Category = "dos"
)

// generalCategories is the allowed taxonomy for the general profile.
var generalCategories = []Category{
	CatCorrectness, CatErrorHandling, CatTests, CatAPICompatibility,
	CatConcurrency, CatPerformance, CatMaintainability, CatReadability,
	CatConfig, CatDependencies, CatSecurityNote, CatOther,
}

// securityCategories is the allowed taxonomy for the security profile.
var securityCategories = []Category{
	CatAuthn, CatAuthz, CatInjection, CatSSRF, CatPathTraversal,
	CatDeserialization, CatXSS, CatSecretLeak, CatCrypto, CatConfig,
	CatTrustBoundary, CatSupplyChain, CatDenialOfService, CatOther,
}

// categoryAliases maps common model spellings onto canonical labels.
var categoryAliases = map[string]Category{
	"bug":                  CatCorrectness,
	"logic":                CatCorrectness,
	"correctness-bug":      CatCorrectness,
	"error":                CatErrorHandling,
	"errors":               CatErrorHandling,
	"error_handling":       CatErrorHandling,
	"testing":              CatTests,
	"test":                 CatTests,
	"api":                  CatAPICompatibility,
	"api_compatibility":    CatAPICompatibility,
	"compat":               CatAPICompatibility,
	"race":                 CatConcurrency,
	"concurrency-bug":      CatConcurrency,
	"perf":                 CatPerformance,
	"style":                CatReadability,
	"docs":                 CatMaintainability,
	"maintenance":          CatMaintainability,
	"configuration":        CatConfig,
	"deps":                 CatDependencies,
	"dependency":           CatDependencies,
	"security":             CatSecurityNote,
	"authentication":       CatAuthn,
	"auth":                 CatAuthn,
	"authorization":        CatAuthz,
	"access-control":       CatAuthz,
	"sqli":                 CatInjection,
	"sql-injection":        CatInjection,
	"command-injection":    CatInjection,
	"rce":                  CatInjection,
	"path_traversal":       CatPathTraversal,
	"traversal":            CatPathTraversal,
	"insecure-deser":       CatDeserialization,
	"cross-site-scripting": CatXSS,
	"secrets":              CatSecretLeak,
	"secret":               CatSecretLeak,
	"credentials":          CatSecretLeak,
	"cryptography":         CatCrypto,
	"tls":                  CatCrypto,
	"trust":                CatTrustBoundary,
	"supply_chain":         CatSupplyChain,
	"denial-of-service":    CatDenialOfService,
	"dos-risk":             CatDenialOfService,
}

// NormalizeCategory canonicalizes a raw label for the given allowed set.
// Unknown labels fall back to "other" so one odd string cannot fail a report.
func NormalizeCategory(raw string, allowed []Category) Category {
	c := Category(strings.ToLower(strings.TrimSpace(strings.ReplaceAll(raw, " ", "-"))))
	if c == "" {
		return CatOther
	}
	for _, a := range allowed {
		if c == a {
			return c
		}
	}
	if alias, ok := categoryAliases[string(c)]; ok {
		for _, a := range allowed {
			if alias == a {
				return alias
			}
		}
	}
	return CatOther
}

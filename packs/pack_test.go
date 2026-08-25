package packs_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// packs/ is the public-API tier: it must build on mow.* / ext.* only.
//
// Go does not enforce this. The internal/ rule is import-path-prefix based and
// packs/ sits under github.com/subosito/mow/, so the import compiles happily —
// it drifted once already (a pack test imported internal/config for
// a single "MOW_HOME" string). Keep the tier honest here instead.
//
// ext/ is the privileged tier and is deliberately exempt: see
// docs/extensions.md. If a pack genuinely needs core internals it belongs in
// ext/, not in a widened exception to this test.
//
// This test covers every pack in packs/, linked or not — packs are not
// blank-imported by cmd/mow but must still honor the tier rule.
func TestPacksDoNotImportInternal(t *testing.T) {
	const needle = `"github.com/subosito/mow/internal/`

	var bad []string
	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == "testdata" || info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// This file names the import path in its own docs.
		if filepath.Base(path) == "pack_test.go" {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(src), needle) {
			bad = append(bad, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(bad) > 0 {
		t.Fatalf("packs/ must not import internal/… (move the pack to ext/ or use public API):\n  %s",
			strings.Join(bad, "\n  "))
	}
}

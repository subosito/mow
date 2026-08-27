// Package extcfg decodes extensions.<name> from explicit config paths.
// It is shared by core extensions and optional packs before mow.New.
//
// DecodeSection never falls back to $MOW_HOME on its own. Hosts that want
// user/global config must pass that path explicitly (engine.New does so when
// Options.LoadUserConfig is true). Hermetic embedding therefore cannot pick
// up MCP/acp sections from the operator's home by accident.
package extcfg

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/subosito/mow"
)

// DecodeSection unmarshals extensions.<section> into dst from configPaths only
// (later files win). Returns true if a section was found.
//
// Paths are tried in order, matching Load / mergeExtensions: global, then
// profile, then explicit --config. A later file that contains the named
// section replaces an earlier one wholesale (not a field-wise merge). Missing
// files and files without that section are skipped so they cannot wipe a
// prior hit. A present file with a YAML error fails the call. Callers that
// want $MOW_HOME/config.yaml must include it in configPaths (CLI/host via
// LoadUserConfig).
func DecodeSection(section string, configPaths []string, dst any) (bool, error) {
	section = strings.TrimSpace(section)
	if section == "" {
		return false, nil
	}
	seen := map[string]bool{}
	var found yaml.Node
	hit := false
	for _, p := range configPaths {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var root struct {
			Extensions map[string]yaml.Node `yaml:"extensions"`
		}
		if err := yaml.Unmarshal(raw, &root); err != nil {
			return false, err
		}
		n, ok := root.Extensions[section]
		if !ok || n.Kind == 0 {
			continue
		}
		found = n
		hit = true
	}
	if !hit {
		return false, nil
	}
	if err := found.Decode(dst); err != nil {
		return false, err
	}
	return true, nil
}

// IncludesUserConfig reports whether configPaths already contains the global
// $MOW_HOME/config.yaml path. Extension packs use this to gate home-file
// fallbacks (mcp.json) so hermetic BeforeNew calls
// with only explicit paths never touch the operator home.
func IncludesUserConfig(configPaths []string) bool {
	want := filepath.Clean(filepath.Join(mow.Home(), "config.yaml"))
	for _, p := range configPaths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if filepath.Clean(p) == want {
			return true
		}
	}
	return false
}

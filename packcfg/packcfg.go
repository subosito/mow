// Package packcfg decodes extensions.<name> from config paths / $MOW_HOME.
// Not a pack: used by ext/mcp, ext/lsp, etc. before mow.New.
package packcfg

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/subosito/mow"
)

// DecodeSection unmarshals extensions.<section> into dst.
// Tries configPaths, then $MOW_HOME/config.yaml. Returns true if a section was found.
func DecodeSection(section string, configPaths []string, dst any) (bool, error) {
	section = strings.TrimSpace(section)
	if section == "" {
		return false, nil
	}
	paths := append([]string{}, configPaths...)
	paths = append(paths, filepath.Join(mow.Home(), "config.yaml"))
	seen := map[string]bool{}
	for _, p := range paths {
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
		if err := n.Decode(dst); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

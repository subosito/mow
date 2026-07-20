package packcfg_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/subosito/mow/packcfg"
)

func TestDecodeSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	raw := []byte(`
extensions:
  mcp:
    servers:
      - name: demo
        command: true
`)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	var c struct {
		Servers []struct {
			Name    string `yaml:"name"`
			Command string `yaml:"command"`
		} `yaml:"servers"`
	}
	ok, err := packcfg.DecodeSection("mcp", []string{path}, &c)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if len(c.Servers) != 1 || c.Servers[0].Name != "demo" {
		t.Fatalf("%+v", c)
	}
}

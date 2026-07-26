package ops

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestResolveOpsNameExplicit(t *testing.T) {
	t.Setenv("MOW_OPS", "")
	if _, err := resolveOpsName(""); err == nil {
		t.Fatal("expected error without name")
	}
	t.Setenv("MOW_OPS", "fleet")
	got, err := resolveOpsName("")
	if err != nil || got != "fleet" {
		t.Fatalf("env: %q %v", got, err)
	}
	got, err = resolveOpsName("lab")
	if err != nil || got != "lab" {
		t.Fatalf("arg wins: %q %v", got, err)
	}
	if _, err := resolveOpsName("../x"); err == nil {
		t.Fatal("reject path traversal")
	}
}

func TestLoadProfileActionsAndACP(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "fleet")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := []byte(`
model: example-model
services:
  - name: gateway
    logs: [/var/log/gateway/app.json]
    actions:
      restart: [/bin/true, restart-gateway]
      status:  [/bin/echo, active]
    acp: gateway-peer
    notes: example service
acp:
  agents:
    - name: gateway-peer
      command: [example-agent, --acp]
      dir: /tmp/gateway
`)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte("Watch the fleet."), 0o644); err != nil {
		t.Fatal(err)
	}
	pack := PackConfig{Root: root}
	p, err := loadProfile("fleet", pack)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "fleet" || p.Model != "example-model" || len(p.Services) != 1 {
		t.Fatalf("%+v", p)
	}
	argv, err := p.actionArgv("gateway", "restart")
	if err != nil || len(argv) != 2 || argv[0] != "/bin/true" {
		t.Fatalf("restart argv=%v err=%v", argv, err)
	}
	if _, err := p.actionArgv("gateway", "reload"); err == nil {
		t.Fatal("reload should fail")
	}
	if len(p.ACP.Agents) != 1 || p.ACP.Agents[0].Name != "gateway-peer" {
		t.Fatalf("acp=%+v", p.ACP)
	}
	sys := p.systemAppend()
	if !strings.Contains(sys, "fleet") || !strings.Contains(sys, "Watch the fleet") {
		t.Fatalf("sys=%s", sys)
	}
}

func TestLogPathAllowed(t *testing.T) {
	svc := Service{Name: "gateway", Logs: []string{"/var/log/gateway/app.json"}}
	if !logPathAllowed(svc, "/var/log/gateway/app.json") {
		t.Fatal("expected allow")
	}
	if logPathAllowed(svc, "/etc/passwd") {
		t.Fatal("must deny foreign path")
	}
}

func TestIncidentOpenDedupe(t *testing.T) {
	dir := t.TempDir()
	if _, err := openIncident(dir, "gateway", "sig-502", "upstream 502", "first"); err != nil {
		t.Fatal(err)
	}
	out2, err := openIncident(dir, "gateway", "sig-502", "upstream 502 again", "second")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out2, "existing open") {
		t.Fatalf("expected dedupe, got %s", out2)
	}
	inc := findOpenBySignature(dir, "sig-502")
	if inc == nil {
		t.Fatal("missing")
	}
	closed, err := updateIncident(dir, inc.ID, "", "done", true)
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(closed, "{")
	var got Incident
	if err := json.Unmarshal([]byte(closed[i:]), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "closed" {
		t.Fatalf("status=%q", got.Status)
	}
}

func TestReadLogFileTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.json")
	var b strings.Builder
	for i := 0; i < 50; i++ {
		b.WriteString(`{"i":` + strconv.Itoa(i) + "}\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	lines, err := readLogFile(path, 5, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 5 || !strings.Contains(lines[4], `"i":49`) {
		t.Fatalf("%v", lines)
	}
}

func TestPeelOpsFlag(t *testing.T) {
	name, rest := peelOpsFlag([]string{"--ops", "fleet", "services", "--workspace", "."})
	if name != "fleet" || len(rest) != 3 || rest[0] != "services" {
		t.Fatalf("%q %v", name, rest)
	}
}

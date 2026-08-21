package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorAcceptsMediaEnableWhenPackLinked(t *testing.T) {
	if mowBinary == "" {
		t.Skip("mowBinary unset")
	}
	home := t.TempDir()
	body := "llm:\n  model: gpt-5-mini\ntools:\n  enable: [read, glob, grep, understand_image]\nextensions:\n  media:\n    understand:\n      image: gpt-4o\n"
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(mowBinary, "doctor")
	cmd.Env = append(os.Environ(), "MOW_HOME="+home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mow doctor: %v\n%s", err, out)
	}
	text := string(out)
	if strings.Contains(text, "not registered") {
		t.Fatalf("mow-full should treat understand_image as known:\n%s", text)
	}
	if !strings.Contains(text, "understand_image") {
		t.Fatalf("want tools check to list understand_image:\n%s", text)
	}
	if !strings.Contains(text, "mow-full") {
		t.Fatalf("want binary identity mow-full:\n%s", text)
	}
}

func TestDoctorDoesNotStartMCPAndBundleIsRedacted(t *testing.T) {
	if mowBinary == "" {
		t.Skip("mowBinary unset")
	}
	home := t.TempDir()
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("llm:\n  api_key: sk-secret-do-not-copy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "mcp.json"), []byte(`{"mcpServers":{"demo":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(mowBinary, "doctor")
	cmd.Dir = ws
	cmd.Env = append(os.Environ(), "MOW_HOME="+home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mow doctor: %v\n%s", err, out)
	}
	text := string(out)
	if !strings.Contains(text, "mcp") || !strings.Contains(text, "not started") {
		t.Fatalf("want mcp listed as not started:\n%s", text)
	}
	if strings.Contains(text, "sk-secret") {
		t.Fatal("doctor printed a secret")
	}

	cmd = exec.Command(mowBinary, "approvals", "remember", "allow", "bash", `{"command":"ls"}`)
	cmd.Dir = ws
	cmd.Env = append(os.Environ(), "MOW_HOME="+home)
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mow approvals remember: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "remembered allow bash") {
		t.Fatalf("remember output: %s", out)
	}

	cmd = exec.Command(mowBinary, "approvals")
	cmd.Dir = ws
	cmd.Env = append(os.Environ(), "MOW_HOME="+home)
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mow approvals: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "bash") {
		t.Fatalf("list output: %s", out)
	}

	cmd = exec.Command(mowBinary, "doctor", "--bundle")
	cmd.Dir = ws
	cmd.Env = append(os.Environ(), "MOW_HOME="+home)
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mow doctor --bundle: %v\n%s", err, out)
	}
	path := strings.TrimSpace(string(out))
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "sk-secret") || strings.Contains(string(body), "api_key") {
		t.Fatalf("bundle leaked secrets:\n%s", body)
	}
}

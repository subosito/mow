package acp

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaterializePromptTextDoesNotCreateMediaDir(t *testing.T) {
	ws := t.TempDir()
	if _, err := materializePrompt([]ContentBlock{{Type: "text", Text: "hello"}}, ws, "s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ws, "media", "acp")); !os.IsNotExist(err) {
		t.Fatalf("text-only prompt created media/acp: %v", err)
	}
}

func TestMaterializePromptImage(t *testing.T) {
	ws := t.TempDir()
	png := []byte{0x89, 0x50, 0x4e, 0x47}
	b64 := base64.StdEncoding.EncodeToString(png)
	text, err := materializePrompt([]ContentBlock{
		{Type: "text", Text: "what is this?"},
		{Type: "image", Data: b64, MimeType: "image/png"},
	}, ws, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "what is this?") {
		t.Fatalf("text=%q", text)
	}
	if !strings.Contains(text, "media/acp/") || !strings.Contains(text, "understand_image") {
		t.Fatalf("missing attachment note: %q", text)
	}
	entries, err := os.ReadDir(filepath.Join(ws, "media", "acp"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
	data, err := os.ReadFile(filepath.Join(ws, "media", "acp", entries[0].Name()))
	if err != nil || string(data) != string(png) {
		t.Fatalf("file data=%v err=%v", data, err)
	}
}

func TestMaterializePromptResourceText(t *testing.T) {
	ws := t.TempDir()
	text, err := materializePrompt([]ContentBlock{
		{Type: "resource", Resource: &ResourceContents{URI: "file:///x.go", Text: "package main"}},
	}, ws, "s")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "package main") || !strings.Contains(text, "x.go") {
		t.Fatalf("%q", text)
	}
}

func TestModeState(t *testing.T) {
	m := modeState(ModeAsk)
	if m["currentModeId"] != ModeAsk {
		t.Fatal(m)
	}
	modes, _ := m["availableModes"].([]map[string]any)
	if len(modes) != 2 {
		t.Fatal(modes)
	}
}

func TestMaterializePromptRejectsMediaSymlinkEscape(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(ws, "media")); err != nil {
		t.Fatal(err)
	}
	b64 := base64.StdEncoding.EncodeToString([]byte("outside"))
	_, err := materializePrompt([]ContentBlock{{Type: "image", Data: b64, MimeType: "image/png"}}, ws, "s1")
	if err == nil {
		t.Fatal("expected media symlink escape to be rejected")
	}
	entries, readErr := os.ReadDir(outside)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("outside directory modified: %v", entries)
	}
}

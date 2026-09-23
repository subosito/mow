package acp

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subosito/mow"
)

func TestMaterializePromptTextDoesNotCreateMediaDir(t *testing.T) {
	ws := t.TempDir()
	if _, _, err := materializePrompt([]ContentBlock{{Type: "text", Text: "hello"}}, ws, "s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ws, "media", "acp")); !os.IsNotExist(err) {
		t.Fatalf("text-only prompt created media/acp: %v", err)
	}
}

func TestMaterializePromptRequiresWorkspace(t *testing.T) {
	_, _, err := materializePrompt([]ContentBlock{{Type: "text", Text: "hi"}}, "", "s1")
	if err == nil {
		t.Fatal("expected error for empty workspace")
	}
}

func TestExpandPromptRefsDoesNotInlineBinaryImage(t *testing.T) {
	ws := t.TempDir()
	image := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	if err := os.WriteFile(filepath.Join(ws, "photo.PNG"), image, 0o600); err != nil {
		t.Fatal(err)
	}
	eng, err := mow.New(mow.Options{Workspace: ws, NoSession: true, Chat: func(context.Context, []mow.Message, []mow.ToolSpec) (mow.Message, error) {
		return mow.Message{Role: "assistant", Content: "ok"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	got := expandPromptFileRefs(eng, "inspect @photo.PNG")
	if !strings.Contains(got, "Image file reference") || !strings.Contains(got, "ACP image content") || strings.Contains(got, string(image)) {
		t.Fatalf("image reference expanded unsafely: %q", got)
	}
}

func TestMaterializePromptImage(t *testing.T) {
	ws := t.TempDir()
	png := []byte{0x89, 0x50, 0x4e, 0x47}
	b64 := base64.StdEncoding.EncodeToString(png)
	text, media, err := materializePrompt([]ContentBlock{
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
	if len(media) != 1 || media[0].Type != "image" || media[0].MimeType != "image/png" {
		t.Fatalf("media=%+v", media)
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

func TestMaterializePromptAudioAndVideo(t *testing.T) {
	for _, tc := range []struct {
		typeName, mime, wantTool, wantExt string
	}{
		{"audio", "audio/wav", "understand_voice", ".wav"},
		{"video", "video/mp4", "understand_video", ".mp4"},
	} {
		t.Run(tc.typeName, func(t *testing.T) {
			ws := t.TempDir()
			text, media, err := materializePrompt([]ContentBlock{{Type: tc.typeName, Data: base64.StdEncoding.EncodeToString([]byte("media")), MimeType: tc.mime}}, ws, "s")
			wantMedia := 1
			if tc.typeName == "video" {
				wantMedia = 0
			}
			if err != nil || len(media) != wantMedia || wantMedia == 1 && media[0].Type != tc.typeName || !strings.Contains(text, tc.wantTool) || !strings.Contains(text, "if an appropriate extension is installed") {
				t.Fatalf("text=%q err=%v", text, err)
			}
			entries, err := os.ReadDir(filepath.Join(ws, "media", "acp"))
			if err != nil || len(entries) != 1 || filepath.Ext(entries[0].Name()) != tc.wantExt {
				t.Fatalf("entries=%v err=%v", entries, err)
			}
		})
	}
}

func TestMaterializePromptResourceText(t *testing.T) {
	ws := t.TempDir()
	text, _, err := materializePrompt([]ContentBlock{
		{Type: "resource", Resource: &ResourceContents{URI: "file:///x.go", Text: "package main"}},
	}, ws, "s")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "package main") || !strings.Contains(text, "x.go") {
		t.Fatalf("%q", text)
	}
}

func TestMaterializePromptRejectsMediaSymlinkEscape(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(ws, "media")); err != nil {
		t.Fatal(err)
	}
	b64 := base64.StdEncoding.EncodeToString([]byte("outside"))
	_, _, err := materializePrompt([]ContentBlock{{Type: "image", Data: b64, MimeType: "image/png"}}, ws, "s1")
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

func TestExpandPromptFileRefsJailSafe(t *testing.T) {
	ws := t.TempDir()
	inside := filepath.Join(ws, "note.txt")
	if err := os.WriteFile(inside, []byte("hello-ref"), 0o644); err != nil {
		t.Fatal(err)
	}
	eng, err := mow.New(mow.Options{
		Workspace: ws,
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	got := expandPromptFileRefs(eng, "see @note.txt please")
	if !strings.Contains(got, "hello-ref") || !strings.Contains(got, "--- note.txt ---") {
		t.Fatalf("got=%q", got)
	}
	escaped := expandPromptFileRefs(eng, "see @../secret.txt")
	if strings.Contains(escaped, "---") {
		t.Fatalf("escaped jail: %q", escaped)
	}
	// An email or decorator is not a file mention even when the file exists.
	if err := os.WriteFile(filepath.Join(ws, "example.com"), []byte("leak"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := expandPromptFileRefs(eng, "mail user@example.com today"); got != "mail user@example.com today" {
		t.Fatalf("email expanded: %q", got)
	}
	if got := expandPromptFileRefs(eng, "@note.txt first"); !strings.Contains(got, "hello-ref") {
		t.Fatalf("leading ref not expanded: %q", got)
	}
}

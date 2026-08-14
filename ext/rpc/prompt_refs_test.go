package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subosito/mow"
)

func TestExpandPromptFileRefsUsesEngineJailAndCapsContent(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("hello ref"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(strings.Repeat("x", maxPromptRefBytes+50)), 0o600); err != nil {
		t.Fatal(err)
	}
	eng, err := mow.New(mow.Options{Workspace: root, NoSession: true, Chat: func(context.Context, []mow.Message, []mow.ToolSpec) (mow.Message, error) {
		return mow.Message{Role: "assistant", Content: "ok"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Engine: eng}
	got, attached := s.expandPromptFileRefs("read @note.md and @note.md, then @big.txt")
	if strings.Count(got, "--- note.md ---") != 1 {
		t.Fatalf("dedupe failed: %q", got)
	}
	if !strings.Contains(got, "```markdown\nhello ref") {
		t.Fatalf("content/language missing: %q", got)
	}
	if !strings.Contains(got, "… (truncated)") {
		t.Fatal("large reference was not capped")
	}
	if len(attached) != 2 || attached[0] != "note.md" || attached[1] != "big.txt" {
		t.Fatalf("attached = %v", attached)
	}
}

func TestExpandPromptFileRefsRejectsEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret-marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	eng, err := mow.New(mow.Options{Workspace: root, NoSession: true, Chat: func(context.Context, []mow.Message, []mow.ToolSpec) (mow.Message, error) {
		return mow.Message{Role: "assistant", Content: "ok"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Engine: eng}
	text := "inspect @../" + filepath.Base(filepath.Dir(outside)) + "/secret.txt"
	got, attached := s.expandPromptFileRefs(text)
	if got != text || len(attached) != 0 || strings.Contains(got, "secret-marker") {
		t.Fatalf("escape expanded: %q %v", got, attached)
	}
}

func TestPromptEphemeralAndAttachedResultShape(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("attached body"), 0o600); err != nil {
		t.Fatal(err)
	}
	var seen []mow.Message
	eng, err := mow.New(mow.Options{
		Workspace: root,
		NoSession: true,
		Chat: func(_ context.Context, messages []mow.Message, _ []mow.ToolSpec) (mow.Message, error) {
			seen = append([]mow.Message{}, messages...)
			return mow.Message{Role: "assistant", Content: "aside answer"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	in := io.NopCloser(strings.NewReader(`{"id":1,"method":"prompt","params":{"text":"look @note.txt","ephemeral":true}}` + "\n"))
	var wire bytes.Buffer
	stream := false
	if err := (&Server{Engine: eng, In: in, Out: &wire, StreamEvents: &stream}).Serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(wire.Bytes()), &response); err != nil {
		t.Fatalf("wire: %v (%q)", err, wire.String())
	}
	if response.Error != nil {
		t.Fatalf("prompt: %+v", response.Error)
	}
	var out struct {
		Ephemeral bool     `json:"ephemeral"`
		Attached  []string `json:"attached"`
	}
	if err := json.Unmarshal(response.Result, &out); err != nil {
		t.Fatal(err)
	}
	if !out.Ephemeral || len(out.Attached) != 1 || out.Attached[0] != "note.txt" {
		t.Fatalf("result = %+v", out)
	}
	if len(seen) == 0 || !strings.Contains(seen[len(seen)-1].Content, "attached body") {
		t.Fatalf("model did not receive attachment: %+v", seen)
	}
	if got := eng.Transcript(); len(got) != 0 {
		t.Fatalf("ephemeral prompt persisted: %+v", got)
	}
}

package tools_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subosito/mow/internal/llm"
	"github.com/subosito/mow/internal/policy"
	"github.com/subosito/mow/internal/tools"
)

func TestUnderstandImageTool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Mow-Component") != "tool.understand_image" {
			t.Errorf("component=%q", r.Header.Get("X-Mow-Component"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": "screenshot of login form"}},
			},
		})
	}))
	defer srv.Close()

	ws := t.TempDir()
	img := filepath.Join(ws, "shot.png")
	if err := os.WriteFile(img, []byte{0x89, 0x50, 0x4e, 0x47}, 0o644); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{Workspace: ws, MaxReadBytes: 1 << 20}
	mc := &llm.MediaClient{BaseURL: srv.URL + "/v1", APIKey: "k"}
	list := tools.MediaTools(p, tools.MediaOptions{Client: mc, UnderstandImage: "vl-m"})
	if len(list) != 1 || list[0].Name() != "understand_image" {
		t.Fatalf("%v", list)
	}
	out, err := list[0].Exec(context.Background(), json.RawMessage(`{"path":"shot.png"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "login form") {
		t.Fatalf("out=%q", out)
	}
}

func TestImageGenToolWritesMediaDir(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{
				{"b64_json": base64.StdEncoding.EncodeToString([]byte("fakepng"))},
			},
		})
	}))
	defer srv.Close()

	ws := t.TempDir()
	p := &policy.Policy{Workspace: ws}
	mc := &llm.MediaClient{BaseURL: srv.URL + "/v1", APIKey: "k"}
	list := tools.MediaTools(p, tools.MediaOptions{Client: mc, GenerateImage: "img-m"})
	out, err := list[0].Exec(context.Background(), json.RawMessage(`{"prompt":"cat"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "path: media/") || !strings.Contains(out, "bytes:") {
		t.Fatalf("out=%q", out)
	}
	// file exists under media/
	ents, _ := os.ReadDir(filepath.Join(ws, "media"))
	if len(ents) != 1 {
		t.Fatalf("media dir: %v", ents)
	}
}

func TestUnderstandVoiceTool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if r.Header.Get("X-Mow-Component") != "tool.understand_voice" {
			t.Errorf("component=%q", r.Header.Get("X-Mow-Component"))
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "hello world"})
	}))
	defer srv.Close()

	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "a.wav"), []byte("RIFF...."), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{Workspace: ws}
	mc := &llm.MediaClient{BaseURL: srv.URL + "/v1", APIKey: "k"}
	list := tools.MediaTools(p, tools.MediaOptions{Client: mc, UnderstandVoice: "whisper"})
	if list[0].Name() != "understand_voice" {
		t.Fatal(list[0].Name())
	}
	out, err := list[0].Exec(context.Background(), json.RawMessage(`{"path":"a.wav"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello world" {
		t.Fatalf("%q", out)
	}
}

func TestSpeechGenToolWritesMediaDir(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/speech" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if r.Header.Get("X-Mow-Component") != "tool.generate_speech" {
			t.Errorf("component=%q", r.Header.Get("X-Mow-Component"))
		}
		var body struct {
			Model  string `json:"model"`
			Input  string `json:"input"`
			Voice  string `json:"voice"`
			Format string `json:"response_format"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Voice != tools.DefaultSpeechVoiceID {
			t.Errorf("voice=%q want default DefaultSpeechVoiceID", body.Voice)
		}
		if body.Format != "mp3" {
			t.Errorf("format=%q want mp3", body.Format)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("AUDIODATA"))
	}))
	defer srv.Close()

	ws := t.TempDir()
	p := &policy.Policy{Workspace: ws}
	mc := &llm.MediaClient{BaseURL: srv.URL + "/v1", APIKey: "k"}
	list := tools.MediaTools(p, tools.MediaOptions{Client: mc, GenerateSpeech: "tts-m"})
	if len(list) != 1 || list[0].Name() != "generate_speech" {
		t.Fatalf("%v", list)
	}
	out, err := list[0].Exec(context.Background(), json.RawMessage(`{"text":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "path: media/speech-") || !strings.Contains(out, "bytes: 9") {
		t.Fatalf("out=%q", out)
	}
	ents, _ := os.ReadDir(filepath.Join(ws, "media"))
	if len(ents) != 1 || !strings.HasSuffix(ents[0].Name(), ".mp3") {
		t.Fatalf("media dir: %v", ents)
	}
}

func TestSpeechGenToolUsesConfiguredDefaultVoice(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Voice string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		got = body.Voice
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("X"))
	}))
	defer srv.Close()

	ws := t.TempDir()
	p := &policy.Policy{Workspace: ws}
	mc := &llm.MediaClient{BaseURL: srv.URL + "/v1", APIKey: "k"}
	list := tools.MediaTools(p, tools.MediaOptions{
		Client: mc, GenerateSpeech: "tts-m", DefaultSpeechVoice: "VoiceX",
	})
	if _, err := list[0].Exec(context.Background(), json.RawMessage(`{"text":"hi"}`)); err != nil {
		t.Fatal(err)
	}
	if got != "VoiceX" {
		t.Fatalf("voice=%q want VoiceX", got)
	}
	// Explicit arg overrides default.
	if _, err := list[0].Exec(context.Background(), json.RawMessage(`{"text":"hi","voice":"Custom"}`)); err != nil {
		t.Fatal(err)
	}
}

func TestVideoGenToolBytesPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Submit returns a completed job with b64 bytes immediately.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"request_id": "vid-1",
			"status":     "completed",
			"b64_json":   base64.StdEncoding.EncodeToString([]byte("MP4BYTES")),
		})
	}))
	defer srv.Close()

	ws := t.TempDir()
	p := &policy.Policy{Workspace: ws}
	mc := &llm.MediaClient{BaseURL: srv.URL + "/v1", APIKey: "k"}
	list := tools.MediaTools(p, tools.MediaOptions{Client: mc, GenerateVideo: "vid-m"})
	out, err := list[0].Exec(context.Background(), json.RawMessage(`{"prompt":"cat playing"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "path: media/video-") || !strings.Contains(out, "bytes: 8") || !strings.Contains(out, "job_id: vid-1") {
		t.Fatalf("out=%q", out)
	}
	ents, _ := os.ReadDir(filepath.Join(ws, "media"))
	if len(ents) != 1 || !strings.HasSuffix(ents[0].Name(), ".mp4") {
		t.Fatalf("media dir: %v", ents)
	}
}

func TestVideoGenToolJobJSONFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No id, no bytes — returns pending job JSON for the agent to inspect.
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "queued"})
	}))
	defer srv.Close()

	ws := t.TempDir()
	p := &policy.Policy{Workspace: ws}
	mc := &llm.MediaClient{BaseURL: srv.URL + "/v1", APIKey: "k"}
	list := tools.MediaTools(p, tools.MediaOptions{Client: mc, GenerateVideo: "vid-m"})
	out, err := list[0].Exec(context.Background(), json.RawMessage(`{"prompt":"cat"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "path: media/video-job-") {
		t.Fatalf("out=%q", out)
	}
	ents, _ := os.ReadDir(filepath.Join(ws, "media"))
	if len(ents) != 1 || !strings.HasSuffix(ents[0].Name(), ".json") {
		t.Fatalf("media dir: %v", ents)
	}
}

func TestUnderstandVideoTool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Mow-Component") != "tool.understand_video" {
			t.Errorf("component=%q", r.Header.Get("X-Mow-Component"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": "scene of a cat"}},
			},
		})
	}))
	defer srv.Close()

	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "v.mp4"), []byte("FAKEMP4"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{Workspace: ws, MaxReadBytes: 1 << 20}
	mc := &llm.MediaClient{BaseURL: srv.URL + "/v1", APIKey: "k"}
	list := tools.MediaTools(p, tools.MediaOptions{Client: mc, UnderstandVideo: "v-m"})
	if list[0].Name() != "understand_video" {
		t.Fatal(list[0].Name())
	}
	out, err := list[0].Exec(context.Background(), json.RawMessage(`{"path":"v.mp4"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "cat") {
		t.Fatalf("out=%q", out)
	}
}

func TestMediaToolsAllConfigured(t *testing.T) {
	list := tools.MediaTools(&policy.Policy{Workspace: t.TempDir()}, tools.MediaOptions{
		Client:          &llm.MediaClient{BaseURL: "http://x", APIKey: "k"},
		GenerateImage:   "a",
		GenerateSpeech:  "b",
		GenerateVideo:   "c",
		UnderstandImage: "d",
		UnderstandVoice: "e",
		UnderstandVideo: "f",
	})
	if len(list) != 6 {
		t.Fatalf("got %d tools", len(list))
	}
}

func TestMediaToolsSkipsUnconfigured(t *testing.T) {
	list := tools.MediaTools(&policy.Policy{Workspace: t.TempDir()}, tools.MediaOptions{
		Client: &llm.MediaClient{BaseURL: "http://x", APIKey: "k"},
	})
	if len(list) != 0 {
		t.Fatalf("want empty, got %d", len(list))
	}
}

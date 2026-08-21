package media

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/subosito/mow/extcfg"
)

// extensions.media is the pack's own section: the model ids moved out of
// llm.generate / llm.understand (legacy keys) so media owns its config like every other
// pack. The provider connection (base_url, api_key, headers) still comes from
// llm.* — these tools share the chat credential.
func TestDecodeMediaSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	yaml := `
llm:
  model: deepseek-chat
  base_url: https://example.invalid/v1
extensions:
  media:
    generate:
      image: gpt-image-1
      speech: tts-1
      speech_voice: alloy
      video: sora-2
    understand:
      image: gpt-5
      voice: whisper-1
      video: gemini-2.5
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	var got Config
	if _, err := extcfg.DecodeSection("media", []string{path}, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	for _, tc := range []struct{ name, got, want string }{
		{"generate.image", got.Generate.Image, "gpt-image-1"},
		{"generate.speech", got.Generate.Speech, "tts-1"},
		{"generate.speech_voice", got.Generate.SpeechVoice, "alloy"},
		{"generate.video", got.Generate.Video, "sora-2"},
		{"understand.image", got.Understand.Image, "gpt-5"},
		{"understand.voice", got.Understand.Voice, "whisper-1"},
		{"understand.video", got.Understand.Video, "gemini-2.5"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

// A missing section is the common case (no media configured) and must be
// silent: no tools, no error, Engine construction unaffected.
func TestDecodeMediaSectionAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(path, []byte("llm:\n  model: deepseek-chat\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var got Config
	if _, err := extcfg.DecodeSection("media", []string{path}, &got); err != nil {
		t.Fatalf("absent section must not error: %v", err)
	}
	if got.Generate.Image != "" || got.Understand.Image != "" {
		t.Fatalf("absent section produced values: %+v", got)
	}
}

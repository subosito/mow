package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMultimodalPartsReachProviderWires(t *testing.T) {
	parts := []ContentPart{{Type: "image", Data: "aW1hZ2U=", MimeType: "image/png"}, {Type: "audio", Data: "YXVkaW8=", MimeType: "audio/wav"}}
	msg := Message{Role: "user", Content: "inspect", Parts: parts}
	open := toOpenAIMessages([]Message{msg})
	raw, _ := json.Marshal(open[0])
	text := string(raw)
	for _, want := range []string{"image_url", "data:image/png;base64,aW1hZ2U=", "input_audio", "YXVkaW8="} {
		if !strings.Contains(text, want) {
			t.Fatalf("openai missing %q: %s", want, text)
		}
	}
	_, anth := toAnthropicMessages([]Message{msg})
	raw, _ = json.Marshal(anth[0])
	text = string(raw)
	for _, want := range []string{`"type":"image"`, `"media_type":"image/png"`, `"type":"document"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("anthropic missing %q: %s", want, text)
		}
	}
	_, responses := toResponsesInput([]Message{msg})
	raw, _ = json.Marshal(responses[0])
	text = string(raw)
	for _, want := range []string{"input_image", "input_audio", "data:image/png;base64,aW1hZ2U="} {
		if !strings.Contains(text, want) {
			t.Fatalf("responses missing %q: %s", want, text)
		}
	}
}

package llm

import (
	"encoding/json"
	"testing"
)

func TestMessagePartsPersistInSessionJSON(t *testing.T) {
	in := Message{Role: "user", Content: "inspect", Parts: []ContentPart{{Type: "image", Data: "aW1n", MimeType: "image/png"}}}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Message
	if json.Unmarshal(raw, &out) != nil || len(out.Parts) != 1 || out.Parts[0].Data != "aW1n" {
		t.Fatalf("out=%+v", out)
	}
}

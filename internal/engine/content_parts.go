package engine

import (
	"encoding/base64"
	"fmt"
	"strings"
)

const maxPromptMediaBytes = 8 << 20

func validateUserParts(parts []ContentPart) error {
	if len(parts) > 4 {
		return fmt.Errorf("mow: at most 4 media parts per prompt")
	}
	total := 0
	for _, p := range parts {
		if p.Type != "image" && p.Type != "audio" && p.Type != "text" {
			return fmt.Errorf("mow: unsupported content part %q", p.Type)
		}
		if p.Type == "text" {
			if len(p.Text) > 100_000 {
				return fmt.Errorf("mow: text content part too large")
			}
			continue
		}
		if !allowedMediaMIME(p.Type, p.MimeType) {
			return fmt.Errorf("mow: unsupported %s MIME type", p.Type)
		}
		data, err := base64.StdEncoding.DecodeString(p.Data)
		if err != nil {
			return fmt.Errorf("mow: invalid %s base64", p.Type)
		}
		total += len(data)
		if total > maxPromptMediaBytes {
			return fmt.Errorf("mow: prompt media exceeds %d bytes", maxPromptMediaBytes)
		}
	}
	return nil
}
func allowedMediaMIME(kind, mime string) bool {
	mime = strings.ToLower(strings.TrimSpace(mime))
	if kind == "image" {
		return mime == "image/png" || mime == "image/jpeg" || mime == "image/gif" || mime == "image/webp"
	}
	return mime == "audio/mpeg" || mime == "audio/wav" || mime == "audio/ogg" || mime == "audio/opus" || mime == "audio/mp4"
}

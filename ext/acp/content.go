package acp

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ContentBlock is an ACP content block (text baseline + common multimodal fields).
// See https://agentclientprotocol.com/protocol/schema — image/audio/resource variants.
type ContentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"` // base64 media
	MimeType string `json:"mimeType,omitempty"`
	URI      string `json:"uri,omitempty"`
	Name     string `json:"name,omitempty"`
	// Resource is embedded context (text or blob).
	Resource *ResourceContents `json:"resource,omitempty"`
}

// ResourceContents is ACP embedded resource payload.
type ResourceContents struct {
	URI      string `json:"uri,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"` // base64
	MimeType string `json:"mimeType,omitempty"`
}

// materializePrompt turns ACP content blocks into a text prompt for Engine.Prompt.
// Images/audio/blobs are written under media/acp/ and referenced by path so tools
// (understand_*) or the model can use them. Non-text blocks are never silently dropped.
func materializePrompt(blocks []ContentBlock, workspace, sessionID string) (string, error) {
	var parts []string
	var nMedia int
	dir := filepath.Join(workspace, "media", "acp")
	stamp := time.Now().Format("20060102-150405")
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		sid = "session"
	}
	sid = sanitizeName(sid)

	for _, c := range blocks {
		typ := strings.ToLower(strings.TrimSpace(c.Type))
		if typ == "" {
			typ = "text"
		}
		switch typ {
		case "text":
			if s := strings.TrimSpace(c.Text); s != "" {
				parts = append(parts, s)
			}
		case "image", "audio":
			data, err := decodeB64(c.Data)
			if err != nil {
				return "", fmt.Errorf("content %s: %w", typ, err)
			}
			if len(data) == 0 {
				continue
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return "", err
			}
			nMedia++
			ext := extFromMIME(c.MimeType, typ)
			name := fmt.Sprintf("%s-%s-%d%s", sid, stamp, nMedia, ext)
			rel := filepath.Join("media", "acp", name)
			abs := filepath.Join(workspace, rel)
			if err := os.WriteFile(abs, data, 0o644); err != nil {
				return "", err
			}
			label := "image"
			if typ == "audio" {
				label = "audio"
			}
			parts = append(parts, fmt.Sprintf(
				"[User attached %s: workspace path %q, mime %q. Use understand_%s or open the file if needed.]",
				label, rel, c.MimeType, map[string]string{"image": "image", "audio": "voice"}[typ],
			))
		case "resource_link", "resourcelink":
			// Link only — no fetch (agent may not have network).
			ref := c.URI
			if ref == "" {
				ref = c.Name
			}
			if ref != "" {
				parts = append(parts, fmt.Sprintf("[Resource link: %s]", ref))
			}
			if s := strings.TrimSpace(c.Text); s != "" {
				parts = append(parts, s)
			}
		case "resource":
			if c.Resource == nil {
				continue
			}
			r := c.Resource
			if s := strings.TrimSpace(r.Text); s != "" {
				uri := r.URI
				if uri == "" {
					uri = "resource"
				}
				parts = append(parts, fmt.Sprintf("[Embedded resource %s]\n%s", uri, s))
			}
			if r.Blob != "" {
				data, err := decodeB64(r.Blob)
				if err != nil {
					return "", fmt.Errorf("resource blob: %w", err)
				}
				if len(data) == 0 {
					continue
				}
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return "", err
				}
				nMedia++
				ext := extFromMIME(r.MimeType, "bin")
				name := fmt.Sprintf("%s-%s-res-%d%s", sid, stamp, nMedia, ext)
				rel := filepath.Join("media", "acp", name)
				if err := os.WriteFile(filepath.Join(workspace, rel), data, 0o644); err != nil {
					return "", err
				}
				parts = append(parts, fmt.Sprintf(
					"[Embedded resource blob saved at workspace path %q (mime %q).]",
					rel, r.MimeType,
				))
			}
		default:
			// Unknown block: keep any text so we do not lose user content.
			if s := strings.TrimSpace(c.Text); s != "" {
				parts = append(parts, s)
			} else if s := strings.TrimSpace(c.URI); s != "" {
				parts = append(parts, fmt.Sprintf("[Content type %q: %s]", typ, s))
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n")), nil
}

func decodeB64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	// data URL prefix
	if i := strings.Index(s, ","); i >= 0 && strings.HasPrefix(s, "data:") {
		s = s[i+1:]
	}
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(s)
	}
	if err != nil {
		return nil, fmt.Errorf("base64: %w", err)
	}
	return data, nil
}

func extFromMIME(mime, kind string) string {
	mime = strings.ToLower(strings.TrimSpace(mime))
	switch {
	case strings.Contains(mime, "png"):
		return ".png"
	case strings.Contains(mime, "jpeg"), strings.Contains(mime, "jpg"):
		return ".jpg"
	case strings.Contains(mime, "gif"):
		return ".gif"
	case strings.Contains(mime, "webp"):
		return ".webp"
	case strings.Contains(mime, "mp3"), strings.Contains(mime, "mpeg"):
		return ".mp3"
	case strings.Contains(mime, "wav"):
		return ".wav"
	case strings.Contains(mime, "ogg"):
		return ".ogg"
	case strings.Contains(mime, "mp4"):
		return ".mp4"
	case strings.Contains(mime, "webm"):
		return ".webm"
	case kind == "image":
		return ".png"
	case kind == "audio":
		return ".wav"
	default:
		return ".bin"
	}
}

func sanitizeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" {
		return "s"
	}
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

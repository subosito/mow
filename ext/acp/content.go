package acp

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/subosito/mow"
	"github.com/subosito/mow/internal/policy"
	toolspkg "github.com/subosito/mow/internal/tools"
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

// materializePrompt turns ACP content blocks into prompt text plus typed media
// parts. Images/audio are sent on the provider wire and also saved under
// <workspace>/media/acp/ so optional tools and session operators have a jailed
// artifact reference. Video is path-only because provider message wires differ.
func materializePrompt(blocks []ContentBlock, workspace, sessionID string) (string, []mow.ContentPart, error) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return "", nil, fmt.Errorf("workspace not set")
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return "", nil, fmt.Errorf("workspace: %w", err)
	}
	workspace = abs

	var parts []string
	var media []mow.ContentPart
	var nMedia int
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
		case "image", "audio", "video":
			data, err := decodeB64(c.Data)
			if err != nil {
				return "", nil, fmt.Errorf("content %s: %w", typ, err)
			}
			if len(data) == 0 {
				continue
			}
			if typ == "image" || typ == "audio" {
				media = append(media, mow.ContentPart{Type: typ, Data: base64.StdEncoding.EncodeToString(data), MimeType: c.MimeType})
			}
			nMedia++
			ext := extFromMIME(c.MimeType, typ)
			name := fmt.Sprintf("%s-%s-%d%s", sid, stamp, nMedia, ext)
			rel := filepath.Join("media", "acp", name)
			pol := &policy.Policy{Workspace: workspace}
			if _, err := toolspkg.WriteFileJailed(pol, rel, data, 0o644); err != nil {
				return "", nil, err
			}
			label, tool := "image", "understand_image"
			switch typ {
			case "audio":
				label, tool = "audio", "understand_voice"
			case "video":
				label, tool = "video", "understand_video"
			}
			parts = append(parts, fmt.Sprintf(
				"[User attached %s: workspace path %q, mime %q. Use %s if an appropriate extension is installed; otherwise explain that this binary cannot inspect it.]",
				label, rel, c.MimeType, tool,
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
					return "", nil, fmt.Errorf("resource blob: %w", err)
				}
				if len(data) == 0 {
					continue
				}
				nMedia++
				ext := extFromMIME(r.MimeType, "bin")
				name := fmt.Sprintf("%s-%s-res-%d%s", sid, stamp, nMedia, ext)
				rel := filepath.Join("media", "acp", name)
				pol := &policy.Policy{Workspace: workspace}
				if _, err := toolspkg.WriteFileJailed(pol, rel, data, 0o644); err != nil {
					return "", nil, err
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
	return strings.TrimSpace(strings.Join(parts, "\n\n")), media, nil
}

const maxPromptRefBytes = 100_000

// A ref starts the text or follows whitespace, so user@example.com and
// Python-style @decorators are not file references.
var promptFileRefRE = regexp.MustCompile(`(?:^|\s)@([A-Za-z0-9._/\-]+)`)

// expandPromptFileRefs resolves jail-safe @path references on session/prompt
// the way an editor's "@file" mention does. Missing, denied, and directory
// refs stay unexpanded.
func expandPromptFileRefs(eng *mow.Engine, text string) string {
	if eng == nil || !strings.Contains(text, "@") {
		return text
	}
	var body strings.Builder
	seen := map[string]bool{}
	for _, match := range promptFileRefRE.FindAllStringSubmatch(text, -1) {
		ref := strings.TrimRight(match[1], ".,;:)")
		if ref == "" || seen[ref] {
			continue
		}
		abs, err := eng.ResolvePath(ref)
		if err != nil {
			continue
		}
		info, err := os.Stat(abs)
		if err != nil || info.IsDir() {
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		if isPromptImage(ref, data) {
			seen[ref] = true
			fmt.Fprintf(&body, "\n\n[Image file reference: workspace path %q. The text read tool cannot decode image bytes. The client should attach it as ACP image content for vision, or use an installed image-understanding extension.]", ref)
			continue
		}
		if isPromptBinary(data) {
			seen[ref] = true
			fmt.Fprintf(&body, "\n\n[Binary file reference: workspace path %q. The text read tool cannot decode this file; use a file-type-specific tool.]", ref)
			continue
		}
		if len(data) > maxPromptRefBytes {
			data = append(append([]byte{}, data[:maxPromptRefBytes]...), []byte("\n… (truncated)")...)
		}
		seen[ref] = true
		fmt.Fprintf(&body, "\n\n--- %s ---\n```%s\n", ref, promptRefLanguage(ref))
		body.Write(data)
		body.WriteString("\n```")
	}
	if body.Len() == 0 {
		return text
	}
	return text + "\n" + body.String()
}

func isPromptImage(path string, data []byte) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	}
	return len(data) >= 8 && bytes.Equal(data[:8], []byte("\x89PNG\r\n\x1a\n")) ||
		len(data) >= 3 && bytes.Equal(data[:3], []byte("\xff\xd8\xff")) ||
		len(data) >= 6 && (bytes.Equal(data[:6], []byte("GIF87a")) || bytes.Equal(data[:6], []byte("GIF89a"))) ||
		len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP"))
}

func isPromptBinary(data []byte) bool {
	prefix := data
	if len(prefix) > 8192 {
		prefix = prefix[:8192]
	}
	return bytes.IndexByte(prefix, 0) >= 0
}

func promptRefLanguage(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".rs":
		return "rust"
	case ".js", ".jsx":
		return "javascript"
	case ".ts", ".tsx":
		return "typescript"
	case ".py":
		return "python"
	case ".sh", ".bash":
		return "bash"
	case ".json":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".md":
		return "markdown"
	case ".toml":
		return "toml"
	default:
		return ""
	}
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
	case kind == "video":
		return ".mp4"
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

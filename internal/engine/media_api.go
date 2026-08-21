// Re-export of internal/llm media types and path-jail helpers for packs/
// (which lives in a separate Go module and cannot import internal/).
//
// packs/media uses these for generate_*/understand_* tools. Do not grow this
// into a dump of internal/llm — only the media side-lane client and the
// workspace jail the tools need to read/write files.
package engine

import (
	"fmt"
	"strings"

	"github.com/subosito/mow/internal/config"
	"github.com/subosito/mow/internal/llm"
	"github.com/subosito/mow/internal/policy"
	"github.com/subosito/mow/internal/tools"
)

// MediaClient is a thin OpenAI-shaped client for generate / understand side
// calls. Same base_url + key as chat; model is per-call.
type MediaClient = llm.MediaClient

// MediaImageResult is a single generated image (b64 and/or URL).
type MediaImageResult = llm.ImageGenResult

// MediaVideoResult is a finished or still-pending video generation outcome.
type MediaVideoResult = llm.VideoResult

// NewMediaClient builds a media side-lane client from the chat credential
// (base_url, api_key, headers). Returns nil when apiKey is empty — media
// tools are opt-in and must not register without a key.
func NewMediaClient(baseURL, apiKey string, extraHeaders map[string]string) *MediaClient {
	if strings.TrimSpace(apiKey) == "" {
		return nil
	}
	return &MediaClient{
		BaseURL:      baseURL,
		APIKey:       apiKey,
		ExtraHeaders: cloneMediaHeaders(extraHeaders),
	}
}

// MediaClientFromConfig loads llm.base_url / resolved api_key / llm.headers
// from the given config paths (BeforeNew / hermetic Engine.ConfigPaths).
// Returns nil when config cannot be loaded or no key is resolvable — never
// an error, because media is an opt-in side lane.
func MediaClientFromConfig(configPaths ...string) *MediaClient {
	cfg, err := config.LoadPaths(configPaths...)
	if err != nil || cfg == nil {
		return nil
	}
	key := strings.TrimSpace(cfg.ResolveAPIKey())
	if key == "" {
		return nil
	}
	return NewMediaClient(cfg.LLM.BaseURL, key, cfg.LLM.Headers)
}

// MediaDataURL builds a data: URL for embedding media bytes in a multimodal request.
func MediaDataURL(mime string, data []byte) string { return llm.DataURL(mime, data) }

// MediaMIMEFromPath guesses media MIME from a file extension.
func MediaMIMEFromPath(p string) string { return llm.MIMEFromPath(p) }

// WriteWorkspaceFile writes data at rel under workspace through the path jail
// (mkdir parents, TOCTOU-safe). Returns the relative path on success so
// generate_* tool results stay workspace-relative. Generated media is the
// documented opt-in write: files land without --allow-write, but still
// inside the jail.
func WriteWorkspaceFile(workspace, rel string, data []byte) (string, error) {
	pol, err := mediaWorkspaceJail(workspace)
	if err != nil {
		return "", err
	}
	if _, err := tools.WriteFileJailed(pol, rel, data, 0o644); err != nil {
		return "", err
	}
	return rel, nil
}

// ReadWorkspaceFile reads rel under workspace through the path jail.
func ReadWorkspaceFile(workspace, rel string) (abs string, data []byte, err error) {
	pol, err := mediaWorkspaceJail(workspace)
	if err != nil {
		return "", nil, err
	}
	return tools.ReadFileJailed(pol, rel)
}

func mediaWorkspaceJail(workspace string) (*policy.Policy, error) {
	ws := strings.TrimSpace(workspace)
	if ws == "" {
		return nil, fmt.Errorf("workspace not set")
	}
	return &policy.Policy{Workspace: ws, MaxReadBytes: 0}, nil
}

func cloneMediaHeaders(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

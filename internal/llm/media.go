package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// MediaClient is a thin OpenAI-shaped client for generate / understand side calls.
// Same base_url + key as chat; model is per-call.
type MediaClient struct {
	BaseURL      string
	APIKey       string
	HTTP         *http.Client
	ExtraHeaders map[string]string
}

// FromClient reuses chat client auth/endpoint for media lanes.
func FromClient(c *Client) *MediaClient {
	if c == nil {
		return nil
	}
	return &MediaClient{
		BaseURL:      c.BaseURL,
		APIKey:       c.APIKey,
		HTTP:         c.HTTP,
		ExtraHeaders: c.ExtraHeaders,
	}
}

// ImageGenResult is a single generated image.
type ImageGenResult struct {
	B64JSON string // may be empty if only URL returned
	URL     string
}

// GenerateImage POSTs /v1/images/generations (openai-images-generations wire).
func (m *MediaClient) GenerateImage(ctx context.Context, model, prompt, size string) (ImageGenResult, error) {
	if err := m.ready(model); err != nil {
		return ImageGenResult{}, err
	}
	if strings.TrimSpace(prompt) == "" {
		return ImageGenResult{}, fmt.Errorf("llm: image prompt required")
	}
	body := map[string]any{
		"model":  model,
		"prompt": prompt,
		"n":      1,
	}
	// Only send size when the caller asked — gateways (e.g. xAI) may reject
	// OpenAI pixel sizes or map them to aspect ratios themselves.
	if s := strings.TrimSpace(size); s != "" {
		body["size"] = s
	}
	// Prefer base64 when the gateway supports it (OpenAI shape).
	body["response_format"] = "b64_json"

	var raw struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
			URL     string `json:"url"`
		} `json:"data"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := m.postJSON(ctx, "/images/generations", "tool.generate_image", body, &raw); err != nil {
		return ImageGenResult{}, err
	}
	if raw.Error != nil && raw.Error.Message != "" {
		return ImageGenResult{}, fmt.Errorf("llm: %s", raw.Error.Message)
	}
	if len(raw.Data) == 0 {
		return ImageGenResult{}, fmt.Errorf("llm: empty image data")
	}
	return ImageGenResult{B64JSON: raw.Data[0].B64JSON, URL: raw.Data[0].URL}, nil
}

// GenerateSpeech POSTs /v1/audio/speech and returns audio bytes.
func (m *MediaClient) GenerateSpeech(ctx context.Context, model, input, voice, format string) ([]byte, error) {
	if err := m.ready(model); err != nil {
		return nil, err
	}
	if strings.TrimSpace(input) == "" {
		return nil, fmt.Errorf("llm: speech input required")
	}
	if voice == "" {
		// OpenAI-ish default; generate_speech tool supplies ElevenLabs voice_id when needed.
		voice = "alloy"
	}
	if format == "" {
		format = "mp3"
	}
	body := map[string]any{
		"model":           model,
		"input":           input,
		"voice":           voice,
		"response_format": format,
	}
	return m.postBytes(ctx, "/audio/speech", "tool.generate_speech", body)
}

// GenerateVideo POSTs /v1/videos/generations. Returns raw JSON (often async job).
func (m *MediaClient) GenerateVideo(ctx context.Context, model, prompt string) (json.RawMessage, error) {
	if err := m.ready(model); err != nil {
		return nil, err
	}
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("llm: video prompt required")
	}
	body := map[string]any{
		"model":  model,
		"prompt": prompt,
	}
	var raw json.RawMessage
	if err := m.postJSON(ctx, "/videos/generations", "tool.generate_video", body, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// VideoResult is a finished or still-pending video generation outcome.
type VideoResult struct {
	// Job is the last job JSON from the gateway (submit or poll).
	Job json.RawMessage
	// ID is request_id / id when present.
	ID string
	// Status from the job payload when known.
	Status string
	// Bytes is the downloaded video when the job completed with a URL or b64.
	Bytes []byte
	// URL is a remote video URL when present (may be empty after download).
	URL string
}

// GenerateVideoWait submits a video job and polls GET /v1/videos/{id} until done,
// failed, or ctx cancelled (OpenAI-shaped request_id + status).
// If the submit response has no id, returns the job JSON without polling.
func (m *MediaClient) GenerateVideoWait(ctx context.Context, model, prompt string) (VideoResult, error) {
	raw, err := m.GenerateVideo(ctx, model, prompt)
	if err != nil {
		return VideoResult{}, err
	}
	id, status := parseVideoJob(raw)
	out := VideoResult{Job: raw, ID: id, Status: status}
	if id == "" || videoJobDone(status) {
		return m.finishVideoResult(ctx, out)
	}
	// Poll until complete (gateway: GET /v1/videos/{id}?model=…).
	// First poll immediately so fast jobs / tests need no artificial delay.
	for {
		job, err := m.getVideo(ctx, model, id)
		if err != nil {
			return out, err
		}
		out.Job = job
		_, status = parseVideoJob(job)
		out.Status = status
		if videoJobFailed(status) {
			return out, fmt.Errorf("llm: video job %s status=%s", id, status)
		}
		if videoJobDone(status) {
			return m.finishVideoResult(ctx, out)
		}
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// finishVideoResult extracts media from Job and downloads remote URLs when needed.
func (m *MediaClient) finishVideoResult(ctx context.Context, out VideoResult) (VideoResult, error) {
	if data, url, ok := extractVideoMedia(out.Job); ok {
		out.Bytes, out.URL = data, url
	}
	if len(out.Bytes) == 0 && out.URL != "" {
		data, err := fetchURL(ctx, out.URL)
		if err != nil {
			return out, err
		}
		out.Bytes = data
	}
	return out, nil
}

func (m *MediaClient) getVideo(ctx context.Context, model, id string) (json.RawMessage, error) {
	if err := m.ready(model); err != nil {
		return nil, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("llm: empty video id")
	}
	u := m.endpoint("/videos/" + path.Base(id))
	if strings.TrimSpace(model) != "" {
		u += "?model=" + urlQueryEscape(model)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	m.setHeaders(req, "tool.generate_video")
	res, err := m.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("llm: HTTP %d: %s", res.StatusCode, truncate(string(body), 400))
	}
	return json.RawMessage(append([]byte(nil), body...)), nil
}

func urlQueryEscape(s string) string {
	// minimal escape for model ids
	return strings.ReplaceAll(strings.ReplaceAll(s, " ", "%20"), "+", "%2B")
}

func parseVideoJob(raw json.RawMessage) (id, status string) {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return "", ""
	}
	for _, k := range []string{"request_id", "id", "video_id"} {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			id = strings.TrimSpace(v)
			break
		}
	}
	if v, ok := m["status"].(string); ok {
		status = strings.ToLower(strings.TrimSpace(v))
	}
	return id, status
}

func videoJobDone(status string) bool {
	switch strings.ToLower(status) {
	case "completed", "complete", "succeeded", "success", "done", "ready":
		return true
	default:
		return false
	}
}

func videoJobFailed(status string) bool {
	switch strings.ToLower(status) {
	case "failed", "error", "cancelled", "canceled":
		return true
	default:
		return false
	}
}

// extractVideoMedia pulls b64 or url from common OpenAI-ish / xAI video job shapes.
func extractVideoMedia(raw json.RawMessage) (data []byte, url string, ok bool) {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil, "", false
	}
	// Top-level url / b64_json
	if u, _ := m["url"].(string); strings.TrimSpace(u) != "" {
		return nil, strings.TrimSpace(u), true
	}
	if b64, _ := m["b64_json"].(string); b64 != "" {
		if data, err := base64.StdEncoding.DecodeString(b64); err == nil {
			return data, "", true
		}
	}
	// xAI: video.url / video.b64_json
	if vid, ok := m["video"].(map[string]any); ok {
		if u, _ := vid["url"].(string); strings.TrimSpace(u) != "" {
			return nil, strings.TrimSpace(u), true
		}
		if b64, _ := vid["b64_json"].(string); b64 != "" {
			if data, err := base64.StdEncoding.DecodeString(b64); err == nil {
				return data, "", true
			}
		}
	}
	// data[0].url / data[0].b64_json
	if arr, ok := m["data"].([]any); ok && len(arr) > 0 {
		if item, ok := arr[0].(map[string]any); ok {
			if u, _ := item["url"].(string); strings.TrimSpace(u) != "" {
				return nil, strings.TrimSpace(u), true
			}
			if b64, _ := item["b64_json"].(string); b64 != "" {
				if data, err := base64.StdEncoding.DecodeString(b64); err == nil {
					return data, "", true
				}
			}
		}
	}
	// result.url
	if res, ok := m["result"].(map[string]any); ok {
		if u, _ := res["url"].(string); strings.TrimSpace(u) != "" {
			return nil, strings.TrimSpace(u), true
		}
	}
	return nil, "", false
}

func fetchURL(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("llm: fetch video URL HTTP %d", res.StatusCode)
	}
	return io.ReadAll(io.LimitReader(res.Body, 64<<20))
}

// DescribeImage asks a vision-capable chat model to describe an image (understand.image).
// imageURL is a data: URL or https URL the model can fetch.
func (m *MediaClient) DescribeImage(ctx context.Context, model, prompt, imageURL string) (string, error) {
	if strings.TrimSpace(prompt) == "" {
		prompt = "Describe this image in detail. Note UI elements, text, layout, and anything relevant to a coding agent."
	}
	return m.chatMultimodal(ctx, model, "tool.understand_image", prompt, "image_url", imageURL)
}

// DescribeVideo asks a video-capable chat model about a video (understand.video).
// videoURL is a data: or https URL. Gateway must route a model that accepts video parts.
func (m *MediaClient) DescribeVideo(ctx context.Context, model, prompt, videoURL string) (string, error) {
	if strings.TrimSpace(prompt) == "" {
		prompt = "Describe this video. Note scenes, on-screen text, UI, and anything relevant to a coding agent."
	}
	// Prefer video_url part (OpenAI-shaped multimodal extensions).
	return m.chatMultimodal(ctx, model, "tool.understand_video", prompt, "video_url", videoURL)
}

// Transcribe POSTs multipart /v1/audio/transcriptions (understand.voice).
func (m *MediaClient) Transcribe(ctx context.Context, model, filename string, data []byte) (string, error) {
	if err := m.ready(model); err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", fmt.Errorf("llm: empty audio")
	}
	if filename == "" {
		filename = "audio.wav"
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", filepath.Base(filename))
	if err != nil {
		return "", err
	}
	if _, err := part.Write(data); err != nil {
		return "", err
	}
	if err := w.WriteField("model", model); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoint("/audio/transcriptions"), &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+m.APIKey)
	req.Header.Set("Content-Type", w.FormDataContentType())
	for k, v := range m.ExtraHeaders {
		req.Header.Set(k, v)
	}
	req.Header.Set(HeaderComponent, "tool.understand_voice")

	res, err := m.httpClient().Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return "", err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("llm: HTTP %d: %s", res.StatusCode, truncate(string(respBody), 400))
	}
	// OpenAI shape: {"text":"..."} or plain text
	var parsed struct {
		Text  string `json:"text"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respBody, &parsed); err == nil {
		if parsed.Error != nil && parsed.Error.Message != "" {
			return "", fmt.Errorf("llm: %s", parsed.Error.Message)
		}
		if strings.TrimSpace(parsed.Text) != "" {
			return parsed.Text, nil
		}
	}
	s := strings.TrimSpace(string(respBody))
	if s == "" {
		return "", fmt.Errorf("llm: empty transcription")
	}
	return s, nil
}

func (m *MediaClient) chatMultimodal(ctx context.Context, model, component, prompt, mediaType, mediaURL string) (string, error) {
	if err := m.ready(model); err != nil {
		return "", err
	}
	if strings.TrimSpace(mediaURL) == "" {
		return "", fmt.Errorf("llm: media url required")
	}
	// Multimodal OpenAI chat content parts (endpoint must accept the modality).
	partKey := "image_url"
	partObj := "image_url"
	if mediaType == "video_url" {
		partKey = "video_url"
		partObj = "video_url"
	}
	body := map[string]any{
		"model": model,
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": prompt},
					map[string]any{
						"type":  partKey,
						partObj: map[string]any{"url": mediaURL},
					},
				},
			},
		},
		"max_tokens": 2048,
	}
	var raw struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := m.postJSON(ctx, "/chat/completions", component, body, &raw); err != nil {
		return "", err
	}
	if raw.Error != nil && raw.Error.Message != "" {
		return "", fmt.Errorf("llm: %s", raw.Error.Message)
	}
	if len(raw.Choices) == 0 || strings.TrimSpace(raw.Choices[0].Message.Content) == "" {
		return "", fmt.Errorf("llm: empty multimodal response")
	}
	return raw.Choices[0].Message.Content, nil
}

// DataURL builds a data: URL for embedding media bytes in a multimodal request.
func DataURL(mime string, data []byte) string {
	if mime == "" {
		mime = "application/octet-stream"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}

// MIMEFromPath guesses media MIME from extension.
func MIMEFromPath(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".m4a":
		return "audio/mp4"
	case ".ogg":
		return "audio/ogg"
	case ".flac":
		return "audio/flac"
	case ".webm":
		return "video/webm"
	case ".mp4":
		return "video/mp4"
	case ".mov":
		return "video/quicktime"
	default:
		return "application/octet-stream"
	}
}

func (m *MediaClient) ready(model string) error {
	if m == nil {
		return fmt.Errorf("llm: nil media client")
	}
	if strings.TrimSpace(m.APIKey) == "" {
		return fmt.Errorf("llm: api key required")
	}
	if strings.TrimSpace(model) == "" {
		return fmt.Errorf("llm: model required")
	}
	return nil
}

func (m *MediaClient) endpoint(suffix string) string {
	base := strings.TrimRight(m.BaseURL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	// Accept both …/v1 and …/v1/chat/completions style base.
	if strings.HasSuffix(base, "/chat/completions") {
		base = strings.TrimSuffix(base, "/chat/completions")
	}
	// Non-/v1 bases (e.g. Anthropic host root): leave as-is; media routes still need
	// an OpenAI-shaped /v1 if the operator wants generate_*/understand_* tools.
	if !strings.HasPrefix(suffix, "/") {
		suffix = "/" + suffix
	}
	return base + suffix
}

func (m *MediaClient) httpClient() *http.Client {
	if m.HTTP != nil {
		return m.HTTP
	}
	return &http.Client{Timeout: 180 * time.Second}
}

func (m *MediaClient) postJSON(ctx context.Context, suffix, component string, body any, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoint(suffix), bytes.NewReader(raw))
	if err != nil {
		return err
	}
	m.setHeaders(req, component)
	res, err := m.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("llm: HTTP %d: %s", res.StatusCode, truncate(string(respBody), 400))
	}
	if out == nil {
		return nil
	}
	// Allow decoding into json.RawMessage
	if rm, ok := out.(*json.RawMessage); ok {
		*rm = append(json.RawMessage(nil), respBody...)
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("llm: decode: %w (body %s)", err, truncate(string(respBody), 200))
	}
	return nil
}

func (m *MediaClient) postBytes(ctx context.Context, suffix, component string, body any) ([]byte, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoint(suffix), bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	m.setHeaders(req, component)
	res, err := m.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("llm: HTTP %d: %s", res.StatusCode, truncate(string(respBody), 400))
	}
	// Some gateways return JSON error even with 200; try detect.
	if len(respBody) > 0 && respBody[0] == '{' {
		var maybe struct {
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(respBody, &maybe) == nil && maybe.Error != nil && maybe.Error.Message != "" {
			return nil, fmt.Errorf("llm: %s", maybe.Error.Message)
		}
	}
	return respBody, nil
}

func (m *MediaClient) setHeaders(req *http.Request, component string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.APIKey)
	for k, v := range m.ExtraHeaders {
		req.Header.Set(k, v)
	}
	// Optional attribution labels (ignored by plain providers).
	// Always override component so chat turn labels on the shared header map
	// cannot leak onto generate/understand tool calls.
	if component != "" {
		req.Header.Set(HeaderComponent, component)
	}
}

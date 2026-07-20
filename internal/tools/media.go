package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/subosito/mow/internal/agent"
	"github.com/subosito/mow/internal/llm"
	"github.com/subosito/mow/internal/policy"
)

// Default media layout under the workspace (path-jailed):
//
//	media/image-*.png   generate_image
//	media/speech-*.mp3  generate_speech
//	media/video-*.json  generate_video job payload
//
// Understand tools only *read* workspace paths and return text to the agent.
// Filesystem is the interaction surface: user or tools drop files → understand_*;
// generate_* write files the user (or later tools) can open.
const mediaDir = "media"

// MediaOptions wires generate / understand model ids to opt-in tools.
// The agent loop stays on the chat model; these are side-lane HTTP calls.
type MediaOptions struct {
	Client *llm.MediaClient

	GenerateImage  string
	GenerateSpeech string
	// DefaultSpeechVoice used when generate_speech omits voice (ElevenLabs voice_id).
	DefaultSpeechVoice string
	GenerateVideo      string

	UnderstandImage string
	UnderstandVoice string
	UnderstandVideo string
}

// DefaultSpeechVoiceID is the fallback TTS voice when none is configured.
// Matches common ElevenLabs / dududu female default (not an OpenAI name).
const DefaultSpeechVoiceID = "LcvlyuBGMjj1h4uAtQjo"

// MediaTools returns generate/understand tools for configured models.
// Caller still filters by tools.enable.
func MediaTools(p *policy.Policy, opt MediaOptions) []agent.Tool {
	if opt.Client == nil {
		return nil
	}
	var out []agent.Tool
	if strings.TrimSpace(opt.GenerateImage) != "" {
		out = append(out, &imageGenTool{p: p, c: opt.Client, model: opt.GenerateImage})
	}
	if strings.TrimSpace(opt.GenerateSpeech) != "" {
		voice := strings.TrimSpace(opt.DefaultSpeechVoice)
		if voice == "" {
			voice = DefaultSpeechVoiceID
		}
		out = append(out, &speechGenTool{p: p, c: opt.Client, model: opt.GenerateSpeech, defaultVoice: voice})
	}
	if strings.TrimSpace(opt.GenerateVideo) != "" {
		out = append(out, &videoGenTool{p: p, c: opt.Client, model: opt.GenerateVideo})
	}
	if strings.TrimSpace(opt.UnderstandImage) != "" {
		out = append(out, &understandImageTool{p: p, c: opt.Client, model: opt.UnderstandImage})
	}
	if strings.TrimSpace(opt.UnderstandVoice) != "" {
		out = append(out, &understandVoiceTool{p: p, c: opt.Client, model: opt.UnderstandVoice})
	}
	if strings.TrimSpace(opt.UnderstandVideo) != "" {
		out = append(out, &understandVideoTool{p: p, c: opt.Client, model: opt.UnderstandVideo})
	}
	return out
}

func mediaStamp() string {
	return time.Now().Format("20060102-150405")
}

// genResult is a stable, parseable tool result for generate_* tools.
func genResult(path string, n int, model string) string {
	return fmt.Sprintf("path: %s\nbytes: %d\nmodel: %s", path, n, model)
}

// Media tool names (prefer generate_* / understand_*; *_gen kept as config aliases).
const (
	ToolGenerateImage   = "generate_image"
	ToolGenerateSpeech  = "generate_speech"
	ToolGenerateVideo   = "generate_video"
	ToolUnderstandImage = "understand_image"
	ToolUnderstandVoice = "understand_voice"
	ToolUnderstandVideo = "understand_video"
)

func writeWorkspaceFile(p *policy.Policy, rel string, data []byte) (string, error) {
	abs, err := p.ResolvePath(rel)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(abs, data, 0o644); err != nil {
		return "", err
	}
	return rel, nil
}

func readWorkspaceFile(p *policy.Policy, rel string, max int) (abs string, data []byte, err error) {
	abs, err = p.ResolvePath(rel)
	if err != nil {
		return "", nil, err
	}
	data, err = os.ReadFile(abs)
	if err != nil {
		return "", nil, err
	}
	if max > 0 && len(data) > max {
		return "", nil, fmt.Errorf("file too large (%d > %d bytes)", len(data), max)
	}
	return abs, data, nil
}

// --- generate_image ---

type imageGenTool struct {
	p     *policy.Policy
	c     *llm.MediaClient
	model string
}

func (t *imageGenTool) Name() string { return ToolGenerateImage }
func (t *imageGenTool) Description() string {
	return "Generate an image (llm.generate.image). Writes a PNG under the workspace. " +
		"Args: prompt (required), path (optional, default media/image-<ts>.png), size (optional)."
}
func (t *imageGenTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"prompt":{"type":"string"},"path":{"type":"string"},"size":{"type":"string"}},"required":["prompt"]}`)
}
func (t *imageGenTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Prompt string `json:"prompt"`
		Path   string `json:"path"`
		Size   string `json:"size"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	res, err := t.c.GenerateImage(ctx, t.model, a.Prompt, a.Size)
	if err != nil {
		return "", err
	}
	outPath := strings.TrimSpace(a.Path)
	if outPath == "" {
		outPath = filepath.Join(mediaDir, "image-"+mediaStamp()+".png")
	}
	var data []byte
	switch {
	case res.B64JSON != "":
		data, err = base64.StdEncoding.DecodeString(res.B64JSON)
		if err != nil {
			return "", fmt.Errorf("generate_image: decode b64: %w", err)
		}
	case res.URL != "":
		data, err = fetchURL(ctx, res.URL)
		if err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("generate_image: no image bytes or url")
	}
	rel, err := writeWorkspaceFile(t.p, outPath, data)
	if err != nil {
		return "", err
	}
	return genResult(rel, len(data), t.model), nil
}

// --- generate_speech ---

type speechGenTool struct {
	p            *policy.Policy
	c            *llm.MediaClient
	model        string
	defaultVoice string
}

func (t *speechGenTool) Name() string { return ToolGenerateSpeech }
func (t *speechGenTool) Description() string {
	return "Synthesize speech (llm.generate.speech). Writes audio under the workspace. " +
		"Args: text (required), path (default media/speech-<ts>.mp3), voice (optional voice_id; " +
		"default from llm.generate.speech_voice or built-in), format."
}
func (t *speechGenTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"},"path":{"type":"string"},"voice":{"type":"string"},"format":{"type":"string"}},"required":["text"]}`)
}
func (t *speechGenTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Text   string `json:"text"`
		Path   string `json:"path"`
		Voice  string `json:"voice"`
		Format string `json:"format"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	format := a.Format
	if format == "" {
		format = "mp3"
	}
	voice := strings.TrimSpace(a.Voice)
	if voice == "" {
		voice = strings.TrimSpace(t.defaultVoice)
	}
	if voice == "" {
		voice = DefaultSpeechVoiceID
	}
	data, err := t.c.GenerateSpeech(ctx, t.model, a.Text, voice, format)
	if err != nil {
		return "", err
	}
	outPath := strings.TrimSpace(a.Path)
	if outPath == "" {
		outPath = filepath.Join(mediaDir, "speech-"+mediaStamp()+"."+format)
	}
	rel, err := writeWorkspaceFile(t.p, outPath, data)
	if err != nil {
		return "", err
	}
	return genResult(rel, len(data), t.model), nil
}

// --- generate_video ---

type videoGenTool struct {
	p     *policy.Policy
	c     *llm.MediaClient
	model string
}

func (t *videoGenTool) Name() string { return ToolGenerateVideo }
func (t *videoGenTool) Description() string {
	return "Generate a video (llm.generate.video). Submits a job and polls until complete when the gateway returns a job id. " +
		"Writes MP4/bytes under media/ when available; otherwise writes the last job JSON. " +
		"Args: prompt (required), path (optional)."
}
func (t *videoGenTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"prompt":{"type":"string"},"path":{"type":"string"}},"required":["prompt"]}`)
}
func (t *videoGenTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Prompt string `json:"prompt"`
		Path   string `json:"path"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	// Bound wait so a hung job cannot pin the agent forever.
	wctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	res, err := t.c.GenerateVideoWait(wctx, t.model, a.Prompt)
	if err != nil {
		return "", err
	}
	outPath := strings.TrimSpace(a.Path)
	if len(res.Bytes) > 0 {
		if outPath == "" {
			outPath = filepath.Join(mediaDir, "video-"+mediaStamp()+".mp4")
		}
		rel, err := writeWorkspaceFile(t.p, outPath, res.Bytes)
		if err != nil {
			return "", err
		}
		msg := genResult(rel, len(res.Bytes), t.model)
		if res.ID != "" {
			msg += "\njob_id: " + res.ID
		}
		if res.Status != "" {
			msg += "\nstatus: " + res.Status
		}
		return msg, nil
	}
	// No bytes yet — keep job JSON for the agent to inspect.
	if outPath == "" {
		outPath = filepath.Join(mediaDir, "video-job-"+mediaStamp()+".json")
	}
	rel, err := writeWorkspaceFile(t.p, outPath, res.Job)
	if err != nil {
		return "", err
	}
	return genResult(rel, len(res.Job), t.model) + "\n" + string(res.Job), nil
}

// --- understand_image ---

type understandImageTool struct {
	p     *policy.Policy
	c     *llm.MediaClient
	model string
}

func (t *understandImageTool) Name() string { return ToolUnderstandImage }
func (t *understandImageTool) Description() string {
	return "Understand a workspace image via llm.understand.image (vision side call; chat model need not be multimodal). " +
		"Args: path (required), prompt (optional focus). Returns text for the agent."
}
func (t *understandImageTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"prompt":{"type":"string"}},"required":["path"]}`)
}
func (t *understandImageTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Path   string `json:"path"`
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	abs, data, err := readWorkspaceFile(t.p, a.Path, 8<<20)
	if err != nil {
		return "", fmt.Errorf("understand_image: %w", err)
	}
	url := llm.DataURL(llm.MIMEFromPath(abs), data)
	return t.c.DescribeImage(ctx, t.model, a.Prompt, url)
}

// --- understand_voice ---

type understandVoiceTool struct {
	p     *policy.Policy
	c     *llm.MediaClient
	model string
}

func (t *understandVoiceTool) Name() string { return ToolUnderstandVoice }
func (t *understandVoiceTool) Description() string {
	return "Transcribe a workspace audio file via llm.understand.voice (OpenAI transcriptions shape). " +
		"Args: path (required). Returns transcript text."
}
func (t *understandVoiceTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)
}
func (t *understandVoiceTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	abs, data, err := readWorkspaceFile(t.p, a.Path, 25<<20)
	if err != nil {
		return "", fmt.Errorf("understand_voice: %w", err)
	}
	return t.c.Transcribe(ctx, t.model, abs, data)
}

// --- understand_video ---

type understandVideoTool struct {
	p     *policy.Policy
	c     *llm.MediaClient
	model string
}

func (t *understandVideoTool) Name() string { return ToolUnderstandVideo }
func (t *understandVideoTool) Description() string {
	return "Understand a workspace video via llm.understand.video (multimodal side call). " +
		"Args: path (required), prompt (optional). Returns text. Large files may be rejected."
}
func (t *understandVideoTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"prompt":{"type":"string"}},"required":["path"]}`)
}
func (t *understandVideoTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Path   string `json:"path"`
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	// Keep payload modest; long videos belong on a provider that accepts URLs/jobs.
	abs, data, err := readWorkspaceFile(t.p, a.Path, 16<<20)
	if err != nil {
		return "", fmt.Errorf("understand_video: %w", err)
	}
	url := llm.DataURL(llm.MIMEFromPath(abs), data)
	return t.c.DescribeVideo(ctx, t.model, a.Prompt, url)
}

func fetchURL(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("generate_image: fetch url HTTP %d", res.StatusCode)
	}
	const max = 32 << 20
	data, err := io.ReadAll(io.LimitReader(res.Body, max+1))
	if err != nil {
		return nil, err
	}
	if len(data) > max {
		return nil, fmt.Errorf("generate_image: downloaded image too large")
	}
	return data, nil
}

package media

import (
	"strings"

	"github.com/subosito/mow/ext"
	"github.com/subosito/mow/extcfg"
	"github.com/subosito/mow/internal/config"
	"github.com/subosito/mow/internal/llm"
)

// init links the media pack: the six generate_*/understand_* tools register
// from config at BeforeNew time, the same way packs/cmdhook loads its plugins.
//
// Registration is per-tool and conditional. A tool appears only when its model
// id is set (llm.generate.image, llm.understand.voice, …) AND an API key is
// resolvable, so a media-capable client can actually be built. That preserves
// the pre-move behavior: listing generate_image in tools.enable without a
// configured model is a no-op, never an Engine construction error.
func init() {
	ext.RegisterBeforeNew(func(configPaths ...string) error {
		return setup(configPaths...)
	})
}

// Config is extensions.media: model ids for the media side lanes.
//
// The provider connection (base_url, api_key, headers) still comes from llm.*
// — these tools share the chat credential and endpoint. Only the model ids are
// pack-owned, which is why they live here rather than under llm.
type Config struct {
	Generate struct {
		Image  string `yaml:"image"`
		Speech string `yaml:"speech"`
		// SpeechVoice is the default TTS voice when a call omits it.
		// For ElevenLabs this must be a voice_id, not a display name.
		SpeechVoice string `yaml:"speech_voice"`
		Video       string `yaml:"video"`
	} `yaml:"generate"`
	Understand struct {
		Image string `yaml:"image"`
		Voice string `yaml:"voice"`
		Video string `yaml:"video"`
	} `yaml:"understand"`
}

// setup rebuilds media tool registrations from the current config paths.
// Errors reading config are not fatal: media is an opt-in side lane, and a
// host with no media config must still start.
func setup(configPaths ...string) error {
	cfg, err := config.LoadPaths(configPaths...)
	if err != nil || cfg == nil {
		// Config problems surface through the normal Engine load path; media
		// simply stays unregistered rather than aborting construction.
		return nil
	}
	key := strings.TrimSpace(cfg.ResolveAPIKey())
	if key == "" {
		return nil
	}
	var mc Config
	// A malformed section must not abort Engine construction — same rule as
	// the other packs. Unset keys simply leave that tool unregistered.
	if _, derr := extcfg.DecodeSection("media", configPaths, &mc); derr != nil {
		mc = Config{}
	}
	client := &llm.MediaClient{
		BaseURL: cfg.LLM.BaseURL,
		APIKey:  key,
		// Headers are per-engine (session/actor); the engine stamps them.
		ExtraHeaders: cloneHeaders(cfg.LLM.Headers),
	}
	for _, t := range MediaTools(nil, MediaOptions{
		Client:             client,
		GenerateImage:      mc.Generate.Image,
		GenerateSpeech:     mc.Generate.Speech,
		DefaultSpeechVoice: mc.Generate.SpeechVoice,
		GenerateVideo:      mc.Generate.Video,
		UnderstandImage:    mc.Understand.Image,
		UnderstandVoice:    mc.Understand.Voice,
		UnderstandVideo:    mc.Understand.Video,
	}) {
		ext.RegisterTool(t)
	}
	return nil
}

func cloneHeaders(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

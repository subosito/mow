# media

Media generation and understanding: `generate_image`, `generate_speech`,
`generate_video`, `understand_image`, `understand_voice`, `understand_video`
— each registered only when its model id and an API key are configured.

## Link

```go
import _ "github.com/subosito/mow/packs/media"
```

`cmd/mowx` blank-imports this package; lean `cmd/mow` does not. It talks
to the chat credential and workspace jail through the public `mow.Media*` /
`mow.WriteWorkspaceFile` APIs.

## Commands and tools

| Surface | Name |
|---|---|
| Tools | `generate_image`, `generate_speech`, `generate_video` |
| Tools | `understand_image`, `understand_voice`, `understand_video` |

No CLI, no slash commands. Registration is per-tool and conditional: a tool
appears only when its model id is set (`extensions.media.generate.image`,
`extensions.media.understand.voice`, …) and an API key is resolvable. No media config
means no tools, never an Engine construction error. The Rust `mowi` sibling
project paints these as ordinary tool rows over `mow acp`.

## Config (`extensions.media`)

Model ids are pack config. The provider connection is not: base_url, api_key
and headers still come from `llm.*`, because these tools share the chat
credential and endpoint.

```yaml
extensions:
  media:
    generate:
      image: gpt-image-1
      speech: tts-1
      speech_voice: alloy   # ElevenLabs needs a voice_id, not a display name
      video: sora-2
    understand:
      image: gpt-5
      voice: whisper-1
      video: gemini-2.5
```

Sharing the host key is also why an untrusted project config cannot set this:
`extensions.media` is stripped from project overlays.

## Docs

- [docs/extensions.md](../../docs/extensions.md)
- [docs/harness.md](../../docs/harness.md)

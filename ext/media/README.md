# media

Media generation and understanding: `generate_image`, `generate_speech`,
`generate_video`, `understand_image`, `understand_voice`, `understand_video`
— each registered only when its model id and an API key are configured.

## Link

```go
import _ "github.com/subosito/mow/ext/media"
```

Stock `cmd/mow` blank-imports this package. It lives in `ext/` because the
tools talk to `internal/llm` media clients directly.

## Commands and tools

| Surface | Name |
|---|---|
| Tools | `generate_image`, `generate_speech`, `generate_video` |
| Tools | `understand_image`, `understand_voice`, `understand_video` |

No CLI, no slash commands. Registration is per-tool and conditional: a tool
appears only when its model id is set (`llm.generate.image`,
`llm.understand.voice`, …) and an API key is resolvable. No media config
means no tools, never an Engine construction error. The Rust `mowi` sibling
project exposes these tools through the RPC surface.

## Config

No `extensions.media` section. Model ids live under `llm.generate.*` /
`llm.understand.*` in the usual config files; the API key is the host's
configured key.

## Docs

- [docs/extensions.md](../../docs/extensions.md)
- [docs/harness.md](../../docs/harness.md)

# Media inputs and generated artifacts

Mow's stock binary is a standalone text/code harness. The built-in `read` tool reads
UTF-8 text only. Images, audio, video, PDFs and archives are binary and require a
compatible client/model or an optional extension/tool specific to the file type.

## Inputs

Mow now preserves image/audio prompt blocks as typed content through session history
and maps them onto supported provider wire parts; it also writes a jailed copy under
`media/acp/` for optional tools/operators. A path mention such as `@photo.png` is
not itself an ACP attachment, but mowi resolves recognized jailed `@path` media and
sends the corresponding content block. Text references stay bounded text expansion.
Video remains path-only because ACP/provider video message contracts differ.

The stock mow binary does not link `understand_image`, `understand_voice`, or
`understand_video`. Those are optional application tools and were moved out of the
root module. A host can provide equivalent tools through an extension or MCP. Mow's
error messages say “if installed” rather than claiming those tools exist.

## Generated outputs

Likewise, `generate_image`, `generate_speech`, and `generate_video` are not stock
mow tools. Optional host extensions may register them; generation writes artifacts
under the workspace and should return a typed path/metadata result. The client is
responsible for rendering or opening artifacts. Mowi currently presents ordinary
tool output/cards; it does not have first-class generated image/audio/video preview
or playback.

Generation and understanding are separate capabilities:

| Operation | Input/output | Required capability |
|---|---|---|
| Understand image | image attachment or jailed image path → text | vision-capable model/tool |
| Understand speech/voice | audio attachment/path → transcript/analysis | audio/STT tool |
| Understand video | video path/block → analysis | video-capable tool; host extension |
| Generate image | prompt → image artifact | image-generation tool + write grant |
| Generate speech | text/voice → audio artifact | speech-generation tool + write grant |
| Generate video | prompt/assets → job/artifact | video-generation tool + write grant |

Read-only understanding tools must explicitly declare themselves read-only.
Generation writes files and requires write permission. Remote URLs, file sizes,
MIME/signature validation and jail boundaries remain tool responsibilities. Never
make the text reader silently upload arbitrary binaries to a model/provider.

## Failure presentation

Tool execution errors travel through Engine events and ACP `tool_call_update.error`.
Hosts should show that error rather than a generic “tool failed.” Read/grep result
bodies remain omitted from ACP cards to avoid flooding the UI, but their explicit
error reason is retained.

## Remaining work

- Add graphical file selection and clipboard image/audio ingestion in mowi; `/attach` and jailed media `@path` are the bounded explicit/automatic baselines.
- Decide whether mowi will support a host-specific video block or require a video
  tool over a workspace path.
- Add generated-artifact metadata/content blocks and first-class preview/playback.
- Supply or document the optional media extension/module that owns generation and
  understanding implementations; do not pull application workflows back into mow.
- Add end-to-end tests against a configured host for each enabled modality, with
  bounded fixtures, provider/model capability discovery and negative cases.

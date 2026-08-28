# Changelog

## 1.0.0-rc.1

ACP v1 is the host protocol (`mow acp` plus optional extras). `mow rpc` is gone.

- Lean `mow` (acp, cli, tty, focus, proc, cmdhook, mcp) vs full `mowx` (+ goal, review, ops, media).
- `extensions.acp.peers`: each row is `command` (external ACP) or `model` (native `mow acp`).
- Session **mode** (`ask`|`code`) confirms power tools; it does not strip them. `--read-only` disables write/shell and wins over yaml.
- Session **approvals** (`prompt`|`always`) skip `session/request_permission`; still gated by allow-write/shell.
- `tool_call_update` omits read/glob/grep bodies (already in history); write/edit diffs stay on the wire with `duration_ms`.
- Shared `~/.agents` discovery; plugin/skill install lives in sibling **asp**.
- `initialize` does not advertise stub `auth.logout`.
EOF

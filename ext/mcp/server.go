package mcp

// This file is the MCP *server* side of the pack: `mow mcp` exposes mow itself
// as an MCP server over stdio with a single `mow_prompt` tool, so other agents
// and editors (Claude Desktop, etc.) can call mow as a delegated sub-agent. It
// mirrors the client side (rest of this package), which connects out to other
// MCP servers and registers their tools. One package, both directions — like
// ext/acp (serve `mow acp` + `acp_delegate` client tool).

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/subosito/mow"
	"github.com/subosito/mow/cliutil"
	"github.com/subosito/mow/ext"
)

// mcpProtocolVersion matches the stdio version the client side negotiates.
const mcpProtocolVersion = "2024-11-05"

func init() {
	ext.RegisterCommand(ext.Command{
		Name:    "mcp",
		Summary: "MCP server on stdin/stdout (mow_prompt tool)",
		Run:     serveCmd,
	})
}

func serveCmd(args []string) int {
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "help" {
			printMCPUsage()
			return 0
		}
	}
	fs := cliutil.NewFlagSet("mcp")
	var ef cliutil.EngineFlags
	ef.Bind(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	eng, err := ef.NewEngine()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow mcp: %v\n", err)
		return 1
	}
	defer eng.Close()
	return serve(context.Background(), eng, os.Stdin, os.Stdout)
}

func printMCPUsage() {
	fmt.Fprintf(os.Stderr, `mow mcp — serve mow as an MCP server over stdio

  Point Claude Desktop / other MCP hosts at this process. Exposes one tool:

    mow_prompt   run the agent on a prompt; returns the final answer
                 args: prompt (required), read_only (optional bool)

  mow mcp [engine flags]

Engine flags: same as mow run (--config --model --workspace --allow-write …).
Power tools for the delegated agent follow --allow-write / --allow-shell.

Client side (outbound MCP servers): extensions.mcp in config — not this command.
See docs/extensions.md.

`)
}

// serve runs the newline-delimited JSON-RPC 2.0 loop until stdin closes.
func serve(ctx context.Context, eng *mow.Engine, in io.Reader, out io.Writer) int {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	r := bufio.NewReader(in)
	enc := json.NewEncoder(out) // Encode appends '\n' → one message per line
	for {
		line, err := readServeLine(ctx, r)
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) > 0 {
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if perr := json.Unmarshal(trimmed, &req); perr != nil {
				// JSON-RPC 2.0: unparseable input gets -32700 with a null id.
				// Dropping it silently left the peer waiting for a reply that
				// was never coming.
				_ = enc.Encode(replyErr(json.RawMessage("null"), -32700, "parse error"))
			} else if len(req.ID) > 0 {
				// Notifications (no id) get no response.
				if resp := serveHandle(ctx, eng, req.ID, req.Method, req.Params); resp != nil {
					_ = enc.Encode(resp)
				}
			}
		}
		if err != nil {
			cancel()
			return 0 // EOF or read error: stdin closed, exit cleanly
		}
	}
}

func readServeLine(ctx context.Context, r *bufio.Reader) ([]byte, error) {
	type lineResult struct {
		line []byte
		err  error
	}
	ch := make(chan lineResult, 1)
	go func() {
		line, err := r.ReadBytes('\n')
		ch <- lineResult{line: line, err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-ch:
		if len(res.line) > maxStdioLineBytes {
			return nil, fmt.Errorf("request line exceeds %d bytes", maxStdioLineBytes)
		}
		return res.line, res.err
	}
}

func serveHandle(ctx context.Context, eng *mow.Engine, id json.RawMessage, method string, params json.RawMessage) any {
	switch method {
	case "initialize":
		return reply(id, map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "mow", "version": mow.VersionString()},
		})
	case "ping":
		return reply(id, map[string]any{})
	case "tools/list":
		return reply(id, map[string]any{"tools": []any{promptTool()}})
	case "tools/call":
		return serveCallTool(ctx, eng, id, params)
	default:
		return replyErr(id, -32601, "method not found: "+method)
	}
}

func promptTool() map[string]any {
	return map[string]any{
		"name":        "mow_prompt",
		"description": "Run the mow coding agent on a prompt in its workspace and return the final answer. Use for delegated coding or codebase-analysis tasks; it may read files and (if the server allows) edit/run.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"prompt":    map[string]any{"type": "string", "description": "The task or question for the agent."},
				"read_only": map[string]any{"type": "boolean", "description": "Restrict to read-only tools (no write/shell) for this call."},
			},
			"required": []string{"prompt"},
		},
	}
}

func serveCallTool(ctx context.Context, eng *mow.Engine, id json.RawMessage, params json.RawMessage) any {
	var p struct {
		Name      string `json:"name"`
		Arguments struct {
			Prompt   string `json:"prompt"`
			ReadOnly bool   `json:"read_only"`
		} `json:"arguments"`
	}
	_ = json.Unmarshal(params, &p)
	if p.Name != "mow_prompt" {
		return replyErr(id, -32602, "unknown tool: "+p.Name)
	}
	if strings.TrimSpace(p.Arguments.Prompt) == "" {
		return toolResult(id, "error: prompt required", true)
	}
	res, err := eng.PromptWith(ctx, p.Arguments.Prompt, mow.PromptOpts{ReadOnly: p.Arguments.ReadOnly})
	if err != nil {
		return toolResult(id, "error: "+err.Error(), true)
	}
	return toolResult(id, res.Text, false)
}

// toolResult wraps text in the MCP tools/call content shape.
func toolResult(id json.RawMessage, text string, isErr bool) any {
	return reply(id, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
		"isError": isErr,
	})
}

func reply(id json.RawMessage, result any) any {
	return map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result}
}

func replyErr(id json.RawMessage, code int, msg string) any {
	return map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "error": map[string]any{"code": code, "message": msg}}
}

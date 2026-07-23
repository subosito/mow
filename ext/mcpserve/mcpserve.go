// Package mcpserve exposes mow as an MCP server over stdio: other agents and
// editors (Claude Desktop, etc.) can call mow as a sub-agent via a single
// `mow_prompt` tool. It is the mirror of ext/mcp (which makes mow an MCP
// client). Link it into a binary with a blank import; it registers the
// `mow mcp-serve` subcommand.
package mcpserve

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

// mcpProtocolVersion matches the stdio version ext/mcp negotiates as a client.
const mcpProtocolVersion = "2024-11-05"

func init() {
	ext.RegisterCommand(ext.Command{
		Name:    "mcp-serve",
		Summary: "Serve mow as an MCP server over stdio (exposes a mow_prompt tool)",
		Run:     runCmd,
	})
}

func runCmd(args []string) int {
	fs := cliutil.NewFlagSet("mcp-serve")
	var ef cliutil.EngineFlags
	ef.Bind(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	eng, err := ef.NewEngine()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow mcp-serve: %v\n", err)
		return 1
	}
	return serve(eng, os.Stdin, os.Stdout)
}

// serve runs the newline-delimited JSON-RPC 2.0 loop until stdin closes.
func serve(eng *mow.Engine, in io.Reader, out io.Writer) int {
	r := bufio.NewReader(in)
	enc := json.NewEncoder(out) // Encode appends '\n' → one message per line
	for {
		line, err := r.ReadBytes('\n')
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) > 0 {
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if json.Unmarshal(trimmed, &req) == nil {
				// Notifications (no id) get no response.
				if len(req.ID) > 0 {
					if resp := handle(eng, req.ID, req.Method, req.Params); resp != nil {
						_ = enc.Encode(resp)
					}
				}
			}
		}
		if err != nil {
			return 0 // EOF or read error: stdin closed, exit cleanly
		}
	}
}

func handle(eng *mow.Engine, id json.RawMessage, method string, params json.RawMessage) any {
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
		return callTool(eng, id, params)
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

func callTool(eng *mow.Engine, id json.RawMessage, params json.RawMessage) any {
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
	res, err := eng.PromptWith(context.Background(), p.Arguments.Prompt, mow.PromptOpts{ReadOnly: p.Arguments.ReadOnly})
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

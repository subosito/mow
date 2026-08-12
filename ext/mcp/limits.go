package mcp

// Wire and tool output bounds — keep MCP I/O aligned with harness scale.
const (
	maxStdioLineBytes  = 4 << 20  // single JSON-RPC line on stdio
	maxHTTPBodyBytes   = 8 << 20  // JSON or SSE aggregate response body
	maxToolOutputBytes = 2 << 20  // tools/call text returned to the agent
	maxStderrRetain    = 16 << 10 // per-server stderr ring (matches ACP peer cap)
)

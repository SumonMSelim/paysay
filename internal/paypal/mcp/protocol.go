// Package mcp implements a minimal Model Context Protocol (MCP) client that
// speaks JSON-RPC 2.0 over a subprocess's stdio. It is purpose-built to drive
// the official PayPal MCP server (https://github.com/paypal/paypal-mcp-server),
// which is launched as `npx -y @paypal/mcp --tools=all`.
//
// We deliberately implement a small, auditable client rather than depend on a
// larger third-party MCP SDK: this is a money-moving path, so minimizing and
// understanding the dependency surface is a security feature.
package mcp

import "encoding/json"

// jsonRPCVersion is the only JSON-RPC version MCP uses.
const jsonRPCVersion = "2.0"

// protocolVersion is the MCP protocol revision this client implements. The
// server echoes/negotiates this during initialize.
const protocolVersion = "2024-11-05"

// request is an outgoing JSON-RPC 2.0 request. ID is omitted for notifications.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// response is an incoming JSON-RPC 2.0 response.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError is a JSON-RPC 2.0 error object.
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// --- MCP initialize ---

type initializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ClientInfo      clientInfo     `json:"clientInfo"`
}

type clientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InitializeResult is the server's response to initialize. We keep only the
// fields we use; unknown fields are ignored by the JSON decoder.
type InitializeResult struct {
	ProtocolVersion string     `json:"protocolVersion"`
	ServerInfo      ServerInfo `json:"serverInfo"`
}

// ServerInfo identifies the connected MCP server.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// --- tools/list ---

type listToolsResult struct {
	Tools []Tool `json:"tools"`
}

// Tool describes a tool exposed by the MCP server. InputSchema is a JSON Schema
// object describing the tool's parameters; we pass it through to Bedrock as the
// tool's input schema.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// --- tools/call ---

type callToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// CallToolResult is the result of invoking a tool. Content is a list of typed
// content blocks (usually text). IsError signals a tool-level (not transport)
// failure, which the agent should surface to the model rather than abort on.
type CallToolResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError"`
}

// ContentBlock is one piece of tool output. Only "text" is handled specially;
// other types are preserved as raw text where possible.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Text concatenates all text content blocks into a single string, which is the
// form Bedrock's toolResult expects.
func (r CallToolResult) Text() string {
	var b []byte
	for i, c := range r.Content {
		if i > 0 {
			b = append(b, '\n')
		}
		b = append(b, c.Text...)
	}
	return string(b)
}

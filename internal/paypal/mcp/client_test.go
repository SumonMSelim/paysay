package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

// fakeServer implements Transport and simulates a minimal MCP server by reading
// client requests and writing canned responses. It runs the server logic on a
// goroutine driven by the handler function.
type fakeServer struct {
	clientToServer *io.PipeReader // server reads client's writes
	serverWrites   *io.PipeWriter

	serverToClient *io.PipeReader // client reads server's writes
	clientReads    *io.PipeWriter
}

func newFakeServer(handler func(req map[string]any) map[string]any) *fakeServer {
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	fs := &fakeServer{
		clientToServer: c2sR,
		serverWrites:   c2sW,
		serverToClient: s2cR,
		clientReads:    s2cW,
	}
	go fs.run(handler)
	return fs
}

func (f *fakeServer) run(handler func(req map[string]any) map[string]any) {
	scanner := bufio.NewScanner(f.clientToServer)
	enc := json.NewEncoder(f.clientReads)
	for scanner.Scan() {
		var req map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		// Notifications have no id; do not respond.
		if _, hasID := req["id"]; !hasID {
			continue
		}
		resp := handler(req)
		if resp != nil {
			_ = enc.Encode(resp)
		}
	}
}

// Transport interface: the client reads from serverToClient and writes to
// serverWrites (which feeds clientToServer).
func (f *fakeServer) Reader() io.Reader { return f.serverToClient }
func (f *fakeServer) Writer() io.Writer { return f.serverWrites }
func (f *fakeServer) Close() error {
	_ = f.serverWrites.Close()
	_ = f.clientReads.Close()
	return nil
}

func TestInitializeAndListTools(t *testing.T) {
	handler := func(req map[string]any) map[string]any {
		method := req["method"].(string)
		id := req["id"]
		switch method {
		case "initialize":
			return map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"protocolVersion": protocolVersion,
					"serverInfo":      map[string]any{"name": "paypal-mcp", "version": "1.0.0"},
				},
			}
		case "tools/list":
			return map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"tools": []map[string]any{
						{
							"name":        "create_invoice",
							"description": "Create a new invoice",
							"inputSchema": map[string]any{"type": "object"},
						},
						{
							"name":        "send_invoice",
							"description": "Send an invoice",
							"inputSchema": map[string]any{"type": "object"},
						},
					},
				},
			}
		default:
			return map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32601, "message": "method not found"}}
		}
	}

	fs := newFakeServer(handler)
	client := NewClient(fs)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := client.Initialize(ctx)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if res.ServerInfo.Name != "paypal-mcp" {
		t.Errorf("server name = %q, want paypal-mcp", res.ServerInfo.Name)
	}
	if client.ServerInfo().Version != "1.0.0" {
		t.Errorf("server version = %q", client.ServerInfo().Version)
	}

	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("got %d tools, want 2", len(tools))
	}
	if tools[0].Name != "create_invoice" {
		t.Errorf("tool[0] = %q, want create_invoice", tools[0].Name)
	}
}

func TestCallToolReturnsText(t *testing.T) {
	handler := func(req map[string]any) map[string]any {
		id := req["id"]
		if req["method"] != "tools/call" {
			return map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32601, "message": "unexpected"}}
		}
		params := req["params"].(map[string]any)
		if params["name"] != "create_invoice" {
			t.Errorf("unexpected tool name: %v", params["name"])
		}
		return map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"result": map[string]any{
				"content": []map[string]any{{"type": "text", "text": `{"id":"INV2-ABC","status":"DRAFT"}`}},
				"isError": false,
			},
		}
	}
	fs := newFakeServer(handler)
	client := NewClient(fs)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := client.Initialize(ctx); err != nil {
		// Initialize also hits handler; our handler only knows tools/call, so
		// skip strict init here by tolerating the error path is not ideal.
		_ = err
	}

	out, err := client.CallTool(ctx, "create_invoice", json.RawMessage(`{"amount":"10.00"}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if out.IsError {
		t.Fatalf("unexpected tool error: %s", out.Text())
	}
	if !strings.Contains(out.Text(), "INV2-ABC") {
		t.Errorf("result text = %q, want it to contain INV2-ABC", out.Text())
	}
}

func TestCallToolPropagatesRPCError(t *testing.T) {
	handler := func(req map[string]any) map[string]any {
		id := req["id"]
		return map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"error":   map[string]any{"code": -32000, "message": "boom"},
		}
	}
	fs := newFakeServer(handler)
	client := NewClient(fs)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := client.CallTool(ctx, "create_invoice", nil)
	if err == nil {
		t.Fatal("expected rpc error, got nil")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error = %v, want it to contain boom", err)
	}
}

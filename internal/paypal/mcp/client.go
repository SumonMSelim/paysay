package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// Transport abstracts the bidirectional byte stream to an MCP server. The
// production transport is a child process's stdio; tests supply an in-memory
// transport. This is the seam that makes the client fully unit-testable without
// spawning Node.
type Transport interface {
	// Reader returns the stream of newline-delimited JSON-RPC messages from the
	// server (its stdout).
	Reader() io.Reader
	// Writer returns the stream to which JSON-RPC messages are written (the
	// server's stdin).
	Writer() io.Writer
	// Close terminates the transport and underlying process.
	Close() error
}

// Client is a JSON-RPC 2.0 client for an MCP server over a Transport. It is safe
// for concurrent use: requests are multiplexed by ID and responses are routed
// back to the waiting caller.
type Client struct {
	transport Transport
	enc       *json.Encoder
	writeMu   sync.Mutex

	nextID atomic.Int64

	pendingMu sync.Mutex
	pending   map[int64]chan response

	// readDone is closed when the read loop exits (the server process died or
	// its output closed); readErr holds why. Closing a channel wakes every
	// waiting call, not just one.
	readDone chan struct{}
	readErr  error
	closed   chan struct{}
	closeMu  sync.Once

	serverInfo ServerInfo
}

// ErrTransportClosed reports that the MCP server stopped responding, typically
// because its process exited. The client cannot recover; callers should
// launch a new server.
var ErrTransportClosed = errors.New("mcp transport closed")

// NewClient creates a client over the given transport and starts the background
// read loop. Call Initialize before issuing other calls.
func NewClient(t Transport) *Client {
	c := &Client{
		transport: t,
		enc:       json.NewEncoder(t.Writer()),
		pending:   make(map[int64]chan response),
		readDone:  make(chan struct{}),
		closed:    make(chan struct{}),
	}
	go c.readLoop()
	return c
}

// ServerInfo returns the identity reported by the server during Initialize.
func (c *Client) ServerInfo() ServerInfo { return c.serverInfo }

// Initialize performs the MCP initialize handshake and sends the required
// "notifications/initialized" notification. It must be called once before
// ListTools or CallTool.
func (c *Client) Initialize(ctx context.Context) (*InitializeResult, error) {
	params := initializeParams{
		ProtocolVersion: protocolVersion,
		Capabilities:    map[string]any{},
		ClientInfo:      clientInfo{Name: "paysay", Version: "0.1.0"},
	}
	raw, err := c.call(ctx, "initialize", params)
	if err != nil {
		return nil, fmt.Errorf("mcp initialize: %w", err)
	}
	var res InitializeResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("mcp initialize decode: %w", err)
	}
	c.serverInfo = res.ServerInfo

	// Per the MCP spec the client must notify the server that initialization is
	// complete before using tools.
	if err := c.notify("notifications/initialized", struct{}{}); err != nil {
		return nil, fmt.Errorf("mcp initialized notification: %w", err)
	}
	return &res, nil
}

// ListTools returns the tools advertised by the server.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	raw, err := c.call(ctx, "tools/list", struct{}{})
	if err != nil {
		return nil, fmt.Errorf("mcp tools/list: %w", err)
	}
	var res listToolsResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("mcp tools/list decode: %w", err)
	}
	return res.Tools, nil
}

// CallTool invokes a named tool with JSON arguments and returns its result. A
// tool-level error (IsError true) is returned in the result, not as a Go error;
// only transport/protocol failures produce a Go error.
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (*CallToolResult, error) {
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	params := callToolParams{Name: name, Arguments: args}
	raw, err := c.call(ctx, "tools/call", params)
	if err != nil {
		return nil, fmt.Errorf("mcp tools/call %q: %w", name, err)
	}
	var res CallToolResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("mcp tools/call %q decode: %w", name, err)
	}
	return &res, nil
}

// Close shuts down the client and transport.
func (c *Client) Close() error {
	c.closeMu.Do(func() { close(c.closed) })
	return c.transport.Close()
}

// --- internals ---

func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	rawParams, err := marshalParams(params)
	if err != nil {
		return nil, err
	}

	ch := make(chan response, 1)
	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()
	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}()

	req := request{JSONRPC: jsonRPCVersion, ID: &id, Method: method, Params: rawParams}
	if err := c.write(req); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.readDone:
		return nil, fmt.Errorf("%w: %v", ErrTransportClosed, c.readErr)
	case <-c.closed:
		return nil, errors.New("mcp client closed")
	case resp := <-ch:
		if resp.Error != nil {
			return nil, fmt.Errorf("mcp rpc error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

func (c *Client) notify(method string, params any) error {
	rawParams, err := marshalParams(params)
	if err != nil {
		return err
	}
	return c.write(request{JSONRPC: jsonRPCVersion, Method: method, Params: rawParams})
}

func (c *Client) write(req request) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.enc.Encode(req)
}

func (c *Client) readLoop() {
	scanner := bufio.NewScanner(c.transport.Reader())
	// MCP messages can be large (tool schemas, invoice lists). Allow up to 4 MiB.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var resp response
		if err := json.Unmarshal(line, &resp); err != nil {
			// Non-JSON line (e.g. server diagnostics on stdout). Ignore.
			continue
		}
		if resp.ID == nil {
			// A notification or request from the server; this client does not
			// handle server-initiated calls, so ignore.
			continue
		}
		c.pendingMu.Lock()
		ch, ok := c.pending[*resp.ID]
		c.pendingMu.Unlock()
		if ok {
			ch <- resp
		}
	}
	err := scanner.Err()
	if err == nil {
		err = io.EOF
	}
	c.readErr = err
	close(c.readDone)
}

func marshalParams(params any) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	b, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal params: %w", err)
	}
	return b, nil
}

// --- process transport ---

// processTransport runs an MCP server as a child process and exposes its stdio.
type processTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
}

// Launch starts the MCP server subprocess with the given command, args, and
// environment, returning a connected Client. The env slice must be in the
// "KEY=VALUE" form expected by os/exec.
func Launch(ctx context.Context, command string, args []string, env []string) (*Client, Transport, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("mcp stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("mcp stdout pipe: %w", err)
	}
	// Server stderr is left attached to the parent for diagnostics; it must not
	// be parsed as protocol data.
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("mcp start %q: %w", command, err)
	}

	t := &processTransport{cmd: cmd, stdin: stdin, stdout: stdout}
	return NewClient(t), t, nil
}

func (p *processTransport) Reader() io.Reader { return p.stdout }
func (p *processTransport) Writer() io.Writer { return p.stdin }

func (p *processTransport) Close() error {
	// Close stdin to signal EOF, then give the process a moment to exit before
	// killing it.
	_ = p.stdin.Close()
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case <-time.After(3 * time.Second):
		_ = p.cmd.Process.Kill()
		<-done
		return nil
	case <-done:
		return nil
	}
}

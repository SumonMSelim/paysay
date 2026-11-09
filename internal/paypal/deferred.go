package paypal

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
)

// ErrConnecting reports that the live PayPal provider has not finished
// connecting yet (token mint, MCP launch and handshake run in the background so
// the HTTP server can start immediately). Callers map this to a 503
// paypal_connecting response.
var ErrConnecting = errors.New("paypal: provider is still connecting")

// DeferredProvider is a ToolProvider whose real backend is attached after
// construction. It lets the agent and the HTTP server hold a stable
// ToolProvider at startup while the live MCP-backed provider connects in the
// background. Until a provider is set, every call returns ErrConnecting; once
// set, calls delegate to it. It is safe for concurrent use.
type DeferredProvider struct {
	mu    sync.RWMutex
	inner ToolProvider
	ready atomic.Bool
}

// NewDeferredProvider returns a provider that is not yet connected.
func NewDeferredProvider() *DeferredProvider {
	return &DeferredProvider{}
}

// Set attaches the real provider and marks the DeferredProvider ready. It is
// idempotent; a later Set replaces the inner provider (used when a background
// reconnect produces a fresh provider).
func (d *DeferredProvider) Set(p ToolProvider) {
	d.mu.Lock()
	d.inner = p
	d.mu.Unlock()
	d.ready.Store(true)
}

// Ready reports whether a backing provider has been attached.
func (d *DeferredProvider) Ready() bool { return d.ready.Load() }

// Tools delegates to the backing provider, or returns ErrConnecting when none
// is attached yet.
func (d *DeferredProvider) Tools(ctx context.Context) ([]ToolDescriptor, error) {
	d.mu.RLock()
	p := d.inner
	d.mu.RUnlock()
	if p == nil {
		return nil, ErrConnecting
	}
	return p.Tools(ctx)
}

// Invoke delegates to the backing provider, or returns ErrConnecting when none
// is attached yet.
func (d *DeferredProvider) Invoke(ctx context.Context, name string, args json.RawMessage) (ToolResult, error) {
	d.mu.RLock()
	p := d.inner
	d.mu.RUnlock()
	if p == nil {
		return ToolResult{}, ErrConnecting
	}
	return p.Invoke(ctx, name, args)
}

// Close closes the backing provider if one is attached.
func (d *DeferredProvider) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.inner == nil {
		return nil
	}
	return d.inner.Close()
}

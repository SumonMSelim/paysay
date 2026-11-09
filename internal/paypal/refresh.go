package paypal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/SumonMSelim/paysay/internal/paypal/mcp"
)

// refreshMargin is how long before a token's expiry the provider replaces it,
// so an in-flight tool call never runs on a token that expires mid-request.
const refreshMargin = 10 * time.Minute

var errClosed = errors.New("paypal: provider is closed")

// RefreshingProvider is a ToolProvider that keeps a client-credentials access
// token fresh. PayPal tokens expire after roughly nine hours and the MCP server
// reads its token once at launch, so when the token nears expiry the provider
// mints a new one and relaunches the MCP server with it. This keeps a
// long-running deployment working without a manual token rotation.
type RefreshingProvider struct {
	mint   func(ctx context.Context) (*TokenResponse, error)
	launch func(accessToken string) (ToolProvider, error)
	now    func() time.Time

	// mu guards current and expiresAt. Tool calls hold the read lock for their
	// duration, so a refresh (write lock) never closes a provider in use.
	mu        sync.RWMutex
	current   ToolProvider
	expiresAt time.Time
	closed    bool
}

// NewRefreshingProvider mints the first token and launches the first provider.
// launch must start the MCP server on a long-lived context (not a request
// context), because the subprocess is killed when that context ends.
func NewRefreshingProvider(
	ctx context.Context,
	mint func(ctx context.Context) (*TokenResponse, error),
	launch func(accessToken string) (ToolProvider, error),
) (*RefreshingProvider, error) {
	r := &RefreshingProvider{mint: mint, launch: launch, now: time.Now}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.refreshLocked(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

// Tools lists tools from the current provider, refreshing first if needed.
func (r *RefreshingProvider) Tools(ctx context.Context) ([]ToolDescriptor, error) {
	p, err := r.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer r.mu.RUnlock()
	return p.Tools(ctx)
}

// Invoke calls a tool on the current provider, refreshing first if needed.
func (r *RefreshingProvider) Invoke(ctx context.Context, name string, args json.RawMessage) (ToolResult, error) {
	p, err := r.acquire(ctx)
	if err != nil {
		return ToolResult{}, err
	}
	res, err := r.invokeAndRelease(ctx, p, name, args)
	if errors.Is(err, mcp.ErrTransportClosed) {
		r.markStale(p)
	}
	return res, err
}

// invokeAndRelease runs one tool call and releases the read lock with a defer,
// so a panic in the underlying provider cannot leave the lock held forever
// (which would deadlock every later call). acquire took the read lock; this
// function owns releasing it.
func (r *RefreshingProvider) invokeAndRelease(ctx context.Context, p ToolProvider, name string, args json.RawMessage) (ToolResult, error) {
	defer r.mu.RUnlock()
	return p.Invoke(ctx, name, args)
}

// markStale forces a relaunch on the next call when the MCP server process
// has died, so live mode recovers without restarting PaySay.
func (r *RefreshingProvider) markStale(dead ToolProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current == dead {
		r.expiresAt = time.Time{}
	}
}

// Close shuts down the current provider.
func (r *RefreshingProvider) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	if r.current == nil {
		return nil
	}
	err := r.current.Close()
	r.current = nil
	return err
}

// acquire returns a provider with a fresh token while holding the read lock.
// The caller must release it with r.mu.RUnlock.
func (r *RefreshingProvider) acquire(ctx context.Context) (ToolProvider, error) {
	r.mu.RLock()
	if r.freshLocked() {
		return r.current, nil
	}
	r.mu.RUnlock()

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, errClosed
	}
	if !r.freshLocked() {
		if err := r.refreshLocked(ctx); err != nil {
			r.mu.Unlock()
			return nil, err
		}
	}
	r.mu.Unlock()

	r.mu.RLock()
	if r.current == nil {
		r.mu.RUnlock()
		return nil, errClosed
	}
	return r.current, nil
}

func (r *RefreshingProvider) freshLocked() bool {
	return r.current != nil && r.now().Before(r.expiresAt)
}

// refreshLocked mints a token, launches a provider with it, and swaps it in,
// closing the old one. The caller must hold the write lock.
func (r *RefreshingProvider) refreshLocked(ctx context.Context) error {
	tok, err := r.mint(ctx)
	if err != nil {
		return fmt.Errorf("paypal: refresh token: %w", err)
	}
	next, err := r.launch(tok.AccessToken)
	if err != nil {
		return fmt.Errorf("paypal: relaunch MCP server: %w", err)
	}
	if r.current != nil {
		_ = r.current.Close()
	}
	r.current = next

	lifetime := time.Duration(tok.ExpiresIn) * time.Second
	if lifetime > 2*refreshMargin {
		lifetime -= refreshMargin
	} else {
		lifetime /= 2
	}
	r.expiresAt = r.now().Add(lifetime)
	return nil
}

package paypal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/SumonMSelim/paysay/internal/paypal/mcp"
)

// tokenProvider is a fake ToolProvider that reports which token it was
// launched with and whether it has been closed.
type tokenProvider struct {
	token  string
	mu     sync.Mutex
	closed bool
}

func (p *tokenProvider) Tools(context.Context) ([]ToolDescriptor, error) { return nil, nil }

func (p *tokenProvider) Invoke(context.Context, string, json.RawMessage) (ToolResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ToolResult{}, errors.New("invoked a closed provider")
	}
	return ToolResult{Text: p.token}, nil
}

func (p *tokenProvider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	return nil
}

type refreshHarness struct {
	mu       sync.Mutex
	clock    time.Time
	mints    int
	mintErr  error
	launched []*tokenProvider
}

func (h *refreshHarness) now() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.clock
}

func (h *refreshHarness) advance(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clock = h.clock.Add(d)
}

func (h *refreshHarness) mint(context.Context) (*TokenResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.mintErr != nil {
		return nil, h.mintErr
	}
	h.mints++
	return &TokenResponse{AccessToken: "tok-" + string(rune('0'+h.mints)), ExpiresIn: 32400}, nil
}

func (h *refreshHarness) launch(token string) (ToolProvider, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := &tokenProvider{token: token}
	h.launched = append(h.launched, p)
	return p, nil
}

func newHarnessProvider(t *testing.T) (*refreshHarness, *RefreshingProvider) {
	t.Helper()
	h := &refreshHarness{clock: time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)}
	r := &RefreshingProvider{mint: h.mint, launch: h.launch, now: h.now}
	r.mu.Lock()
	if err := r.refreshLocked(context.Background()); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}
	r.mu.Unlock()
	return h, r
}

func TestRefreshingProviderReusesTokenUntilNearExpiry(t *testing.T) {
	h, r := newHarnessProvider(t)

	h.advance(8 * time.Hour)
	res, err := r.Invoke(context.Background(), "list_invoices", nil)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.Text != "tok-1" {
		t.Errorf("got token %q, want tok-1 (no refresh before margin)", res.Text)
	}
	if h.mints != 1 {
		t.Errorf("mints = %d, want 1", h.mints)
	}
}

func TestRefreshingProviderRelaunchesWithNewToken(t *testing.T) {
	h, r := newHarnessProvider(t)

	// 9h lifetime minus the 10m margin: 8h55m in, the token must be replaced.
	h.advance(8*time.Hour + 55*time.Minute)
	res, err := r.Invoke(context.Background(), "list_invoices", nil)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.Text != "tok-2" {
		t.Errorf("got token %q, want tok-2", res.Text)
	}
	if len(h.launched) != 2 {
		t.Fatalf("launched %d providers, want 2", len(h.launched))
	}
	if !h.launched[0].closed {
		t.Error("old provider was not closed after refresh")
	}
}

func TestRefreshingProviderSurfacesMintError(t *testing.T) {
	h, r := newHarnessProvider(t)

	h.advance(9 * time.Hour)
	h.mintErr = errors.New("invalid_client")
	if _, err := r.Invoke(context.Background(), "list_invoices", nil); err == nil {
		t.Fatal("expected error when token refresh fails")
	}

	// A later call retries the refresh once PayPal recovers.
	h.mintErr = nil
	if _, err := r.Invoke(context.Background(), "list_invoices", nil); err != nil {
		t.Fatalf("invoke after recovery: %v", err)
	}
}

func TestRefreshingProviderConcurrentCallsNeverHitClosedProvider(t *testing.T) {
	h, r := newHarnessProvider(t)

	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%8 == 0 {
				h.advance(3 * time.Hour)
			}
			if _, err := r.Invoke(context.Background(), "list_invoices", nil); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent invoke: %v", err)
	}
}

func TestRefreshingProviderCloseStopsCalls(t *testing.T) {
	h, r := newHarnessProvider(t)

	if err := r.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !h.launched[0].closed {
		t.Error("provider not closed")
	}
	if _, err := r.Invoke(context.Background(), "list_invoices", nil); err == nil {
		t.Error("invoke after close should fail, not relaunch the MCP server")
	}
	if len(h.launched) != 1 {
		t.Errorf("launched %d providers after close, want 1", len(h.launched))
	}
}

// dyingProvider reports that its MCP server has exited.
type dyingProvider struct{ tokenProvider }

func (p *dyingProvider) Invoke(context.Context, string, json.RawMessage) (ToolResult, error) {
	return ToolResult{}, fmt.Errorf("mcp tools/call: %w: EOF", mcp.ErrTransportClosed)
}

func TestRefreshingProviderRelaunchesAfterServerDies(t *testing.T) {
	h, r := newHarnessProvider(t)
	r.mu.Lock()
	r.current = &dyingProvider{}
	r.mu.Unlock()

	if _, err := r.Invoke(context.Background(), "list_invoices", nil); !errors.Is(err, mcp.ErrTransportClosed) {
		t.Fatalf("err = %v, want ErrTransportClosed", err)
	}
	res, err := r.Invoke(context.Background(), "list_invoices", nil)
	if err != nil {
		t.Fatalf("invoke after relaunch: %v", err)
	}
	if res.Text != "tok-2" || len(h.launched) != 2 {
		t.Errorf("got %q with %d launches, want a relaunched server on tok-2", res.Text, len(h.launched))
	}
}

// panickingProvider panics on Invoke, simulating a bug in the underlying MCP
// provider.
type panickingProvider struct{ tokenProvider }

func (p *panickingProvider) Invoke(context.Context, string, json.RawMessage) (ToolResult, error) {
	panic("boom")
}

func TestRefreshingProviderInvokePanicReleasesLock(t *testing.T) {
	h, r := newHarnessProvider(t)
	r.mu.Lock()
	r.current = &panickingProvider{}
	r.mu.Unlock()

	func() {
		defer func() {
			if rec := recover(); rec == nil {
				t.Fatal("expected Invoke to propagate the panic")
			}
		}()
		_, _ = r.Invoke(context.Background(), "list_invoices", nil)
	}()

	// If the read lock leaked, this call would block forever; run it on a
	// goroutine with a deadline so a regression fails the test instead of
	// hanging it.
	done := make(chan struct{})
	go func() {
		h.advance(9 * time.Hour) // force a refresh path that takes the write lock
		_, _ = r.Invoke(context.Background(), "list_invoices", nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Invoke deadlocked: the read lock was not released after a panic")
	}
}

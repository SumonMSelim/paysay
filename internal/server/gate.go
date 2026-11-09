package server

import "sync"

// providerGate tracks whether the PayPal tool provider has finished connecting
// and holds the InvoiceLister once it has. In live mode PayPal is connected in
// the background (token mint, MCP launch, handshake) so the HTTP server can
// start serving immediately; until the gate is ready, PayPal-backed routes
// return 503 paypal_connecting. In mock mode the gate is marked ready at
// construction. It is safe for concurrent use.
type providerGate struct {
	mu       sync.RWMutex
	ready    bool
	invoices InvoiceLister
}

// newProviderGate creates a gate. When ready is true the provider state is
// final at construction: either a working provider (mock mode) or a deliberate
// nil provider (no PayPal configured), both of which never transition. When
// ready is false the gate is awaiting a background connect and PayPal-backed
// routes return 503 paypal_connecting until set is called.
func newProviderGate(invoices InvoiceLister, ready bool) *providerGate {
	return &providerGate{ready: ready, invoices: invoices}
}

// set records a connected provider and marks the gate ready. It is idempotent.
func (g *providerGate) set(invoices InvoiceLister) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.invoices = invoices
	g.ready = true
}

// lister returns the current InvoiceLister and whether the gate is ready. When
// not ready, the lister is nil and callers must return paypal_connecting.
func (g *providerGate) lister() (InvoiceLister, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.invoices, g.ready
}

// isReady reports whether PayPal is connected and usable.
func (g *providerGate) isReady() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.ready
}

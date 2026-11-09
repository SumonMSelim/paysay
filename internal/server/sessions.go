package server

import (
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/SumonMSelim/paysay/internal/agent"
)

// sessionCookie names the cookie that groups one visitor's invoices. On the
// public demo every visitor shares one PayPal sandbox account, so this lets
// the dashboard default to "your invoices" instead of everyone's.
const sessionCookie = "paysay_session"

// invoiceIDPattern matches PayPal invoice IDs (e.g. INV2-ABCD-1234) in tool
// output, which works for both the mock and the live MCP response shapes.
var invoiceIDPattern = regexp.MustCompile(`INV2-[A-Z0-9-]+`)

// maxTrackedSessions caps how many sessions sessionInvoices remembers. A public
// demo accumulates a new session per visitor, so without a cap the map grows
// without bound; past the cap the oldest-inserted session is evicted (it only
// loses the ?scope=mine filter, which already degrades gracefully after a
// restart).
const maxTrackedSessions = 10000

// sessionInvoices remembers which invoices each session created. It is
// in-memory: after a restart, earlier invoices show only under "all".
type sessionInvoices struct {
	mu    sync.Mutex
	byID  map[string]map[string]bool
	order []string // session IDs in insertion order, for oldest-first eviction
}

func newSessionInvoices() *sessionInvoices {
	return &sessionInvoices{byID: make(map[string]map[string]bool)}
}

// recordCreated stores the IDs of invoices that successful create_invoice
// calls returned.
func (s *sessionInvoices) recordCreated(session string, calls []agent.ToolCall) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range calls {
		if c.Name != "create_invoice" || c.IsError {
			continue
		}
		id := invoiceIDPattern.FindString(c.Output)
		if id == "" {
			continue
		}
		if s.byID[session] == nil {
			s.byID[session] = make(map[string]bool)
			s.order = append(s.order, session)
			s.evictOldestLocked()
		}
		s.byID[session][id] = true
	}
}

// evictOldestLocked drops oldest-inserted sessions until the map is within the
// cap. Caller holds the lock.
func (s *sessionInvoices) evictOldestLocked() {
	for len(s.byID) > maxTrackedSessions && len(s.order) > 0 {
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.byID, oldest)
	}
}

// filter keeps only the rows this session created.
func (s *sessionInvoices) filter(session string, rows []InvoiceRow) []InvoiceRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	mine := s.byID[session]
	out := make([]InvoiceRow, 0, len(mine))
	for _, r := range rows {
		if mine[r.ID] {
			out = append(out, r)
		}
	}
	return out
}

// pendingTTL bounds how long an action waits for the merchant's approval, so a
// stale confirmation card cannot run an action long after it was proposed.
const pendingTTL = 10 * time.Minute

// pendingAction is a set of money-moving tool calls the agent proposed in one
// turn and the merchant has not approved yet. They are approved together.
type pendingAction struct {
	calls   []agent.ToolCall
	expires time.Time
}

// pendingActions holds proposed actions per session. Approval looks the
// action up by session and a random single-use ID, so a visitor can only
// approve actions proposed in their own session, and only once.
type pendingActions struct {
	mu        sync.Mutex
	bySession map[string]map[string]pendingAction
}

func newPendingActions() *pendingActions {
	return &pendingActions{bySession: make(map[string]map[string]pendingAction)}
}

// add stores proposed calls and returns their approval ID.
func (p *pendingActions) add(session string, calls []agent.ToolCall, now time.Time) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	id := uuid.NewString()
	actions := p.bySession[session]
	if actions == nil {
		actions = make(map[string]pendingAction)
		p.bySession[session] = actions
	}
	for k, a := range actions {
		if now.After(a.expires) {
			delete(actions, k)
		}
	}
	actions[id] = pendingAction{calls: calls, expires: now.Add(pendingTTL)}
	return id
}

// take removes and returns unexpired calls.
func (p *pendingActions) take(session, id string, now time.Time) ([]agent.ToolCall, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	a, ok := p.bySession[session][id]
	if !ok {
		return nil, false
	}
	delete(p.bySession[session], id)
	if now.After(a.expires) {
		return nil, false
	}
	return a.calls, true
}

// sweepExpired drops expired actions across every session and removes sessions
// left with no actions, so the store does not accumulate stale entries for
// sessions that never call add again. It is driven by the periodic sweeper.
func (p *pendingActions) sweepExpired(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for session, actions := range p.bySession {
		for id, a := range actions {
			if now.After(a.expires) {
				delete(actions, id)
			}
		}
		if len(actions) == 0 {
			delete(p.bySession, session)
		}
	}
}

// sessionID returns the visitor's session ID, issuing a new cookie if absent.
func sessionID(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(sessionCookie); err == nil && uuid.Validate(c.Value) == nil {
		return c.Value
	}
	id := uuid.NewString()
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		MaxAge:   30 * 24 * 60 * 60,
	})
	return id
}

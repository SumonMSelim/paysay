package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/SumonMSelim/paysay/internal/agent"
)

// The daily collections check is a two-sided feature. A scheduled job
// (Render Workflow) decides which sent-but-unpaid invoices still need chasing,
// cross-checks PayPal Transaction Search so already-paid customers are left
// alone, and POSTs the result here. The merchant then sees one Overview card
// and approves the reminders through the existing confirmation gate: the job
// never sends a reminder itself.
//
// GET /api/collections is read-only: it returns the latest plan and reports
// which invoices have already been reminded since the plan was generated, so a
// reload or a second tab cannot cause a second reminder. Minting a pending
// action is done only by POST /api/collections/approve, which also takes a
// per-invoice claim so two overlapping batches cannot be created before either
// is confirmed; handleConfirm records every reminder that actually ran so the
// next approve excludes it.

// Bounds on a submitted plan. A real merchant has far fewer open invoices than
// these, so they only reject malformed or abusive payloads.
const (
	maxCollectionsInvoices = 500
	maxCollectionsField    = 256
)

// Validation errors returned to the job as 400 responses.
var (
	errTooManyInvoices  = errors.New("too many invoices in plan")
	errInvalidInvoiceID = errors.New("invoice id is malformed")
	errFieldTooLong     = errors.New("invoice field is too long")
)

// collectionsChaseInvoice is one invoice the job recommends chasing.
type collectionsChaseInvoice struct {
	ID       string `json:"id"`
	Customer string `json:"customer"`
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
	Reason   string `json:"reason"`
}

// collectionsSkippedInvoice is one invoice the job skipped because Transaction
// Search found a matching completed payment.
type collectionsSkippedInvoice struct {
	ID       string `json:"id"`
	Customer string `json:"customer"`
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
	Reason   string `json:"reason"`
}

// collectionsPlan is the body the job POSTs and the body the UI reads back.
type collectionsPlan struct {
	GeneratedAt time.Time                   `json:"generated_at"`
	Chase       []collectionsChaseInvoice   `json:"chase"`
	Skipped     []collectionsSkippedInvoice `json:"skipped"`
}

// collectionsStore holds the latest plan in memory behind a mutex, plus the set
// of invoice IDs that have actually been reminded since that plan was
// generated, plus per-invoice claims taken at approve time. Like the session
// and pending-action stores, it is intentionally not persisted: after a restart
// the Overview card is empty until the next job run. A new plan resets both the
// reminded set and the claims.
//
// A claim prevents the same invoice from being approved twice before the first
// batch is confirmed: a second approve (double click, or a second tab) that
// hits a live claim is turned away, and the same session's re-approve returns
// its existing batch rather than minting a new one. Claims expire on the same
// TTL as pendingActions, so a declined card frees its invoices after the TTL.
type collectionsStore struct {
	mu       sync.Mutex
	plan     *collectionsPlan
	reminded map[string]bool
	claims   map[string]collectionsClaim
}

// collectionsClaim records that one invoice is held by a pending approval
// batch: which session took it, which pending ID it belongs to, and when the
// claim expires.
type collectionsClaim struct {
	session   string
	pendingID string
	expires   time.Time
}

func newCollectionsStore() *collectionsStore {
	return &collectionsStore{
		reminded: make(map[string]bool),
		claims:   make(map[string]collectionsClaim),
	}
}

// set stores a new plan and resets the reminded set and claims, so a fresh job
// run starts the approve/remind cycle over.
func (s *collectionsStore) set(p *collectionsPlan) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plan = p
	s.reminded = make(map[string]bool)
	s.claims = make(map[string]collectionsClaim)
}

// snapshot returns the latest plan and a copy of the reminded-invoice set, or
// nil when no plan has been submitted. The stored plan is never mutated after
// it is set, so sharing the pointer is safe.
func (s *collectionsStore) snapshot() (*collectionsPlan, map[string]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.plan == nil {
		return nil, nil
	}
	reminded := make(map[string]bool, len(s.reminded))
	for id := range s.reminded {
		reminded[id] = true
	}
	return s.plan, reminded
}

// markReminded records that an invoice has been reminded against the current
// plan, and releases any claim on it. It is a no-op when there is no plan.
func (s *collectionsStore) markReminded(invoiceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.plan == nil {
		return
	}
	s.reminded[invoiceID] = true
	delete(s.claims, invoiceID)
}

// approve returns a pending batch of reminders for the caller's session.
//
// If the session already holds a live claim, the SAME pending ID and the same
// invoices are returned, so a double click or a second tab cannot create a
// second runnable batch. Otherwise a batch is minted over every chase invoice
// that is neither already reminded nor claimed by another live session; mint is
// called to produce the pending ID (it records the calls in pendingActions),
// and a claim is taken on each invoice. ok is false when no plan exists or no
// invoice is eligible.
//
// mint is a callback rather than a direct pendingActions call so the whole
// operation stays under the store's lock, keeping the claim and the pending
// batch consistent under concurrent approves.
func (s *collectionsStore) approve(session string, mint func(invoiceIDs []string) string, now time.Time) (string, []string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.plan == nil {
		return "", nil, false
	}
	s.pruneExpiredLocked(now)

	// An existing live claim for this session: return its batch unchanged.
	if id, ids := s.sessionBatchLocked(session); id != "" {
		return id, ids, true
	}

	// Otherwise select eligible invoices: not reminded, not claimed by anyone.
	eligible := make([]string, 0, len(s.plan.Chase))
	for _, inv := range s.plan.Chase {
		if s.reminded[inv.ID] {
			continue
		}
		if _, claimed := s.claims[inv.ID]; claimed {
			continue
		}
		eligible = append(eligible, inv.ID)
	}
	if len(eligible) == 0 {
		return "", nil, false
	}

	pendingID := mint(eligible)
	expires := now.Add(pendingTTL)
	for _, id := range eligible {
		s.claims[id] = collectionsClaim{session: session, pendingID: pendingID, expires: expires}
	}
	return pendingID, eligible, true
}

// pruneExpiredLocked drops claims whose TTL has passed. Caller holds the lock.
func (s *collectionsStore) pruneExpiredLocked(now time.Time) {
	for id, c := range s.claims {
		if now.After(c.expires) {
			delete(s.claims, id)
		}
	}
}

// sessionBatchLocked returns the pending ID and invoice IDs of the session's
// current live claim batch, or an empty ID when it has none. Caller holds the
// lock and has already pruned expired claims. A session only ever has one live
// batch, so every one of its claims shares a pending ID.
func (s *collectionsStore) sessionBatchLocked(session string) (string, []string) {
	pendingID := ""
	// Preserve the plan's chase order for a stable batch.
	ids := make([]string, 0)
	for _, inv := range s.plan.Chase {
		c, ok := s.claims[inv.ID]
		if !ok || c.session != session {
			continue
		}
		pendingID = c.pendingID
		ids = append(ids, inv.ID)
	}
	return pendingID, ids
}

// handleCollectionsInternal receives a plan from the scheduled job. It is
// authenticated with a bearer token compared in constant time, validates the
// body strictly, stores it, and returns 204. It is disabled (404) when no
// token is configured, so the feature is strictly opt-in.
func (s *Server) handleCollectionsInternal(w http.ResponseWriter, r *http.Request) {
	token := s.cfg.CollectionsToken
	if token == "" {
		http.NotFound(w, r)
		return
	}
	if !bearerTokenMatches(r.Header.Get("Authorization"), token) {
		writeError(w, http.StatusUnauthorized, "invalid or missing bearer token")
		return
	}

	var plan collectionsPlan
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&plan); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := validateCollectionsPlan(&plan); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.collections.set(&plan)
	w.WriteHeader(http.StatusNoContent)
}

// collectionsDTO is the UI-facing shape of the latest plan. It is read-only:
// it carries no pending action and GET sets no cookie. RemindedIDs lists the
// chase invoices already reminded since the plan was generated, and
// RemindedCount is their count, so the card can show "N reminded" and total
// only the invoices still to chase.
type collectionsDTO struct {
	GeneratedAt   time.Time                   `json:"generated_at"`
	Chase         []collectionsChaseInvoice   `json:"chase"`
	Skipped       []collectionsSkippedInvoice `json:"skipped"`
	RemindedIDs   []string                    `json:"reminded_ids"`
	RemindedCount int                         `json:"reminded_count"`
}

// handleCollections returns the latest plan (or an empty one). It is strictly
// read-only: no pending action is minted and no session cookie is set, so a
// reload or a second tab has no side effects.
func (s *Server) handleCollections(w http.ResponseWriter, _ *http.Request) {
	plan, reminded := s.collections.snapshot()
	if plan == nil {
		writeJSON(w, http.StatusOK, collectionsDTO{
			Chase:       []collectionsChaseInvoice{},
			Skipped:     []collectionsSkippedInvoice{},
			RemindedIDs: []string{},
		})
		return
	}

	remindedIDs := make([]string, 0, len(reminded))
	for _, inv := range plan.Chase {
		if reminded[inv.ID] {
			remindedIDs = append(remindedIDs, inv.ID)
		}
	}

	writeJSON(w, http.StatusOK, collectionsDTO{
		GeneratedAt:   plan.GeneratedAt,
		Chase:         plan.Chase,
		Skipped:       plan.Skipped,
		RemindedIDs:   remindedIDs,
		RemindedCount: len(remindedIDs),
	})
}

// handleCollectionsApprove returns a pending action for the caller's session:
// one send_invoice_reminder call per chase invoice that has not already been
// reminded and is not claimed by another live approval. If this session
// already has a live batch, the same pending ID and actions are returned
// rather than a new batch, so a double click or a second tab cannot create two
// runnable batches for the same invoices. It returns the pendingDTO the
// confirm card expects; approval then runs through the unchanged /api/confirm.
// It is rate-limited like chat and confirm.
func (s *Server) handleCollectionsApprove(w http.ResponseWriter, r *http.Request) {
	session := sessionID(w, r)

	// mint records the batch in pendingActions and returns its single-use ID.
	// It runs under the collections store's lock (see collectionsStore.approve)
	// so the claim and the pending batch are created atomically.
	mint := func(invoiceIDs []string) string {
		calls := make([]agent.ToolCall, 0, len(invoiceIDs))
		for _, id := range invoiceIDs {
			// The input shape must match what the agent produces for
			// send_invoice_reminder: {"invoice_id":"INV2-..."}.
			input, _ := json.Marshal(map[string]string{"invoice_id": id})
			calls = append(calls, agent.ToolCall{Name: "send_invoice_reminder", Input: input})
		}
		return s.pending.add(session, calls, now())
	}

	pendingID, invoiceIDs, ok := s.collections.approve(session, mint, now())
	if !ok {
		writeError(w, http.StatusNotFound, "no invoices left to chase")
		return
	}

	dto := &pendingDTO{ID: pendingID}
	for _, id := range invoiceIDs {
		input, _ := json.Marshal(map[string]string{"invoice_id": id})
		dto.Actions = append(dto.Actions, pendingActionDTO{Name: "send_invoice_reminder", Input: input})
	}
	writeJSON(w, http.StatusOK, dto)
}

// bearerTokenMatches reports whether the Authorization header carries exactly
// the expected bearer token. It compares fixed-length SHA-256 digests in
// constant time: subtle.ConstantTimeCompare returns early when its inputs
// differ in length, which would leak the token length, so hashing first makes
// the comparison length-independent.
func bearerTokenMatches(header, token string) bool {
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return false
	}
	got := sha256.Sum256([]byte(header[len(prefix):]))
	want := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}

// validateCollectionsPlan rejects malformed or oversized plans. IDs must match
// the invoice ID pattern, lists are bounded, and string fields are length-capped.
func validateCollectionsPlan(p *collectionsPlan) error {
	if len(p.Chase) > maxCollectionsInvoices || len(p.Skipped) > maxCollectionsInvoices {
		return errTooManyInvoices
	}
	for i := range p.Chase {
		if err := validateCollectionsInvoice(p.Chase[i].ID, p.Chase[i].Customer, p.Chase[i].Amount, p.Chase[i].Currency, p.Chase[i].Reason); err != nil {
			return err
		}
	}
	for i := range p.Skipped {
		if err := validateCollectionsInvoice(p.Skipped[i].ID, p.Skipped[i].Customer, p.Skipped[i].Amount, p.Skipped[i].Currency, p.Skipped[i].Reason); err != nil {
			return err
		}
	}
	// Default a missing timestamp rather than rejecting it, so a minimal valid
	// body still stores cleanly; the UI only displays it.
	if p.GeneratedAt.IsZero() {
		p.GeneratedAt = now().UTC()
	}
	return nil
}

func validateCollectionsInvoice(id, customer, amount, currency, reason string) error {
	// A full-string match: invoiceIDPattern is unanchored (it extracts IDs
	// from tool output), so FindString must return the whole id here.
	if invoiceIDPattern.FindString(id) != id || id == "" {
		return errInvalidInvoiceID
	}
	for _, f := range []string{customer, amount, currency, reason} {
		if len(f) > maxCollectionsField {
			return errFieldTooLong
		}
	}
	return nil
}

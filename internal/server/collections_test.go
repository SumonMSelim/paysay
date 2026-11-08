package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SumonMSelim/paysay/internal/config"
)

const testCollectionsToken = "test-collections-secret-token"

// collectionsServer builds a server with the given collections token (empty to
// leave the endpoint disabled) and a recording invoker, so an approved reminder
// can be observed running.
func collectionsServer(t *testing.T, token string) (http.Handler, *recordingInvoker) {
	t.Helper()
	srv, inv := collectionsServerSrv(t, token)
	return srv.Handler(), inv
}

// collectionsServerSrv is like collectionsServer but also returns the *Server,
// for tests that need the collections store directly or that inject the clock.
func collectionsServerSrv(t *testing.T, token string) (*Server, *recordingInvoker) {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.CollectionsToken = token
	inv := &recordingInvoker{}
	return New(Options{Config: cfg, Agent: &stubAgent{}, Invoices: inv}), inv
}

// samplePlan is a minimal valid plan body with one invoice to chase and one
// skipped because payment was found.
const samplePlan = `{
	"generated_at": "2026-11-07T09:00:00Z",
	"chase": [
		{"id": "INV2-AAAA-1111", "customer": "maria@example.com", "amount": "45.00", "currency": "USD", "reason": "sent 5 days ago"}
	],
	"skipped": [
		{"id": "INV2-BBBB-2222", "customer": "sam@example.com", "amount": "20.00", "currency": "USD", "reason": "payment found"}
	]
}`

func postPlan(h http.Handler, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/internal/collections", strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestCollectionsEndpointDisabledWhenTokenUnset(t *testing.T) {
	h, _ := collectionsServer(t, "")
	rr := postPlan(h, "whatever", samplePlan)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when token unset", rr.Code)
	}
}

func TestCollectionsRejectsMissingToken(t *testing.T) {
	h, _ := collectionsServer(t, testCollectionsToken)
	rr := postPlan(h, "", samplePlan)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for missing token", rr.Code)
	}
}

func TestCollectionsRejectsWrongToken(t *testing.T) {
	h, _ := collectionsServer(t, testCollectionsToken)
	rr := postPlan(h, "wrong-token-value", samplePlan)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for wrong token", rr.Code)
	}
}

func TestCollectionsAcceptsRightTokenAndStores(t *testing.T) {
	h, _ := collectionsServer(t, testCollectionsToken)
	rr := postPlan(h, testCollectionsToken, samplePlan)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body: %s)", rr.Code, rr.Body.String())
	}

	// The stored plan is returned on GET.
	gr := httptest.NewRecorder()
	h.ServeHTTP(gr, httptest.NewRequest(http.MethodGet, "/api/collections", nil))
	if gr.Code != http.StatusOK {
		t.Fatalf("GET status = %d", gr.Code)
	}
	var dto collectionsDTO
	if err := json.Unmarshal(gr.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(dto.Chase) != 1 || dto.Chase[0].ID != "INV2-AAAA-1111" {
		t.Errorf("chase = %+v, want one invoice INV2-AAAA-1111", dto.Chase)
	}
	if len(dto.Skipped) != 1 || dto.Skipped[0].ID != "INV2-BBBB-2222" {
		t.Errorf("skipped = %+v, want one skipped invoice", dto.Skipped)
	}
}

// TestCollectionsGetHasNoSideEffects verifies GET is read-only: it sets no
// session cookie and carries no pending action, so a reload or second tab
// cannot trigger a reminder.
func TestCollectionsGetHasNoSideEffects(t *testing.T) {
	h, _ := collectionsServer(t, testCollectionsToken)
	if rr := postPlan(h, testCollectionsToken, samplePlan); rr.Code != http.StatusNoContent {
		t.Fatalf("post status = %d", rr.Code)
	}
	gr := httptest.NewRecorder()
	h.ServeHTTP(gr, httptest.NewRequest(http.MethodGet, "/api/collections", nil))
	if len(gr.Result().Cookies()) != 0 {
		t.Errorf("GET set cookies %v, want none", gr.Result().Cookies())
	}
	if strings.Contains(gr.Body.String(), "pending") {
		t.Errorf("GET body carried a pending action: %s", gr.Body.String())
	}
}

func TestCollectionsRejectsMalformedBody(t *testing.T) {
	h, _ := collectionsServer(t, testCollectionsToken)
	if rr := postPlan(h, testCollectionsToken, `{not json`); rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for malformed JSON", rr.Code)
	}
}

func TestCollectionsRejectsUnknownFields(t *testing.T) {
	h, _ := collectionsServer(t, testCollectionsToken)
	body := `{"chase":[],"skipped":[],"unexpected":true}`
	if rr := postPlan(h, testCollectionsToken, body); rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for unknown field", rr.Code)
	}
}

func TestCollectionsRejectsBadInvoiceID(t *testing.T) {
	h, _ := collectionsServer(t, testCollectionsToken)
	body := `{"chase":[{"id":"not-an-invoice","customer":"x","amount":"1","currency":"USD","reason":"r"}],"skipped":[]}`
	if rr := postPlan(h, testCollectionsToken, body); rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for malformed invoice id", rr.Code)
	}
}

func TestCollectionsRejectsOverlongField(t *testing.T) {
	h, _ := collectionsServer(t, testCollectionsToken)
	long := strings.Repeat("x", maxCollectionsField+1)
	body := fmt.Sprintf(`{"chase":[{"id":"INV2-AAAA-1111","customer":%q,"amount":"1","currency":"USD","reason":"r"}],"skipped":[]}`, long)
	if rr := postPlan(h, testCollectionsToken, body); rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an over-long field", rr.Code)
	}
}

func TestCollectionsRejectsTooManyInvoices(t *testing.T) {
	h, _ := collectionsServer(t, testCollectionsToken)
	var sb strings.Builder
	sb.WriteString(`{"chase":[`)
	for i := 0; i <= maxCollectionsInvoices; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"id":"INV2-X-%d","customer":"c","amount":"1","currency":"USD","reason":"r"}`, i)
	}
	sb.WriteString(`],"skipped":[]}`)
	if rr := postPlan(h, testCollectionsToken, sb.String()); rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for oversized plan", rr.Code)
	}
}

func TestCollectionsEmptyWhenNoPlan(t *testing.T) {
	h, _ := collectionsServer(t, testCollectionsToken)
	gr := httptest.NewRecorder()
	h.ServeHTTP(gr, httptest.NewRequest(http.MethodGet, "/api/collections", nil))
	if gr.Code != http.StatusOK {
		t.Fatalf("status = %d", gr.Code)
	}
	var dto collectionsDTO
	if err := json.Unmarshal(gr.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(dto.Chase) != 0 || len(dto.Skipped) != 0 || dto.RemindedCount != 0 {
		t.Errorf("empty plan = %+v, want no chase/skipped/reminded", dto)
	}
}

// approveCollections POSTs /api/collections/approve and returns the recorder.
func approveCollections(h http.Handler, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/collections/approve", nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// TestCollectionsApproveConfirmThenApproveAgain is the core duplicate-reminder
// guard: GET has no side effects, approve mints a pending batch, confirm runs
// it (recording the reminders), and a second approve then has nothing runnable.
func TestCollectionsApproveConfirmThenApproveAgain(t *testing.T) {
	h, inv := collectionsServer(t, testCollectionsToken)

	body := `{"chase":[
		{"id":"INV2-AAAA-1111","customer":"a@example.com","amount":"45.00","currency":"USD","reason":"sent 5 days ago"},
		{"id":"INV2-CCCC-3333","customer":"c@example.com","amount":"12.00","currency":"USD","reason":"sent 9 days ago"}
	],"skipped":[]}`
	if rr := postPlan(h, testCollectionsToken, body); rr.Code != http.StatusNoContent {
		t.Fatalf("post status = %d (body: %s)", rr.Code, rr.Body.String())
	}

	// GET is read-only: it must not mint a pending action.
	gr := httptest.NewRecorder()
	h.ServeHTTP(gr, httptest.NewRequest(http.MethodGet, "/api/collections", nil))
	if gr.Code != http.StatusOK {
		t.Fatalf("GET status = %d", gr.Code)
	}

	// Approve mints the pending batch for this session.
	ar := approveCollections(h, nil)
	if ar.Code != http.StatusOK {
		t.Fatalf("approve status = %d (body: %s)", ar.Code, ar.Body.String())
	}
	var pending pendingDTO
	if err := json.Unmarshal(ar.Body.Bytes(), &pending); err != nil {
		t.Fatalf("decode approve: %v", err)
	}
	if pending.ID == "" || len(pending.Actions) != 2 {
		t.Fatalf("pending = %+v, want two reminder actions", pending)
	}
	for _, a := range pending.Actions {
		if a.Name != "send_invoice_reminder" {
			t.Errorf("action name = %q, want send_invoice_reminder", a.Name)
		}
		var in struct {
			InvoiceID string `json:"invoice_id"`
		}
		if err := json.Unmarshal(a.Input, &in); err != nil || in.InvoiceID == "" {
			t.Errorf("action input = %s, want {invoice_id}", a.Input)
		}
	}

	// Confirm runs the reminders using the session cookie from approve.
	cookie := ar.Result().Cookies()[0]
	cr := confirm(h, pending.ID, cookie)
	if cr.Code != http.StatusOK {
		t.Fatalf("confirm status = %d (body: %s)", cr.Code, cr.Body.String())
	}
	if len(inv.ran) != 2 || inv.ran[0] != "send_invoice_reminder" {
		t.Errorf("ran = %v, want two send_invoice_reminder calls", inv.ran)
	}

	// A second approve now has nothing runnable: both invoices were reminded.
	ar2 := approveCollections(h, cookie)
	if ar2.Code != http.StatusNotFound {
		t.Fatalf("second approve status = %d, want 404 (nothing to approve)", ar2.Code)
	}

	// GET now reports both invoices as already reminded.
	gr2 := httptest.NewRecorder()
	h.ServeHTTP(gr2, httptest.NewRequest(http.MethodGet, "/api/collections", nil))
	var dto collectionsDTO
	_ = json.Unmarshal(gr2.Body.Bytes(), &dto)
	if dto.RemindedCount != 2 {
		t.Errorf("reminded_count = %d, want 2", dto.RemindedCount)
	}
}

// TestCollectionsApproveExcludesAlreadyReminded checks a partial case: one of
// two invoices was reminded, so the next approve offers only the other.
func TestCollectionsApproveExcludesAlreadyReminded(t *testing.T) {
	srv, _ := collectionsServerSrv(t, testCollectionsToken)
	h := srv.Handler()
	body := `{"chase":[
		{"id":"INV2-AAAA-1111","customer":"a@example.com","amount":"45.00","currency":"USD","reason":"r"},
		{"id":"INV2-CCCC-3333","customer":"c@example.com","amount":"12.00","currency":"USD","reason":"r"}
	],"skipped":[]}`
	if rr := postPlan(h, testCollectionsToken, body); rr.Code != http.StatusNoContent {
		t.Fatalf("post status = %d", rr.Code)
	}

	// Mark one invoice as already reminded, as a successful confirm would.
	srv.collections.markReminded("INV2-AAAA-1111")

	ar := approveCollections(h, nil)
	if ar.Code != http.StatusOK {
		t.Fatalf("approve status = %d (body: %s)", ar.Code, ar.Body.String())
	}
	var pending pendingDTO
	if err := json.Unmarshal(ar.Body.Bytes(), &pending); err != nil {
		t.Fatalf("decode approve: %v", err)
	}
	if len(pending.Actions) != 1 {
		t.Fatalf("actions = %d, want exactly 1 (the un-reminded invoice)", len(pending.Actions))
	}
	var in struct {
		InvoiceID string `json:"invoice_id"`
	}
	_ = json.Unmarshal(pending.Actions[0].Input, &in)
	if in.InvoiceID != "INV2-CCCC-3333" {
		t.Errorf("offered invoice = %q, want INV2-CCCC-3333 (the other one)", in.InvoiceID)
	}
}

// TestCollectionsNewPlanResetsReminded confirms a fresh job run clears the
// reminded set so every invoice is chaseable again.
func TestCollectionsNewPlanResetsReminded(t *testing.T) {
	srv, inv := collectionsServerSrv(t, testCollectionsToken)
	h := srv.Handler()
	body := `{"chase":[
		{"id":"INV2-AAAA-1111","customer":"a@example.com","amount":"45.00","currency":"USD","reason":"r"},
		{"id":"INV2-CCCC-3333","customer":"c@example.com","amount":"12.00","currency":"USD","reason":"r"}
	],"skipped":[]}`
	if rr := postPlan(h, testCollectionsToken, body); rr.Code != http.StatusNoContent {
		t.Fatalf("post status = %d", rr.Code)
	}
	ar := approveCollections(h, nil)
	var pending pendingDTO
	_ = json.Unmarshal(ar.Body.Bytes(), &pending)
	cookie := ar.Result().Cookies()[0]
	if cr := confirm(h, pending.ID, cookie); cr.Code != http.StatusOK {
		t.Fatalf("confirm status = %d", cr.Code)
	}
	if len(inv.ran) != 2 {
		t.Fatalf("ran = %v, want 2", inv.ran)
	}

	// A new plan resets the reminded set: both are chaseable again.
	if rr := postPlan(h, testCollectionsToken, body); rr.Code != http.StatusNoContent {
		t.Fatalf("re-post status = %d", rr.Code)
	}
	ar2 := approveCollections(h, cookie)
	if ar2.Code != http.StatusOK {
		t.Fatalf("approve after new plan status = %d, want 200", ar2.Code)
	}
	var pending2 pendingDTO
	_ = json.Unmarshal(ar2.Body.Bytes(), &pending2)
	if len(pending2.Actions) != 2 {
		t.Errorf("after new plan, actions = %d, want 2 (reminded set reset)", len(pending2.Actions))
	}
}

func TestCollectionsApproveWithoutPlan(t *testing.T) {
	h, _ := collectionsServer(t, testCollectionsToken)
	if rr := approveCollections(h, nil); rr.Code != http.StatusNotFound {
		t.Errorf("approve without plan status = %d, want 404", rr.Code)
	}
}

// twoInvoicePlan is a plan body with two chase invoices, used by the claim
// tests below.
const twoInvoicePlan = `{"chase":[
	{"id":"INV2-AAAA-1111","customer":"a@example.com","amount":"45.00","currency":"USD","reason":"r"},
	{"id":"INV2-CCCC-3333","customer":"c@example.com","amount":"12.00","currency":"USD","reason":"r"}
],"skipped":[]}`

// decodePending decodes an approve response body into a pendingDTO.
func decodePending(t *testing.T, rr *httptest.ResponseRecorder) pendingDTO {
	t.Helper()
	var p pendingDTO
	if err := json.Unmarshal(rr.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode pending: %v", err)
	}
	return p
}

// TestCollectionsDoubleApproveSameSessionReturnsSameBatch verifies the claim
// guard: approving twice in one session before confirming returns the SAME
// pending ID, and only one batch can ever be confirmed.
func TestCollectionsDoubleApproveSameSessionReturnsSameBatch(t *testing.T) {
	h, inv := collectionsServer(t, testCollectionsToken)
	if rr := postPlan(h, testCollectionsToken, twoInvoicePlan); rr.Code != http.StatusNoContent {
		t.Fatalf("post status = %d", rr.Code)
	}

	ar1 := approveCollections(h, nil)
	if ar1.Code != http.StatusOK {
		t.Fatalf("first approve status = %d", ar1.Code)
	}
	p1 := decodePending(t, ar1)
	cookie := ar1.Result().Cookies()[0]

	// Second approve in the same session: same ID, same actions, no new batch.
	ar2 := approveCollections(h, cookie)
	if ar2.Code != http.StatusOK {
		t.Fatalf("second approve status = %d", ar2.Code)
	}
	p2 := decodePending(t, ar2)
	if p2.ID != p1.ID {
		t.Fatalf("second approve ID = %q, want same as first %q", p2.ID, p1.ID)
	}
	if len(p2.Actions) != len(p1.Actions) {
		t.Errorf("second approve actions = %d, want %d", len(p2.Actions), len(p1.Actions))
	}

	// Confirm the first ID: runs the two reminders once.
	if cr := confirm(h, p1.ID, cookie); cr.Code != http.StatusOK {
		t.Fatalf("confirm status = %d", cr.Code)
	}
	if len(inv.ran) != 2 {
		t.Errorf("ran = %v, want exactly 2 (only one batch confirmable)", inv.ran)
	}

	// Confirming the same ID again is rejected (single-use), so no second send.
	if cr := confirm(h, p2.ID, cookie); cr.Code != http.StatusNotFound {
		t.Errorf("second confirm status = %d, want 404", cr.Code)
	}
	if len(inv.ran) != 2 {
		t.Errorf("ran = %v after second confirm, want still 2", inv.ran)
	}
}

// TestCollectionsApproveExcludesOtherSessionClaim verifies that an approve from
// a second session excludes invoices a first session has a live claim on.
func TestCollectionsApproveExcludesOtherSessionClaim(t *testing.T) {
	h, _ := collectionsServer(t, testCollectionsToken)
	if rr := postPlan(h, testCollectionsToken, twoInvoicePlan); rr.Code != http.StatusNoContent {
		t.Fatalf("post status = %d", rr.Code)
	}

	// Session A claims both invoices (approves, does not confirm).
	arA := approveCollections(h, nil)
	pA := decodePending(t, arA)
	if len(pA.Actions) != 2 {
		t.Fatalf("session A actions = %d, want 2", len(pA.Actions))
	}

	// Session B (no cookie → new session) now finds both claimed: nothing left.
	arB := approveCollections(h, nil)
	if arB.Code != http.StatusNotFound {
		t.Errorf("session B approve status = %d, want 404 (both claimed by A)", arB.Code)
	}
}

// TestCollectionsClaimExpiryFreesInvoices verifies that once a claim's TTL
// passes, the invoices can be approved again by another session.
func TestCollectionsClaimExpiryFreesInvoices(t *testing.T) {
	h, _ := collectionsServer(t, testCollectionsToken)

	// Inject the clock the handlers read, as the pending-action tests do.
	realNow := now
	defer func() { now = realNow }()
	base := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	now = func() time.Time { return base }

	if rr := postPlan(h, testCollectionsToken, twoInvoicePlan); rr.Code != http.StatusNoContent {
		t.Fatalf("post status = %d", rr.Code)
	}

	// Session A claims both invoices.
	arA := approveCollections(h, nil)
	if pA := decodePending(t, arA); len(pA.Actions) != 2 {
		t.Fatalf("session A actions = %d, want 2", len(pA.Actions))
	}

	// Before the TTL, another session is still excluded.
	if arB := approveCollections(h, nil); arB.Code != http.StatusNotFound {
		t.Fatalf("session B before expiry status = %d, want 404", arB.Code)
	}

	// Advance past the TTL: the claims expire and the invoices are free again.
	now = func() time.Time { return base.Add(pendingTTL + time.Second) }
	arC := approveCollections(h, nil)
	if arC.Code != http.StatusOK {
		t.Fatalf("approve after expiry status = %d, want 200", arC.Code)
	}
	if pC := decodePending(t, arC); len(pC.Actions) != 2 {
		t.Errorf("after expiry actions = %d, want 2 (claims freed)", len(pC.Actions))
	}
}

// TestCollectionsConcurrentApprovesNoDoubleClaim runs many approves at once and
// checks that no invoice is handed to two different pending batches. Run under
// -race, this also exercises the store's locking.
func TestCollectionsConcurrentApprovesNoDoubleClaim(t *testing.T) {
	h, _ := collectionsServer(t, testCollectionsToken)
	if rr := postPlan(h, testCollectionsToken, twoInvoicePlan); rr.Code != http.StatusNoContent {
		t.Fatalf("post status = %d", rr.Code)
	}

	const n = 20
	var wg sync.WaitGroup
	results := make([]pendingDTO, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Each goroutine is its own session (no cookie), competing for the
			// same two invoices.
			rr := approveCollections(h, nil)
			if rr.Code == http.StatusOK {
				results[i] = decodePendingNoFatal(rr)
			}
		}(i)
	}
	wg.Wait()

	// Every invoice ID may appear in at most one batch.
	seen := map[string]int{}
	for _, p := range results {
		for _, a := range p.Actions {
			var in struct {
				InvoiceID string `json:"invoice_id"`
			}
			_ = json.Unmarshal(a.Input, &in)
			if in.InvoiceID != "" {
				seen[in.InvoiceID]++
			}
		}
	}
	for id, count := range seen {
		if count != 1 {
			t.Errorf("invoice %s claimed by %d concurrent batches, want 1", id, count)
		}
	}
}

// decodePendingNoFatal decodes without a *testing.T, for use inside goroutines.
func decodePendingNoFatal(rr *httptest.ResponseRecorder) pendingDTO {
	var p pendingDTO
	_ = json.Unmarshal(rr.Body.Bytes(), &p)
	return p
}

func TestBearerTokenMatches(t *testing.T) {
	cases := []struct {
		header string
		want   bool
	}{
		{"Bearer " + testCollectionsToken, true},
		{"bearer " + testCollectionsToken, true}, // scheme is case-insensitive
		{"Bearer wrong", false},
		{"Bearer " + testCollectionsToken + "-extra", false}, // wrong length, must still reject
		{"Bearer " + testCollectionsToken[:len(testCollectionsToken)-1], false},
		{"Basic " + testCollectionsToken, false},
		{testCollectionsToken, false},
		{"", false},
		{"Bearer ", false},
	}
	for _, c := range cases {
		if got := bearerTokenMatches(c.header, testCollectionsToken); got != c.want {
			t.Errorf("bearerTokenMatches(%q) = %v, want %v", c.header, got, c.want)
		}
	}
}

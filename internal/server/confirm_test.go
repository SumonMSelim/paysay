package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/paysay/internal/agent"
	"github.com/SumonMSelim/paysay/internal/config"
)

// recordingInvoker records which tools ran.
type recordingInvoker struct {
	ran []string
}

func (r *recordingInvoker) Invoke(_ context.Context, name string, _ json.RawMessage) (toolResult, error) {
	r.ran = append(r.ran, name)
	return toolResult{Text: `{"id":"INV2-SENT","status":"SENT"}`}, nil
}

func confirmServer(t *testing.T, ag Agent) (http.Handler, *recordingInvoker) {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	inv := &recordingInvoker{}
	return New(Options{Config: cfg, Agent: ag, Invoices: inv, ApprovalTools: []string{"send_invoice", "send_invoice_reminder", "create_invoice"}}).Handler(), inv
}

// proposeSend chats once and returns the pending approval ID and session cookie.
func proposeSend(t *testing.T, h http.Handler) (string, *http.Cookie) {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"message":"send it"}`)))
	var resp chatResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode chat: %v", err)
	}
	if resp.Pending == nil || resp.Pending.ID == "" || len(resp.Pending.Actions) != 1 || resp.Pending.Actions[0].Name != "send_invoice" {
		t.Fatalf("pending = %+v, want a send_invoice approval", resp.Pending)
	}
	return resp.Pending.ID, rr.Result().Cookies()[0]
}

func confirm(h http.Handler, id string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/confirm", strings.NewReader(fmt.Sprintf(`{"id":%q}`, id)))
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func pendingAgent() *stubAgent {
	return &stubAgent{result: &agent.Result{
		Pending: []agent.ToolCall{{Name: "send_invoice", Input: json.RawMessage(`{"invoice_id":"INV2-SENT"}`)}},
	}}
}

func TestChatDoesNotRunPendingAction(t *testing.T) {
	h, inv := confirmServer(t, pendingAgent())
	proposeSend(t, h)
	if len(inv.ran) != 0 {
		t.Errorf("tools ran before approval: %v", inv.ran)
	}
}

func TestConfirmRunsApprovedActionOnce(t *testing.T) {
	h, inv := confirmServer(t, pendingAgent())
	id, cookie := proposeSend(t, h)

	rr := confirm(h, id, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("confirm status = %d, body %s", rr.Code, rr.Body.String())
	}
	var out struct {
		ToolCalls []agent.ToolCall `json:"tool_calls"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	if len(out.ToolCalls) != 1 || out.ToolCalls[0].Name != "send_invoice" || !strings.Contains(out.ToolCalls[0].Output, "SENT") {
		t.Errorf("tool calls = %+v", out.ToolCalls)
	}
	if len(inv.ran) != 1 || inv.ran[0] != "send_invoice" {
		t.Errorf("ran = %v, want send_invoice once", inv.ran)
	}

	if rr := confirm(h, id, cookie); rr.Code != http.StatusNotFound {
		t.Errorf("second confirm status = %d, want 404 (single use)", rr.Code)
	}
	if len(inv.ran) != 1 {
		t.Errorf("action ran %d times, want once", len(inv.ran))
	}
}

func TestConfirmRejectsOtherSession(t *testing.T) {
	h, inv := confirmServer(t, pendingAgent())
	id, _ := proposeSend(t, h)

	if rr := confirm(h, id, nil); rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for a different session", rr.Code)
	}
	if len(inv.ran) != 0 {
		t.Errorf("another session ran the action: %v", inv.ran)
	}
}

func TestPendingActionExpires(t *testing.T) {
	p := newPendingActions()
	start := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	id := p.add("s1", []agent.ToolCall{{Name: "send_invoice"}}, start)

	if _, ok := p.take("s1", id, start.Add(pendingTTL+time.Second)); ok {
		t.Error("expired action was returned")
	}
}

func TestChatReportsUnavailableModel(t *testing.T) {
	ag := &stubAgent{err: fmt.Errorf("agent: converse: %w: %w", agent.ErrModelUnavailable, errors.New("no credentials"))}
	h, _ := confirmServer(t, ag)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"message":"hi"}`)))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"code":"assistant_unavailable"`) {
		t.Errorf("body = %s, want assistant_unavailable code", rr.Body.String())
	}
}

func TestConfirmRunsEveryApprovedCall(t *testing.T) {
	ag := &stubAgent{result: &agent.Result{Pending: []agent.ToolCall{
		{Name: "send_invoice_reminder", Input: json.RawMessage(`{"invoice_id":"INV2-A"}`)},
		{Name: "send_invoice_reminder", Input: json.RawMessage(`{"invoice_id":"INV2-B"}`)},
	}}}
	h, inv := confirmServer(t, ag)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"message":"remind everyone"}`)))
	var resp chatResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Pending == nil || len(resp.Pending.Actions) != 2 {
		t.Fatalf("pending = %+v, want two reminders on one approval", resp.Pending)
	}

	if rr := confirm(h, resp.Pending.ID, rr.Result().Cookies()[0]); rr.Code != http.StatusOK {
		t.Fatalf("confirm status = %d", rr.Code)
	}
	if len(inv.ran) != 2 {
		t.Errorf("ran %v, want both reminders", inv.ran)
	}
}

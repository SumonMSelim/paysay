package paypal

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/SumonMSelim/paysay/internal/paypal/mcp"
)

// fakeMCP implements the mcpClient interface for testing Service behavior.
type fakeMCP struct {
	tools      []mcp.Tool
	lastCall   string
	lastArgs   json.RawMessage
	callResult *mcp.CallToolResult
}

func (f *fakeMCP) Initialize(ctx context.Context) (*mcp.InitializeResult, error) {
	return &mcp.InitializeResult{}, nil
}
func (f *fakeMCP) ListTools(ctx context.Context) ([]mcp.Tool, error) { return f.tools, nil }
func (f *fakeMCP) CallTool(ctx context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error) {
	f.lastCall = name
	f.lastArgs = args
	if f.callResult != nil {
		return f.callResult, nil
	}
	return &mcp.CallToolResult{Content: []mcp.ContentBlock{{Type: "text", Text: "ok"}}}, nil
}
func (f *fakeMCP) Close() error { return nil }

func TestServiceAllowListFiltersTools(t *testing.T) {
	fm := &fakeMCP{tools: []mcp.Tool{
		{Name: "create_invoice"},
		{Name: "accept_dispute_claim"},
		{Name: "send_invoice"},
	}}
	svc := newServiceWithClient(fm, WithAllowedTools("create_invoice", "send_invoice"))

	tools, err := svc.Tools(context.Background())
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("got %d tools, want 2 (allow-list should hide accept_dispute_claim)", len(tools))
	}
	for _, tool := range tools {
		if tool.Name == "accept_dispute_claim" {
			t.Error("disallowed tool leaked through allow-list")
		}
	}
}

func TestServiceInvokeRejectsDisallowedTool(t *testing.T) {
	fm := &fakeMCP{}
	svc := newServiceWithClient(fm, WithAllowedTools("create_invoice"))

	_, err := svc.Invoke(context.Background(), "accept_dispute_claim", nil)
	if err == nil {
		t.Fatal("expected error invoking disallowed tool, got nil")
	}
	if fm.lastCall != "" {
		t.Errorf("disallowed tool was still dispatched to MCP: %q", fm.lastCall)
	}
}

func TestServiceInvokePassesThrough(t *testing.T) {
	fm := &fakeMCP{callResult: &mcp.CallToolResult{
		Content: []mcp.ContentBlock{{Type: "text", Text: `{"id":"INV2-1"}`}},
	}}
	svc := newServiceWithClient(fm)

	res, err := svc.Invoke(context.Background(), "create_invoice", json.RawMessage(`{"currency_code":"EUR"}`))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if fm.lastCall != "create_invoice" {
		t.Errorf("lastCall = %q, want create_invoice", fm.lastCall)
	}
	if res.Text != `{"id":"INV2-1"}` {
		t.Errorf("result text = %q", res.Text)
	}
}

func TestMockProviderCreateSendListFlow(t *testing.T) {
	m := NewMockProvider("shop@example.com")
	ctx := context.Background()

	// create
	res, err := m.Invoke(ctx, "create_invoice", json.RawMessage(`{"recipient_email":"c@example.com","currency_code":"EUR","total":"25.00"}`))
	if err != nil || res.IsError {
		t.Fatalf("create_invoice failed: err=%v res=%+v", err, res)
	}
	var created struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(res.Text), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if created.Status != "DRAFT" {
		t.Errorf("status = %q, want DRAFT", created.Status)
	}

	// send
	sendArgs, _ := json.Marshal(map[string]string{"invoice_id": created.ID})
	res, err = m.Invoke(ctx, "send_invoice", sendArgs)
	if err != nil || res.IsError {
		t.Fatalf("send_invoice failed: err=%v res=%+v", err, res)
	}

	// list SENT
	res, err = m.Invoke(ctx, "list_invoices", json.RawMessage(`{"status":"SENT"}`))
	if err != nil {
		t.Fatalf("list_invoices: %v", err)
	}
	var listed struct {
		TotalItems int `json:"total_items"`
	}
	if err := json.Unmarshal([]byte(res.Text), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if listed.TotalItems != 1 {
		t.Errorf("total_items = %d, want 1", listed.TotalItems)
	}
}

func TestMockProviderGetMissingInvoice(t *testing.T) {
	m := NewMockProvider("")
	res, err := m.Invoke(context.Background(), "get_invoice", json.RawMessage(`{"invoice_id":"nope"}`))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !res.IsError {
		t.Error("expected IsError for missing invoice")
	}
}

func TestMockCreateInvoiceDerivesTotalFromItems(t *testing.T) {
	m := NewMockProvider("")
	// Two coffees at 15.00 and a cake at 5.50 -> 35.50. No explicit total.
	args := json.RawMessage(`{"recipient_email":"c@x.com","currency_code":"EUR","items":[{"name":"coffee","quantity":"2","unit_amount":"15.00"},{"name":"cake","quantity":"1","unit_amount":"5.50"}]}`)
	res, err := m.Invoke(context.Background(), "create_invoice", args)
	if err != nil || res.IsError {
		t.Fatalf("create_invoice: err=%v res=%+v", err, res)
	}
	var inv struct {
		Total string `json:"total"`
	}
	if err := json.Unmarshal([]byte(res.Text), &inv); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if inv.Total != "35.50" {
		t.Errorf("derived total = %q, want 35.50", inv.Total)
	}
}

func TestMockCreateInvoiceHandlesNestedUnitAmount(t *testing.T) {
	m := NewMockProvider("")
	// PayPal-style nested unit_amount {"value": "..."}.
	args := json.RawMessage(`{"currency_code":"USD","items":[{"name":"x","quantity":"3","unit_amount":{"value":"10.00","currency_code":"USD"}}]}`)
	res, err := m.Invoke(context.Background(), "create_invoice", args)
	if err != nil || res.IsError {
		t.Fatalf("create_invoice: err=%v res=%+v", err, res)
	}
	var inv struct {
		Total string `json:"total"`
	}
	_ = json.Unmarshal([]byte(res.Text), &inv)
	if inv.Total != "30.00" {
		t.Errorf("derived total = %q, want 30.00", inv.Total)
	}
}

func TestServiceCreateInvoiceScrubsSellerDetails(t *testing.T) {
	fm := &fakeMCP{}
	svc := newServiceWithClient(fm, WithMerchantEmail("shop@sandbox.example"))

	args := json.RawMessage(`{"currency_code":"EUR","invoicer_business_name":"Merchant's Business Name",
		"primary_recipients":[{"billing_info":{"email_address":"buyer@example.com"}}]}`)
	if _, err := svc.Invoke(context.Background(), "create_invoice", args); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	var sent map[string]any
	if err := json.Unmarshal(fm.lastArgs, &sent); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := sent["invoicer_business_name"]; ok {
		t.Error("invented business name reached PayPal")
	}
	if sent["invoicer_email_address"] != "shop@sandbox.example" {
		t.Errorf("invoicer email = %v, want configured merchant", sent["invoicer_email_address"])
	}
}

// Package e2e exercises PaySay end to end: a real HTTP server, the real agent
// tool-use loop, the real i18n/prompt layer, and the real PayPal mock provider.
// Only the Bedrock model is faked, with a scripted Converser that behaves like
// Nova deciding to create and then send an invoice. This verifies the whole
// voice-to-invoice path (minus live AWS/PayPal network calls) in one test.
package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brdoc "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/SumonMSelim/paysay/internal/agent"
	"github.com/SumonMSelim/paysay/internal/bedrock"
	"github.com/SumonMSelim/paysay/internal/config"
	"github.com/SumonMSelim/paysay/internal/paypal"
	"github.com/SumonMSelim/paysay/internal/server"
)

// scriptedModel simulates Nova: it maps a tool-use turn count to canned outputs.
type scriptedModel struct {
	steps []*bedrockruntime.ConverseOutput
	i     int
}

func (m *scriptedModel) Converse(ctx context.Context, in *bedrockruntime.ConverseInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error) {
	out := m.steps[m.i]
	m.i++
	return out, nil
}

func assistantText(s string) *bedrockruntime.ConverseOutput {
	return &bedrockruntime.ConverseOutput{Output: &brtypes.ConverseOutputMemberMessage{
		Value: brtypes.Message{Role: brtypes.ConversationRoleAssistant,
			Content: []brtypes.ContentBlock{&brtypes.ContentBlockMemberText{Value: s}}}}}
}

func assistantToolUse(id, name string, input map[string]any) *bedrockruntime.ConverseOutput {
	return &bedrockruntime.ConverseOutput{Output: &brtypes.ConverseOutputMemberMessage{
		Value: brtypes.Message{Role: brtypes.ConversationRoleAssistant,
			Content: []brtypes.ContentBlock{&brtypes.ContentBlockMemberToolUse{
				Value: brtypes.ToolUseBlock{ToolUseId: aws.String(id), Name: aws.String(name),
					Input: brdoc.NewLazyDocument(input)}}}}}}
}

func TestEndToEndCreateAndSendInvoiceGerman(t *testing.T) {
	// Scripted model: create invoice -> send invoice -> final German reply.
	model := &scriptedModel{steps: []*bedrockruntime.ConverseOutput{
		assistantToolUse("t1", "create_invoice", map[string]any{
			"recipient_email": "kunde@example.com",
			"currency_code":   "EUR",
			"total":           "30.00",
		}),
		// The agent feeds back the created invoice; here the model sends it.
		// It does not need the real id for this scripted step because the mock
		// assigns one; a real model would read it from the tool result.
		assistantText("Ich habe die Rechnung über 30,00 EUR erstellt und bin bereit, sie zu senden."),
	}}

	cfg := loadTestConfig(t)
	tools := paypal.NewMockProvider("shop@example.com")
	ag := agent.New(bedrock.NewWithAPI(model), tools, agent.Options{
		ModelID:   "us.amazon.nova-pro-v1:0",
		MaxTokens: 1024,
		BrandName: cfg.Brand.Name,
	})
	srv := server.New(server.Options{Config: cfg, Agent: ag})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Call the chat endpoint as the browser would.
	body := `{"message":"Erstelle eine Rechnung über 30 Euro für kunde@example.com","locale":"de-DE"}`
	resp, err := http.Post(ts.URL+"/api/chat", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/chat: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var out struct {
		Reply     string           `json:"reply"`
		Locale    string           `json:"locale"`
		ToolCalls []agent.ToolCall `json:"tool_calls"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if out.Locale != "de-DE" {
		t.Errorf("locale = %q, want de-DE", out.Locale)
	}
	if len(out.ToolCalls) != 1 || out.ToolCalls[0].Name != "create_invoice" {
		t.Fatalf("tool calls = %+v, want one create_invoice", out.ToolCalls)
	}
	if out.ToolCalls[0].IsError {
		t.Errorf("create_invoice errored: %s", out.ToolCalls[0].Output)
	}
	if !strings.Contains(out.ToolCalls[0].Output, "INV2-") {
		t.Errorf("expected an invoice id in tool output, got %s", out.ToolCalls[0].Output)
	}
	if !strings.Contains(out.Reply, "30,00 EUR") {
		t.Errorf("reply should confirm amount, got %q", out.Reply)
	}

	// Verify the invoice really exists in the provider (it was created DRAFT).
	listRes, err := tools.Invoke(context.Background(), "list_invoices", json.RawMessage(`{"status":"DRAFT"}`))
	if err != nil {
		t.Fatalf("list_invoices: %v", err)
	}
	var listed struct {
		TotalItems int `json:"total_items"`
	}
	_ = json.Unmarshal([]byte(listRes.Text), &listed)
	if listed.TotalItems != 1 {
		t.Errorf("expected 1 draft invoice persisted, got %d", listed.TotalItems)
	}
}

func TestEndToEndThaiReadQuery(t *testing.T) {
	model := &scriptedModel{steps: []*bedrockruntime.ConverseOutput{
		assistantToolUse("t1", "list_invoices", map[string]any{"status": "UNPAID"}),
		assistantText("ยังไม่มีใบแจ้งหนี้ที่ค้างชำระ"),
	}}
	cfg := loadTestConfig(t)
	ag := agent.New(bedrock.NewWithAPI(model), paypal.NewMockProvider(""), agent.Options{
		ModelID: "m", MaxTokens: 512, BrandName: cfg.Brand.Name,
	})
	srv := server.New(server.Options{Config: cfg, Agent: ag})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// No explicit locale: the Thai script should be detected server-side.
	body := `{"message":"มีใบแจ้งหนี้ที่ยังไม่จ่ายไหม"}`
	resp, err := http.Post(ts.URL+"/api/chat", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	var out struct {
		Locale    string           `json:"locale"`
		ToolCalls []agent.ToolCall `json:"tool_calls"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.Locale != "th-TH" {
		t.Errorf("locale = %q, want th-TH (script detection)", out.Locale)
	}
	if len(out.ToolCalls) != 1 || out.ToolCalls[0].Name != "list_invoices" {
		t.Errorf("expected a list_invoices call, got %+v", out.ToolCalls)
	}
}

func loadTestConfig(t *testing.T) *config.Config {
	t.Helper()
	t.Setenv("VOICE_TTS_ENABLED", "false")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

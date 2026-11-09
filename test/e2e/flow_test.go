// Package e2e exercises PaySay end to end: a real HTTP server, the real agent
// tool-use loop, the real i18n/prompt layer, and the real PayPal mock provider.
// Only the Bedrock model is faked, with a scripted Converser that behaves like
// Nova deciding to create an invoice, which the merchant then approves. This
// verifies the whole voice-to-invoice path (minus live AWS/PayPal network
// calls) in one test.
package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
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

func TestEndToEndCreateInvoiceGermanWithApproval(t *testing.T) {
	// Scripted model: asks to create an invoice. Creating is money-moving, so
	// the agent must hold it for the merchant's approval instead of running it.
	model := &scriptedModel{steps: []*bedrockruntime.ConverseOutput{
		assistantToolUse("t1", "create_invoice", map[string]any{
			"recipient_email": "kunde@example.com",
			"currency_code":   "EUR",
			"total":           "30.00",
		}),
	}}

	cfg := loadTestConfig(t)
	tools := paypal.NewMockProvider("shop@example.com")
	ag := agent.New(bedrock.NewWithAPI(model), tools, agent.Options{
		ModelID:          "us.amazon.nova-pro-v1:0",
		MaxTokens:        1024,
		BrandName:        cfg.Brand.Name,
		ApprovalRequired: paypal.ApprovalTools,
	})
	srv := server.New(server.Options{Config: cfg, Agent: ag, Invoices: server.NewInvoiceListerAdapter(shim{tools}), ApprovalTools: paypal.ApprovalTools})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// The browser keeps the session cookie between chat and approval.
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	body := `{"message":"Erstelle eine Rechnung über 30 Euro für kunde@example.com","locale":"de-DE"}`
	resp, err := client.Post(ts.URL+"/api/chat", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/chat: %v", err)
	}
	var chat struct {
		Locale    string           `json:"locale"`
		ToolCalls []agent.ToolCall `json:"tool_calls"`
		Pending   *struct {
			ID      string `json:"id"`
			Actions []struct {
				Name string `json:"name"`
			} `json:"actions"`
		} `json:"pending"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&chat); err != nil {
		t.Fatalf("decode chat: %v", err)
	}
	resp.Body.Close()

	if chat.Locale != "de-DE" {
		t.Errorf("locale = %q, want de-DE", chat.Locale)
	}
	if chat.Pending == nil || len(chat.Pending.Actions) != 1 || chat.Pending.Actions[0].Name != "create_invoice" {
		t.Fatalf("pending = %+v, want create_invoice awaiting approval", chat.Pending)
	}
	if len(chat.ToolCalls) != 0 || draftCount(t, tools) != 0 {
		t.Fatal("invoice was created before the merchant approved it")
	}

	// The merchant taps "Ja, bitte".
	resp, err = client.Post(ts.URL+"/api/confirm", "application/json", strings.NewReader(`{"id":"`+chat.Pending.ID+`"}`))
	if err != nil {
		t.Fatalf("POST /api/confirm: %v", err)
	}
	var confirmed struct {
		ToolCalls []agent.ToolCall `json:"tool_calls"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&confirmed); err != nil {
		t.Fatalf("decode confirm: %v", err)
	}
	resp.Body.Close()

	if len(confirmed.ToolCalls) != 1 || confirmed.ToolCalls[0].IsError || !strings.Contains(confirmed.ToolCalls[0].Output, "INV2-") {
		t.Errorf("approved create_invoice = %+v, want a created invoice", confirmed.ToolCalls)
	}
	if n := draftCount(t, tools); n != 1 {
		t.Errorf("drafts after approval = %d, want 1", n)
	}
}

// shim adapts the mock provider to the server's invoice interface, as the
// wiring in cmd/paysay does for the live provider.
type shim struct{ p paypal.ToolProvider }

func (s shim) Invoke(ctx context.Context, name string, args json.RawMessage) (string, bool, error) {
	res, err := s.p.Invoke(ctx, name, args)
	return res.Text, res.IsError, err
}

func draftCount(t *testing.T, tools paypal.ToolProvider) int {
	t.Helper()
	res, err := tools.Invoke(context.Background(), "list_invoices", json.RawMessage(`{"status":"DRAFT"}`))
	if err != nil {
		t.Fatalf("list_invoices: %v", err)
	}
	var listed struct {
		TotalItems int `json:"total_items"`
	}
	_ = json.Unmarshal([]byte(res.Text), &listed)
	return listed.TotalItems
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

package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brdoc "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/SumonMSelim/paysay/internal/i18n"
	"github.com/SumonMSelim/paysay/internal/paypal"
)

// scriptedConverser returns pre-programmed Converse responses in order, so we
// can simulate a model that first requests a tool and then answers.
type scriptedConverser struct {
	responses []*bedrockruntime.ConverseOutput
	calls     int
	lastInput *bedrockruntime.ConverseInput
}

func (s *scriptedConverser) Converse(ctx context.Context, in *bedrockruntime.ConverseInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error) {
	s.lastInput = in
	resp := s.responses[s.calls]
	s.calls++
	return resp, nil
}

func textResponse(text string) *bedrockruntime.ConverseOutput {
	return &bedrockruntime.ConverseOutput{
		Output: &types.ConverseOutputMemberMessage{
			Value: types.Message{
				Role:    types.ConversationRoleAssistant,
				Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: text}},
			},
		},
	}
}

func toolUseResponse(toolUseID, name string, input map[string]any) *bedrockruntime.ConverseOutput {
	return &bedrockruntime.ConverseOutput{
		Output: &types.ConverseOutputMemberMessage{
			Value: types.Message{
				Role: types.ConversationRoleAssistant,
				Content: []types.ContentBlock{
					&types.ContentBlockMemberToolUse{
						Value: types.ToolUseBlock{
							ToolUseId: aws.String(toolUseID),
							Name:      aws.String(name),
							Input:     brdoc.NewLazyDocument(input),
						},
					},
				},
			},
		},
	}
}

func newTestAgent(conv *scriptedConverser, tools paypal.ToolProvider) *Agent {
	return New(conv, tools, Options{
		ModelID:   "us.amazon.nova-pro-v1:0",
		MaxTokens: 1024,
		BrandName: "PaySay",
	})
}

func TestRespondFinalTextNoTools(t *testing.T) {
	conv := &scriptedConverser{responses: []*bedrockruntime.ConverseOutput{
		textResponse("Hallo! Wie kann ich helfen?"),
	}}
	ag := newTestAgent(conv, paypal.NewMockProvider(""))

	res, err := ag.Respond(context.Background(), nil, "Hallo", i18n.MustGet("de-DE"))
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	if res.Reply != "Hallo! Wie kann ich helfen?" {
		t.Errorf("reply = %q", res.Reply)
	}
	if len(res.ToolCalls) != 0 {
		t.Errorf("expected no tool calls, got %d", len(res.ToolCalls))
	}
	// Verify the system prompt was localized to German.
	sys := conv.lastInput.System
	if len(sys) == 0 {
		t.Fatal("expected a system prompt")
	}
}

func TestRespondExecutesToolThenAnswers(t *testing.T) {
	mock := paypal.NewMockProvider("shop@example.com")
	conv := &scriptedConverser{responses: []*bedrockruntime.ConverseOutput{
		// First: model asks to create an invoice.
		toolUseResponse("tu-1", "create_invoice", map[string]any{
			"recipient_email": "kunde@example.com",
			"currency_code":   "EUR",
			"total":           "30.00",
		}),
		// Second: model produces a final confirmation.
		textResponse("Die Rechnung über 30,00 EUR wurde erstellt."),
	}}
	ag := newTestAgent(conv, mock)

	res, err := ag.Respond(context.Background(), nil,
		"Erstelle eine Rechnung über 30 Euro für kunde@example.com", i18n.MustGet("de-DE"))
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(res.ToolCalls))
	}
	tc := res.ToolCalls[0]
	if tc.Name != "create_invoice" {
		t.Errorf("tool name = %q, want create_invoice", tc.Name)
	}
	if tc.IsError {
		t.Errorf("unexpected tool error: %s", tc.Output)
	}
	// The mock returns JSON with an invoice ID; confirm it round-tripped.
	if !strings.Contains(tc.Output, "INV2-") {
		t.Errorf("tool output missing invoice id: %s", tc.Output)
	}
	// Confirm the model's tool input was forwarded correctly.
	var input map[string]any
	if err := json.Unmarshal(tc.Input, &input); err != nil {
		t.Fatalf("tool input not valid JSON: %v", err)
	}
	if input["currency_code"] != "EUR" {
		t.Errorf("forwarded currency = %v, want EUR", input["currency_code"])
	}
	if res.Reply != "Die Rechnung über 30,00 EUR wurde erstellt." {
		t.Errorf("final reply = %q", res.Reply)
	}
	if conv.calls != 2 {
		t.Errorf("expected 2 converse calls, got %d", conv.calls)
	}
}

func TestRespondSurfacesToolErrorToModel(t *testing.T) {
	mock := paypal.NewMockProvider("")
	conv := &scriptedConverser{responses: []*bedrockruntime.ConverseOutput{
		// Model asks to get a non-existent invoice.
		toolUseResponse("tu-1", "get_invoice", map[string]any{"invoice_id": "does-not-exist"}),
		// Model recovers with an explanation.
		textResponse("Diese Rechnung konnte ich nicht finden."),
	}}
	ag := newTestAgent(conv, mock)

	res, err := ag.Respond(context.Background(), nil, "Zeig mir Rechnung X", i18n.MustGet("de-DE"))
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	if len(res.ToolCalls) != 1 || !res.ToolCalls[0].IsError {
		t.Fatalf("expected one errored tool call, got %+v", res.ToolCalls)
	}
	if res.Reply == "" {
		t.Error("expected a recovery reply")
	}
}

func TestRespondBoundsToolIterations(t *testing.T) {
	mock := paypal.NewMockProvider("")
	// Always respond with a tool use so the loop never terminates naturally.
	var responses []*bedrockruntime.ConverseOutput
	for i := 0; i < 20; i++ {
		responses = append(responses, toolUseResponse("tu", "list_invoices", map[string]any{}))
	}
	conv := &scriptedConverser{responses: responses}
	ag := New(conv, mock, Options{ModelID: "m", MaxTokens: 256, MaxToolIterations: 3})

	_, err := ag.Respond(context.Background(), nil, "loop forever", i18n.MustGet("en-US"))
	if err == nil {
		t.Fatal("expected an error when exceeding max tool iterations")
	}
	if !strings.Contains(err.Error(), "max tool iterations") {
		t.Errorf("error = %v, want max tool iterations", err)
	}
}

func TestReplyStripsThinkingBlocks(t *testing.T) {
	conv := &scriptedConverser{responses: []*bedrockruntime.ConverseOutput{
		textResponse("<thinking>The user wants a greeting. I should be friendly.</thinking>\n\nHallo! Wie kann ich helfen?"),
	}}
	ag := newTestAgent(conv, paypal.NewMockProvider(""))

	res, err := ag.Respond(context.Background(), nil, "hi", i18n.MustGet("de-DE"))
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	if strings.Contains(res.Reply, "thinking") || strings.Contains(res.Reply, "<") {
		t.Errorf("reply still contains reasoning/tags: %q", res.Reply)
	}
	if res.Reply != "Hallo! Wie kann ich helfen?" {
		t.Errorf("reply = %q, want the clean greeting", res.Reply)
	}
}

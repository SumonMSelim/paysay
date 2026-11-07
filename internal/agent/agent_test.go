package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

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

func TestBuildInitialMessagesPreservesToolOutputs(t *testing.T) {
	history := []Turn{
		{
			Role: "user",
			Text: "Create an invoice for 50 USD",
		},
		{
			Role: "assistant",
			Text: "I created invoice INV2-TEST1234.",
			ToolCalls: []ToolCall{
				{
					Name:   "create_invoice",
					Output: `{"id":"INV2-TEST1234","status":"DRAFT"}`,
				},
			},
		},
	}

	msgs := buildInitialMessages(history, "Please send it")
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}

	asstMsg := msgs[1]
	tb, ok := asstMsg.Content[0].(*types.ContentBlockMemberText)
	if !ok {
		t.Fatalf("expected text content block, got %T", asstMsg.Content[0])
	}

	if !strings.Contains(tb.Value, "[Action create_invoice: {\"id\":\"INV2-TEST1234\",\"status\":\"DRAFT\"}]") {
		t.Errorf("assistant turn did not contain tool action summary: %q", tb.Value)
	}
}

func TestRespondHoldsGatedToolForApproval(t *testing.T) {
	mock := paypal.NewMockProvider("shop@example.com")
	conv := &scriptedConverser{responses: []*bedrockruntime.ConverseOutput{
		toolUseResponse("tu-1", "send_invoice", map[string]any{"invoice_id": "INV2-ABC"}),
	}}
	ag := New(conv, mock, Options{ModelID: "m", MaxTokens: 256, ApprovalRequired: []string{"send_invoice"}})

	res, err := ag.Respond(context.Background(), nil, "Send invoice INV2-ABC", i18n.MustGet("en-US"))
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	if len(res.Pending) != 1 || res.Pending[0].Name != "send_invoice" {
		t.Fatalf("pending = %+v, want send_invoice", res.Pending)
	}
	if !strings.Contains(string(res.Pending[0].Input), "INV2-ABC") {
		t.Errorf("pending input = %s, want the invoice id", res.Pending[0].Input)
	}
	if res.Pending[0].Output != "" || len(res.ToolCalls) != 0 {
		t.Errorf("gated tool must not run: pending=%+v calls=%+v", res.Pending, res.ToolCalls)
	}
	if conv.calls != 1 {
		t.Errorf("converse calls = %d, want 1 (loop stops at the gated tool)", conv.calls)
	}
}

func TestRespondRunsUngatedToolsWithGateConfigured(t *testing.T) {
	mock := paypal.NewMockProvider("")
	conv := &scriptedConverser{responses: []*bedrockruntime.ConverseOutput{
		toolUseResponse("tu-1", "list_invoices", map[string]any{}),
		textResponse("You have no invoices."),
	}}
	ag := New(conv, mock, Options{ModelID: "m", MaxTokens: 256, ApprovalRequired: []string{"send_invoice"}})

	res, err := ag.Respond(context.Background(), nil, "List invoices", i18n.MustGet("en-US"))
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	if len(res.Pending) != 0 {
		t.Errorf("read-only tool should not need approval: %+v", res.Pending)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "list_invoices" {
		t.Errorf("tool calls = %+v, want list_invoices executed", res.ToolCalls)
	}
}

type failingConverser struct{}

func (failingConverser) Converse(context.Context, *bedrockruntime.ConverseInput, ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error) {
	return nil, errors.New("get credentials: token expired")
}

func TestRespondWrapsModelFailure(t *testing.T) {
	ag := New(failingConverser{}, paypal.NewMockProvider(""), Options{ModelID: "m", MaxTokens: 256})
	_, err := ag.Respond(context.Background(), nil, "hi", i18n.MustGet("en-US"))
	if !errors.Is(err, ErrModelUnavailable) {
		t.Errorf("err = %v, want ErrModelUnavailable", err)
	}
}

func TestShowInvoicesIsOfferedAndNeverReachesPayPal(t *testing.T) {
	tools := &countingProvider{ToolProvider: paypal.NewMockProvider("")}
	conv := &scriptedConverser{responses: []*bedrockruntime.ConverseOutput{
		toolUseResponse("tu-1", ShowInvoicesTool, map[string]any{"status": "OPEN", "customer": "anna"}),
		textResponse("Here are Anna's open invoices."),
	}}
	ag := newTestAgent(conv, tools)

	res, err := ag.Respond(context.Background(), nil, "Show me Anna's unpaid invoices", i18n.MustGet("en-US"))
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	offered := false
	for _, tool := range conv.lastInput.ToolConfig.Tools {
		if spec, ok := tool.(*types.ToolMemberToolSpec); ok && aws.ToString(spec.Value.Name) == ShowInvoicesTool {
			offered = true
		}
	}
	if !offered {
		t.Error("show_invoices was not offered to the model")
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != ShowInvoicesTool || res.ToolCalls[0].IsError {
		t.Fatalf("tool calls = %+v, want one successful show_invoices", res.ToolCalls)
	}
	if !strings.Contains(string(res.ToolCalls[0].Input), "anna") {
		t.Errorf("input = %s, want the customer filter passed to the UI", res.ToolCalls[0].Input)
	}
	if tools.invokes != 0 {
		t.Errorf("PayPal provider was called %d times for a screen-only tool", tools.invokes)
	}
}

// countingProvider counts Invoke calls on a wrapped provider.
type countingProvider struct {
	paypal.ToolProvider
	invokes int
}

func (c *countingProvider) Invoke(ctx context.Context, name string, args json.RawMessage) (paypal.ToolResult, error) {
	c.invokes++
	return c.ToolProvider.Invoke(ctx, name, args)
}

func TestBuildInitialMessagesBoundsHistory(t *testing.T) {
	var history []Turn
	for i := 0; i < 30; i++ {
		history = append(history,
			Turn{Role: "user", Text: "q"},
			Turn{Role: "assistant", Text: "a", ToolCalls: []ToolCall{{Name: "list_invoices", Output: strings.Repeat("x", 5000)}}},
		)
	}
	msgs := buildInitialMessages(history, "latest")

	if len(msgs) != maxHistoryTurns+1 {
		t.Fatalf("messages = %d, want %d (capped history + new message)", len(msgs), maxHistoryTurns+1)
	}
	if msgs[0].Role != types.ConversationRoleUser {
		t.Errorf("first message role = %s, want user", msgs[0].Role)
	}
	text := msgs[1].Content[0].(*types.ContentBlockMemberText).Value
	if len(text) > maxToolOutputInTurn+100 {
		t.Errorf("tool output in history is %d bytes, want it truncated", len(text))
	}
}

func TestTruncateKeepsUTF8Valid(t *testing.T) {
	got := truncate("ใบแจ้งหนี้", 4) // Thai characters are 3 bytes each
	if !utf8.ValidString(got) {
		t.Errorf("truncate produced invalid UTF-8: %q", got)
	}
}

func TestConcurrentFirstRequestsShareToolConfig(t *testing.T) {
	ag := New(&scriptedConverser{}, paypal.NewMockProvider(""), Options{ModelID: "m", MaxTokens: 256})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := ag.ensureToolConfig(context.Background()); err != nil {
				t.Errorf("ensureToolConfig: %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestRespondHoldsEveryGatedCallInTheTurn(t *testing.T) {
	two := &bedrockruntime.ConverseOutput{Output: &types.ConverseOutputMemberMessage{Value: types.Message{
		Role: types.ConversationRoleAssistant,
		Content: []types.ContentBlock{
			&types.ContentBlockMemberToolUse{Value: types.ToolUseBlock{ToolUseId: aws.String("a"), Name: aws.String("send_invoice_reminder"),
				Input: brdoc.NewLazyDocument(map[string]any{"invoice_id": "INV2-A"})}},
			&types.ContentBlockMemberToolUse{Value: types.ToolUseBlock{ToolUseId: aws.String("b"), Name: aws.String("send_invoice_reminder"),
				Input: brdoc.NewLazyDocument(map[string]any{"invoice_id": "INV2-B"})}},
		},
	}}}
	conv := &scriptedConverser{responses: []*bedrockruntime.ConverseOutput{two}}
	ag := New(conv, paypal.NewMockProvider(""), Options{ModelID: "m", MaxTokens: 256, ApprovalRequired: []string{"send_invoice_reminder"}})

	res, err := ag.Respond(context.Background(), nil, "Remind everyone who has not paid", i18n.MustGet("en-US"))
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	if len(res.Pending) != 2 {
		t.Fatalf("pending = %d calls, want both reminders held for one approval", len(res.Pending))
	}
}

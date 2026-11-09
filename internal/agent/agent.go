// Package agent implements the PaySay conversational agent: a Bedrock Converse
// tool-use loop that understands a merchant's natural-language request (in their
// own language) and fulfills it by calling PayPal tools exposed over MCP.
//
// The loop is deliberately explicit and bounded. Each turn:
//  1. Send the conversation + tool specs to Bedrock (Converse).
//  2. If the model asks to use tools, validate and execute each tool via the
//     PayPal ToolProvider, append the results, and loop.
//  3. If the model returns a final text answer, return it.
//
// Safety properties baked in here:
//   - Tool arguments produced by the model are treated as untrusted and are
//     re-validated by the ToolProvider allow-list before any call.
//   - The loop is bounded by MaxToolIterations to prevent runaway tool calls.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/SumonMSelim/paysay/internal/bedrock"
	"github.com/SumonMSelim/paysay/internal/i18n"
	"github.com/SumonMSelim/paysay/internal/paypal"
)

// defaultMaxToolIterations bounds how many tool round-trips the agent will make
// for a single user turn before giving up, preventing infinite tool loops.
const defaultMaxToolIterations = 8

// ShowInvoicesTool is a screen-only tool: it filters the merchant's invoice
// table and never contacts PayPal. The agent answers it locally and the web UI
// applies the filter from the recorded tool call.
const ShowInvoicesTool = "show_invoices"

const showInvoicesSchema = `{"type":"object","properties":{
"status":{"type":"string","enum":["ALL","DRAFT","OPEN","PAID"],"description":"DRAFT = not sent yet, OPEN = sent and waiting for payment, PAID = paid, ALL = every status."},
"customer":{"type":"string","description":"Part of the customer's email or name to search for. Omit to show all customers."}}}`

// ShowTimelineTool is a screen-only tool: it opens the merchant's payment timeline
// view, optionally filtered to a specific customer, and never contacts PayPal.
const ShowTimelineTool = "show_timeline"

const showTimelineSchema = `{"type":"object","properties":{
"customer":{"type":"string","description":"Part of the customer's email or name to filter the timeline by. Omit to show all customers."}}}`

// ErrModelUnavailable reports that the language model could not be reached
// (for example missing or expired cloud credentials), as opposed to a problem
// with the merchant's request.
var ErrModelUnavailable = errors.New("model unavailable")

// Agent orchestrates a Bedrock model and a PayPal tool provider.
type Agent struct {
	model     bedrock.Converser
	tools     paypal.ToolProvider
	modelID   string
	maxTokens int32
	temp      *float32
	brandName string
	maxIters  int
	gated     map[string]bool

	// toolCache stores the Bedrock tool configuration built from the provider's
	// tool list. It is populated lazily on first use.
	// toolMu guards toolConfig, which is built on first use; concurrent
	// first requests would otherwise race to build it.
	toolMu     sync.Mutex
	toolConfig *types.ToolConfiguration
}

// Options configures an Agent.
type Options struct {
	ModelID     string
	MaxTokens   int32
	Temperature *float32
	BrandName   string
	// MaxToolIterations overrides the default tool-loop bound when > 0.
	MaxToolIterations int
	// ApprovalRequired lists tools that move money or message customers. The
	// agent never runs these itself: it stops and returns the request as
	// Result.Pending, so only an explicit merchant approval can execute it.
	ApprovalRequired []string
}

// New creates an Agent.
func New(model bedrock.Converser, tools paypal.ToolProvider, opts Options) *Agent {
	iters := opts.MaxToolIterations
	if iters <= 0 {
		iters = defaultMaxToolIterations
	}
	brand := opts.BrandName
	if brand == "" {
		brand = "PaySay"
	}
	gated := make(map[string]bool, len(opts.ApprovalRequired))
	for _, n := range opts.ApprovalRequired {
		gated[n] = true
	}
	return &Agent{
		gated:     gated,
		model:     model,
		tools:     tools,
		modelID:   opts.ModelID,
		maxTokens: opts.MaxTokens,
		temp:      opts.Temperature,
		brandName: brand,
		maxIters:  iters,
	}
}

// Turn represents one entry in the conversation history exchanged with the
// client. Only user and assistant text is carried across turns at the API edge;
// the richer Bedrock message structure (including tool-use blocks) is managed
// internally within a single Respond call.
type Turn struct {
	Role      string     `json:"role"` // "user" or "assistant"
	Text      string     `json:"text"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// Proposed are money-moving calls shown for approval that did not run;
	// approved ones move to ToolCalls. Declined reports the merchant
	// cancelled them.
	Proposed []ToolCall `json:"proposed,omitempty"`
	Declined bool       `json:"declined,omitempty"`
}

// Result is the outcome of a single Respond call.
type Result struct {
	// Reply is the assistant's final natural-language reply in the merchant's
	// language.
	Reply string
	// ToolCalls records which PayPal tools were invoked and with what result,
	// for display and auditing in the UI.
	ToolCalls []ToolCall
	// Pending holds tool calls the model requested that need the merchant's
	// approval before they run (for example one reminder per unpaid invoice).
	// They are approved together. Outputs are empty: nothing has run.
	Pending []ToolCall
}

// ToolCall is an audit record of one executed tool.
type ToolCall struct {
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Output  string          `json:"output"`
	IsError bool            `json:"is_error"`
}

// Respond runs the full tool-use loop for one merchant message and returns the
// assistant's reply. history is the prior conversation (may be empty); message
// is the new merchant utterance; locale selects the reply language.
func (a *Agent) Respond(ctx context.Context, history []Turn, message string, locale i18n.Locale) (*Result, error) {
	toolConfig, err := a.ensureToolConfig(ctx)
	if err != nil {
		return nil, err
	}

	messages := buildInitialMessages(history, message)
	system := []types.SystemContentBlock{
		&types.SystemContentBlockMemberText{Value: i18n.SystemPrompt(a.brandName, locale)},
	}

	result := &Result{}

	for iter := 0; iter < a.maxIters; iter++ {
		out, err := a.model.Converse(ctx, &bedrockruntime.ConverseInput{
			ModelId:         aws.String(a.modelID),
			Messages:        messages,
			System:          system,
			ToolConfig:      toolConfig,
			InferenceConfig: a.inferenceConfig(),
		})
		if err != nil {
			return nil, fmt.Errorf("agent: converse: %w: %w", ErrModelUnavailable, err)
		}

		assistantMsg, ok := outputMessage(out)
		if !ok {
			return nil, errors.New("agent: converse returned no output message")
		}
		// Record the assistant message in history so tool results can reference
		// its tool-use IDs.
		messages = append(messages, assistantMsg)

		toolUses := extractToolUses(assistantMsg)
		if len(toolUses) == 0 {
			// No tools requested: this is the final answer.
			result.Reply = extractText(assistantMsg)
			return result, nil
		}

		// Money-moving tools stop the loop instead of running. The merchant
		// approves them separately, so the model can never act on its own.
		if pending := a.pendingApproval(toolUses); len(pending) > 0 {
			result.Pending = pending
			result.Reply = extractText(assistantMsg)
			return result, nil
		}

		// Execute each requested tool and build a single user message carrying
		// all tool results, as the Converse API expects.
		toolResultBlocks := make([]types.ContentBlock, 0, len(toolUses))
		for _, tu := range toolUses {
			call, block := a.runTool(ctx, tu)
			result.ToolCalls = append(result.ToolCalls, call)
			toolResultBlocks = append(toolResultBlocks, block)
		}
		messages = append(messages, types.Message{
			Role:    types.ConversationRoleUser,
			Content: toolResultBlocks,
		})
	}

	return nil, fmt.Errorf("agent: exceeded max tool iterations (%d)", a.maxIters)
}

// pendingApproval returns every requested tool call that needs approval.
func (a *Agent) pendingApproval(uses []types.ToolUseBlock) []ToolCall {
	var pending []ToolCall
	for _, tu := range uses {
		name := aws.ToString(tu.Name)
		if !a.gated[name] {
			continue
		}
		args, err := documentToJSON(tu.Input)
		if err != nil {
			// Unreadable input cannot be shown for approval; let runTool
			// report the error to the model instead.
			continue
		}
		// For invoice creation, resolve the invoice and due dates now so the
		// confirmation card shows the exact dates the invoice will use.
		if name == "create_invoice" {
			args = paypal.ApplyCreateInvoiceDates(args)
		}
		pending = append(pending, ToolCall{Name: name, Input: args})
	}
	return pending
}

// runTool executes one tool-use request and returns both the audit record and
// the Bedrock tool-result content block to feed back to the model.
func (a *Agent) runTool(ctx context.Context, tu types.ToolUseBlock) (ToolCall, types.ContentBlock) {
	name := aws.ToString(tu.Name)
	toolUseID := aws.ToString(tu.ToolUseId)

	// Convert the model-produced input document to JSON. This is untrusted
	// input; the ToolProvider re-validates the tool name against its allow-list.
	argsJSON, marshalErr := documentToJSON(tu.Input)

	call := ToolCall{Name: name, Input: argsJSON}

	var resultText string
	var isError bool
	if marshalErr != nil {
		resultText = fmt.Sprintf(`{"error":"could not read tool input: %s"}`, marshalErr)
		isError = true
	} else if name == ShowInvoicesTool {
		// Handled by the UI; tell the model the table is now on screen.
		resultText = `{"shown":true,"note":"The filtered invoice table is now on the merchant's screen."}`
	} else if name == ShowTimelineTool {
		// Handled by the UI; tell the model the timeline is now on screen.
		resultText = `{"shown":true,"note":"The customer payment timeline is now on the merchant's screen."}`
	} else {
		res, err := a.tools.Invoke(ctx, name, argsJSON)
		if err != nil {
			resultText = fmt.Sprintf(`{"error":%q}`, err.Error())
			isError = true
		} else {
			resultText = res.Text
			isError = res.IsError
		}
	}

	call.Output = resultText
	call.IsError = isError

	status := types.ToolResultStatusSuccess
	if isError {
		status = types.ToolResultStatusError
	}

	block := &types.ContentBlockMemberToolResult{
		Value: types.ToolResultBlock{
			ToolUseId: aws.String(toolUseID),
			Status:    status,
			Content: []types.ToolResultContentBlock{
				&types.ToolResultContentBlockMemberText{Value: resultText},
			},
		},
	}
	return call, block
}

// ensureToolConfig lazily builds the Bedrock ToolConfiguration from the PayPal
// provider's advertised tools.
func (a *Agent) ensureToolConfig(ctx context.Context) (*types.ToolConfiguration, error) {
	a.toolMu.Lock()
	defer a.toolMu.Unlock()
	if a.toolConfig != nil {
		return a.toolConfig, nil
	}
	descs, err := a.tools.Tools(ctx)
	if err != nil {
		return nil, fmt.Errorf("agent: list tools: %w", err)
	}
	if len(descs) == 0 {
		return nil, errors.New("agent: no PayPal tools available")
	}

	descs = append(descs,
		paypal.ToolDescriptor{
			Name:        ShowInvoicesTool,
			Description: "Show the merchant's invoice table on screen, filtered by status and/or customer. Use it when the merchant asks to see, show or open invoices. It only changes the screen and does not contact PayPal.",
			InputSchema: json.RawMessage(showInvoicesSchema),
		},
		paypal.ToolDescriptor{
			Name:        ShowTimelineTool,
			Description: "Show the merchant's payment timeline / schedule on screen, optionally filtered to a specific customer. Use it when the merchant asks to see a schedule, timeline or payment history of customer invoices. It only changes the screen and does not contact PayPal.",
			InputSchema: json.RawMessage(showTimelineSchema),
		},
	)

	tools := make([]types.Tool, 0, len(descs))
	for _, d := range descs {
		schemaDoc, err := jsonToDocument(d.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("agent: tool %q schema: %w", d.Name, err)
		}
		tools = append(tools, &types.ToolMemberToolSpec{
			Value: types.ToolSpecification{
				Name:        aws.String(d.Name),
				Description: aws.String(d.Description),
				InputSchema: &types.ToolInputSchemaMemberJson{Value: schemaDoc},
			},
		})
	}
	a.toolConfig = &types.ToolConfiguration{Tools: tools}
	return a.toolConfig, nil
}

func (a *Agent) inferenceConfig() *types.InferenceConfiguration {
	ic := &types.InferenceConfiguration{
		// Always set MaxTokens explicitly; leaving it unset reserves the model's
		// maximum and can trigger throttling.
		MaxTokens: aws.Int32(a.maxTokens),
	}
	if a.temp != nil {
		ic.Temperature = a.temp
	}
	return ic
}

// --- message/document helpers ---

// History limits keep each model call's size, and so its cost, bounded: the
// browser sends the whole conversation back on every turn.
const (
	maxHistoryTurns     = 20
	maxToolOutputInTurn = 1500
)

func buildInitialMessages(history []Turn, message string) []types.Message {
	if len(history) > maxHistoryTurns {
		history = history[len(history)-maxHistoryTurns:]
	}
	// Bedrock requires the conversation to start with a user turn.
	for len(history) > 0 && history[0].Role != "user" {
		history = history[1:]
	}
	msgs := make([]types.Message, 0, len(history)*2+1)
	for i, t := range history {
		if t.Role != "assistant" {
			msgs = append(msgs, textMessage(types.ConversationRoleUser, t.Text))
			continue
		}
		// Past actions are replayed as real tool-use and tool-result blocks.
		// Summarizing them as text taught the model to type fake action
		// records instead of calling tools.
		if uses, results := historyToolBlocks(i, t); len(uses) > 0 {
			msgs = append(msgs,
				types.Message{Role: types.ConversationRoleAssistant, Content: uses},
				types.Message{Role: types.ConversationRoleUser, Content: results},
			)
		}
		msgs = append(msgs, textMessage(types.ConversationRoleAssistant, t.Text))
	}
	msgs = append(msgs, types.Message{
		Role:    types.ConversationRoleUser,
		Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: message}},
	})
	return msgs
}

// Results replayed for proposed actions that never ran.
const (
	declinedResult    = `{"status":"declined","note":"The merchant cancelled this action. Nothing was sent to PayPal."}`
	notApprovedResult = `{"status":"not_approved","note":"The merchant did not approve this action, so it did not run."}`
)

// historyToolBlocks rebuilds a past turn's tool calls as tool-use blocks and
// their matching results.
func historyToolBlocks(turn int, t Turn) (uses, results []types.ContentBlock) {
	add := func(n int, c ToolCall, output string, failed bool) {
		input, err := jsonToDocument(c.Input)
		if err != nil {
			return
		}
		id := fmt.Sprintf("history-%d-%d", turn, n)
		uses = append(uses, &types.ContentBlockMemberToolUse{Value: types.ToolUseBlock{
			ToolUseId: aws.String(id), Name: aws.String(c.Name), Input: input,
		}})
		status := types.ToolResultStatusSuccess
		if failed {
			status = types.ToolResultStatusError
		}
		results = append(results, &types.ContentBlockMemberToolResult{Value: types.ToolResultBlock{
			ToolUseId: aws.String(id),
			Status:    status,
			Content:   []types.ToolResultContentBlock{&types.ToolResultContentBlockMemberText{Value: truncate(output, maxToolOutputInTurn)}},
		}})
	}
	n := 0
	for _, c := range t.ToolCalls {
		add(n, c, c.Output, c.IsError)
		n++
	}
	for _, c := range t.Proposed {
		out := notApprovedResult
		if t.Declined {
			out = declinedResult
		}
		add(n, c, out, false)
		n++
	}
	return uses, results
}

// textMessage builds a single-text message. Bedrock rejects blank text, so an
// empty turn becomes an ellipsis.
func textMessage(role types.ConversationRole, text string) types.Message {
	if strings.TrimSpace(text) == "" {
		text = "…"
	}
	return types.Message{Role: role, Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: text}}}
}

// truncate shortens s to at most n bytes without splitting a UTF-8 character.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…(truncated)"
}

func outputMessage(out *bedrockruntime.ConverseOutput) (types.Message, bool) {
	if out == nil || out.Output == nil {
		return types.Message{}, false
	}
	m, ok := out.Output.(*types.ConverseOutputMemberMessage)
	if !ok {
		return types.Message{}, false
	}
	return m.Value, true
}

func extractToolUses(m types.Message) []types.ToolUseBlock {
	var uses []types.ToolUseBlock
	for _, c := range m.Content {
		if tu, ok := c.(*types.ContentBlockMemberToolUse); ok {
			uses = append(uses, tu.Value)
		}
	}
	return uses
}

func extractText(m types.Message) string {
	var text string
	for _, c := range m.Content {
		if t, ok := c.(*types.ContentBlockMemberText); ok {
			if text != "" {
				text += "\n"
			}
			text += t.Value
		}
	}
	return cleanReply(text)
}

// thinkingBlock matches a model's internal reasoning wrapped in <thinking>...
// </thinking> tags. Some models (including Nova) emit these; they must never
// reach the merchant, who may hear the reply read aloud.
var thinkingBlock = regexp.MustCompile(`(?is)<thinking>.*?</thinking>`)

// cleanReply strips internal reasoning blocks and trims surrounding whitespace
// so only the user-facing answer remains.
func cleanReply(s string) string {
	s = thinkingBlock.ReplaceAllString(s, "")
	// Remove any stray, unmatched thinking tags left by truncation.
	s = strings.ReplaceAll(s, "<thinking>", "")
	s = strings.ReplaceAll(s, "</thinking>", "")
	return strings.TrimSpace(s)
}

// jsonToDocument converts a raw JSON schema/value into the Bedrock document
// type used for tool input schemas and tool-use inputs.
func jsonToDocument(raw json.RawMessage) (brtypes.Interface, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return brtypes.NewLazyDocument(v), nil
}

// documentToJSON serializes a Bedrock document (the model's tool-use input) back
// to raw JSON for passing to the PayPal tool. MarshalSmithyDocument yields the
// document's JSON encoding directly, which is exactly the form PayPal tools
// expect for their arguments.
func documentToJSON(doc brtypes.Interface) (json.RawMessage, error) {
	if doc == nil {
		return json.RawMessage(`{}`), nil
	}
	b, err := doc.MarshalSmithyDocument()
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return json.RawMessage(`{}`), nil
	}
	return json.RawMessage(b), nil
}

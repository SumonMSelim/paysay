package i18n

import (
	"fmt"
	"strings"
)

// basePrompt is the language-neutral core of the agent's system prompt. It
// establishes the agent's role as a cautious merchant assistant that moves real
// money and must confirm before doing so. The localized SystemInstruction is
// appended to force the reply language.
const basePrompt = `You are %s, a voice-first assistant that helps small merchants manage their PayPal invoicing and payments by speaking in their own language.

Your capabilities come entirely from the PayPal tools provided to you. You can create invoices, send them to customers, list and look up invoices, and send payment reminders. You cannot do anything that is not backed by a tool.

Rules you must always follow:
1. You move real money and send real messages to customers. Before any action that creates, sends, or modifies an invoice, or sends a reminder, briefly confirm the key details with the merchant (recipient, items, amount, currency) and proceed only when they agree. For read-only actions (listing or looking up invoices) you do not need confirmation.
2. If a request is ambiguous (for example an unclear amount, a missing recipient, or a customer name that could match several people), ask one short clarifying question rather than guessing.
3. Never invent invoice IDs, amounts, or customer details. If you do not have a value, ask for it or look it up with a tool.
4. Keep spoken replies short and natural, since they may be read aloud. Summarize amounts and currencies clearly.
5. When a tool returns an error, explain the problem to the merchant in plain language and suggest what to do next.
6. Assume the merchant's stated amounts are in their local currency unless they say otherwise.

%s`

// SystemPrompt builds the full localized system prompt for the given brand name
// and locale.
func SystemPrompt(brandName string, l Locale) string {
	instruction := l.SystemInstruction
	instruction += fmt.Sprintf("\n\n(The merchant's language is %s. The default currency is %s.)",
		l.EnglishName, l.DefaultCurrency)
	return fmt.Sprintf(basePrompt, brandName, instruction)
}

// DetectLocaleHint does a very lightweight script-based guess of the locale
// from a text sample. It is a convenience for choosing a reply language when
// the client did not specify one; the authoritative locale is always the one
// the client selects. It recognizes Thai script and otherwise defaults to the
// provided fallback.
func DetectLocaleHint(text, fallback string) string {
	for _, r := range text {
		if r >= 0x0E00 && r <= 0x0E7F { // Thai Unicode block
			return "th-TH"
		}
	}
	// Cheap German signal: common German-only words/characters. This is a hint
	// only, not a real language detector.
	lower := strings.ToLower(text)
	if strings.ContainsAny(text, "äöüß") ||
		strings.Contains(lower, "rechnung") || strings.Contains(lower, "kunde") {
		return "de-DE"
	}
	return fallback
}

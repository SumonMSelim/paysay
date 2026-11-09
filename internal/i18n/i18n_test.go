package i18n

import (
	"strings"
	"testing"
)

func TestGetKnownAndUnknown(t *testing.T) {
	de, ok := Get("de-DE")
	if !ok {
		t.Fatal("expected de-DE to be known")
	}
	if de.DefaultCurrency != "EUR" {
		t.Errorf("de-DE currency = %q, want EUR", de.DefaultCurrency)
	}

	fallback, ok := Get("xx-XX")
	if ok {
		t.Error("expected xx-XX to be unknown")
	}
	if fallback.Tag != "en-US" {
		t.Errorf("fallback tag = %q, want en-US", fallback.Tag)
	}
}

func TestThaiHasNoTTS(t *testing.T) {
	th := MustGet("th-TH")
	if th.SupportsTTS() {
		t.Error("expected Thai to report no TTS (no Polly voice configured)")
	}
	de := MustGet("de-DE")
	if !de.SupportsTTS() {
		t.Error("expected German to support TTS")
	}
}

func TestAllReturnsStableOrder(t *testing.T) {
	all := All()
	if len(all) != 3 {
		t.Fatalf("len(All()) = %d, want 3", len(all))
	}
	want := []string{"en-US", "de-DE", "th-TH"}
	for i, l := range all {
		if l.Tag != want[i] {
			t.Errorf("All()[%d] = %q, want %q", i, l.Tag, want[i])
		}
	}
}

func TestSystemPromptIsLocalized(t *testing.T) {
	th := MustGet("th-TH")
	prompt := SystemPrompt("PaySay", th)
	if !strings.Contains(prompt, "PaySay") {
		t.Error("prompt should contain brand name")
	}
	// Should contain the Thai instruction fragment.
	if !strings.Contains(prompt, th.SystemInstruction) {
		t.Error("prompt should contain the localized Thai instruction")
	}
	if !strings.Contains(prompt, "THB") {
		t.Error("prompt should mention the default currency THB")
	}
}

func TestDetectLocaleHint(t *testing.T) {
	cases := []struct {
		text     string
		fallback string
		want     string
	}{
		{"สวัสดี สร้างใบแจ้งหนี้", "en-US", "th-TH"},
		{"Erstelle eine Rechnung für Herrn Müller", "en-US", "de-DE"},
		{"Create an invoice for John", "en-US", "en-US"},
		{"plain text", "de-DE", "de-DE"},
	}
	for _, c := range cases {
		if got := DetectLocaleHint(c.text, c.fallback); got != c.want {
			t.Errorf("DetectLocaleHint(%q, %q) = %q, want %q", c.text, c.fallback, got, c.want)
		}
	}
}

func TestSystemPromptExplainsInvoiceStatuses(t *testing.T) {
	p := SystemPrompt("PaySay", MustGet("en-US"))
	for _, want := range []string{"DRAFT means created but not sent yet", "customer and amount with currency", "a draft that was never sent", "runs only if they approve"} {
		if !strings.Contains(p, want) {
			t.Errorf("system prompt missing %q", want)
		}
	}
}

func TestSystemPromptBansRawStatusCodesInReplies(t *testing.T) {
	p := SystemPrompt("PaySay", MustGet("en-US"))
	for _, want := range []string{
		"Never read out a raw status code",
		"still waiting to be paid",
		"not \"Invoice ID: INV2-MVAT-",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("system prompt missing %q", want)
		}
	}
}

func TestSystemPromptMentionsDueDateDefault(t *testing.T) {
	p := SystemPrompt("PaySay", MustGet("en-US"))
	for _, want := range []string{"pass a due date if the merchant mentions one", "14 days"} {
		if !strings.Contains(p, want) {
			t.Errorf("system prompt missing %q", want)
		}
	}
}

func TestGermanInstructionUsesFormalAddressAndDraftWording(t *testing.T) {
	de := MustGet("de-DE").SystemInstruction
	for _, want := range []string{"„Sie“", "noch nicht versendet"} {
		if !strings.Contains(de, want) {
			t.Errorf("German instruction missing %q", want)
		}
	}
}

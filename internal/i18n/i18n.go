// Package i18n holds locale definitions and the system-prompt localization for
// PaySay. The agent speaks to merchants in their own language, so each
// supported locale carries a human-language name, its speech recognition code,
// its currency default, and a localized instruction fragment appended to the
// agent system prompt.
package i18n

import "fmt"

// Locale describes one supported language for the merchant assistant.
type Locale struct {
	// Tag is the BCP-47 tag, e.g. "de-DE".
	Tag string
	// EnglishName is the language name in English, for logs/UI.
	EnglishName string
	// NativeName is the language name in its own script, for the UI selector.
	NativeName string
	// TranscribeCode is the Amazon Transcribe language code.
	TranscribeCode string
	// PollyVoiceID is an Amazon Polly voice for TTS, or "" if TTS is
	// unavailable for this language (e.g. Thai).
	PollyVoiceID string
	// DefaultCurrency is the ISO-4217 currency assumed when the merchant does
	// not state one explicitly.
	DefaultCurrency string
	// SystemInstruction is appended to the agent system prompt so the model
	// replies in this language. Written in the target language for best effect.
	SystemInstruction string
}

// registry is the set of supported locales keyed by BCP-47 tag.
var registry = map[string]Locale{
	"en-US": {
		Tag:               "en-US",
		EnglishName:       "English",
		NativeName:        "English",
		TranscribeCode:    "en-US",
		PollyVoiceID:      "Joanna",
		DefaultCurrency:   "USD",
		SystemInstruction: "Respond to the merchant in clear, friendly English.",
	},
	"de-DE": {
		Tag:               "de-DE",
		EnglishName:       "German",
		NativeName:        "Deutsch",
		TranscribeCode:    "de-DE",
		PollyVoiceID:      "Vicki",
		DefaultCurrency:   "EUR",
		SystemInstruction: "Antworte dem Händler auf Deutsch, klar und freundlich. Verwende Euro als Standardwährung, sofern nichts anderes gesagt wird.",
	},
	"th-TH": {
		Tag:            "th-TH",
		EnglishName:    "Thai",
		NativeName:     "ไทย",
		TranscribeCode: "th-TH",
		// Amazon Polly has no standard Thai voice; TTS falls back to text.
		PollyVoiceID:      "",
		DefaultCurrency:   "THB",
		SystemInstruction: "ตอบผู้ค้าเป็นภาษาไทยอย่างชัดเจนและเป็นมิตร ใช้สกุลเงินบาทเป็นค่าเริ่มต้นเว้นแต่จะระบุเป็นอย่างอื่น",
	},
}

// Get returns the locale for a tag, falling back to English if unknown. The
// boolean reports whether the tag was recognized.
func Get(tag string) (Locale, bool) {
	l, ok := registry[tag]
	if !ok {
		return registry["en-US"], false
	}
	return l, true
}

// MustGet returns the locale for a tag or panics; use only for known-good tags
// such as compile-time constants.
func MustGet(tag string) Locale {
	l, ok := registry[tag]
	if !ok {
		panic(fmt.Sprintf("i18n: unknown locale %q", tag))
	}
	return l
}

// All returns every supported locale in a stable order (English, German, Thai).
func All() []Locale {
	order := []string{"en-US", "de-DE", "th-TH"}
	out := make([]Locale, 0, len(order))
	for _, tag := range order {
		if l, ok := registry[tag]; ok {
			out = append(out, l)
		}
	}
	return out
}

// SupportsTTS reports whether text-to-speech is available for the locale.
func (l Locale) SupportsTTS() bool { return l.PollyVoiceID != "" }

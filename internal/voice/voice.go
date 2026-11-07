// Package voice provides text-to-speech for the agent's replies so merchants
// can hear responses in their own language.
//
// Design note on speech-to-text: live microphone capture and recognition is
// performed in the browser using the Web Speech API, which supports the demo
// locales (de-DE, th-TH, en-US) and avoids a fragile server-side audio stream.
// The server therefore only needs the outbound direction (TTS), defined here
// behind the Synthesizer interface. Amazon Polly backs the real implementation;
// a no-op implementation is used when TTS is disabled or unavailable for a
// locale (for example Thai, which Polly does not voice).
package voice

import (
	"context"

	"github.com/SumonMSelim/paysay/internal/i18n"
)

// Audio is a synthesized speech clip.
type Audio struct {
	// MIMEType is the audio content type, e.g. "audio/mpeg".
	MIMEType string
	// Data is the raw audio bytes.
	Data []byte
}

// Synthesizer converts text to speech for a given locale.
type Synthesizer interface {
	// Synthesize returns spoken audio for text in the locale's voice. If the
	// locale has no voice available, it returns (nil, nil) so callers can fall
	// back to text-only.
	Synthesize(ctx context.Context, text string, locale i18n.Locale) (*Audio, error)
}

// NoopSynthesizer returns no audio. Used when TTS is disabled.
type NoopSynthesizer struct{}

// Synthesize always returns no audio.
func (NoopSynthesizer) Synthesize(context.Context, string, i18n.Locale) (*Audio, error) {
	return nil, nil
}

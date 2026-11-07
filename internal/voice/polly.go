package voice

import (
	"context"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/polly"
	pollytypes "github.com/aws/aws-sdk-go-v2/service/polly/types"

	"github.com/SumonMSelim/paysay/internal/i18n"
)

// pollyAPI is the subset of the Polly client used here, declared as an interface
// for testability.
type pollyAPI interface {
	SynthesizeSpeech(ctx context.Context, in *polly.SynthesizeSpeechInput, optFns ...func(*polly.Options)) (*polly.SynthesizeSpeechOutput, error)
}

// PollySynthesizer synthesizes speech with Amazon Polly. Locales without a
// configured Polly voice (e.g. Thai) return no audio so the caller falls back
// to text, rather than erroring.
type PollySynthesizer struct {
	api pollyAPI
}

// NewPolly builds a Polly-backed synthesizer using the default AWS credential
// chain.
func NewPolly(ctx context.Context, region string) (*PollySynthesizer, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, err
	}
	return &PollySynthesizer{api: polly.NewFromConfig(cfg)}, nil
}

// newPollyWithAPI wraps an existing Polly API (used in tests).
func newPollyWithAPI(api pollyAPI) *PollySynthesizer {
	return &PollySynthesizer{api: api}
}

// Synthesize produces MP3 audio for the text in the locale's voice.
func (p *PollySynthesizer) Synthesize(ctx context.Context, text string, locale i18n.Locale) (*Audio, error) {
	if !locale.SupportsTTS() || text == "" {
		return nil, nil
	}
	out, err := p.api.SynthesizeSpeech(ctx, &polly.SynthesizeSpeechInput{
		Text:         aws.String(text),
		OutputFormat: pollytypes.OutputFormatMp3,
		VoiceId:      pollytypes.VoiceId(locale.PollyVoiceID),
		// Neural engine gives the most natural output for supported voices.
		Engine:       pollytypes.EngineNeural,
		LanguageCode: pollytypes.LanguageCode(locale.Tag),
	})
	if err != nil {
		return nil, fmt.Errorf("polly synthesize: %w", err)
	}
	defer out.AudioStream.Close()

	data, err := io.ReadAll(out.AudioStream)
	if err != nil {
		return nil, fmt.Errorf("polly read audio: %w", err)
	}
	return &Audio{MIMEType: "audio/mpeg", Data: data}, nil
}

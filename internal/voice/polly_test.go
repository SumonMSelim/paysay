package voice

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/polly"

	"github.com/SumonMSelim/paysay/internal/i18n"
)

type fakePolly struct {
	called   bool
	gotVoice string
}

func (f *fakePolly) SynthesizeSpeech(ctx context.Context, in *polly.SynthesizeSpeechInput, _ ...func(*polly.Options)) (*polly.SynthesizeSpeechOutput, error) {
	f.called = true
	f.gotVoice = string(in.VoiceId)
	return &polly.SynthesizeSpeechOutput{
		AudioStream: io.NopCloser(strings.NewReader("FAKE_MP3_BYTES")),
	}, nil
}

func TestPollySynthesizeGerman(t *testing.T) {
	fp := &fakePolly{}
	s := newPollyWithAPI(fp)

	audio, err := s.Synthesize(context.Background(), "Hallo", i18n.MustGet("de-DE"))
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if audio == nil {
		t.Fatal("expected audio for German")
	}
	if audio.MIMEType != "audio/mpeg" {
		t.Errorf("mime = %q, want audio/mpeg", audio.MIMEType)
	}
	if string(audio.Data) != "FAKE_MP3_BYTES" {
		t.Errorf("unexpected audio data: %q", audio.Data)
	}
	if fp.gotVoice != "Vicki" {
		t.Errorf("voice = %q, want Vicki", fp.gotVoice)
	}
}

func TestPollySkipsThai(t *testing.T) {
	fp := &fakePolly{}
	s := newPollyWithAPI(fp)

	audio, err := s.Synthesize(context.Background(), "สวัสดี", i18n.MustGet("th-TH"))
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if audio != nil {
		t.Error("expected no audio for Thai (no Polly voice)")
	}
	if fp.called {
		t.Error("Polly should not be called for a locale without TTS")
	}
}

func TestNoopSynthesizer(t *testing.T) {
	audio, err := NoopSynthesizer{}.Synthesize(context.Background(), "x", i18n.MustGet("de-DE"))
	if err != nil || audio != nil {
		t.Errorf("noop should return (nil,nil), got (%v,%v)", audio, err)
	}
}

package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SumonMSelim/paysay/internal/agent"
	"github.com/SumonMSelim/paysay/internal/config"
	"github.com/SumonMSelim/paysay/internal/i18n"
	"github.com/SumonMSelim/paysay/internal/voice"
)

// stubAgent implements the server Agent interface.
type stubAgent struct {
	lastMessage string
	lastLocale  string
	result      *agent.Result
	err         error
}

func (s *stubAgent) Respond(ctx context.Context, history []agent.Turn, message string, locale i18n.Locale) (*agent.Result, error) {
	s.lastMessage = message
	s.lastLocale = locale.Tag
	if s.err != nil {
		return nil, s.err
	}
	if s.result != nil {
		return s.result, nil
	}
	return &agent.Result{Reply: "ok"}, nil
}

func testServer(t *testing.T, ag Agent, synth voice.Synthesizer) *Server {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return New(Options{Config: cfg, Agent: ag, Synthesizer: synth})
}

func TestHealthEndpoint(t *testing.T) {
	srv := testServer(t, &stubAgent{}, nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"status":"ok"`) {
		t.Errorf("body = %s", rr.Body.String())
	}
}

func TestChatRoutesToAgentWithLocale(t *testing.T) {
	ag := &stubAgent{result: &agent.Result{
		Reply:     "Rechnung erstellt",
		ToolCalls: []agent.ToolCall{{Name: "create_invoice", Output: `{"id":"INV2-1"}`}},
	}}
	srv := testServer(t, ag, nil)

	body := `{"message":"Erstelle eine Rechnung","locale":"de-DE"}`
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	if ag.lastLocale != "de-DE" {
		t.Errorf("agent locale = %q, want de-DE", ag.lastLocale)
	}
	var resp chatResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Reply != "Rechnung erstellt" {
		t.Errorf("reply = %q", resp.Reply)
	}
	if len(resp.ToolCalls) != 1 {
		t.Errorf("tool calls = %d, want 1", len(resp.ToolCalls))
	}
}

func TestChatDetectsThaiWhenNoLocale(t *testing.T) {
	ag := &stubAgent{}
	srv := testServer(t, ag, nil)

	body := `{"message":"สร้างใบแจ้งหนี้"}`
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(body))
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if ag.lastLocale != "th-TH" {
		t.Errorf("detected locale = %q, want th-TH", ag.lastLocale)
	}
}

func TestChatRejectsEmptyMessage(t *testing.T) {
	srv := testServer(t, &stubAgent{}, nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"message":""}`))
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestChatAgentErrorReturns502(t *testing.T) {
	ag := &stubAgent{err: io.ErrUnexpectedEOF}
	srv := testServer(t, ag, nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"message":"hi","locale":"en-US"}`))
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rr.Code)
	}
}

func TestSecurityHeadersPresent(t *testing.T) {
	srv := testServer(t, &stubAgent{}, nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	srv.Handler().ServeHTTP(rr, req)

	if rr.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("missing X-Content-Type-Options")
	}
	if rr.Header().Get("Content-Security-Policy") == "" {
		t.Error("missing Content-Security-Policy")
	}
	if rr.Header().Get("X-Frame-Options") != "DENY" {
		t.Error("missing X-Frame-Options")
	}
}

func TestLocalesEndpoint(t *testing.T) {
	srv := testServer(t, &stubAgent{}, nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/locales", nil)
	srv.Handler().ServeHTTP(rr, req)

	var resp struct {
		Locales []localeDTO `json:"locales"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Locales) != 3 {
		t.Fatalf("locales = %d, want 3", len(resp.Locales))
	}
	// Thai should report no TTS.
	for _, l := range resp.Locales {
		if l.Tag == "th-TH" && l.SupportsTTS {
			t.Error("Thai should report supports_tts=false")
		}
	}
}

func TestSpeakReturnsAudio(t *testing.T) {
	srv := testServer(t, &stubAgent{}, stubSynth{data: []byte("MP3")})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/speak", strings.NewReader(`{"text":"Hallo","locale":"de-DE"}`))
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if rr.Header().Get("Content-Type") != "audio/mpeg" {
		t.Errorf("content-type = %q", rr.Header().Get("Content-Type"))
	}
	if rr.Body.String() != "MP3" {
		t.Errorf("body = %q", rr.Body.String())
	}
}

func TestSpeakNoAudioReturns204(t *testing.T) {
	srv := testServer(t, &stubAgent{}, stubSynth{data: nil})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/speak", strings.NewReader(`{"text":"สวัสดี","locale":"th-TH"}`))
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rr.Code)
	}
}

type stubSynth struct {
	data []byte
}

func (s stubSynth) Synthesize(ctx context.Context, text string, locale i18n.Locale) (*voice.Audio, error) {
	if s.data == nil {
		return nil, nil
	}
	return &voice.Audio{MIMEType: "audio/mpeg", Data: s.data}, nil
}

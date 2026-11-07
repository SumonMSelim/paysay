package server

import (
	"encoding/json"
	"net/http"

	"github.com/SumonMSelim/paysay/internal/agent"
	"github.com/SumonMSelim/paysay/internal/i18n"
)

// writeJSON encodes v as JSON with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a JSON error envelope.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"time":   now().UTC(),
	})
}

// handleConfig returns non-sensitive config the UI needs to render: brand,
// default locale, and whether PayPal is in live or mock mode. It never returns
// credentials.
func (s *Server) handleConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"brand":          s.cfg.Brand.Name,
		"tagline":        s.cfg.Brand.Tagline,
		"default_locale": s.cfg.DefaultLocale,
		"live_mode":      s.liveMode,
		"tts_enabled":    s.cfg.Voice.TTSEnabled,
	})
}

// localeDTO is the UI-facing shape of a locale.
type localeDTO struct {
	Tag         string `json:"tag"`
	EnglishName string `json:"english_name"`
	NativeName  string `json:"native_name"`
	SpeechCode  string `json:"speech_code"`
	SupportsTTS bool   `json:"supports_tts"`
	Currency    string `json:"currency"`
}

func (s *Server) handleLocales(w http.ResponseWriter, _ *http.Request) {
	all := i18n.All()
	out := make([]localeDTO, 0, len(all))
	for _, l := range all {
		// Only advertise locales the operator has enabled.
		if !contains(s.cfg.SupportedLocales, l.Tag) {
			continue
		}
		out = append(out, localeDTO{
			Tag:         l.Tag,
			EnglishName: l.EnglishName,
			NativeName:  l.NativeName,
			SpeechCode:  l.TranscribeCode,
			SupportsTTS: l.SupportsTTS(),
			Currency:    l.DefaultCurrency,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"locales": out})
}

// chatRequest is the body of POST /api/chat.
type chatRequest struct {
	Message string       `json:"message"`
	Locale  string       `json:"locale"`
	History []agent.Turn `json:"history"`
}

// chatResponse is the body returned by POST /api/chat.
type chatResponse struct {
	Reply     string           `json:"reply"`
	Locale    string           `json:"locale"`
	ToolCalls []agent.ToolCall `json:"tool_calls"`
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Message == "" {
		writeError(w, http.StatusBadRequest, "message is required")
		return
	}

	// Resolve locale: explicit selection wins; otherwise fall back to the
	// configured default, with a lightweight script-based hint as a tiebreaker.
	tag := req.Locale
	if tag == "" {
		tag = i18n.DetectLocaleHint(req.Message, s.cfg.DefaultLocale)
	}
	if !contains(s.cfg.SupportedLocales, tag) {
		tag = s.cfg.DefaultLocale
	}
	locale, _ := i18n.Get(tag)

	res, err := s.agent.Respond(r.Context(), req.History, req.Message, locale)
	if err != nil {
		s.log.Error("agent respond failed", "error", err, "locale", tag)
		writeError(w, http.StatusBadGateway, "the assistant could not complete the request")
		return
	}

	writeJSON(w, http.StatusOK, chatResponse{
		Reply:     res.Reply,
		Locale:    tag,
		ToolCalls: res.ToolCalls,
	})
}

// speakRequest is the body of POST /api/speak.
type speakRequest struct {
	Text   string `json:"text"`
	Locale string `json:"locale"`
}

func (s *Server) handleSpeak(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Voice.TTSEnabled {
		writeError(w, http.StatusNotFound, "text-to-speech is disabled")
		return
	}
	var req speakRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Text == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return
	}
	locale, _ := i18n.Get(req.Locale)

	audio, err := s.synth.Synthesize(r.Context(), req.Text, locale)
	if err != nil {
		s.log.Error("synthesize failed", "error", err, "locale", req.Locale)
		writeError(w, http.StatusBadGateway, "could not synthesize speech")
		return
	}
	if audio == nil {
		// No voice for this locale; signal the client to use text only.
		writeJSON(w, http.StatusNoContent, nil)
		return
	}
	w.Header().Set("Content-Type", audio.MIMEType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(audio.Data)
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

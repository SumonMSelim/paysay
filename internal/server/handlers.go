package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"unicode/utf8"

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

// Input limits bound what one request can cost. A spoken or typed request is
// short; Polly rejects more than 3,000 characters per call anyway.
const (
	maxMessageChars = 2000
	maxSpeakChars   = 3000
)

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
	Pending   *pendingDTO      `json:"pending,omitempty"`
}

// pendingDTO is a set of actions awaiting the merchant's approval. The UI
// shows them on one confirmation card and approves them together by ID.
type pendingDTO struct {
	ID      string             `json:"id"`
	Actions []pendingActionDTO `json:"actions"`
}

type pendingActionDTO struct {
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
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
	if utf8.RuneCountInString(req.Message) > maxMessageChars {
		writeError(w, http.StatusBadRequest, "message is too long")
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
	session := sessionID(w, r)

	res, err := s.agent.Respond(r.Context(), req.History, req.Message, locale)
	if err != nil {
		s.log.Error("agent respond failed", "error", err, "locale", tag)
		if errors.Is(err, agent.ErrModelUnavailable) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error": "the assistant is unavailable right now",
				"code":  "assistant_unavailable",
			})
			return
		}
		writeError(w, http.StatusBadGateway, "the assistant could not complete the request")
		return
	}
	s.sessions.recordCreated(session, res.ToolCalls)

	resp := chatResponse{
		Reply:     res.Reply,
		Locale:    tag,
		ToolCalls: res.ToolCalls,
	}
	if len(res.Pending) > 0 {
		dto := &pendingDTO{ID: s.pending.add(session, res.Pending, now())}
		for _, c := range res.Pending {
			dto.Actions = append(dto.Actions, pendingActionDTO{Name: c.Name, Input: c.Input})
		}
		resp.Pending = dto
	}
	writeJSON(w, http.StatusOK, resp)
}

// confirmRequest is the body of POST /api/confirm.
type confirmRequest struct {
	ID string `json:"id"`
}

// handleConfirm runs the actions the merchant approved on a confirmation card.
// It executes exactly the calls the agent proposed in this session; the
// browser only supplies the approval ID, never the actions themselves.
func (s *Server) handleConfirm(w http.ResponseWriter, r *http.Request) {
	var req confirmRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		writeError(w, http.StatusBadRequest, "approval id is required")
		return
	}
	session := sessionID(w, r)
	calls, ok := s.pending.take(session, req.ID, now())
	if !ok {
		writeError(w, http.StatusNotFound, "this action expired or was already handled; please ask again")
		return
	}
	if s.invoices == nil {
		writeError(w, http.StatusServiceUnavailable, "PayPal is not available")
		return
	}

	// Run each approved call; one failing does not stop the others.
	for i := range calls {
		res, err := s.invoices.Invoke(r.Context(), calls[i].Name, calls[i].Input)
		if err != nil {
			s.log.Error("approved action failed", "tool", calls[i].Name, "error", err)
			calls[i].Output = `{"error":"PayPal could not complete this action"}`
			calls[i].IsError = true
			continue
		}
		calls[i].Output = res.Text
		calls[i].IsError = res.IsError
		// Record a successful reminder so the collections card and the approve
		// endpoint do not offer to remind the same invoice twice.
		if calls[i].Name == "send_invoice_reminder" && !calls[i].IsError {
			if id := invoiceIDPattern.FindString(string(calls[i].Input)); id != "" {
				s.collections.markReminded(id)
			}
		}
	}
	s.sessions.recordCreated(session, calls)
	writeJSON(w, http.StatusOK, map[string]any{"tool_calls": calls})
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
	if utf8.RuneCountInString(req.Text) > maxSpeakChars {
		writeError(w, http.StatusBadRequest, "text is too long")
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

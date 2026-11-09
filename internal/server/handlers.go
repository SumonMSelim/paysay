package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/SumonMSelim/paysay/internal/agent"
	"github.com/SumonMSelim/paysay/internal/i18n"
)

// Bounds on how long a single state-changing request may run. An agent turn
// makes several model + tool round-trips, so it gets a generous deadline; a
// confirmed PayPal action is a small, fixed set of calls that must never be cut
// off half-way, so it gets its own bounded context rather than inheriting the
// chat deadline. Both are shorter than the server's WriteTimeout so the error
// response can still be written. They are vars (not consts) only so tests can
// shrink them.
var (
	chatTurnTimeout = 90 * time.Second
	confirmTimeout  = 60 * time.Second
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
	hasBryntum := false
	if s.webFS != nil {
		if f, err := s.webFS.Open("vendor/bryntum/scheduler.umd.js"); err == nil {
			_ = f.Close()
			hasBryntum = true
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"brand":          s.cfg.Brand.Name,
		"tagline":        s.cfg.Brand.Tagline,
		"default_locale": s.cfg.DefaultLocale,
		"live_mode":      s.liveMode,
		"paypal_ready":   s.gate.isReady(),
		"tts_enabled":    s.cfg.Voice.TTSEnabled,
		"has_bryntum":    hasBryntum,
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

	// An agent turn can run several model + tool round-trips and may outlast
	// the HTTP WriteTimeout, so give it its own bounded deadline. On timeout the
	// switch below maps context.DeadlineExceeded to 504 assistant_timeout. The
	// server's WriteTimeout is configured to be longer than this (see
	// config.go defaults) so the 504 can actually be written.
	ctx, cancel := context.WithTimeout(r.Context(), chatTurnTimeout)
	defer cancel()

	res, err := s.agent.Respond(ctx, req.History, req.Message, locale)
	if err != nil {
		s.log.Error("agent respond failed", "error", err, "locale", tag)
		switch {
		case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
			writeJSON(w, http.StatusGatewayTimeout, map[string]string{
				"error": "the assistant took too long to respond, please try again",
				"code":  "assistant_timeout",
			})
		case errors.Is(err, agent.ErrModelBusy):
			w.Header().Set("Retry-After", "30")
			writeJSON(w, http.StatusTooManyRequests, map[string]string{
				"error": "the assistant is busy right now, please try again in a moment",
				"code":  "assistant_busy",
			})
		case errors.Is(err, agent.ErrModelUnavailable):
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error": "the assistant is unavailable right now",
				"code":  "assistant_unavailable",
			})
		default:
			writeJSON(w, http.StatusBadGateway, map[string]string{
				"error": "the assistant could not complete the request",
				"code":  "assistant_error",
			})
		}
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
	invoices, ready := s.gate.lister()
	if !ready {
		writePayPalConnecting(w)
		return
	}
	if invoices == nil {
		writeError(w, http.StatusServiceUnavailable, "PayPal is not available")
		return
	}

	// A confirmed action must not be cut off half-way, so it runs on its own
	// bounded context derived from the background context rather than the
	// request context (which the browser could cancel). confirmTimeout bounds
	// the whole approved batch.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), confirmTimeout)
	defer cancel()

	// Run each approved call; one failing does not stop the others.
	for i := range calls {
		// Defence in depth: only ever run tools on the approval allow-list,
		// even though pending only holds agent-gated calls. A name outside the
		// list is refused without contacting PayPal.
		if !s.approvalTools[calls[i].Name] {
			s.log.Error("confirm refused non-approval tool", "tool", calls[i].Name)
			calls[i].Output = `{"error":"this action is not permitted"}`
			calls[i].IsError = true
			continue
		}
		res, err := invoices.Invoke(ctx, calls[i].Name, calls[i].Input)
		if err != nil {
			s.log.Error("approved action failed", "tool", calls[i].Name, "error", err)
			calls[i].Output = `{"error":"PayPal could not complete this action"}`
			calls[i].IsError = true
			continue
		}
		calls[i].Output = res.Text
		calls[i].IsError = res.IsError
		if !calls[i].IsError {
			// A successful send or remind changes invoice state, so drop the
			// cached list; the next read reflects it.
			s.invoiceCache.invalidate()
		}
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

package hub

import (
	"encoding/json"
	"net/http"

	"github.com/leejeonghun001/cloud-pulse/internal/i18n"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// SettingNotifyLanguage is the settings table key for the hub-wide
// notification message language (SPEC-v0.8 §3-3): which language
// notification message bodies and chart titles are rendered in. Absent
// means i18n.DefaultLang ("en"). This is independent of the dashboard's
// own browser-local language preference (localStorage "cp_lang",
// web/assets/js/core/i18n.js) — one controls the UI a signed-in admin
// sees, the other controls what a notified Discord/Telegram/WhatsApp
// recipient sees.
const SettingNotifyLanguage = "notify_language"

// maxNotifyLanguageBodyBytes bounds PUT /api/v1/settings/notify-language
// request bodies.
const maxNotifyLanguageBodyBytes = 1 << 10 // 1 KiB

// registerI18nRoutes registers the notification-language setting
// endpoints (SPEC-v0.8 §3-3 / Part B common ground): GET/PUT
// /api/v1/settings/notify-language.
func (s *Server) registerI18nRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/settings/notify-language", s.requireAdmin(s.handleGetNotifyLanguage))
	mux.HandleFunc("PUT /api/v1/settings/notify-language", s.requireAdmin(s.handleSetNotifyLanguage))
}

// notifyLanguageResponse is the body of both GET and PUT
// /api/v1/settings/notify-language.
type notifyLanguageResponse struct {
	Language string `json:"language"`
}

// handleGetNotifyLanguage serves GET /api/v1/settings/notify-language
// (admin): the hub's currently effective notification language.
func (s *Server) handleGetNotifyLanguage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	raw, ok, err := s.store.GetSetting(ctx, SettingNotifyLanguage)
	if err != nil {
		s.logger.Error("get notify language setting failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	lang := i18n.DefaultLang
	if ok {
		if parsed, parsedOK := i18n.Parse(raw); parsedOK {
			lang = parsed
		}
	}
	writeJSON(w, http.StatusOK, notifyLanguageResponse{Language: string(lang)})
}

// AuditActionNotifyLanguageChange records a change to the hub-wide
// notification message language (SPEC-v0.8 §3-3).
const AuditActionNotifyLanguageChange models.AuditAction = "notify_language.change"

// notifyLanguageRequest is the body of PUT
// /api/v1/settings/notify-language.
type notifyLanguageRequest struct {
	Language string `json:"language"`
}

// handleSetNotifyLanguage sets the hub-wide notification message
// language: PUT /api/v1/settings/notify-language (admin). Only "en" or
// "ko" (case-insensitive) is accepted; any other value is rejected with
// 400. Does not itself change any already-rendered/queued message —
// callers (internal/alerting, internal/notify) re-read the effective
// setting each time a message is built.
func (s *Server) handleSetNotifyLanguage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	r.Body = http.MaxBytesReader(w, r.Body, maxNotifyLanguageBodyBytes)
	var req notifyLanguageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid request body"})
		return
	}
	lang, ok := i18n.Parse(req.Language)
	if !ok {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "language must be one of en, ko"})
		return
	}

	beforeRaw, hadBefore, err := s.store.GetSetting(ctx, SettingNotifyLanguage)
	if err != nil {
		s.logger.Error("get notify language setting failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	before := string(i18n.DefaultLang)
	if hadBefore {
		before = beforeRaw
	}

	if err := s.store.SetSetting(ctx, SettingNotifyLanguage, string(lang)); err != nil {
		s.logger.Error("set notify language setting failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	s.recordAudit(ctx, r, AuditActionNotifyLanguageChange, "notify_language", "",
		map[string]string{"language": before},
		map[string]string{"language": string(lang)},
	)
	writeJSON(w, http.StatusOK, notifyLanguageResponse{Language: string(lang)})
}

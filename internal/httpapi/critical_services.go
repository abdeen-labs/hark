package httpapi

import (
	"net/http"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/secret"
)

// criticalServiceDTO is a webhook service managed in the separate Critical
// Alerts flow. Its webhook accepts the same payload as every other service.
type criticalServiceDTO struct {
	ID              string    `json:"id"`
	Title           string    `json:"title"`
	ImageURL        *string   `json:"image_url"`
	URL             *string   `json:"url"`
	Priority        string    `json:"priority"`
	CriticalEnabled bool      `json:"critical_enabled"`
	WebhookURL      *string   `json:"webhook_url"`
	CreatedAt       Timestamp `json:"created_at"`
	UpdatedAt       Timestamp `json:"updated_at"`
}

func (s *server) newCriticalServiceDTO(r *http.Request, svc db.Service) criticalServiceDTO {
	return criticalServiceDTO{
		ID:              svc.ID,
		Title:           svc.Title,
		ImageURL:        svc.ImageURL,
		URL:             svc.URL,
		Priority:        svc.Priority,
		CriticalEnabled: svc.CriticalEnabled,
		WebhookURL:      s.webhookURL(r, svc),
		CreatedAt:       Timestamp(svc.CreatedAt),
		UpdatedAt:       Timestamp(svc.UpdatedAt),
	}
}

type criticalServiceListResponse struct {
	Services []criticalServiceDTO `json:"services"`
}

type criticalServiceResponse struct {
	Service criticalServiceDTO `json:"service"`
}

type createdCriticalServiceResponse struct {
	Service    criticalServiceDTO `json:"service"`
	WebhookURL string             `json:"webhook_url"`
}

func (s *server) handleListCriticalServices(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFrom(r.Context())
	services, err := s.store().Services.ListCriticalForUser(r.Context(), principal.UserID())
	if err != nil {
		s.writeInternal(w, r, "listing critical services failed", err)
		return
	}
	out := make([]criticalServiceDTO, 0, len(services))
	for _, svc := range services {
		out = append(out, s.newCriticalServiceDTO(r, svc))
	}
	WriteJSON(w, r, http.StatusOK, criticalServiceListResponse{Services: out})
}

func (s *server) handleGetCriticalService(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFrom(r.Context())
	svc, err := s.store().Services.CriticalByID(r.Context(), r.PathValue("id"), principal.UserID())
	if err != nil {
		s.writeStoreError(w, r, "critical service", err)
		return
	}
	WriteJSON(w, r, http.StatusOK, criticalServiceResponse{Service: s.newCriticalServiceDTO(r, *svc)})
}

type createCriticalServiceRequest struct {
	Title           string  `json:"title"`
	ImageURL        *string `json:"image_url"`
	URL             *string `json:"url"`
	Priority        *string `json:"priority"`
	CriticalEnabled *bool   `json:"critical_enabled"`
}

func (s *server) handleCreateCriticalService(w http.ResponseWriter, r *http.Request) {
	var body createCriticalServiceRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	var v validator
	title := v.text("title", body.Title, 1, maxTitleLen)
	imageURL := v.httpsURL("image_url", body.ImageURL)
	linkURL := v.linkURL("url", body.URL)
	priority := v.enum("priority", body.Priority, db.CriticalPriorities, db.PriorityNormal)
	if !v.done(w, r) {
		return
	}
	criticalEnabled := true
	if body.CriticalEnabled != nil {
		criticalEnabled = *body.CriticalEnabled
	}

	token := auth.NewWebhookToken()
	ciphertext, err := s.opts.Secrets.Encrypt(secret.PurposeWebhookToken, token)
	if err != nil {
		s.writeInternal(w, r, "sealing a webhook token failed", err)
		return
	}
	principal := auth.PrincipalFrom(r.Context())
	svc, err := s.store().Services.Create(r.Context(), db.CreateServiceParams{
		ID: newID(), UserID: principal.UserID(), Title: title,
		ImageURL: imageURL, URL: linkURL, Priority: priority,
		CriticalCapable: true, CriticalEnabled: criticalEnabled,
		TokenHash: auth.WebhookTokenHash(token), TokenCiphertext: ciphertext, Now: s.now(),
	})
	if err != nil {
		s.writeInternal(w, r, "creating a critical service failed", err)
		return
	}
	url := s.hookURL(token)
	WriteJSON(w, r, http.StatusCreated, createdCriticalServiceResponse{
		Service: s.newCriticalServiceDTO(r, *svc), WebhookURL: url,
	})
}

type updateCriticalServiceRequest struct {
	Title           optional[string]  `json:"title"`
	ImageURL        optional[*string] `json:"image_url"`
	URL             optional[*string] `json:"url"`
	Priority        optional[string]  `json:"priority"`
	CriticalEnabled optional[bool]    `json:"critical_enabled"`
}

func (s *server) handleUpdateCriticalService(w http.ResponseWriter, r *http.Request) {
	var body updateCriticalServiceRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	var v validator
	params := db.UpdateServiceParams{
		ID: r.PathValue("id"), UserID: auth.PrincipalFrom(r.Context()).UserID(),
		CriticalCapable: true, Now: s.now(),
	}
	if title, ok := body.Title.Get(); ok {
		params.Title = db.Value(v.text("title", title, 1, maxTitleLen))
	}
	if image, ok := body.ImageURL.Get(); ok {
		params.ImageURL = db.Value(v.httpsURL("image_url", image))
	}
	if link, ok := body.URL.Get(); ok {
		params.URL = db.Value(v.linkURL("url", link))
	}
	if priority, ok := body.Priority.Get(); ok {
		params.Priority = db.Value(v.enum("priority", &priority, db.CriticalPriorities, db.PriorityNormal))
	}
	if enabled, ok := body.CriticalEnabled.Get(); ok {
		params.CriticalEnabled = db.Value(enabled)
	}
	if !params.Title.IsSet() && !params.ImageURL.IsSet() && !params.URL.IsSet() &&
		!params.Priority.IsSet() && !params.CriticalEnabled.IsSet() {
		v.add("title", "at least one editable field is required")
	}
	if !v.done(w, r) {
		return
	}
	svc, err := s.store().Services.Update(r.Context(), params)
	if err != nil {
		s.writeStoreError(w, r, "critical service", err)
		return
	}
	WriteJSON(w, r, http.StatusOK, criticalServiceResponse{Service: s.newCriticalServiceDTO(r, *svc)})
}

func (s *server) handleRotateCriticalWebhookToken(w http.ResponseWriter, r *http.Request) {
	token := auth.NewWebhookToken()
	ciphertext, err := s.opts.Secrets.Encrypt(secret.PurposeWebhookToken, token)
	if err != nil {
		s.writeInternal(w, r, "sealing a webhook token failed", err)
		return
	}
	principal := auth.PrincipalFrom(r.Context())
	svc, err := s.store().Services.RotateCriticalToken(r.Context(), r.PathValue("id"),
		principal.UserID(), auth.WebhookTokenHash(token), ciphertext, s.now())
	if err != nil {
		s.writeStoreError(w, r, "critical service", err)
		return
	}
	url := s.hookURL(token)
	WriteJSON(w, r, http.StatusCreated, createdCriticalServiceResponse{
		Service: s.newCriticalServiceDTO(r, *svc), WebhookURL: url,
	})
}

func (s *server) handleDeleteCriticalService(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFrom(r.Context())
	deleted, err := s.store().Services.DeleteCritical(r.Context(), r.PathValue("id"), principal.UserID())
	switch {
	case err != nil:
		s.writeInternal(w, r, "deleting a critical service failed", err)
	case !deleted:
		s.writeNotFound(w, r, "critical service")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

type criticalSettingsResponse struct {
	CriticalAlertsEnabled bool `json:"critical_alerts_enabled"`
}

func (s *server) handleGetCriticalSettings(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFrom(r.Context())
	user, err := s.store().Users.ByID(r.Context(), principal.UserID())
	if err != nil {
		s.writeInternal(w, r, "loading the Critical Alert settings failed", err)
		return
	}
	WriteJSON(w, r, http.StatusOK, criticalSettingsResponse{CriticalAlertsEnabled: user.CriticalAlertsEnabled})
}

type updateCriticalSettingsRequest struct {
	CriticalAlertsEnabled *bool `json:"critical_alerts_enabled"`
}

func (s *server) handleUpdateCriticalSettings(w http.ResponseWriter, r *http.Request) {
	var body updateCriticalSettingsRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	var v validator
	if body.CriticalAlertsEnabled == nil {
		v.add("critical_alerts_enabled", "must be true or false")
	}
	if !v.done(w, r) {
		return
	}
	principal := auth.PrincipalFrom(r.Context())
	if err := s.store().Users.SetCriticalAlertsEnabled(r.Context(), principal.UserID(),
		*body.CriticalAlertsEnabled, s.now()); err != nil {
		s.writeInternal(w, r, "saving the Critical Alert settings failed", err)
		return
	}
	WriteJSON(w, r, http.StatusOK, criticalSettingsResponse{CriticalAlertsEnabled: *body.CriticalAlertsEnabled})
}

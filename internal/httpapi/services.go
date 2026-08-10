package httpapi

import (
	"errors"
	"net/http"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/secret"
)

// serviceDTO is a webhook source as every response renders it.
type serviceDTO struct {
	ID       string  `json:"id"`
	Title    string  `json:"title"`
	ImageURL *string `json:"image_url"`
	URL      *string `json:"url"`
	Priority string  `json:"priority"`
	// WebhookURL is the ingest URL, credential and all. It is shown only to the
	// account owner's session: a token holding services:read is a read
	// credential, and handing it a URL that can send notifications would let it
	// widen its own reach.
	WebhookURL *string   `json:"webhook_url"`
	CreatedAt  Timestamp `json:"created_at"`
	UpdatedAt  Timestamp `json:"updated_at"`
}

func newServiceDTO(s db.Service, webhookURL *string) serviceDTO {
	return serviceDTO{
		ID:         s.ID,
		Title:      s.Title,
		ImageURL:   s.ImageURL,
		URL:        s.URL,
		Priority:   s.Priority,
		WebhookURL: webhookURL,
		CreatedAt:  Timestamp(s.CreatedAt),
		UpdatedAt:  Timestamp(s.UpdatedAt),
	}
}

// webhookURL rebuilds a service's ingest URL from the stored ciphertext, for a
// caller allowed to see it.
//
// A ciphertext this key cannot open is reported as "no URL" rather than as an
// error: the service still works for whoever already holds the token, and the
// owner's remedy — rotate it — is one request away.
func (s *server) webhookURL(r *http.Request, svc db.Service) *string {
	if principal := auth.PrincipalFrom(r.Context()); !principal.IsSession() {
		return nil
	}
	token, err := s.opts.Secrets.Decrypt(secret.PurposeWebhookToken, svc.TokenCiphertext)
	if err != nil {
		LoggerFrom(r.Context()).WarnContext(r.Context(), "webhook token could not be decrypted",
			"service_id", svc.ID, "error", err)
		return nil
	}
	url := s.hookURL(token)
	return &url
}

// hookURL renders the public ingest URL for a plaintext webhook token.
func (s *server) hookURL(token string) string {
	return s.publicPath(APIPrefix + "/hooks/" + token)
}

type serviceListResponse struct {
	Services []serviceDTO `json:"services"`
}

type serviceResponse struct {
	Service serviceDTO `json:"service"`
}

// createdServiceResponse carries the plaintext ingest URL alongside the service.
// Creation and rotation are both session-only, so the URL goes to the
// authenticated session that minted it — shown this once, because the stored
// form is a ciphertext nobody else will decrypt for them.
type createdServiceResponse struct {
	Service    serviceDTO `json:"service"`
	WebhookURL string     `json:"webhook_url"`
}

// handleListServices returns the account's webhook sources, newest first.
func (s *server) handleListServices(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFrom(r.Context())

	services, err := s.store().Services.ListForUser(r.Context(), principal.UserID())
	if err != nil {
		s.writeInternal(w, r, "listing services failed", err)
		return
	}

	out := make([]serviceDTO, 0, len(services))
	for _, svc := range services {
		out = append(out, newServiceDTO(svc, s.webhookURL(r, svc)))
	}
	WriteJSON(w, r, http.StatusOK, serviceListResponse{Services: out})
}

// handleGetService returns one service.
func (s *server) handleGetService(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFrom(r.Context())

	svc, err := s.store().Services.ByID(r.Context(), r.PathValue("id"), principal.UserID())
	if err != nil {
		s.writeStoreError(w, r, "service", err)
		return
	}
	WriteJSON(w, r, http.StatusOK, serviceResponse{Service: newServiceDTO(*svc, s.webhookURL(r, *svc))})
}

type createServiceRequest struct {
	Title    string  `json:"title"`
	ImageURL *string `json:"image_url"`
	URL      *string `json:"url"`
	Priority *string `json:"priority"`
}

// handleCreateService mints a webhook source and its credential.
func (s *server) handleCreateService(w http.ResponseWriter, r *http.Request) {
	var body createServiceRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	var v validator
	title := v.text("title", body.Title, 1, maxTitleLen)
	imageURL := v.httpsURL("image_url", body.ImageURL)
	linkURL := v.linkURL("url", body.URL)
	priority := v.enum("priority", body.Priority, db.Priorities, db.PriorityNormal)
	if !v.done(w, r) {
		return
	}

	token := auth.NewWebhookToken()
	ciphertext, err := s.opts.Secrets.Encrypt(secret.PurposeWebhookToken, token)
	if err != nil {
		s.writeInternal(w, r, "sealing a webhook token failed", err)
		return
	}

	principal := auth.PrincipalFrom(r.Context())
	svc, err := s.store().Services.Create(r.Context(), db.CreateServiceParams{
		ID:              newID(),
		UserID:          principal.UserID(),
		Title:           title,
		ImageURL:        imageURL,
		URL:             linkURL,
		Priority:        priority,
		TokenHash:       auth.WebhookTokenHash(token),
		TokenCiphertext: ciphertext,
		Now:             s.now(),
	})
	if err != nil {
		s.writeInternal(w, r, "creating a service failed", err)
		return
	}

	url := s.hookURL(token)
	WriteJSON(w, r, http.StatusCreated, createdServiceResponse{
		Service:    newServiceDTO(*svc, &url),
		WebhookURL: url,
	})
}

type updateServiceRequest struct {
	Title    optional[string]  `json:"title"`
	ImageURL optional[*string] `json:"image_url"`
	URL      optional[*string] `json:"url"`
	Priority optional[string]  `json:"priority"`
}

// handleUpdateService applies a partial change to a service's defaults.
//
// Only the fields the request names are written, and a null clears one: a
// notification's sender name, avatar and tap destination are what a webhook
// leaves out, so being able to remove one is as necessary as being able to set
// it.
func (s *server) handleUpdateService(w http.ResponseWriter, r *http.Request) {
	var body updateServiceRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	var v validator
	params := db.UpdateServiceParams{
		ID:     r.PathValue("id"),
		UserID: auth.PrincipalFrom(r.Context()).UserID(),
		Now:    s.now(),
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
		params.Priority = db.Value(v.enum("priority", &priority, db.Priorities, db.PriorityNormal))
	}
	if !params.Title.IsSet() && !params.ImageURL.IsSet() && !params.URL.IsSet() && !params.Priority.IsSet() {
		v.add("title", "at least one of title, image_url, url or priority is required")
	}
	if !v.done(w, r) {
		return
	}

	svc, err := s.store().Services.Update(r.Context(), params)
	if err != nil {
		s.writeStoreError(w, r, "service", err)
		return
	}
	WriteJSON(w, r, http.StatusOK, serviceResponse{Service: newServiceDTO(*svc, s.webhookURL(r, *svc))})
}

// handleRotateWebhookToken replaces a service's credential.
//
// The previous token stops working immediately: there is no grace period and no
// second slot, because a rotation is what an owner reaches for when a token has
// leaked, and a leaked token that keeps working for an hour is not rotated.
func (s *server) handleRotateWebhookToken(w http.ResponseWriter, r *http.Request) {
	token := auth.NewWebhookToken()
	ciphertext, err := s.opts.Secrets.Encrypt(secret.PurposeWebhookToken, token)
	if err != nil {
		s.writeInternal(w, r, "sealing a webhook token failed", err)
		return
	}

	principal := auth.PrincipalFrom(r.Context())
	svc, err := s.store().Services.RotateToken(r.Context(),
		r.PathValue("id"), principal.UserID(), auth.WebhookTokenHash(token), ciphertext, s.now())
	if err != nil {
		s.writeStoreError(w, r, "service", err)
		return
	}

	url := s.hookURL(token)
	WriteJSON(w, r, http.StatusCreated, createdServiceResponse{
		Service:    newServiceDTO(*svc, &url),
		WebhookURL: url,
	})
}

// handleDeleteService removes a service and everything it produced.
func (s *server) handleDeleteService(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFrom(r.Context())

	deleted, err := s.store().Services.Delete(r.Context(), r.PathValue("id"), principal.UserID())
	switch {
	case err != nil:
		s.writeInternal(w, r, "deleting a service failed", err)
	case !deleted:
		s.writeNotFound(w, r, "service")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// authenticateWebhook resolves the service behind a path token.
//
// Every failure is the same 404: an unknown token, a malformed one, and a
// deleted service are indistinguishable to the caller, so the URL cannot be
// probed to learn which of the three it is.
func (s *server) authenticateWebhook(w http.ResponseWriter, r *http.Request) (*db.Service, bool) {
	token := r.PathValue("token")
	if !auth.ValidWebhookToken(token) {
		s.writeNotFound(w, r, "webhook")
		return nil, false
	}

	svc, err := s.store().Services.ByTokenHash(r.Context(), auth.WebhookTokenHash(token))
	switch {
	case errors.Is(err, db.ErrNotFound):
		s.writeNotFound(w, r, "webhook")
		return nil, false
	case err != nil:
		s.writeInternal(w, r, "resolving a webhook failed", err)
		return nil, false
	}
	return svc, true
}

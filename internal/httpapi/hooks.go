package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/push"
	"github.com/abdeen-labs/hark/internal/secret"
)

// Callback bearer-token bounds. Long enough to be a real credential, short
// enough that nobody is storing a certificate in there.
const (
	minCallbackTokenLen = 16
	maxCallbackTokenLen = 512
)

// webhookEventDTO is what the sender of a webhook is told about its delivery.
//
// It is deliberately smaller than the owner-facing event: no provider error
// text, because an APNs failure can embed a device token and the holder of a
// webhook URL is not necessarily the account owner.
type webhookEventDTO struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	// DeliveredCount is how many messages APNs accepted.
	DeliveredCount int       `json:"delivered_count"`
	CreatedAt      Timestamp `json:"created_at"`
}

func newWebhookEventDTO(e db.Event) webhookEventDTO {
	return webhookEventDTO{
		ID:             e.ID,
		Status:         e.Status,
		DeliveredCount: e.DeliveredCount,
		CreatedAt:      Timestamp(e.CreatedAt),
	}
}

// webhookResponseDTO is the answer half of a webhook that asked a question.
type webhookResponseDTO struct {
	InteractionID string `json:"interaction_id"`
	Status        string `json:"status"`
	// Action is the answer for an approval or a yes/no; Text is the answer to a
	// reply. Exactly one of them is ever set, and both are null until answered.
	Action        *string    `json:"action"`
	Text          *string    `json:"text"`
	CorrelationID *string    `json:"correlation_id"`
	ExpiresAt     Timestamp  `json:"expires_at"`
	RespondedAt   *Timestamp `json:"responded_at"`
}

func newWebhookResponseDTO(in db.Interaction) webhookResponseDTO {
	out := webhookResponseDTO{
		InteractionID: in.ID,
		Status:        in.Status,
		CorrelationID: in.CorrelationID,
		ExpiresAt:     Timestamp(in.ExpiresAt),
		RespondedAt:   TimestampPtr(in.RespondedAt),
	}
	if in.Kind == db.InteractionReply {
		out.Text = in.Response
	} else {
		out.Action = in.Response
	}
	return out
}

type webhookNotifyResponse struct {
	Event webhookEventDTO `json:"event"`
	// Response is present only when the request asked a question.
	Response *webhookResponseDTO `json:"response"`
	Replayed bool                `json:"replayed"`
	Message  *string             `json:"message"`
}

type webhookEventResponse struct {
	Event    webhookEventDTO     `json:"event"`
	Response *webhookResponseDTO `json:"response"`
}

type webhookNotifyRequest struct {
	Body     string  `json:"body"`
	Title    *string `json:"title"`
	ImageURL *string `json:"image_url"`
	URL      *string `json:"url"`
	Priority *string `json:"priority"`
	// DeviceIDs narrows the send. Absent means every device that can receive
	// one.
	DeviceIDs []string `json:"device_ids"`
	// Response turns the notification into a question.
	Response *webhookQuestionRequest `json:"response"`
}

type webhookQuestionRequest struct {
	Kind             string  `json:"kind"`
	ExpiresInSeconds *int    `json:"expires_in_seconds"`
	CorrelationID    *string `json:"correlation_id"`
	// Callback is where the answer is posted when it arrives, so a caller does
	// not have to poll for it.
	Callback *webhookCallbackRequest `json:"callback"`
}

type webhookCallbackRequest struct {
	URL string `json:"url"`
	// Token is presented as a bearer credential on the callback, so the receiver
	// can tell a real answer from anyone who guessed the URL.
	Token string `json:"token"`
}

// webhookPayload is the validated request, and what the idempotency hash covers.
type webhookPayload struct {
	Title            string   `json:"title"`
	Body             string   `json:"body"`
	ImageURL         *string  `json:"image_url"`
	URL              *string  `json:"url"`
	Priority         string   `json:"priority"`
	DeviceIDs        []string `json:"device_ids"`
	Kind             *string  `json:"kind"`
	ExpiresInSeconds int      `json:"expires_in_seconds"`
	CorrelationID    *string  `json:"correlation_id"`
	CallbackURL      *string  `json:"callback_url"`
}

// handleWebhookNotify is the ingest endpoint: one request, one notification.
//
// The credential is in the path because that is what a webhook is — a URL you
// give to something that can only be given a URL. Everything that follows from
// that is deliberate: the token is never written to the access log, every
// authentication failure is the same 404, and rotating the token is one request
// away.
//
// Fields the request omits fall back to the service's defaults, so a sender that
// can only produce a body still produces a notification with a name, an avatar
// and a tap destination.
func (s *server) handleWebhookNotify(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.authenticateWebhook(w, r)
	if !ok {
		return
	}

	var body webhookNotifyRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	var v validator
	payload := webhookPayload{
		Title:     svc.Title,
		Body:      v.text("body", body.Body, 1, maxBodyLen),
		ImageURL:  svc.ImageURL,
		URL:       svc.URL,
		Priority:  svc.Priority,
		DeviceIDs: v.ids("device_ids", body.DeviceIDs),
	}
	if body.Title != nil {
		payload.Title = v.text("title", *body.Title, 1, maxTitleLen)
	}
	if body.ImageURL != nil {
		payload.ImageURL = v.httpsURL("image_url", body.ImageURL)
	}
	if body.URL != nil {
		payload.URL = v.linkURL("url", body.URL)
	}
	if body.Priority != nil {
		payload.Priority = v.enum("priority", body.Priority, db.Priorities, db.PriorityNormal)
	}

	var callbackToken string
	if q := body.Response; q != nil {
		payload.Kind = ptr(v.enum("response.kind", &q.Kind, interactionKinds, ""))
		payload.ExpiresInSeconds = v.intRange("response.expires_in_seconds", q.ExpiresInSeconds,
			minInteractionTTL, maxInteractionTTL, defaultInteractionTTL)
		payload.CorrelationID = v.optionalText("response.correlation_id", q.CorrelationID, maxIDLen)
		if c := q.Callback; c != nil {
			payload.CallbackURL = v.httpsURL("response.callback.url", &c.URL)
			callbackToken = v.text("response.callback.token", c.Token, minCallbackTokenLen, maxCallbackTokenLen)
		}
	}
	if !v.done(w, r) {
		return
	}

	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	hash, err := requestHash(payload)
	if err != nil {
		s.writeInternal(w, r, "hashing a webhook request failed", err)
		return
	}

	req := serviceRequester(svc)
	if key != nil && s.replayWebhook(w, r, svc.ID, *key, hash) {
		return
	}
	if !s.checkQuota(w, r, req) {
		return
	}

	devices, ok := s.selectTargets(w, r, svc.UserID, payload.DeviceIDs)
	if !ok {
		return
	}

	event, err := s.store().Events.Create(r.Context(), db.CreateEventParams{
		ID:             newID(),
		ServiceID:      svc.ID,
		Title:          payload.Title,
		Body:           payload.Body,
		ImageURL:       payload.ImageURL,
		URL:            payload.URL,
		Priority:       payload.Priority,
		Status:         db.EventProcessing,
		IdempotencyKey: key,
		RequestHash:    storedHash(key, hash),
		Now:            s.now(),
	})
	if err != nil {
		if key != nil && db.IsUniqueViolation(err) && s.replayWebhook(w, r, svc.ID, *key, hash) {
			return
		}
		s.writeInternal(w, r, "recording a webhook delivery failed", err)
		return
	}

	question, responseToken, ok := s.createWebhookQuestion(w, r, svc, event, payload, callbackToken)
	if !ok {
		return
	}

	out := webhookNotifyResponse{}
	targets := devices
	if question != nil {
		// A question can only go to a device that knows how to answer one.
		targets = nil
		for _, d := range devices {
			if d.InteractionCapable() {
				targets = append(targets, d)
			}
		}
	}

	result := s.fanOutWebhook(r, svc, event, question, responseToken, payload, targets)
	status := deliveryStatus(len(targets), result.Accepted)

	settled, err := s.store().Events.Settle(detach(r.Context()), event.ID, status, result.Accepted,
		failureSummary(result.Failures))
	if err != nil {
		s.logError(r, "settling a webhook delivery failed", err)
		settled = event
	}
	out.Event = newWebhookEventDTO(*settled)

	if question != nil {
		if updated, err := s.store().Interactions.SettleAccepted(detach(r.Context()), question.ID, result.Accepted); err == nil {
			question = updated
		}
		out.Response = ptr(newWebhookResponseDTO(*question))
	}
	switch status {
	case db.EventNoDevices:
		out.Message = ptr(messageNoDevices)
	case db.EventFailed:
		// The provider's own text stays in the owner-visible log.
		out.Message = ptr(messageNoneAccepted)
	}
	WriteJSON(w, r, http.StatusCreated, out)
}

// createWebhookQuestion creates the interaction a webhook asked for, if it asked
// for one, and returns the plaintext credential the push will carry.
func (s *server) createWebhookQuestion(w http.ResponseWriter, r *http.Request, svc *db.Service, event *db.Event, payload webhookPayload, callbackToken string) (*db.Interaction, string, bool) {
	if payload.Kind == nil {
		return nil, "", true
	}

	var callbackCiphertext *string
	if payload.CallbackURL != nil {
		sealed, err := s.opts.Secrets.Encrypt(secret.PurposeCallbackToken, callbackToken)
		if err != nil {
			s.writeInternal(w, r, "sealing a callback token failed", err)
			return nil, "", false
		}
		callbackCiphertext = &sealed
	}

	interactionID := newID()
	digest, err := requestHash(interactionDigest{
		InteractionID: interactionID,
		Title:         payload.Title,
		Prompt:        payload.Body,
		Kind:          *payload.Kind,
		Choices:       db.ChoicesFor(*payload.Kind),
		URL:           payload.URL,
		Presentation:  db.PresentationNotification,
	})
	if err != nil {
		s.writeInternal(w, r, "computing an interaction digest failed", err)
		return nil, "", false
	}

	responseToken := auth.NewResponseToken()
	now := s.now()
	question, err := s.store().Interactions.Create(r.Context(), db.CreateInteractionParams{
		ID:                      interactionID,
		UserID:                  svc.UserID,
		RequesterServiceID:      &svc.ID,
		EventID:                 &event.ID,
		Title:                   payload.Title,
		Prompt:                  payload.Body,
		Kind:                    *payload.Kind,
		Presentation:            db.PresentationNotification,
		Choices:                 db.ChoicesFor(*payload.Kind),
		URL:                     payload.URL,
		ImageURL:                payload.ImageURL,
		CorrelationID:           payload.CorrelationID,
		ActionDigest:            digest,
		ResponseTokenHash:       ptr(auth.ResponseTokenHash(responseToken)),
		CallbackURL:             payload.CallbackURL,
		CallbackTokenCiphertext: callbackCiphertext,
		ExpiresAt:               now.Add(time.Duration(payload.ExpiresInSeconds) * time.Second),
		Now:                     now,
	})
	if err != nil {
		s.writeInternal(w, r, "recording a webhook question failed", err)
		return nil, "", false
	}
	return question, responseToken, true
}

// fanOutWebhook delivers the notification, as a question when one was asked.
func (s *server) fanOutWebhook(r *http.Request, svc *db.Service, event *db.Event, question *db.Interaction, responseToken string, payload webhookPayload, devices []db.Device) push.AlertResult {
	content := alertContent{
		Title:      payload.Title,
		Body:       payload.Body,
		ImageURL:   payload.ImageURL,
		URL:        payload.URL,
		Priority:   payload.Priority,
		SourceID:   svc.ID,
		SourceName: svc.Title,
		RecordID:   event.ID,
		ThreadKey:  "service-" + svc.ID,
	}
	if question != nil {
		content.RecordID = question.ID
		content.ThreadKey = "interaction-" + question.ID
		content.Interaction = &push.AlertInteraction{
			ID:            question.ID,
			Kind:          question.Kind,
			ActionDigest:  question.ActionDigest,
			ResponseToken: responseToken,
			ExpiresAt:     question.ExpiresAt,
		}
	}
	return s.fanOut(r, content, devices)
}

// replayWebhook answers a repeated delivery.
func (s *server) replayWebhook(w http.ResponseWriter, r *http.Request, serviceID, key, hash string) bool {
	stored, err := s.store().Events.ByIdempotencyKey(r.Context(), serviceID, key)
	switch {
	case errors.Is(err, db.ErrNotFound):
		return false
	case err != nil:
		s.writeInternal(w, r, "checking an Idempotency-Key failed", err)
		return true
	}

	if classifyReplay(stored.RequestHash, hash) == replayConflict {
		s.writeIdempotencyConflict(w, r)
		return true
	}

	out := webhookNotifyResponse{Event: newWebhookEventDTO(*stored), Replayed: true}
	if question, err := s.store().Interactions.ByEventForService(r.Context(), stored.ID, serviceID); err == nil {
		out.Response = ptr(newWebhookResponseDTO(*question))
	}
	WriteJSON(w, r, http.StatusOK, out)
	return true
}

// handleWebhookEvent reports what became of one delivery, and how its question
// was answered.
//
// This is the polling half of the answer contract: a caller that cannot receive
// a callback asks here instead. A question whose deadline has passed is expired
// on read, so a poller sees the state change without any background job.
func (s *server) handleWebhookEvent(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.authenticateWebhook(w, r)
	if !ok {
		return
	}

	event, err := s.store().Events.ByID(r.Context(), r.PathValue("event_id"))
	if err != nil || event.ServiceID != svc.ID {
		s.writeNotFound(w, r, "delivery")
		return
	}

	out := webhookEventResponse{Event: newWebhookEventDTO(*event)}
	question, err := s.store().Interactions.ByEventForService(r.Context(), event.ID, svc.ID)
	if err == nil {
		question = s.expireInteractionIfDue(r, question)
		out.Response = ptr(newWebhookResponseDTO(*question))
	} else if !errors.Is(err, db.ErrNotFound) {
		s.writeInternal(w, r, "loading a webhook question failed", err)
		return
	}
	WriteJSON(w, r, http.StatusOK, out)
}

// handleWebhookCancel withdraws the question a delivery asked.
//
// Unlike the owner's cancel this does not require the question to be unexpired:
// a service withdrawing its own request should succeed even when the deadline
// has just passed and nothing has expired the row yet. The outcome is the same
// for the phone either way — the question is gone.
func (s *server) handleWebhookCancel(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.authenticateWebhook(w, r)
	if !ok {
		return
	}

	canceled, err := s.store().Interactions.CancelForEvent(r.Context(), r.PathValue("event_id"), svc.ID, s.now())
	switch {
	case errors.Is(err, db.ErrNotFound):
		s.writeNotFound(w, r, "question awaiting an answer")
		return
	case err != nil:
		s.writeInternal(w, r, "canceling a webhook question failed", err)
		return
	}
	WriteJSON(w, r, http.StatusOK, interactionReadResponse{Interaction: newInteractionDTO(*canceled)})
}

// The Live Activity half of the webhook surface. It is the same code as the
// token surface with a different requester, which is the point: an integration
// that can only hold a URL drives an activity exactly the way an agent does.

func (s *server) handleWebhookStartActivity(w http.ResponseWriter, r *http.Request) {
	if svc, ok := s.authenticateWebhook(w, r); ok {
		s.startActivity(w, r, serviceRequester(svc))
	}
}

func (s *server) handleWebhookUpdateActivity(w http.ResponseWriter, r *http.Request) {
	if svc, ok := s.authenticateWebhook(w, r); ok {
		s.updateActivity(w, r, serviceRequester(svc))
	}
}

func (s *server) handleWebhookEndActivity(w http.ResponseWriter, r *http.Request) {
	if svc, ok := s.authenticateWebhook(w, r); ok {
		s.endActivity(w, r, serviceRequester(svc))
	}
}

// handleWebhookGetActivity returns one of the service's own activities.
func (s *server) handleWebhookGetActivity(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.authenticateWebhook(w, r)
	if !ok {
		return
	}

	act, err := s.store().Activities.Resolve(r.Context(), db.ResolveParams{
		Identifier:         r.PathValue("identifier"),
		RequesterServiceID: &svc.ID,
	})
	if err != nil {
		s.writeStoreError(w, r, "Live Activity", err)
		return
	}
	act = s.expireActivityIfDue(r.Context(), act)
	WriteJSON(w, r, http.StatusOK, activityReadResponse{Activity: newActivityDTO(*act)})
}

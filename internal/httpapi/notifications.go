package httpapi

import (
	"errors"
	"net/http"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
)

// notificationDTO is one agent push as every response renders it.
type notificationDTO struct {
	ID       string  `json:"id"`
	Title    string  `json:"title"`
	Body     string  `json:"body"`
	ImageURL *string `json:"image_url"`
	URL      *string `json:"url"`
	Priority string  `json:"priority"`
	// AcceptedCount is how many messages APNs took. It is not proof that a phone
	// showed anything, and a caller that treats it as delivery confirmation will
	// be wrong on exactly the days it matters.
	AcceptedCount int       `json:"accepted_count"`
	CreatedAt     Timestamp `json:"created_at"`
}

func newNotificationDTO(n db.AgentNotification) notificationDTO {
	return notificationDTO{
		ID:            n.ID,
		Title:         n.Title,
		Body:          n.Body,
		ImageURL:      n.ImageURL,
		URL:           n.URL,
		Priority:      n.Priority,
		AcceptedCount: n.AcceptedCount,
		CreatedAt:     Timestamp(n.CreatedAt),
	}
}

type notificationResponse struct {
	Notification notificationDTO `json:"notification"`
	// Replayed is true when an Idempotency-Key matched an earlier request and
	// nothing new was sent.
	Replayed bool `json:"replayed"`
	// Message explains an outcome a caller would otherwise have to infer from a
	// zero count.
	Message *string `json:"message"`
}

type sendNotificationRequest struct {
	Body     string  `json:"body"`
	Title    *string `json:"title"`
	ImageURL *string `json:"image_url"`
	URL      *string `json:"url"`
	Priority *string `json:"priority"`
	// DeviceIDs narrows the send to specific phones. Absent means every device
	// that can receive one.
	DeviceIDs []string `json:"device_ids"`
}

// notificationPayload is the validated request, and the thing hashed for
// idempotency. It is a separate type from the request so that the hash covers
// defaulted, trimmed, sorted values — the request as the server understood it,
// not as it happened to be spelled.
type notificationPayload struct {
	Title     string   `json:"title"`
	Body      string   `json:"body"`
	ImageURL  *string  `json:"image_url"`
	URL       *string  `json:"url"`
	Priority  string   `json:"priority"`
	DeviceIDs []string `json:"device_ids"`
}

// defaultNotificationTitle is the sender name an agent gets when it does not
// pick one.
const defaultNotificationTitle = "Hark"

// Messages that explain a zero count. They are the difference between "your
// account has no phones" and "your phones did not answer", which are different
// problems with different fixes.
const (
	messageNoDevices    = "No active device is registered for this account."
	messageNoneAccepted = "No message was accepted by APNs."
)

// handleSendNotification sends a one-shot push.
//
// The row is written before anything is sent, on purpose: a duplicate that
// arrives while the first is still in flight loses the unique index on the
// idempotency key, re-reads, and replays — rather than pushing a second copy to
// somebody's Lock Screen.
func (s *server) handleSendNotification(w http.ResponseWriter, r *http.Request) {
	var body sendNotificationRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	var v validator
	payload := notificationPayload{
		Body:      v.text("body", body.Body, 1, maxBodyLen),
		ImageURL:  v.httpsURL("image_url", body.ImageURL),
		URL:       v.linkURL("url", body.URL),
		Priority:  v.enum("priority", body.Priority, db.Priorities, db.PriorityNormal),
		DeviceIDs: v.ids("device_ids", body.DeviceIDs),
	}
	payload.Title = defaultNotificationTitle
	if body.Title != nil {
		payload.Title = v.text("title", *body.Title, 1, maxTitleLen)
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
		s.writeInternal(w, r, "hashing a notification request failed", err)
		return
	}

	principal := auth.PrincipalFrom(r.Context())
	req := tokenRequester(principal)

	if key != nil && s.replayNotification(w, r, *req.TokenID, *key, hash) {
		return
	}
	if !s.checkQuota(w, r, req) {
		return
	}

	devices, ok := s.selectTargets(w, r, req.UserID, payload.DeviceIDs)
	if !ok {
		return
	}

	notification, err := s.store().Notifications.Create(r.Context(), db.CreateNotificationParams{
		ID:               newID(),
		UserID:           req.UserID,
		RequesterTokenID: *req.TokenID,
		Title:            payload.Title,
		Body:             payload.Body,
		ImageURL:         payload.ImageURL,
		URL:              payload.URL,
		Priority:         payload.Priority,
		IdempotencyKey:   key,
		RequestHash:      storedHash(key, hash),
		Now:              s.now(),
	})
	if err != nil {
		// Losing the unique index means a duplicate got there first; its outcome
		// is the answer to this request too.
		if key != nil && db.IsUniqueViolation(err) && s.replayNotification(w, r, *req.TokenID, *key, hash) {
			return
		}
		s.writeInternal(w, r, "recording a notification failed", err)
		return
	}

	if len(devices) == 0 {
		settled := s.settleNotification(r, notification, db.EventNoDevices, 0)
		WriteJSON(w, r, http.StatusCreated, notificationResponse{
			Notification: newNotificationDTO(*settled),
			Message:      ptr(messageNoDevices),
		})
		return
	}

	result := s.fanOut(r, alertContent{
		Title:      payload.Title,
		Body:       payload.Body,
		ImageURL:   payload.ImageURL,
		URL:        payload.URL,
		Priority:   payload.Priority,
		SourceID:   *req.TokenID,
		SourceName: req.Name,
		RecordID:   notification.ID,
		ThreadKey:  threadKey(*req.TokenID, payload.Title),
	}, devices)

	settled := s.settleNotification(r, notification, deliveryStatus(len(devices), result.Accepted), result.Accepted)

	out := notificationResponse{Notification: newNotificationDTO(*settled)}
	if result.Accepted == 0 {
		// Provider text remains in the owner-only delivery log because it may
		// include a device token. API-token responses use a safe summary.
		out.Message = ptr(messageNoneAccepted)
	}
	WriteJSON(w, r, http.StatusCreated, out)
}

// settleNotification writes the outcome of a send and returns the row to render.
//
// It uses a detached context so client cancellation after the push does not
// prevent recording the result. Settlement failures are logged, and the caller
// still receives the in-memory result.
func (s *server) settleNotification(r *http.Request, n *db.AgentNotification, status string, accepted int) *db.AgentNotification {
	settled, err := s.store().Notifications.Settle(detach(r.Context()), n.ID, status, accepted)
	if err != nil {
		LoggerFrom(r.Context()).ErrorContext(r.Context(), "settling a notification failed",
			"notification_id", n.ID, "error", err)
		return n
	}
	return settled
}

// replayNotification answers a repeated request, reporting whether it answered.
func (s *server) replayNotification(w http.ResponseWriter, r *http.Request, tokenID, key, hash string) bool {
	stored, err := s.store().Notifications.ByIdempotencyKey(r.Context(), tokenID, key)
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
	WriteJSON(w, r, http.StatusOK, notificationResponse{
		Notification: newNotificationDTO(*stored),
		Replayed:     true,
	})
	return true
}

// storedHash returns the fingerprint to write alongside a key, or nil when the
// request carried none: a hash with no key would compare against nothing.
func storedHash(key *string, hash string) *string {
	if key == nil {
		return nil
	}
	return &hash
}

package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
)

const (
	// safetyCoalesceWindow suppresses repeated active reports.
	safetyCoalesceWindow = 5 * time.Minute
	// safetyHourlyCap limits pushes from one source in safetyCapWindow.
	safetyHourlyCap = 10
	safetyCapWindow = time.Hour
	// safetyTestInterval limits setup tests for each source.
	safetyTestInterval = 10 * time.Minute
)

// Messages returned with reports recorded without a push.
const (
	messageSafetyCoalesced   = "An alert for this source was sent in the last 5 minutes. The event was recorded without another push."
	messageSafetyRateLimited = "This source reached its hourly alert limit. The event was recorded without a push."
)

// safetySourceDTO is a configured alert source.
type safetySourceDTO struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	ImageURL        *string   `json:"image_url"`
	URL             *string   `json:"url"`
	CriticalEnabled bool      `json:"critical_enabled"`
	CreatedAt       Timestamp `json:"created_at"`
	UpdatedAt       Timestamp `json:"updated_at"`
}

func newSafetySourceDTO(src db.SafetySource) safetySourceDTO {
	return safetySourceDTO{
		ID:              src.ID,
		Name:            src.Name,
		ImageURL:        src.ImageURL,
		URL:             src.URL,
		CriticalEnabled: src.CriticalEnabled,
		CreatedAt:       Timestamp(src.CreatedAt),
		UpdatedAt:       Timestamp(src.UpdatedAt),
	}
}

type safetySourceListResponse struct {
	Sources []safetySourceDTO `json:"sources"`
}

type safetySourceResponse struct {
	Source safetySourceDTO `json:"source"`
}

// safetyEventDTO omits provider errors, which remain in owner-only history.
type safetyEventDTO struct {
	ID             string    `json:"id"`
	SourceID       string    `json:"source_id"`
	SourceName     string    `json:"source_name"`
	State          string    `json:"state"`
	Title          string    `json:"title"`
	Body           string    `json:"body"`
	Priority       string    `json:"priority"`
	Status         string    `json:"status"`
	DeliveredCount int       `json:"delivered_count"`
	CreatedAt      Timestamp `json:"created_at"`
}

func newSafetyEventDTO(e db.SafetyEvent, sourceName string) safetyEventDTO {
	return safetyEventDTO{
		ID:             e.ID,
		SourceID:       e.SourceID,
		SourceName:     sourceName,
		State:          e.State,
		Title:          e.Title,
		Body:           e.Body,
		Priority:       e.Priority,
		Status:         e.Status,
		DeliveredCount: e.DeliveredCount,
		CreatedAt:      Timestamp(e.CreatedAt),
	}
}

type safetyEventResponse struct {
	Event safetyEventDTO `json:"event"`
	// Replayed is true when an Idempotency-Key matched an earlier request and
	// nothing new was sent.
	Replayed bool `json:"replayed"`
	// Message explains an outcome a caller would otherwise have to infer from
	// the status.
	Message *string `json:"message"`
}

type safetyTestResponse struct {
	Event safetyEventDTO `json:"event"`
}

type safetySettingsResponse struct {
	CriticalAlertsEnabled bool `json:"critical_alerts_enabled"`
}

// handleListSafetySources returns configured alert sources, newest first.
func (s *server) handleListSafetySources(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFrom(r.Context())

	sources, err := s.store().SafetySources.ListForUser(r.Context(), principal.UserID())
	if err != nil {
		s.writeInternal(w, r, "listing safety sources failed", err)
		return
	}

	out := make([]safetySourceDTO, 0, len(sources))
	for _, src := range sources {
		out = append(out, newSafetySourceDTO(src))
	}
	WriteJSON(w, r, http.StatusOK, safetySourceListResponse{Sources: out})
}

// handleGetSafetySource returns one source.
func (s *server) handleGetSafetySource(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFrom(r.Context())

	src, err := s.store().SafetySources.ByID(r.Context(), r.PathValue("id"), principal.UserID())
	if err != nil {
		s.writeStoreError(w, r, "alert source", err)
		return
	}
	WriteJSON(w, r, http.StatusOK, safetySourceResponse{Source: newSafetySourceDTO(*src)})
}

type createSafetySourceRequest struct {
	Name            string  `json:"name"`
	ImageURL        *string `json:"image_url"`
	URL             *string `json:"url"`
	CriticalEnabled *bool   `json:"critical_enabled"`
}

// handleCreateSafetySource creates a Critical Alert source. Critical delivery
// starts on unless the owner explicitly switches it off in the request.
func (s *server) handleCreateSafetySource(w http.ResponseWriter, r *http.Request) {
	var body createSafetySourceRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	var v validator
	name := v.text("name", body.Name, 1, maxNameLen)
	imageURL := v.httpsURL("image_url", body.ImageURL)
	linkURL := v.linkURL("url", body.URL)
	if !v.done(w, r) {
		return
	}
	criticalEnabled := true
	if body.CriticalEnabled != nil {
		criticalEnabled = *body.CriticalEnabled
	}

	principal := auth.PrincipalFrom(r.Context())
	src, err := s.store().SafetySources.Create(r.Context(), db.CreateSafetySourceParams{
		ID:              newID(),
		UserID:          principal.UserID(),
		Name:            name,
		ImageURL:        imageURL,
		URL:             linkURL,
		CriticalEnabled: criticalEnabled,
		Now:             s.now(),
	})
	if err != nil {
		s.writeInternal(w, r, "creating a safety source failed", err)
		return
	}
	WriteJSON(w, r, http.StatusCreated, safetySourceResponse{Source: newSafetySourceDTO(*src)})
}

type updateSafetySourceRequest struct {
	Name            optional[string]  `json:"name"`
	ImageURL        optional[*string] `json:"image_url"`
	URL             optional[*string] `json:"url"`
	CriticalEnabled optional[bool]    `json:"critical_enabled"`
}

// handleUpdateSafetySource changes a source's editable fields.
func (s *server) handleUpdateSafetySource(w http.ResponseWriter, r *http.Request) {
	var body updateSafetySourceRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	var v validator
	params := db.UpdateSafetySourceParams{
		ID:     r.PathValue("id"),
		UserID: auth.PrincipalFrom(r.Context()).UserID(),
		Now:    s.now(),
	}
	if name, ok := body.Name.Get(); ok {
		params.Name = db.Value(v.text("name", name, 1, maxNameLen))
	}
	if image, ok := body.ImageURL.Get(); ok {
		params.ImageURL = db.Value(v.httpsURL("image_url", image))
	}
	if link, ok := body.URL.Get(); ok {
		params.URL = db.Value(v.linkURL("url", link))
	}
	if critical, ok := body.CriticalEnabled.Get(); ok {
		params.CriticalEnabled = db.Value(critical)
	}
	if !params.Name.IsSet() && !params.ImageURL.IsSet() && !params.URL.IsSet() && !params.CriticalEnabled.IsSet() {
		v.add("name", "at least one of name, image_url, url or critical_enabled is required")
	}
	if !v.done(w, r) {
		return
	}

	src, err := s.store().SafetySources.Update(r.Context(), params)
	if err != nil {
		s.writeStoreError(w, r, "alert source", err)
		return
	}
	WriteJSON(w, r, http.StatusOK, safetySourceResponse{Source: newSafetySourceDTO(*src)})
}

// handleDeleteSafetySource removes a source and its event history.
func (s *server) handleDeleteSafetySource(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFrom(r.Context())

	deleted, err := s.store().SafetySources.Delete(r.Context(), r.PathValue("id"), principal.UserID())
	switch {
	case err != nil:
		s.writeInternal(w, r, "deleting a safety source failed", err)
	case !deleted:
		s.writeNotFound(w, r, "alert source")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleGetSafetySettings returns the account-wide critical delivery toggle.
func (s *server) handleGetSafetySettings(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFrom(r.Context())

	user, err := s.store().Users.ByID(r.Context(), principal.UserID())
	if err != nil {
		s.writeInternal(w, r, "loading the safety settings failed", err)
		return
	}
	WriteJSON(w, r, http.StatusOK, safetySettingsResponse{CriticalAlertsEnabled: user.CriticalAlertsEnabled})
}

type updateSafetySettingsRequest struct {
	CriticalAlertsEnabled *bool `json:"critical_alerts_enabled"`
}

// handleUpdateSafetySettings writes the account-wide toggle.
func (s *server) handleUpdateSafetySettings(w http.ResponseWriter, r *http.Request) {
	var body updateSafetySettingsRequest
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
	if err := s.store().Users.SetCriticalAlertsEnabled(r.Context(),
		principal.UserID(), *body.CriticalAlertsEnabled, s.now()); err != nil {
		s.writeInternal(w, r, "saving the safety settings failed", err)
		return
	}
	WriteJSON(w, r, http.StatusOK, safetySettingsResponse{CriticalAlertsEnabled: *body.CriticalAlertsEnabled})
}

type reportSafetyEventRequest struct {
	SourceID string `json:"source_id"`
	State    string `json:"state"`
}

// safetyEventPayload is the normalized request used for idempotency hashing.
type safetyEventPayload struct {
	SourceID string `json:"source_id"`
	State    string `json:"state"`
}

// handleReportSafetyEvent records an event and sends its alert. The source row
// lock serializes coalescing and hourly-limit checks.
func (s *server) handleReportSafetyEvent(w http.ResponseWriter, r *http.Request) {
	var body reportSafetyEventRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	var v validator
	payload := safetyEventPayload{
		SourceID: v.text("source_id", body.SourceID, 1, maxIDLen),
		State:    v.enum("state", &body.State, db.SafetyReportStates, ""),
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
		s.writeInternal(w, r, "hashing a safety report failed", err)
		return
	}

	principal := auth.PrincipalFrom(r.Context())
	req := tokenRequester(principal)

	if key != nil && s.replaySafetyEvent(w, r, *req.TokenID, *key, hash) {
		return
	}
	if !s.checkQuota(w, r, req) {
		return
	}

	user, err := s.store().Users.ByID(r.Context(), req.UserID)
	if err != nil {
		s.writeInternal(w, r, "loading the safety settings failed", err)
		return
	}

	var (
		src   *db.SafetySource
		event *db.SafetyEvent
	)
	err = s.store().Tx(r.Context(), func(ctx context.Context, store *db.Store) error {
		src, err = store.SafetySources.ByIDForUpdate(ctx, payload.SourceID, req.UserID)
		if err != nil {
			return err
		}

		now := s.now()
		status := db.EventProcessing
		if payload.State == db.SafetyStateActive {
			recent, err := store.SafetyEvents.CountPushedForSourceStateSince(ctx,
				src.ID, db.SafetyStateActive, now.Add(-safetyCoalesceWindow))
			if err != nil {
				return err
			}
			if recent > 0 {
				status = db.SafetyCoalesced
			}
		}
		if status == db.EventProcessing {
			pushed, err := store.SafetyEvents.CountPushedForSourceSince(ctx,
				src.ID, now.Add(-safetyCapWindow))
			if err != nil {
				return err
			}
			if pushed >= safetyHourlyCap {
				status = db.SafetyRateLimited
			}
		}

		title, alertBody := db.SafetyAlertContent(src.Name, payload.State)
		event, err = store.SafetyEvents.Create(ctx, db.CreateSafetyEventParams{
			ID:               newID(),
			SourceID:         src.ID,
			RequesterTokenID: req.TokenID,
			State:            payload.State,
			Title:            title,
			Body:             alertBody,
			Priority:         db.SafetyAlertPriority(payload.State, user.CriticalAlertsEnabled, src.CriticalEnabled),
			Status:           status,
			IdempotencyKey:   key,
			RequestHash:      storedHash(key, hash),
			Now:              now,
		})
		return err
	})
	switch {
	case errors.Is(err, db.ErrNotFound):
		WriteFieldErrors(w, r, "The request body is invalid.", []FieldError{{
			Field:   "source_id",
			Message: "names an alert source that is not configured on this account",
		}})
		return
	case err != nil:
		// Losing the unique index means a duplicate got there first; its outcome
		// is the answer to this request too.
		if key != nil && db.IsUniqueViolation(err) && s.replaySafetyEvent(w, r, *req.TokenID, *key, hash) {
			return
		}
		s.writeInternal(w, r, "recording a safety event failed", err)
		return
	}

	// Suppressed reports are recorded and returned without a push.
	switch event.Status {
	case db.SafetyCoalesced:
		WriteJSON(w, r, http.StatusCreated, safetyEventResponse{
			Event:   newSafetyEventDTO(*event, src.Name),
			Message: ptr(messageSafetyCoalesced),
		})
		return
	case db.SafetyRateLimited:
		WriteJSON(w, r, http.StatusCreated, safetyEventResponse{
			Event:   newSafetyEventDTO(*event, src.Name),
			Message: ptr(messageSafetyRateLimited),
		})
		return
	}

	devices, ok := s.selectTargets(w, r, req.UserID, nil)
	if !ok {
		return
	}
	if len(devices) == 0 {
		settled := s.settleSafetyEvent(r, event, db.EventNoDevices, 0, nil)
		WriteJSON(w, r, http.StatusCreated, safetyEventResponse{
			Event:   newSafetyEventDTO(*settled, src.Name),
			Message: ptr(messageNoDevices),
		})
		return
	}

	result := s.fanOut(r, alertContent{
		Title:      event.Title,
		Body:       event.Body,
		ImageURL:   src.ImageURL,
		URL:        src.URL,
		Priority:   event.Priority,
		SourceID:   src.ID,
		SourceName: src.Name,
		RecordID:   event.ID,
		ThreadKey:  "safety-" + src.ID,
	}, devices)

	settled := s.settleSafetyEvent(r, event,
		deliveryStatus(len(devices), result.Accepted), result.Accepted, failureSummary(result.Failures))

	out := safetyEventResponse{Event: newSafetyEventDTO(*settled, src.Name)}
	if result.Accepted == 0 {
		out.Message = ptr(messageNoneAccepted)
	}
	WriteJSON(w, r, http.StatusCreated, out)
}

// handleSafetySourceTest sends and records a setup test.
func (s *server) handleSafetySourceTest(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFrom(r.Context())

	user, err := s.store().Users.ByID(r.Context(), principal.UserID())
	if err != nil {
		s.writeInternal(w, r, "loading the safety settings failed", err)
		return
	}

	var (
		src       *db.SafetySource
		event     *db.SafetyEvent
		throttled bool
	)
	err = s.store().Tx(r.Context(), func(ctx context.Context, store *db.Store) error {
		src, err = store.SafetySources.ByIDForUpdate(ctx, r.PathValue("id"), principal.UserID())
		if err != nil {
			return err
		}

		now := s.now()
		recent, err := store.SafetyEvents.CountPushedForSourceStateSince(ctx,
			src.ID, db.SafetyStateTest, now.Add(-safetyTestInterval))
		if err != nil {
			return err
		}
		if recent > 0 {
			throttled = true
			return nil
		}

		title, body := db.SafetyAlertContent(src.Name, db.SafetyStateTest)
		event, err = store.SafetyEvents.Create(ctx, db.CreateSafetyEventParams{
			ID:       newID(),
			SourceID: src.ID,
			State:    db.SafetyStateTest,
			Title:    title,
			Body:     body,
			Priority: db.SafetyAlertPriority(db.SafetyStateTest, user.CriticalAlertsEnabled, src.CriticalEnabled),
			Status:   db.EventProcessing,
			Now:      now,
		})
		return err
	})
	if err != nil {
		s.writeStoreError(w, r, "alert source", err)
		return
	}
	if throttled {
		writeRetryAfter(w, safetyTestInterval)
		WriteError(w, r, http.StatusTooManyRequests, CodeRateLimited,
			"A test was sent for this source in the last 10 minutes.")
		return
	}

	devices, ok := s.selectTargets(w, r, principal.UserID(), nil)
	if !ok {
		return
	}
	if len(devices) == 0 {
		settled := s.settleSafetyEvent(r, event, db.EventNoDevices, 0, nil)
		WriteJSON(w, r, http.StatusCreated, safetyTestResponse{Event: newSafetyEventDTO(*settled, src.Name)})
		return
	}

	result := s.fanOut(r, alertContent{
		Title:      event.Title,
		Body:       event.Body,
		ImageURL:   src.ImageURL,
		URL:        src.URL,
		Priority:   event.Priority,
		SourceID:   src.ID,
		SourceName: src.Name,
		RecordID:   event.ID,
		ThreadKey:  "safety-" + src.ID,
	}, devices)

	settled := s.settleSafetyEvent(r, event,
		deliveryStatus(len(devices), result.Accepted), result.Accepted, failureSummary(result.Failures))
	WriteJSON(w, r, http.StatusCreated, safetyTestResponse{Event: newSafetyEventDTO(*settled, src.Name)})
}

// settleSafetyEvent records the outcome on a detached context.
func (s *server) settleSafetyEvent(r *http.Request, e *db.SafetyEvent, status string, delivered int, failure *string) *db.SafetyEvent {
	settled, err := s.store().SafetyEvents.Settle(detach(r.Context()), e.ID, status, delivered, failure)
	if err != nil {
		LoggerFrom(r.Context()).ErrorContext(r.Context(), "settling a safety event failed",
			"safety_event_id", e.ID, "error", err)
		return e
	}
	return settled
}

// replaySafetyEvent returns the stored result for an idempotent retry.
func (s *server) replaySafetyEvent(w http.ResponseWriter, r *http.Request, tokenID, key, hash string) bool {
	stored, err := s.store().SafetyEvents.ByIdempotencyKey(r.Context(), tokenID, key)
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

	principal := auth.PrincipalFrom(r.Context())
	src, err := s.store().SafetySources.ByID(r.Context(), stored.SourceID, principal.UserID())
	if err != nil {
		s.writeInternal(w, r, "loading the safety source failed", err)
		return true
	}
	WriteJSON(w, r, http.StatusOK, safetyEventResponse{
		Event:    newSafetyEventDTO(*stored, src.Name),
		Replayed: true,
	})
	return true
}

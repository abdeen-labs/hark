package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
)

// Live Activity durations, in seconds.
//
// The ceiling is Apple's: an activity may run for eight hours, after which iOS
// removes it whatever the server thinks. Expiry is therefore not a policy Hark
// invented but the truth about the thing being modelled, and an activity that
// has passed it is expired on the next read rather than left claiming a Lock
// Screen it no longer occupies.
const (
	minActivityTTL    = 60
	maxActivityTTL    = 8 * 60 * 60
	defaultStaleAfter = 4 * 60 * 60
	maxDismissAfter   = 4 * 60 * 60
	defaultEndStatus  = "Complete"
)

// activityDTO is a Live Activity as every response renders it.
type activityDTO struct {
	ID string `json:"id"`
	// Key is the requester's own handle for the activity, free again once this
	// one ends.
	Key    *string `json:"key"`
	Status string  `json:"status"`
	// Sequence is the optimistic-concurrency token: send it back as
	// if_sequence to make a change conditional on nothing else having happened.
	Sequence int `json:"sequence"`
	// State is the content-state document the phone renders.
	State json.RawMessage `json:"state"`
	// AcceptedCount and FailedCount describe the most recent operation, not the
	// activity's lifetime.
	AcceptedCount int        `json:"accepted_count"`
	FailedCount   int        `json:"failed_count"`
	ExpiresAt     Timestamp  `json:"expires_at"`
	StaleAt       *Timestamp `json:"stale_at"`
	CreatedAt     Timestamp  `json:"created_at"`
	UpdatedAt     Timestamp  `json:"updated_at"`
	EndedAt       *Timestamp `json:"ended_at"`
}

func newActivityDTO(a db.LiveActivity) activityDTO {
	return activityDTO{
		ID:            a.ID,
		Key:           a.Key,
		Status:        a.Status,
		Sequence:      a.Sequence,
		State:         json.RawMessage(a.Props),
		AcceptedCount: a.AcceptedCount,
		FailedCount:   a.FailedCount,
		ExpiresAt:     Timestamp(a.ExpiresAt),
		StaleAt:       TimestampPtr(a.StaleAt),
		CreatedAt:     Timestamp(a.CreatedAt),
		UpdatedAt:     Timestamp(a.UpdatedAt),
		EndedAt:       TimestampPtr(a.EndedAt),
	}
}

// activityListItemDTO adds the sender, which a list shows and a single read does
// not need: whoever asked for one activity by id already knows who started it.
type activityListItemDTO struct {
	activityDTO
	SourceName     string  `json:"source_name"`
	SourceImageURL *string `json:"source_image_url"`
}

type activityListResponse struct {
	Activities []activityListItemDTO `json:"activities"`
	NextCursor *string               `json:"next_cursor"`
}

// activityResponse is the envelope every write returns, on both the token and
// the webhook surface. One shape, so a client that learns it once can drive an
// activity from either.
type activityResponse struct {
	Activity activityDTO `json:"activity"`
	// Accepted and Failed count the devices this request reached.
	Accepted int `json:"accepted"`
	Failed   int `json:"failed"`
	// Replaced is present only when the request asked to replace, and counts the
	// activities that were ended to make room.
	Replaced *int `json:"replaced,omitempty"`
	// Replayed is true when an Idempotency-Key matched an earlier request.
	Replayed bool `json:"replayed"`
	// Message names the distinct reasons a device was not reached.
	Message *string `json:"message"`
}

type activityReadResponse struct {
	Activity activityDTO `json:"activity"`
}

// handleListActivities pages the account's activities, newest first.
//
// `status=live` is the dashboard's question — what is on a Lock Screen right
// now — and the default, because a list of finished activities is history and
// history has its own endpoint.
func (s *server) handleListActivities(w http.ResponseWriter, r *http.Request) {
	query, ok := s.parseList(w, r)
	if !ok {
		return
	}
	liveOnly, ok := s.parseStatusFilter(w, r, "live")
	if !ok {
		return
	}

	principal := auth.PrincipalFrom(r.Context())
	page, err := s.store().Activities.List(r.Context(), db.ListActivitiesParams{
		UserID:   principal.UserID(),
		LiveOnly: liveOnly,
		Now:      s.now(),
		Cursor:   query.Cursor,
		Limit:    query.Limit,
	})
	if err != nil {
		s.writeInternal(w, r, "listing Live Activities failed", err)
		return
	}

	out := make([]activityListItemDTO, 0, len(page.Items))
	for _, item := range page.Items {
		out = append(out, activityListItemDTO{
			activityDTO:    newActivityDTO(item.LiveActivity),
			SourceName:     item.SourceName,
			SourceImageURL: item.SourceImageURL,
		})
	}
	WriteJSON(w, r, http.StatusOK, activityListResponse{Activities: out, NextCursor: nextCursor(page)})
}

// parseStatusFilter reads the shared `status=live|all` query parameter.
func (s *server) parseStatusFilter(w http.ResponseWriter, r *http.Request, fallback string) (bool, bool) {
	switch status := r.URL.Query().Get("status"); status {
	case "":
		return fallback == "live", true
	case "live":
		return true, true
	case "all":
		return false, true
	default:
		WriteFieldErrors(w, r, "The request query is invalid.", []FieldError{{
			Field:   "status",
			Message: "must be live or all",
		}})
		return false, false
	}
}

// handleGetActivity returns one activity from the account's point of view.
func (s *server) handleGetActivity(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFrom(r.Context())

	act, err := s.store().Activities.ResolveForUser(r.Context(), r.PathValue("identifier"), principal.UserID())
	if err != nil {
		s.writeStoreError(w, r, "Live Activity", err)
		return
	}
	act = s.expireActivityIfDue(r.Context(), act)
	WriteJSON(w, r, http.StatusOK, activityReadResponse{Activity: newActivityDTO(*act)})
}

// handleStartActivity starts an activity for an API token.
func (s *server) handleStartActivity(w http.ResponseWriter, r *http.Request) {
	s.startActivity(w, r, tokenRequester(auth.PrincipalFrom(r.Context())))
}

// handleUpdateActivity applies a change for an API token.
func (s *server) handleUpdateActivity(w http.ResponseWriter, r *http.Request) {
	s.updateActivity(w, r, tokenRequester(auth.PrincipalFrom(r.Context())))
}

// handleEndActivity finishes an activity for an API token.
func (s *server) handleEndActivity(w http.ResponseWriter, r *http.Request) {
	s.endActivity(w, r, tokenRequester(auth.PrincipalFrom(r.Context())))
}

type startActivityRequest struct {
	Key *string `json:"key"`
	// Replace takes over the device slot, or the key, that another activity
	// holds instead of refusing.
	Replace           *bool    `json:"replace"`
	Title             string   `json:"title"`
	Status            string   `json:"status"`
	Detail            *string  `json:"detail"`
	Progress          *float64 `json:"progress"`
	Symbol            *string  `json:"symbol"`
	PrivacyMode       *string  `json:"privacy_mode"`
	AccentColor       *string  `json:"accent_color"`
	Style             *string  `json:"style"`
	DeviceIDs         []string `json:"device_ids"`
	ExpiresInSeconds  *int     `json:"expires_in_seconds"`
	StaleAfterSeconds *int     `json:"stale_after_seconds"`
}

// activityStartPayload is the validated request, and what the idempotency hash
// covers.
type activityStartPayload struct {
	Key               *string  `json:"key"`
	Replace           bool     `json:"replace"`
	Title             string   `json:"title"`
	Status            string   `json:"status"`
	Detail            *string  `json:"detail"`
	Progress          *float64 `json:"progress"`
	Symbol            string   `json:"symbol"`
	PrivacyMode       string   `json:"privacy_mode"`
	AccentColor       string   `json:"accent_color"`
	Style             string   `json:"style"`
	DeviceIDs         []string `json:"device_ids"`
	ExpiresInSeconds  int      `json:"expires_in_seconds"`
	StaleAfterSeconds int      `json:"stale_after_seconds"`
}

// startActivity creates an activity and puts it on every capable device.
//
// A phone shows at most one ordinary activity at a time — that is an iOS
// constraint, enforced here by a partial unique index rather than discovered
// later as a silent failure — so a start either takes the slot, refuses, or is
// told to replace whatever holds it.
func (s *server) startActivity(w http.ResponseWriter, r *http.Request, req requester) {
	var body startActivityRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	var v validator
	payload := activityStartPayload{
		Key:               v.optionalText("key", body.Key, maxKeyLen),
		Replace:           body.Replace != nil && *body.Replace,
		Title:             v.text("title", body.Title, 1, maxTitleLen),
		Status:            v.text("status", body.Status, 1, maxStatusLen),
		Detail:            v.optionalText("detail", body.Detail, maxDetailLen),
		Progress:          v.fraction("progress", body.Progress),
		Symbol:            v.enum("symbol", body.Symbol, activitySymbols, symbolTerminal),
		PrivacyMode:       v.enum("privacy_mode", body.PrivacyMode, activityPrivacyModes, privacyStandard),
		AccentColor:       defaultAccentColor,
		Style:             v.enum("style", body.Style, activityStyles, styleStandard),
		DeviceIDs:         v.ids("device_ids", body.DeviceIDs),
		ExpiresInSeconds:  v.intRange("expires_in_seconds", body.ExpiresInSeconds, minActivityTTL, maxActivityTTL, maxActivityTTL),
		StaleAfterSeconds: v.intRange("stale_after_seconds", body.StaleAfterSeconds, 0, maxActivityTTL, defaultStaleAfter),
	}
	if colour := v.accentColor("accent_color", body.AccentColor); colour != nil {
		payload.AccentColor = *colour
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
		s.writeInternal(w, r, "hashing a Live Activity request failed", err)
		return
	}
	if key != nil && s.replayActivityStart(w, r, req, *key, hash) {
		return
	}
	if !s.checkQuota(w, r, req) {
		return
	}

	devices, ok := s.selectTargets(w, r, req.UserID, payload.DeviceIDs)
	if !ok {
		return
	}
	capable := make([]db.Device, 0, len(devices))
	for _, d := range devices {
		if d.LiveActivityCapable() {
			capable = append(capable, d)
		}
	}

	blocking, ok := s.clearTheWay(w, r, req, capable, payload)
	if !ok {
		return
	}

	activityID := newID()
	state, err := encodeActivityState(activityState{
		ActivityID:  activityID,
		Title:       payload.Title,
		Status:      payload.Status,
		Detail:      payload.Detail,
		Progress:    payload.Progress,
		UpdatedAt:   Timestamp(s.now()),
		Symbol:      payload.Symbol,
		PrivacyMode: payload.PrivacyMode,
		AccentColor: payload.AccentColor,
		Style:       payload.Style,
	})
	if err != nil {
		s.writeInternal(w, r, "building a Live Activity state failed", err)
		return
	}

	now := s.now()
	expiresAt := now.Add(time.Duration(payload.ExpiresInSeconds) * time.Second)
	staleAt := earliest(now.Add(time.Duration(payload.StaleAfterSeconds)*time.Second), expiresAt)

	targets := make([]db.ActivityTarget, 0, len(capable))
	for _, d := range capable {
		targets = append(targets, db.ActivityTarget{
			DeliveryID:  newID(),
			DeviceID:    d.ID,
			Environment: deref(d.PushToStartEnvironment),
			Purpose:     db.PurposeTask,
		})
	}

	started, err := s.store().Activities.Start(r.Context(), db.StartActivityParams{
		ID:                 activityID,
		UserID:             req.UserID,
		RequesterTokenID:   req.TokenID,
		RequesterServiceID: req.ServiceID,
		Key:                payload.Key,
		SchemaVersion:      activityStateVersion,
		Props:              state,
		IdempotencyKey:     key,
		RequestHash:        storedHash(key, hash),
		ExpiresAt:          expiresAt,
		StaleAt:            &staleAt,
		OperationID:        newID(),
		Targets:            targets,
		Now:                now,
	})
	if err != nil {
		s.writeStartConflict(w, r, req, key, hash, err)
		return
	}

	outcome := s.dispatchActivity(r, activityDispatch{
		Activity:   started.Activity,
		Operation:  started.Operation,
		Requester:  req,
		Deliveries: started.Deliveries,
		Devices:    deviceIndex(capable),
	})
	settled := s.settleActivity(r, started.Activity, started.Operation, outcome)

	out := activityResponse{
		Activity: newActivityDTO(settled),
		Accepted: outcome.Accepted,
		Failed:   outcome.Failed,
		Message:  outcome.message(),
	}
	if payload.Replace {
		out.Replaced = &blocking
	}
	if len(targets) == 0 {
		out.Message = ptr("No device on this account can show a Live Activity.")
	}
	WriteJSON(w, r, http.StatusCreated, out)
}

// clearTheWay resolves what already occupies the devices and the key this start
// wants, reporting how many activities were replaced.
//
// Deliveries whose activity has already finished are released here rather than
// treated as blockers: they are bookkeeping left behind by a card the phone
// stopped showing long ago, and a start refused on their account would be
// refused forever.
func (s *server) clearTheWay(w http.ResponseWriter, r *http.Request, req requester, devices []db.Device, payload activityStartPayload) (int, bool) {
	deviceIDs := make([]string, 0, len(devices))
	for _, d := range devices {
		deviceIDs = append(deviceIDs, d.ID)
	}

	occupied, err := s.store().Deliveries.Occupancy(r.Context(), deviceIDs, s.now())
	if err != nil {
		s.writeInternal(w, r, "scanning device occupancy failed", err)
		return 0, false
	}

	var (
		blockers []db.DeviceOccupancy
		expired  []string
	)
	for _, o := range occupied {
		if o.ActivityLive {
			blockers = append(blockers, o)
		} else {
			expired = append(expired, o.ID)
		}
	}
	if len(expired) > 0 {
		if _, err := s.store().Deliveries.Release(r.Context(), expired, s.now()); err != nil {
			s.logError(r, "releasing finished deliveries failed", err)
		}
	}

	holder, ok := s.keyHolder(w, r, req, payload.Key)
	if !ok {
		return 0, false
	}

	if !payload.Replace {
		if len(blockers) > 0 {
			s.writeActivityConflict(w, r, req, blockers[0])
			return 0, false
		}
		if holder != nil {
			WriteError(w, r, http.StatusConflict, CodeActivityConflict,
				"A Live Activity with the key "+deref(payload.Key)+" is still running ("+holder.ID+
					"). End it, use another key, or send replace: true.")
			return 0, false
		}
		return 0, true
	}

	ids := make([]string, 0, len(blockers)+1)
	for _, b := range blockers {
		if !slices.Contains(ids, b.ActivityID) {
			ids = append(ids, b.ActivityID)
		}
	}
	if holder != nil && !slices.Contains(ids, holder.ID) {
		ids = append(ids, holder.ID)
	}
	return s.takeOver(r, ids), true
}

// keyHolder returns the live activity currently holding a key for this
// requester, expiring it first if its time has passed.
func (s *server) keyHolder(w http.ResponseWriter, r *http.Request, req requester, key *string) (*db.LiveActivity, bool) {
	if key == nil {
		return nil, true
	}
	holder, err := s.store().Activities.KeyHolder(r.Context(), req.TokenID, req.ServiceID, *key)
	switch {
	case errors.Is(err, db.ErrNotFound):
		return nil, true
	case err != nil:
		s.writeInternal(w, r, "resolving a Live Activity key failed", err)
		return nil, false
	}

	holder = s.expireActivityIfDue(r.Context(), holder)
	if !holder.Live() {
		return nil, true
	}
	return holder, true
}

// writeActivityConflict refuses a start that would displace another activity.
//
// The blocking activity's id is named only when this requester started it.
// Another integration on the same account may legitimately hold the device, but
// telling this caller which id holds it would hand it a handle it has no
// business having.
func (s *server) writeActivityConflict(w http.ResponseWriter, r *http.Request, req requester, blocker db.DeviceOccupancy) {
	mine := (req.TokenID != nil && blocker.ActivityRequesterTokenID != nil && *req.TokenID == *blocker.ActivityRequesterTokenID) ||
		(req.ServiceID != nil && blocker.ActivityRequesterServiceID != nil && *req.ServiceID == *blocker.ActivityRequesterServiceID)

	message := "A Live Activity is already running on a target device. End it or send replace: true."
	if mine {
		message = "The Live Activity " + blocker.ActivityID +
			" is already running on a target device. End it or send replace: true."
	}
	WriteError(w, r, http.StatusConflict, CodeActivityConflict, message)
}

// writeStartConflict turns a failed insert into the right answer.
//
// Every one of these is a race that the unique indexes caught: a duplicate
// idempotency key, a key claimed a moment ago, or a device that took another
// activity while this request was deciding. The index is what makes the
// invariant true; this only has to explain which one held.
func (s *server) writeStartConflict(w http.ResponseWriter, r *http.Request, req requester, key *string, hash string, err error) {
	if !db.IsUniqueViolation(err) {
		s.writeInternal(w, r, "starting a Live Activity failed", err)
		return
	}

	switch db.ConstraintName(err) {
	case "live_activities_token_idempotency_key", "live_activities_service_idempotency_key":
		if key != nil && s.replayActivityStart(w, r, req, *key, hash) {
			return
		}
	case "live_activities_token_key_key", "live_activities_service_key_key":
		WriteError(w, r, http.StatusConflict, CodeActivityConflict,
			"A Live Activity with that key was started a moment ago. Use another key, or send replace: true.")
		return
	case "live_activity_deliveries_one_task_per_device_key":
		WriteError(w, r, http.StatusConflict, CodeActivityConflict,
			"A Live Activity claimed a target device a moment ago. Retry, or send replace: true.")
		return
	}
	s.writeInternal(w, r, "starting a Live Activity failed", err)
}

// replayActivityStart answers a repeated start, reporting whether it answered.
func (s *server) replayActivityStart(w http.ResponseWriter, r *http.Request, req requester, key, hash string) bool {
	stored, err := s.store().Activities.ByIdempotencyKey(r.Context(), req.TokenID, req.ServiceID, key)
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
	stored = s.expireActivityIfDue(r.Context(), stored)
	WriteJSON(w, r, http.StatusOK, activityResponse{
		Activity: newActivityDTO(*stored),
		Accepted: stored.AcceptedCount,
		Failed:   stored.FailedCount,
		Replayed: true,
	})
	return true
}

type updateActivityRequest struct {
	Title       *string            `json:"title"`
	Status      *string            `json:"status"`
	Detail      optional[*string]  `json:"detail"`
	Progress    optional[*float64] `json:"progress"`
	Symbol      *string            `json:"symbol"`
	PrivacyMode *string            `json:"privacy_mode"`
	AccentColor *string            `json:"accent_color"`
	Style       *string            `json:"style"`
	// StaleAfterSeconds restarts the staleness window. Omitting it keeps the
	// window the activity already had, measured forward from now.
	StaleAfterSeconds *int `json:"stale_after_seconds"`
	// IfSequence makes the change conditional on nothing else having happened
	// since the caller last read the activity.
	IfSequence *int `json:"if_sequence"`
}

// activityUpdatePayload is the validated update, and what the idempotency hash
// covers. The identifier participates, so the same body against two activities
// hashes differently.
type activityUpdatePayload struct {
	Identifier        string   `json:"identifier"`
	Title             *string  `json:"title"`
	Status            *string  `json:"status"`
	Detail            *string  `json:"detail"`
	ClearDetail       bool     `json:"clear_detail"`
	Progress          *float64 `json:"progress"`
	ClearProgress     bool     `json:"clear_progress"`
	Symbol            *string  `json:"symbol"`
	PrivacyMode       *string  `json:"privacy_mode"`
	AccentColor       *string  `json:"accent_color"`
	Style             *string  `json:"style"`
	StaleAfterSeconds *int     `json:"stale_after_seconds"`
}

// updateActivity applies a partial change and pushes it.
func (s *server) updateActivity(w http.ResponseWriter, r *http.Request, req requester) {
	var body updateActivityRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	var v validator
	payload := activityUpdatePayload{
		Identifier:        r.PathValue("identifier"),
		StaleAfterSeconds: body.StaleAfterSeconds,
	}
	if body.Title != nil {
		payload.Title = ptr(v.text("title", *body.Title, 1, maxTitleLen))
	}
	if body.Status != nil {
		payload.Status = ptr(v.text("status", *body.Status, 1, maxStatusLen))
	}
	if detail, ok := body.Detail.Get(); ok {
		payload.ClearDetail = detail == nil
		payload.Detail = v.optionalText("detail", detail, maxDetailLen)
	}
	if progress, ok := body.Progress.Get(); ok {
		payload.ClearProgress = progress == nil
		payload.Progress = v.fraction("progress", progress)
	}
	if body.Symbol != nil {
		payload.Symbol = ptr(v.enum("symbol", body.Symbol, activitySymbols, symbolTerminal))
	}
	if body.PrivacyMode != nil {
		payload.PrivacyMode = ptr(v.enum("privacy_mode", body.PrivacyMode, activityPrivacyModes, privacyStandard))
	}
	if body.AccentColor != nil {
		payload.AccentColor = v.accentColor("accent_color", body.AccentColor)
	}
	if body.Style != nil {
		payload.Style = ptr(v.enum("style", body.Style, activityStyles, styleStandard))
	}
	if body.StaleAfterSeconds != nil {
		v.intRange("stale_after_seconds", body.StaleAfterSeconds, 0, maxActivityTTL, defaultStaleAfter)
	}
	if payload.isEmpty() {
		v.add("status", "at least one field to change is required")
	}
	if !v.done(w, r) {
		return
	}

	act, ok := s.prepareMutation(w, r, req, body.IfSequence)
	if !ok {
		return
	}

	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	hash, err := requestHash(payload)
	if err != nil {
		s.writeInternal(w, r, "hashing a Live Activity update failed", err)
		return
	}
	if key != nil && s.replayActivityOperation(w, r, req, *key, hash, *act) {
		return
	}
	if !s.checkQuota(w, r, req) {
		return
	}

	state, err := decodeActivityState(act.Props)
	if err != nil {
		s.writeInternal(w, r, "decoding a Live Activity state failed", err)
		return
	}
	payload.applyTo(&state, s.now())
	merged, err := encodeActivityState(state)
	if err != nil {
		s.writeInternal(w, r, "encoding a Live Activity state failed", err)
		return
	}

	staleAt := s.nextStaleAt(*act, payload.StaleAfterSeconds)
	s.applyMutation(w, r, req, *act, db.MutateActivityParams{
		ActivityID:         act.ID,
		ExpectedSequence:   act.Sequence,
		Event:              db.OperationUpdate,
		Props:              merged,
		StaleAt:            db.Value(&staleAt),
		OperationID:        newID(),
		RequesterTokenID:   req.TokenID,
		RequesterServiceID: req.ServiceID,
		IdempotencyKey:     key,
		RequestHash:        storedHash(key, hash),
		Now:                s.now(),
	}, key, hash)
}

// isEmpty reports whether the update would change nothing.
func (p activityUpdatePayload) isEmpty() bool {
	return p.Title == nil && p.Status == nil && p.Detail == nil && !p.ClearDetail &&
		p.Progress == nil && !p.ClearProgress && p.Symbol == nil && p.PrivacyMode == nil &&
		p.AccentColor == nil && p.Style == nil && p.StaleAfterSeconds == nil
}

// applyTo merges an update into the current state.
//
// Clearing is explicit: a null removes the key rather than leaving an empty
// value behind, because the widget lays itself out differently with and without
// a detail line or a progress bar.
func (p activityUpdatePayload) applyTo(state *activityState, now time.Time) {
	if p.Title != nil {
		state.Title = *p.Title
	}
	if p.Status != nil {
		state.Status = *p.Status
	}
	switch {
	case p.ClearDetail:
		state.Detail = nil
	case p.Detail != nil:
		state.Detail = p.Detail
	}
	switch {
	case p.ClearProgress:
		state.Progress = nil
	case p.Progress != nil:
		state.Progress = p.Progress
	}
	if p.Symbol != nil {
		state.Symbol = *p.Symbol
	}
	if p.PrivacyMode != nil {
		state.PrivacyMode = *p.PrivacyMode
	}
	if p.AccentColor != nil {
		state.AccentColor = *p.AccentColor
	}
	if p.Style != nil {
		state.Style = *p.Style
	}
	state.UpdatedAt = Timestamp(now)
}

type endActivityRequest struct {
	Status      *string            `json:"status"`
	Detail      optional[*string]  `json:"detail"`
	Progress    optional[*float64] `json:"progress"`
	Symbol      *string            `json:"symbol"`
	AccentColor *string            `json:"accent_color"`
	// DismissAfterSeconds keeps the finished card on screen for a moment. Zero,
	// the default, removes it immediately.
	DismissAfterSeconds *int `json:"dismiss_after_seconds"`
	IfSequence          *int `json:"if_sequence"`
}

type activityEndPayload struct {
	Identifier          string   `json:"identifier"`
	Status              string   `json:"status"`
	Detail              *string  `json:"detail"`
	ClearDetail         bool     `json:"clear_detail"`
	Progress            *float64 `json:"progress"`
	ClearProgress       bool     `json:"clear_progress"`
	Symbol              string   `json:"symbol"`
	AccentColor         *string  `json:"accent_color"`
	DismissAfterSeconds int      `json:"dismiss_after_seconds"`
}

// endActivity finishes an activity and pushes the final state.
//
// The end is a state transition with content, not a deletion: the last thing a
// person sees is the card saying how it went, which is why this is a POST with a
// body rather than a DELETE. The row stays for the history.
func (s *server) endActivity(w http.ResponseWriter, r *http.Request, req requester) {
	var body endActivityRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	var v validator
	payload := activityEndPayload{
		Identifier:          r.PathValue("identifier"),
		Status:              defaultEndStatus,
		Symbol:              v.enum("symbol", body.Symbol, activitySymbols, symbolSuccess),
		AccentColor:         v.accentColor("accent_color", body.AccentColor),
		DismissAfterSeconds: v.intRange("dismiss_after_seconds", body.DismissAfterSeconds, 0, maxDismissAfter, 0),
	}
	if body.Status != nil {
		payload.Status = v.text("status", *body.Status, 1, maxStatusLen)
	}
	if detail, ok := body.Detail.Get(); ok {
		payload.ClearDetail = detail == nil
		payload.Detail = v.optionalText("detail", detail, maxDetailLen)
	}
	if progress, ok := body.Progress.Get(); ok {
		payload.ClearProgress = progress == nil
		payload.Progress = v.fraction("progress", progress)
	}
	if !v.done(w, r) {
		return
	}

	act, ok := s.prepareMutation(w, r, req, body.IfSequence)
	if !ok {
		return
	}

	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	hash, err := requestHash(payload)
	if err != nil {
		s.writeInternal(w, r, "hashing a Live Activity end failed", err)
		return
	}
	if key != nil && s.replayActivityOperation(w, r, req, *key, hash, *act) {
		return
	}
	if !s.checkQuota(w, r, req) {
		return
	}

	state, err := decodeActivityState(act.Props)
	if err != nil {
		s.writeInternal(w, r, "decoding a Live Activity state failed", err)
		return
	}
	activityUpdatePayload{
		Status:        &payload.Status,
		Detail:        payload.Detail,
		ClearDetail:   payload.ClearDetail,
		Progress:      payload.Progress,
		ClearProgress: payload.ClearProgress,
		Symbol:        &payload.Symbol,
		AccentColor:   payload.AccentColor,
	}.applyTo(&state, s.now())
	final, err := encodeActivityState(state)
	if err != nil {
		s.writeInternal(w, r, "encoding a Live Activity state failed", err)
		return
	}

	dismissal := s.now().Add(time.Duration(payload.DismissAfterSeconds) * time.Second)
	s.applyMutation(w, r, req, *act, db.MutateActivityParams{
		ActivityID:         act.ID,
		ExpectedSequence:   act.Sequence,
		Event:              db.OperationEnd,
		Props:              final,
		DismissalAt:        db.Value(&dismissal),
		OperationID:        newID(),
		RequesterTokenID:   req.TokenID,
		RequesterServiceID: req.ServiceID,
		IdempotencyKey:     key,
		RequestHash:        storedHash(key, hash),
		Now:                s.now(),
	}, key, hash)
}

// prepareMutation resolves the activity a change addresses and checks that it
// can still take one.
func (s *server) prepareMutation(w http.ResponseWriter, r *http.Request, req requester, ifSequence *int) (*db.LiveActivity, bool) {
	act, err := s.store().Activities.Resolve(r.Context(), db.ResolveParams{
		Identifier:         r.PathValue("identifier"),
		RequesterTokenID:   req.TokenID,
		RequesterServiceID: req.ServiceID,
	})
	if err != nil {
		s.writeStoreError(w, r, "Live Activity", err)
		return nil, false
	}

	act = s.expireActivityIfDue(r.Context(), act)
	if !act.Live() {
		s.writeConflict(w, r, "That Live Activity has already finished ("+act.Status+").")
		return nil, false
	}
	if ifSequence != nil && *ifSequence != act.Sequence {
		s.writeSequenceConflict(w, r, act.Sequence)
		return nil, false
	}
	return act, true
}

// applyMutation writes the change, pushes it, and answers.
func (s *server) applyMutation(w http.ResponseWriter, r *http.Request, req requester, act db.LiveActivity, params db.MutateActivityParams, key *string, hash string) {
	updated, op, err := s.store().Activities.Mutate(r.Context(), params)
	switch {
	case errors.Is(err, db.ErrNotFound):
		// The guard did not match: something changed the activity between the
		// read and the write.
		s.writeSequenceConflict(w, r, act.Sequence)
		return
	case err != nil:
		if key != nil && db.IsUniqueViolation(err) && s.replayActivityOperation(w, r, req, *key, hash, act) {
			return
		}
		s.writeInternal(w, r, "changing a Live Activity failed", err)
		return
	}

	deliveries, err := s.store().Deliveries.ListForActivity(r.Context(), updated.ID, db.LiveStatuses())
	if err != nil {
		s.writeInternal(w, r, "listing Live Activity deliveries failed", err)
		return
	}

	outcome := s.dispatchActivity(r, activityDispatch{
		Activity:   *updated,
		Operation:  *op,
		Requester:  req,
		Deliveries: deliveries,
	})
	settled := s.settleActivity(r, *updated, *op, outcome)

	WriteJSON(w, r, http.StatusOK, activityResponse{
		Activity: newActivityDTO(settled),
		Accepted: outcome.Accepted,
		Failed:   outcome.Failed,
		Message:  outcome.message(),
	})
}

// replayActivityOperation answers a repeated update or end.
//
// Updates and ends key off the operation rather than the activity, because one
// activity legitimately sees many keyed changes over its life; only the start is
// identified by the activity's own key.
func (s *server) replayActivityOperation(w http.ResponseWriter, r *http.Request, req requester, key, hash string, act db.LiveActivity) bool {
	stored, err := s.store().Operations.ByIdempotencyKey(r.Context(), req.TokenID, req.ServiceID, key)
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
	current, err := s.store().Activities.ByID(r.Context(), stored.ActivityID)
	if err != nil {
		current = &act
	}
	WriteJSON(w, r, http.StatusOK, activityResponse{
		Activity: newActivityDTO(*current),
		Accepted: stored.AcceptedCount,
		Failed:   stored.FailedCount,
		Replayed: true,
	})
	return true
}

func (s *server) writeSequenceConflict(w http.ResponseWriter, r *http.Request, sequence int) {
	WriteError(w, r, http.StatusConflict, CodeSequenceConflict,
		"That Live Activity has moved on; it is now at sequence "+strconv.Itoa(sequence)+". Re-read it and reapply the change.")
}

// nextStaleAt recomputes when the card should be shown as stale.
//
// Omitting stale_after_seconds preserves the window the activity already had,
// measured forward from now rather than from the start — an activity that
// reports every minute should keep looking fresh, not decay because its first
// window is running out.
func (s *server) nextStaleAt(act db.LiveActivity, staleAfter *int) time.Time {
	window := time.Duration(defaultStaleAfter) * time.Second
	switch {
	case staleAfter != nil:
		window = time.Duration(*staleAfter) * time.Second
	case act.StaleAt != nil:
		if gap := act.StaleAt.Sub(act.UpdatedAt); gap > 0 {
			window = gap
		} else {
			window = 0
		}
	}
	return earliest(s.now().Add(window), act.ExpiresAt)
}

// earliest returns the earlier of two instants. A staleness date beyond the
// activity's own deadline would promise a card that outlives it.
func earliest(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// deviceIndex keys devices by id for the dispatcher.
func deviceIndex(devices []db.Device) map[string]db.Device {
	index := make(map[string]db.Device, len(devices))
	for _, d := range devices {
		index[d.ID] = d
	}
	return index
}

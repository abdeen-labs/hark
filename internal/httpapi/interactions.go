package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/push"
)

// Interaction bounds, in seconds.
//
// A question has to expire: an agent blocked on an answer needs to know when to
// give up, and a prompt that lives forever is a prompt nobody answers. A day is
// the ceiling for a notification, and a Live Activity is additionally bound by
// the eight hours iOS gives it.
const (
	minInteractionTTL = 30
	maxInteractionTTL = 24 * 60 * 60
	// defaultInteractionTTL is fifteen minutes: long enough to walk back to your
	// desk, short enough that a forgotten prompt clears itself.
	defaultInteractionTTL = 15 * 60

	// maxLiveActivityPrompt is what fits on a Lock Screen card without being
	// truncated into meaninglessness.
	maxLiveActivityPrompt = 240
)

// What a create says when the question could not be presented the way it was
// asked for.
const (
	messageLiveActivityFallback = "No device on this account can show a question on its Lock Screen. " +
		"It was sent as a notification instead."
	messageNoInteractiveDevice = "No device on this account can present a question, " +
		"on the Lock Screen or as a notification."
)

// waitCeiling bounds a long poll. It is under the server's write timeout, so a
// waiting client is answered rather than disconnected, and short enough that an
// agent's own timeouts stay in charge.
const (
	waitCeiling  = 25 * time.Second
	waitInterval = 250 * time.Millisecond
)

// interactionDTO is a question as every response renders it.
type interactionDTO struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Prompt       string `json:"prompt"`
	Kind         string `json:"kind"`
	Presentation string `json:"presentation"`
	Status       string `json:"status"`
	// Choices are the answers this kind accepts, derived from the kind rather
	// than stored input.
	Choices []string `json:"choices"`
	// Response is the answer: the action for an approval or a yes/no, the text
	// for a reply.
	Response *string `json:"response"`
	URL      *string `json:"url"`
	ImageURL *string `json:"image_url"`
	// ActionDigest binds an answer to this exact question. A client sends it
	// back when it answers, which is what stops a phone showing a stale prompt
	// from answering the one that replaced it.
	ActionDigest       string     `json:"action_digest"`
	PrimaryLabel       *string    `json:"primary_label"`
	SecondaryLabel     *string    `json:"secondary_label"`
	CorrelationID      *string    `json:"correlation_id"`
	AcceptedCount      int        `json:"accepted_count"`
	RespondingDeviceID *string    `json:"responding_device_id"`
	ExpiresAt          Timestamp  `json:"expires_at"`
	CreatedAt          Timestamp  `json:"created_at"`
	RespondedAt        *Timestamp `json:"responded_at"`
	CanceledAt         *Timestamp `json:"canceled_at"`
}

func newInteractionDTO(i db.Interaction) interactionDTO {
	return interactionDTO{
		ID:                 i.ID,
		Title:              i.Title,
		Prompt:             i.Prompt,
		Kind:               i.Kind,
		Presentation:       i.Presentation,
		Status:             i.Status,
		Choices:            i.Choices,
		Response:           i.Response,
		URL:                i.URL,
		ImageURL:           i.ImageURL,
		ActionDigest:       i.ActionDigest,
		PrimaryLabel:       i.PrimaryLabel,
		SecondaryLabel:     i.SecondaryLabel,
		CorrelationID:      i.CorrelationID,
		AcceptedCount:      i.AcceptedCount,
		RespondingDeviceID: i.RespondingDeviceID,
		ExpiresAt:          Timestamp(i.ExpiresAt),
		CreatedAt:          Timestamp(i.CreatedAt),
		RespondedAt:        TimestampPtr(i.RespondedAt),
		CanceledAt:         TimestampPtr(i.CanceledAt),
	}
}

// interactionListItemDTO adds who is asking, which the inbox shows above the
// prompt: "Deploy bot asks…" is a different question from "some agent asks…".
type interactionListItemDTO struct {
	interactionDTO
	SourceName     string  `json:"source_name"`
	SourceImageURL *string `json:"source_image_url"`
}

type interactionListResponse struct {
	Interactions []interactionListItemDTO `json:"interactions"`
	NextCursor   *string                  `json:"next_cursor"`
}

type interactionReadResponse struct {
	Interaction interactionDTO `json:"interaction"`
}

// interactionResponse is what a create answers with.
type interactionResponse struct {
	Interaction interactionDTO `json:"interaction"`
	// Accepted counts the devices the question reached.
	Accepted int `json:"accepted"`
	// ActivityID is the Live Activity presenting the question, when one could be
	// started. Null means it went out as a notification instead.
	ActivityID *string `json:"activity_id"`
	Replayed   bool    `json:"replayed"`
	Message    *string `json:"message"`
}

// interactionForPrincipal reads one question with the caller's own reach: a
// session reads anything on the account, an API token reads only the questions
// it asked itself. Anything outside that reach — another token's question, a
// webhook service's, an unknown id — is the same [db.ErrNotFound], so a foreign
// id is indistinguishable from one that never existed.
func (s *server) interactionForPrincipal(ctx context.Context, principal *auth.Principal, id string) (*db.Interaction, error) {
	if principal.IsAPIToken() {
		return s.store().Interactions.ByIDForToken(ctx, id, principal.APIToken.ID)
	}
	return s.store().Interactions.ByIDForUser(ctx, id, principal.UserID())
}

// cancelInteractionForPrincipal withdraws a question with the same reach as
// [server.interactionForPrincipal]: a session cancels any pending question on
// the account, an API token only what it asked itself.
func (s *server) cancelInteractionForPrincipal(ctx context.Context, principal *auth.Principal, id string, now time.Time) (*db.Interaction, error) {
	if principal.IsAPIToken() {
		return s.store().Interactions.CancelForToken(ctx, id, principal.APIToken.ID, now)
	}
	return s.store().Interactions.CancelForUser(ctx, id, principal.UserID(), now)
}

// handleListInteractions pages the caller's questions, newest first.
//
// `status=pending` — the default — is the inbox: what is still waiting for an
// answer. Expired questions are filtered out rather than expired here, because a
// phone opening its inbox should not resolve every stale prompt at once.
//
// A session pages the account-wide inbox. An API token pages only the questions
// it asked itself: another integration's questions are not its business, and a
// webhook service's are not either.
func (s *server) handleListInteractions(w http.ResponseWriter, r *http.Request) {
	query, ok := s.parseList(w, r)
	if !ok {
		return
	}

	pendingOnly := true
	switch status := r.URL.Query().Get("status"); status {
	case "", "pending":
	case "all":
		pendingOnly = false
	default:
		WriteFieldErrors(w, r, "The request query is invalid.", []FieldError{{
			Field:   "status",
			Message: "must be pending or all",
		}})
		return
	}

	principal := auth.PrincipalFrom(r.Context())
	params := db.ListInteractionsParams{
		UserID:      principal.UserID(),
		PendingOnly: pendingOnly,
		Now:         s.now(),
		Cursor:      query.Cursor,
		Limit:       query.Limit,
	}
	if principal.IsAPIToken() {
		params.RequesterTokenID = &principal.APIToken.ID
	}
	page, err := s.store().Interactions.List(r.Context(), params)
	if err != nil {
		s.writeInternal(w, r, "listing interactions failed", err)
		return
	}

	out := make([]interactionListItemDTO, 0, len(page.Items))
	for _, item := range page.Items {
		out = append(out, interactionListItemDTO{
			interactionDTO: newInteractionDTO(item.Interaction),
			SourceName:     item.SourceName,
			SourceImageURL: item.SourceImageURL,
		})
	}
	WriteJSON(w, r, http.StatusOK, interactionListResponse{Interactions: out, NextCursor: nextCursor(page)})
}

// handleGetInteraction returns one question, optionally waiting for it to be
// answered.
//
// The wait is what turns "ask and poll" into "ask and block": an agent that
// wants an answer before it continues holds one request open instead of
// hammering the endpoint, and gets the answer the moment it lands. It always
// returns 200 — a question that is still pending when the wait runs out is an
// answer to "what is it doing", not an error.
//
// A session reads any question on the account; an API token reads only its own
// (§ interactionForPrincipal), and a foreign id 404s before any waiting starts.
func (s *server) handleGetInteraction(w http.ResponseWriter, r *http.Request) {
	wait, ok := s.parseWait(w, r)
	if !ok {
		return
	}

	principal := auth.PrincipalFrom(r.Context())
	in, err := s.interactionForPrincipal(r.Context(), principal, r.PathValue("id"))
	if err != nil {
		s.writeStoreError(w, r, "interaction", err)
		return
	}
	in = s.expireInteractionIfDue(r, in)

	deadline := s.now().Add(wait)
	for in.Status == db.InteractionPending && wait > 0 {
		if s.now().After(deadline) {
			break
		}
		select {
		case <-r.Context().Done():
			// The caller hung up. Nothing has been written, so there is nothing
			// to undo and nothing to say.
			return
		case <-time.After(waitInterval):
		}

		// The refresh re-reads with the caller's own reach, same as the first
		// read: a loop that widened to the whole account would hand an API
		// token exactly the bypass the first read refused.
		fresh, err := s.interactionForPrincipal(r.Context(), principal, in.ID)
		if err != nil {
			s.writeStoreError(w, r, "interaction", err)
			return
		}
		in = s.expireInteractionIfDue(r, fresh)
	}
	WriteJSON(w, r, http.StatusOK, interactionReadResponse{Interaction: newInteractionDTO(*in)})
}

// parseWait reads the optional long-poll duration.
func (s *server) parseWait(w http.ResponseWriter, r *http.Request) (time.Duration, bool) {
	raw := r.URL.Query().Get("wait_seconds")
	if raw == "" {
		return 0, true
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds < 0 || time.Duration(seconds)*time.Second > waitCeiling {
		WriteFieldErrors(w, r, "The request query is invalid.", []FieldError{{
			Field:   "wait_seconds",
			Message: rangeMessage(0, int(waitCeiling.Seconds())),
		}})
		return 0, false
	}
	return time.Duration(seconds) * time.Second, true
}

type createInteractionRequest struct {
	Title            string   `json:"title"`
	Prompt           string   `json:"prompt"`
	Kind             string   `json:"kind"`
	Presentation     *string  `json:"presentation"`
	Style            *string  `json:"style"`
	PrimaryLabel     *string  `json:"primary_label"`
	SecondaryLabel   *string  `json:"secondary_label"`
	ImageURL         *string  `json:"image_url"`
	URL              *string  `json:"url"`
	Priority         *string  `json:"priority"`
	DeviceIDs        []string `json:"device_ids"`
	ExpiresInSeconds *int     `json:"expires_in_seconds"`
}

// interactionPayload is the validated request, and what the idempotency hash
// covers.
type interactionPayload struct {
	Title            string   `json:"title"`
	Prompt           string   `json:"prompt"`
	Kind             string   `json:"kind"`
	Presentation     string   `json:"presentation"`
	Style            string   `json:"style"`
	PrimaryLabel     *string  `json:"primary_label"`
	SecondaryLabel   *string  `json:"secondary_label"`
	ImageURL         *string  `json:"image_url"`
	URL              *string  `json:"url"`
	Priority         string   `json:"priority"`
	DeviceIDs        []string `json:"device_ids"`
	ExpiresInSeconds int      `json:"expires_in_seconds"`
}

// interactionKinds and presentations are the closed vocabularies of a question.
var (
	interactionKinds         = []string{db.InteractionApproval, db.InteractionYesNo, db.InteractionReply}
	interactionPresentations = []string{db.PresentationNotification, db.PresentationLiveActivity}
)

// handleCreateInteraction asks the phone a question.
//
// A question is a notification plus a promise: the answer comes back to whoever
// asked. That is why creating one needs both scopes — it sends a push *and*
// creates something the sender can read the answer to — and why the row is
// written before anything is delivered.
func (s *server) handleCreateInteraction(w http.ResponseWriter, r *http.Request) {
	var body createInteractionRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	var v validator
	payload := interactionPayload{
		Title:            v.text("title", body.Title, 1, maxTitleLen),
		Prompt:           v.text("prompt", body.Prompt, 1, maxBodyLen),
		Kind:             v.enum("kind", &body.Kind, interactionKinds, ""),
		Presentation:     v.enum("presentation", body.Presentation, interactionPresentations, db.PresentationNotification),
		Style:            v.enum("style", body.Style, interactiveStyles, styleApproval),
		PrimaryLabel:     v.label("primary_label", body.PrimaryLabel),
		SecondaryLabel:   v.label("secondary_label", body.SecondaryLabel),
		ImageURL:         v.httpsURL("image_url", body.ImageURL),
		URL:              v.linkURL("url", body.URL),
		Priority:         v.enum("priority", body.Priority, db.Priorities, db.PriorityNormal),
		DeviceIDs:        v.ids("device_ids", body.DeviceIDs),
		ExpiresInSeconds: v.intRange("expires_in_seconds", body.ExpiresInSeconds, minInteractionTTL, maxInteractionTTL, defaultInteractionTTL),
	}
	s.validatePresentation(&v, &payload, body)
	if !v.done(w, r) {
		return
	}

	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	hash, err := requestHash(payload)
	if err != nil {
		s.writeInternal(w, r, "hashing an interaction request failed", err)
		return
	}

	req := tokenRequester(auth.PrincipalFrom(r.Context()))
	if key != nil && s.replayInteraction(w, r, *req.TokenID, *key, hash) {
		return
	}
	if !s.checkQuota(w, r, req) {
		return
	}

	devices, ok := s.selectTargets(w, r, req.UserID, payload.DeviceIDs)
	if !ok {
		return
	}

	// The credential the phone answers with. It exists in plaintext only inside
	// this request and the push it produces; the row keeps a digest.
	responseToken := auth.NewResponseToken()

	interactionID := newID()
	digest, err := requestHash(interactionDigest{
		InteractionID:  interactionID,
		Title:          payload.Title,
		Prompt:         payload.Prompt,
		Kind:           payload.Kind,
		Choices:        db.ChoicesFor(payload.Kind),
		URL:            payload.URL,
		Presentation:   payload.Presentation,
		PrimaryLabel:   payload.PrimaryLabel,
		SecondaryLabel: payload.SecondaryLabel,
	})
	if err != nil {
		s.writeInternal(w, r, "computing an interaction digest failed", err)
		return
	}

	// Custom labels only mean something on a card that draws buttons; storing
	// them for a notification would promise a rendering that never happens.
	primary, secondary := payload.PrimaryLabel, payload.SecondaryLabel
	if payload.Presentation != db.PresentationLiveActivity {
		primary, secondary = nil, nil
	}

	now := s.now()
	in, err := s.store().Interactions.Create(r.Context(), db.CreateInteractionParams{
		ID:                interactionID,
		UserID:            req.UserID,
		RequesterTokenID:  req.TokenID,
		Title:             payload.Title,
		Prompt:            payload.Prompt,
		Kind:              payload.Kind,
		Presentation:      payload.Presentation,
		PrimaryLabel:      primary,
		SecondaryLabel:    secondary,
		Choices:           db.ChoicesFor(payload.Kind),
		URL:               payload.URL,
		ImageURL:          payload.ImageURL,
		ActionDigest:      digest,
		IdempotencyKey:    key,
		RequestHash:       storedHash(key, hash),
		ResponseTokenHash: ptr(auth.ResponseTokenHash(responseToken)),
		ExpiresAt:         now.Add(time.Duration(payload.ExpiresInSeconds) * time.Second),
		Now:               now,
	})
	if err != nil {
		if key != nil && db.IsUniqueViolation(err) && s.replayInteraction(w, r, *req.TokenID, *key, hash) {
			return
		}
		s.writeInternal(w, r, "recording an interaction failed", err)
		return
	}

	out := interactionResponse{Interaction: newInteractionDTO(*in)}
	switch {
	case len(devices) == 0:
		out.Message = ptr(messageNoDevices)
	case payload.Presentation == db.PresentationLiveActivity:
		activityID, outcome := s.startInteractionActivity(r, *in, payload.Style, responseToken, devices)
		if activityID != nil {
			out.ActivityID, out.Accepted = activityID, outcome.Accepted
			if outcome.Accepted == 0 {
				out.Message = outcome.message()
			}
			break
		}

		// No phone on this account can draw a card. The question is still worth
		// asking, so it goes out as a notification instead: a plainer surface,
		// but the same buttons, the same credential and the same answer route.
		// `activity_id` stays null, which is how a caller tells the two apart.
		out.Accepted = s.deliverInteractionAlert(r, *in, req, payload, responseToken, devices)
		out.Message = ptr(messageLiveActivityFallback)
		if out.Accepted == 0 {
			out.Message = ptr(messageNoInteractiveDevice)
		}
	default:
		out.Accepted = s.deliverInteractionAlert(r, *in, req, payload, responseToken, devices)
		if out.Accepted == 0 {
			out.Message = ptr(messageNoneAccepted)
		}
	}

	if settled, err := s.store().Interactions.SettleAccepted(detach(r.Context()), in.ID, out.Accepted); err != nil {
		s.logError(r, "settling an interaction failed", err)
	} else {
		out.Interaction = newInteractionDTO(*settled)
	}
	WriteJSON(w, r, http.StatusCreated, out)
}

// deliverInteractionAlert sends the question as a notification with answer
// actions, returning how many devices took it.
func (s *server) deliverInteractionAlert(r *http.Request, in db.Interaction, req requester, payload interactionPayload, responseToken string, devices []db.Device) int {
	// Only a device that knows how to draw answer actions can be asked: an older
	// installation would show a notification with no way to reply to it.
	capable := make([]db.Device, 0, len(devices))
	for _, d := range devices {
		if d.InteractionCapable() {
			capable = append(capable, d)
		}
	}
	if len(capable) == 0 {
		return 0
	}

	result := s.fanOut(r, alertContent{
		Title:      in.Title,
		Body:       in.Prompt,
		ImageURL:   in.ImageURL,
		URL:        in.URL,
		Priority:   payload.Priority,
		SourceID:   deref(req.TokenID) + deref(req.ServiceID),
		SourceName: req.Name,
		RecordID:   in.ID,
		ThreadKey:  "interaction-" + in.ID,
		Interaction: &push.AlertInteraction{
			ID:             in.ID,
			Kind:           in.Kind,
			ActionDigest:   in.ActionDigest,
			ResponseToken:  responseToken,
			PrimaryLabel:   deref(in.PrimaryLabel),
			SecondaryLabel: deref(in.SecondaryLabel),
			ExpiresAt:      in.ExpiresAt,
		},
	}, capable)
	return result.Accepted
}

// validatePresentation enforces the rules that only make sense across fields.
//
// A Lock Screen card is a smaller, more constrained surface than a notification:
// it has two buttons and no room for a free-text reply, it cannot open a link,
// and it cannot outlive the eight hours iOS gives an activity. Saying so at
// creation is better than accepting the request and quietly dropping half of it.
func (s *server) validatePresentation(v *validator, payload *interactionPayload, body createInteractionRequest) {
	if payload.Presentation != db.PresentationLiveActivity {
		if body.Style != nil {
			v.add("style", "requires presentation: live_activity")
		}
		if body.PrimaryLabel != nil || body.SecondaryLabel != nil {
			v.add("primary_label", "custom labels require presentation: live_activity")
		}
		return
	}

	if payload.Kind == db.InteractionReply {
		v.add("kind", "a Live Activity can present approval or yes_no, not a free-text reply")
	}
	if len([]rune(payload.Prompt)) > maxLiveActivityPrompt {
		v.add("prompt", "must be at most "+strconv.Itoa(maxLiveActivityPrompt)+
			" characters when presented as a Live Activity")
	}
	if payload.ExpiresInSeconds > maxActivityTTL {
		v.add("expires_in_seconds", "must be at most "+strconv.Itoa(maxActivityTTL)+
			" seconds when presented as a Live Activity")
	}
	if body.ImageURL != nil {
		v.add("image_url", "is not shown on a Live Activity")
	}
	if body.URL != nil {
		v.add("url", "is not shown on a Live Activity")
	}
}

// interactionDigest is the immutable part of a question. Its hash is what a
// client echoes back when it answers.
type interactionDigest struct {
	InteractionID  string   `json:"interaction_id"`
	Title          string   `json:"title"`
	Prompt         string   `json:"prompt"`
	Kind           string   `json:"kind"`
	Choices        []string `json:"choices"`
	URL            *string  `json:"url"`
	Presentation   string   `json:"presentation"`
	PrimaryLabel   *string  `json:"primary_label"`
	SecondaryLabel *string  `json:"secondary_label"`
}

// replayInteraction answers a repeated create.
func (s *server) replayInteraction(w http.ResponseWriter, r *http.Request, tokenID, key, hash string) bool {
	stored, err := s.store().Interactions.ByIdempotencyKey(r.Context(), tokenID, key)
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

	out := interactionResponse{
		Interaction: newInteractionDTO(*stored),
		Accepted:    stored.AcceptedCount,
		Replayed:    true,
	}
	if act, err := s.store().Activities.ByInteractionID(r.Context(), stored.ID); err == nil {
		out.ActivityID = &act.ID
	}
	WriteJSON(w, r, http.StatusOK, out)
	return true
}

type respondRequest struct {
	Action string `json:"action"`
	// Text is the answer to a reply question, and must be absent otherwise.
	Text *string `json:"text"`
	// DeviceID attributes the answer to the phone that gave it.
	DeviceID string `json:"device_id"`
	// ActionDigest is what the phone was shown. It is required on every path: an
	// answer to a question the phone is no longer displaying is not an answer.
	ActionDigest string `json:"action_digest"`
	// ResponseToken is the credential from the push payload. It is what lets a
	// notification action or a Lock Screen button answer with no session.
	ResponseToken *string `json:"response_token"`
}

// handleRespondToInteraction records an answer.
//
// One route serves all three ways an answer arrives — the app with a session,
// the notification-service extension, and the Lock Screen widget — because they
// are the same act. The last two hold no session, so they present the one-shot
// credential the push carried; a session needs none.
//
// A credential of the right kind that does not open this question answers 404,
// the same as an unknown id, so the route cannot be used to find out which
// questions exist. A credential of the wrong kind is refused before any lookup
// happens — see [server.resolveRespondent].
func (s *server) handleRespondToInteraction(w http.ResponseWriter, r *http.Request) {
	var body respondRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	var v validator
	action := v.text("action", body.Action, 1, 20)
	deviceID := v.text("device_id", body.DeviceID, 1, maxIDLen)
	digest := v.text("action_digest", body.ActionDigest, 1, 128)
	text := v.optionalText("text", body.Text, maxReplyLen)
	if !v.done(w, r) {
		return
	}

	in, ok := s.resolveRespondent(w, r, body.ResponseToken)
	if !ok {
		return
	}

	// The device has to belong to the account the question was addressed to, and
	// still be active: an answer is attributed to a phone, and attributing one to
	// a retired device would put a name to something that cannot have happened.
	device, err := s.store().Devices.ByID(r.Context(), deviceID, in.UserID)
	if err != nil || !device.Active {
		s.writeNotFound(w, r, "device")
		return
	}

	if digest != in.ActionDigest {
		WriteError(w, r, http.StatusConflict, CodeDigestMismatch,
			"That answer refers to a different version of this question. Re-read it and answer again.")
		return
	}

	status, ok := db.StatusForAction(in.Kind, action)
	if !ok {
		WriteFieldErrors(w, r, "The request body is invalid.", []FieldError{{
			Field:   "action",
			Message: "must be one of " + strings.Join(in.Choices, ", "),
		}})
		return
	}
	if in.Kind == db.InteractionReply && text == nil {
		WriteFieldErrors(w, r, "The request body is invalid.", []FieldError{{
			Field:   "text",
			Message: "is required to answer a reply question",
		}})
		return
	}

	in = s.expireInteractionIfDue(r, in)

	response := &action
	if in.Kind == db.InteractionReply {
		response = text
	}

	answered, err := s.store().Interactions.Respond(r.Context(), db.RespondParams{
		ID:              in.ID,
		UserID:          in.UserID,
		Status:          status,
		Response:        response,
		DeviceID:        &device.ID,
		TriggerCallback: in.CallbackURL != nil,
		Now:             s.now(),
	})
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			s.writeAlreadyAnswered(w, r, in.ID, device.ID, status)
			return
		}
		s.writeInternal(w, r, "recording an answer failed", err)
		return
	}

	// The card presenting the question has to stop asking it. This runs inline:
	// a goroutine could be lost to a shutdown, and a Lock Screen still showing an
	// answered question is worse than an answer that took a moment longer.
	s.resolveInteractionActivity(r, *answered)

	// Answering armed the callback row. Waking the worker is the difference
	// between the caller hearing back now and hearing back on the next sweep.
	if s.opts.Callbacks != nil && answered.CallbackURL != nil {
		s.opts.Callbacks.Nudge()
	}
	WriteJSON(w, r, http.StatusOK, interactionReadResponse{Interaction: newInteractionDTO(*answered)})
}

// resolveRespondent finds the question an answer is for, and decides whether
// this caller may answer it.
//
// Answering is the owner's act, and exactly two credentials express it: the
// session they are signed in with, and the one-shot `response_token` the push
// carried to their phone. The token is checked first because it is the more
// specific claim — it names one question — and because the extension and the
// Lock Screen widget that present it hold no session at all.
//
// An API token is neither. It is the agent that asks questions, and a
// credential that could answer its own request would make approval a formality:
// an agent wanting a "yes" would simply grant itself one. So a token gets 403
// rather than the account owner's authority, however broad its scopes.
func (s *server) resolveRespondent(w http.ResponseWriter, r *http.Request, responseToken *string) (*db.Interaction, bool) {
	if responseToken != nil {
		if !auth.ValidResponseToken(*responseToken) {
			s.writeNotFound(w, r, "interaction")
			return nil, false
		}
		in, err := s.store().Interactions.ByResponseToken(r.Context(),
			r.PathValue("id"), auth.ResponseTokenHash(*responseToken))
		if err != nil {
			s.writeStoreError(w, r, "interaction", err)
			return nil, false
		}
		return in, true
	}

	principal := auth.PrincipalFrom(r.Context())
	switch {
	case principal == nil:
		writeUnauthenticated(w, r)
		return nil, false
	case !principal.IsSession():
		WriteError(w, r, http.StatusForbidden, CodeSessionRequired,
			"Answering a question requires the owner's session, or the response_token the push carried. "+
				"An API token cannot answer the questions it asks.")
		return nil, false
	}

	in, err := s.store().Interactions.ByIDForUser(r.Context(), r.PathValue("id"), principal.UserID())
	if err != nil {
		s.writeStoreError(w, r, "interaction", err)
		return nil, false
	}
	return in, true
}

// writeAlreadyAnswered answers a race, or a repeat.
//
// A phone that taps twice, or two phones tapping the same button, must not get
// an error for agreeing with what already happened: the same answer from the
// same device is reported as success. Anything else is a real conflict — the
// question has been settled differently, and the caller should re-read it.
func (s *server) writeAlreadyAnswered(w http.ResponseWriter, r *http.Request, interactionID, deviceID, wanted string) {
	current, err := s.store().Interactions.ByID(r.Context(), interactionID)
	if err != nil {
		s.writeStoreError(w, r, "interaction", err)
		return
	}
	if current.Status == wanted && current.RespondingDeviceID != nil && *current.RespondingDeviceID == deviceID {
		WriteJSON(w, r, http.StatusOK, interactionReadResponse{Interaction: newInteractionDTO(*current)})
		return
	}
	s.writeConflict(w, r, "That question has already been settled ("+current.Status+").")
}

// handleCancelInteraction withdraws a question.
//
// The owner's session can withdraw any question addressed to them, whoever
// asked it: an agent that crashed after asking should not be able to leave a
// prompt on the Lock Screen until it expires. An API token withdraws only the
// questions it asked itself (§ cancelInteractionForPrincipal) — anybody else's
// is the same 404 as an id that never existed.
func (s *server) handleCancelInteraction(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFrom(r.Context())

	canceled, err := s.cancelInteractionForPrincipal(r.Context(), principal, r.PathValue("id"), s.now())
	switch {
	case err == nil:
		s.resolveInteractionActivity(r, *canceled)
		WriteJSON(w, r, http.StatusOK, interactionReadResponse{Interaction: newInteractionDTO(*canceled)})
		return
	case !errors.Is(err, db.ErrNotFound):
		s.writeInternal(w, r, "canceling an interaction failed", err)
		return
	}

	// The guard did not match: either there is no such question within the
	// caller's reach, or it is no longer pending. The re-read uses the same
	// reach as the cancel, so a foreign question stays a plain 404 rather than
	// leaking its current status through the conflict message.
	current, err := s.interactionForPrincipal(r.Context(), principal, r.PathValue("id"))
	if err != nil {
		s.writeStoreError(w, r, "interaction", err)
		return
	}
	current = s.expireInteractionIfDue(r, current)
	s.writeConflict(w, r, "That question can no longer be canceled ("+current.Status+").")
}

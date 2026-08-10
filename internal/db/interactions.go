package db

import (
	"context"
	"slices"
	"time"
)

// Interaction kinds and the answers each one accepts.
const (
	InteractionApproval = "approval"
	InteractionYesNo    = "yes_no"
	InteractionReply    = "reply"
)

// Interaction statuses. pending is the only non-terminal one; every other
// status is final and nothing ever transitions out of it.
const (
	InteractionPending  = "pending"
	InteractionApproved = "approved"
	InteractionDenied   = "denied"
	InteractionYes      = "yes"
	InteractionNo       = "no"
	InteractionReplied  = "replied"
	InteractionCanceled = "canceled"
	InteractionExpired  = "expired"
)

// How an interaction is presented on the phone.
const (
	// PresentationNotification is an alert with action buttons.
	PresentationNotification = "notification"
	// PresentationLiveActivity puts the question on the Lock Screen, answerable
	// without unlocking.
	PresentationLiveActivity = "live_activity"
)

// Callback delivery states for a webhook interaction that asked to be told the
// answer. NULL means no callback was requested.
const (
	CallbackPending   = "pending"
	CallbackRetrying  = "retrying"
	CallbackDelivered = "delivered"
	CallbackFailed    = "failed"
)

// MaxCallbackErrorLen bounds the stored callback failure message.
const MaxCallbackErrorLen = 200

// answeredStatuses are the statuses that mean a person actually answered, as
// opposed to the question lapsing or being withdrawn. They decide which
// interactions appear in the history feed and which may be deleted from it.
var answeredStatuses = []string{
	InteractionApproved, InteractionDenied, InteractionYes, InteractionNo, InteractionReplied,
}

// AnsweredStatuses returns the statuses that mean a person answered.
func AnsweredStatuses() []string { return slices.Clone(answeredStatuses) }

// ChoicesFor returns the answers a kind accepts. It is derived, never stored
// input: the persisted choices column is written from this.
func ChoicesFor(kind string) []string {
	switch kind {
	case InteractionApproval:
		return []string{"approve", "deny"}
	case InteractionYesNo:
		return []string{"yes", "no"}
	case InteractionReply:
		return []string{"reply"}
	default:
		return nil
	}
}

// StatusForAction maps an answer to the status it produces, reporting false
// when the action does not belong to the kind.
func StatusForAction(kind, action string) (string, bool) {
	switch {
	case kind == InteractionApproval && action == "approve":
		return InteractionApproved, true
	case kind == InteractionApproval && action == "deny":
		return InteractionDenied, true
	case kind == InteractionYesNo && action == "yes":
		return InteractionYes, true
	case kind == InteractionYesNo && action == "no":
		return InteractionNo, true
	case kind == InteractionReply && action == "reply":
		return InteractionReplied, true
	default:
		return "", false
	}
}

// Interaction is a question sent to the phone that expects an answer.
//
// It is created either by an API token (the agent surface) or by a service (a
// webhook carrying a response block), never both, and it can be answered from
// three places: a signed-in app, a push payload's response token, or a Live
// Activity button.
type Interaction struct {
	ID                 string   `db:"id"`
	UserID             string   `db:"user_id"`
	RequesterTokenID   *string  `db:"requester_token_id"`
	RequesterServiceID *string  `db:"requester_service_id"`
	EventID            *string  `db:"event_id"`
	Title              string   `db:"title"`
	Prompt             string   `db:"prompt"`
	Kind               string   `db:"kind"`
	Presentation       string   `db:"presentation"`
	PrimaryLabel       *string  `db:"primary_label"`
	SecondaryLabel     *string  `db:"secondary_label"`
	Status             string   `db:"status"`
	Choices            []string `db:"choices"`
	// Response is the action string for approval and yes_no, the free text for
	// reply.
	Response      *string `db:"response"`
	URL           *string `db:"url"`
	ImageURL      *string `db:"image_url"`
	CorrelationID *string `db:"correlation_id"`
	// ActionDigest binds an answer to the exact question that was displayed: a
	// phone that shows a stale copy of the prompt cannot answer the new one.
	ActionDigest   string  `db:"action_digest"`
	IdempotencyKey *string `db:"idempotency_key"`
	RequestHash    *string `db:"request_hash"`
	// ResponseTokenHash lets the phone answer straight from the push payload,
	// with no session. The plaintext lives only in that payload.
	ResponseTokenHash *string `db:"response_token_hash"`
	// The callback fields drive the outbound delivery worker.
	CallbackURL             *string    `db:"callback_url"`
	CallbackTokenCiphertext *string    `db:"callback_token_ciphertext"`
	CallbackStatus          *string    `db:"callback_status"`
	CallbackAttempts        int        `db:"callback_attempts"`
	CallbackNextAttemptAt   *time.Time `db:"callback_next_attempt_at"`
	CallbackLastError       *string    `db:"callback_last_error"`
	CallbackDeliveredAt     *time.Time `db:"callback_delivered_at"`
	AcceptedCount           int        `db:"accepted_count"`
	RespondingDeviceID      *string    `db:"responding_device_id"`
	ExpiresAt               time.Time  `db:"expires_at"`
	CreatedAt               time.Time  `db:"created_at"`
	RespondedAt             *time.Time `db:"responded_at"`
	CanceledAt              *time.Time `db:"canceled_at"`
}

// Terminal reports whether the interaction has reached a final status.
func (i Interaction) Terminal() bool { return i.Status != InteractionPending }

// Answered reports whether a person actually answered.
func (i Interaction) Answered() bool { return slices.Contains(answeredStatuses, i.Status) }

// Live reports whether the interaction can still be answered at now.
func (i Interaction) Live(now time.Time) bool {
	return i.Status == InteractionPending && i.ExpiresAt.After(now)
}

// InteractionListItem is an interaction with the name of whatever asked the
// question, which is what the inbox shows above the prompt.
type InteractionListItem struct {
	Interaction
	SourceName     string  `db:"source_name"`
	SourceImageURL *string `db:"source_image_url"`
}

// Interactions stores questions and their answers.
type Interactions struct{ q Querier }

const interactionColumns = `id, user_id, requester_token_id, requester_service_id, event_id,
	title, prompt, kind, presentation, primary_label, secondary_label, status, choices,
	response, url, image_url, correlation_id, action_digest, idempotency_key, request_hash,
	response_token_hash, callback_url, callback_token_ciphertext, callback_status,
	callback_attempts, callback_next_attempt_at, callback_last_error, callback_delivered_at,
	accepted_count, responding_device_id, expires_at, created_at, responded_at, canceled_at`

const interactionColumnsQualified = `i.id, i.user_id, i.requester_token_id, i.requester_service_id,
	i.event_id, i.title, i.prompt, i.kind, i.presentation, i.primary_label, i.secondary_label,
	i.status, i.choices, i.response, i.url, i.image_url, i.correlation_id, i.action_digest,
	i.idempotency_key, i.request_hash, i.response_token_hash, i.callback_url,
	i.callback_token_ciphertext, i.callback_status, i.callback_attempts,
	i.callback_next_attempt_at, i.callback_last_error, i.callback_delivered_at,
	i.accepted_count, i.responding_device_id, i.expires_at, i.created_at, i.responded_at,
	i.canceled_at`

// CreateInteractionParams asks a question.
//
// Exactly one of RequesterTokenID and RequesterServiceID must be set; the
// database enforces it. The webhook flow additionally sets EventID,
// CorrelationID, ResponseTokenHash and the callback fields, none of which the
// agent flow uses.
type CreateInteractionParams struct {
	ID                 string
	UserID             string
	RequesterTokenID   *string
	RequesterServiceID *string
	EventID            *string
	Title              string
	Prompt             string
	Kind               string
	Presentation       string
	PrimaryLabel       *string
	SecondaryLabel     *string
	Choices            []string
	URL                *string
	ImageURL           *string
	CorrelationID      *string
	ActionDigest       string
	IdempotencyKey     *string
	RequestHash        *string
	ResponseTokenHash  *string
	// Callback fields. When CallbackURL is set the row starts in
	// CallbackPending with its first attempt due immediately.
	CallbackURL             *string
	CallbackTokenCiphertext *string
	ExpiresAt               time.Time
	Now                     time.Time
}

// Create inserts a pending interaction.
func (s *Interactions) Create(ctx context.Context, p CreateInteractionParams) (*Interaction, error) {
	const q = `
		INSERT INTO interactions (
			id, user_id, requester_token_id, requester_service_id, event_id, title, prompt,
			kind, presentation, primary_label, secondary_label, status, choices, url, image_url,
			correlation_id, action_digest, idempotency_key, request_hash, response_token_hash,
			callback_url, callback_token_ciphertext, callback_status, callback_next_attempt_at,
			expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'pending', $12, $13, $14,
		        $15, $16, $17, $18, $19, $20, $21,
		        CASE WHEN $20::text IS NULL THEN NULL ELSE 'pending' END,
		        CASE WHEN $20::text IS NULL THEN NULL ELSE $22::timestamptz END,
		        $23, $22)
		RETURNING ` + interactionColumns
	return queryOne[Interaction](ctx, s.q, "create interaction", q,
		p.ID, p.UserID, p.RequesterTokenID, p.RequesterServiceID, p.EventID, p.Title, p.Prompt,
		p.Kind, p.Presentation, p.PrimaryLabel, p.SecondaryLabel, p.Choices, p.URL, p.ImageURL,
		p.CorrelationID, p.ActionDigest, p.IdempotencyKey, p.RequestHash, p.ResponseTokenHash,
		p.CallbackURL, p.CallbackTokenCiphertext, Millis(p.Now), Millis(p.ExpiresAt))
}

// ByID loads one interaction.
func (s *Interactions) ByID(ctx context.Context, id string) (*Interaction, error) {
	const q = `SELECT ` + interactionColumns + ` FROM interactions WHERE id = $1`
	return queryOne[Interaction](ctx, s.q, "load interaction", q, id)
}

// ByIDForUser loads an interaction addressed to the caller.
func (s *Interactions) ByIDForUser(ctx context.Context, id, userID string) (*Interaction, error) {
	const q = `SELECT ` + interactionColumns + ` FROM interactions WHERE id = $1 AND user_id = $2`
	return queryOne[Interaction](ctx, s.q, "load interaction", q, id, userID)
}

// ByIDForToken loads an interaction the calling token asked. A token cannot
// read another token's questions, so this is the agent surface's only lookup.
func (s *Interactions) ByIDForToken(ctx context.Context, id, tokenID string) (*Interaction, error) {
	const q = `SELECT ` + interactionColumns + ` FROM interactions
		WHERE id = $1 AND requester_token_id = $2`
	return queryOne[Interaction](ctx, s.q, "load interaction", q, id, tokenID)
}

// ByEventForService loads the interaction a webhook event spawned, scoped to
// the service that owns the event.
func (s *Interactions) ByEventForService(ctx context.Context, eventID, serviceID string) (*Interaction, error) {
	const q = `SELECT ` + interactionColumns + ` FROM interactions
		WHERE event_id = $1 AND requester_service_id = $2`
	return queryOne[Interaction](ctx, s.q, "load event response", q, eventID, serviceID)
}

// ByIdempotencyKey looks up an earlier question from the same token with the
// same key, for the replay comparison.
func (s *Interactions) ByIdempotencyKey(ctx context.Context, tokenID, key string) (*Interaction, error) {
	const q = `SELECT ` + interactionColumns + ` FROM interactions
		WHERE requester_token_id = $1 AND idempotency_key = $2`
	return queryOne[Interaction](ctx, s.q, "load interaction by idempotency key", q, tokenID, key)
}

// ListInteractionsParams pages the account's questions.
type ListInteractionsParams struct {
	UserID string
	// PendingOnly keeps the inbox: questions still awaiting an answer and not
	// yet past their deadline.
	PendingOnly bool
	Now         time.Time
	Cursor      Cursor
	Limit       int
}

// List pages the account's questions, newest first.
//
// The pending filter excludes expired rows rather than expiring them: this is a
// read of an inbox, and rewriting rows on a plain list would make a phone
// opening the app resolve every stale question at once. Expiry happens on the
// paths that can act on a row.
func (s *Interactions) List(ctx context.Context, p ListInteractionsParams) (Page[InteractionListItem], error) {
	const q = `
		SELECT ` + interactionColumnsQualified + `,
		       coalesce(sv.title, t.name, i.title)  AS source_name,
		       coalesce(i.image_url, sv.image_url)  AS source_image_url
		FROM interactions i
		LEFT JOIN services sv  ON sv.id = i.requester_service_id
		LEFT JOIN api_tokens t ON t.id  = i.requester_token_id
		WHERE i.user_id = $1
		  AND (NOT $2::boolean OR (i.status = 'pending' AND i.expires_at > $3))
		  AND ($4::timestamptz IS NULL OR (i.created_at, i.id) < ($4, $5))
		ORDER BY i.created_at DESC, i.id DESC
		LIMIT $6`

	limit := ClampLimit(p.Limit)
	at, id := p.Cursor.args()
	rows, err := queryAll[InteractionListItem](ctx, s.q, "list interactions", q,
		p.UserID, p.PendingOnly, Millis(p.Now), at, id, limit+1)
	if err != nil {
		return Page[InteractionListItem]{}, err
	}
	return paginate(rows, limit, func(i InteractionListItem) Cursor {
		return Cursor{Time: i.CreatedAt, ID: i.ID}
	}), nil
}

// ListPending returns the questions still awaiting an answer, newest first.
func (s *Interactions) ListPending(ctx context.Context, userID string, now time.Time, cursor Cursor, limit int) (Page[InteractionListItem], error) {
	return s.List(ctx, ListInteractionsParams{
		UserID: userID, PendingOnly: true, Now: now, Cursor: cursor, Limit: limit,
	})
}

// ExpireIfDue retires a pending interaction whose deadline has passed.
//
// There is no expiry job: every path that can act on an interaction runs this
// first. A caller that gets [ErrNotFound] should re-read — either the row was
// not due, or another request expired it first.
func (s *Interactions) ExpireIfDue(ctx context.Context, id string, now time.Time) (*Interaction, error) {
	const q = `
		UPDATE interactions SET status = 'expired'
		WHERE id = $1 AND status = 'pending' AND expires_at <= $2
		RETURNING ` + interactionColumns
	return queryOne[Interaction](ctx, s.q, "expire interaction", q, id, Millis(now))
}

// RespondParams records an answer.
type RespondParams struct {
	ID     string
	UserID string
	// Status is the terminal status the action maps to; use
	// [StatusForAction] so a mismatched action cannot be written.
	Status string
	// Response is the reply text, or the action string for the other kinds.
	Response *string
	// DeviceID attributes the answer to the phone that gave it.
	DeviceID *string
	// TriggerCallback arms the outbound callback worker for this row. It is
	// set on every successful answer that has a callback configured.
	TriggerCallback bool
	Now             time.Time
}

// Respond records an answer, guarded on the interaction still being pending and
// unexpired.
//
// [ErrNotFound] means the question was already answered, canceled or expired —
// the caller re-reads and reports the conflict with the current state. This
// guard is the entire concurrency story for interactions: there are no locks,
// and two phones racing to answer produce exactly one winner.
func (s *Interactions) Respond(ctx context.Context, p RespondParams) (*Interaction, error) {
	const q = `
		UPDATE interactions SET
			status               = $3,
			response             = $4,
			responding_device_id = $5,
			responded_at         = $6,
			callback_next_attempt_at = CASE
				WHEN $7::boolean AND callback_status IN ('pending', 'retrying') THEN $6
				ELSE callback_next_attempt_at END
		WHERE id = $1 AND user_id = $2 AND status = 'pending' AND expires_at > $6
		RETURNING ` + interactionColumns
	return queryOne[Interaction](ctx, s.q, "respond to interaction", q,
		p.ID, p.UserID, p.Status, p.Response, p.DeviceID, Millis(p.Now), p.TriggerCallback)
}

// CancelForToken withdraws a pending question the calling token asked.
func (s *Interactions) CancelForToken(ctx context.Context, id, tokenID string, now time.Time) (*Interaction, error) {
	const q = `
		UPDATE interactions SET status = 'canceled', canceled_at = $3
		WHERE id = $1 AND requester_token_id = $2 AND status = 'pending' AND expires_at > $3
		RETURNING ` + interactionColumns
	return queryOne[Interaction](ctx, s.q, "cancel interaction", q, id, tokenID, Millis(now))
}

// CancelForUser withdraws a pending question on the account, whoever asked it.
//
// The owner can always withdraw a question addressed to them: an agent that
// crashed after asking should not be able to leave a prompt on the Lock Screen
// until it expires.
func (s *Interactions) CancelForUser(ctx context.Context, id, userID string, now time.Time) (*Interaction, error) {
	const q = `
		UPDATE interactions SET status = 'canceled', canceled_at = $3
		WHERE id = $1 AND user_id = $2 AND status = 'pending' AND expires_at > $3
		RETURNING ` + interactionColumns
	return queryOne[Interaction](ctx, s.q, "cancel interaction", q, id, userID, Millis(now))
}

// CancelForEvent withdraws the pending question a webhook event spawned.
//
// Unlike the agent path this does not require the interaction to be unexpired:
// a service cancelling its own request should succeed even when the deadline
// has just passed and nothing has lazily expired the row yet. The outcome is
// the same for the phone either way — the question is gone.
func (s *Interactions) CancelForEvent(ctx context.Context, eventID, serviceID string, now time.Time) (*Interaction, error) {
	const q = `
		UPDATE interactions SET status = 'canceled', canceled_at = $3
		WHERE event_id = $1 AND requester_service_id = $2 AND status = 'pending'
		RETURNING ` + interactionColumns
	return queryOne[Interaction](ctx, s.q, "cancel event response", q, eventID, serviceID, Millis(now))
}

// SettleAccepted records how many devices APNs took the question for.
func (s *Interactions) SettleAccepted(ctx context.Context, id string, accepted int) (*Interaction, error) {
	const q = `UPDATE interactions SET accepted_count = $2 WHERE id = $1
		RETURNING ` + interactionColumns
	return queryOne[Interaction](ctx, s.q, "settle interaction", q, id, accepted)
}

// ByResponseToken resolves the credential embedded in a push payload. The
// digest is compared in SQL, so an unknown id and a wrong token are one
// outcome and neither confirms the other.
func (s *Interactions) ByResponseToken(ctx context.Context, id, tokenHash string) (*Interaction, error) {
	const q = `SELECT ` + interactionColumns + ` FROM interactions
		WHERE id = $1 AND response_token_hash = $2`
	return queryOne[Interaction](ctx, s.q, "load interaction by response token", q, id, tokenHash)
}

// CountForTokenSince counts one token's questions inside the rate-limit window.
func (s *Interactions) CountForTokenSince(ctx context.Context, tokenID string, since time.Time) (int, error) {
	const q = `SELECT count(*) FROM interactions WHERE requester_token_id = $1 AND created_at >= $2`
	return queryValue[int](ctx, s.q, "count token interactions", q, tokenID, Millis(since))
}

// CountForServiceSince counts one service's questions inside the window.
func (s *Interactions) CountForServiceSince(ctx context.Context, serviceID string, since time.Time) (int, error) {
	const q = `SELECT count(*) FROM interactions WHERE requester_service_id = $1 AND created_at >= $2`
	return queryValue[int](ctx, s.q, "count service interactions", q, serviceID, Millis(since))
}

// CountForUserSince counts the account's questions inside the window.
func (s *Interactions) CountForUserSince(ctx context.Context, userID string, since time.Time) (int, error) {
	const q = `SELECT count(*) FROM interactions WHERE user_id = $1 AND created_at >= $2`
	return queryValue[int](ctx, s.q, "count account interactions", q, userID, Millis(since))
}

// Delete removes an answered interaction from the account's history. Pending
// questions cannot be deleted: they must be answered, canceled or left to
// expire, so nothing can make a live prompt silently vanish from the phone.
func (s *Interactions) Delete(ctx context.Context, id, userID string) (bool, error) {
	const q = `
		DELETE FROM interactions
		WHERE id = $1 AND user_id = $2
		  AND status IN ('approved', 'denied', 'yes', 'no', 'replied')`
	return execMatched(ctx, s.q, "delete interaction", q, id, userID)
}

// ClaimDueCallbacks takes ownership of up to limit callbacks that are due.
//
// The claim is a lease: the rows' next attempt is pushed forward by lease
// before they are returned, and SKIP LOCKED means a second worker takes
// different rows rather than blocking. Together those make it safe to run the
// worker in more than one process — without them two replicas would deliver
// every callback twice.
//
// A worker that crashes mid-flight loses nothing: the lease simply expires and
// the row becomes due again. Delivery therefore stays at-least-once, never
// exactly-once — a crash between sending and settling legitimately repeats a
// callback, which is why receivers must treat interaction_id as idempotent.
//
// Callers should ask only for what they can start immediately: the lease clock
// runs from the claim, so a row claimed into a queue spends its lease standing
// still, and one that outlives the lease is handed to the next worker while
// the first is still holding it.
func (s *Interactions) ClaimDueCallbacks(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]Interaction, error) {
	const q = `
		WITH due AS (
			SELECT id FROM interactions
			WHERE status IN ('approved', 'denied', 'yes', 'no', 'replied')
			  AND callback_status IN ('pending', 'retrying')
			  AND callback_url IS NOT NULL
			  AND callback_token_ciphertext IS NOT NULL
			  AND callback_next_attempt_at <= $1
			ORDER BY callback_next_attempt_at
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		UPDATE interactions i SET callback_next_attempt_at = $3
		FROM due WHERE i.id = due.id
		RETURNING ` + interactionColumnsQualified
	stamp := Millis(now)
	return queryAll[Interaction](ctx, s.q, "claim due callbacks", q,
		stamp, limit, stamp.Add(lease))
}

// SettleCallbackParams records the outcome of one callback attempt.
type SettleCallbackParams struct {
	ID       string
	Attempts int
	// Status is one of CallbackDelivered, CallbackRetrying or CallbackFailed.
	Status string
	// NextAttemptAt is set only for CallbackRetrying; delivered and failed
	// rows carry no next attempt, which is what takes them out of the worker's
	// query for good.
	NextAttemptAt *time.Time
	// LastError is truncated to MaxCallbackErrorLen.
	LastError *string
	Now       time.Time
}

// SettleCallback records a delivery attempt's result.
func (s *Interactions) SettleCallback(ctx context.Context, p SettleCallbackParams) (*Interaction, error) {
	const q = `
		UPDATE interactions SET
			callback_status          = $2,
			callback_attempts        = $3,
			callback_next_attempt_at = $4,
			callback_last_error      = left($5, $6),
			callback_delivered_at    = CASE WHEN $2 = 'delivered' THEN $7 ELSE callback_delivered_at END
		WHERE id = $1
		RETURNING ` + interactionColumns
	return queryOne[Interaction](ctx, s.q, "settle callback", q,
		p.ID, p.Status, p.Attempts, millisPtr(p.NextAttemptAt), p.LastError,
		MaxCallbackErrorLen, Millis(p.Now))
}

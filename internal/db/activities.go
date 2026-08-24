package db

import (
	"context"
	"encoding/json"
	"time"
)

// Live Activity statuses. starting, active and partial are live; failed, ended
// and expired are terminal.
const (
	// ActivityStarting means the start fan-out has not settled yet.
	ActivityStarting = "starting"
	// ActivityActive means every delivery APNs was asked for was accepted.
	ActivityActive = "active"
	// ActivityPartial means some deliveries failed and some did not.
	ActivityPartial = "partial"
	// ActivityFailed means nothing was accepted.
	ActivityFailed = "failed"
	// ActivityEnded means the requester ended it, it was replaced, or its
	// interaction resolved.
	ActivityEnded = "ended"
	// ActivityExpired means its deadline passed while it was still live.
	ActivityExpired = "expired"
)

// Live Activity operation events.
const (
	OperationStart  = "start"
	OperationUpdate = "update"
	OperationEnd    = "end"
)

// Delivery purposes. The distinction is load-bearing: a phone shows at most one
// task activity at a time, while an interaction activity may sit alongside it.
const (
	PurposeTask        = "task"
	PurposeInteraction = "interaction"
)

// LiveActivity is the logical activity: one requester-visible thing on a Lock
// Screen, materialised per device by [LiveActivityDelivery].
type LiveActivity struct {
	ID                 string  `db:"id"`
	UserID             string  `db:"user_id"`
	RequesterTokenID   *string `db:"requester_token_id"`
	RequesterServiceID *string `db:"requester_service_id"`
	// InteractionID marks an interactive activity: one that presents a question
	// on the Lock Screen. Those are hidden from the ordinary activity surfaces
	// and are driven by their interaction rather than by the requester.
	InteractionID *string `db:"interaction_id"`
	// Key is a requester-chosen handle, unique among that requester's live
	// activities and reusable once this one ends.
	Key           *string `db:"key"`
	SchemaVersion int     `db:"schema_version"`
	// Props is the ActivityKit content-state document.
	Props  json.RawMessage `db:"props"`
	Status string          `db:"status"`
	// Sequence is the optimistic-concurrency token: every mutation is guarded
	// on the value it read.
	Sequence int `db:"sequence"`
	// APNsTimestamp is epoch seconds, but as a strictly increasing counter
	// rather than a clock — ActivityKit uses it to drop out-of-order pushes, so
	// two mutations inside one second still produce increasing values.
	APNsTimestamp int64 `db:"apns_timestamp"`
	// AcceptedCount and FailedCount describe the most recent operation, not the
	// activity's lifetime.
	AcceptedCount  int       `db:"accepted_count"`
	FailedCount    int       `db:"failed_count"`
	IdempotencyKey *string   `db:"idempotency_key"`
	RequestHash    *string   `db:"request_hash"`
	ExpiresAt      time.Time `db:"expires_at"`
	// StaleAt and DismissalAt become the APNs stale-date and dismissal-date.
	StaleAt     *time.Time `db:"stale_at"`
	DismissalAt *time.Time `db:"dismissal_at"`
	CreatedAt   time.Time  `db:"created_at"`
	UpdatedAt   time.Time  `db:"updated_at"`
	EndedAt     *time.Time `db:"ended_at"`
}

// Live reports whether the activity is still running.
func (a LiveActivity) Live() bool {
	return a.Status == ActivityStarting || a.Status == ActivityActive || a.Status == ActivityPartial
}

// Terminal reports whether the activity has finished.
func (a LiveActivity) Terminal() bool { return !a.Live() }

// Interactive reports whether the activity presents an interaction.
func (a LiveActivity) Interactive() bool { return a.InteractionID != nil }

// ActivityListItem includes the name of the activity requester.
type ActivityListItem struct {
	LiveActivity
	SourceName     string  `db:"source_name"`
	SourceImageURL *string `db:"source_image_url"`
}

// Activities stores Live Activities.
type Activities struct {
	q     Querier
	store *Store
}

const activityColumns = `id, user_id, requester_token_id, requester_service_id, interaction_id,
	key, schema_version, props, status, sequence, apns_timestamp, accepted_count, failed_count,
	idempotency_key, request_hash, expires_at, stale_at, dismissal_at, created_at, updated_at, ended_at`

const activityColumnsQualified = `a.id, a.user_id, a.requester_token_id, a.requester_service_id,
	a.interaction_id, a.key, a.schema_version, a.props, a.status, a.sequence, a.apns_timestamp,
	a.accepted_count, a.failed_count, a.idempotency_key, a.request_hash, a.expires_at,
	a.stale_at, a.dismissal_at, a.created_at, a.updated_at, a.ended_at`

// ActivityTarget is one device a start will be delivered to.
type ActivityTarget struct {
	DeliveryID string
	DeviceID   string
	// Environment is copied from the device: an ActivityKit token only works
	// against the APNs environment it was minted in.
	Environment string
	// Purpose is PurposeTask or PurposeInteraction.
	Purpose string
}

// StartActivityParams creates an activity, its start operation, and one
// delivery per target device.
type StartActivityParams struct {
	ID                 string
	UserID             string
	RequesterTokenID   *string
	RequesterServiceID *string
	InteractionID      *string
	Key                *string
	SchemaVersion      int
	Props              json.RawMessage
	IdempotencyKey     *string
	RequestHash        *string
	ExpiresAt          time.Time
	StaleAt            *time.Time
	OperationID        string
	Targets            []ActivityTarget
	Now                time.Time
}

// StartedActivity is everything a start wrote.
type StartedActivity struct {
	Activity   LiveActivity
	Operation  LiveActivityOperation
	Deliveries []LiveActivityDelivery
}

// Start writes the activity, its start operation and its deliveries as one
// unit.
//
// They must land together: a delivery without its activity is unpushable, and
// an operation without its deliveries would meter a start that never happened.
// The unique indexes do the conflict detection — a duplicate idempotency key,
// a key already held by a live activity, or a device that already hosts a task
// activity each abort the transaction, and the caller re-reads to decide
// between replaying, reporting a conflict, and taking over.
func (s *Activities) Start(ctx context.Context, p StartActivityParams) (*StartedActivity, error) {
	var out StartedActivity
	err := s.store.Tx(ctx, func(ctx context.Context, tx *Store) error {
		activity, err := tx.Activities.insert(ctx, p)
		if err != nil {
			return err
		}
		op, err := tx.Operations.Insert(ctx, CreateOperationParams{
			ID:                 p.OperationID,
			ActivityID:         activity.ID,
			RequesterTokenID:   p.RequesterTokenID,
			RequesterServiceID: p.RequesterServiceID,
			Event:              OperationStart,
			Sequence:           activity.Sequence,
			Props:              p.Props,
			IdempotencyKey:     p.IdempotencyKey,
			RequestHash:        p.RequestHash,
			Now:                p.Now,
		})
		if err != nil {
			return err
		}
		deliveries, err := tx.Deliveries.InsertMany(ctx, activity.ID, p.SchemaVersion, p.Targets, p.Now)
		if err != nil {
			return err
		}
		out = StartedActivity{Activity: *activity, Operation: *op, Deliveries: deliveries}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *Activities) insert(ctx context.Context, p StartActivityParams) (*LiveActivity, error) {
	const q = `
		INSERT INTO live_activities (id, user_id, requester_token_id, requester_service_id,
		                             interaction_id, key, schema_version, props, status, sequence,
		                             apns_timestamp, idempotency_key, request_hash,
		                             expires_at, stale_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'starting', 0, $9, $10, $11, $12, $13, $14, $14)
		RETURNING ` + activityColumns
	now := Millis(p.Now)
	return queryOne[LiveActivity](ctx, s.q, "create Live Activity", q,
		p.ID, p.UserID, p.RequesterTokenID, p.RequesterServiceID, p.InteractionID, p.Key,
		p.SchemaVersion, p.Props, now.Unix(), p.IdempotencyKey, p.RequestHash,
		Millis(p.ExpiresAt), millisPtr(p.StaleAt), now)
}

// ByID loads one activity.
func (s *Activities) ByID(ctx context.Context, id string) (*LiveActivity, error) {
	const q = `SELECT ` + activityColumns + ` FROM live_activities WHERE id = $1`
	return queryOne[LiveActivity](ctx, s.q, "load Live Activity", q, id)
}

// ByIDs loads several activities, which the takeover path needs after grouping
// blocking deliveries by activity.
func (s *Activities) ByIDs(ctx context.Context, ids []string) ([]LiveActivity, error) {
	const q = `SELECT ` + activityColumns + ` FROM live_activities WHERE id = ANY($1)`
	return queryAll[LiveActivity](ctx, s.q, "load Live Activities", q, ids)
}

// ByInteractionID loads the activity presenting an interaction, if any.
func (s *Activities) ByInteractionID(ctx context.Context, interactionID string) (*LiveActivity, error) {
	const q = `SELECT ` + activityColumns + ` FROM live_activities WHERE interaction_id = $1`
	return queryOne[LiveActivity](ctx, s.q, "load interaction Live Activity", q, interactionID)
}

// ResolveParams addresses an activity by id or by key.
type ResolveParams struct {
	// Identifier is matched against both the activity id and its key, because
	// the API lets a requester use either.
	Identifier string
	// Exactly one requester scope is set; only the creator can address the
	// activity.
	RequesterTokenID   *string
	RequesterServiceID *string
	// IncludeInteractive admits interaction-backed activities. The agent
	// surface leaves it false: those activities are driven by their
	// interaction, and letting a requester patch one directly would put the
	// Lock Screen out of step with the question it is showing.
	IncludeInteractive bool
}

// Resolve finds an activity by id or key.
//
// A key is reusable once its activity ends, so an identifier can match one live
// row plus any number of finished ones. Live rows win, then the newest — which
// is what a requester means by "the deploy activity".
func (s *Activities) Resolve(ctx context.Context, p ResolveParams) (*LiveActivity, error) {
	const q = `
		SELECT ` + activityColumns + ` FROM live_activities
		WHERE (id = $1 OR key = $1)
		  AND requester_token_id   IS NOT DISTINCT FROM $2
		  AND requester_service_id IS NOT DISTINCT FROM $3
		  AND ($4::boolean OR interaction_id IS NULL)
		ORDER BY (status IN ('starting', 'active', 'partial')) DESC, created_at DESC, id DESC
		LIMIT 1`
	return queryOne[LiveActivity](ctx, s.q, "resolve Live Activity", q,
		p.Identifier, p.RequesterTokenID, p.RequesterServiceID, p.IncludeInteractive)
}

// ByIdempotencyKey looks up an earlier start from the same requester with the
// same key. Starts key off the activity; updates and ends key off the
// operation, because the same key may legitimately be reused across the
// lifetime of one activity.
//
// The requester must match exactly — a token's key never resolves a service's
// row — so pass the one identifier the caller authenticated as and leave the
// other nil.
func (s *Activities) ByIdempotencyKey(ctx context.Context, tokenID, serviceID *string, key string) (*LiveActivity, error) {
	const q = `
		SELECT ` + activityColumns + ` FROM live_activities
		WHERE idempotency_key = $3
		  AND requester_token_id   IS NOT DISTINCT FROM $1
		  AND requester_service_id IS NOT DISTINCT FROM $2
		LIMIT 1`
	return queryOne[LiveActivity](ctx, s.q, "load Live Activity by idempotency key", q, tokenID, serviceID, key)
}

// KeyHolder returns the live activity currently holding a key for a requester,
// which is what a start has to displace or refuse. Like every requester-scoped
// lookup it matches one requester exactly.
func (s *Activities) KeyHolder(ctx context.Context, tokenID, serviceID *string, key string) (*LiveActivity, error) {
	const q = `
		SELECT ` + activityColumns + ` FROM live_activities
		WHERE key = $3
		  AND status IN ('starting', 'active', 'partial')
		  AND requester_token_id   IS NOT DISTINCT FROM $1
		  AND requester_service_id IS NOT DISTINCT FROM $2
		LIMIT 1`
	return queryOne[LiveActivity](ctx, s.q, "load Live Activity key holder", q, tokenID, serviceID, key)
}

// ResolveForUser finds an activity by id or key anywhere on the account.
//
// It is the owner's read: the dashboard shows every activity on the Lock
// Screen, not only the ones one credential started. Interaction-backed
// activities stay hidden — they are shown as the question they present.
func (s *Activities) ResolveForUser(ctx context.Context, identifier, userID string) (*LiveActivity, error) {
	const q = `
		SELECT ` + activityColumns + ` FROM live_activities
		WHERE (id = $1 OR key = $1) AND user_id = $2 AND interaction_id IS NULL
		ORDER BY (status IN ('starting', 'active', 'partial')) DESC, created_at DESC, id DESC
		LIMIT 1`
	return queryOne[LiveActivity](ctx, s.q, "resolve Live Activity", q, identifier, userID)
}

// ListActivitiesParams pages the account's activities.
type ListActivitiesParams struct {
	UserID string
	// LiveOnly keeps the ones still on a Lock Screen: non-terminal and not past
	// their deadline.
	LiveOnly bool
	Now      time.Time
	Cursor   Cursor
	Limit    int
}

// List pages the account's activities newest first and includes each requester
// name.
//
// Ordering is by creation rather than by last update: a paged list has to be
// stable, and updated_at moves under the reader's feet on every push.
func (s *Activities) List(ctx context.Context, p ListActivitiesParams) (Page[ActivityListItem], error) {
	const q = `
		SELECT ` + activityColumnsQualified + `,
		       coalesce(sv.title, t.name, a.props->>'title', 'Hark') AS source_name,
		       sv.image_url AS source_image_url
		FROM live_activities a
		LEFT JOIN services sv  ON sv.id = a.requester_service_id
		LEFT JOIN api_tokens t ON t.id  = a.requester_token_id
		WHERE a.user_id = $1
		  AND a.interaction_id IS NULL
		  AND (NOT $2::boolean OR (a.status IN ('starting', 'active', 'partial') AND a.expires_at > $3))
		  AND ($4::timestamptz IS NULL OR (a.created_at, a.id) < ($4, $5))
		ORDER BY a.created_at DESC, a.id DESC
		LIMIT $6`

	limit := ClampLimit(p.Limit)
	at, id := p.Cursor.args()
	rows, err := queryAll[ActivityListItem](ctx, s.q, "list activities", q,
		p.UserID, p.LiveOnly, Millis(p.Now), at, id, limit+1)
	if err != nil {
		return Page[ActivityListItem]{}, err
	}
	return paginate(rows, limit, func(a ActivityListItem) Cursor {
		return Cursor{Time: a.CreatedAt, ID: a.ID}
	}), nil
}

// ListForToken pages the activities one token started, newest first.
// Interaction-backed activities are excluded: they belong to their question.
func (s *Activities) ListForToken(ctx context.Context, tokenID string, cursor Cursor, limit int) (Page[LiveActivity], error) {
	const q = `
		SELECT ` + activityColumns + ` FROM live_activities
		WHERE requester_token_id = $1
		  AND interaction_id IS NULL
		  AND ($2::timestamptz IS NULL OR (created_at, id) < ($2, $3))
		ORDER BY created_at DESC, id DESC
		LIMIT $4`

	limit = ClampLimit(limit)
	at, id := cursor.args()
	rows, err := queryAll[LiveActivity](ctx, s.q, "list Live Activities", q, tokenID, at, id, limit+1)
	if err != nil {
		return Page[LiveActivity]{}, err
	}
	return paginate(rows, limit, func(a LiveActivity) Cursor {
		return Cursor{Time: a.CreatedAt, ID: a.ID}
	}), nil
}

// ListLiveForUser returns the account's running activities, most recently
// updated first — the dashboard's "what is on my Lock Screen right now".
//
// Rows whose deadline has passed are excluded rather than expired here; the
// caller expires the ones it acts on.
func (s *Activities) ListLiveForUser(ctx context.Context, userID string, now time.Time, limit int) ([]ActivityListItem, error) {
	const q = `
		SELECT ` + activityColumnsQualified + `,
		       coalesce(sv.title, t.name, a.props->>'title', 'Hark') AS source_name,
		       sv.image_url AS source_image_url
		FROM live_activities a
		LEFT JOIN services sv  ON sv.id = a.requester_service_id
		LEFT JOIN api_tokens t ON t.id  = a.requester_token_id
		WHERE a.user_id = $1
		  AND a.interaction_id IS NULL
		  AND a.status IN ('starting', 'active', 'partial')
		  AND a.expires_at > $2
		ORDER BY a.updated_at DESC, a.id DESC
		LIMIT $3`
	return queryAll[ActivityListItem](ctx, s.q, "list live activities", q, userID, Millis(now), ClampLimit(limit))
}

// ExpireIfDue retires a running activity whose deadline has passed, which also
// frees its key for reuse. Like interactions, expiry is lazy: every read that
// can act on an activity runs this first.
func (s *Activities) ExpireIfDue(ctx context.Context, id string, now time.Time) (*LiveActivity, error) {
	const q = `
		UPDATE live_activities SET status = 'expired', ended_at = $2, updated_at = $2
		WHERE id = $1 AND status IN ('starting', 'active', 'partial') AND expires_at <= $2
		RETURNING ` + activityColumns
	return queryOne[LiveActivity](ctx, s.q, "expire Live Activity", q, id, Millis(now))
}

// MutateActivityParams applies one requester-initiated change.
type MutateActivityParams struct {
	ActivityID string
	// ExpectedSequence is the sequence the caller read. The update matches only
	// while the activity is still at it.
	ExpectedSequence int
	// Event is OperationUpdate or OperationEnd; an end also moves the activity
	// to its terminal status.
	Event string
	Props json.RawMessage
	// StaleAt and DismissalAt are left alone unless set.
	StaleAt            Set[*time.Time]
	DismissalAt        Set[*time.Time]
	OperationID        string
	RequesterTokenID   *string
	RequesterServiceID *string
	IdempotencyKey     *string
	RequestHash        *string
	Now                time.Time
}

// Mutate applies an update or an end and records the operation that caused it,
// in one transaction.
//
// The guard is the whole concurrency model for activities: the update matches
// only while the activity is at the sequence the caller read and still live, so
// two requesters racing produce one winner and one [ErrNotFound], which the
// caller reports as a sequence conflict. Because the operation is written in
// the same transaction, an operation exists exactly when its mutation landed —
// so the history never shows a change that did not happen.
func (s *Activities) Mutate(ctx context.Context, p MutateActivityParams) (*LiveActivity, *LiveActivityOperation, error) {
	var (
		activity  *LiveActivity
		operation *LiveActivityOperation
	)
	err := s.store.Tx(ctx, func(ctx context.Context, tx *Store) error {
		updated, err := tx.Activities.applyMutation(ctx, p)
		if err != nil {
			return err
		}
		op, err := tx.Operations.Insert(ctx, CreateOperationParams{
			ID:                 p.OperationID,
			ActivityID:         updated.ID,
			RequesterTokenID:   p.RequesterTokenID,
			RequesterServiceID: p.RequesterServiceID,
			Event:              p.Event,
			Sequence:           updated.Sequence,
			Props:              p.Props,
			IdempotencyKey:     p.IdempotencyKey,
			RequestHash:        p.RequestHash,
			Now:                p.Now,
		})
		if err != nil {
			return err
		}
		activity, operation = updated, op
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return activity, operation, nil
}

func (s *Activities) applyMutation(ctx context.Context, p MutateActivityParams) (*LiveActivity, error) {
	const q = `
		UPDATE live_activities SET
			props          = $4,
			sequence       = sequence + 1,
			apns_timestamp = greatest($5, apns_timestamp + 1),
			stale_at       = CASE WHEN $6::boolean  THEN $7::timestamptz  ELSE stale_at     END,
			dismissal_at   = CASE WHEN $8::boolean  THEN $9::timestamptz  ELSE dismissal_at END,
			status         = CASE WHEN $10::boolean THEN 'ended'          ELSE status       END,
			ended_at       = CASE WHEN $10::boolean THEN $3               ELSE ended_at     END,
			updated_at     = $3
		WHERE id = $1 AND sequence = $2 AND status IN ('starting', 'active', 'partial')
		RETURNING ` + activityColumns

	now := Millis(p.Now)
	staleSet, staleAt := p.StaleAt.args()
	dismissSet, dismissAt := p.DismissalAt.args()
	return queryOne[LiveActivity](ctx, s.q, "mutate Live Activity", q,
		p.ActivityID, p.ExpectedSequence, now, p.Props, now.Unix(),
		staleSet, millisPtr(staleAt), dismissSet, millisPtr(dismissAt),
		p.Event == OperationEnd)
}

// EndUnmetered ends an activity without recording an operation.
//
// Two paths use it, and both are consequences of something else rather than
// requests in their own right: a start taking over the device or key an older
// activity held, and an activity whose deliveries have all been released. They
// create no operation row and count against no rate limit, because the
// requester did not ask for them — only the surrounding start is metered.
func (s *Activities) EndUnmetered(ctx context.Context, id string, expectedSequence int, dismissalAt, now time.Time) (*LiveActivity, error) {
	const q = `
		UPDATE live_activities SET
			sequence       = sequence + 1,
			apns_timestamp = greatest($4, apns_timestamp + 1),
			status         = 'ended',
			dismissal_at   = $5,
			updated_at     = $3,
			ended_at       = $3
		WHERE id = $1 AND sequence = $2 AND status IN ('starting', 'active', 'partial')
		RETURNING ` + activityColumns
	stamp := Millis(now)
	return queryOne[LiveActivity](ctx, s.q, "end Live Activity", q,
		id, expectedSequence, stamp, stamp.Unix(), Millis(dismissalAt))
}

// Settle records the outcome of a fan-out.
//
// It is guarded on the sequence the dispatch ran at, so a settle that arrives
// after a newer mutation is dropped instead of overwriting fresher counts with
// stale ones. [ErrNotFound] means exactly that and is not an error worth
// surfacing.
func (s *Activities) Settle(ctx context.Context, id string, sequence int, status string, accepted, failed int, now time.Time) (*LiveActivity, error) {
	const q = `
		UPDATE live_activities SET
			status = $3, accepted_count = $4, failed_count = $5, updated_at = $6
		WHERE id = $1 AND sequence = $2
		RETURNING ` + activityColumns
	return queryOne[LiveActivity](ctx, s.q, "settle Live Activity", q,
		id, sequence, status, accepted, failed, Millis(now))
}

// CountLiveDeliveries reports how many of an activity's deliveries are still
// running. It decides whether a fan-out that accepted nothing is a failure or
// merely retryable, and whether a takeover has emptied an activity out.
func (s *Activities) CountLiveDeliveries(ctx context.Context, activityID string) (int, error) {
	const q = `
		SELECT count(*) FROM live_activity_deliveries
		WHERE activity_id = $1 AND status IN ('pending', 'accepted', 'active')`
	return queryValue[int](ctx, s.q, "count live deliveries", q, activityID)
}

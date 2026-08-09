package db

import (
	"context"
	"encoding/json"
	"time"
)

// LiveActivityOperation is one requester-initiated change to an activity.
//
// It is written in the same transaction as the change itself, so an operation
// exists exactly when its mutation landed. That makes it three things at once:
// the audit trail, the unit of rate-limit accounting, and the source of the
// Live Activity rows in the history feed.
type LiveActivityOperation struct {
	ID                 string  `db:"id"`
	ActivityID         string  `db:"activity_id"`
	RequesterTokenID   *string `db:"requester_token_id"`
	RequesterServiceID *string `db:"requester_service_id"`
	Event              string  `db:"event"`
	// Sequence is the activity's sequence after this operation.
	Sequence int `db:"sequence"`
	// Props is the activity state immediately after the operation, which is
	// what lets the feed describe a change without replaying every one since.
	Props          json.RawMessage `db:"props"`
	IdempotencyKey *string         `db:"idempotency_key"`
	RequestHash    *string         `db:"request_hash"`
	AcceptedCount  int             `db:"accepted_count"`
	FailedCount    int             `db:"failed_count"`
	CreatedAt      time.Time       `db:"created_at"`
}

// Operations stores Live Activity mutations.
type Operations struct{ q Querier }

const operationColumns = `id, activity_id, requester_token_id, requester_service_id, event,
	sequence, props, idempotency_key, request_hash, accepted_count, failed_count, created_at`

// CreateOperationParams records one mutation.
type CreateOperationParams struct {
	ID                 string
	ActivityID         string
	RequesterTokenID   *string
	RequesterServiceID *string
	Event              string
	Sequence           int
	Props              json.RawMessage
	IdempotencyKey     *string
	RequestHash        *string
	Now                time.Time
}

// Insert records a mutation. It is called from inside the transaction that
// applies the mutation — see [Activities.Start] and [Activities.Mutate] — and
// is exported only because a caller composing a larger unit of work needs it.
func (s *Operations) Insert(ctx context.Context, p CreateOperationParams) (*LiveActivityOperation, error) {
	const q = `
		INSERT INTO live_activity_operations (id, activity_id, requester_token_id,
		                                      requester_service_id, event, sequence, props,
		                                      idempotency_key, request_hash, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING ` + operationColumns
	return queryOne[LiveActivityOperation](ctx, s.q, "create Live Activity operation", q,
		p.ID, p.ActivityID, p.RequesterTokenID, p.RequesterServiceID, p.Event, p.Sequence,
		p.Props, p.IdempotencyKey, p.RequestHash, Millis(p.Now))
}

// ByID loads one operation.
func (s *Operations) ByID(ctx context.Context, id string) (*LiveActivityOperation, error) {
	const q = `SELECT ` + operationColumns + ` FROM live_activity_operations WHERE id = $1`
	return queryOne[LiveActivityOperation](ctx, s.q, "load Live Activity operation", q, id)
}

// ByIdempotencyKey looks up an earlier update or end from the same requester
// with the same key.
//
// Updates and ends key off the operation rather than the activity, because one
// activity legitimately sees many keyed mutations over its life; only the start
// is identified by the activity's own key.
func (s *Operations) ByIdempotencyKey(ctx context.Context, tokenID, serviceID *string, key string) (*LiveActivityOperation, error) {
	const q = `
		SELECT ` + operationColumns + ` FROM live_activity_operations
		WHERE idempotency_key = $3
		  AND requester_token_id   IS NOT DISTINCT FROM $1
		  AND requester_service_id IS NOT DISTINCT FROM $2
		LIMIT 1`
	return queryOne[LiveActivityOperation](ctx, s.q, "load operation by idempotency key", q,
		tokenID, serviceID, key)
}

// Settle records how the operation's fan-out went.
func (s *Operations) Settle(ctx context.Context, id string, accepted, failed int) (*LiveActivityOperation, error) {
	const q = `
		UPDATE live_activity_operations SET accepted_count = $2, failed_count = $3
		WHERE id = $1
		RETURNING ` + operationColumns
	return queryOne[LiveActivityOperation](ctx, s.q, "settle Live Activity operation", q, id, accepted, failed)
}

// CountForTokenSince counts a token's metered operations inside the rate-limit
// window. Interaction-backed activities are excluded: their operations are
// consequences of answering a question, not requests the token made.
func (s *Operations) CountForTokenSince(ctx context.Context, tokenID string, since time.Time) (int, error) {
	const q = `
		SELECT count(*) FROM live_activity_operations o
		JOIN live_activities a ON a.id = o.activity_id
		WHERE o.requester_token_id = $1 AND o.created_at >= $2 AND a.interaction_id IS NULL`
	return queryValue[int](ctx, s.q, "count token operations", q, tokenID, Millis(since))
}

// CountForServiceSince counts a service's metered operations inside the window.
func (s *Operations) CountForServiceSince(ctx context.Context, serviceID string, since time.Time) (int, error) {
	const q = `
		SELECT count(*) FROM live_activity_operations o
		JOIN live_activities a ON a.id = o.activity_id
		WHERE o.requester_service_id = $1 AND o.created_at >= $2 AND a.interaction_id IS NULL`
	return queryValue[int](ctx, s.q, "count service operations", q, serviceID, Millis(since))
}

// CountForUserSince counts the account's metered operations inside the window.
func (s *Operations) CountForUserSince(ctx context.Context, userID string, since time.Time) (int, error) {
	const q = `
		SELECT count(*) FROM live_activity_operations o
		JOIN live_activities a ON a.id = o.activity_id
		WHERE a.user_id = $1 AND o.created_at >= $2 AND a.interaction_id IS NULL`
	return queryValue[int](ctx, s.q, "count account operations", q, userID, Millis(since))
}

// Delete removes one operation from the account's history feed. The activity it
// describes is untouched.
func (s *Operations) Delete(ctx context.Context, id, userID string) (bool, error) {
	const q = `
		DELETE FROM live_activity_operations o
		USING live_activities a
		WHERE o.activity_id = a.id AND o.id = $1 AND a.user_id = $2`
	return execMatched(ctx, s.q, "delete Live Activity operation", q, id, userID)
}

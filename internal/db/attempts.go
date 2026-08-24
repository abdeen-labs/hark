package db

import (
	"context"
	"time"
)

// Synthetic APNs reasons are written when no HTTP request was made. They share
// the APNs reason column so the audit trail has one failure field.
const (
	// ReasonInteractionTerminal: the question was answered or withdrawn before
	// its Live Activity could be started.
	ReasonInteractionTerminal = "InteractionTerminal"
	// ReasonMissingPushToStartToken: the device is no longer Live-Activity
	// capable.
	ReasonMissingPushToStartToken = "MissingPushToStartToken"
	// ReasonMissingUpdateToken: the phone never confirmed the activity, so
	// there is no address to update it at.
	ReasonMissingUpdateToken = "MissingUpdateToken"
	// ReasonProviderNotConfigured: no APNs credentials are configured.
	ReasonProviderNotConfigured = "ProviderNotConfigured"
	// ReasonEnvironmentMismatch: the token belongs to the other APNs
	// environment, where it would silently do nothing.
	ReasonEnvironmentMismatch = "EnvironmentMismatch"
	// ReasonOwnerChanged: the device moved to another account.
	ReasonOwnerChanged = "OwnerChanged"
)

// LiveActivityDeliveryAttempt is one APNs call.
//
// This append-only diagnostic table is not read while serving requests. Old
// rows can be pruned with [Attempts.DeleteBefore].
type LiveActivityDeliveryAttempt struct {
	ID                 string  `db:"id"`
	ActivityID         string  `db:"activity_id"`
	DeliveryID         string  `db:"delivery_id"`
	OperationID        string  `db:"operation_id"`
	RequesterTokenID   *string `db:"requester_token_id"`
	RequesterServiceID *string `db:"requester_service_id"`
	Event              string  `db:"event"`
	Sequence           int     `db:"sequence"`
	// APNsStatus is NULL when no request was made; APNsReason then carries one
	// of the synthetic reasons above.
	APNsStatus *int      `db:"apns_status"`
	APNsReason *string   `db:"apns_reason"`
	APNsID     *string   `db:"apns_id"`
	CreatedAt  time.Time `db:"created_at"`
}

// Attempts stores the APNs audit trail.
type Attempts struct{ q Querier }

const attemptColumns = `id, activity_id, delivery_id, operation_id, requester_token_id,
	requester_service_id, event, sequence, apns_status, apns_reason, apns_id, created_at`

// CreateAttemptParams records one APNs call.
type CreateAttemptParams struct {
	ID                 string
	ActivityID         string
	DeliveryID         string
	OperationID        string
	RequesterTokenID   *string
	RequesterServiceID *string
	Event              string
	Sequence           int
	APNsStatus         *int
	APNsReason         *string
	APNsID             *string
	Now                time.Time
}

// Insert appends an attempt. It is called from
// [Deliveries.RecordAttempt], which writes it together with the delivery state
// it explains.
func (s *Attempts) Insert(ctx context.Context, p CreateAttemptParams) (*LiveActivityDeliveryAttempt, error) {
	const q = `
		INSERT INTO live_activity_delivery_attempts (id, activity_id, delivery_id, operation_id,
		                                             requester_token_id, requester_service_id,
		                                             event, sequence, apns_status, apns_reason,
		                                             apns_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, nullif($9::integer, 0), $10, $11, $12)
		RETURNING ` + attemptColumns
	return queryOne[LiveActivityDeliveryAttempt](ctx, s.q, "record APNs attempt", q,
		p.ID, p.ActivityID, p.DeliveryID, p.OperationID, p.RequesterTokenID, p.RequesterServiceID,
		p.Event, p.Sequence, p.APNsStatus, p.APNsReason, p.APNsID, Millis(p.Now))
}

// ListForActivity returns an activity's attempts, newest first. Nothing in the
// API calls this; it is here for operator tooling.
func (s *Attempts) ListForActivity(ctx context.Context, activityID string, limit int) ([]LiveActivityDeliveryAttempt, error) {
	const q = `
		SELECT ` + attemptColumns + ` FROM live_activity_delivery_attempts
		WHERE activity_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2`
	return queryAll[LiveActivityDeliveryAttempt](ctx, s.q, "list APNs attempts", q, activityID, ClampLimit(limit))
}

// DeleteBefore prunes the audit trail.
//
// It is the retention policy the table needs and would otherwise not have:
// every push writes a row here and nothing ever reads one back, so without
// pruning this becomes the largest table in the database with no benefit.
func (s *Attempts) DeleteBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	const q = `DELETE FROM live_activity_delivery_attempts WHERE created_at < $1`
	return execAffected(ctx, s.q, "prune APNs attempts", q, Millis(cutoff))
}

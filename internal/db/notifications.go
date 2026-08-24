package db

import (
	"context"
	"time"
)

// AgentNotification is a one-shot push sent with an API token.
//
// The row is kept for two reasons: an idempotent retry has to replay the
// original outcome instead of pushing again, and the send belongs in the
// account's history alongside webhook events.
type AgentNotification struct {
	ID     string `db:"id"`
	UserID string `db:"user_id"`
	// RequesterTokenID is mandatory here, unlike the other requester tables:
	// there is no webhook equivalent of an agent notification.
	RequesterTokenID string  `db:"requester_token_id"`
	Title            string  `db:"title"`
	Body             string  `db:"body"`
	ImageURL         *string `db:"image_url"`
	URL              *string `db:"url"`
	Priority         string  `db:"priority"`
	// Status speaks the same vocabulary as an event's, for the same reason: the
	// history feed shows both, and the difference between "nothing to send to"
	// and "nothing got through" is the difference between an account that is not
	// set up and a delivery problem.
	Status string `db:"status"`
	// AcceptedCount is settled after the fan-out; it starts at zero because the
	// row is written before anything is sent.
	AcceptedCount  int       `db:"accepted_count"`
	IdempotencyKey *string   `db:"idempotency_key"`
	RequestHash    *string   `db:"request_hash"`
	CreatedAt      time.Time `db:"created_at"`
}

// Notifications stores agent pushes.
type Notifications struct{ q Querier }

const notificationColumns = `id, user_id, requester_token_id, title, body, image_url, url,
	priority, status, accepted_count, idempotency_key, request_hash, created_at`

// CreateNotificationParams records an agent push.
type CreateNotificationParams struct {
	ID               string
	UserID           string
	RequesterTokenID string
	Title            string
	Body             string
	ImageURL         *string
	URL              *string
	Priority         string
	IdempotencyKey   *string
	RequestHash      *string
	Now              time.Time
}

// Create inserts the row before the push is sent, so a raced duplicate replays
// it rather than double-pushing. A violation of
// agent_notifications_token_idempotency_key means the race was lost: re-read
// and replay.
func (s *Notifications) Create(ctx context.Context, p CreateNotificationParams) (*AgentNotification, error) {
	const q = `
		INSERT INTO agent_notifications (id, user_id, requester_token_id, title, body,
		                                 image_url, url, priority, status, accepted_count,
		                                 idempotency_key, request_hash, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'processing', 0, $9, $10, $11)
		RETURNING ` + notificationColumns
	return queryOne[AgentNotification](ctx, s.q, "create agent notification", q,
		p.ID, p.UserID, p.RequesterTokenID, p.Title, p.Body, p.ImageURL, p.URL,
		p.Priority, p.IdempotencyKey, p.RequestHash, Millis(p.Now))
}

// ByID loads one notification.
func (s *Notifications) ByID(ctx context.Context, id string) (*AgentNotification, error) {
	const q = `SELECT ` + notificationColumns + ` FROM agent_notifications WHERE id = $1`
	return queryOne[AgentNotification](ctx, s.q, "load agent notification", q, id)
}

// ByIdempotencyKey looks up an earlier send from the same token with the same
// key, for the replay comparison.
func (s *Notifications) ByIdempotencyKey(ctx context.Context, tokenID, key string) (*AgentNotification, error) {
	const q = `SELECT ` + notificationColumns + ` FROM agent_notifications
		WHERE requester_token_id = $1 AND idempotency_key = $2`
	return queryOne[AgentNotification](ctx, s.q, "load agent notification by idempotency key", q, tokenID, key)
}

// Settle records what the fan-out achieved: the terminal status and how many
// messages APNs accepted. Every send reaches it, including sends with no target
// device, so records do not remain in processing state.
func (s *Notifications) Settle(ctx context.Context, id, status string, accepted int) (*AgentNotification, error) {
	const q = `UPDATE agent_notifications SET status = $2, accepted_count = $3 WHERE id = $1
		RETURNING ` + notificationColumns
	return queryOne[AgentNotification](ctx, s.q, "settle agent notification", q, id, status, accepted)
}

// CountForTokenSince counts one token's sends inside the rate-limit window.
func (s *Notifications) CountForTokenSince(ctx context.Context, tokenID string, since time.Time) (int, error) {
	const q = `SELECT count(*) FROM agent_notifications WHERE requester_token_id = $1 AND created_at >= $2`
	return queryValue[int](ctx, s.q, "count token notifications", q, tokenID, Millis(since))
}

// CountForUserSince counts the account's sends inside the rate-limit window.
func (s *Notifications) CountForUserSince(ctx context.Context, userID string, since time.Time) (int, error) {
	const q = `SELECT count(*) FROM agent_notifications WHERE user_id = $1 AND created_at >= $2`
	return queryValue[int](ctx, s.q, "count account notifications", q, userID, Millis(since))
}

// Delete removes one notification from the account's history.
func (s *Notifications) Delete(ctx context.Context, id, userID string) (bool, error) {
	const q = `DELETE FROM agent_notifications WHERE id = $1 AND user_id = $2`
	return execMatched(ctx, s.q, "delete agent notification", q, id, userID)
}

package db

import (
	"context"
	"time"
)

// Event statuses. processing is written before any push is attempted; every
// other status is terminal and nothing transitions out of it.
const (
	// EventProcessing means the row exists but the fan-out has not settled.
	// Only an idempotent replay that races the original ever observes it.
	EventProcessing = "processing"
	// EventNoDevices means there was nothing to push to.
	EventNoDevices = "no_devices"
	// EventAccepted means APNs took every message.
	EventAccepted = "accepted"
	// EventPartial means APNs took some.
	EventPartial = "partial"
	// EventFailed means APNs took none.
	EventFailed = "failed"
)

// MaxEventErrorLen bounds the stored failure summary. APNs reasons are short,
// but a fan-out over many devices concatenates many of them.
const MaxEventErrorLen = 1000

// Event is one webhook request that reached validation: the account's delivery
// log, and the source of the notification rows in the history feed.
type Event struct {
	ID        string `db:"id"`
	ServiceID string `db:"service_id"`
	// Resolved fields: the request's value where it gave one, the service's
	// default otherwise.
	Title    string  `db:"title"`
	Body     string  `db:"body"`
	ImageURL *string `db:"image_url"`
	URL      *string `db:"url"`
	Priority string  `db:"priority"`
	Status   string  `db:"status"`
	// DeliveredCount counts APNs acceptances, which is not the same as
	// deliveries: APNs accepting a message says nothing about the phone.
	DeliveredCount int `db:"delivered_count"`
	// Error holds the joined provider failure reasons. It stays out of the
	// caller's response because APNs errors can embed device tokens.
	Error          *string   `db:"error"`
	IdempotencyKey *string   `db:"idempotency_key"`
	RequestHash    *string   `db:"request_hash"`
	CreatedAt      time.Time `db:"created_at"`
}

// EventListItem is an event with the name of the service that produced it,
// which every list of events shows.
type EventListItem struct {
	Event
	ServiceTitle    string  `db:"service_title"`
	ServiceImageURL *string `db:"service_image_url"`
}

// Events stores the webhook delivery log.
type Events struct{ q Querier }

const eventColumns = `id, service_id, title, body, image_url, url, priority, status,
	delivered_count, error, idempotency_key, request_hash, created_at`

// CreateEventParams records a webhook request.
type CreateEventParams struct {
	ID        string
	ServiceID string
	Title     string
	Body      string
	ImageURL  *string
	URL       *string
	Priority  string
	Status    string
	// IdempotencyKey and RequestHash are set together or not at all: the hash
	// is what distinguishes a replay from a reuse of the same key.
	IdempotencyKey *string
	RequestHash    *string
	Now            time.Time
}

// Create inserts the log row.
//
// It happens before any push is attempted, so a concurrent duplicate loses the
// unique index on (service_id, idempotency_key) and replays the stored outcome
// instead of pushing twice. Callers must treat a violation of
// events_service_idempotency_key as "re-read and replay", not as an error.
func (s *Events) Create(ctx context.Context, p CreateEventParams) (*Event, error) {
	const q = `
		INSERT INTO events (id, service_id, title, body, image_url, url, priority, status,
		                    delivered_count, idempotency_key, request_hash, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 0, $9, $10, $11)
		RETURNING ` + eventColumns
	return queryOne[Event](ctx, s.q, "create event", q,
		p.ID, p.ServiceID, p.Title, p.Body, p.ImageURL, p.URL, p.Priority, p.Status,
		p.IdempotencyKey, p.RequestHash, Millis(p.Now))
}

// ByID loads one event.
func (s *Events) ByID(ctx context.Context, id string) (*Event, error) {
	const q = `SELECT ` + eventColumns + ` FROM events WHERE id = $1`
	return queryOne[Event](ctx, s.q, "load event", q, id)
}

// ByIdempotencyKey looks up a previous request from the same service with the
// same key. The caller compares request hashes: equal means replay the stored
// outcome, different means the key was reused for another payload.
func (s *Events) ByIdempotencyKey(ctx context.Context, serviceID, key string) (*Event, error) {
	const q = `SELECT ` + eventColumns + ` FROM events WHERE service_id = $1 AND idempotency_key = $2`
	return queryOne[Event](ctx, s.q, "load event by idempotency key", q, serviceID, key)
}

// Settle records the outcome of the fan-out.
func (s *Events) Settle(ctx context.Context, id, status string, deliveredCount int, failure *string) (*Event, error) {
	const q = `
		UPDATE events SET status = $2, delivered_count = $3, error = left($4, $5)
		WHERE id = $1
		RETURNING ` + eventColumns
	return queryOne[Event](ctx, s.q, "settle event", q, id, status, deliveredCount, failure, MaxEventErrorLen)
}

// ListForUser pages the account's events across every service, newest first.
func (s *Events) ListForUser(ctx context.Context, userID string, cursor Cursor, limit int) (Page[EventListItem], error) {
	const q = `
		SELECT e.id, e.service_id, e.title, e.body, e.image_url, e.url, e.priority, e.status,
		       e.delivered_count, e.error, e.idempotency_key, e.request_hash, e.created_at,
		       s.title AS service_title, s.image_url AS service_image_url
		FROM events e
		JOIN services s ON s.id = e.service_id
		WHERE s.user_id = $1
		  AND ($2::timestamptz IS NULL OR (e.created_at, e.id) < ($2, $3))
		ORDER BY e.created_at DESC, e.id DESC
		LIMIT $4`

	limit = ClampLimit(limit)
	at, id := cursor.args()
	rows, err := queryAll[EventListItem](ctx, s.q, "list events", q, userID, at, id, limit+1)
	if err != nil {
		return Page[EventListItem]{}, err
	}
	return paginate(rows, limit, func(e EventListItem) Cursor {
		return Cursor{Time: e.CreatedAt, ID: e.ID}
	}), nil
}

// CountForServiceSince counts a service's events inside the rate-limit window.
func (s *Events) CountForServiceSince(ctx context.Context, serviceID string, since time.Time) (int, error) {
	const q = `SELECT count(*) FROM events WHERE service_id = $1 AND created_at >= $2`
	return queryValue[int](ctx, s.q, "count service events", q, serviceID, Millis(since))
}

// CountForUserSince counts the account's events across every service inside the
// rate-limit window.
func (s *Events) CountForUserSince(ctx context.Context, userID string, since time.Time) (int, error) {
	const q = `
		SELECT count(*) FROM events e
		JOIN services s ON s.id = e.service_id
		WHERE s.user_id = $1 AND e.created_at >= $2`
	return queryValue[int](ctx, s.q, "count account events", q, userID, Millis(since))
}

// Delete removes one event from the account's history. The cascade takes the
// interaction the event spawned with it.
func (s *Events) Delete(ctx context.Context, id, userID string) (bool, error) {
	const q = `
		DELETE FROM events e
		USING services s
		WHERE e.service_id = s.id AND e.id = $1 AND s.user_id = $2`
	return execMatched(ctx, s.q, "delete event", q, id, userID)
}

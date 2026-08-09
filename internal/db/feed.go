package db

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Feed kinds, as shown to a client.
const (
	// FeedKindNotification covers both webhook events and agent pushes: from
	// the phone's point of view they are the same thing arriving from
	// different senders.
	FeedKindNotification = "notification"
	// FeedKindResponse is a question that was answered.
	FeedKindResponse = "response"
	// FeedKindLiveActivity is one change to a Live Activity.
	FeedKindLiveActivity = "live_activity"
)

// Feed filters. FeedFilterAll is the default.
const (
	FeedFilterAll          = "all"
	FeedFilterNotification = FeedKindNotification
	FeedFilterResponse     = FeedKindResponse
	FeedFilterLiveActivity = FeedKindLiveActivity
)

// ValidFeedFilter reports whether f is an accepted filter.
func ValidFeedFilter(f string) bool {
	switch f {
	case FeedFilterAll, FeedFilterNotification, FeedFilterResponse, FeedFilterLiveActivity:
		return true
	default:
		return false
	}
}

// Feed sources, which are the prefixes of a feed item's composite id. There are
// four because two distinct tables both surface as notifications, and a delete
// has to know which one a row came from.
const (
	FeedSourceEvent        = "event"
	FeedSourceNotification = "notification"
	FeedSourceResponse     = "response"
	FeedSourceLiveActivity = "live_activity"
)

// FeedItem is one entry in the account's unified history.
//
// The four sources have little in common, so most fields are nullable and a
// client reads whichever ones its kind populates.
type FeedItem struct {
	// ID is "<source>:<row id>". It is the handle a delete takes, which is why
	// it names the source rather than just the row.
	ID   string `db:"id"`
	Kind string `db:"kind"`
	// SourceName is whatever sent this: a service title, an API token name, or
	// a fallback.
	SourceName     string  `db:"source_name"`
	SourceImageURL *string `db:"source_image_url"`
	Title          string  `db:"title"`
	Detail         *string `db:"detail"`
	URL            *string `db:"url"`
	// Result is the outcome for kinds that have one: an interaction status, or
	// a Live Activity operation event.
	Result *string `db:"result"`
	// Status, DeliveredCount, Error and Priority are populated for
	// notifications only.
	Status         *string `db:"status"`
	DeliveredCount *int    `db:"delivered_count"`
	Error          *string `db:"error"`
	Priority       *string `db:"priority"`
	// CreatedAt is when the entry happened: for an answered question that is
	// when it was answered, not when it was asked, because that is where it
	// belongs in a history.
	CreatedAt time.Time `db:"created_at"`
}

// Feed reads the account's unified history.
type Feed struct {
	q     Querier
	store *Store
}

// feedQuery is a union of the four sources with a shared column shape.
//
// Every NULL is cast, because a UNION resolves each column's type from the
// branches and an uncast NULL would leave it unknown. The filter is applied per
// branch rather than around the union so that a filtered read does not
// materialise the sources it is about to discard.
const feedQuery = `
	WITH feed AS (
		SELECT 'event:' || e.id                        AS id,
		       'notification'::text                    AS kind,
		       s.title                                 AS source_name,
		       coalesce(e.image_url, s.image_url)      AS source_image_url,
		       e.title                                 AS title,
		       e.body                                  AS detail,
		       e.url                                   AS url,
		       NULL::text                              AS result,
		       e.status                                AS status,
		       e.delivered_count                       AS delivered_count,
		       e.error                                 AS error,
		       e.priority                              AS priority,
		       e.created_at                            AS created_at
		FROM events e
		JOIN services s ON s.id = e.service_id
		WHERE s.user_id = $1 AND $2::text IN ('all', 'notification')

		UNION ALL

		SELECT 'notification:' || n.id, 'notification'::text, t.name, n.image_url,
		       n.title, n.body, n.url, NULL::text,
		       n.status, n.accepted_count, NULL::text, n.priority, n.created_at
		FROM agent_notifications n
		JOIN api_tokens t ON t.id = n.requester_token_id
		WHERE n.user_id = $1 AND $2::text IN ('all', 'notification')

		UNION ALL

		SELECT 'response:' || i.id, 'response'::text,
		       coalesce(sv.title, t.name, i.title), coalesce(i.image_url, sv.image_url),
		       i.title, i.prompt, i.url, i.status,
		       NULL::text, NULL::integer, NULL::text, NULL::text, i.responded_at
		FROM interactions i
		LEFT JOIN services sv  ON sv.id = i.requester_service_id
		LEFT JOIN api_tokens t ON t.id  = i.requester_token_id
		WHERE i.user_id = $1 AND $2::text IN ('all', 'response')
		  AND i.status IN ('approved', 'denied', 'yes', 'no', 'replied')
		  AND i.responded_at IS NOT NULL

		UNION ALL

		SELECT 'live_activity:' || o.id, 'live_activity'::text,
		       coalesce(sv.title, t.name, 'Hark'), sv.image_url,
		       coalesce(o.props->>'title', a.props->>'title', 'Live Activity'),
		       coalesce(o.props->>'status', a.props->>'status'),
		       NULL::text, o.event,
		       NULL::text, NULL::integer, NULL::text, NULL::text, o.created_at
		FROM live_activity_operations o
		JOIN live_activities a ON a.id = o.activity_id
		LEFT JOIN services sv  ON sv.id = o.requester_service_id
		LEFT JOIN api_tokens t ON t.id  = o.requester_token_id
		WHERE a.user_id = $1 AND $2::text IN ('all', 'live_activity')
		  AND a.interaction_id IS NULL
	)
	SELECT id, kind, source_name, source_image_url, title, detail, url, result,
	       status, delivered_count, error, priority, created_at
	FROM feed
	WHERE ($3::timestamptz IS NULL OR (created_at, id) < ($3, $4))
	ORDER BY created_at DESC, id DESC
	LIMIT $5`

// List pages the account's history, newest first.
//
// Ordering is by (created_at, id) descending, and the cursor is a position in
// exactly that order — which is why the composite id has to participate: four
// sources routinely produce rows in the same millisecond, and without the
// tie-break a page boundary would drop or repeat them.
func (s *Feed) List(ctx context.Context, userID, filter string, cursor Cursor, limit int) (Page[FeedItem], error) {
	if filter == "" {
		filter = FeedFilterAll
	}
	if !ValidFeedFilter(filter) {
		return Page[FeedItem]{}, fmt.Errorf("db: list feed: unknown filter %q", filter)
	}

	limit = ClampLimit(limit)
	at, id := cursor.args()
	rows, err := queryAll[FeedItem](ctx, s.q, "list feed", feedQuery, userID, filter, at, id, limit+1)
	if err != nil {
		return Page[FeedItem]{}, err
	}
	return paginate(rows, limit, func(i FeedItem) Cursor {
		return Cursor{Time: i.CreatedAt, ID: i.ID}
	}), nil
}

// ParseFeedID splits a composite feed id into its source and row id. It reports
// false for anything that is not "<known source>:<non-empty id>".
func ParseFeedID(feedID string) (source, rowID string, ok bool) {
	source, rowID, found := strings.Cut(feedID, ":")
	if !found || rowID == "" {
		return "", "", false
	}
	switch source {
	case FeedSourceEvent, FeedSourceNotification, FeedSourceResponse, FeedSourceLiveActivity:
		return source, rowID, true
	default:
		return "", "", false
	}
}

// Delete removes one entry from the history.
//
// Which table that touches depends on the source, and each has its own
// ownership guard. Deleting a webhook event also removes the question it asked,
// through the foreign key; a question still awaiting an answer cannot be
// deleted at all.
func (s *Feed) Delete(ctx context.Context, userID, feedID string) (bool, error) {
	source, rowID, ok := ParseFeedID(feedID)
	if !ok {
		return false, nil
	}
	switch source {
	case FeedSourceEvent:
		return s.store.Events.Delete(ctx, rowID, userID)
	case FeedSourceNotification:
		return s.store.Notifications.Delete(ctx, rowID, userID)
	case FeedSourceResponse:
		return s.store.Interactions.Delete(ctx, rowID, userID)
	case FeedSourceLiveActivity:
		return s.store.Operations.Delete(ctx, rowID, userID)
	default:
		return false, nil
	}
}

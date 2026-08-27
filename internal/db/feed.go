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

// ValidRecordedPriority reports whether p is stored in history.
func ValidRecordedPriority(p string) bool {
	switch p {
	case PriorityNormal, PriorityTimeSensitive, PriorityCritical:
		return true
	default:
		return false
	}
}

// FeedFilters narrows a history query. The zero value matches all entries.
type FeedFilters struct {
	Kind     string
	Source   string
	Priority string
}

func (f FeedFilters) validate(op string) error {
	if f.Kind != "" && !ValidFeedFilter(f.Kind) {
		return fmt.Errorf("db: %s: unknown filter %q", op, f.Kind)
	}
	if f.Priority != "" && !ValidRecordedPriority(f.Priority) {
		return fmt.Errorf("db: %s: unknown priority %q", op, f.Priority)
	}
	return nil
}

func (f FeedFilters) args() (kind string, source, priority *string) {
	kind = f.Kind
	if kind == "" {
		kind = FeedFilterAll
	}
	if f.Source != "" {
		source = &f.Source
	}
	if f.Priority != "" {
		priority = &f.Priority
	}
	return kind, source, priority
}

// Feed sources prefix a feed item's composite id.
const (
	FeedSourceEvent        = "event"
	FeedSourceNotification = "notification"
	FeedSourceResponse     = "response"
	FeedSourceLiveActivity = "live_activity"
	FeedSourceSafetyEvent  = "safety_event"
)

// FeedItem is one entry in the account's unified history.
//
// The sources have little in common, so most fields are nullable and a
// client reads whichever ones its kind populates.
type FeedItem struct {
	// ID is "<source>:<row id>". It is the handle a delete takes, which is why
	// it names the source rather than just the row.
	ID   string `db:"id"`
	Kind string `db:"kind"`
	// SourceName is the service title, API token name, or fallback sender name.
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

// feedQuery is a union of all feed sources with a shared column shape.
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
		  AND ($3::text IS NULL OR s.title = $3)
		  AND ($4::text IS NULL OR e.priority = $4)

		UNION ALL

		SELECT 'notification:' || n.id, 'notification'::text, t.name, n.image_url,
		       n.title, n.body, n.url, NULL::text,
		       n.status, n.accepted_count, NULL::text, n.priority, n.created_at
		FROM agent_notifications n
		JOIN api_tokens t ON t.id = n.requester_token_id
		WHERE n.user_id = $1 AND $2::text IN ('all', 'notification')
		  AND ($3::text IS NULL OR t.name = $3)
		  AND ($4::text IS NULL OR n.priority = $4)

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
		  AND ($3::text IS NULL OR coalesce(sv.title, t.name, i.title) = $3)
		  AND $4::text IS NULL

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
		  AND ($3::text IS NULL OR coalesce(sv.title, t.name, 'Hark') = $3)
		  AND $4::text IS NULL

		UNION ALL

		SELECT 'safety_event:' || se.id, 'notification'::text, ss.name, NULL::text,
		       se.title, se.body, NULL::text, NULL::text,
		       se.status, se.delivered_count, se.error, se.priority, se.created_at
		FROM safety_events se
		JOIN safety_sources ss ON ss.id = se.source_id
		WHERE ss.user_id = $1 AND $2::text IN ('all', 'notification')
		  AND ($3::text IS NULL OR ss.name = $3)
		  AND ($4::text IS NULL OR se.priority = $4)
	)
	SELECT id, kind, source_name, source_image_url, title, detail, url, result,
	       status, delivered_count, error, priority, created_at
	FROM feed
	WHERE ($5::timestamptz IS NULL OR (created_at, id) < ($5, $6))
	ORDER BY created_at DESC, id DESC
	LIMIT $7`

// List pages the account's history, newest first.
//
// Ordering is by (created_at, id) descending, and the cursor is a position in
// exactly that order — which is why the composite id has to participate: the
// sources routinely produce rows in the same millisecond, and without the
// tie-break a page boundary would drop or repeat them.
func (s *Feed) List(ctx context.Context, userID, filter string, cursor Cursor, limit int) (Page[FeedItem], error) {
	return s.ListFiltered(ctx, userID, FeedFilters{Kind: filter}, cursor, limit)
}

// ListFiltered pages the history matching f.
func (s *Feed) ListFiltered(ctx context.Context, userID string, f FeedFilters, cursor Cursor, limit int) (Page[FeedItem], error) {
	if err := f.validate("list feed"); err != nil {
		return Page[FeedItem]{}, err
	}

	limit = ClampLimit(limit)
	kind, source, priority := f.args()
	at, id := cursor.args()
	rows, err := queryAll[FeedItem](ctx, s.q, "list feed", feedQuery,
		userID, kind, source, priority, at, id, limit+1)
	if err != nil {
		return Page[FeedItem]{}, err
	}
	return paginate(rows, limit, func(i FeedItem) Cursor {
		return Cursor{Time: i.CreatedAt, ID: i.ID}
	}), nil
}

const feedSourcesQuery = `
	SELECT source_name FROM (
		SELECT s.title AS source_name
		FROM events e
		JOIN services s ON s.id = e.service_id
		WHERE s.user_id = $1

		UNION

		SELECT t.name
		FROM agent_notifications n
		JOIN api_tokens t ON t.id = n.requester_token_id
		WHERE n.user_id = $1

		UNION

		SELECT coalesce(sv.title, t.name, i.title)
		FROM interactions i
		LEFT JOIN services sv  ON sv.id = i.requester_service_id
		LEFT JOIN api_tokens t ON t.id  = i.requester_token_id
		WHERE i.user_id = $1
		  AND i.status IN ('approved', 'denied', 'yes', 'no', 'replied')
		  AND i.responded_at IS NOT NULL

		UNION

		SELECT coalesce(sv.title, t.name, 'Hark')
		FROM live_activity_operations o
		JOIN live_activities a ON a.id = o.activity_id
		LEFT JOIN services sv  ON sv.id = o.requester_service_id
		LEFT JOIN api_tokens t ON t.id  = o.requester_token_id
		WHERE a.user_id = $1 AND a.interaction_id IS NULL

		UNION

		SELECT ss.name
		FROM safety_events se
		JOIN safety_sources ss ON ss.id = se.source_id
		WHERE ss.user_id = $1
	) sources
	ORDER BY lower(source_name), source_name`

type feedSource struct {
	SourceName string `db:"source_name"`
}

// Sources lists distinct source names in the account's history.
func (s *Feed) Sources(ctx context.Context, userID string) ([]string, error) {
	rows, err := queryAll[feedSource](ctx, s.q, "list feed sources", feedSourcesQuery, userID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.SourceName)
	}
	return out, nil
}

// ParseFeedID splits a composite feed id into its source and row id. It reports
// false for anything that is not "<known source>:<non-empty id>".
func ParseFeedID(feedID string) (source, rowID string, ok bool) {
	source, rowID, found := strings.Cut(feedID, ":")
	if !found || rowID == "" {
		return "", "", false
	}
	switch source {
	case FeedSourceEvent, FeedSourceNotification, FeedSourceResponse,
		FeedSourceLiveActivity, FeedSourceSafetyEvent:
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
	case FeedSourceSafetyEvent:
		return s.store.SafetyEvents.Delete(ctx, rowID, userID)
	default:
		return false, nil
	}
}

var feedDeleteQueries = []string{
	`DELETE FROM events e
	 USING services s
	 WHERE e.service_id = s.id AND s.user_id = $1
	   AND $2::text IN ('all', 'notification')
	   AND ($3::text IS NULL OR s.title = $3)
	   AND ($4::text IS NULL OR e.priority = $4)`,

	`DELETE FROM agent_notifications n
	 USING api_tokens t
	 WHERE t.id = n.requester_token_id AND n.user_id = $1
	   AND $2::text IN ('all', 'notification')
	   AND ($3::text IS NULL OR t.name = $3)
	   AND ($4::text IS NULL OR n.priority = $4)`,

	`DELETE FROM interactions
	 WHERE id IN (
	 	SELECT i.id
	 	FROM interactions i
	 	LEFT JOIN services sv  ON sv.id = i.requester_service_id
	 	LEFT JOIN api_tokens t ON t.id  = i.requester_token_id
	 	WHERE i.user_id = $1 AND $2::text IN ('all', 'response')
	 	  AND i.status IN ('approved', 'denied', 'yes', 'no', 'replied')
	 	  AND i.responded_at IS NOT NULL
	 	  AND ($3::text IS NULL OR coalesce(sv.title, t.name, i.title) = $3)
	 	  AND $4::text IS NULL
	 )`,

	`DELETE FROM live_activity_operations
	 WHERE id IN (
	 	SELECT o.id
	 	FROM live_activity_operations o
	 	JOIN live_activities a ON a.id = o.activity_id
	 	LEFT JOIN services sv  ON sv.id = o.requester_service_id
	 	LEFT JOIN api_tokens t ON t.id  = o.requester_token_id
	 	WHERE a.user_id = $1 AND $2::text IN ('all', 'live_activity')
	 	  AND a.interaction_id IS NULL
	 	  AND ($3::text IS NULL OR coalesce(sv.title, t.name, 'Hark') = $3)
	 	  AND $4::text IS NULL
	 )`,

	`DELETE FROM safety_events se
	 USING safety_sources ss
	 WHERE se.source_id = ss.id AND ss.user_id = $1
	   AND $2::text IN ('all', 'notification')
	   AND ($3::text IS NULL OR ss.name = $3)
	   AND ($4::text IS NULL OR se.priority = $4)`,
}

// DeleteAll removes every history entry matching f.
func (s *Feed) DeleteAll(ctx context.Context, userID string, f FeedFilters) error {
	if err := f.validate("delete feed"); err != nil {
		return err
	}
	kind, source, priority := f.args()
	return s.store.Tx(ctx, func(ctx context.Context, tx *Store) error {
		for _, q := range feedDeleteQueries {
			if _, err := execAffected(ctx, tx.q, "delete feed entries", q,
				userID, kind, source, priority); err != nil {
				return err
			}
		}
		return nil
	})
}

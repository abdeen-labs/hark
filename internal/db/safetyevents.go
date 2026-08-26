package db

import (
	"context"
	"time"
)

// Safety event states. API-token reports accept active and resolved only.
const (
	SafetyStateActive   = "active"
	SafetyStateResolved = "resolved"
	SafetyStateTest     = "test"
)

// SafetyStates lists every state a safety event can be stored with.
var SafetyStates = []string{SafetyStateActive, SafetyStateResolved, SafetyStateTest}

// SafetyReportStates lists states accepted by API-token reports.
var SafetyReportStates = []string{SafetyStateActive, SafetyStateResolved}

// ValidSafetyState reports whether s is an accepted state.
func ValidSafetyState(s string) bool {
	for _, v := range SafetyStates {
		if v == s {
			return true
		}
	}
	return false
}

// Safety-only statuses for events recorded without a push.
const (
	// SafetyCoalesced marks a repeated active report.
	SafetyCoalesced = "coalesced"
	// SafetyRateLimited marks a source over its hourly push limit.
	SafetyRateLimited = "rate_limited"
)

// safetyAlertText defines alert copy for each source kind.
var safetyAlertText = map[string]struct{ title, active string }{
	SafetyKindSmoke:          {"Smoke alarm", "Smoke detected."},
	SafetyKindCarbonMonoxide: {"Carbon monoxide alarm", "Carbon monoxide detected."},
	SafetyKindPanic:          {"Panic alarm", "Panic button pressed."},
	SafetyKindIntrusion:      {"Intrusion alarm", "Intrusion detected."},
	SafetyKindWaterLeak:      {"Water leak alarm", "Water leak detected."},
}

// SafetyAlertContent returns server-defined copy for a safety event.
func SafetyAlertContent(kind, name, state string) (title, body string) {
	text, ok := safetyAlertText[kind]
	if !ok {
		text = struct{ title, active string }{"Safety alarm", "Alarm triggered."}
	}
	switch state {
	case SafetyStateResolved:
		return text.title + " cleared", name + ": Alarm cleared."
	case SafetyStateTest:
		return "Safety alert test", name + ": This is a safety alert test."
	default:
		return text.title, name + ": " + text.active
	}
}

// SafetyAlertPriority applies the account and source settings to an event.
func SafetyAlertPriority(state string, userEnabled, sourceEnabled bool) string {
	if state == SafetyStateResolved {
		return PriorityNormal
	}
	if userEnabled && sourceEnabled {
		return PriorityCritical
	}
	return PriorityTimeSensitive
}

// SafetyEvent records one report or setup test.
type SafetyEvent struct {
	ID       string `db:"id"`
	SourceID string `db:"source_id"`
	// RequesterTokenID is nil for session-initiated setup tests.
	RequesterTokenID *string `db:"requester_token_id"`
	State            string  `db:"state"`
	// Title and Body are server-composed, never caller-supplied.
	Title    string `db:"title"`
	Body     string `db:"body"`
	Priority string `db:"priority"`
	Status   string `db:"status"`
	// DeliveredCount counts APNs acceptances, not device receipts.
	DeliveredCount int `db:"delivered_count"`
	// Error contains APNs failure details shown only to the owner.
	Error          *string   `db:"error"`
	IdempotencyKey *string   `db:"idempotency_key"`
	RequestHash    *string   `db:"request_hash"`
	CreatedAt      time.Time `db:"created_at"`
}

// SafetyEvents stores the safety delivery log.
type SafetyEvents struct{ q Querier }

const safetyEventColumns = `id, source_id, requester_token_id, state, title, body,
	priority, status, delivered_count, error, idempotency_key, request_hash, created_at`

// CreateSafetyEventParams records a safety report or setup test.
type CreateSafetyEventParams struct {
	ID               string
	SourceID         string
	RequesterTokenID *string
	State            string
	Title            string
	Body             string
	Priority         string
	// Status may be terminal when a report is suppressed before insertion.
	Status         string
	IdempotencyKey *string
	RequestHash    *string
	Now            time.Time
}

// Create inserts an event before its push is attempted.
func (s *SafetyEvents) Create(ctx context.Context, p CreateSafetyEventParams) (*SafetyEvent, error) {
	const q = `
		INSERT INTO safety_events (id, source_id, requester_token_id, state, title, body,
		                           priority, status, delivered_count, idempotency_key,
		                           request_hash, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 0, $9, $10, $11)
		RETURNING ` + safetyEventColumns
	return queryOne[SafetyEvent](ctx, s.q, "create safety event", q,
		p.ID, p.SourceID, p.RequesterTokenID, p.State, p.Title, p.Body,
		p.Priority, p.Status, p.IdempotencyKey, p.RequestHash, Millis(p.Now))
}

// ByIdempotencyKey looks up an earlier report from the same token.
func (s *SafetyEvents) ByIdempotencyKey(ctx context.Context, tokenID, key string) (*SafetyEvent, error) {
	const q = `SELECT ` + safetyEventColumns + ` FROM safety_events
		WHERE requester_token_id = $1 AND idempotency_key = $2`
	return queryOne[SafetyEvent](ctx, s.q, "load safety event by idempotency key", q, tokenID, key)
}

// Settle records the outcome of the fan-out.
func (s *SafetyEvents) Settle(ctx context.Context, id, status string, deliveredCount int, failure *string) (*SafetyEvent, error) {
	const q = `
		UPDATE safety_events SET status = $2, delivered_count = $3, error = left($4, $5)
		WHERE id = $1
		RETURNING ` + safetyEventColumns
	return queryOne[SafetyEvent](ctx, s.q, "settle safety event", q, id, status, deliveredCount, failure, MaxEventErrorLen)
}

// CountPushedForSourceSince excludes suppressed events.
func (s *SafetyEvents) CountPushedForSourceSince(ctx context.Context, sourceID string, since time.Time) (int, error) {
	const q = `SELECT count(*) FROM safety_events
		WHERE source_id = $1 AND created_at >= $2
		  AND status NOT IN ('coalesced', 'rate_limited')`
	return queryValue[int](ctx, s.q, "count pushed safety events", q, sourceID, Millis(since))
}

// CountPushedForSourceStateSince counts pushed events in one state.
func (s *SafetyEvents) CountPushedForSourceStateSince(ctx context.Context, sourceID, state string, since time.Time) (int, error) {
	const q = `SELECT count(*) FROM safety_events
		WHERE source_id = $1 AND state = $2 AND created_at >= $3
		  AND status NOT IN ('coalesced', 'rate_limited')`
	return queryValue[int](ctx, s.q, "count pushed safety events by state", q, sourceID, state, Millis(since))
}

// CountForTokenSince counts one token's reports inside the rate-limit window.
func (s *SafetyEvents) CountForTokenSince(ctx context.Context, tokenID string, since time.Time) (int, error) {
	const q = `SELECT count(*) FROM safety_events WHERE requester_token_id = $1 AND created_at >= $2`
	return queryValue[int](ctx, s.q, "count token safety events", q, tokenID, Millis(since))
}

// CountForUserSince counts the account's safety events across every source
// inside the rate-limit window.
func (s *SafetyEvents) CountForUserSince(ctx context.Context, userID string, since time.Time) (int, error) {
	const q = `
		SELECT count(*) FROM safety_events se
		JOIN safety_sources ss ON ss.id = se.source_id
		WHERE ss.user_id = $1 AND se.created_at >= $2`
	return queryValue[int](ctx, s.q, "count account safety events", q, userID, Millis(since))
}

// Delete removes one safety event from the account's history.
func (s *SafetyEvents) Delete(ctx context.Context, id, userID string) (bool, error) {
	const q = `
		DELETE FROM safety_events se
		USING safety_sources ss
		WHERE se.source_id = ss.id AND se.id = $1 AND ss.user_id = $2`
	return execMatched(ctx, s.q, "delete safety event", q, id, userID)
}

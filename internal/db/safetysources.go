package db

import (
	"context"
	"time"
)

// Safety source kinds accepted by the reporting endpoint. General sources are
// Time Sensitive only; the remaining kinds may use Critical Alerts.
const (
	SafetyKindGeneral        = "general"
	SafetyKindSmoke          = "smoke"
	SafetyKindCarbonMonoxide = "carbon_monoxide"
	SafetyKindPanic          = "panic"
	SafetyKindIntrusion      = "intrusion"
	SafetyKindWaterLeak      = "water_leak"
)

// SafetyKinds lists every accepted safety-source kind.
var SafetyKinds = []string{
	SafetyKindGeneral, SafetyKindSmoke, SafetyKindCarbonMonoxide, SafetyKindPanic,
	SafetyKindIntrusion, SafetyKindWaterLeak,
}

// CriticalSafetyKinds lists the safety categories allowed to use Critical
// Alerts. A general source can still report events, but they stay Time
// Sensitive.
var CriticalSafetyKinds = []string{
	SafetyKindSmoke, SafetyKindCarbonMonoxide, SafetyKindPanic,
	SafetyKindIntrusion, SafetyKindWaterLeak,
}

// ValidSafetyKind reports whether k is an accepted kind.
func ValidSafetyKind(k string) bool {
	for _, v := range SafetyKinds {
		if v == k {
			return true
		}
	}
	return false
}

// SafetyKindAllowsCritical reports whether a source kind is eligible for
// Critical Alerts.
func SafetyKindAllowsCritical(k string) bool {
	for _, v := range CriticalSafetyKinds {
		if k == v {
			return true
		}
	}
	return false
}

// SafetySource is an owner-configured alert source.
type SafetySource struct {
	ID     string `db:"id"`
	UserID string `db:"user_id"`
	Kind   string `db:"kind"`
	Name   string `db:"name"`
	// CriticalEnabled is false until set by an update.
	CriticalEnabled bool      `db:"critical_enabled"`
	CreatedAt       time.Time `db:"created_at"`
	UpdatedAt       time.Time `db:"updated_at"`
}

// SafetySources stores configured alert sources.
type SafetySources struct{ q Querier }

const safetySourceColumns = `id, user_id, kind, name, critical_enabled, created_at, updated_at`

// CreateSafetySourceParams configures an alert source.
type CreateSafetySourceParams struct {
	ID     string
	UserID string
	Name   string
	Now    time.Time
}

// Create inserts a source with critical delivery disabled.
func (s *SafetySources) Create(ctx context.Context, p CreateSafetySourceParams) (*SafetySource, error) {
	const q = `
		INSERT INTO safety_sources (id, user_id, name, critical_enabled, created_at, updated_at)
		VALUES ($1, $2, $3, false, $4, $4)
		RETURNING ` + safetySourceColumns
	return queryOne[SafetySource](ctx, s.q, "create safety source", q,
		p.ID, p.UserID, p.Name, Millis(p.Now))
}

// ByID loads a source the caller owns.
func (s *SafetySources) ByID(ctx context.Context, id, userID string) (*SafetySource, error) {
	const q = `SELECT ` + safetySourceColumns + ` FROM safety_sources WHERE id = $1 AND user_id = $2`
	return queryOne[SafetySource](ctx, s.q, "load safety source", q, id, userID)
}

// ByIDForUpdate locks a source while delivery limits are checked.
func (s *SafetySources) ByIDForUpdate(ctx context.Context, id, userID string) (*SafetySource, error) {
	const q = `SELECT ` + safetySourceColumns + ` FROM safety_sources
		WHERE id = $1 AND user_id = $2 FOR UPDATE`
	return queryOne[SafetySource](ctx, s.q, "lock safety source", q, id, userID)
}

// ListForUser returns the account's sources, newest first.
func (s *SafetySources) ListForUser(ctx context.Context, userID string) ([]SafetySource, error) {
	const q = `SELECT ` + safetySourceColumns + ` FROM safety_sources
		WHERE user_id = $1 ORDER BY created_at DESC, id DESC`
	return queryAll[SafetySource](ctx, s.q, "list safety sources", q, userID)
}

// UpdateSafetySourceParams is a partial update: an unset field is left alone.
type UpdateSafetySourceParams struct {
	ID              string
	UserID          string
	Kind            Set[string]
	Name            Set[string]
	CriticalEnabled Set[bool]
	Now             time.Time
}

// Update applies a partial change. It returns [ErrNotFound] when the source
// does not exist or belongs to someone else.
func (s *SafetySources) Update(ctx context.Context, p UpdateSafetySourceParams) (*SafetySource, error) {
	const q = `
		UPDATE safety_sources SET
			kind             = CASE WHEN $3::boolean THEN $4::text    ELSE kind             END,
			name             = CASE WHEN $5::boolean THEN $6::text    ELSE name             END,
			critical_enabled = CASE WHEN $7::boolean THEN $8::boolean ELSE critical_enabled END,
			updated_at = $9
		WHERE id = $1 AND user_id = $2
		RETURNING ` + safetySourceColumns

	kindSet, kind := p.Kind.args()
	nameSet, name := p.Name.args()
	criticalSet, critical := p.CriticalEnabled.args()

	return queryOne[SafetySource](ctx, s.q, "update safety source", q,
		p.ID, p.UserID,
		kindSet, kind,
		nameSet, name,
		criticalSet, critical,
		Millis(p.Now))
}

// Delete removes a source and its event history.
func (s *SafetySources) Delete(ctx context.Context, id, userID string) (bool, error) {
	const q = `DELETE FROM safety_sources WHERE id = $1 AND user_id = $2`
	return execMatched(ctx, s.q, "delete safety source", q, id, userID)
}

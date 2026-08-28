package db

import (
	"context"
	"time"
)

// SafetySource is an owner-configured alert source.
type SafetySource struct {
	ID              string    `db:"id"`
	UserID          string    `db:"user_id"`
	Name            string    `db:"name"`
	ImageURL        *string   `db:"image_url"`
	URL             *string   `db:"url"`
	CriticalEnabled bool      `db:"critical_enabled"`
	CreatedAt       time.Time `db:"created_at"`
	UpdatedAt       time.Time `db:"updated_at"`
}

// SafetySources stores configured alert sources.
type SafetySources struct{ q Querier }

const safetySourceColumns = `id, user_id, name, image_url, url, critical_enabled, created_at, updated_at`

// CreateSafetySourceParams configures an alert source.
type CreateSafetySourceParams struct {
	ID              string
	UserID          string
	Name            string
	ImageURL        *string
	URL             *string
	CriticalEnabled bool
	Now             time.Time
}

// Create inserts a source with the owner's chosen defaults.
func (s *SafetySources) Create(ctx context.Context, p CreateSafetySourceParams) (*SafetySource, error) {
	const q = `
		INSERT INTO safety_sources (id, user_id, name, image_url, url, critical_enabled, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $7)
		RETURNING ` + safetySourceColumns
	return queryOne[SafetySource](ctx, s.q, "create safety source", q,
		p.ID, p.UserID, p.Name, p.ImageURL, p.URL, p.CriticalEnabled, Millis(p.Now))
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
	Name            Set[string]
	ImageURL        Set[*string]
	URL             Set[*string]
	CriticalEnabled Set[bool]
	Now             time.Time
}

// Update applies a partial change. It returns [ErrNotFound] when the source
// does not exist or belongs to someone else.
func (s *SafetySources) Update(ctx context.Context, p UpdateSafetySourceParams) (*SafetySource, error) {
	const q = `
		UPDATE safety_sources SET
			name             = CASE WHEN $3::boolean THEN $4::text     ELSE name             END,
			image_url        = CASE WHEN $5::boolean THEN $6::text     ELSE image_url        END,
			url              = CASE WHEN $7::boolean THEN $8::text     ELSE url              END,
			critical_enabled = CASE WHEN $9::boolean THEN $10::boolean ELSE critical_enabled END,
			updated_at = $11
		WHERE id = $1 AND user_id = $2
		RETURNING ` + safetySourceColumns

	nameSet, name := p.Name.args()
	imageSet, image := p.ImageURL.args()
	urlSet, url := p.URL.args()
	criticalSet, critical := p.CriticalEnabled.args()

	return queryOne[SafetySource](ctx, s.q, "update safety source", q,
		p.ID, p.UserID,
		nameSet, name,
		imageSet, image,
		urlSet, url,
		criticalSet, critical,
		Millis(p.Now))
}

// Delete removes a source and its event history.
func (s *SafetySources) Delete(ctx context.Context, id, userID string) (bool, error) {
	const q = `DELETE FROM safety_sources WHERE id = $1 AND user_id = $2`
	return execMatched(ctx, s.q, "delete safety source", q, id, userID)
}

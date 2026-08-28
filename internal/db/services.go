package db

import (
	"context"
	"time"
)

// Priority is a notification's delivery urgency. It is shared by services,
// events and agent notifications.
const (
	PriorityNormal        = "normal"
	PriorityTimeSensitive = "time_sensitive"
	PriorityCritical      = "critical"
)

// Priorities lists every priority a caller may request, in ascending urgency.
var Priorities = []string{PriorityNormal, PriorityTimeSensitive}

// CriticalPriorities are available only to services created in the Critical
// Alerts flow.
var CriticalPriorities = []string{PriorityNormal, PriorityTimeSensitive, PriorityCritical}

// ValidPriority reports whether p is a priority a caller may request.
func ValidPriority(p string) bool {
	for _, v := range Priorities {
		if v == p {
			return true
		}
	}
	return false
}

// ValidCriticalPriority reports whether p is available to a critical service.
func ValidCriticalPriority(p string) bool {
	for _, v := range CriticalPriorities {
		if v == p {
			return true
		}
	}
	return false
}

// Service is a named webhook endpoint. Its token, carried in the URL path, is
// the only credential. The row also holds defaults for omitted webhook fields.
type Service struct {
	ID       string  `db:"id"`
	UserID   string  `db:"user_id"`
	Title    string  `db:"title"`
	ImageURL *string `db:"image_url"`
	URL      *string `db:"url"`
	Priority string  `db:"priority"`
	// CriticalCapable places the service in the separate Critical Alerts flow.
	// CriticalEnabled is the service half of the two-switch delivery gate.
	CriticalCapable bool `db:"critical_capable"`
	CriticalEnabled bool `db:"critical_enabled"`
	// TokenHash authenticates an inbound webhook; TokenCiphertext lets the
	// owner read the full URL back after creation. The plaintext is returned
	// once, at creation and rotation, and never stored.
	TokenHash       string    `db:"token_hash"`
	TokenCiphertext string    `db:"token_ciphertext"`
	CreatedAt       time.Time `db:"created_at"`
	UpdatedAt       time.Time `db:"updated_at"`
}

// Services stores webhook sources.
type Services struct{ q Querier }

const serviceColumns = `id, user_id, title, image_url, url, priority, critical_capable, critical_enabled,
	token_hash, token_ciphertext, created_at, updated_at`

// CreateServiceParams creates a webhook source.
type CreateServiceParams struct {
	ID              string
	UserID          string
	Title           string
	ImageURL        *string
	URL             *string
	Priority        string
	CriticalCapable bool
	CriticalEnabled bool
	TokenHash       string
	TokenCiphertext string
	Now             time.Time
}

// Create inserts a service.
func (s *Services) Create(ctx context.Context, p CreateServiceParams) (*Service, error) {
	const q = `
		INSERT INTO services (id, user_id, title, image_url, url, priority,
		                      critical_capable, critical_enabled,
		                      token_hash, token_ciphertext, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11)
		RETURNING ` + serviceColumns
	return queryOne[Service](ctx, s.q, "create service", q,
		p.ID, p.UserID, p.Title, p.ImageURL, p.URL, p.Priority,
		p.CriticalCapable, p.CriticalEnabled,
		p.TokenHash, p.TokenCiphertext, Millis(p.Now))
}

// ByID loads a regular service the caller owns.
func (s *Services) ByID(ctx context.Context, id, userID string) (*Service, error) {
	const q = `SELECT ` + serviceColumns + ` FROM services
		WHERE id = $1 AND user_id = $2 AND NOT critical_capable`
	return queryOne[Service](ctx, s.q, "load service", q, id, userID)
}

// CriticalByID loads a critical-capable service the caller owns.
func (s *Services) CriticalByID(ctx context.Context, id, userID string) (*Service, error) {
	const q = `SELECT ` + serviceColumns + ` FROM services
		WHERE id = $1 AND user_id = $2 AND critical_capable`
	return queryOne[Service](ctx, s.q, "load critical service", q, id, userID)
}

// ByTokenHash authenticates an inbound webhook. This is the hot path of the
// whole ingest surface and is served entirely by the unique index.
func (s *Services) ByTokenHash(ctx context.Context, tokenHash string) (*Service, error) {
	const q = `SELECT ` + serviceColumns + ` FROM services WHERE token_hash = $1`
	return queryOne[Service](ctx, s.q, "authenticate webhook", q, tokenHash)
}

// ListForUser returns the account's regular services, newest first.
func (s *Services) ListForUser(ctx context.Context, userID string) ([]Service, error) {
	const q = `SELECT ` + serviceColumns + ` FROM services
		WHERE user_id = $1 AND NOT critical_capable ORDER BY created_at DESC, id DESC`
	return queryAll[Service](ctx, s.q, "list services", q, userID)
}

// ListCriticalForUser returns the account's critical services, newest first.
func (s *Services) ListCriticalForUser(ctx context.Context, userID string) ([]Service, error) {
	const q = `SELECT ` + serviceColumns + ` FROM services
		WHERE user_id = $1 AND critical_capable ORDER BY created_at DESC, id DESC`
	return queryAll[Service](ctx, s.q, "list critical services", q, userID)
}

// UpdateServiceParams is a partial update: an unset field is left alone, and a
// Set[*string] holding nil clears the column.
type UpdateServiceParams struct {
	ID       string
	UserID   string
	Title    Set[string]
	ImageURL Set[*string]
	URL      Set[*string]
	Priority Set[string]
	// CriticalCapable scopes the update to the correct management flow.
	CriticalCapable bool
	CriticalEnabled Set[bool]
	Now             time.Time
}

// Update applies a partial change. It returns [ErrNotFound] when the service
// does not exist or belongs to someone else.
func (s *Services) Update(ctx context.Context, p UpdateServiceParams) (*Service, error) {
	const q = `
		UPDATE services SET
			title            = CASE WHEN $4::boolean  THEN $5::text    ELSE title            END,
			image_url        = CASE WHEN $6::boolean  THEN $7::text    ELSE image_url        END,
			url              = CASE WHEN $8::boolean  THEN $9::text    ELSE url              END,
			priority         = CASE WHEN $10::boolean THEN $11::text   ELSE priority         END,
			critical_enabled = CASE WHEN $12::boolean THEN $13::boolean ELSE critical_enabled END,
			updated_at = $14
		WHERE id = $1 AND user_id = $2 AND critical_capable = $3
		RETURNING ` + serviceColumns

	titleSet, title := p.Title.args()
	imageSet, image := p.ImageURL.args()
	urlSet, url := p.URL.args()
	prioritySet, priority := p.Priority.args()
	criticalSet, critical := p.CriticalEnabled.args()

	return queryOne[Service](ctx, s.q, "update service", q,
		p.ID, p.UserID, p.CriticalCapable,
		titleSet, title,
		imageSet, image,
		urlSet, url,
		prioritySet, priority,
		criticalSet, critical,
		Millis(p.Now))
}

// RotateToken replaces a service's webhook credential. The previous token stops
// authenticating immediately: there is no grace period and no second slot.
func (s *Services) RotateToken(ctx context.Context, id, userID, tokenHash, tokenCiphertext string, now time.Time) (*Service, error) {
	const q = `
		UPDATE services SET token_hash = $3, token_ciphertext = $4, updated_at = $5
		WHERE id = $1 AND user_id = $2 AND NOT critical_capable
		RETURNING ` + serviceColumns
	return queryOne[Service](ctx, s.q, "rotate webhook token", q,
		id, userID, tokenHash, tokenCiphertext, Millis(now))
}

// RotateCriticalToken replaces a critical service's webhook credential.
func (s *Services) RotateCriticalToken(ctx context.Context, id, userID, tokenHash, tokenCiphertext string, now time.Time) (*Service, error) {
	const q = `
		UPDATE services SET token_hash = $3, token_ciphertext = $4, updated_at = $5
		WHERE id = $1 AND user_id = $2 AND critical_capable
		RETURNING ` + serviceColumns
	return queryOne[Service](ctx, s.q, "rotate critical webhook token", q,
		id, userID, tokenHash, tokenCiphertext, Millis(now))
}

// Delete removes a service.
//
// This is destructive to history: the cascade takes the service's events with
// it, the interactions those events spawned, and every Live Activity,
// operation and attempt the service requested.
func (s *Services) Delete(ctx context.Context, id, userID string) (bool, error) {
	const q = `DELETE FROM services WHERE id = $1 AND user_id = $2 AND NOT critical_capable`
	return execMatched(ctx, s.q, "delete service", q, id, userID)
}

// DeleteCritical removes a critical service and everything it produced.
func (s *Services) DeleteCritical(ctx context.Context, id, userID string) (bool, error) {
	const q = `DELETE FROM services WHERE id = $1 AND user_id = $2 AND critical_capable`
	return execMatched(ctx, s.q, "delete critical service", q, id, userID)
}

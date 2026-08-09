package db

import (
	"context"
	"slices"
	"time"
)

// The scope vocabulary. An API token carries a subset of these and nothing
// else; the database rejects any other member, so a bug in the validation
// layer cannot mint a token with an invented capability.
const (
	ScopeActivitiesRead   = "activities:read"
	ScopeActivitiesWrite  = "activities:write"
	ScopeDevicesRead      = "devices:read"
	ScopeEventsRead       = "events:read"
	ScopeInteractionsRead = "interactions:read"
	ScopeInteractionsNew  = "interactions:create"
	ScopeNotificationsNew = "notifications:send"
	ScopeServicesRead     = "services:read"
	ScopeServicesWrite    = "services:write"
)

// Scopes lists every scope, sorted. Stored scope arrays are deduplicated and
// sorted on write so that two tokens with the same capabilities compare equal.
var Scopes = []string{
	ScopeActivitiesRead, ScopeActivitiesWrite, ScopeDevicesRead, ScopeEventsRead,
	ScopeInteractionsNew, ScopeInteractionsRead, ScopeNotificationsNew,
	ScopeServicesRead, ScopeServicesWrite,
}

// ValidScope reports whether s is a known scope.
func ValidScope(s string) bool { return slices.Contains(Scopes, s) }

// NormalizeScopes deduplicates and sorts a scope list, and reports whether
// every member is known.
func NormalizeScopes(in []string) ([]string, bool) {
	out := slices.Clone(in)
	slices.Sort(out)
	out = slices.Compact(out)
	for _, s := range out {
		if !ValidScope(s) {
			return out, false
		}
	}
	return out, true
}

// MaxActiveAPITokens caps how many usable tokens an account may hold at once.
// It is enforced inside the transaction that inserts, so two concurrent mints
// cannot both see room for the last one.
const MaxActiveAPITokens = 25

// APIToken is an agent's bearer credential.
type APIToken struct {
	ID     string `db:"id"`
	UserID string `db:"user_id"`
	Name   string `db:"name"`
	// TokenHash is the domain-separated digest of a secret that was shown once
	// and never stored. Prefix is a display fragment of that same secret, so a
	// person can recognise which token a log line is about.
	TokenHash string   `db:"token_hash"`
	Prefix    string   `db:"prefix"`
	Scopes    []string `db:"scopes"`
	// ExpiresAt nil means the token never expires.
	ExpiresAt *time.Time `db:"expires_at"`
	// LastUsedAt is written at most once a minute per token.
	LastUsedAt *time.Time `db:"last_used_at"`
	// RevokedAt nil means active. Tokens are never hard-deleted, so everything
	// they created keeps its attribution.
	RevokedAt *time.Time `db:"revoked_at"`
	CreatedAt time.Time  `db:"created_at"`
}

// Active reports whether the token may authenticate at now.
func (t APIToken) Active(now time.Time) bool {
	if t.RevokedAt != nil {
		return false
	}
	return t.ExpiresAt == nil || t.ExpiresAt.After(now)
}

// HasScope reports whether the token carries scope.
func (t APIToken) HasScope(scope string) bool { return slices.Contains(t.Scopes, scope) }

// HasScopes reports whether the token carries all of them.
func (t APIToken) HasScopes(scopes ...string) bool {
	for _, s := range scopes {
		if !t.HasScope(s) {
			return false
		}
	}
	return true
}

// APITokens stores agent credentials.
type APITokens struct{ q Querier }

const apiTokenColumns = `id, user_id, name, token_hash, prefix, scopes,
	expires_at, last_used_at, revoked_at, created_at`

// CreateAPITokenParams mints a token.
type CreateAPITokenParams struct {
	ID        string
	UserID    string
	Name      string
	TokenHash string
	Prefix    string
	Scopes    []string
	ExpiresAt *time.Time
	Now       time.Time
}

// Create inserts a token. Callers must run it inside the same transaction as
// [APITokens.CountActive] so the active-token cap cannot be raced.
func (s *APITokens) Create(ctx context.Context, p CreateAPITokenParams) (*APIToken, error) {
	const q = `
		INSERT INTO api_tokens (id, user_id, name, token_hash, prefix, scopes, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING ` + apiTokenColumns
	return queryOne[APIToken](ctx, s.q, "create API token", q,
		p.ID, p.UserID, p.Name, p.TokenHash, p.Prefix, p.Scopes, millisPtr(p.ExpiresAt), Millis(p.Now))
}

// ByTokenHash resolves a bearer credential.
//
// Revoked and expired tokens are returned rather than filtered out: expiry is
// checked in Go because a SQL comparison would also discard the NULL expiry
// that means "never", and the caller answers all three failures identically so
// that an attacker cannot tell a revoked token from an unknown one.
func (s *APITokens) ByTokenHash(ctx context.Context, tokenHash string) (*APIToken, error) {
	const q = `SELECT ` + apiTokenColumns + ` FROM api_tokens WHERE token_hash = $1`
	return queryOne[APIToken](ctx, s.q, "authenticate API token", q, tokenHash)
}

// ByID loads a token the caller owns.
func (s *APITokens) ByID(ctx context.Context, id, userID string) (*APIToken, error) {
	const q = `SELECT ` + apiTokenColumns + ` FROM api_tokens WHERE id = $1 AND user_id = $2`
	return queryOne[APIToken](ctx, s.q, "load API token", q, id, userID)
}

// ListForUser returns every token on the account, newest first. Revoked tokens
// stay listed so their history is explainable.
func (s *APITokens) ListForUser(ctx context.Context, userID string) ([]APIToken, error) {
	const q = `SELECT ` + apiTokenColumns + ` FROM api_tokens
		WHERE user_id = $1 ORDER BY created_at DESC, id DESC`
	return queryAll[APIToken](ctx, s.q, "list API tokens", q, userID)
}

// CountActive counts the account's usable tokens against [MaxActiveAPITokens].
func (s *APITokens) CountActive(ctx context.Context, userID string, now time.Time) (int, error) {
	const q = `
		SELECT count(*) FROM api_tokens
		WHERE user_id = $1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > $2)`
	return queryValue[int](ctx, s.q, "count active API tokens", q, userID, Millis(now))
}

// Revoke disables a token the caller owns, reporting whether it was active
// beforehand. Revoking an already-revoked token reports false: the state is
// what the caller asked for, but nothing changed.
func (s *APITokens) Revoke(ctx context.Context, id, userID string, now time.Time) (bool, error) {
	const q = `
		UPDATE api_tokens SET revoked_at = $3
		WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL`
	return execMatched(ctx, s.q, "revoke API token", q, id, userID, Millis(now))
}

// RevokeSelf disables the calling token, for an agent retiring its own
// credential without a session.
func (s *APITokens) RevokeSelf(ctx context.Context, id string, now time.Time) (bool, error) {
	const q = `UPDATE api_tokens SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL`
	return execMatched(ctx, s.q, "revoke API token", q, id, Millis(now))
}

// TouchLastUsed records that a token authenticated, at most once per interval.
//
// The throttle matters: without it every agent request would dirty a row, and
// an agent polling an interaction makes several requests a second.
func (s *APITokens) TouchLastUsed(ctx context.Context, id string, now time.Time, interval time.Duration) (bool, error) {
	const q = `
		UPDATE api_tokens SET last_used_at = $2
		WHERE id = $1 AND (last_used_at IS NULL OR last_used_at <= $3)`
	stamp := Millis(now)
	return execMatched(ctx, s.q, "touch API token", q, id, stamp, stamp.Add(-interval))
}

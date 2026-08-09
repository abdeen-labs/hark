package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Session is one signed-in browser or app.
//
// The token itself is never stored: only its digest is, exactly as for API
// tokens and webhook tokens. A database dump therefore hands over no usable
// session.
type Session struct {
	ID        string    `db:"id"`
	UserID    string    `db:"user_id"`
	TokenHash string    `db:"token_hash"`
	CreatedAt time.Time `db:"created_at"`
	// RefreshedAt is when the sliding refresh last extended this session; the
	// refresh threshold is measured from it.
	RefreshedAt time.Time `db:"refreshed_at"`
	ExpiresAt   time.Time `db:"expires_at"`
}

// Sessions stores sign-ins.
type Sessions struct{ q Querier }

const sessionColumns = `id, user_id, token_hash, created_at, refreshed_at, expires_at`

// CreateSessionParams issues a session.
type CreateSessionParams struct {
	ID        string
	UserID    string
	TokenHash string
	ExpiresAt time.Time
	Now       time.Time
}

// Create records a new sign-in.
func (s *Sessions) Create(ctx context.Context, p CreateSessionParams) (*Session, error) {
	const q = `
		INSERT INTO sessions (id, user_id, token_hash, created_at, refreshed_at, expires_at)
		VALUES ($1, $2, $3, $4, $4, $5)
		RETURNING ` + sessionColumns
	return queryOne[Session](ctx, s.q, "create session", q,
		p.ID, p.UserID, p.TokenHash, Millis(p.Now), Millis(p.ExpiresAt))
}

// Authenticated is a session together with its owner, which is what every
// session-authenticated request needs.
type Authenticated struct {
	Session Session
	User    User
}

// ByTokenHash resolves a session cookie to its session and account. Expired
// sessions are returned rather than hidden: the caller decides whether to
// delete the row and clear the cookie, and it needs the row to do so.
func (s *Sessions) ByTokenHash(ctx context.Context, tokenHash string) (*Authenticated, error) {
	const q = `
		SELECT s.id, s.user_id, s.token_hash, s.created_at, s.refreshed_at, s.expires_at,
		       u.id, u.username, u.email, u.display_name, u.password_hash,
		       u.password_updated_at, u.welcome_sent_at, u.created_at, u.updated_at
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1`

	var a Authenticated
	err := s.q.QueryRow(ctx, q, tokenHash).Scan(
		&a.Session.ID, &a.Session.UserID, &a.Session.TokenHash,
		&a.Session.CreatedAt, &a.Session.RefreshedAt, &a.Session.ExpiresAt,
		&a.User.ID, &a.User.Username, &a.User.Email, &a.User.DisplayName, &a.User.PasswordHash,
		&a.User.PasswordUpdatedAt, &a.User.WelcomeSentAt, &a.User.CreatedAt, &a.User.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("db: load session: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("db: load session: %w", err)
	}
	return &a, nil
}

// Refresh extends a session's lifetime. It reports false when the row is gone,
// which is the concurrent-sign-out case: the caller then clears the cookie
// instead of pretending the session is alive.
func (s *Sessions) Refresh(ctx context.Context, id string, expiresAt, now time.Time) (bool, error) {
	const q = `UPDATE sessions SET expires_at = $2, refreshed_at = $3 WHERE id = $1`
	return execMatched(ctx, s.q, "refresh session", q, id, Millis(expiresAt), Millis(now))
}

// Delete signs one session out.
func (s *Sessions) Delete(ctx context.Context, id string) (bool, error) {
	return execMatched(ctx, s.q, "delete session", `DELETE FROM sessions WHERE id = $1`, id)
}

// DeleteForUser signs an account out everywhere, optionally keeping one
// session alive — the "change my password and log out my other devices" case,
// where signing out the caller too would be hostile.
func (s *Sessions) DeleteForUser(ctx context.Context, userID string, keepID string) (int64, error) {
	const q = `DELETE FROM sessions WHERE user_id = $1 AND ($2 = '' OR id <> $2)`
	return execAffected(ctx, s.q, "delete user sessions", q, userID, keepID)
}

// DeleteExpired removes sessions that are past their expiry. Nothing depends on
// it running — expiry is enforced on read — so it is housekeeping, safe to call
// on a timer or never.
func (s *Sessions) DeleteExpired(ctx context.Context, now time.Time) (int64, error) {
	const q = `DELETE FROM sessions WHERE expires_at <= $1`
	return execAffected(ctx, s.q, "delete expired sessions", q, Millis(now))
}

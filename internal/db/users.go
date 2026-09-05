package db

import (
	"context"
	"time"
)

const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

// User is an account. The initial admin provisions all additional users.
type User struct {
	ID       string `db:"id"`
	Username string `db:"username"`
	Email    string `db:"email"`
	Role     string `db:"role"`
	// DisplayName is the human-facing name; Username is the sign-in handle and
	// is stored already normalised (lowercased) by the caller.
	DisplayName string `db:"display_name"`
	// PasswordHash is NULL only for an account seeded without a password. Such an
	// account cannot sign in.
	PasswordHash      *string    `db:"password_hash"`
	PasswordUpdatedAt *time.Time `db:"password_updated_at"`
	// WelcomeSentAt is the claim flag for the one-shot welcome push. Once set it
	// is never cleared, not even when the push failed: the welcome is one-shot
	// per account for all time.
	WelcomeSentAt *time.Time `db:"welcome_sent_at"`
	// CriticalAlertsEnabled controls Critical Alerts across the account.
	CriticalAlertsEnabled bool      `db:"critical_alerts_enabled"`
	CreatedAt             time.Time `db:"created_at"`
	UpdatedAt             time.Time `db:"updated_at"`
}

// Users stores accounts.
type Users struct{ q Querier }

const userColumns = `id, username, email, role, display_name, password_hash,
	password_updated_at, welcome_sent_at, critical_alerts_enabled, created_at, updated_at`

// CreateUserParams describes a new account. Callers cannot choose its role.
type CreateUserParams struct {
	ID           string
	Username     string
	Email        string
	DisplayName  string
	PasswordHash *string
	Now          time.Time
}

// Create inserts a regular user. A duplicate username or email is a unique
// violation. Only CreateFirst can create an admin.
func (s *Users) Create(ctx context.Context, p CreateUserParams) (*User, error) {
	const q = `
		INSERT INTO users (id, username, email, display_name, password_hash,
		                   password_updated_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, CASE WHEN $5::text IS NULL THEN NULL ELSE $6::timestamptz END, $6, $6)
		RETURNING ` + userColumns
	now := Millis(p.Now)
	return queryOne[User](ctx, s.q, "create user", q,
		p.ID, p.Username, p.Email, p.DisplayName, p.PasswordHash, now)
}

// CreateFirst inserts the admin only while the table is still empty,
// returning [ErrNotFound] when one already exists.
//
// The unique admin index and ON CONFLICT guard allow only one startup process
// or CLI invocation to win, even if both see an empty table.
func (s *Users) CreateFirst(ctx context.Context, p CreateUserParams) (*User, error) {
	const q = `
		INSERT INTO users (id, username, email, display_name, password_hash,
		                   password_updated_at, created_at, updated_at, role)
		SELECT $1::text, $2::text, $3::text, $4::text, $5::text,
		       CASE WHEN $5::text IS NULL THEN NULL ELSE $6::timestamptz END,
		       $6::timestamptz, $6::timestamptz, 'admin'
		WHERE NOT EXISTS (SELECT 1 FROM users)
		ON CONFLICT DO NOTHING
		RETURNING ` + userColumns
	now := Millis(p.Now)
	return queryOne[User](ctx, s.q, "create first user", q,
		p.ID, p.Username, p.Email, p.DisplayName, p.PasswordHash, now)
}

// List returns accounts in creation order for the administrator.
func (s *Users) List(ctx context.Context) ([]User, error) {
	const q = `SELECT ` + userColumns + ` FROM users ORDER BY created_at, id`
	return queryAll[User](ctx, s.q, "list users", q)
}

// ByID loads one account.
func (s *Users) ByID(ctx context.Context, id string) (*User, error) {
	const q = `SELECT ` + userColumns + ` FROM users WHERE id = $1`
	return queryOne[User](ctx, s.q, "load user", q, id)
}

// ByUsername loads an account by its normalised sign-in handle.
func (s *Users) ByUsername(ctx context.Context, username string) (*User, error) {
	const q = `SELECT ` + userColumns + ` FROM users WHERE username = $1`
	return queryOne[User](ctx, s.q, "load user by username", q, username)
}

// SetPassword replaces the stored password hash.
func (s *Users) SetPassword(ctx context.Context, id, hash string, now time.Time) error {
	const q = `
		UPDATE users SET password_hash = $2, password_updated_at = $3, updated_at = $3
		WHERE id = $1`
	return execOne(ctx, s.q, "set password", q, id, hash, Millis(now))
}

// SetCriticalAlertsEnabled writes the account-wide critical delivery toggle.
func (s *Users) SetCriticalAlertsEnabled(ctx context.Context, id string, enabled bool, now time.Time) error {
	const q = `
		UPDATE users SET critical_alerts_enabled = $2, updated_at = $3
		WHERE id = $1`
	return execOne(ctx, s.q, "set critical alerts toggle", q, id, enabled, Millis(now))
}

// ClaimWelcome atomically claims the account's one-time welcome notification.
// A failed push does not release the claim.
func (s *Users) ClaimWelcome(ctx context.Context, id string, now time.Time) (bool, error) {
	const q = `
		UPDATE users SET welcome_sent_at = $2, updated_at = $2
		WHERE id = $1 AND welcome_sent_at IS NULL`
	return execMatched(ctx, s.q, "claim welcome notification", q, id, Millis(now))
}

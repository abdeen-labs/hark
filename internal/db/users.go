package db

import (
	"context"
	"time"
)

// User is the single account this deployment serves. There is no sign-up
// surface: the row is seeded at boot and never created by a request.
type User struct {
	ID       string `db:"id"`
	Username string `db:"username"`
	Email    string `db:"email"`
	// DisplayName is the human-facing name; Username is the sign-in handle and
	// is stored already normalised (lowercased) by the caller.
	DisplayName string `db:"display_name"`
	// PasswordHash is NULL only for an account seeded without a password, which
	// nobody can sign in to.
	PasswordHash      *string    `db:"password_hash"`
	PasswordUpdatedAt *time.Time `db:"password_updated_at"`
	// WelcomeSentAt is the claim flag for the one-shot welcome push. Once set it
	// is never cleared, not even when the push failed: the welcome is one-shot
	// per account for all time.
	WelcomeSentAt *time.Time `db:"welcome_sent_at"`
	CreatedAt     time.Time  `db:"created_at"`
	UpdatedAt     time.Time  `db:"updated_at"`
}

// Users stores accounts.
type Users struct{ q Querier }

const userColumns = `id, username, email, display_name, password_hash,
	password_updated_at, welcome_sent_at, created_at, updated_at`

// CreateUserParams seeds the account.
type CreateUserParams struct {
	ID           string
	Username     string
	Email        string
	DisplayName  string
	PasswordHash *string
	Now          time.Time
}

// Create inserts the account. A duplicate username or email is a unique
// violation, which the boot-time seeder treats as "someone else seeded first".
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

// CreateFirst inserts the account only while the table is still empty,
// returning [ErrNotFound] when one already exists.
//
// The guard lives in the statement rather than in a preceding SELECT because
// that is what makes it a real invariant: two processes seeding at boot, or a
// seed racing an operator running the CLI, cannot both succeed no matter how
// their transactions interleave. It is the whole mechanism behind Hark being
// single-user — there is no sign-up route to close because there is no second
// way in.
func (s *Users) CreateFirst(ctx context.Context, p CreateUserParams) (*User, error) {
	const q = `
		INSERT INTO users (id, username, email, display_name, password_hash,
		                   password_updated_at, created_at, updated_at)
		SELECT $1::text, $2::text, $3::text, $4::text, $5::text,
		       CASE WHEN $5::text IS NULL THEN NULL ELSE $6::timestamptz END,
		       $6::timestamptz, $6::timestamptz
		WHERE NOT EXISTS (SELECT 1 FROM users)
		RETURNING ` + userColumns
	now := Millis(p.Now)
	return queryOne[User](ctx, s.q, "create first user", q,
		p.ID, p.Username, p.Email, p.DisplayName, p.PasswordHash, now)
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

// Count reports how many accounts exist. The seeder uses it to decide whether
// there is anything to seed.
func (s *Users) Count(ctx context.Context) (int, error) {
	return queryValue[int](ctx, s.q, "count users", `SELECT count(*) FROM users`)
}

// SetPassword replaces the stored password hash.
func (s *Users) SetPassword(ctx context.Context, id, hash string, now time.Time) error {
	const q = `
		UPDATE users SET password_hash = $2, password_updated_at = $3, updated_at = $3
		WHERE id = $1`
	return execOne(ctx, s.q, "set password", q, id, hash, Millis(now))
}

// ClaimWelcome atomically claims the account's one-shot welcome notification,
// reporting whether this caller won. It is the whole mechanism: the claim is
// what authorises sending the welcome pushes, and it is deliberately not rolled
// back when those pushes fail.
func (s *Users) ClaimWelcome(ctx context.Context, id string, now time.Time) (bool, error) {
	const q = `
		UPDATE users SET welcome_sent_at = $2, updated_at = $2
		WHERE id = $1 AND welcome_sent_at IS NULL`
	return execMatched(ctx, s.q, "claim welcome notification", q, id, Millis(now))
}

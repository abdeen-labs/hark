// Package auth owns every credential this deployment recognises: account
// passwords, browser and app sessions, agent API tokens, and the
// device-grant flow a CLI uses to pair itself.
//
// Nothing here formats HTTP. The package issues and resolves credentials and
// returns typed outcomes; internal/httpapi decides status codes, sets the
// cookie, and applies rate limits. That split is what lets the same session
// token arrive either in a cookie or in an Authorization header without the
// domain logic knowing which.
//
// Three rules hold for every credential in this package:
//
//   - Secrets come from crypto/rand and are handed to the caller exactly once.
//     Only a digest is stored, so a database dump grants nothing.
//   - Digests are domain-separated by credential kind, so a value lifted from
//     one table cannot be replayed against another.
//   - Every resolution failure — unknown, malformed, expired, revoked — is
//     reported as the same [ErrInvalidCredentials], so a caller cannot probe
//     for which of those it hit.
package auth

import (
	"errors"
	"time"

	"github.com/abdeen-labs/hark/internal/db"
)

// Errors callers branch on. Everything else is wrapped and only logged.
var (
	// ErrInvalidCredentials is the single answer to every failed credential
	// resolution: unknown username, wrong password, unknown or malformed
	// token, expired session, revoked API token.
	ErrInvalidCredentials = errors.New("auth: invalid credentials")

	// ErrAccountExists reports that the deployment is already bootstrapped.
	ErrAccountExists = errors.New("auth: an account already exists")

	// ErrAdminRequired reports an attempt to manage users without an admin session.
	ErrAdminRequired = errors.New("auth: an administrator session is required")

	// ErrNotFound reports that the addressed credential or pairing request does
	// not exist.
	ErrNotFound = errors.New("auth: not found")

	// ErrConflict reports that the target is no longer in a state the operation
	// accepts — a pairing request already resolved, a token already revoked.
	ErrConflict = errors.New("auth: not in an actionable state")

	// ErrTokenLimit reports that the account already holds [db.MaxActiveAPITokens]
	// usable tokens.
	ErrTokenLimit = errors.New("auth: active API token limit reached")

	// ErrUnavailable reports a transient failure the caller should retry, which
	// today means only repeated code collisions when starting a device grant.
	ErrUnavailable = errors.New("auth: temporarily unavailable")
)

// InvalidInputError reports a value the caller can fix. Field names the JSON
// field it came from so the transport can render a per-field validation error.
type InvalidInputError struct {
	Field   string
	Message string
}

func (e *InvalidInputError) Error() string { return e.Field + ": " + e.Message }

func invalid(field, message string) error {
	return &InvalidInputError{Field: field, Message: message}
}

// Service issues and resolves credentials against the store.
//
// It holds no mutable state, so one instance is shared by every request.
type Service struct {
	store *db.Store
	now   func() time.Time
}

// New returns a Service backed by store. A nil now uses [time.Now]; tests pass
// their own so that expiry and sliding refresh are exercised without sleeping.
func New(store *db.Store, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, now: now}
}

// Now is the service's clock, exported so a handler stamps a response with the
// same instant the service used.
func (s *Service) Now() time.Time { return db.Millis(s.now()) }

// translate maps the store's sentinels onto this package's, so callers never
// need to import internal/db to interpret an error.
func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, db.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, db.ErrConflict):
		return ErrConflict
	default:
		return err
	}
}

// touchInterval throttles the last_used_at write on an API token: an agent
// polling an interaction makes several requests a second, and none of them
// need to dirty the row.
const touchInterval = time.Minute

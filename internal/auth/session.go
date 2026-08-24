package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/id"
)

// Session lifetime.
//
// SessionTTL is an idle timeout. Requests refresh expiry at most once per
// SessionRefreshInterval. SessionMaxLifetime is the fixed upper limit from
// creation and cannot be extended by activity.
const (
	SessionTTL             = 30 * 24 * time.Hour
	SessionRefreshInterval = time.Hour
	SessionMaxLifetime     = 180 * 24 * time.Hour
)

// Login verifies a username and password and issues a session.
//
// The returned string is the session secret. It is the only time the plaintext
// exists outside the caller's request: the row keeps a digest.
//
// Every failure is [ErrInvalidCredentials] — unknown user, no password set, or
// wrong password. Unknown usernames return faster because no password hash is
// computed. The transport rate limit bounds guessing attempts without running
// a 64 MiB hash for arbitrary usernames.
func (s *Service) Login(ctx context.Context, username, password string) (*Principal, string, error) {
	normalized, err := normalizeUsername(username)
	if err != nil {
		// A username that cannot exist is answered like one that does not.
		return nil, "", ErrInvalidCredentials
	}

	user, err := s.store.Users.ByUsername(ctx, normalized)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, "", ErrInvalidCredentials
		}
		return nil, "", fmt.Errorf("auth: load account: %w", err)
	}
	if user.PasswordHash == nil || *user.PasswordHash == "" {
		return nil, "", ErrInvalidCredentials
	}
	if err := VerifyPassword(*user.PasswordHash, password); err != nil {
		return nil, "", ErrInvalidCredentials
	}

	now := s.Now()
	if NeedsRehash(*user.PasswordHash) {
		// Raising the Argon2 cost upgrades the stored hash on the next
		// successful sign-in. Failing to write it must not fail the sign-in:
		// the password was correct either way.
		if hash, err := HashPassword(password); err == nil {
			if err := s.store.Users.SetPassword(ctx, user.ID, hash, now); err == nil {
				user.PasswordHash = &hash
			}
		}
	}

	// Expired rows are swept here rather than by a scheduled job, the same way
	// resolved device-authorization requests are: signing in is the only thing
	// that creates a session, so it is the right moment to clear the ones that
	// are past use. A sweep that fails is not a failed sign-in.
	_, _ = s.store.Sessions.DeleteExpired(ctx, now)

	token := NewSessionToken()
	session, err := s.store.Sessions.Create(ctx, db.CreateSessionParams{
		ID:        id.New(),
		UserID:    user.ID,
		TokenHash: SessionTokenHash(token),
		ExpiresAt: now.Add(SessionTTL),
		Now:       now,
	})
	if err != nil {
		return nil, "", fmt.Errorf("auth: create session: %w", err)
	}

	return &Principal{Kind: KindSession, User: *user, Session: session}, token, nil
}

// AuthenticateSession resolves a session secret to its principal, sliding the
// expiry forward when the session has not been refreshed recently.
//
// It satisfies the transport's credential-resolver interface, which is why it
// takes only the token: the cookie and the Authorization header carry the same
// value and must resolve identically.
func (s *Service) AuthenticateSession(ctx context.Context, token string) (*Principal, error) {
	if !ValidSessionToken(token) {
		return nil, ErrInvalidCredentials
	}

	found, err := s.store.Sessions.ByTokenHash(ctx, SessionTokenHash(token))
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("auth: load session: %w", err)
	}

	now := s.Now()
	session := found.Session
	if !session.ExpiresAt.After(now) {
		// Expiry is enforced on read, and the read is what cleans up. There is
		// no sweeper to depend on.
		if _, err := s.store.Sessions.Delete(ctx, session.ID); err != nil {
			return nil, fmt.Errorf("auth: delete expired session: %w", err)
		}
		return nil, ErrInvalidCredentials
	}

	principal := &Principal{Kind: KindSession, User: found.User, Session: &session}
	if now.Sub(session.RefreshedAt) < SessionRefreshInterval {
		return principal, nil
	}

	expiresAt := sessionExpiry(session.CreatedAt, now)
	if !expiresAt.After(now) {
		// The absolute ceiling has been reached: the session cannot be extended
		// and is done, however recently it was used.
		if _, err := s.store.Sessions.Delete(ctx, session.ID); err != nil {
			return nil, fmt.Errorf("auth: delete session past its maximum lifetime: %w", err)
		}
		return nil, ErrInvalidCredentials
	}

	refreshed, err := s.store.Sessions.Refresh(ctx, session.ID, expiresAt, now)
	if err != nil {
		return nil, fmt.Errorf("auth: refresh session: %w", err)
	}
	if !refreshed {
		// Signed out from another client between the read and the write.
		return nil, ErrInvalidCredentials
	}

	principal.Session.ExpiresAt = db.Millis(expiresAt)
	principal.Session.RefreshedAt = now
	principal.Refreshed = true
	return principal, nil
}

// Logout drops one session. It reports no error for a session that is already
// gone: signing out twice is the same outcome as signing out once.
func (s *Service) Logout(ctx context.Context, sessionID string) error {
	if _, err := s.store.Sessions.Delete(ctx, sessionID); err != nil {
		return fmt.Errorf("auth: delete session: %w", err)
	}
	return nil
}

// Signing out of every session at once is not a method here. The two things
// that need it — replacing a password from the command line and changing it
// from a session — each do it inside their own transaction, where the deletion
// either commits with the new password or not at all.

// sessionExpiry is the new expiry a refresh grants: a full TTL from now, but
// never past the absolute ceiling measured from when the session began.
func sessionExpiry(createdAt, now time.Time) time.Time {
	expiresAt := now.Add(SessionTTL)
	if ceiling := createdAt.Add(SessionMaxLifetime); expiresAt.After(ceiling) {
		return ceiling
	}
	return expiresAt
}

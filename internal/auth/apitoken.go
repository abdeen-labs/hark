package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/id"
)

// API token bounds.
const (
	MaxAPITokenNameLength = 80

	// MinAPITokenLifetime and MaxAPITokenLifetime bound a requested expiry. The
	// floor keeps a token from being born useless; the ceiling keeps "expires"
	// from meaning "in a decade".
	MinAPITokenLifetime = time.Hour
	MaxAPITokenLifetime = 365 * 24 * time.Hour

	// DefaultAPITokenLifetime is what a device grant asks for when the client
	// does not say.
	DefaultAPITokenLifetime = 90 * 24 * time.Hour
)

// CreateAPITokenParams mints an agent credential.
type CreateAPITokenParams struct {
	// Name is what the token is for, shown in listings. Required.
	Name string
	// Scopes are the capabilities granted. At least one, all known; the stored
	// list is deduplicated and sorted so two tokens with the same authority
	// compare equal.
	Scopes []string
	// ExpiresIn bounds the token's life. Zero means it never expires.
	ExpiresIn time.Duration
}

// CreateAPIToken mints a token and returns it together with its plaintext
// secret. The secret is never stored and never recoverable: this return value
// is the only time it exists.
//
// The active-token cap is counted and enforced inside the same transaction as
// the insert, so two concurrent mints cannot both see room for the last slot.
func (s *Service) CreateAPIToken(ctx context.Context, userID string, p CreateAPITokenParams) (*db.APIToken, string, error) {
	name := strings.TrimSpace(p.Name)
	if name == "" || len(name) > MaxAPITokenNameLength {
		return nil, "", invalid("name", fmt.Sprintf("must be 1-%d characters", MaxAPITokenNameLength))
	}
	scopes, err := validateScopes(p.Scopes)
	if err != nil {
		return nil, "", err
	}
	expiresIn, err := validateLifetime("expires_in_seconds", p.ExpiresIn)
	if err != nil {
		return nil, "", err
	}

	now := s.Now()
	var expiresAt *time.Time
	if expiresIn > 0 {
		at := now.Add(expiresIn)
		expiresAt = &at
	}

	secret := NewAPIToken()
	var token *db.APIToken
	err = s.store.Tx(ctx, func(ctx context.Context, tx *db.Store) error {
		active, err := tx.APITokens.CountActive(ctx, userID, now)
		if err != nil {
			return fmt.Errorf("auth: count active API tokens: %w", err)
		}
		if active >= db.MaxActiveAPITokens {
			return ErrTokenLimit
		}
		token, err = tx.APITokens.Create(ctx, db.CreateAPITokenParams{
			ID:        id.New(),
			UserID:    userID,
			Name:      name,
			TokenHash: APITokenHash(secret),
			Prefix:    APITokenDisplayPrefix(secret),
			Scopes:    scopes,
			ExpiresAt: expiresAt,
			Now:       now,
		})
		if err != nil {
			return fmt.Errorf("auth: create API token: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return token, secret, nil
}

// ListAPITokens returns every token on the account, newest first. Revoked and
// expired tokens stay listed so their history stays explainable.
func (s *Service) ListAPITokens(ctx context.Context, userID string) ([]db.APIToken, error) {
	tokens, err := s.store.APITokens.ListForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("auth: list API tokens: %w", err)
	}
	return tokens, nil
}

// RevokeAPIToken disables a token the account owns, immediately: the next
// request carrying it is rejected.
//
// An unknown id, another account's id, and an already-revoked token are all
// [ErrNotFound] — the caller asked for a token it cannot act on, and which of
// the three it was is not its business.
func (s *Service) RevokeAPIToken(ctx context.Context, tokenID, userID string) error {
	revoked, err := s.store.APITokens.Revoke(ctx, tokenID, userID, s.Now())
	if err != nil {
		return fmt.Errorf("auth: revoke API token: %w", err)
	}
	if !revoked {
		return ErrNotFound
	}
	return nil
}

// RevokeSelf retires the calling token. It is how an agent signs itself out
// without holding a session, and it is idempotent.
func (s *Service) RevokeSelf(ctx context.Context, tokenID string) error {
	if _, err := s.store.APITokens.RevokeSelf(ctx, tokenID, s.Now()); err != nil {
		return fmt.Errorf("auth: revoke API token: %w", err)
	}
	return nil
}

// AuthenticateAPIToken resolves an agent secret to its principal.
//
// Unknown, revoked and expired tokens are all [ErrInvalidCredentials]: the row
// is fetched without filtering so that expiry is judged in Go, where a NULL
// expiry unambiguously means "never" rather than "excluded by the comparison".
func (s *Service) AuthenticateAPIToken(ctx context.Context, secret string) (*Principal, error) {
	if !ValidAPIToken(secret) {
		return nil, ErrInvalidCredentials
	}

	token, err := s.store.APITokens.ByTokenHash(ctx, APITokenHash(secret))
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("auth: load API token: %w", err)
	}

	now := s.Now()
	if !token.Active(now) {
		return nil, ErrInvalidCredentials
	}

	user, err := s.store.Users.ByID(ctx, token.UserID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("auth: load token account: %w", err)
	}

	// Last-use updates are best effort and limited to once a minute per token.
	// The in-memory value changes only after the guarded database update succeeds.
	if stamped, err := s.store.APITokens.TouchLastUsed(ctx, token.ID, now, touchInterval); err == nil && stamped {
		token.LastUsedAt = &now
	}

	return &Principal{Kind: KindAPIToken, User: *user, APIToken: token}, nil
}

// validateScopes checks membership and returns the canonical stored form.
func validateScopes(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, invalid("scopes", "must list at least one scope")
	}
	scopes, ok := db.NormalizeScopes(raw)
	if !ok {
		return nil, invalid("scopes", "must contain only known scopes: "+strings.Join(db.Scopes, ", "))
	}
	return scopes, nil
}

func validateLifetime(field string, d time.Duration) (time.Duration, error) {
	if d == 0 {
		return 0, nil
	}
	if d < MinAPITokenLifetime || d > MaxAPITokenLifetime {
		return 0, invalid(field, fmt.Sprintf("must be between %d and %d seconds",
			int(MinAPITokenLifetime.Seconds()), int(MaxAPITokenLifetime.Seconds())))
	}
	return d, nil
}

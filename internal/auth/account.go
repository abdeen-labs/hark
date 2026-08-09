package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/id"
)

// Username bounds. The handle is only ever typed into a sign-in form, so it is
// deliberately narrow: ASCII letters, digits, underscore and dot.
const (
	MinUsernameLength = 3
	MaxUsernameLength = 30
)

// CreateAccountParams provisions the deployment's single account.
type CreateAccountParams struct {
	Username string
	Password string
	// Email is stored for display only — Hark sends no mail, ever. Empty
	// derives <username>@hark.local.
	Email string
	// DisplayName is the human-facing name. Empty uses Username verbatim,
	// before it is lowercased.
	DisplayName string
}

// CreateAccount provisions the one account this deployment serves.
//
// It is the only path that creates a user, and it refuses once an account
// exists — the guard lives in the INSERT itself, so two processes racing at
// boot cannot both win. There is no sign-up endpoint precisely because this is
// the whole surface: `harkd create-user`, or the boot-time seed reading the
// same values from the environment.
func (s *Service) CreateAccount(ctx context.Context, p CreateAccountParams) (*db.User, error) {
	username, err := normalizeUsername(p.Username)
	if err != nil {
		return nil, err
	}

	email := strings.ToLower(strings.TrimSpace(p.Email))
	if email == "" {
		email = username + "@hark.local"
	} else if !strings.Contains(email, "@") {
		return nil, invalid("email", "must be an email address")
	}

	display := strings.TrimSpace(p.DisplayName)
	if display == "" {
		display = strings.TrimSpace(p.Username)
	}

	hash, err := HashPassword(p.Password)
	if err != nil {
		return nil, passwordInputError("password", err)
	}

	now := s.Now()
	user, err := s.store.Users.CreateFirst(ctx, db.CreateUserParams{
		ID:           id.New(),
		Username:     username,
		Email:        email,
		DisplayName:  display,
		PasswordHash: &hash,
		Now:          now,
	})
	switch {
	case errors.Is(err, db.ErrNotFound):
		// The guarded INSERT matched no row, which can only mean the table was
		// already occupied.
		return nil, ErrAccountExists
	case err != nil:
		return nil, fmt.Errorf("auth: create account: %w", err)
	}
	return user, nil
}

// SetPassword replaces an account's password without knowing the old one. It is
// the operator's recovery path (`harkd set-password`) and is reachable only
// from the command line — no request can call it.
//
// Every session is dropped, because a password reset means the old one may be
// in the wrong hands.
func (s *Service) SetPassword(ctx context.Context, username, password string) error {
	normalized, err := normalizeUsername(username)
	if err != nil {
		return err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return passwordInputError("password", err)
	}

	user, err := s.store.Users.ByUsername(ctx, normalized)
	if err != nil {
		return translate(err)
	}

	now := s.Now()
	return s.store.Tx(ctx, func(ctx context.Context, tx *db.Store) error {
		if err := tx.Users.SetPassword(ctx, user.ID, hash, now); err != nil {
			return translate(err)
		}
		if _, err := tx.Sessions.DeleteForUser(ctx, user.ID, ""); err != nil {
			return fmt.Errorf("auth: revoke sessions: %w", err)
		}
		return nil
	})
}

// ChangePassword re-keys the account for a signed-in caller.
//
// The current password is required, so a stolen session alone cannot lock the
// owner out. Every other session is dropped and the caller's own survives:
// changing a password from the dashboard should sign out the laptop you lost,
// not the browser you are typing into.
func (s *Service) ChangePassword(ctx context.Context, p *Principal, current, next string) error {
	if !p.IsSession() {
		return ErrConflict
	}
	if p.User.PasswordHash == nil {
		return ErrInvalidCredentials
	}
	if err := VerifyPassword(*p.User.PasswordHash, current); err != nil {
		return ErrInvalidCredentials
	}

	hash, err := HashPassword(next)
	if err != nil {
		return passwordInputError("new_password", err)
	}

	now := s.Now()
	return s.store.Tx(ctx, func(ctx context.Context, tx *db.Store) error {
		if err := tx.Users.SetPassword(ctx, p.User.ID, hash, now); err != nil {
			return translate(err)
		}
		if _, err := tx.Sessions.DeleteForUser(ctx, p.User.ID, p.Session.ID); err != nil {
			return fmt.Errorf("auth: revoke other sessions: %w", err)
		}
		return nil
	})
}

// normalizeUsername lowercases and validates a sign-in handle. Lookups use the
// lowercased form, so "Admin" and "admin" are the same account.
func normalizeUsername(raw string) (string, error) {
	username := strings.ToLower(strings.TrimSpace(raw))
	if n := len(username); n < MinUsernameLength || n > MaxUsernameLength {
		return "", invalid("username", fmt.Sprintf("must be %d-%d characters",
			MinUsernameLength, MaxUsernameLength))
	}
	for i := 0; i < len(username); i++ {
		c := username[i]
		ok := c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '.'
		if !ok {
			return "", invalid("username", "may contain only letters, digits, underscore, and dot")
		}
	}
	return username, nil
}

// passwordInputError turns a policy failure into a field error the transport
// can render, and leaves anything else alone.
func passwordInputError(field string, err error) error {
	switch {
	case errors.Is(err, ErrPasswordTooShort):
		return invalid(field, fmt.Sprintf("must be at least %d characters", MinPasswordLength))
	case errors.Is(err, ErrPasswordTooLong):
		return invalid(field, fmt.Sprintf("must be at most %d characters", MaxPasswordLength))
	case errors.Is(err, ErrPasswordControl):
		return invalid(field, "must not contain control characters")
	default:
		return err
	}
}

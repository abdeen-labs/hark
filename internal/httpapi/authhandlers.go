package httpapi

import (
	"errors"
	"net/http"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
)

// userDTO is the public account representation. Password hashes stay private.
type userDTO struct {
	ID          string    `json:"id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	Email       string    `json:"email"`
	Role        string    `json:"role"`
	CreatedAt   Timestamp `json:"created_at"`
}

func newUserDTO(u db.User) userDTO {
	return userDTO{
		ID:          u.ID,
		Username:    u.Username,
		DisplayName: u.DisplayName,
		Email:       u.Email,
		Role:        u.Role,
		CreatedAt:   Timestamp(u.CreatedAt),
	}
}

// sessionDTO describes one sign-in. The token is never echoed: the caller
// already holds it, and a value that appears in a response body ends up in
// somebody's log.
type sessionDTO struct {
	ID        string    `json:"id"`
	CreatedAt Timestamp `json:"created_at"`
	ExpiresAt Timestamp `json:"expires_at"`
}

func newSessionDTO(s db.Session) sessionDTO {
	return sessionDTO{
		ID:        s.ID,
		CreatedAt: Timestamp(s.CreatedAt),
		ExpiresAt: Timestamp(s.ExpiresAt),
	}
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token     string     `json:"token"`
	ExpiresAt Timestamp  `json:"expires_at"`
	User      userDTO    `json:"user"`
	Session   sessionDTO `json:"session"`
}

// handleLogin exchanges a username and password for a session.
//
// The same token is returned in the body and set as a cookie. A browser uses
// the cookie and ignores the body's copy; a native client stores the token and
// sends it as a bearer credential. One credential, two transports, so there is
// no second code path to keep honest.
func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body loginRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	principal, token, err := s.opts.Auth.Login(r.Context(), body.Username, body.Password)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			WriteError(w, r, http.StatusUnauthorized, CodeUnauthorized,
				"That username and password do not match an account.")
			return
		}
		s.writeInternal(w, r, "sign-in failed", err)
		return
	}

	now := s.opts.Auth.Now()
	s.cookie.Set(w, token, principal.Session.ExpiresAt, now)
	// The body carries a live credential; it must not sit in any cache.
	w.Header().Set("Cache-Control", "no-store")
	WriteJSON(w, r, http.StatusOK, loginResponse{
		Token:     token,
		ExpiresAt: Timestamp(principal.Session.ExpiresAt),
		User:      newUserDTO(principal.User),
		Session:   newSessionDTO(*principal.Session),
	})
}

// handleLogout retires whichever credential the caller presented: a session is
// deleted and its cookie cleared, an API token is revoked.
//
// One endpoint covers both because "sign out" means the same thing to a browser
// and to a CLI — the credential I am holding should stop working — and a client
// should not have to know which kind it holds to say so. It is idempotent:
// retiring a credential that is already gone is a success.
func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFrom(r.Context())

	var err error
	if principal.IsSession() {
		err = s.opts.Auth.Logout(r.Context(), principal.Session.ID)
		s.cookie.Clear(w)
	} else {
		err = s.opts.Auth.RevokeSelf(r.Context(), principal.APIToken.ID)
	}
	if err != nil {
		s.writeInternal(w, r, "sign-out failed", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// principalResponse is what the caller is, as the server sees it.
type principalResponse struct {
	Kind     string      `json:"kind"`
	User     userDTO     `json:"user"`
	Session  *sessionDTO `json:"session"`
	APIToken *tokenDTO   `json:"api_token"`
}

// handleSession describes the current principal. It is how a dashboard decides
// whether to render a sign-in form and how an agent checks that its token still
// works.
func (s *server) handleSession(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFrom(r.Context())

	out := principalResponse{
		Kind: string(principal.Kind),
		User: newUserDTO(principal.User),
	}
	if principal.Session != nil {
		dto := newSessionDTO(*principal.Session)
		out.Session = &dto
	}
	if principal.APIToken != nil {
		dto := newTokenDTO(*principal.APIToken)
		out.APIToken = &dto
	}

	w.Header().Set("Cache-Control", "no-store")
	WriteJSON(w, r, http.StatusOK, out)
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// handleChangePassword re-keys the account. Every other session is dropped and
// the caller's own survives, so changing the password from the dashboard signs
// out the device you lost rather than the browser you are typing into.
func (s *server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var body changePasswordRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	principal := auth.PrincipalFrom(r.Context())
	err := s.opts.Auth.ChangePassword(r.Context(), principal, body.CurrentPassword, body.NewPassword)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, auth.ErrInvalidCredentials):
		WriteError(w, r, http.StatusUnauthorized, CodeUnauthorized,
			"The current password is not correct.")
	default:
		s.writeAuthError(w, r, "changing the password failed", err)
	}
}

// writeAuthError renders the outcomes every auth handler shares, and falls back
// to a 500 for anything unrecognised.
func (s *server) writeAuthError(w http.ResponseWriter, r *http.Request, what string, err error) {
	var invalid *auth.InvalidInputError
	switch {
	case errors.Is(err, auth.ErrAdminRequired):
		WriteError(w, r, http.StatusForbidden, CodeAdminRequired,
			"Only the administrator can manage accounts.")
	case errors.As(err, &invalid):
		WriteFieldErrors(w, r, "The request body is invalid.",
			[]FieldError{{Field: invalid.Field, Message: invalid.Message}})
	case errors.Is(err, auth.ErrNotFound):
		WriteError(w, r, http.StatusNotFound, CodeNotFound, "No such resource.")
	case errors.Is(err, auth.ErrConflict):
		WriteError(w, r, http.StatusConflict, CodeConflict,
			"That resource is not in a state this request can act on.")
	case errors.Is(err, auth.ErrTokenLimit):
		WriteError(w, r, http.StatusConflict, CodeTokenLimitReached,
			"This account already holds the maximum number of active API tokens.")
	case errors.Is(err, auth.ErrUnavailable):
		WriteError(w, r, http.StatusServiceUnavailable, CodeUnavailable,
			"The request could not be completed right now. Try again.")
	default:
		s.writeInternal(w, r, what, err)
	}
}

// writeInternal logs the cause and answers with the opaque 500. The message a
// client sees never carries internal detail; the request id ties it to the log
// line that does.
func (s *server) writeInternal(w http.ResponseWriter, r *http.Request, what string, err error) {
	LoggerFrom(r.Context()).ErrorContext(r.Context(), what, "error", err)
	WriteError(w, r, http.StatusInternalServerError, CodeInternal,
		"The server hit an unexpected error.")
}

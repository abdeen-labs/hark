package dashboard

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"

	"github.com/abdeen-labs/hark/internal/httpapi"
)

// CSRF protection is a double-submit cookie.
//
// The API already enforces same-origin rules for unsafe cookie-authenticated
// requests. The double-submit token also protects sign-in, which happens before
// the browser has a session cookie. Server-rendered forms copy the HttpOnly
// cookie value into a hidden field without exposing it to scripts.
const (
	csrfCookieName = "hark_csrf"
	csrfField      = "csrf_token"
	csrfTokenBytes = 32
)

// csrfCookie issues and checks the anti-forgery token.
type csrfCookie struct {
	name   string
	secure bool
}

// newCSRFCookie derives the token cookie's flags from the session cookie's, so
// the two travel under the same rules on any given origin.
func newCSRFCookie(session httpapi.SessionCookie) csrfCookie {
	if session.Secure() {
		return csrfCookie{name: "__Host-" + csrfCookieName, secure: true}
	}
	return csrfCookie{name: csrfCookieName, secure: false}
}

// token returns the token the request carries, or "".
func (c csrfCookie) token(r *http.Request) string {
	cookie, err := r.Cookie(c.name)
	if err != nil {
		return ""
	}
	return cookie.Value
}

// issue returns the request's token, minting and setting one when the browser
// has none. It is called before rendering any page that carries a form.
func (c csrfCookie) issue(w http.ResponseWriter, r *http.Request) (string, error) {
	if token := c.token(r); len(token) == csrfTokenLen {
		return token, nil
	}
	return c.rotate(w)
}

// rotate replaces the token with a fresh one.
//
// It runs on sign-in as well as after a rejected form: a token minted before
// the session began should not carry over into it.
func (c csrfCookie) rotate(w http.ResponseWriter) (string, error) {
	token, err := newCSRFToken()
	if err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     c.name,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   c.secure,
		SameSite: http.SameSiteLaxMode,
	})
	return token, nil
}

// verify reports whether the submitted form carries the cookie's token.
func (c csrfCookie) verify(r *http.Request) bool {
	cookie := c.token(r)
	submitted := r.PostFormValue(csrfField)
	if len(cookie) != csrfTokenLen || len(submitted) != csrfTokenLen {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookie), []byte(submitted)) == 1
}

// csrfTokenLen is the encoded length of a token, checked before the comparison
// so a length mismatch is never what the constant-time compare has to catch.
var csrfTokenLen = base64.RawURLEncoding.EncodedLen(csrfTokenBytes)

func newCSRFToken() (string, error) {
	raw := make([]byte, csrfTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("dashboard: read random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

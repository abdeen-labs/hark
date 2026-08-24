package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/abdeen-labs/hark/internal/auth"
)

// CredentialResolver resolves the credentials a request can present. Both
// methods answer [auth.ErrInvalidCredentials] for every kind of failure, so the
// middleware never has to decide which of them is worth distinguishing.
//
// *auth.Service satisfies it; tests supply their own.
type CredentialResolver interface {
	AuthenticateSession(ctx context.Context, token string) (*auth.Principal, error)
	AuthenticateAPIToken(ctx context.Context, secret string) (*auth.Principal, error)
}

// SessionCookie describes how a session travels in a browser.
//
// Over HTTPS the cookie takes the `__Host-` prefix, which a browser only
// accepts when the cookie is also Secure, Path=/ and carries no Domain — so the
// name itself is a promise that no sibling subdomain can plant a session on the
// dashboard. Over plain HTTP the prefix would be rejected outright, so it is
// dropped along with Secure; that combination only ever happens on localhost.
//
// It is exported because the embedded dashboard signs in through the same
// cookie: a second, subtly different definition of "the session cookie" is
// exactly the kind of duplication that ends with one of them missing a flag.
type SessionCookie struct {
	name   string
	secure bool
}

const (
	sessionCookieName       = "hark_session"
	sessionCookieHostPrefix = "__Host-"
)

// NewSessionCookie derives the cookie's name and flags from the public origin.
func NewSessionCookie(publicURL *url.URL) SessionCookie {
	if publicURL != nil && publicURL.Scheme == "https" {
		return SessionCookie{name: sessionCookieHostPrefix + sessionCookieName, secure: true}
	}
	return SessionCookie{name: sessionCookieName, secure: false}
}

// Name is the cookie's name on this origin.
func (c SessionCookie) Name() string { return c.name }

// Secure reports whether cookies on this origin carry the Secure attribute.
// The dashboard's CSRF cookie follows the session cookie's lead.
func (c SessionCookie) Secure() bool { return c.secure }

// Read returns the session token the request carries, or "".
func (c SessionCookie) Read(r *http.Request) string {
	cookie, err := r.Cookie(c.name)
	if err != nil {
		return ""
	}
	return cookie.Value
}

// Set issues the cookie for token, expiring alongside the session row.
func (c SessionCookie) Set(w http.ResponseWriter, token string, expiresAt time.Time, now time.Time) {
	maxAge := int(expiresAt.Sub(now).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{
		Name:     c.name,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   c.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// Clear expires the cookie. It is emitted whenever a presented cookie turns out
// to be unusable, so a browser sheds a dead session instead of sending it
// forever.
func (c SessionCookie) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     c.name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   c.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// authenticator resolves the credential on each request and attaches the
// principal.
type authenticator struct {
	resolver CredentialResolver
	cookie   SessionCookie
	// origin is the configured public origin, e.g. "https://hark.example.com".
	// It is kept for the refusal message; the judgment itself is crossOrigin's.
	origin string
	// crossOrigin decides whether a state-changing request made with the
	// ambient cookie came from this application. It is the standard library's
	// CSRF check: Sec-Fetch-Site where the browser sends it, the Origin header
	// against the request's own host for older browsers, and no headers at all
	// means no browser and nothing ambient to ride.
	crossOrigin *http.CrossOriginProtection
	now         func() time.Time
}

// Authenticate resolves a session cookie, a bearer session token, or a bearer
// API token, and puts the resulting principal on the request context.
//
// The two credential transports have different failure behavior:
//
//   - If an Authorization header is malformed or the
//     credential behind it is unknown, the request is refused with 401 rather
//     than quietly continuing as anonymous, because continuing would turn a
//     typo into a confusing 404 further down.
//   - A cookie is ambient. A stale one is treated as no credential at all and
//     cleared, so a browser holding a week-old session can still reach public
//     routes and can still sign in.
//
// Cookie-authenticated unsafe methods additionally have to come from the app's
// own origin, as judged by [http.CrossOriginProtection]. That is the CSRF
// gate: SameSite=Lax already blocks the common cases, and this closes the rest
// without a token round-trip. Bearer callers skip it — nothing is ambient
// about a header a client had to set.
func Authenticate(resolver CredentialResolver, publicURL *url.URL, now func() time.Time) Middleware {
	if now == nil {
		now = time.Now
	}
	a := &authenticator{
		resolver:    resolver,
		cookie:      NewSessionCookie(publicURL),
		origin:      originOf(publicURL),
		crossOrigin: http.NewCrossOriginProtection(),
		now:         now,
	}
	// The configured origin is trusted on top of the library's own same-host
	// check, so a deployment reached through a second hostname still accepts
	// its real public origin.
	if a.origin != "" {
		if err := a.crossOrigin.AddTrustedOrigin(a.origin); err != nil {
			panic("httpapi: the public URL is not a valid origin: " + err.Error())
		}
	}
	return a.middleware
}

func (a *authenticator) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if header := r.Header.Get("Authorization"); header != "" {
			principal, ok := a.fromHeader(w, r, header)
			if !ok {
				return
			}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), principal)))
			return
		}

		token := a.cookie.Read(r)
		if token == "" {
			next.ServeHTTP(w, r)
			return
		}

		// The CSRF gate runs before the lookup, so a forged cross-origin write
		// costs no database work and cannot slide a session's expiry forward.
		// Safe methods pass through inside Check.
		if err := a.crossOrigin.Check(r); err != nil {
			WriteError(w, r, http.StatusForbidden, CodeOriginNotAllowed,
				"A cookie-authenticated request must come from this application's own origin"+
					a.originSuffix()+"; present an Authorization header instead.")
			return
		}

		principal, err := a.resolver.AuthenticateSession(r.Context(), token)
		if err != nil {
			if !errors.Is(err, auth.ErrInvalidCredentials) {
				a.fail(w, r, err)
				return
			}
			a.cookie.Clear(w)
			next.ServeHTTP(w, r)
			return
		}

		if principal.Refreshed {
			a.cookie.Set(w, token, principal.Session.ExpiresAt, a.now())
		}
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), principal)))
	})
}

// fromHeader resolves an Authorization header, reporting whether the request
// may continue.
func (a *authenticator) fromHeader(w http.ResponseWriter, r *http.Request, header string) (*auth.Principal, bool) {
	scheme, secret, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || secret == "" {
		WriteError(w, r, http.StatusUnauthorized, CodeUnauthorized,
			"Present a credential as `Authorization: Bearer <token>`.")
		return nil, false
	}

	var (
		principal *auth.Principal
		err       error
	)
	switch {
	case strings.HasPrefix(secret, auth.SessionTokenPrefix):
		principal, err = a.resolver.AuthenticateSession(r.Context(), secret)
	case strings.HasPrefix(secret, auth.APITokenPrefix):
		principal, err = a.resolver.AuthenticateAPIToken(r.Context(), secret)
	default:
		err = auth.ErrInvalidCredentials
	}
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		WriteError(w, r, http.StatusUnauthorized, CodeUnauthorized,
			"That credential is not valid. It may have expired or been revoked.")
		return nil, false
	case err != nil:
		a.fail(w, r, err)
		return nil, false
	}
	return principal, true
}

// fail answers a credential lookup that broke rather than a credential that was
// wrong. Treating it as 401 would tell a caller their token is bad when the
// database is merely down.
func (a *authenticator) fail(w http.ResponseWriter, r *http.Request, err error) {
	LoggerFrom(r.Context()).ErrorContext(r.Context(), "resolving credentials failed", "error", err)
	WriteError(w, r, http.StatusServiceUnavailable, CodeUnavailable,
		"Credentials could not be checked right now.")
}

func (a *authenticator) originSuffix() string {
	if a.origin == "" {
		return ""
	}
	return " (" + a.origin + ")"
}

func originOf(u *url.URL) string {
	if u == nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// RequireAuth admits any authenticated caller, session or API token.
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth.PrincipalFrom(r.Context()) == nil {
			writeUnauthenticated(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireSession admits only the account owner signed in with a password.
//
// This is the boundary that keeps an API token from escalating itself: token
// management and device-grant approval sit behind it, so a leaked agent
// credential can act within its scopes but can never mint a second credential
// or widen its own.
func RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal := auth.PrincipalFrom(r.Context())
		switch {
		case principal == nil:
			writeUnauthenticated(w, r)
		case !principal.IsSession():
			WriteError(w, r, http.StatusForbidden, CodeSessionRequired,
				"This endpoint requires a signed-in session; an API token cannot manage credentials.")
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// RequireScopes admits callers granted every listed scope.
//
// Scopes constrain API tokens only. Owner sessions pass unconditionally.
func RequireScopes(scopes ...string) Middleware {
	required := strings.Join(scopes, ", ")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal := auth.PrincipalFrom(r.Context())
			switch {
			case principal == nil:
				writeUnauthenticated(w, r)
			case !principal.HasScopes(scopes...):
				WriteError(w, r, http.StatusForbidden, CodeInsufficientScope,
					"This endpoint requires the scopes: "+required+".")
			default:
				next.ServeHTTP(w, r)
			}
		})
	}
}

func writeUnauthenticated(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="hark"`)
	WriteError(w, r, http.StatusUnauthorized, CodeUnauthorized,
		"This endpoint requires authentication.")
}

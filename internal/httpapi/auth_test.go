package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
)

// stubResolver stands in for *auth.Service so the middleware can be exercised
// without a database. It records what it was asked so a test can assert that a
// credential was routed to the right lookup.
type stubResolver struct {
	session  *auth.Principal
	token    *auth.Principal
	err      error
	sawToken string
	kind     string
}

func (s *stubResolver) AuthenticateSession(_ context.Context, token string) (*auth.Principal, error) {
	s.sawToken, s.kind = token, "session"
	if s.err != nil {
		return nil, s.err
	}
	if s.session == nil {
		return nil, auth.ErrInvalidCredentials
	}
	return s.session, nil
}

func (s *stubResolver) AuthenticateAPIToken(_ context.Context, secret string) (*auth.Principal, error) {
	s.sawToken, s.kind = secret, "api_token"
	if s.err != nil {
		return nil, s.err
	}
	if s.token == nil {
		return nil, auth.ErrInvalidCredentials
	}
	return s.token, nil
}

var testUser = db.User{ID: "0198f3a1-2b4c-7d8e-9f01-23456789abcd", Username: "admin"}

func sessionPrincipal(expiresAt time.Time, refreshed bool) *auth.Principal {
	return &auth.Principal{
		Kind:      auth.KindSession,
		User:      testUser,
		Session:   &db.Session{ID: "session-1", UserID: testUser.ID, ExpiresAt: expiresAt},
		Refreshed: refreshed,
	}
}

func tokenPrincipal(scopes ...string) *auth.Principal {
	return &auth.Principal{
		Kind:     auth.KindAPIToken,
		User:     testUser,
		APIToken: &db.APIToken{ID: "token-1", UserID: testUser.ID, Scopes: scopes},
	}
}

const testOrigin = "https://hark.example.com"

func testPublicURL() *url.URL { return &url.URL{Scheme: "https", Host: "hark.example.com"} }

// authHarness wires the middleware in front of a probe handler that records the
// principal it saw.
type authHarness struct {
	handler  http.Handler
	resolver *stubResolver
	seen     *auth.Principal
	called   bool
}

func newAuthHarness(t *testing.T, resolver *stubResolver, publicURL *url.URL, guard Middleware) *authHarness {
	t.Helper()
	h := &authHarness{resolver: resolver}

	probe := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.called = true
		h.seen = auth.PrincipalFrom(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})
	var inner http.Handler = probe
	if guard != nil {
		inner = guard(probe)
	}

	rt := newRouter()
	rt.handle(http.MethodGet, "/probe", inner)
	rt.handle(http.MethodPost, "/probe", inner)

	h.handler = Chain(rt.handler(), RequestID, Authenticate(resolver, publicURL, nil))
	return h
}

func (h *authHarness) send(t *testing.T, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

func TestBearerSessionTokenAuthenticates(t *testing.T) {
	resolver := &stubResolver{session: sessionPrincipal(time.Now().Add(time.Hour), false)}
	h := newAuthHarness(t, resolver, testPublicURL(), nil)

	secret := auth.NewSessionToken()
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer "+secret)

	if rec := h.send(t, req); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body)
	}
	if resolver.kind != "session" {
		t.Errorf("credential was routed to the %q lookup, want session", resolver.kind)
	}
	if resolver.sawToken != secret {
		t.Errorf("resolver saw %q, want the secret verbatim", resolver.sawToken)
	}
	if !h.seen.IsSession() {
		t.Errorf("principal = %+v, want a session", h.seen)
	}
}

func TestBearerAPITokenAuthenticates(t *testing.T) {
	resolver := &stubResolver{token: tokenPrincipal("notifications:send")}
	h := newAuthHarness(t, resolver, testPublicURL(), nil)

	secret := auth.NewAPIToken()
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "bearer "+secret) // the scheme is case-insensitive

	if rec := h.send(t, req); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body)
	}
	if resolver.kind != "api_token" {
		t.Errorf("credential was routed to the %q lookup, want api_token", resolver.kind)
	}
	if !h.seen.IsAPIToken() || !h.seen.HasScope("notifications:send") {
		t.Errorf("principal = %+v, want an api token carrying notifications:send", h.seen)
	}
}

// TestBadAuthorizationHeaderIs401 covers the different behavior from cookies:
// a header is an explicit act, so a bad one is refused rather than downgraded
// to anonymous.
func TestBadAuthorizationHeaderIs401(t *testing.T) {
	for name, header := range map[string]string{
		"unknown scheme":   "Basic " + auth.NewAPIToken(),
		"no scheme":        auth.NewAPIToken(),
		"empty credential": "Bearer ",
		"two spaces":       "Bearer  " + auth.NewAPIToken(),
		// A credential whose marker names no kind is rejected before any
		// lookup: the prefix is what selects the table.
		"unknown prefix": "Bearer sk_live_something_else_entirely",
	} {
		t.Run(name, func(t *testing.T) {
			resolver := &stubResolver{token: tokenPrincipal(), session: sessionPrincipal(time.Now().Add(time.Hour), false)}
			h := newAuthHarness(t, resolver, testPublicURL(), nil)

			req := httptest.NewRequest(http.MethodGet, "/probe", nil)
			req.Header.Set("Authorization", header)
			rec := h.send(t, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			if h.called {
				t.Error("the handler ran despite an unusable credential")
			}
			if got := decodeError(t, rec); got.Error.Code != CodeUnauthorized {
				t.Errorf("code = %q, want %q", got.Error.Code, CodeUnauthorized)
			}
		})
	}
}

// TestResolverFailureIs503 separates "your credential is wrong" from "the
// database is down". Answering 401 for the latter would tell a client to
// re-authenticate when nothing is wrong with its token.
func TestResolverFailureIs503(t *testing.T) {
	resolver := &stubResolver{err: errors.New("connection refused")}
	h := newAuthHarness(t, resolver, testPublicURL(), nil)

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer "+auth.NewAPIToken())
	rec := h.send(t, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("the driver error leaked to the client: %s", rec.Body)
	}
}

func TestSessionCookieAuthenticates(t *testing.T) {
	resolver := &stubResolver{session: sessionPrincipal(time.Now().Add(time.Hour), false)}
	h := newAuthHarness(t, resolver, testPublicURL(), nil)

	secret := auth.NewSessionToken()
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.AddCookie(&http.Cookie{Name: "__Host-hark_session", Value: secret})

	if rec := h.send(t, req); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body)
	}
	if !h.seen.IsSession() {
		t.Errorf("principal = %+v, want a session", h.seen)
	}
}

// TestStaleCookieIsClearedAndIgnored is the other half of the asymmetry: a
// browser holding a dead session must still be able to reach public routes and
// sign in again, and must be told to drop the cookie.
func TestStaleCookieIsClearedAndIgnored(t *testing.T) {
	h := newAuthHarness(t, &stubResolver{}, testPublicURL(), nil)

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.AddCookie(&http.Cookie{Name: "__Host-hark_session", Value: auth.NewSessionToken()})
	rec := h.send(t, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want the request to continue as anonymous", rec.Code)
	}
	if h.seen != nil {
		t.Errorf("principal = %+v, want anonymous", h.seen)
	}
	cookie := findCookie(t, rec, "__Host-hark_session")
	if cookie.MaxAge >= 0 {
		t.Errorf("stale cookie was not expired: MaxAge = %d", cookie.MaxAge)
	}
}

func TestRefreshedSessionReissuesTheCookie(t *testing.T) {
	expiresAt := time.Now().Add(720 * time.Hour).Truncate(time.Second)
	resolver := &stubResolver{session: sessionPrincipal(expiresAt, true)}
	h := newAuthHarness(t, resolver, testPublicURL(), nil)

	secret := auth.NewSessionToken()
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.AddCookie(&http.Cookie{Name: "__Host-hark_session", Value: secret})
	rec := h.send(t, req)

	cookie := findCookie(t, rec, "__Host-hark_session")
	if cookie.Value != secret {
		t.Errorf("the re-issued cookie changed the token: %q", cookie.Value)
	}
	if cookie.MaxAge < int(700*time.Hour.Seconds()) {
		t.Errorf("re-issued cookie MaxAge = %d, want roughly the remaining lifetime", cookie.MaxAge)
	}
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" {
		t.Errorf("re-issued cookie lost its attributes: %+v", cookie)
	}
}

// TestSessionCookieNaming checks the `__Host-` prefix, which a browser only
// honours for a Secure, path-scoped, domain-less cookie — so the name is itself
// a promise no sibling subdomain can plant a session.
func TestSessionCookieNaming(t *testing.T) {
	https := NewSessionCookie(&url.URL{Scheme: "https", Host: "hark.example.com"})
	if https.name != "__Host-hark_session" || !https.secure {
		t.Errorf("https cookie = %+v, want a __Host- prefixed Secure cookie", https)
	}

	plain := NewSessionCookie(&url.URL{Scheme: "http", Host: "localhost:8080"})
	if plain.name != "hark_session" || plain.secure {
		t.Errorf("http cookie = %+v, want an unprefixed insecure cookie", plain)
	}
}

// TestCookieWritesRequireTheAppOrigin is the CSRF gate, judged by the standard
// library's http.CrossOriginProtection. SameSite=Lax already stops most of it;
// this closes cross-origin fetches that carry the cookie. Modern browsers are
// judged by Sec-Fetch-Site — which marks a scheme downgrade cross-site — and
// older ones by their Origin header against the request's host and the
// configured public origin.
func TestCookieWritesRequireTheAppOrigin(t *testing.T) {
	tests := map[string]struct {
		origin       string
		secFetchSite string
		want         int
	}{
		"same origin":      {testOrigin, "", http.StatusNoContent},
		"no origin":        {"", "", http.StatusNoContent},
		"foreign origin":   {"https://evil.example", "", http.StatusForbidden},
		"sneaky subdomain": {"https://hark.example.com.evil.example", "", http.StatusForbidden},
		"scheme downgrade": {"http://hark.example.com", "", http.StatusForbidden},

		// Fetch metadata is judged where the browser sends it — and a
		// cross-site marking is refused even with no Origin at all, which the
		// old hand-rolled gate would have waved through.
		"cross-site metadata, no origin":      {"", "cross-site", http.StatusForbidden},
		"cross-site metadata, foreign origin": {"https://evil.example", "cross-site", http.StatusForbidden},
		"same-origin metadata":                {"", "same-origin", http.StatusNoContent},
		"direct navigation (none)":            {"", "none", http.StatusNoContent},

		// The configured public origin is trusted outright. A browser cannot
		// forge Origin, so this combination only ever describes our own page,
		// however the fetch metadata got mangled on the way.
		"cross-site metadata, trusted origin": {testOrigin, "cross-site", http.StatusNoContent},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			resolver := &stubResolver{session: sessionPrincipal(time.Now().Add(time.Hour), false)}
			h := newAuthHarness(t, resolver, testPublicURL(), nil)

			req := httptest.NewRequest(http.MethodPost, "/probe", nil)
			req.AddCookie(&http.Cookie{Name: "__Host-hark_session", Value: auth.NewSessionToken()})
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.secFetchSite != "" {
				req.Header.Set("Sec-Fetch-Site", tc.secFetchSite)
			}
			rec := h.send(t, req)

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
			if tc.want == http.StatusForbidden {
				if got := decodeError(t, rec); got.Error.Code != CodeOriginNotAllowed {
					t.Errorf("code = %q, want %q", got.Error.Code, CodeOriginNotAllowed)
				}
			}
		})
	}
}

// TestCookieReadsSkipTheOriginGate keeps the gate on state-changing methods
// only: a cross-origin GET cannot be used to change anything.
func TestCookieReadsSkipTheOriginGate(t *testing.T) {
	resolver := &stubResolver{session: sessionPrincipal(time.Now().Add(time.Hour), false)}
	h := newAuthHarness(t, resolver, testPublicURL(), nil)

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.AddCookie(&http.Cookie{Name: "__Host-hark_session", Value: auth.NewSessionToken()})
	req.Header.Set("Origin", "https://evil.example")

	if rec := h.send(t, req); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
}

// TestBearerWritesSkipTheOriginGate matters for the CLI and the native app,
// which set an Origin the server has never heard of but carry no ambient
// credential for an attacker to ride.
func TestBearerWritesSkipTheOriginGate(t *testing.T) {
	resolver := &stubResolver{token: tokenPrincipal()}
	h := newAuthHarness(t, resolver, testPublicURL(), nil)

	req := httptest.NewRequest(http.MethodPost, "/probe", nil)
	req.Header.Set("Authorization", "Bearer "+auth.NewAPIToken())
	req.Header.Set("Origin", "hark://")

	if rec := h.send(t, req); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body)
	}
}

func TestRequireSessionRefusesAPITokens(t *testing.T) {
	resolver := &stubResolver{token: tokenPrincipal("notifications:send")}
	h := newAuthHarness(t, resolver, testPublicURL(), RequireSession)

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer "+auth.NewAPIToken())
	rec := h.send(t, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if got := decodeError(t, rec); got.Error.Code != CodeSessionRequired {
		t.Errorf("code = %q, want %q", got.Error.Code, CodeSessionRequired)
	}
	if h.called {
		t.Error("an API token reached a session-only handler")
	}
}

func TestRequireScopes(t *testing.T) {
	guard := RequireScopes("interactions:create", "notifications:send")

	t.Run("granted", func(t *testing.T) {
		resolver := &stubResolver{token: tokenPrincipal("interactions:create", "notifications:send")}
		h := newAuthHarness(t, resolver, testPublicURL(), guard)

		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		req.Header.Set("Authorization", "Bearer "+auth.NewAPIToken())
		if rec := h.send(t, req); rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body)
		}
	})

	t.Run("partially granted", func(t *testing.T) {
		resolver := &stubResolver{token: tokenPrincipal("notifications:send")}
		h := newAuthHarness(t, resolver, testPublicURL(), guard)

		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		req.Header.Set("Authorization", "Bearer "+auth.NewAPIToken())
		rec := h.send(t, req)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
		got := decodeError(t, rec)
		if got.Error.Code != CodeInsufficientScope {
			t.Errorf("code = %q, want %q", got.Error.Code, CodeInsufficientScope)
		}
		// The message has to name what was needed, or a CLI cannot tell its
		// user which scope to add.
		if !strings.Contains(got.Error.Message, "interactions:create") {
			t.Errorf("message does not name the missing scope: %q", got.Error.Message)
		}
	})

	// A session is the account owner in person: scopes constrain tokens only.
	t.Run("session passes unconditionally", func(t *testing.T) {
		resolver := &stubResolver{session: sessionPrincipal(time.Now().Add(time.Hour), false)}
		h := newAuthHarness(t, resolver, testPublicURL(), guard)

		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		req.Header.Set("Authorization", "Bearer "+auth.NewSessionToken())
		if rec := h.send(t, req); rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body)
		}
	})
}

func findCookie(t *testing.T, rec *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no %q cookie in the response: %v", name, rec.Header().Values("Set-Cookie"))
	return nil
}

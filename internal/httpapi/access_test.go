package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/push"
)

// fakeSender stands in for the APNs transport.
//
// It records delivery requests and returns configured test responses, allowing
// delivery paths to run without APNs credentials or network access.
type fakeSender struct {
	mu sync.Mutex
	// accept decides whether APNs "took" each message.
	accept     bool
	alerts     []push.Alert
	activities []push.ActivityEvent
	// stale is reported as a permanently invalid token for these device tokens.
	stale map[string]bool
}

func newFakeSender() *fakeSender { return &fakeSender{accept: true, stale: map[string]bool{}} }

func (f *fakeSender) SendAlerts(_ context.Context, alerts []push.Alert) push.AlertResult {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.alerts = append(f.alerts, alerts...)
	var out push.AlertResult
	for _, a := range alerts {
		switch {
		case f.stale[a.Target.Token]:
			out.Failures = append(out.Failures, "APNs 410 Unregistered")
			out.StaleTokens = append(out.StaleTokens, a.Target.Token)
		case f.accept:
			out.Accepted++
		default:
			out.Failures = append(out.Failures, "APNs 400 BadDeviceToken")
		}
	}
	return out
}

func (f *fakeSender) SendActivity(_ context.Context, event push.ActivityEvent) push.ActivityResult {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.activities = append(f.activities, event)
	if f.accept {
		return push.ActivityResult{Accepted: true, APNsStatus: ptr(200)}
	}
	reason := "BadDeviceToken"
	return push.ActivityResult{APNsStatus: ptr(400), Reason: &reason}
}

func (f *fakeSender) lastAlert(t *testing.T) push.Alert {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.alerts) == 0 {
		t.Fatal("no alert was sent")
	}
	return f.alerts[len(f.alerts)-1]
}

func (f *fakeSender) lastActivity(t *testing.T) push.ActivityEvent {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.activities) == 0 {
		t.Fatal("no Live Activity push was sent")
	}
	return f.activities[len(f.activities)-1]
}

func (f *fakeSender) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.alerts, f.activities = nil, nil
}

// TestDeliveryRoutesAreClosedByDefault walks the whole surface without a
// credential. It is the cheapest guard against a route being added to the table
// without the middleware that protects it.
func TestDeliveryRoutesAreClosedByDefault(t *testing.T) {
	h := newTestServer(t, stubPinger{})

	closed := []struct{ method, path string }{
		{http.MethodGet, "/services"},
		{http.MethodPost, "/services"},
		{http.MethodGet, "/services/svc"},
		{http.MethodPatch, "/services/svc"},
		{http.MethodDelete, "/services/svc"},
		{http.MethodPost, "/services/svc/webhook-token"},
		{http.MethodGet, "/devices"},
		{http.MethodPost, "/devices"},
		{http.MethodGet, "/devices/dev"},
		{http.MethodDelete, "/devices/dev"},
		{http.MethodPut, "/devices/dev/push-to-start-token"},
		{http.MethodPut, "/devices/dev/activity-update-token"},
		{http.MethodGet, "/events"},
		{http.MethodDelete, "/events/evt"},
		{http.MethodGet, "/history"},
		{http.MethodGet, "/history/sources"},
		{http.MethodDelete, "/history"},
		{http.MethodDelete, "/history/event:evt"},
		{http.MethodPost, "/notifications"},
		{http.MethodGet, "/safety-sources"},
		{http.MethodPost, "/safety-sources"},
		{http.MethodGet, "/safety-sources/src"},
		{http.MethodPatch, "/safety-sources/src"},
		{http.MethodDelete, "/safety-sources/src"},
		{http.MethodPost, "/safety-sources/src/test"},
		{http.MethodGet, "/safety-settings"},
		{http.MethodPatch, "/safety-settings"},
		{http.MethodPost, "/safety-events"},
		{http.MethodPost, "/interactions"},
		{http.MethodGet, "/interactions"},
		{http.MethodGet, "/interactions/int"},
		{http.MethodPost, "/interactions/int/cancel"},
		{http.MethodGet, "/activities"},
		{http.MethodPost, "/activities"},
		{http.MethodGet, "/activities/act"},
		{http.MethodPatch, "/activities/act"},
		{http.MethodPost, "/activities/act/end"},
	}
	for _, route := range closed {
		rec := do(t, h, route.method, route.path, strings.NewReader("{}"))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: status = %d, want 401: %s", route.method, route.path, rec.Code, rec.Body)
			continue
		}
		if got := decodeError(t, rec); got.Error.Code != CodeUnauthorized {
			t.Errorf("%s %s: code = %q, want %q", route.method, route.path, got.Error.Code, CodeUnauthorized)
		}
	}
}

// TestCredentialRoutesTakeTheirOwnCredential documents the two routes that carry
// their credential in the request rather than in a header: the webhook token in
// the path, and the capability a start push handed to the widget. Neither may be
// turned away by an auth middleware, and both must answer 404 rather than 401 —
// a caller with a bad credential learns nothing about what exists.
func TestCredentialRoutesTakeTheirOwnCredential(t *testing.T) {
	h := newTestServer(t, stubPinger{})

	// A malformed webhook token never reaches the database, so this exercises
	// the whole route without a store.
	rec := do(t, h, http.MethodPost, "/hooks/not-a-token", strings.NewReader(`{"body":"hi"}`))
	if rec.Code != http.StatusNotFound {
		t.Errorf("POST /hooks/{bad}: status = %d, want 404: %s", rec.Code, rec.Body)
	}
	if got := decodeError(t, rec); got.Error.Code != CodeNotFound {
		t.Errorf("code = %q, want %q", got.Error.Code, CodeNotFound)
	}

	// The response path is reachable without a session; the body decides.
	rec = do(t, h, http.MethodPost, "/interactions/int/response", strings.NewReader(`{}`))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("POST /interactions/{id}/response: status = %d, want 422: %s", rec.Code, rec.Body)
	}
}

// TestRequireAPITokenNamesWhyItRefused pins the distinction between the two
// kinds of credential. A session is a person and cannot be the requester of a
// delivery; a token can. Both are legitimate callers, so the refusal has to say
// which one the route wants rather than answering a blanket 403.
func TestRequireAPITokenNamesWhyItRefused(t *testing.T) {
	reached := false
	handler := RequireAPIToken(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))

	session := &auth.Principal{Kind: auth.KindSession, User: db.User{ID: "u"}, Session: &db.Session{ID: "s"}}
	rec := serveWithPrincipal(handler, session)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("session: status = %d, want 403: %s", rec.Code, rec.Body)
	}
	if got := decodeErrorBody(t, rec); got.Error.Code != CodeAPITokenRequired {
		t.Errorf("session: code = %q, want %q", got.Error.Code, CodeAPITokenRequired)
	}

	token := &auth.Principal{Kind: auth.KindAPIToken, User: db.User{ID: "u"}, APIToken: &db.APIToken{ID: "t"}}
	if rec := serveWithPrincipal(handler, token); rec.Code != http.StatusOK || !reached {
		t.Errorf("token: status = %d, reached = %v; want 200, true", rec.Code, reached)
	}

	if rec := serveWithPrincipal(handler, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: status = %d, want 401", rec.Code)
	}
}

// TestCreateServiceRequiresSession pins the credential boundary on service
// creation. The 201 carries the plaintext webhook URL — a second credential
// that can send, ask, and manage Live Activities. Only an owner session may
// create one.
func TestCreateServiceRequiresSession(t *testing.T) {
	h := newTestServer(t, stubPinger{})

	// The request presents no credential of its own; the principal is placed on
	// the context directly, which the resolver middleware leaves untouched. That
	// exercises the registered route and the real wrapper around the handler.
	post := func(t *testing.T, p *auth.Principal, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/services", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if p != nil {
			req = req.WithContext(auth.WithPrincipal(req.Context(), p))
		}
		return send(t, h, req)
	}

	tokenWith := func(scopes ...string) *auth.Principal {
		return &auth.Principal{
			Kind:     auth.KindAPIToken,
			User:     db.User{ID: "u"},
			APIToken: &db.APIToken{ID: "t", Scopes: scopes},
		}
	}

	refused := map[string]*auth.Principal{
		"token with services:write": tokenWith(db.ScopeServicesWrite),
		"token with every scope":    tokenWith(db.Scopes...),
	}
	for name, principal := range refused {
		t.Run(name, func(t *testing.T) {
			rec := post(t, principal, `{"title":"Deploy bot"}`)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body)
			}
			if got := decodeError(t, rec); got.Error.Code != CodeSessionRequired {
				t.Errorf("code = %q, want %q", got.Error.Code, CodeSessionRequired)
			}
		})
	}

	t.Run("session reaches the handler", func(t *testing.T) {
		session := &auth.Principal{Kind: auth.KindSession, User: db.User{ID: "u"}, Session: &db.Session{ID: "s"}}
		// An empty object decodes but fails the handler's own validation, so a
		// 422 can only mean the middleware admitted the session and the handler
		// ran — proven without the database a valid create would need.
		rec := post(t, session, `{}`)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body)
		}
		if got := decodeError(t, rec); got.Error.Code != CodeValidation {
			t.Errorf("code = %q, want %q", got.Error.Code, CodeValidation)
		}
	})

	// Anonymous stays 401; TestDeliveryRoutesAreClosedByDefault walks the same
	// route, and this pins it against that test ever dropping the entry.
	t.Run("anonymous", func(t *testing.T) {
		rec := post(t, nil, `{"title":"Deploy bot"}`)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401: %s", rec.Code, rec.Body)
		}
		if got := decodeError(t, rec); got.Error.Code != CodeUnauthorized {
			t.Errorf("code = %q, want %q", got.Error.Code, CodeUnauthorized)
		}
	})
}

// TestScopesConstrainTokensOnly pins the other half: a token carries exactly
// what it was granted, and a session — the account owner in person — satisfies
// every scope check without carrying any.
func TestScopesConstrainTokensOnly(t *testing.T) {
	handler := RequireScopes(db.ScopeActivitiesWrite)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	narrow := &auth.Principal{
		Kind:     auth.KindAPIToken,
		User:     db.User{ID: "u"},
		APIToken: &db.APIToken{ID: "t", Scopes: []string{db.ScopeActivitiesRead}},
	}
	rec := serveWithPrincipal(handler, narrow)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("narrow token: status = %d, want 403", rec.Code)
	}
	got := decodeErrorBody(t, rec)
	if got.Error.Code != CodeInsufficientScope {
		t.Errorf("code = %q, want %q", got.Error.Code, CodeInsufficientScope)
	}

	session := &auth.Principal{Kind: auth.KindSession, User: db.User{ID: "u"}, Session: &db.Session{ID: "s"}}
	if rec := serveWithPrincipal(handler, session); rec.Code != http.StatusNoContent {
		t.Errorf("session: status = %d, want 204", rec.Code)
	}
}

func serveWithPrincipal(h http.Handler, p *auth.Principal) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/anything", nil)
	if p != nil {
		req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestWebhookTokensAreNeverLogged is the reason redactPath exists: the webhook
// credential travels in the URL, so the access log could otherwise
// otherwise be written down in the clear, forever.
func TestWebhookTokensAreNeverLogged(t *testing.T) {
	secretToken := "harkhook_V3kQ2mZ8bR1tXyLp0aNfCd7eJhSu4WgO7xY2bWv"
	tests := map[string]string{
		"/hooks/" + secretToken:                       "/hooks/{token}",
		"/hooks/" + secretToken + "/events/evt":       "/hooks/{token}/events/evt",
		"/hooks/" + secretToken + "/activities/build": "/hooks/{token}/activities/build",
		"/services": "/services",
		"/healthz":  "/healthz",
	}
	for in, want := range tests {
		if got := redactPath(in); got != want {
			t.Errorf("redactPath(%q) = %q, want %q", in, got, want)
		}
		if strings.Contains(redactPath(in), secretToken) {
			t.Errorf("redactPath(%q) leaked the token", in)
		}
	}
}

// TestPaginationRejectsForeignCursors covers the shared list contract. A cursor
// this API did not mint is a validation failure rather than an empty page:
// silently starting from the top would look like the list had been emptied.
func TestPaginationRejectsForeignCursors(t *testing.T) {
	tests := map[string]struct {
		query string
		field string
	}{
		"bad cursor":      {"?cursor=not-a-cursor", "cursor"},
		"cursor from now": {"?cursor=" + strings.Repeat("A", 200), "cursor"},
		"zero limit":      {"?limit=0", "limit"},
		"negative limit":  {"?limit=-3", "limit"},
		"unparsed limit":  {"?limit=many", "limit"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/events"+tc.query, nil)

			s := &server{}
			if _, ok := s.parseList(rec, req); ok {
				t.Fatalf("%s was accepted", tc.query)
			}
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422", rec.Code)
			}
			got := decodeErrorBody(t, rec)
			if got.Error.Code != CodeValidation {
				t.Errorf("code = %q, want %q", got.Error.Code, CodeValidation)
			}
			if len(got.Error.Fields) != 1 || got.Error.Fields[0].Field != tc.field {
				t.Errorf("fields = %+v, want one entry naming %q", got.Error.Fields, tc.field)
			}
		})
	}

	// A limit past the ceiling is clamped rather than refused: asking for more
	// than the server will give is not a mistake, it is optimism.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/events?limit=5000", nil)
	s := &server{}
	query, ok := s.parseList(rec, req)
	if !ok || query.Limit != db.MaxPageSize {
		t.Errorf("limit = %d, ok = %v; want %d, true", query.Limit, ok, db.MaxPageSize)
	}
}

// TestURLValidationRefusesUnreachableHosts pins the rule that keeps a
// notification from turning the phone, or the server, into a client of the
// caller's own network. The classification itself lives in netpolicy — the
// callback worker applies the same one at dial time — and this table is what
// proves the validator still delegates to it.
func TestURLValidationRefusesUnreachableHosts(t *testing.T) {
	accepted := []string{
		"https://example.com/a.png",
		"https://cdn.example.co.uk:8443/a.png",
		"https://8.8.8.8/a.png",
		"https://[2606:4700:4700::1111]/a.png",
	}
	for _, raw := range accepted {
		var v validator
		if got := v.httpsURL("image_url", &raw); got == nil || !v.ok() {
			t.Errorf("httpsURL(%q) was refused: %+v", raw, v.fields)
		}
	}

	refused := []string{
		"http://example.com/a.png",
		"https://localhost/a.png",
		"https://printer.local/a.png",
		"https://127.0.0.1/a.png",
		"https://10.1.2.3/a.png",
		"https://192.168.0.1/a.png",
		"https://169.254.169.254/latest/meta-data",
		"https://100.64.0.1/a.png",
		"https://[::1]/a.png",
		"https://0.0.0.0/a.png",
		"https://224.0.0.1/a.png",
		"https://[fe80::1]/a.png",
		"https://[fd00::1]/a.png",
		"https://[ff02::1]/a.png",
		"https://[::ffff:192.168.0.1]/a.png",
		"ftp://example.com/a.png",
	}
	for _, raw := range refused {
		var v validator
		if got := v.httpsURL("image_url", &raw); got != nil || v.ok() {
			t.Errorf("httpsURL(%q) was accepted", raw)
		}
	}

	// A tap destination is looser: any app can be deep-linked into, but nothing
	// that executes or embeds content on tap.
	for _, raw := range []string{"https://example.com", "http://example.com", "myapp://open/thing"} {
		var v validator
		if got := v.linkURL("url", &raw); got == nil || !v.ok() {
			t.Errorf("linkURL(%q) was refused: %+v", raw, v.fields)
		}
	}
	for _, raw := range []string{"javascript:alert(1)", "data:text/html,<b>", "file:///etc/passwd", "not a url"} {
		var v validator
		if got := v.linkURL("url", &raw); got != nil || v.ok() {
			t.Errorf("linkURL(%q) was accepted", raw)
		}
	}
}

// decodeErrorBody reads the error envelope off a raw recorder.
func decodeErrorBody(t *testing.T, rec *httptest.ResponseRecorder) ErrorResponse {
	t.Helper()
	return decodeError(t, rec)
}

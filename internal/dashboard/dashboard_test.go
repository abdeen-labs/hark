package dashboard

import (
	"context"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/secret"
)

// fakeAuth stands in for *auth.Service. Every test in this file exercises the
// routing, the session gate, CSRF and the templates — none of which reach the
// database; the store below is not queried.
type fakeAuth struct {
	now       time.Time
	loginErr  error
	loggedOut []string

	// Device authorization request, configured result, and recorded decision.
	grant     *db.DeviceAuthorization
	grantErr  error
	decideErr error
	approved  []string
	denied    []string
}

func (f *fakeAuth) Login(context.Context, string, string) (*auth.Principal, string, error) {
	if f.loginErr != nil {
		return nil, "", f.loginErr
	}
	return &auth.Principal{
		Kind: auth.KindSession,
		User: db.User{ID: "user-1", Username: "admin"},
		Session: &db.Session{
			ID:        "session-1",
			ExpiresAt: f.now.Add(24 * time.Hour),
		},
	}, "hark_sk_test", nil
}

func (f *fakeAuth) Logout(_ context.Context, sessionID string) error {
	f.loggedOut = append(f.loggedOut, sessionID)
	return nil
}

func (f *fakeAuth) ListAPITokens(context.Context, string) ([]db.APIToken, error) { return nil, nil }

func (f *fakeAuth) CreateAPIToken(context.Context, string, auth.CreateAPITokenParams) (*db.APIToken, string, error) {
	return nil, "", nil
}

func (f *fakeAuth) RevokeAPIToken(context.Context, string, string) error { return nil }

func (f *fakeAuth) DeviceGrantByUserCode(_ context.Context, code string) (*db.DeviceAuthorization, error) {
	if f.grantErr != nil {
		return nil, f.grantErr
	}
	if f.grant == nil || f.grant.UserCode != code {
		return nil, auth.ErrNotFound
	}
	return f.grant, nil
}

func (f *fakeAuth) ApproveDeviceGrant(_ context.Context, code, userID string) (*db.DeviceAuthorization, error) {
	if f.decideErr != nil {
		return nil, f.decideErr
	}
	f.approved = append(f.approved, code+" by "+userID)
	return f.grant, nil
}

func (f *fakeAuth) DenyDeviceGrant(_ context.Context, code string) (*db.DeviceAuthorization, error) {
	if f.decideErr != nil {
		return nil, f.decideErr
	}
	f.denied = append(f.denied, code)
	return f.grant, nil
}

func (f *fakeAuth) Now() time.Time { return f.now }

func newTestDashboard(t *testing.T) (*Dashboard, *fakeAuth) {
	t.Helper()
	service := &fakeAuth{now: time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)}
	d := New(Options{
		Auth:                  service,
		Store:                 db.New(nil),
		Secrets:               testKeeper(),
		PublicURL:             &url.URL{Scheme: "https", Host: "hark.example.com"},
		TrustedClientIPHeader: "X-Real-Ip",
		Version:               "test",
	})
	return d, service
}

func testKeeper() *secret.Keeper {
	return secret.NewKeeper([]byte("test-root-key-long-enough-for-real"))
}

// signedIn returns a request carrying the account owner's session, as
// internal/httpapi's authenticator would have left it.
func signedIn(method, target string, body string) *http.Request {
	req := request(method, target, body)
	return req.WithContext(auth.WithPrincipal(req.Context(), &auth.Principal{
		Kind:    auth.KindSession,
		User:    db.User{ID: "user-1", Username: "admin"},
		Session: &db.Session{ID: "session-1"},
	}))
}

func request(method, target, body string) *http.Request {
	if body == "" {
		return httptest.NewRequest(method, target, nil)
	}
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func send(d *Dashboard, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	d.ServeHTTP(rec, req)
	return rec
}

// withCSRF gives the request a token cookie and the matching form field, which
// is what a browser that loaded the page first would send.
func withCSRF(t *testing.T, d *Dashboard, req *http.Request, form string) *http.Request {
	t.Helper()
	token, err := newCSRFToken()
	if err != nil {
		t.Fatalf("mint a CSRF token: %v", err)
	}

	values, err := url.ParseQuery(form)
	if err != nil {
		t.Fatalf("parse the form fixture %q: %v", form, err)
	}
	values.Set(csrfField, token)

	next := request(req.Method, req.URL.String(), values.Encode())
	next.AddCookie(&http.Cookie{Name: d.csrf.name, Value: token})
	return next.WithContext(req.Context())
}

func TestRootRedirectsToTheDashboard(t *testing.T) {
	d, _ := newTestDashboard(t)

	rec := send(d, request(http.MethodGet, "/", ""))
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	if got := rec.Header().Get("Location"); got != pathHome {
		t.Errorf("Location = %q, want %q", got, pathHome)
	}
}

// TestSignedOutPagesRedirectToSignIn is the guard against a page being added
// without [Dashboard.page] around it.
func TestSignedOutPagesRedirectToSignIn(t *testing.T) {
	d, _ := newTestDashboard(t)

	for _, path := range []string{
		pathHome, pathHistory, pathLiveOverview,
		pathServices, pathServices + "/0198f3a1-2b4c-7d8e-9f01-23456789abcd",
		pathSafety, pathDevices, pathTokens, pathTest, pathAuthorize,
	} {
		rec := send(d, request(http.MethodGet, path, ""))
		if rec.Code != http.StatusSeeOther {
			t.Errorf("GET %s: status = %d, want %d", path, rec.Code, http.StatusSeeOther)
			continue
		}
		if location := rec.Header().Get("Location"); !strings.HasPrefix(location, pathLogin) {
			t.Errorf("GET %s: Location = %q, want the sign-in page", path, location)
		}
	}
}

// TestHistoryRejectsJunkInput covers the two query parameters the archive
// takes. Both are only ever written by this page's own links, so anything else
// is answered before the store is reached — which is also what lets this test
// run without one.
func TestHistoryRejectsJunkInput(t *testing.T) {
	d, _ := newTestDashboard(t)

	for _, target := range []string{
		pathHistory + "?kind=everything",
		pathHistory + "?after=not-a-cursor",
	} {
		rec := send(d, signedIn(http.MethodGet, target, ""))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: status = %d, want 404", target, rec.Code)
		}
	}
}

func TestHistoryURLSpellsThePage(t *testing.T) {
	after := db.Cursor{Time: time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC), ID: "event:1"}
	tests := map[string]string{
		historyURL(db.FeedFilterAll, db.Cursor{}):          pathHistory,
		historyURL(db.FeedFilterNotification, db.Cursor{}): pathHistory + "?kind=notification",
		historyURL(db.FeedFilterAll, after):                pathHistory + "?after=" + after.String(),
	}
	for got, want := range tests {
		if got != want {
			t.Errorf("historyURL = %q, want %q", got, want)
		}
	}
}

// TestAnAPITokenIsNotASession pins the boundary the API draws with
// RequireSession: a credential minted for an agent cannot use the surface that
// mints credentials.
func TestAnAPITokenIsNotASession(t *testing.T) {
	d, _ := newTestDashboard(t)

	req := request(http.MethodGet, pathTokens, "")
	req = req.WithContext(auth.WithPrincipal(req.Context(), &auth.Principal{
		Kind:     auth.KindAPIToken,
		User:     db.User{ID: "user-1"},
		APIToken: &db.APIToken{ID: "token-1", Scopes: db.Scopes},
	}))

	rec := send(d, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusSeeOther)
	}
}

func TestTokenPageOffersEveryScope(t *testing.T) {
	d, _ := newTestDashboard(t)
	rec := send(d, signedIn(http.MethodGet, pathTokens, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}

	body := rec.Body.String()
	for _, scope := range db.Scopes {
		if !strings.Contains(body, scope) {
			t.Errorf("the token page does not offer %q", scope)
		}
	}
}

// TestFormsRequireACSRFToken walks every mutating route without a token. The
// handlers behind them are never reached, which is also why none of them needs
// a database.
func TestFormsRequireACSRFToken(t *testing.T) {
	d, _ := newTestDashboard(t)

	paths := []string{
		pathLogin,
		pathLogout,
		pathServices,
		pathServices + "/0198f3a1-2b4c-7d8e-9f01-23456789abcd",
		pathServices + "/0198f3a1-2b4c-7d8e-9f01-23456789abcd/rotate",
		pathServices + "/0198f3a1-2b4c-7d8e-9f01-23456789abcd/delete",
		pathSafety,
		pathSafety + "/settings",
		pathSafety + "/0198f3a1-2b4c-7d8e-9f01-23456789abcd",
		pathSafety + "/0198f3a1-2b4c-7d8e-9f01-23456789abcd/test",
		pathSafety + "/0198f3a1-2b4c-7d8e-9f01-23456789abcd/delete",
		pathDevices + "/0198f3a1-2b4c-7d8e-9f01-23456789abcd/delete",
		pathTokens,
		pathTokens + "/0198f3a1-2b4c-7d8e-9f01-23456789abcd/revoke",
		pathTest,
		pathAuthorize,
	}
	for _, path := range paths {
		rec := send(d, signedIn(http.MethodPost, path, "name=x"))
		if rec.Code != http.StatusForbidden {
			t.Errorf("POST %s: status = %d, want %d", path, rec.Code, http.StatusForbidden)
		}
	}
}

// TestFormsRejectAMismatchedCSRFToken covers the case a missing token does not:
// a value that is present but is not the one in the cookie.
func TestFormsRejectAMismatchedCSRFToken(t *testing.T) {
	d, _ := newTestDashboard(t)

	cookie, err := newCSRFToken()
	if err != nil {
		t.Fatalf("mint a CSRF token: %v", err)
	}
	submitted, err := newCSRFToken()
	if err != nil {
		t.Fatalf("mint a CSRF token: %v", err)
	}
	req := signedIn(http.MethodPost, pathLogout, csrfField+"="+submitted)
	req.AddCookie(&http.Cookie{Name: d.csrf.name, Value: cookie})

	if rec := send(d, req); rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestSignOutRetiresTheSessionAndClearsTheCookie(t *testing.T) {
	d, service := newTestDashboard(t)

	rec := send(d, withCSRF(t, d, signedIn(http.MethodPost, pathLogout, ""), ""))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusSeeOther, rec.Body)
	}
	if want := pathLogin + "?done=signed_out"; rec.Header().Get("Location") != want {
		t.Errorf("Location = %q, want %q", rec.Header().Get("Location"), want)
	}
	if len(service.loggedOut) != 1 || service.loggedOut[0] != "session-1" {
		t.Errorf("logged out = %v, want [session-1]", service.loggedOut)
	}
	if !clearsCookie(rec, d.session.Name()) {
		t.Errorf("the session cookie was not cleared: %v", rec.Result().Cookies())
	}
}

func clearsCookie(rec *httptest.ResponseRecorder, name string) bool {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name && c.MaxAge < 0 {
			return true
		}
	}
	return false
}

func TestSignInIssuesTheSessionCookie(t *testing.T) {
	d, _ := newTestDashboard(t)

	req := withCSRF(t, d, request(http.MethodPost, pathLogin, ""), "username=admin&password=hunter2&next="+url.QueryEscape(pathDevices))
	rec := send(d, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusSeeOther, rec.Body)
	}
	if got := rec.Header().Get("Location"); got != pathDevices {
		t.Errorf("Location = %q, want %q", got, pathDevices)
	}

	var session *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == d.session.Name() {
			session = c
		}
	}
	if session == nil {
		t.Fatalf("no session cookie was set: %v", rec.Result().Cookies())
	}
	if session.Value != "hark_sk_test" || !session.HttpOnly || !session.Secure {
		t.Errorf("session cookie = %+v, want the login token, HttpOnly and Secure", session)
	}
}

func TestSignInRejectsWrongCredentials(t *testing.T) {
	d, service := newTestDashboard(t)
	service.loginErr = auth.ErrInvalidCredentials

	rec := send(d, withCSRF(t, d, request(http.MethodPost, pathLogin, ""), "username=admin&password=wrong"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	// The form comes back with the username filled in and an error banner.
	body := rec.Body.String()
	if !strings.Contains(body, `value="admin"`) {
		t.Errorf("the username was not echoed back:\n%s", body)
	}
	if !strings.Contains(body, `data-notice="error"`) {
		t.Errorf("the page does not carry an error banner:\n%s", body)
	}
}

func TestSignInIsRateLimited(t *testing.T) {
	d, service := newTestDashboard(t)
	service.loginErr = auth.ErrInvalidCredentials

	var last *httptest.ResponseRecorder
	for range loginPerClient + 1 {
		req := withCSRF(t, d, request(http.MethodPost, pathLogin, ""), "username=admin&password=wrong")
		req.Header.Set("X-Real-Ip", "203.0.113.7")
		last = send(d, req)
	}
	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("status after %d attempts = %d, want %d", loginPerClient+1, last.Code, http.StatusTooManyRequests)
	}
	if last.Header().Get("Retry-After") == "" {
		t.Error("a rate-limited sign-in carries no Retry-After")
	}
}

func TestUnknownDashboardPathsRenderAnHTMLNotFound(t *testing.T) {
	d, _ := newTestDashboard(t)

	rec := send(d, request(http.MethodGet, pathHome+"/nope", ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Content-Type = %q, want HTML", got)
	}
}

func TestAssetsAreContentAddressedAndImmutable(t *testing.T) {
	d, _ := newTestDashboard(t)

	for _, link := range []string{assets.CSS, assets.JS} {
		if !strings.HasPrefix(link, pathAssets+"/") {
			t.Fatalf("asset link %q is not under %s", link, pathAssets)
		}

		rec := send(d, request(http.MethodGet, link, ""))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status = %d, want 200", link, rec.Code)
		}
		if rec.Body.Len() == 0 {
			t.Errorf("GET %s: empty body", link)
		}
		if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
			t.Errorf("GET %s: Cache-Control = %q, want an immutable response", link, got)
		}

		etag := rec.Header().Get("ETag")
		if etag == "" {
			t.Fatalf("GET %s: no ETag", link)
		}
		req := request(http.MethodGet, link, "")
		req.Header.Set("If-None-Match", etag)
		if again := send(d, req); again.Code != http.StatusNotModified {
			t.Errorf("GET %s with If-None-Match: status = %d, want 304", link, again.Code)
		}
	}

	if rec := send(d, request(http.MethodGet, pathAssets+"/missing.css", "")); rec.Code != http.StatusNotFound {
		t.Errorf("unknown asset: status = %d, want 404", rec.Code)
	}
}

func TestEveryResponseCarriesTheContentSecurityPolicy(t *testing.T) {
	d, _ := newTestDashboard(t)

	rec := send(d, request(http.MethodGet, pathLogin, ""))
	if got := rec.Header().Get("Content-Security-Policy"); got != contentSecurityPolicy {
		t.Errorf("Content-Security-Policy = %q, want the dashboard's policy", got)
	}
}

func TestPagesAreNotCached(t *testing.T) {
	d, _ := newTestDashboard(t)

	rec := send(d, request(http.MethodGet, pathLogin, ""))
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

func TestCSRFCookieTakesTheHostPrefixOverHTTPS(t *testing.T) {
	d, _ := newTestDashboard(t)
	if !strings.HasPrefix(d.csrf.name, "__Host-") || !d.csrf.secure {
		t.Errorf("csrf cookie = %q secure=%v, want a __Host- prefixed secure cookie", d.csrf.name, d.csrf.secure)
	}

	plain := New(Options{
		Auth: &fakeAuth{}, Store: db.New(nil), Secrets: testKeeper(),
		PublicURL: &url.URL{Scheme: "http", Host: "localhost:8080"},
	})
	if strings.HasPrefix(plain.csrf.name, "__Host-") || plain.csrf.secure {
		t.Errorf("csrf cookie over http = %q secure=%v, want no prefix and no Secure",
			plain.csrf.name, plain.csrf.secure)
	}
}

func TestSafeNext(t *testing.T) {
	tests := map[string]string{
		"":                       pathHome,
		pathHome:                 pathHome,
		pathDevices:              pathDevices,
		pathTokens:               pathTokens,
		"https://evil.example":   pathHome,
		"//evil.example":         pathHome,
		"/v1/tokens":             pathHome,
		"/dashboard/../../etc":   pathHome,
		"/dashboard/x\nSet: y":   pathHome,
		"/dashboard//evil":       pathHome,
		`/dashboard/\evil.local`: pathHome,

		// The approval page is the one destination outside the prefix, and the
		// one allowed a query. Its code survives, canonicalised; anything else
		// in that query is dropped rather than followed.
		pathAuthorize:                     pathAuthorize,
		pathAuthorize + "?code=abcd+efgh": pathAuthorize + "?code=ABCD-EFGH",
		pathAuthorize + "?code=ABCD-EFGH": pathAuthorize + "?code=ABCD-EFGH",
		pathAuthorize + "?code=nope":      pathAuthorize,
		pathAuthorize + "?next=//evil":    pathAuthorize,
		pathAuthorize + "?code=A&x=y":     pathAuthorize,
		pathAuthorize + "/../dashboard/x": pathHome,
		pathAuthorize + "x":               pathHome,
	}
	for in, want := range tests {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestTemplatesRender executes every page with a payload that exercises the
// nullable columns in both states.
//
// A template failure is otherwise invisible until the page is requested: the
// parse at init only proves the syntax, not that a field exists or that a
// helper accepts what it is handed.
// pageFixture is one page and a payload for it.
type pageFixture struct {
	tmpl *template.Template
	data any
}

// fixturePages builds a payload for every page, with the nullable columns
// exercised in both states. It is the data behind [TestTemplatesRender].
func fixturePages(d *Dashboard) map[string]pageFixture {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)

	frame := d.shell("Test", "overview", "csrf-token-value", &notice{Kind: noticeOK, Message: "Done."})
	frame.Username = "admin"

	return map[string]pageFixture{
		"login": {tmplLogin, loginPage{view: frame, Next: pathDevices, Username: "admin"}},
		"error": {tmplError, errorPage{view: frame, Status: 404, Message: "There is no such page."}},
		"overview": {tmplOverview, overviewPage{
			view:  frame,
			Stats: overviewStats{Devices: 2, ActiveDevices: 1, Tokens: 3, ActiveTokens: 2, LiveActivities: 1},
			Activities: []db.ActivityListItem{
				{
					LiveActivity: db.LiveActivity{
						ID: "act-1", Key: ptr("deploy"), Status: db.ActivityActive, Sequence: 4,
						AcceptedCount: 1, ExpiresAt: now.Add(time.Hour), UpdatedAt: now, CreatedAt: now,
					},
					SourceName: "deploy-bot",
				},
				{
					LiveActivity: db.LiveActivity{
						ID: "act-2", Status: db.ActivityStarting,
						ExpiresAt: now.Add(time.Hour), UpdatedAt: now, CreatedAt: now,
					},
					SourceName: "ci",
				},
			},
			History: []db.FeedItem{
				{
					ID: "event:1", Kind: db.FeedKindNotification, SourceName: "CI",
					Title: "Build failed", Detail: ptr("main @ abc1234"),
					Status: ptr(db.EventFailed), Error: ptr("BadDeviceToken"),
					Priority: ptr(db.PriorityTimeSensitive), CreatedAt: now,
				},
				{ID: "response:2", Kind: db.FeedKindResponse, SourceName: "agent", Title: "Deploy?", Result: ptr("approved"), CreatedAt: now},
			},
		}},
		"history": {tmplHistory, historyPage{
			view:   frame,
			Filter: db.FeedFilterNotification,
			Older:  pathHistory + "?after=abc&kind=notification",
			Newest: pathHistory + "?kind=notification",
			Items: []db.FeedItem{
				{
					ID: "event:1", Kind: db.FeedKindNotification, SourceName: "<script>alert(1)</script>",
					Title: "Build failed", Detail: ptr("main @ abc1234"),
					Status: ptr(db.EventFailed), Error: ptr("BadDeviceToken"),
					Priority: ptr(db.PriorityTimeSensitive), CreatedAt: now,
				},
				{ID: "live_activity:2", Kind: db.FeedKindLiveActivity, SourceName: "deploy-bot", Title: "Deploy", Result: ptr("update"), CreatedAt: now},
			},
		}},
		"history/empty": {tmplHistory, historyPage{view: frame, Filter: db.FeedFilterAll}},
		"devices": {tmplDevices, devicesPage{
			view: frame,
			Devices: []db.Device{
				{
					ID: "dev-1", Name: ptr("<script>alert(1)</script>"), Platform: db.PlatformIOS, Active: true,
					PushToStartTokenCiphertext: ptr("sealed"), PushToStartEnvironment: ptr(db.EnvironmentProduction),
					LiveActivitySchemaVersion: ptr(db.LiveActivitySchemaVersion),
					CreatedAt:                 now, LastSeenAt: now,
				},
				{ID: "dev-2", Platform: db.PlatformIOS, CreatedAt: now, LastSeenAt: now},
			},
		}},
		"tokens": {tmplTokens, tokensPage{
			view:   frame,
			Scopes: db.Scopes,
			Secret: "hark_at_notarealsecret",
			Form:   tokenForm{Name: "deploy-bot", Scopes: []string{db.ScopeEventsRead}, ExpiresIn: "90d"},
			Now:    now,
			Tokens: []db.APIToken{
				{ID: "tok-1", Name: "deploy-bot", Prefix: "hark_at_abcd", Scopes: []string{db.ScopeNotificationsNew}, CreatedAt: now, LastUsedAt: &now},
				{ID: "tok-2", Name: "retired", Prefix: "hark_at_efgh", RevokedAt: &now, CreatedAt: now},
			},
		}},
		"authorize": {tmplAuthorize, authorizePage{
			view:    frame,
			Code:    "ABCD-EFGH",
			Pending: true,
			Request: &db.DeviceAuthorization{
				UserCode: "ABCD-EFGH", ClientName: "<script>alert(1)</script>",
				RequestedScopes: []string{db.ScopeNotificationsNew}, Status: db.DeviceAuthPending,
				ExpiresAt: now.Add(10 * time.Minute), TokenExpiresAt: now.Add(24 * time.Hour),
			},
		}},
		"authorize/settled": {tmplAuthorize, authorizePage{
			view: frame,
			Code: "ABCD-EFGH",
			Request: &db.DeviceAuthorization{
				UserCode: "ABCD-EFGH", ClientName: "harkctl", Status: db.DeviceAuthDenied,
				ExpiresAt: now, TokenExpiresAt: now,
			},
		}},
		"authorize/empty": {tmplAuthorize, authorizePage{view: frame}},
		"services": {tmplServices, servicesPage{
			view:       frame,
			Priorities: db.Priorities,
			Form:       serviceForm{Title: "<script>alert(1)</script>", Priority: db.PriorityNormal},
			Services: []serviceRow{
				{
					Service: db.Service{
						ID: "svc-1", Title: "<script>alert(1)</script>",
						ImageURL: ptr("https://example.com/logo.png"), URL: ptr("https://example.com/run"),
						Priority: db.PriorityTimeSensitive, CreatedAt: now, UpdatedAt: now,
					},
					WebhookURL: ptr("https://hark.example.com/v1/hooks/harkhook_notarealtoken"),
				},
				{
					Service: db.Service{ID: "svc-2", Title: "ci", Priority: db.PriorityNormal, CreatedAt: now, UpdatedAt: now},
					// A ciphertext that would not open: the row renders without a copy button.
					WebhookURL: nil,
				},
			},
		}},
		"service": {tmplService, servicePage{
			view: frame,
			Service: db.Service{
				ID: "svc-1", Title: "<script>alert(1)</script>",
				ImageURL: ptr("https://example.com/logo.png"), URL: ptr("https://example.com/run"),
				Priority: db.PriorityTimeSensitive, CreatedAt: now, UpdatedAt: now,
			},
			WebhookURL: ptr("https://hark.example.com/v1/hooks/harkhook_notarealtoken"),
			Priorities: db.Priorities,
			Form:       serviceForm{Title: "CI", ImageURL: "https://example.com/logo.png", Priority: db.PriorityTimeSensitive},
			Deliveries: []db.EventListItem{
				{Event: db.Event{
					ID: "evt-1", Title: "<script>alert(1)</script>", Body: "Build 4821 failed",
					Priority: db.PriorityTimeSensitive, Status: db.EventFailed,
					Error: ptr("BadDeviceToken"), CreatedAt: now,
				}},
				{Event: db.Event{
					ID: "evt-2", Title: "Deploy", Body: "Build 4820 succeeded",
					Priority: db.PriorityNormal, Status: db.EventAccepted, DeliveredCount: 2, CreatedAt: now,
				}},
			},
		}},
		"service/unreadable": {tmplService, servicePage{
			view:       frame,
			Service:    db.Service{ID: "svc-2", Title: "ci", Priority: db.PriorityNormal, CreatedAt: now, UpdatedAt: now},
			Priorities: db.Priorities,
			Form:       serviceForm{Title: "ci", Priority: db.PriorityNormal},
		}},
		"safety": {tmplSafety, safetyPage{
			view:                  frame,
			CriticalAlertsEnabled: true,
			Kinds:                 db.CriticalSafetyKinds,
			Form:                  safetyForm{Name: "<script>alert(1)</script>"},
			Sources: []db.SafetySource{
				{
					ID: "safe-1", Kind: db.SafetyKindSmoke, Name: "<script>alert(1)</script>",
					CriticalEnabled: true, CreatedAt: now, UpdatedAt: now,
				},
				{
					ID: "safe-2", Kind: db.SafetyKindGeneral, Name: "Home Assistant",
					CriticalEnabled: false, CreatedAt: now, UpdatedAt: now,
				},
			},
		}},
		"safety/empty": {tmplSafety, safetyPage{
			view:  frame,
			Kinds: db.CriticalSafetyKinds,
		}},
		"test": {tmplTest, testPage{
			view:       frame,
			Devices:    []db.Device{{ID: "dev-1", Name: ptr("iPhone"), Active: true}, {ID: "dev-2"}},
			Priorities: db.Priorities,
			Form:       testForm{Body: "hello", DeviceID: "dev-1", Priority: db.PriorityNormal},
			Result:     &testResult{Attempted: 2, Accepted: 1, Failures: []string{"APNs request failed: BadDeviceToken"}},
		}},
	}
}

// TestTemplatesRender executes every page with a payload that exercises the
// nullable columns in both states.
//
// A template failure is otherwise invisible until the page is requested: the
// parse at init only proves the syntax, not that a field exists or that a
// helper accepts what it is handed.
func TestTemplatesRender(t *testing.T) {
	d, _ := newTestDashboard(t)

	pages := fixturePages(d)
	for name, page := range pages {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			d.render(rec, request(http.MethodGet, pathHome, ""), http.StatusOK, page.tmpl, page.data)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
			}
			body := rec.Body.String()
			if !strings.HasPrefix(body, "<!DOCTYPE html>") {
				t.Errorf("the layout did not run:\n%s", body)
			}
			// html/template rewrites a value it refuses to trust into this
			// marker rather than failing, so it has to be asserted on.
			if strings.Contains(body, "ZgotmplZ") {
				t.Errorf("a value was filtered by html/template:\n%s", body)
			}
			if strings.Contains(body, "<script>alert(1)</script>") {
				t.Errorf("a stored value was not escaped:\n%s", body)
			}
			if !strings.Contains(body, assets.CSS) {
				t.Errorf("the stylesheet is not linked:\n%s", body)
			}
			if !strings.Contains(body, assets.HTMX) || !strings.Contains(body, `hx-boost="true"`) {
				t.Errorf("boosted navigation is not wired:\n%s", body)
			}
			// Generic test notifications cannot request critical priority.
			if name == "test" && strings.Contains(body, `value="`+db.PriorityCritical+`"`) {
				t.Errorf("the test page offers a critical option:\n%s", body)
			}
		})
	}
}

// TestFormsCarryTheCSRFToken checks the other half of the double submit: a page
// that renders a form has to write the cookie's token into it.
func TestFormsCarryTheCSRFToken(t *testing.T) {
	d, _ := newTestDashboard(t)

	rec := send(d, request(http.MethodGet, pathLogin, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}

	var issued string
	for _, c := range rec.Result().Cookies() {
		if c.Name == d.csrf.name {
			issued = c.Value
		}
	}
	if issued == "" {
		t.Fatal("no CSRF cookie was issued with the sign-in form")
	}
	if !strings.Contains(rec.Body.String(), `value="`+issued+`"`) {
		t.Errorf("the form does not carry the issued token %q:\n%s", issued, rec.Body)
	}
}

func ptr[T any](v T) *T { return &v }

// TestAPageEmbedsTheTokenItJustIssued is the regression guard for reading the
// token back off the request: a browser arriving with no CSRF cookie gets one
// on the response, and the form on that very page has to carry it. Reading the
// request's cookie instead would render an empty field, and the first thing the
// owner submitted would be refused.
func TestAPageEmbedsTheTokenItJustIssued(t *testing.T) {
	d, _ := newTestDashboard(t)

	var rendered string
	page := d.page(func(_ http.ResponseWriter, r *http.Request, p *auth.Principal) {
		rendered = d.newView(r, p, "Test", "tokens").CSRF
	})

	rec := httptest.NewRecorder()
	page(rec, signedIn(http.MethodGet, pathTokens, ""))

	var issued string
	for _, c := range rec.Result().Cookies() {
		if c.Name == d.csrf.name {
			issued = c.Value
		}
	}
	if issued == "" {
		t.Fatal("no CSRF cookie was issued to a browser that had none")
	}
	if rendered != issued {
		t.Errorf("the page carries %q but the cookie says %q", rendered, issued)
	}
}

// TestAPageKeepsTheTokenTheBrowserAlreadyHas is the other half: an existing
// cookie must not be rotated on every page load, or two tabs would fight over
// which token is current.
func TestAPageKeepsTheTokenTheBrowserAlreadyHas(t *testing.T) {
	d, _ := newTestDashboard(t)

	existing, err := newCSRFToken()
	if err != nil {
		t.Fatalf("mint a CSRF token: %v", err)
	}

	var rendered string
	page := d.page(func(_ http.ResponseWriter, r *http.Request, p *auth.Principal) {
		rendered = d.newView(r, p, "Test", "tokens").CSRF
	})

	req := signedIn(http.MethodGet, pathTokens, "")
	req.AddCookie(&http.Cookie{Name: d.csrf.name, Value: existing})

	rec := httptest.NewRecorder()
	page(rec, req)

	if rendered != existing {
		t.Errorf("the page carries %q, want the browser's own %q", rendered, existing)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Errorf("the token was reissued unnecessarily: %v", rec.Result().Cookies())
	}
}

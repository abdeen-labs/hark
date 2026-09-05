package httpapi

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abdeen-labs/hark/internal/auth"
)

// TestCredentialRoutesAreClosedByDefault walks the whole auth surface without a
// credential. It is the cheapest guard against a route being added to the table
// without a Require* middleware around it.
func TestCredentialRoutesAreClosedByDefault(t *testing.T) {
	h := newTestServer(t, stubPinger{})

	closed := []struct {
		method, path string
	}{
		{http.MethodPost, "/auth/logout"},
		{http.MethodGet, "/auth/session"},
		{http.MethodPost, "/auth/password"},
		{http.MethodGet, "/accounts"},
		{http.MethodPost, "/accounts"},
		{http.MethodGet, "/auth/device/requests/K7QM-3XPD"},
		{http.MethodPost, "/auth/device/requests/K7QM-3XPD/approve"},
		{http.MethodPost, "/auth/device/requests/K7QM-3XPD/deny"},
		{http.MethodGet, "/tokens"},
		{http.MethodPost, "/tokens"},
		{http.MethodDelete, "/tokens/0198f3a1-2b4c-7d8e-9f01-23456789abcd"},
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

// TestPublicRoutesTakeNoCredential documents the three endpoints that must work
// for a caller who has nothing yet: the readiness probe, sign-in, and the two
// halves of the device grant a CLI uses before it owns a token.
func TestPublicRoutesTakeNoCredential(t *testing.T) {
	h := newTestServer(t, stubPinger{})

	// A malformed body proves the request reached the handler rather than
	// being turned away by an auth middleware, without needing a database.
	for _, path := range []string{"/auth/login", "/auth/device/code", "/auth/device/token"} {
		rec := do(t, h, http.MethodPost, path, strings.NewReader("not json"))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST %s: status = %d, want 400 from the handler: %s", path, rec.Code, rec.Body)
			continue
		}
		if got := decodeError(t, rec); got.Error.Code != CodeBadRequest {
			t.Errorf("POST %s: code = %q, want %q", path, got.Error.Code, CodeBadRequest)
		}
	}
}

func TestJSONBodiesMustBeJSON(t *testing.T) {
	h := newTestServer(t, stubPinger{})

	tests := map[string]struct {
		contentType string
		body        string
		wantStatus  int
		wantCode    string
	}{
		"no content type": {"", `{"username":"admin","password":"x"}`,
			http.StatusUnsupportedMediaType, CodeUnsupportedMedia},
		"form encoded": {"application/x-www-form-urlencoded", "username=admin",
			http.StatusUnsupportedMediaType, CodeUnsupportedMedia},
		"empty body": {"application/json", "",
			http.StatusBadRequest, CodeBadRequest},
		"unknown field": {"application/json", `{"username":"admin","password":"x","remember_me":true}`,
			http.StatusBadRequest, CodeBadRequest},
		"trailing garbage": {"application/json", `{"username":"admin","password":"x"} {}`,
			http.StatusBadRequest, CodeBadRequest},
		"wrong type": {"application/json", `{"username":42,"password":"x"}`,
			http.StatusBadRequest, CodeBadRequest},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			req := newRequest(t, http.MethodPost, "/auth/login", tc.body)
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			rec := send(t, h, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantStatus, rec.Body)
			}
			if got := decodeError(t, rec); got.Error.Code != tc.wantCode {
				t.Errorf("code = %q, want %q", got.Error.Code, tc.wantCode)
			}
		})
	}

	// A charset parameter is part of the media type, not a different one.
	req := newRequest(t, http.MethodPost, "/auth/login", `{"username":"admin"`)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	if rec := send(t, h, req); rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d for a charset parameter, want the body to be parsed", rec.Code)
	}
}

// TestTimestampFormat pins the one timestamp rendering the whole API uses.
// Go's default would drop trailing zeros, so the same instant would render
// differently from one response to the next.
func TestTimestampFormat(t *testing.T) {
	tests := map[string]struct {
		in   time.Time
		want string
	}{
		"milliseconds":  {time.Date(2026, 8, 9, 12, 34, 56, 789_000_000, time.UTC), `"2026-08-09T12:34:56.789Z"`},
		"whole second":  {time.Date(2026, 8, 9, 12, 34, 56, 0, time.UTC), `"2026-08-09T12:34:56.000Z"`},
		"trailing zero": {time.Date(2026, 8, 9, 12, 34, 56, 100_000_000, time.UTC), `"2026-08-09T12:34:56.100Z"`},
		"other zone": {time.Date(2026, 8, 9, 14, 34, 56, 0, time.FixedZone("CEST", 2*60*60)),
			`"2026-08-09T12:34:56.000Z"`},
	}
	for name, tc := range tests {
		got, err := json.Marshal(Timestamp(tc.in))
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		if string(got) != tc.want {
			t.Errorf("%s: %s, want %s", name, got, tc.want)
		}
	}

	if got, err := json.Marshal(struct {
		At *Timestamp `json:"at"`
	}{}); err != nil || string(got) != `{"at":null}` {
		t.Errorf("nil timestamp = %s, %v; want {\"at\":null}", got, err)
	}
	if TimestampPtr(nil) != nil {
		t.Error("TimestampPtr(nil) is not nil")
	}
}

// TestParseSecondsRejectsOverflow is the reason the range is checked before the
// multiplication: 9_223_372_037 seconds overflows time.Duration and wraps to a
// small negative value, which would sail past a ceiling expressed in hours.
func TestParseSecondsRejectsOverflow(t *testing.T) {
	valid := int64(3600)
	if got, ok := parseSeconds(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil),
		"expires_in_seconds", &valid); !ok || got != time.Hour {
		t.Errorf("parseSeconds(3600) = %v, %v; want 1h, true", got, ok)
	}

	if got, ok := parseSeconds(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil),
		"expires_in_seconds", nil); !ok || got != 0 {
		t.Errorf("parseSeconds(nil) = %v, %v; want 0, true", got, ok)
	}

	for name, seconds := range map[string]int64{
		"zero":         0,
		"negative":     -1,
		"past maximum": maxDurationSeconds + 1,
		"max int64":    math.MaxInt64,
	} {
		rec := httptest.NewRecorder()
		if _, ok := parseSeconds(rec, httptest.NewRequest(http.MethodPost, "/", nil),
			"expires_in_seconds", &seconds); ok {
			t.Errorf("%s: parseSeconds(%d) was accepted", name, seconds)
			continue
		}
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d, want 422", name, rec.Code)
		}
	}
}

// TestDashboardMount pins the URL space the embedded admin UI takes over: the
// site root and its own subtree, every method, and nothing else. Everything the
// dashboard does not claim keeps answering in the JSON envelope.
func TestDashboardMount(t *testing.T) {
	admin := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("dashboard"))
	})
	h := newTestServer(t, stubPinger{}, func(o *Options) { o.Dashboard = admin })

	dashboardPaths := []struct{ method, path string }{
		{http.MethodGet, "/"},
		{http.MethodGet, DashboardPrefix},
		{http.MethodGet, DashboardPrefix + "/"},
		{http.MethodGet, DashboardPrefix + "/history"},
		{http.MethodGet, DashboardPrefix + "/live/overview"},
		{http.MethodGet, DashboardPrefix + "/devices"},
		{http.MethodPost, DashboardPrefix + "/tokens"},
		{http.MethodGet, DashboardPrefix + "/assets/app.css"},
		// Two pages live outside the prefix because their URLs are addresses
		// something else hands out: the link a CLI prints, and the contract.
		{http.MethodGet, DeviceVerificationPath},
		{http.MethodPost, DeviceVerificationPath},
		{http.MethodGet, DocsPath},
		{http.MethodGet, DocsMarkdownPath},
		{http.MethodGet, OpenAPIPath},
		{http.MethodGet, LLMsPath},
	}
	for _, route := range dashboardPaths {
		rec := do(t, h, route.method, route.path, nil)
		if rec.Code != http.StatusOK || rec.Body.String() != "dashboard" {
			t.Errorf("%s %s: status = %d body = %q, want the dashboard",
				route.method, route.path, rec.Code, rec.Body)
		}
	}

	// The API catch-all still owns paths the dashboard does not claim.
	for _, path := range []string{"/nope", "/apiish/nope", "/dashboardish"} {
		rec := do(t, h, http.MethodGet, path, nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: status = %d, want 404", path, rec.Code)
			continue
		}
		if got := decodeError(t, rec); got.Error.Code != CodeNotFound {
			t.Errorf("GET %s: code = %q, want %q", path, got.Error.Code, CodeNotFound)
		}
	}
}

// TestTheContractIsServedOutsideTheCredentialChain pins where the public page
// is mounted, not merely that nothing guards it.
//
// A malformed Authorization header is a 401 everywhere else on this server —
// the authenticator refuses a header it cannot parse rather than continuing as
// anonymous — so a /docs that answers one anyway is proof the credential
// middleware never ran on the way there.
func TestTheContractIsServedOutsideTheCredentialChain(t *testing.T) {
	var served int
	h := newTestServer(t, stubPinger{}, func(o *Options) {
		o.Dashboard = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			served++
			if auth.PrincipalFrom(r.Context()) != nil {
				t.Error("the public page was handed a principal")
			}
			if LoggerFrom(r.Context()) == nil {
				t.Error("the public page runs without the request-scoped logger")
			}
			if RequestIDFrom(r.Context()) == "" {
				t.Error("the public page runs without a request id")
			}
			w.WriteHeader(http.StatusOK)
		})
	})

	for _, path := range []string{DocsPath, DocsMarkdownPath, OpenAPIPath, LLMsPath} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer not-a-credential")
		if rec := send(t, h, req); rec.Code != http.StatusOK {
			t.Errorf("GET %s with a junk credential: status = %d, want 200", path, rec.Code)
		}
	}
	if served != 4 {
		t.Fatalf("the public documents were served %d times, want four", served)
	}

	// The same header on an API route is refused, preserving the boundary
	// above mean anything.
	req := httptest.NewRequest(http.MethodGet, "/interactions", nil)
	req.Header.Set("Authorization", "Bearer not-a-credential")
	if rec := send(t, h, req); rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /interactions with the same header: status = %d, want 401", rec.Code)
	}
}

// TestWithoutADashboardTheRootIs404 keeps the mount optional: a deployment that
// wires no admin UI serves the API alone, in the JSON envelope throughout.
func TestWithoutADashboardTheRootIs404(t *testing.T) {
	h := newTestServer(t, stubPinger{})

	for _, path := range []string{"/", DashboardPrefix, DeviceVerificationPath, DocsPath, DocsMarkdownPath, OpenAPIPath, LLMsPath} {
		rec := do(t, h, http.MethodGet, path, nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: status = %d, want 404", path, rec.Code)
			continue
		}
		if got := decodeError(t, rec); got.Error.Code != CodeNotFound {
			t.Errorf("GET %s: code = %q, want %q", path, got.Error.Code, CodeNotFound)
		}
	}
}

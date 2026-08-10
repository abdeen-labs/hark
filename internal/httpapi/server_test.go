package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/id"
	"github.com/abdeen-labs/hark/internal/push"
	"github.com/abdeen-labs/hark/internal/secret"
)

type stubPinger struct{ err error }

func (s stubPinger) Ping(context.Context) error { return s.err }

// newTestServer builds the real handler over a Service with no database behind
// it. Nothing in these tests presents a credential, so no store call is ever
// reached; the flows that do need one live in auth_pg_test.go.
//
// The variadic functions adjust the options a test cares about, so adding a
// dependency does not mean adding a parameter every call site has to pass.
func newTestServer(t *testing.T, pinger Pinger, with ...func(*Options)) http.Handler {
	t.Helper()
	store := db.New(nil)
	opts := Options{
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:              pinger,
		Auth:            auth.New(store, nil),
		Store:           store,
		Secrets:         secret.NewKeeper([]byte("test-key-that-is-long-enough-abcdef")),
		Push:            push.Noop{},
		PublicURL:       &url.URL{Scheme: "https", Host: "hark.example.com"},
		MaxRequestBytes: 64 << 10,
		Version:         "test",
	}
	for _, apply := range with {
		apply(&opts)
	}
	return New(opts)
}

// do sends a request with the JSON content type every API endpoint expects.
func do(t *testing.T, h http.Handler, method, target string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, body)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return send(t, h, req)
}

func newRequest(t *testing.T, method, target, body string) *http.Request {
	t.Helper()
	if body == "" {
		return httptest.NewRequest(method, target, nil)
	}
	return httptest.NewRequest(method, target, strings.NewReader(body))
}

func send(t *testing.T, h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) ErrorResponse {
	t.Helper()
	var got ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not the error envelope: %v\n%s", err, rec.Body.String())
	}
	if got.Error.Code == "" || got.Error.Message == "" {
		t.Errorf("error envelope is incomplete: %s", rec.Body.String())
	}
	return got
}

func TestHealthOK(t *testing.T) {
	rec := do(t, newTestServer(t, stubPinger{}), http.MethodGet, "/healthz", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	var got healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Status != "ok" || got.Database != "ok" || got.Version != "test" {
		t.Errorf("body = %+v", got)
	}
}

func TestHealthReportsDatabaseFailure(t *testing.T) {
	rec := do(t, newTestServer(t, stubPinger{err: errors.New("connection refused")}), http.MethodGet, "/healthz", nil)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	got := decodeError(t, rec)
	if got.Error.Code != CodeUnavailable {
		t.Errorf("code = %q, want %q", got.Error.Code, CodeUnavailable)
	}
	// The probe must not leak the driver's error text to unauthenticated callers.
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("body leaks the underlying error: %s", rec.Body)
	}
}

func TestHealthAnswersHEAD(t *testing.T) {
	if rec := do(t, newTestServer(t, stubPinger{}), http.MethodHead, "/healthz", nil); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestNewRequiresItsDependencies(t *testing.T) {
	store := db.New(nil)
	keeper := secret.NewKeeper([]byte("test-key-that-is-long-enough-abcdef"))
	for name, opts := range map[string]Options{
		"no database": {Auth: auth.New(store, nil), Store: store, Secrets: keeper},
		"no auth":     {DB: stubPinger{}, Store: store, Secrets: keeper},
		"no store":    {DB: stubPinger{}, Auth: auth.New(store, nil), Secrets: keeper},
		"no secrets":  {DB: stubPinger{}, Auth: auth.New(store, nil), Store: store},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("New did not panic on a missing dependency")
				}
			}()
			New(opts)
		})
	}
}

func TestUnknownRouteIsJSON404(t *testing.T) {
	for _, target := range []string{"/", "/nope", "/v1/services/", "/v1/unicorns", "/healthz/"} {
		rec := do(t, newTestServer(t, stubPinger{}), http.MethodGet, target, nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: status = %d, want 404", target, rec.Code)
			continue
		}
		if got := decodeError(t, rec); got.Error.Code != CodeNotFound {
			t.Errorf("GET %s: code = %q, want %q", target, got.Error.Code, CodeNotFound)
		}
	}
}

func TestMethodMismatchIs405WithAllow(t *testing.T) {
	rec := do(t, newTestServer(t, stubPinger{}), http.MethodPost, "/healthz", strings.NewReader("{}"))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != "GET, HEAD" {
		t.Errorf("Allow = %q, want %q", allow, "GET, HEAD")
	}
	if got := decodeError(t, rec); got.Error.Code != CodeMethodNotAllow {
		t.Errorf("code = %q, want %q", got.Error.Code, CodeMethodNotAllow)
	}
}

func TestOversizedBodyIsRejected(t *testing.T) {
	store := db.New(nil)
	h := New(Options{
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:              stubPinger{},
		Auth:            auth.New(store, nil),
		Store:           store,
		Secrets:         secret.NewKeeper([]byte("test-key-that-is-long-enough-abcdef")),
		Push:            push.Noop{},
		MaxRequestBytes: 16,
	})
	rec := do(t, h, http.MethodPost, "/healthz", bytes.NewReader(bytes.Repeat([]byte("x"), 64)))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	if got := decodeError(t, rec); got.Error.Code != CodePayloadTooLarge {
		t.Errorf("code = %q, want %q", got.Error.Code, CodePayloadTooLarge)
	}
}

func TestRequestIDIsAssignedAndEchoed(t *testing.T) {
	h := newTestServer(t, stubPinger{})

	rec := do(t, h, http.MethodGet, "/healthz", nil)
	assigned := rec.Header().Get(RequestIDHeader)
	if !id.Valid(assigned) {
		t.Errorf("assigned request id %q is not a UUIDv7", assigned)
	}

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set(RequestIDHeader, "trace-abc.123")
	echo := httptest.NewRecorder()
	h.ServeHTTP(echo, req)
	if got := echo.Header().Get(RequestIDHeader); got != "trace-abc.123" {
		t.Errorf("client request id = %q, want it echoed", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set(RequestIDHeader, "bad id with spaces")
	replaced := httptest.NewRecorder()
	h.ServeHTTP(replaced, req)
	if got := replaced.Header().Get(RequestIDHeader); !id.Valid(got) {
		t.Errorf("malformed client request id was not replaced: %q", got)
	}
}

func TestPanicBecomes500(t *testing.T) {
	var logs bytes.Buffer
	rt := newRouter()
	rt.handleFunc(http.MethodGet, "/boom", func(http.ResponseWriter, *http.Request) {
		panic("kaboom")
	})
	h := Chain(rt.handler(),
		RequestID,
		WithLogger(slog.New(slog.NewTextHandler(&logs, nil))),
		Recover,
	)

	rec := do(t, h, http.MethodGet, "/boom", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if got := decodeError(t, rec); got.Error.Code != CodeInternal {
		t.Errorf("code = %q, want %q", got.Error.Code, CodeInternal)
	}
	if strings.Contains(rec.Body.String(), "kaboom") {
		t.Errorf("panic value leaked into the response: %s", rec.Body)
	}
	if !strings.Contains(logs.String(), "kaboom") {
		t.Errorf("panic was not logged:\n%s", logs.String())
	}
}

// TestPanicPathIsRedacted holds the recovery log to the same rule as the access
// log: a handler that panics mid-webhook must not write the URL credential into
// the one log line operators will certainly read.
func TestPanicPathIsRedacted(t *testing.T) {
	const sentinelToken = "harkhook_PANICSENTINELq8Zt3vXy1LmNoP2Rs4TuVwAbCd"

	var logs bytes.Buffer
	rt := newRouter()
	rt.handleFunc(http.MethodPost, "/v1/hooks/{token}", func(http.ResponseWriter, *http.Request) {
		panic("hook exploded")
	})
	h := Chain(rt.handler(),
		RequestID,
		WithLogger(slog.New(slog.NewTextHandler(&logs, nil))),
		Recover,
	)

	rec := do(t, h, http.MethodPost, "/v1/hooks/"+sentinelToken, strings.NewReader(`{"body":"hi"}`))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if got := decodeError(t, rec); got.Error.Code != CodeInternal {
		t.Errorf("code = %q, want %q", got.Error.Code, CodeInternal)
	}

	logged := logs.String()
	if !strings.Contains(logged, "hook exploded") {
		t.Errorf("panic was not logged:\n%s", logged)
	}
	if !strings.Contains(logged, "/v1/hooks/{token}") {
		t.Errorf("logs do not carry the redacted placeholder path:\n%s", logged)
	}
	// Both the recovery line and the access line are in this buffer, and the
	// stack is attached to the recovery line; the token may appear in none of it.
	if strings.Contains(logged, sentinelToken) {
		t.Errorf("the webhook token leaked into the logs:\n%s", logged)
	}
}

func TestRequestWithoutCredentialsIsAnonymous(t *testing.T) {
	rec := do(t, newTestServer(t, stubPinger{}), http.MethodGet, "/v1/auth/session", nil)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if got := decodeError(t, rec); got.Error.Code != CodeUnauthorized {
		t.Errorf("code = %q, want %q", got.Error.Code, CodeUnauthorized)
	}
}

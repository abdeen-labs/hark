package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestLimiterAllowsUpToTheCeiling(t *testing.T) {
	l := newLimiter(time.Minute)
	now := time.Now()

	for i := 1; i <= 3; i++ {
		if !l.allow("k", 3, now) {
			t.Fatalf("request %d was refused below the ceiling", i)
		}
	}
	if l.allow("k", 3, now) {
		t.Error("the fourth request was allowed past a ceiling of 3")
	}
}

func TestLimiterWindowResets(t *testing.T) {
	l := newLimiter(time.Minute)
	now := time.Now()

	l.allow("k", 1, now)
	if l.allow("k", 1, now) {
		t.Fatal("the second request in the window was allowed")
	}
	if !l.allow("k", 1, now.Add(time.Minute+time.Second)) {
		t.Error("the window did not reset")
	}
}

func TestLimiterKeysAreIndependent(t *testing.T) {
	l := newLimiter(time.Minute)
	now := time.Now()

	l.allow("a", 1, now)
	if !l.allow("b", 1, now) {
		t.Error("exhausting one key's bucket blocked another key")
	}
}

// TestLimiterRefusesWhenFull checks the memory bound. Reaching it is itself a
// signal of abuse, so a new key arriving with the map full after a sweep is
// refused rather than admitted — the alternative is unbounded growth driven by
// whoever is attacking.
func TestLimiterRefusesWhenFull(t *testing.T) {
	l := newLimiter(time.Minute)
	now := time.Now()

	for i := range maxBuckets {
		l.allow("live-"+strconv.Itoa(i), 100, now)
	}
	if l.allow("one-more", 100, now) {
		t.Error("a new key was admitted with the bucket map full of live entries")
	}

	// Once the live entries age out, the sweep makes room again.
	later := now.Add(2 * time.Minute)
	if !l.allow("one-more", 100, later) {
		t.Error("expired buckets were not swept to make room")
	}
}

func TestLimiterRetryAfter(t *testing.T) {
	l := newLimiter(time.Minute)
	now := time.Now()

	l.allow("k", 1, now)
	if got := l.retryAfter("k", now.Add(20*time.Second)); got != 40*time.Second {
		t.Errorf("retryAfter = %v, want 40s", got)
	}
	// An unknown or expired key still asks for a pause rather than inviting an
	// immediate retry.
	if got := l.retryAfter("unknown", now); got < time.Second {
		t.Errorf("retryAfter(unknown) = %v, want at least 1s", got)
	}
}

func TestRateLimitedRequestsGet429WithRetryAfter(t *testing.T) {
	s := &server{
		opts:    Options{TrustedClientIPHeader: "X-Real-IP"},
		limiter: newLimiter(time.Minute),
	}
	h := s.rateLimit("probe", 2, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	send := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/probe", nil)
		req.Header.Set("X-Real-IP", "198.51.100.4")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	for i := 1; i <= 2; i++ {
		if rec := send(); rec.Code != http.StatusNoContent {
			t.Fatalf("request %d: status = %d, want 204", i, rec.Code)
		}
	}

	rec := send()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if got := decodeError(t, rec); got.Error.Code != CodeRateLimited {
		t.Errorf("code = %q, want %q", got.Error.Code, CodeRateLimited)
	}
	retry, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	if err != nil || retry < 1 {
		t.Errorf("Retry-After = %q, want a positive number of seconds", rec.Header().Get("Retry-After"))
	}
}

// TestClientKeyNeedsATrustedHeader is the whole reason per-client limiting is
// opt-in: without a header a trusted edge overwrites, any caller could rotate
// the value and mint itself a fresh bucket per request, which is worse than
// having only a global one.
func TestClientKeyNeedsATrustedHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	req.Header.Set("CF-Connecting-IP", "203.0.113.9, 198.51.100.2")

	untrusting := &server{}
	if got := untrusting.clientKey(req); got != "" {
		t.Errorf("clientKey = %q with no trusted header configured, want \"\"", got)
	}

	trusting := &server{opts: Options{TrustedClientIPHeader: "CF-Connecting-IP"}}
	if got := trusting.clientKey(req); got != "203.0.113.9" {
		t.Errorf("clientKey = %q, want the first entry of the trusted header", got)
	}
}

// TestPerClientBucketsAreSeparate checks that two callers behind a trusted edge
// do not share a ceiling.
func TestPerClientBucketsAreSeparate(t *testing.T) {
	s := &server{
		opts:    Options{TrustedClientIPHeader: "X-Real-IP"},
		limiter: newLimiter(time.Minute),
	}
	h := s.rateLimit("probe", 1, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	send := func(ip string) int {
		req := httptest.NewRequest(http.MethodPost, "/probe", nil)
		req.Header.Set("X-Real-IP", ip)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	if got := send("198.51.100.1"); got != http.StatusNoContent {
		t.Fatalf("first caller: status = %d, want 204", got)
	}
	if got := send("198.51.100.1"); got != http.StatusTooManyRequests {
		t.Fatalf("first caller's second request: status = %d, want 429", got)
	}
	if got := send("198.51.100.2"); got != http.StatusNoContent {
		t.Errorf("second caller: status = %d, want 204 — buckets are shared", got)
	}
}

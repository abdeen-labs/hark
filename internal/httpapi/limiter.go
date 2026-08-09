package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Rate-limit window and ceilings for the unauthenticated auth surface.
//
// These endpoints are the ones an anonymous caller can reach, so they are the
// ones worth capping: sign-in because a password is guessable, and the device
// grant because it writes a row and hands out a code before anyone has proved
// anything.
const (
	rateLimitWindow = time.Minute

	// limitLogin is generous for a human who mistypes and tight enough that
	// online guessing is hopeless against an Argon2id hash.
	limitLogin = 10
	// limitDeviceStart bounds how many pairing requests one client may open.
	limitDeviceStart = 20
	// limitDevicePoll comfortably clears the five-second poll pace with room
	// for retries.
	limitDevicePoll = 120

	// globalMultiplier turns a per-client ceiling into the process-wide one.
	// The global bucket is the only defence when no client key can be derived,
	// and a backstop against a distributed attempt when one can.
	globalMultiplier = 20

	// maxBuckets caps the limiter's memory. Reaching it is itself a signal of
	// abuse, so a new key that arrives with the map full after a sweep is
	// refused rather than admitted.
	maxBuckets = 10_000
)

// limiter is a process-local fixed-window counter.
//
// Fixed windows let a caller spend two windows' worth of requests across a
// boundary. That is fine here: these ceilings exist to make brute force and
// runaway polling impractical, not to meter a paid resource, and a fixed window
// costs one map entry instead of a queue of timestamps.
//
// It is per process. Two replicas therefore allow twice the traffic — a
// deliberate trade against putting a shared counter in the request path of
// every sign-in.
type limiter struct {
	mu      sync.Mutex
	window  time.Duration
	buckets map[string]bucket
}

type bucket struct {
	count   int
	resetAt time.Time
}

func newLimiter(window time.Duration) *limiter {
	return &limiter{window: window, buckets: make(map[string]bucket)}
}

// allow records one request against key and reports whether it fits under
// limit.
func (l *limiter) allow(key string, limit int, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[key]
	if ok && b.resetAt.After(now) {
		b.count++
		l.buckets[key] = b
		return b.count <= limit
	}

	if !ok && len(l.buckets) >= maxBuckets {
		l.sweep(now)
		if len(l.buckets) >= maxBuckets {
			return false
		}
	}
	l.buckets[key] = bucket{count: 1, resetAt: now.Add(l.window)}
	return true
}

// retryAfter reports how long key's window has left, for the Retry-After
// header. It never returns less than a second, because "retry after 0" invites
// an immediate retry that will also fail.
func (l *limiter) retryAfter(key string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()

	if b, ok := l.buckets[key]; ok && b.resetAt.After(now) {
		return b.resetAt.Sub(now)
	}
	return time.Second
}

// sweep drops expired buckets. The caller holds the lock.
func (l *limiter) sweep(now time.Time) {
	for key, b := range l.buckets {
		if !b.resetAt.After(now) {
			delete(l.buckets, key)
		}
	}
}

// rateLimit wraps h with a per-client bucket and a process-wide one.
//
// The client key comes only from the header a trusted edge overwrites. With no
// such header configured there is no per-client bucket at all, because the
// alternative — trusting X-Forwarded-For — lets any caller mint itself a fresh
// bucket per request, which is worse than having none.
func (s *server) rateLimit(kind string, perClient int, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()

		if key := s.clientKey(r); key != "" {
			bucketKey := kind + ":client:" + key
			if !s.limiter.allow(bucketKey, perClient, now) {
				s.writeRateLimited(w, r, bucketKey, now)
				return
			}
		}

		globalKey := kind + ":global"
		if !s.limiter.allow(globalKey, perClient*globalMultiplier, now) {
			s.writeRateLimited(w, r, globalKey, now)
			return
		}

		h.ServeHTTP(w, r)
	})
}

func (s *server) writeRateLimited(w http.ResponseWriter, r *http.Request, key string, now time.Time) {
	retry := s.limiter.retryAfter(key, now)
	writeRetryAfter(w, retry)
	WriteError(w, r, http.StatusTooManyRequests, CodeRateLimited,
		"Too many requests. Try again shortly.")
}

// clientKey derives a per-caller bucket key, or "" when none can be trusted.
func (s *server) clientKey(r *http.Request) string {
	header := s.opts.TrustedClientIPHeader
	if header == "" {
		return ""
	}
	// A trusted edge writes one address, but tolerate a chain and take the
	// first entry rather than keying every request on a slightly different
	// string.
	value, _, _ := strings.Cut(r.Header.Get(header), ",")
	return strings.TrimSpace(value)
}

// writeRetryAfter sets Retry-After, rounded up so a client that obeys it never
// arrives a millisecond early.
func writeRetryAfter(w http.ResponseWriter, d time.Duration) {
	seconds := int((d + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
}

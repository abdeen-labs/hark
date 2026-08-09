package dashboard

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

// Sign-in ceilings. The dashboard's form is a second door onto the same
// password as POST /v1/auth/login, so it needs its own ceiling — otherwise the
// API's limit is a formality anyone can walk around by posting a form instead.
//
// The numbers match the API's: generous for a person who mistypes, hopeless for
// online guessing against an Argon2id hash. The buckets are separate from the
// API's, which means the two doors each allow the ceiling rather than sharing
// one. That is the honest cost of not putting a shared counter in the request
// path, and it does not change the order of magnitude.
const (
	loginWindow      = time.Minute
	loginPerClient   = 10
	loginGlobal      = 200
	maxLoginBuckets  = 4096
	minRetryInterval = time.Second
)

// limiter is a fixed-window counter, keyed per client and per process.
//
// A fixed window lets a caller spend two windows' worth across a boundary. That
// is fine for this: the ceiling exists to make brute force impractical, not to
// meter anything, and a fixed window costs one map entry rather than a queue of
// timestamps.
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

// allow records one attempt against key and reports whether it fits under
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
	if !ok && len(l.buckets) >= maxLoginBuckets {
		l.sweep(now)
		if len(l.buckets) >= maxLoginBuckets {
			// A map this full is itself the signal. Refusing is safer than
			// admitting an unbounded number of new keys.
			return false
		}
	}
	l.buckets[key] = bucket{count: 1, resetAt: now.Add(l.window)}
	return true
}

// retryAfter reports how long key's window has left, never less than a second:
// "retry after 0" invites an immediate retry that will also fail.
func (l *limiter) retryAfter(key string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()

	if b, ok := l.buckets[key]; ok && b.resetAt.After(now) {
		return b.resetAt.Sub(now)
	}
	return minRetryInterval
}

// sweep drops expired buckets. The caller holds the lock.
func (l *limiter) sweep(now time.Time) {
	for key, b := range l.buckets {
		if !b.resetAt.After(now) {
			delete(l.buckets, key)
		}
	}
}

// allowLogin charges one sign-in attempt, returning the bucket that refused it.
//
// The per-client key comes only from the header a trusted edge overwrites.
// Without one there is no per-client bucket at all: trusting X-Forwarded-For
// would let any caller mint a fresh bucket per request, which is worse than
// having none, and the process-wide bucket still holds.
func (d *Dashboard) allowLogin(r *http.Request, now time.Time) (key string, ok bool) {
	if client := d.clientKey(r); client != "" {
		key = "client:" + client
		if !d.logins.allow(key, loginPerClient, now) {
			return key, false
		}
	}
	if !d.logins.allow("global", loginGlobal, now) {
		return "global", false
	}
	return "", true
}

func (d *Dashboard) clientKey(r *http.Request) string {
	header := d.opts.TrustedClientIPHeader
	if header == "" {
		return ""
	}
	// A trusted edge writes one address, but tolerate a chain and take the
	// first entry rather than keying every request on a slightly different
	// string.
	value, _, _ := strings.Cut(r.Header.Get(header), ",")
	return strings.TrimSpace(value)
}

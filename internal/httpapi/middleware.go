package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/abdeen-labs/hark/internal/id"
)

// Middleware decorates a handler. Middlewares are applied outermost-first by
// [Chain].
type Middleware func(http.Handler) http.Handler

// Chain wraps h so that mw[0] sees the request first.
func Chain(h http.Handler, mw ...Middleware) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

type contextKey int

const (
	requestIDKey contextKey = iota
	loggerKey
)

// RequestIDHeader carries the correlation id on both the request and the
// response. A client-supplied value is honoured when it looks sane, so a
// reverse proxy can stitch its own traces together.
const RequestIDHeader = "X-Request-Id"

const maxInboundRequestIDLen = 128

// RequestID assigns every request a correlation id and echoes it back.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := sanitizeRequestID(r.Header.Get(RequestIDHeader))
		if rid == "" {
			rid = id.New()
		}
		w.Header().Set(RequestIDHeader, rid)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, rid)))
	})
}

// RequestIDFrom returns the correlation id, or "" outside a request.
func RequestIDFrom(ctx context.Context) string {
	rid, _ := ctx.Value(requestIDKey).(string)
	return rid
}

func sanitizeRequestID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxInboundRequestIDLen {
		return ""
	}
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '-' || c == '_' || c == '.'
		if !ok {
			return ""
		}
	}
	return raw
}

// WithLogger puts a request-scoped logger on the context and logs one line per
// completed request. Query strings are never logged: they can carry user codes
// and other credentials.
func WithLogger(base *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			log := base.With("request_id", RequestIDFrom(r.Context()))
			ctx := context.WithValue(r.Context(), loggerKey, log)

			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			start := time.Now()
			next.ServeHTTP(rec, r.WithContext(ctx))
			elapsed := time.Since(start)

			level := slog.LevelInfo
			switch {
			case rec.status >= 500:
				level = slog.LevelError
			case rec.status >= 400:
				level = slog.LevelWarn
			}
			log.LogAttrs(ctx, level, "request",
				slog.String("method", r.Method),
				slog.String("path", redactPath(r.URL.Path)),
				slog.Int("status", rec.status),
				slog.Int64("bytes", rec.written),
				slog.Duration("duration", elapsed.Round(time.Microsecond)),
			)
		})
	}
}

// hooksPrefix is the one part of the API whose path contains a credential.
const hooksPrefix = APIPrefix + "/hooks/"

// redactPath replaces a webhook token with a placeholder.
//
// Webhook credentials are part of the URL, so redact them before writing access
// logs.
func redactPath(path string) string {
	if !strings.HasPrefix(path, hooksPrefix) {
		return path
	}
	rest := path[len(hooksPrefix):]
	if tail := strings.Index(rest, "/"); tail >= 0 {
		return hooksPrefix + "{token}" + rest[tail:]
	}
	return hooksPrefix + "{token}"
}

// LoggerFrom returns the request-scoped logger, or a discarding one.
func LoggerFrom(ctx context.Context) *slog.Logger {
	if log, ok := ctx.Value(loggerKey).(*slog.Logger); ok {
		return log
	}
	return discard
}

// Recover turns a panicking handler into a 500 with the standard envelope and
// logs the stack. A panic after the response started cannot be rewritten, so
// the connection is dropped instead of emitting a corrupt body.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec, ok := w.(*statusRecorder)
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if v == http.ErrAbortHandler { //nolint:errorlint // sentinel is compared by identity
				panic(v)
			}
			LoggerFrom(r.Context()).ErrorContext(r.Context(), "panic serving request",
				"error", v,
				"method", r.Method,
				"path", redactPath(r.URL.Path),
				"stack", string(debug.Stack()),
			)
			if ok && rec.wroteHeader {
				panic(http.ErrAbortHandler)
			}
			WriteError(w, r, http.StatusInternalServerError, CodeInternal,
				"The server hit an unexpected error.")
		}()
		next.ServeHTTP(w, r)
	})
}

// LimitBody rejects request bodies larger than max bytes. Content-Length is
// trusted when present so an oversized upload is refused before it is read;
// otherwise the body is capped while streaming.
func LimitBody(max int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > max {
				WriteError(w, r, http.StatusRequestEntityTooLarge, CodePayloadTooLarge,
					"The request body is larger than the limit.")
				return
			}
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, max)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// statusRecorder captures the status and size of a response for the access log.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	written     int64
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(status int) {
	if s.wroteHeader {
		return
	}
	s.status = status
	s.wroteHeader = true
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wroteHeader {
		s.WriteHeader(http.StatusOK)
	}
	n, err := s.ResponseWriter.Write(b)
	s.written += int64(n)
	return n, err
}

// Unwrap lets net/http reach the underlying writer for Flush, Hijack, and
// ReadFrom via http.ResponseController.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

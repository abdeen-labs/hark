package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net/http"
	"strings"
	"time"
)

// timestampLayout is the API's one timestamp format: RFC 3339, UTC, always
// three fractional digits. Go's default marshalling drops trailing zeros, which
// would make the same instant render differently from one response to the next.
const timestampLayout = "2006-01-02T15:04:05.000Z"

// Timestamp renders a time in the API's canonical form. Every timestamp in
// every response body is one of these.
type Timestamp time.Time

// MarshalJSON renders t as an RFC 3339 UTC string with millisecond precision.
func (t Timestamp) MarshalJSON() ([]byte, error) {
	b := make([]byte, 0, len(timestampLayout)+2)
	b = append(b, '"')
	b = time.Time(t).UTC().AppendFormat(b, timestampLayout)
	return append(b, '"'), nil
}

// UnmarshalJSON parses any RFC 3339 timestamp, which is what the stored Live
// Activity state documents round-trip through.
func (t *Timestamp) UnmarshalJSON(b []byte) error {
	var raw string
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return err
	}
	*t = Timestamp(parsed.UTC())
	return nil
}

// Time returns the underlying instant.
func (t Timestamp) Time() time.Time { return time.Time(t) }

// TimestampPtr renders an optional time, mapping a nil input to JSON null.
func TimestampPtr(t *time.Time) *Timestamp {
	if t == nil {
		return nil
	}
	ts := Timestamp(*t)
	return &ts
}

// decodeJSON reads the request body into v, writing the error response itself
// and reporting whether the handler should continue.
//
// Unknown fields are rejected. A client that misspells a field name gets told
// so instead of silently having it ignored, which is the failure mode that
// costs the most time to diagnose; the cost is that adding a request field is a
// change clients must be ready for, which the contract already says.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if !hasJSONContentType(r) {
		WriteError(w, r, http.StatusUnsupportedMediaType, CodeUnsupportedMedia,
			"Send this request with Content-Type: application/json.")
		return false
	}

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(v); err != nil {
		var maxBytes *http.MaxBytesError
		switch {
		case errors.Is(err, io.EOF):
			WriteError(w, r, http.StatusBadRequest, CodeBadRequest,
				"The request body is required and must be a JSON object.")
		case errors.As(err, &maxBytes):
			WriteError(w, r, http.StatusRequestEntityTooLarge, CodePayloadTooLarge,
				"The request body is larger than the limit.")
		default:
			WriteError(w, r, http.StatusBadRequest, CodeBadRequest,
				"The request body is not valid JSON: "+err.Error())
		}
		return false
	}

	// A body of "{} {}" is a client bug worth reporting rather than silently
	// honouring the first half.
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		WriteError(w, r, http.StatusBadRequest, CodeBadRequest,
			"The request body must contain exactly one JSON object.")
		return false
	}
	return true
}

// optional distinguishes the three states a PATCH field can be in: absent,
// present with a value, and present as null.
//
// A plain pointer conflates the first two, which is exactly the distinction a
// partial update turns on — "leave the image alone" and "remove the image" are
// different requests, and both are legitimate.
type optional[T any] struct {
	value T
	set   bool
}

// UnmarshalJSON records that the field appeared, whatever it held.
func (o *optional[T]) UnmarshalJSON(b []byte) error {
	o.set = true
	return json.Unmarshal(b, &o.value)
}

// Get returns the value and whether the field was present.
func (o optional[T]) Get() (T, bool) { return o.value, o.set }

// IsSet reports whether the field was present.
func (o optional[T]) IsSet() bool { return o.set }

// maxDurationSeconds is the largest value that still fits a time.Duration.
const maxDurationSeconds = int64(math.MaxInt64) / int64(time.Second)

// parseSeconds converts an optional duration field, writing the error response
// itself and reporting whether the handler should continue. A nil field yields
// a zero duration, which each endpoint documents the meaning of.
//
// The range is checked before the multiplication, not after: a large enough
// number of seconds overflows time.Duration and would land back inside the
// range every caller-facing ceiling is expressed in.
func parseSeconds(w http.ResponseWriter, r *http.Request, field string, seconds *int64) (time.Duration, bool) {
	if seconds == nil {
		return 0, true
	}
	if *seconds <= 0 || *seconds > maxDurationSeconds {
		WriteFieldErrors(w, r, "The request body is invalid.", []FieldError{{
			Field:   field,
			Message: "must be a positive number of seconds",
		}})
		return 0, false
	}
	return time.Duration(*seconds) * time.Second, true
}

func hasJSONContentType(r *http.Request) bool {
	raw := r.Header.Get("Content-Type")
	if raw == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(raw)
	return err == nil && strings.EqualFold(mediaType, "application/json")
}

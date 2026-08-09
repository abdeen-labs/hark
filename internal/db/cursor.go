package db

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cursor is a position in a list ordered by (timestamp DESC, id DESC).
//
// Every paginated endpoint uses keyset pagination rather than an offset: the
// lists it pages over are append-mostly, and an offset would silently skip or
// repeat rows whenever something is inserted between two requests. The
// timestamp alone is not unique enough — a fan-out writes several rows in the
// same millisecond — so the id breaks ties, which works because ids are UUIDv7
// and therefore sort in creation order.
//
// The encoded form is opaque to clients: it is deliberately not documented as
// parseable and its layout may change.
type Cursor struct {
	// Time is the ordering timestamp of the last item on the previous page:
	// created_at for most lists, responded_at for answered interactions.
	Time time.Time
	// ID is that item's identifier. For the activity feed it is the composite
	// "<kind>:<uuid>" id, which is what the feed orders by.
	ID string
}

// MaxCursorIDLen bounds the id half of a cursor. It exists so a hostile client
// cannot make the server allocate or compare an unbounded string; every real
// id is a UUID, or a short kind prefix plus a UUID.
const MaxCursorIDLen = 64

// ErrInvalidCursor reports a cursor that did not come from [Cursor.String].
var ErrInvalidCursor = errors.New("db: malformed pagination cursor")

// IsZero reports whether c addresses the start of the list.
func (c Cursor) IsZero() bool { return c.ID == "" && c.Time.IsZero() }

// String renders the opaque form: unpadded base64url of
// "<unix milliseconds>.<id>". Millisecond resolution matches what the API
// exposes and what the store writes, so a cursor never falls between two
// representable timestamps.
func (c Cursor) String() string {
	if c.IsZero() {
		return ""
	}
	raw := strconv.FormatInt(Millis(c.Time).UnixMilli(), 10) + "." + c.ID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// ParseCursor decodes the form produced by [Cursor.String]. The empty string
// decodes to the zero Cursor, so "no cursor supplied" needs no special case at
// the call site.
func ParseCursor(s string) (Cursor, error) {
	if s == "" {
		return Cursor{}, nil
	}
	if len(s) > 2*MaxCursorIDLen+32 {
		return Cursor{}, fmt.Errorf("%w: too long", ErrInvalidCursor)
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}
	millis, id, found := strings.Cut(string(raw), ".")
	if !found {
		return Cursor{}, fmt.Errorf("%w: missing separator", ErrInvalidCursor)
	}
	ms, err := strconv.ParseInt(millis, 10, 64)
	if err != nil {
		return Cursor{}, fmt.Errorf("%w: bad timestamp", ErrInvalidCursor)
	}
	if id == "" || len(id) > MaxCursorIDLen {
		return Cursor{}, fmt.Errorf("%w: bad id", ErrInvalidCursor)
	}
	for i := range len(id) {
		if c := id[i]; c < 0x20 || c > 0x7e {
			return Cursor{}, fmt.Errorf("%w: bad id", ErrInvalidCursor)
		}
	}
	return Cursor{Time: time.UnixMilli(ms).UTC(), ID: id}, nil
}

// args expands a cursor into the two parameters every paginated query takes.
// A zero cursor becomes (NULL, "") and the query's
// "$n::timestamptz IS NULL OR ..." predicate drops out.
func (c Cursor) args() (*time.Time, string) {
	if c.IsZero() {
		return nil, ""
	}
	t := Millis(c.Time)
	return &t, c.ID
}

// Page is one page of a keyset-paginated list.
type Page[T any] struct {
	Items []T
	// Next is the cursor for the following page, or the zero Cursor when this
	// page is the last one.
	Next Cursor
}

// HasMore reports whether another page exists.
func (p Page[T]) HasMore() bool { return !p.Next.IsZero() }

// paginate trims an over-fetched slice to limit and derives the next cursor
// from the last surviving item. Every list query asks for limit+1 rows: that is
// what makes "is there a next page" answerable without a second count query.
func paginate[T any](items []T, limit int, key func(T) Cursor) Page[T] {
	if limit <= 0 || len(items) <= limit {
		return Page[T]{Items: items}
	}
	items = items[:limit]
	return Page[T]{Items: items, Next: key(items[len(items)-1])}
}

// DefaultPageSize and MaxPageSize bound every paginated list.
const (
	DefaultPageSize = 20
	MaxPageSize     = 100
)

// ClampLimit applies the shared page-size bounds. A non-positive limit means
// "unspecified" and yields the default.
func ClampLimit(limit int) int {
	switch {
	case limit <= 0:
		return DefaultPageSize
	case limit > MaxPageSize:
		return MaxPageSize
	default:
		return limit
	}
}

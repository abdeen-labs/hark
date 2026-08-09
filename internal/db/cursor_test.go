package db

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCursorRoundTrip(t *testing.T) {
	want := Cursor{
		Time: time.Date(2026, 8, 9, 12, 34, 56, 789_000_000, time.UTC),
		ID:   "0198f3a1-2b4c-7d8e-9f01-23456789abcd",
	}
	encoded := want.String()
	if encoded == "" {
		t.Fatal("String() returned empty for a non-zero cursor")
	}
	if strings.ContainsAny(encoded, "+/=") {
		t.Errorf("encoding %q is not URL-safe and unpadded", encoded)
	}

	got, err := ParseCursor(encoded)
	if err != nil {
		t.Fatalf("ParseCursor(%q): %v", encoded, err)
	}
	if !got.Time.Equal(want.Time) {
		t.Errorf("Time = %s, want %s", got.Time, want.Time)
	}
	if got.ID != want.ID {
		t.Errorf("ID = %q, want %q", got.ID, want.ID)
	}
}

// A cursor is compared against stored timestamps, which are written truncated
// to milliseconds. Carrying sub-millisecond precision through the cursor would
// place it between two representable rows and drop one.
func TestCursorTruncatesToMilliseconds(t *testing.T) {
	c := Cursor{
		Time: time.Date(2026, 8, 9, 12, 34, 56, 789_999_999, time.UTC),
		ID:   "row",
	}
	got, err := ParseCursor(c.String())
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 8, 9, 12, 34, 56, 789_000_000, time.UTC)
	if !got.Time.Equal(want) {
		t.Errorf("Time = %s, want %s", got.Time, want)
	}
}

func TestCursorKeepsNonUTCInstants(t *testing.T) {
	zone := time.FixedZone("UTC+3", 3*60*60)
	local := time.Date(2026, 8, 9, 15, 0, 0, 0, zone)
	got, err := ParseCursor(Cursor{Time: local, ID: "row"}.String())
	if err != nil {
		t.Fatal(err)
	}
	if !got.Time.Equal(local) {
		t.Errorf("Time = %s, want the same instant as %s", got.Time, local)
	}
}

func TestCursorZeroValue(t *testing.T) {
	var zero Cursor
	if !zero.IsZero() {
		t.Error("zero Cursor reports IsZero() false")
	}
	if zero.String() != "" {
		t.Errorf("String() = %q, want empty", zero.String())
	}
	got, err := ParseCursor("")
	if err != nil {
		t.Fatalf("ParseCursor(\"\"): %v", err)
	}
	if !got.IsZero() {
		t.Error("empty string did not decode to the zero Cursor")
	}
	at, id := zero.args()
	if at != nil || id != "" {
		t.Errorf("args() = (%v, %q), want (nil, \"\")", at, id)
	}
}

func TestParseCursorRejectsMalformed(t *testing.T) {
	cases := map[string]string{
		"not base64":        "!!!!",
		"padded base64":     "MTIzNDU2Nzg5LmFiYw==",
		"no separator":      encodeRaw("1754743496789"),
		"empty id":          encodeRaw("1754743496789."),
		"non-numeric time":  encodeRaw("later.abc"),
		"empty time":        encodeRaw(".abc"),
		"control byte":      encodeRaw("1754743496789.a\x00b"),
		"oversized id":      encodeRaw("1754743496789." + strings.Repeat("a", MaxCursorIDLen+1)),
		"oversized encoded": strings.Repeat("A", 4096),
	}
	for name, encoded := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseCursor(encoded); !errors.Is(err, ErrInvalidCursor) {
				t.Errorf("ParseCursor(%q) error = %v, want ErrInvalidCursor", encoded, err)
			}
		})
	}
}

// encodeRaw builds the on-the-wire form directly so the test can feed
// ParseCursor payloads that Cursor.String would never produce.
func encodeRaw(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func TestPaginate(t *testing.T) {
	type row struct {
		at time.Time
		id string
	}
	key := func(r row) Cursor { return Cursor{Time: r.at, ID: r.id} }
	base := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
	rows := make([]row, 5)
	for i := range rows {
		rows[i] = row{at: base.Add(time.Duration(i) * time.Minute), id: string(rune('a' + i))}
	}

	t.Run("full page has a next cursor", func(t *testing.T) {
		page := paginate(rows, 4, key)
		if len(page.Items) != 4 {
			t.Fatalf("returned %d items, want 4", len(page.Items))
		}
		if !page.HasMore() {
			t.Fatal("HasMore() = false with an over-fetched row available")
		}
		// The cursor points at the last item returned, not at the extra row
		// that revealed there was more: the next page must resume from where
		// this one stopped.
		if page.Next.ID != "d" {
			t.Errorf("Next.ID = %q, want %q", page.Next.ID, "d")
		}
	})

	t.Run("short page has no next cursor", func(t *testing.T) {
		page := paginate(rows, 5, key)
		if len(page.Items) != 5 {
			t.Fatalf("returned %d items, want 5", len(page.Items))
		}
		if page.HasMore() {
			t.Errorf("HasMore() = true, want false (Next = %+v)", page.Next)
		}
	})

	t.Run("empty", func(t *testing.T) {
		page := paginate(nil, 10, key)
		if len(page.Items) != 0 || page.HasMore() {
			t.Errorf("got %+v, want an empty last page", page)
		}
	})
}

func TestClampLimit(t *testing.T) {
	for in, want := range map[int]int{
		0:               DefaultPageSize,
		-1:              DefaultPageSize,
		1:               1,
		MaxPageSize:     MaxPageSize,
		MaxPageSize + 1: MaxPageSize,
		1 << 20:         MaxPageSize,
	} {
		if got := ClampLimit(in); got != want {
			t.Errorf("ClampLimit(%d) = %d, want %d", in, got, want)
		}
	}
}

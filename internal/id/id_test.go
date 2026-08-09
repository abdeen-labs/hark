package id

import (
	"testing"
	"time"
)

func TestNewIsCanonicalV7(t *testing.T) {
	s := New()
	if len(s) != Len {
		t.Fatalf("len(%q) = %d, want %d", s, len(s), Len)
	}
	if !Valid(s) {
		t.Fatalf("Valid(%q) = false", s)
	}
	u, err := Parse(s)
	if err != nil {
		t.Fatalf("Parse(%q): %v", s, err)
	}
	if got := u.Version(); got != 7 {
		t.Errorf("version = %d, want 7", got)
	}
	if got := u[8] & 0xc0; got != 0x80 {
		t.Errorf("variant bits = %#x, want 0x80", got)
	}
	if s != u.String() {
		t.Errorf("round trip: %q != %q", s, u.String())
	}
}

func TestNewAtEmbedsTimestamp(t *testing.T) {
	want := time.Date(2026, 8, 9, 12, 34, 56, 789_000_000, time.UTC)
	got := NewAt(want).Time()
	if !got.Equal(want) {
		t.Errorf("Time() = %s, want %s", got, want)
	}
}

func TestIDsSortChronologically(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	prev := ""
	for i := range 50 {
		s := NewAt(base.Add(time.Duration(i) * time.Millisecond)).String()
		if prev >= s {
			t.Fatalf("id %d (%q) does not sort after %q", i, s, prev)
		}
		prev = s
	}
}

func TestNewIsUnique(t *testing.T) {
	seen := make(map[string]struct{}, 10_000)
	for range 10_000 {
		s := New()
		if _, dup := seen[s]; dup {
			t.Fatalf("duplicate id %q", s)
		}
		seen[s] = struct{}{}
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	for _, s := range []string{
		"",
		"not-a-uuid",
		"0198f3a12b4c7d8e9f0123456789abcd",                    // no hyphens
		"0198f3a1-2b4c-7d8e-9f01-23456789abc",                 // too short
		"0198f3a1-2b4c-7d8e-9f01-23456789abcdd",               // too long
		"0198f3a1-2b4c-7d8e-9f01-23456789abcg",                // non-hex
		"0198f3a1x2b4c-7d8e-9f01-23456789abcd",                // hyphen misplaced
		"0198f3a1-2b4c-7d8e-9f01-23456789abcd\x00",            // trailing NUL
		"  0198f3a1-2b4c-7d8e-9f01-23456789abcd",              // padded
		"0198F3A1-2B4C-7D8E-9F01-23456789ABCD-extra-garbage!", // long junk
	} {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) = nil error, want failure", s)
		}
	}
}

func TestValidRejectsOtherVersions(t *testing.T) {
	// A well-formed UUIDv4 parses but is not a valid Hark id.
	const v4 = "9f1c0ad4-b3e2-4a65-90cc-48d2ee7b3a41"
	if _, err := Parse(v4); err != nil {
		t.Fatalf("Parse(%q): %v", v4, err)
	}
	if Valid(v4) {
		t.Errorf("Valid(%q) = true, want false", v4)
	}
}

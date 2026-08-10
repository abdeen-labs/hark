package id

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNewIsCanonicalV7(t *testing.T) {
	s := New()
	if len(s) != Len {
		t.Fatalf("len(New()) = %d, want %d", len(s), Len)
	}
	if s != strings.ToLower(s) {
		t.Errorf("New() = %q, want lowercase", s)
	}
	if !Valid(s) {
		t.Errorf("Valid(New()) = false for %q", s)
	}
}

func TestNewEmbedsTheWallClock(t *testing.T) {
	before := time.Now().Add(-time.Second)
	u, err := uuid.Parse(New())
	if err != nil {
		t.Fatalf("parse a fresh id: %v", err)
	}
	sec, nsec := u.Time().UnixTime()
	got := time.Unix(sec, nsec)
	if got.Before(before) || got.After(time.Now().Add(time.Second)) {
		t.Errorf("embedded time = %v, want about now", got)
	}
}

func TestNewDoesNotCollide(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for range 1000 {
		s := New()
		if seen[s] {
			t.Fatalf("New() repeated %q", s)
		}
		seen[s] = true
	}
}

func TestValid(t *testing.T) {
	canonical := New()
	tests := map[string]bool{
		canonical:                  true,
		strings.ToUpper(canonical): true, // hex case is forgiven; shape is not

		"":                                     false,
		strings.ReplaceAll(canonical, "-", ""): false, // bare hex
		"urn:uuid:" + canonical:                false, // URN form
		"{" + canonical + "}":                  false, // braces
		canonical[:Len-1]:                      false, // truncated
		canonical[:Len-1] + "g":                false, // not hex

		// The right shape but the wrong version.
		"0198f3a1-2b4c-4d8e-9f01-23456789abcd": false,
	}
	for in, want := range tests {
		if got := Valid(in); got != want {
			t.Errorf("Valid(%q) = %v, want %v", in, got, want)
		}
	}
}

// Package id generates and validates the identifiers used across the Hark API.
//
// Every entity id on the wire is a UUIDv7 (RFC 9562 §5.7) rendered in the
// canonical lowercase hyphenated form. UUIDv7 embeds a millisecond Unix
// timestamp in its high bits, so ids sort chronologically as text and index
// well as a PostgreSQL primary key.
package id

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Len is the length of the canonical textual form, e.g.
// "0198f3a1-2b4c-7d8e-9f01-23456789abcd".
const Len = 36

// UUID is a raw 128-bit identifier.
type UUID [16]byte

// New returns a fresh UUIDv7 for the current time.
func New() string { return NewAt(time.Now()).String() }

// NewAt returns a UUIDv7 whose embedded timestamp is t.
func NewAt(t time.Time) UUID {
	var u UUID
	// crypto/rand.Read is documented never to fail; it panics internally on a
	// broken system entropy source rather than returning an error.
	rand.Read(u[6:]) //nolint:errcheck // documented to always succeed

	ms := t.UTC().UnixMilli()
	if ms < 0 {
		ms = 0
	}
	u[0] = byte(ms >> 40)
	u[1] = byte(ms >> 32)
	u[2] = byte(ms >> 24)
	u[3] = byte(ms >> 16)
	u[4] = byte(ms >> 8)
	u[5] = byte(ms)

	u[6] = 0x70 | (u[6] & 0x0f) // version 7
	u[8] = 0x80 | (u[8] & 0x3f) // RFC 9562 variant
	return u
}

// String renders the canonical lowercase hyphenated form.
func (u UUID) String() string {
	var b [Len]byte
	hex.Encode(b[0:8], u[0:4])
	b[8] = '-'
	hex.Encode(b[9:13], u[4:6])
	b[13] = '-'
	hex.Encode(b[14:18], u[6:8])
	b[18] = '-'
	hex.Encode(b[19:23], u[8:10])
	b[23] = '-'
	hex.Encode(b[24:36], u[10:16])
	return string(b[:])
}

// Time returns the millisecond timestamp embedded in a UUIDv7.
func (u UUID) Time() time.Time {
	ms := int64(u[0])<<40 | int64(u[1])<<32 | int64(u[2])<<24 |
		int64(u[3])<<16 | int64(u[4])<<8 | int64(u[5])
	return time.UnixMilli(ms).UTC()
}

// ErrInvalid reports a string that is not a canonical UUID.
var ErrInvalid = errors.New("id: not a canonical UUID")

// Parse decodes the canonical hyphenated form. It accepts any UUID version so
// that callers can distinguish "malformed" from "wrong version"; use
// [UUID.Version] when the distinction matters.
func Parse(s string) (UUID, error) {
	var u UUID
	if len(s) != Len || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return u, fmt.Errorf("%w: %q", ErrInvalid, s)
	}
	var packed [32]byte
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '-' {
			continue
		}
		packed[n] = s[i]
		n++
	}
	if _, err := hex.Decode(u[:], packed[:]); err != nil {
		return UUID{}, fmt.Errorf("%w: %q", ErrInvalid, s)
	}
	return u, nil
}

// Version reports the UUID version nibble.
func (u UUID) Version() int { return int(u[6] >> 4) }

// Valid reports whether s is a canonical UUIDv7 string.
func Valid(s string) bool {
	u, err := Parse(s)
	return err == nil && u.Version() == 7
}

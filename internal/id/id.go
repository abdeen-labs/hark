// Package id generates and validates the identifiers used across the Hark API.
//
// Every entity id on the wire is a UUIDv7 (RFC 9562 §5.7) rendered in the
// canonical lowercase hyphenated form. UUIDv7 embeds a millisecond Unix
// timestamp in its high bits, so ids sort chronologically as text and index
// well as a PostgreSQL primary key.
//
// Generation is github.com/google/uuid's. What this package adds is the two
// promises the contract makes that uuid.Parse alone does not check: the
// version, and the canonical 36-character form — the library also accepts
// URNs, braces and bare hex, none of which are valid on this API.
package id

import "github.com/google/uuid"

// Len is the length of the canonical textual form, e.g.
// "0198f3a1-2b4c-7d8e-9f01-23456789abcd".
const Len = 36

// New returns a fresh UUIDv7 for the current time.
func New() string {
	u, err := uuid.NewV7()
	if err != nil {
		// Only a broken entropy source lands here, and crypto/rand already
		// treats that as fatal; panicking beats handing out a zero id.
		panic("id: generate a UUIDv7: " + err.Error())
	}
	return u.String()
}

// Valid reports whether s is a canonical UUIDv7 string. Hex case is forgiven,
// as it always was; shape is not.
func Valid(s string) bool {
	if len(s) != Len || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return false
	}
	u, err := uuid.Parse(s)
	return err == nil && u.Version() == 7
}

package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/crypto/argon2"
	"golang.org/x/text/unicode/norm"
)

// Password policy.
//
// NIST SP 800-63B asks for a generous minimum length and no composition rules:
// length is the only requirement worth enforcing. The maximum exists solely to
// bound the work one request can ask the hasher to do.
const (
	MinPasswordLength = 12
	MaxPasswordLength = 256
)

// Errors from the password primitives.
var (
	// ErrPasswordTooShort and ErrPasswordTooLong are the only policy failures.
	ErrPasswordTooShort = errors.New("auth: password is shorter than the minimum")
	ErrPasswordTooLong  = errors.New("auth: password is longer than the maximum")

	// ErrPasswordControl rejects control characters, which are always a paste
	// accident rather than a deliberate choice.
	ErrPasswordControl = errors.New("auth: password contains a control character")

	// ErrPasswordMismatch reports a correct-looking hash that the password does
	// not open.
	ErrPasswordMismatch = errors.New("auth: password does not match")

	// ErrInvalidHash reports a stored hash this build cannot parse. It is a
	// data problem, never a caller problem, and is never surfaced to a client.
	ErrInvalidHash = errors.New("auth: stored password hash is malformed")
)

// argonParams are the cost parameters of one hash. They are stored inside every
// encoded hash rather than assumed, so raising the cost later still verifies
// every password already on disk.
type argonParams struct {
	MemoryKiB uint32
	Time      uint32
	Lanes     uint8
	KeyLen    uint32
}

// currentParams is what new hashes are made with: RFC 9106 §4's second
// recommended Argon2id configuration (64 MiB, three passes, four lanes). It
// costs roughly a tenth of a second per attempt on commodity hardware, which is
// the point — this is the only credential in the system a human can guess.
var currentParams = argonParams{MemoryKiB: 64 * 1024, Time: 3, Lanes: 4, KeyLen: 32}

// saltLength is 16 bytes: enough that two accounts, or one account across two
// password changes, never share a salt.
const saltLength = 16

// HashPassword validates plaintext against the policy and returns a PHC-encoded
// Argon2id hash:
//
//	$argon2id$v=19$m=65536,t=3,p=4$<salt>$<key>
//
// Both trailing fields are unpadded standard base64, as the PHC string format
// specifies. The result is safe to store as-is and carries everything
// [VerifyPassword] needs.
func HashPassword(plaintext string) (string, error) {
	normalized, err := preparePassword(plaintext)
	if err != nil {
		return "", err
	}

	salt := make([]byte, saltLength)
	// crypto/rand.Read never returns an error; it panics on a broken entropy
	// source rather than handing back predictable bytes.
	rand.Read(salt) //nolint:errcheck // documented to always succeed

	key := deriveKey(normalized, salt, currentParams)
	return encodeHash(currentParams, salt, key), nil
}

// VerifyPassword reports whether plaintext opens encoded.
//
// It returns [ErrPasswordMismatch] for a wrong password and [ErrInvalidHash]
// for a hash it cannot parse; callers collapse both into
// [ErrInvalidCredentials] so the difference never reaches a client.
func VerifyPassword(encoded, plaintext string) error {
	params, salt, want, err := decodeHash(encoded)
	if err != nil {
		return err
	}

	got := deriveKey(normalizePassword(plaintext), salt, params)
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrPasswordMismatch
	}
	return nil
}

// NeedsRehash reports whether encoded was made with parameters weaker than the
// current ones. Sign-in checks it and transparently re-hashes, so raising the
// cost upgrades the stored hash on the next successful login instead of
// requiring a password change.
func NeedsRehash(encoded string) bool {
	params, salt, _, err := decodeHash(encoded)
	if err != nil {
		return true
	}
	return params != currentParams || len(salt) != saltLength
}

// ValidatePassword reports whether plaintext satisfies the policy. It measures
// the normalized form, so the length a user is told about is the length that is
// actually checked.
func ValidatePassword(plaintext string) error {
	_, err := preparePassword(plaintext)
	return err
}

func preparePassword(plaintext string) (string, error) {
	normalized := normalizePassword(plaintext)
	n := 0
	for _, r := range normalized {
		if unicode.IsControl(r) {
			return "", ErrPasswordControl
		}
		n++
	}
	switch {
	case n < MinPasswordLength:
		return "", ErrPasswordTooShort
	case n > MaxPasswordLength:
		return "", ErrPasswordTooLong
	}
	return normalized, nil
}

// normalizePassword applies the RFC 8265 OpaqueString profile: every Unicode
// space becomes U+0020, case is left alone, and the result is normalized to
// NFC.
//
// Without this, a password containing an accented character can hash
// differently depending on whether it was typed on a Mac (which composes) or
// pasted from a source that decomposes — the same keystrokes, a different hash.
func normalizePassword(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.Is(unicode.Zs, r) {
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(r)
	}
	return norm.NFC.String(b.String())
}

func deriveKey(normalized string, salt []byte, p argonParams) []byte {
	return argon2.IDKey([]byte(normalized), salt, p.Time, p.MemoryKiB, p.Lanes, p.KeyLen)
}

// b64 is the PHC string format's encoding: standard alphabet, no padding.
var b64 = base64.RawStdEncoding

func encodeHash(p argonParams, salt, key []byte) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.MemoryKiB, p.Time, p.Lanes,
		b64.EncodeToString(salt), b64.EncodeToString(key))
}

func decodeHash(encoded string) (argonParams, []byte, []byte, error) {
	var p argonParams

	// A PHC string starts with "$", so splitting yields an empty first field.
	fields := strings.Split(encoded, "$")
	if len(fields) != 6 || fields[0] != "" || fields[1] != "argon2id" {
		return p, nil, nil, ErrInvalidHash
	}

	var version int
	if _, err := fmt.Sscanf(fields[2], "v=%d", &version); err != nil || version != argon2.Version {
		return p, nil, nil, ErrInvalidHash
	}

	if complete, err := scanParams(fields[3], &p); err != nil || !complete {
		return p, nil, nil, ErrInvalidHash
	}

	salt, err := b64.DecodeString(fields[4])
	if err != nil || len(salt) == 0 {
		return p, nil, nil, ErrInvalidHash
	}
	key, err := b64.DecodeString(fields[5])
	if err != nil || len(key) == 0 {
		return p, nil, nil, ErrInvalidHash
	}

	p.KeyLen = uint32(len(key))
	return p, salt, key, nil
}

// scanParams reads the "m=…,t=…,p=…" field. It insists on all three being
// present rather than defaulting a missing one, because a hash whose cost is
// guessed is a hash that silently verifies against the wrong work factor.
func scanParams(field string, p *argonParams) (bool, error) {
	var seenM, seenT, seenP bool
	for _, part := range strings.Split(field, ",") {
		name, value, ok := strings.Cut(part, "=")
		if !ok {
			return false, ErrInvalidHash
		}
		n, err := strconv.ParseUint(value, 10, 32)
		if err != nil || n == 0 {
			return false, ErrInvalidHash
		}
		switch name {
		case "m":
			p.MemoryKiB, seenM = uint32(n), true
		case "t":
			p.Time, seenT = uint32(n), true
		case "p":
			if n > 255 {
				return false, ErrInvalidHash
			}
			p.Lanes, seenP = uint8(n), true
		default:
			return false, ErrInvalidHash
		}
	}
	return seenM && seenT && seenP, nil
}

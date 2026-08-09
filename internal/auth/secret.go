package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Secret prefixes. Every credential this deployment issues announces what it is
// in its first characters, so a value found in a log or a config file is
// immediately identifiable — and so the Authorization header parser can route a
// bearer credential to the right lookup without a database round trip.
//
// None of these is a prefix of another, so the order they are tested in does
// not matter.
const (
	// APITokenPrefix marks an agent bearer credential.
	APITokenPrefix = "hark_"
	// SessionTokenPrefix marks a session token, whether it arrives in the
	// cookie or in an Authorization header.
	SessionTokenPrefix = "harksess_"
	// DeviceCodePrefix marks the machine half of a device grant.
	DeviceCodePrefix = "harkdev_"
	// WebhookTokenPrefix marks a service's webhook credential. It is the one
	// secret that travels in a URL, so it is also the one that must never reach
	// a log sink.
	WebhookTokenPrefix = "harkhook_"
	// ResponseTokenPrefix marks the one-shot credential a push payload carries
	// so a phone can answer a question without a session.
	ResponseTokenPrefix = "harkresp_"
)

// secretBodyLength is the number of random characters after the prefix. At
// log2(62) ≈ 5.954 bits per character, 43 characters carry just over 256 bits.
const secretBodyLength = 43

// APITokenDisplayLength is how much of an API token secret is kept in the
// clear, in the `prefix` column and in every listing: the marker plus eight
// body characters. Enough to recognise which token a line is about, far too
// little to guess the rest.
const APITokenDisplayLength = len(APITokenPrefix) + 8

const base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// userCodeAlphabet is Crockford's base32: the digits and uppercase letters with
// I, L, O and U removed. The first three are removed because a human reading a
// code off a terminal confuses them with 1 and 0; U is removed so a random code
// cannot spell an unfortunate word.
const userCodeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// userCodeLength is 8 symbols — 40 bits. The code is only useful while a
// pairing request is pending, which lasts ten minutes and is rate limited, so
// 40 bits is far more than a guessing attack can cover.
const userCodeLength = 8

// NewSessionToken returns a fresh session secret.
func NewSessionToken() string {
	return SessionTokenPrefix + randomString(base62Alphabet, secretBodyLength)
}

// NewAPIToken returns a fresh agent bearer secret.
func NewAPIToken() string { return APITokenPrefix + randomString(base62Alphabet, secretBodyLength) }

// NewDeviceCode returns a fresh device-grant secret.
func NewDeviceCode() string { return DeviceCodePrefix + randomString(base62Alphabet, secretBodyLength) }

// NewWebhookToken returns a fresh webhook secret, the credential embedded in a
// service's ingest URL.
func NewWebhookToken() string {
	return WebhookTokenPrefix + randomString(base62Alphabet, secretBodyLength)
}

// NewResponseToken returns a fresh interaction response credential.
//
// It is minted with the question, travels only inside the push payload, and is
// stored as a digest — so answering from a notification action or a Lock Screen
// button needs no session, and a database dump grants no answers.
func NewResponseToken() string {
	return ResponseTokenPrefix + randomString(base62Alphabet, secretBodyLength)
}

// NewUserCode returns a fresh human-typed pairing code in its canonical
// `XXXX-XXXX` form.
func NewUserCode() string {
	raw := randomString(userCodeAlphabet, userCodeLength)
	return raw[:4] + "-" + raw[4:]
}

// APITokenDisplayPrefix returns the leading characters of secret that are kept
// for display. A secret shorter than that is returned whole, which can only
// happen for a value that will fail the shape check anyway.
func APITokenDisplayPrefix(secret string) string {
	if len(secret) <= APITokenDisplayLength {
		return secret
	}
	return secret[:APITokenDisplayLength]
}

// ValidSessionToken reports whether s has the shape of a session secret.
// Checking the shape first means a malformed value never reaches the database.
func ValidSessionToken(s string) bool { return validSecret(s, SessionTokenPrefix) }

// ValidAPIToken reports whether s has the shape of an agent secret.
func ValidAPIToken(s string) bool { return validSecret(s, APITokenPrefix) }

// ValidDeviceCode reports whether s has the shape of a device-grant secret.
func ValidDeviceCode(s string) bool { return validSecret(s, DeviceCodePrefix) }

// ValidWebhookToken reports whether s has the shape of a webhook secret.
func ValidWebhookToken(s string) bool { return validSecret(s, WebhookTokenPrefix) }

// ValidResponseToken reports whether s has the shape of a response credential.
func ValidResponseToken(s string) bool { return validSecret(s, ResponseTokenPrefix) }

func validSecret(s, prefix string) bool {
	if len(s) != len(prefix)+secretBodyLength || !strings.HasPrefix(s, prefix) {
		return false
	}
	for i := len(prefix); i < len(s); i++ {
		if !strings.ContainsRune(base62Alphabet, rune(s[i])) {
			return false
		}
	}
	return true
}

// NormalizeUserCode canonicalises a code a human typed, reporting whether the
// result is well formed.
//
// It accepts any spacing and any case, and applies Crockford's substitutions —
// I and L read as 1, O reads as 0 — so "abcd efgh", "ABCD-EFGH" and
// "abcdefgh" all resolve to the same lookup key.
func NormalizeUserCode(raw string) (string, bool) {
	var b strings.Builder
	b.Grow(userCodeLength)
	for _, r := range strings.ToUpper(raw) {
		switch r {
		case ' ', '\t', '\n', '\r', '-':
			continue
		case 'I', 'L':
			r = '1'
		case 'O':
			r = '0'
		}
		if !strings.ContainsRune(userCodeAlphabet, r) {
			return "", false
		}
		if b.Len() == userCodeLength {
			return "", false
		}
		b.WriteRune(r)
	}
	if b.Len() != userCodeLength {
		return "", false
	}
	s := b.String()
	return s[:4] + "-" + s[4:], true
}

// Digest domains. Every stored digest is salted by the kind of credential it
// belongs to, so a value read out of one table cannot be replayed against
// another even if the two ever hold the same secret.
const (
	domainSession       = "hark.session.v1"
	domainAPIToken      = "hark.api-token.v1"
	domainDeviceCode    = "hark.device-code.v1"
	domainWebhookToken  = "hark.webhook-token.v1"
	domainResponseToken = "hark.response-token.v1"
)

// SessionTokenHash returns the stored digest of a session secret.
func SessionTokenHash(token string) string { return digest(domainSession, token) }

// APITokenHash returns the stored digest of an agent secret.
func APITokenHash(token string) string { return digest(domainAPIToken, token) }

// DeviceCodeHash returns the stored digest of a device-grant secret.
func DeviceCodeHash(code string) string { return digest(domainDeviceCode, code) }

// WebhookTokenHash returns the stored digest of a webhook secret.
func WebhookTokenHash(token string) string { return digest(domainWebhookToken, token) }

// ResponseTokenHash returns the stored digest of an interaction response
// credential.
func ResponseTokenHash(token string) string { return digest(domainResponseToken, token) }

// digest is SHA-256 over the domain, a NUL separator, and the secret, rendered
// as lowercase hex.
//
// A bare hash is the right construction here and a KDF would not be: these
// secrets carry 256 bits of uniform randomness, so there is nothing for an
// attacker to grind. The NUL is what makes the domain unambiguous — no domain
// contains one, so no two (domain, secret) pairs can produce the same input.
func digest(domain, secret string) string {
	h := sha256.New()
	h.Write([]byte(domain))
	h.Write([]byte{0})
	h.Write([]byte(secret))
	return hex.EncodeToString(h.Sum(nil))
}

// randomString returns n characters drawn uniformly from alphabet.
//
// Bytes at or above the largest multiple of the alphabet size are discarded
// rather than folded in, because taking a plain modulus would make the first
// few symbols measurably more likely than the rest.
func randomString(alphabet string, n int) string {
	size := len(alphabet)
	if size < 2 || size > 256 {
		panic("auth: alphabet must hold between 2 and 256 symbols")
	}
	limit := 256 - (256 % size) // == 256 when size is a power of two: nothing is discarded

	out := make([]byte, 0, n)
	buf := make([]byte, n+n/4+8) // headroom so one read almost always suffices
	for len(out) < n {
		rand.Read(buf) //nolint:errcheck // documented to always succeed
		for _, b := range buf {
			if int(b) >= limit {
				continue
			}
			out = append(out, alphabet[int(b)%size])
			if len(out) == n {
				break
			}
		}
	}
	return string(out)
}

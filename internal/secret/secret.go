// Package secret holds the two constructions the API needs beyond hashing:
// reversible encryption for credentials the server must replay, and detached
// capability tokens for callers that hold no credential at all.
//
// Everything is derived from one root key (HARK_SECRET_KEY), so rotating it
// invalidates every stored ciphertext and every outstanding capability at once
// — which is the point of having a single root.
//
// Hashing is not here. A credential Hark only ever *verifies* is stored as a
// digest by internal/auth; this package is for the ones it has to hand back
// (a webhook URL the owner re-copies) or hand on (an ActivityKit push token it
// must present to Apple).
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// Purposes. Each one derives its own encryption key, so a ciphertext lifted out
// of one column cannot be decrypted as another — a webhook token pasted into a
// device's push-to-start column is unreadable rather than usable.
const (
	// PurposeWebhookToken protects the plaintext webhook token, so a service's
	// URL can be shown to its owner again.
	PurposeWebhookToken = "webhook-token"
	// PurposeActivityToken protects ActivityKit push tokens: a device's
	// push-to-start token and a delivery's update token.
	PurposeActivityToken = "live-activity-token"
	// PurposeCallbackToken protects the bearer token a webhook caller asked Hark
	// to present when it posts an answer back.
	PurposeCallbackToken = "callback-token"

	// PurposeActivityRegistration signs the capability a start push carries, and
	// which the phone presents to report the activity's update token.
	PurposeActivityRegistration = "live-activity-registration"
)

// ErrInvalid reports a ciphertext or capability that this key cannot read.
// Callers treat it as "not found" rather than surfacing it: a caller who
// presents an unreadable value learns nothing from being told why.
var ErrInvalid = errors.New("secret: value cannot be read with this key")

// envelopeVersion prefixes every ciphertext. It exists so the format can change
// without guessing what an old value is.
const envelopeVersion = "v1"

// Keeper derives per-purpose keys from one root secret. It holds no mutable
// state and is safe to share.
type Keeper struct {
	root []byte
}

// NewKeeper returns a Keeper over root, which must be non-empty.
func NewKeeper(root []byte) *Keeper {
	if len(root) == 0 {
		panic("secret: the root key is required")
	}
	return &Keeper{root: append([]byte(nil), root...)}
}

// Encrypt seals plaintext for a purpose.
//
// The result is `v1.<base64url nonce>.<base64url sealed>`: printable, so it
// stores in a text column, and self-describing enough that a future format can
// be told apart from this one. A fresh random nonce per call means encrypting
// the same token twice produces different ciphertexts, which is what stops the
// column from revealing that two rows hold the same secret.
func (k *Keeper) Encrypt(purpose, plaintext string) (string, error) {
	gcm, err := k.aead(purpose)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("secret: read random nonce: %w", err)
	}
	sealed := gcm.Seal(nil, nonce, []byte(plaintext), []byte(purpose))
	return envelopeVersion + "." + b64(nonce) + "." + b64(sealed), nil
}

// Decrypt opens a ciphertext produced by [Keeper.Encrypt] for the same purpose.
// Anything else — a different purpose, a different root key, a truncated or
// edited value — is [ErrInvalid].
func (k *Keeper) Decrypt(purpose, ciphertext string) (string, error) {
	version, rest, ok := strings.Cut(ciphertext, ".")
	if !ok || version != envelopeVersion {
		return "", fmt.Errorf("%w: unknown envelope", ErrInvalid)
	}
	rawNonce, rawSealed, ok := strings.Cut(rest, ".")
	if !ok || strings.Contains(rawSealed, ".") {
		return "", fmt.Errorf("%w: malformed envelope", ErrInvalid)
	}
	nonce, err := unb64(rawNonce)
	if err != nil {
		return "", fmt.Errorf("%w: malformed nonce", ErrInvalid)
	}
	sealed, err := unb64(rawSealed)
	if err != nil {
		return "", fmt.Errorf("%w: malformed ciphertext", ErrInvalid)
	}

	gcm, err := k.aead(purpose)
	if err != nil {
		return "", err
	}
	if len(nonce) != gcm.NonceSize() {
		return "", fmt.Errorf("%w: wrong nonce size", ErrInvalid)
	}
	plaintext, err := gcm.Open(nil, nonce, sealed, []byte(purpose))
	if err != nil {
		return "", fmt.Errorf("%w: authentication failed", ErrInvalid)
	}
	return string(plaintext), nil
}

// Sign returns a detached capability over parts for a purpose.
//
// It is how a caller with no credential proves it was told something: the phone
// reporting an ActivityKit update token holds nothing but what the start push
// gave it, and this token says "the server issued this, for exactly this
// delivery, until exactly this instant". Nothing is stored, so nothing has to
// be cleaned up when it lapses.
//
// The parts are joined with a NUL, which no part may contain, so no two
// different part lists can produce the same message.
func (k *Keeper) Sign(purpose string, parts ...string) string {
	mac := hmac.New(sha256.New, k.derive(purpose))
	mac.Write([]byte(purpose))
	for _, part := range parts {
		mac.Write([]byte{0})
		mac.Write([]byte(part))
	}
	return b64(mac.Sum(nil))
}

// Verify reports whether token is the capability [Keeper.Sign] would produce.
// The comparison is constant time, so a caller cannot learn a valid token one
// character at a time.
func (k *Keeper) Verify(purpose, token string, parts ...string) bool {
	want := k.Sign(purpose, parts...)
	return len(token) == len(want) && hmac.Equal([]byte(token), []byte(want))
}

// aead builds the cipher for a purpose.
func (k *Keeper) aead(purpose string) (cipher.AEAD, error) {
	block, err := aes.NewCipher(k.derive(purpose))
	if err != nil {
		return nil, fmt.Errorf("secret: build cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secret: build AEAD: %w", err)
	}
	return gcm, nil
}

// derive returns the 32-byte key for a purpose.
//
// SHA-256 over the purpose, a NUL, and the root is the right construction: the
// root is required to carry real entropy, so there is nothing for an attacker
// to grind and a slow KDF would buy nothing.
func (k *Keeper) derive(purpose string) []byte {
	h := sha256.New()
	h.Write([]byte(purpose))
	h.Write([]byte{0})
	h.Write(k.root)
	return h.Sum(nil)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func unb64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

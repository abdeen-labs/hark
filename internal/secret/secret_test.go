package secret

import (
	"errors"
	"strings"
	"testing"
)

func newTestKeeper() *Keeper { return NewKeeper([]byte("a-root-key-with-enough-entropy-1234")) }

func TestEncryptRoundTrips(t *testing.T) {
	k := newTestKeeper()

	sealed, err := k.Encrypt(PurposeWebhookToken, "harkhook_secret")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if strings.Contains(sealed, "harkhook_secret") {
		t.Fatalf("the ciphertext contains the plaintext: %q", sealed)
	}

	got, err := k.Decrypt(PurposeWebhookToken, sealed)
	if err != nil || got != "harkhook_secret" {
		t.Fatalf("decrypt = %q, %v; want the plaintext back", got, err)
	}
}

// TestEncryptIsNotDeterministic is why a fresh nonce is drawn per call: a
// deterministic ciphertext would let anyone with the column see which two rows
// hold the same secret.
func TestEncryptIsNotDeterministic(t *testing.T) {
	k := newTestKeeper()

	first, err := k.Encrypt(PurposeActivityToken, "0a1b2c")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	second, err := k.Encrypt(PurposeActivityToken, "0a1b2c")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if first == second {
		t.Error("encrypting the same value twice produced the same ciphertext")
	}
}

// TestPurposesAreSeparate is the reason each purpose derives its own key: a
// ciphertext lifted out of one column must be unreadable in another, so a
// mix-up is a loud failure rather than a quiet one.
func TestPurposesAreSeparate(t *testing.T) {
	k := newTestKeeper()

	sealed, err := k.Encrypt(PurposeWebhookToken, "secret")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := k.Decrypt(PurposeCallbackToken, sealed); !errors.Is(err, ErrInvalid) {
		t.Errorf("decrypting under another purpose gave %v, want ErrInvalid", err)
	}

	other := NewKeeper([]byte("a-different-root-key-1234567890abc"))
	if _, err := other.Decrypt(PurposeWebhookToken, sealed); !errors.Is(err, ErrInvalid) {
		t.Errorf("decrypting with another root key gave %v, want ErrInvalid", err)
	}
}

func TestMalformedCiphertextIsRejected(t *testing.T) {
	k := newTestKeeper()

	sealed, err := k.Encrypt(PurposeWebhookToken, "secret")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	parts := strings.Split(sealed, ".")

	tests := map[string]string{
		"empty":             "",
		"no version":        parts[1] + "." + parts[2],
		"unknown version":   "v2." + parts[1] + "." + parts[2],
		"missing part":      "v1." + parts[1],
		"extra part":        sealed + ".extra",
		"bad base64":        "v1.!!!." + parts[2],
		"edited ciphertext": "v1." + parts[1] + "." + flipFirst(parts[2]),
	}
	for name, value := range tests {
		if _, err := k.Decrypt(PurposeWebhookToken, value); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

// TestSignBindsEveryPart is what makes a capability a capability: it grants one
// delivery, of one activity, until one instant, and nothing else.
func TestSignBindsEveryPart(t *testing.T) {
	k := newTestKeeper()

	token := k.Sign(PurposeActivityRegistration, "lad_1", "act_1", "1754743496789")
	if !k.Verify(PurposeActivityRegistration, token, "lad_1", "act_1", "1754743496789") {
		t.Fatal("a freshly signed capability does not verify")
	}

	tampered := [][]string{
		{"lad_2", "act_1", "1754743496789"},
		{"lad_1", "act_2", "1754743496789"},
		{"lad_1", "act_1", "1754743496790"},
		{"lad_1", "act_1"},
		{"lad_1", "act_1", "1754743496789", "extra"},
	}
	for _, parts := range tampered {
		if k.Verify(PurposeActivityRegistration, token, parts...) {
			t.Errorf("capability verified against %v", parts)
		}
	}

	if k.Verify(PurposeWebhookToken, token, "lad_1", "act_1", "1754743496789") {
		t.Error("capability verified under another purpose")
	}
	if k.Verify(PurposeActivityRegistration, "", "lad_1", "act_1", "1754743496789") {
		t.Error("an empty capability verified")
	}
}

// TestSignedPartsCannotBeReshuffled covers the reason parts are NUL-separated:
// without it, ("ab","c") and ("a","bc") would produce the same message.
func TestSignedPartsCannotBeReshuffled(t *testing.T) {
	k := newTestKeeper()

	if k.Sign(PurposeActivityRegistration, "ab", "c") == k.Sign(PurposeActivityRegistration, "a", "bc") {
		t.Error("two different part lists produced the same capability")
	}
}

// flipFirst changes one character of a base64url segment.
//
// It edits the first rather than the last on purpose. The final character of an
// unpadded encoding can carry unused low bits, which Go's decoder ignores, so
// changing it sometimes yields the very same bytes and the tampering under test
// never happens. The first character always carries six significant bits.
func flipFirst(s string) string {
	if s == "" {
		return "A"
	}
	if s[0] == 'A' {
		return "B" + s[1:]
	}
	return "A" + s[1:]
}

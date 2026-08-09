package auth

import (
	"errors"
	"strings"
	"testing"
)

const goodPassword = "correct horse battery staple"

func TestHashPasswordRoundTrips(t *testing.T) {
	encoded, err := HashPassword(goodPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	if !strings.HasPrefix(encoded, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Errorf("encoded hash does not carry the current parameters: %s", encoded)
	}
	if strings.Contains(encoded, goodPassword) {
		t.Error("the encoded hash contains the plaintext")
	}
	if err := VerifyPassword(encoded, goodPassword); err != nil {
		t.Errorf("VerifyPassword(correct password) = %v, want nil", err)
	}
}

func TestHashPasswordIsSalted(t *testing.T) {
	first, err := HashPassword(goodPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	second, err := HashPassword(goodPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	if first == second {
		t.Error("hashing the same password twice produced identical output, so the salt is not random")
	}
	// Both must still verify: a salt that is not stored alongside the key is
	// worse than no salt at all.
	if err := VerifyPassword(second, goodPassword); err != nil {
		t.Errorf("second hash does not verify: %v", err)
	}
}

func TestVerifyPasswordRejectsWrongPassword(t *testing.T) {
	encoded, err := HashPassword(goodPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	for _, wrong := range []string{
		"correct horse battery stapl",
		"correct horse battery staple ",
		"Correct horse battery staple",
		"",
	} {
		if err := VerifyPassword(encoded, wrong); !errors.Is(err, ErrPasswordMismatch) {
			t.Errorf("VerifyPassword(%q) = %v, want ErrPasswordMismatch", wrong, err)
		}
	}
}

// TestVerifyPasswordRejectsTamperedHash covers the shapes an attacker with
// write access to the row would try: flipping a key byte, swapping the salt,
// and weakening the cost parameters so a precomputed key matches.
func TestVerifyPasswordRejectsTamperedHash(t *testing.T) {
	encoded, err := HashPassword(goodPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	fields := strings.Split(encoded, "$")

	flip := func(s string) string {
		b := []byte(s)
		if b[0] == 'A' {
			b[0] = 'B'
		} else {
			b[0] = 'A'
		}
		return string(b)
	}

	tampered := map[string]string{
		"flipped key byte":  strings.Join([]string{fields[0], fields[1], fields[2], fields[3], fields[4], flip(fields[5])}, "$"),
		"flipped salt byte": strings.Join([]string{fields[0], fields[1], fields[2], fields[3], flip(fields[4]), fields[5]}, "$"),
		"weakened cost":     strings.Join([]string{fields[0], fields[1], fields[2], "m=8,t=1,p=1", fields[4], fields[5]}, "$"),
		"truncated key":     strings.Join([]string{fields[0], fields[1], fields[2], fields[3], fields[4], fields[5][:20]}, "$"),
	}
	for name, hash := range tampered {
		if err := VerifyPassword(hash, goodPassword); err == nil {
			t.Errorf("%s: VerifyPassword accepted a tampered hash", name)
		}
	}
}

func TestVerifyPasswordRejectsMalformedHash(t *testing.T) {
	for name, encoded := range map[string]string{
		"empty":            "",
		"not phc":          "deadbeef:cafebabe",
		"wrong algorithm":  "$argon2i$v=19$m=65536,t=3,p=4$c2FsdHNhbHRzYWx0$a2V5",
		"wrong version":    "$argon2id$v=16$m=65536,t=3,p=4$c2FsdHNhbHRzYWx0$a2V5",
		"missing lanes":    "$argon2id$v=19$m=65536,t=3$c2FsdHNhbHRzYWx0$a2V5",
		"unknown param":    "$argon2id$v=19$m=65536,t=3,p=4,x=9$c2FsdHNhbHRzYWx0$a2V5",
		"zero cost":        "$argon2id$v=19$m=65536,t=0,p=4$c2FsdHNhbHRzYWx0$a2V5",
		"bad base64 salt":  "$argon2id$v=19$m=65536,t=3,p=4$!!!!$a2V5",
		"empty salt field": "$argon2id$v=19$m=65536,t=3,p=4$$a2V5",
	} {
		if err := VerifyPassword(encoded, goodPassword); !errors.Is(err, ErrInvalidHash) {
			t.Errorf("%s: VerifyPassword = %v, want ErrInvalidHash", name, err)
		}
	}
}

func TestPasswordPolicy(t *testing.T) {
	tests := map[string]struct {
		password string
		want     error
	}{
		"at the minimum":  {strings.Repeat("a", MinPasswordLength), nil},
		"below minimum":   {strings.Repeat("a", MinPasswordLength-1), ErrPasswordTooShort},
		"at the maximum":  {strings.Repeat("a", MaxPasswordLength), nil},
		"above maximum":   {strings.Repeat("a", MaxPasswordLength+1), ErrPasswordTooLong},
		"empty":           {"", ErrPasswordTooShort},
		"embedded newlin": {"hunter2hunter2\n", ErrPasswordControl},
		"embedded tab":    {"hunter2\thunter2", ErrPasswordControl},
	}
	for name, tc := range tests {
		if err := ValidatePassword(tc.password); !errors.Is(err, tc.want) {
			t.Errorf("%s: ValidatePassword = %v, want %v", name, err, tc.want)
		}
	}
}

// TestPasswordLengthCountsCharacters checks that the policy counts characters
// rather than bytes, so a short passphrase in a non-Latin script is not
// accidentally accepted (or a long one rejected) on byte count.
func TestPasswordLengthCountsCharacters(t *testing.T) {
	// Eleven characters, thirty-three bytes: under the minimum either way you
	// count, but only the character count says so for the right reason.
	short := strings.Repeat("あ", MinPasswordLength-1)
	if err := ValidatePassword(short); !errors.Is(err, ErrPasswordTooShort) {
		t.Errorf("ValidatePassword(%d multibyte characters) = %v, want ErrPasswordTooShort", MinPasswordLength-1, err)
	}
	long := strings.Repeat("あ", MinPasswordLength)
	if err := ValidatePassword(long); err != nil {
		t.Errorf("ValidatePassword(%d multibyte characters) = %v, want nil", MinPasswordLength, err)
	}
}

// TestPasswordNormalizationIsStable is the reason normalization exists: the
// same passphrase typed on a platform that composes accents and one that
// decomposes them must open the same hash.
func TestPasswordNormalizationIsStable(t *testing.T) {
	const (
		composed   = "un caf\u00e9 tr\u00e8s fort"      // precomposed \u00e9 and \u00e8
		decomposed = "un cafe\u0301 tre\u0300s fort"    // e plus a combining accent
		nbsp       = "un\u00a0caf\u00e9 tr\u00e8s fort" // a non-breaking space
	)

	encoded, err := HashPassword(composed)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if err := VerifyPassword(encoded, decomposed); err != nil {
		t.Errorf("the decomposed form does not open the composed form's hash: %v", err)
	}
	if err := VerifyPassword(encoded, nbsp); err != nil {
		t.Errorf("a non-breaking space was not folded to an ordinary one: %v", err)
	}

	// Folding must not go so far that a different passphrase verifies.
	if err := VerifyPassword(encoded, "un cafe tres fort"); err == nil {
		t.Error("normalization stripped the accents entirely")
	}
}

func TestNeedsRehash(t *testing.T) {
	encoded, err := HashPassword(goodPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if NeedsRehash(encoded) {
		t.Error("a freshly made hash reports that it needs rehashing")
	}

	weaker := strings.Replace(encoded, "m=65536,t=3,p=4", "m=16384,t=2,p=1", 1)
	if !NeedsRehash(weaker) {
		t.Error("a hash made with weaker parameters does not report that it needs rehashing")
	}
	if !NeedsRehash("not a hash at all") {
		t.Error("an unparseable hash does not report that it needs rehashing")
	}
}

func BenchmarkHashPassword(b *testing.B) {
	for b.Loop() {
		if _, err := HashPassword(goodPassword); err != nil {
			b.Fatal(err)
		}
	}
}

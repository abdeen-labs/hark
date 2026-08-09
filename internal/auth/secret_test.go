package auth

import (
	"strings"
	"testing"
)

func TestGeneratedSecretsHaveTheRightShape(t *testing.T) {
	tests := map[string]struct {
		generate func() string
		valid    func(string) bool
		prefix   string
	}{
		"session token": {NewSessionToken, ValidSessionToken, SessionTokenPrefix},
		"api token":     {NewAPIToken, ValidAPIToken, APITokenPrefix},
		"device code":   {NewDeviceCode, ValidDeviceCode, DeviceCodePrefix},
	}
	for name, tc := range tests {
		secret := tc.generate()
		if !strings.HasPrefix(secret, tc.prefix) {
			t.Errorf("%s: %q does not start with %q", name, secret, tc.prefix)
		}
		if got, want := len(secret), len(tc.prefix)+secretBodyLength; got != want {
			t.Errorf("%s: length = %d, want %d", name, got, want)
		}
		if !tc.valid(secret) {
			t.Errorf("%s: a freshly generated secret failed its own shape check: %q", name, secret)
		}
	}
}

// TestSecretKindsDoNotCrossValidate is what lets the Authorization header
// parser pick a lookup from the prefix alone: no kind may accept another's
// secret.
func TestSecretKindsDoNotCrossValidate(t *testing.T) {
	session, token, device := NewSessionToken(), NewAPIToken(), NewDeviceCode()

	checks := map[string]bool{
		"session accepts api token":  ValidSessionToken(token),
		"session accepts device":     ValidSessionToken(device),
		"api token accepts session":  ValidAPIToken(session),
		"api token accepts device":   ValidAPIToken(device),
		"device accepts session":     ValidDeviceCode(session),
		"device accepts api token":   ValidDeviceCode(token),
		"api token accepts no marks": ValidAPIToken(strings.TrimPrefix(token, APITokenPrefix)),
	}
	for name, accepted := range checks {
		if accepted {
			t.Errorf("%s", name)
		}
	}
}

func TestSecretsAreUnique(t *testing.T) {
	const draws = 2000
	seen := make(map[string]struct{}, draws)
	for range draws {
		secret := NewAPIToken()
		if _, dup := seen[secret]; dup {
			t.Fatalf("generated the same secret twice in %d draws: %q", draws, secret)
		}
		seen[secret] = struct{}{}
	}
}

func TestValidSecretRejectsMalformed(t *testing.T) {
	good := NewAPIToken()
	body := strings.TrimPrefix(good, APITokenPrefix)

	for name, candidate := range map[string]string{
		"empty":          "",
		"prefix only":    APITokenPrefix,
		"one short":      APITokenPrefix + body[:len(body)-1],
		"one long":       good + "a",
		"uppercase mark": strings.ToUpper(APITokenPrefix) + body,
		"non-base62":     APITokenPrefix + body[:len(body)-1] + "-",
		"whitespace":     APITokenPrefix + body[:len(body)-1] + " ",
	} {
		if ValidAPIToken(candidate) {
			t.Errorf("%s: ValidAPIToken(%q) = true", name, candidate)
		}
	}
}

// TestRandomStringCoversItsAlphabet is a coarse smoke test for the rejection
// sampling: over enough draws every symbol must appear. A modulus bug that
// skewed the distribution would still pass, but one that silently truncated the
// alphabet would not.
func TestRandomStringCoversItsAlphabet(t *testing.T) {
	for _, alphabet := range []string{base62Alphabet, userCodeAlphabet} {
		seen := make(map[rune]int, len(alphabet))
		for _, r := range randomString(alphabet, 200*len(alphabet)) {
			seen[r]++
		}
		for _, want := range alphabet {
			if seen[want] == 0 {
				t.Errorf("alphabet %q: symbol %q never appeared in %d draws",
					alphabet, want, 200*len(alphabet))
			}
		}
		if len(seen) != len(alphabet) {
			t.Errorf("alphabet %q: produced %d distinct symbols, want %d", alphabet, len(seen), len(alphabet))
		}
	}
}

func TestNewUserCodeIsCanonical(t *testing.T) {
	for range 100 {
		code := NewUserCode()
		if len(code) != userCodeLength+1 || code[4] != '-' {
			t.Fatalf("user code %q is not XXXX-XXXX", code)
		}
		normalized, ok := NormalizeUserCode(code)
		if !ok || normalized != code {
			t.Fatalf("NormalizeUserCode(%q) = %q, %v; want it unchanged and valid", code, normalized, ok)
		}
	}
}

func TestNormalizeUserCode(t *testing.T) {
	const want = "K7QM-3XPD"
	for name, input := range map[string]string{
		"canonical":       "K7QM-3XPD",
		"lowercase":       "k7qm-3xpd",
		"no hyphen":       "K7QM3XPD",
		"spaced":          "  K7QM 3XPD  ",
		"hyphens all out": "K-7-Q-M-3-X-P-D",
		"tabbed":          "K7QM\t3XPD",
	} {
		got, ok := NormalizeUserCode(input)
		if !ok || got != want {
			t.Errorf("%s: NormalizeUserCode(%q) = %q, %v; want %q, true", name, input, got, ok, want)
		}
	}

	// Crockford's confusable substitutions: what a human reads as a letter, the
	// alphabet stores as a digit.
	for input, want := range map[string]string{
		"IIII-IIII": "1111-1111",
		"llll-LLLL": "1111-1111",
		"oooo-OOOO": "0000-0000",
	} {
		if got, ok := NormalizeUserCode(input); !ok || got != want {
			t.Errorf("NormalizeUserCode(%q) = %q, %v; want %q, true", input, got, ok, want)
		}
	}

	for name, input := range map[string]string{
		"empty":        "",
		"too short":    "K7QM-3XP",
		"too long":     "K7QM-3XPDD",
		"not in set":   "K7QM-3XPU",
		"punctuation":  "K7QM_3XPD",
		"non-ascii":    "K7QM-3XPÐ",
		"only hyphens": "--------",
	} {
		if got, ok := NormalizeUserCode(input); ok {
			t.Errorf("%s: NormalizeUserCode(%q) = %q, true; want false", name, input, got)
		}
	}
}

// TestDigestsAreDomainSeparated is the property that stops a value read out of
// one table from being replayed against another.
func TestDigestsAreDomainSeparated(t *testing.T) {
	const secret = "the same bytes in every table"

	session := SessionTokenHash(secret)
	token := APITokenHash(secret)
	device := DeviceCodeHash(secret)

	if session == token || session == device || token == device {
		t.Fatalf("two credential kinds digest the same secret identically:\n%s\n%s\n%s",
			session, token, device)
	}
	for name, got := range map[string]string{"session": session, "api token": token, "device": device} {
		if len(got) != 64 {
			t.Errorf("%s digest is %d characters, want 64 hex characters", name, len(got))
		}
		if strings.ContainsAny(got, "ABCDEF") {
			t.Errorf("%s digest is not lowercase hex: %s", name, got)
		}
		if got != APITokenHash(secret) && got != SessionTokenHash(secret) && got != DeviceCodeHash(secret) {
			t.Errorf("%s digest is not deterministic", name)
		}
	}
}

func TestAPITokenDisplayPrefix(t *testing.T) {
	secret := NewAPIToken()

	got := APITokenDisplayPrefix(secret)
	if len(got) != APITokenDisplayLength {
		t.Errorf("display prefix = %q (%d characters), want %d", got, len(got), APITokenDisplayLength)
	}
	if !strings.HasPrefix(secret, got) {
		t.Errorf("display prefix %q is not a prefix of %q", got, secret)
	}
	// The point of a display prefix is that it is not the secret.
	if got == secret {
		t.Error("the display prefix is the whole secret")
	}
}

package apns

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// testKey generates a P-256 key and returns it with its PKCS#8 PEM encoding,
// which is the form Apple issues a .p8 auth key in.
func testKey(t *testing.T) (*ecdsa.PrivateKey, []byte) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshalling the key: %v", err)
	}
	return key, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// decodeSegment reads one base64url JWT segment into v.
func decodeSegment(t *testing.T, segment string, v any) {
	t.Helper()

	raw, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		t.Fatalf("decoding %q: %v", segment, err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("unmarshalling %q: %v", raw, err)
	}
}

// TestProviderTokenShape pins the JWT Apple accepts: an ES256 signature over a
// header naming the key and a claim set naming the team and the moment.
func TestProviderTokenShape(t *testing.T) {
	key, pemKey := testKey(t)
	issued := time.Date(2026, 8, 9, 12, 0, 0, 500_000_000, time.UTC)

	source, err := newTokenSource("ABCDE12345", "TEAM123456", pemKey, 0, func() time.Time { return issued })
	if err != nil {
		t.Fatalf("newTokenSource: %v", err)
	}
	token, err := source.get()
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d segments, want 3: %q", len(parts), token)
	}

	var header map[string]any
	decodeSegment(t, parts[0], &header)
	want := map[string]any{"alg": "ES256", "kid": "ABCDE12345"}
	if len(header) != len(want) {
		t.Errorf("header = %v, want exactly %v", header, want)
	}
	for k, v := range want {
		if header[k] != v {
			t.Errorf("header[%q] = %v, want %v", k, header[k], v)
		}
	}

	var claims map[string]any
	decodeSegment(t, parts[1], &claims)
	if len(claims) != 2 {
		t.Errorf("claims = %v, want exactly iss and iat", claims)
	}
	if claims["iss"] != "TEAM123456" {
		t.Errorf("iss = %v, want TEAM123456", claims["iss"])
	}
	// iat is whole seconds, floored — never a fraction, and never rounded up
	// past the moment the token was actually minted.
	if iat, ok := claims["iat"].(float64); !ok || int64(iat) != issued.Unix() {
		t.Errorf("iat = %v, want %d", claims["iat"], issued.Unix())
	}

	// The signature must verify against the public key, which also proves it is
	// the raw 64-byte r||s form: a DER signature would not parse here, and APNs
	// would reject it.
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decoding the signature: %v", err)
	}
	if len(signature) != 64 {
		t.Errorf("signature is %d bytes, want 64 (raw r||s, not DER)", len(signature))
	}
	if err := jwt.SigningMethodES256.Verify(parts[0]+"."+parts[1], signature, &key.PublicKey); err != nil {
		t.Errorf("verifying the signature: %v", err)
	}
}

// TestProviderTokenCaching checks the refresh cadence: one token is reused
// until it ages out, and the boundary is exclusive.
func TestProviderTokenCaching(t *testing.T) {
	_, pemKey := testKey(t)
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)

	source, err := newTokenSource("ABCDE12345", "TEAM123456", pemKey, 0, func() time.Time { return now })
	if err != nil {
		t.Fatalf("newTokenSource: %v", err)
	}

	first, err := source.get()
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	// One second before the TTL the same token comes back, even though the
	// clock has moved: minting per request is what Apple rate limits.
	now = now.Add(DefaultTokenTTL - time.Second)
	if again, _ := source.get(); again != first {
		t.Error("a token inside its TTL was replaced")
	}

	// At exactly the TTL it is stale.
	now = now.Add(time.Second)
	second, err := source.get()
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if second == first {
		t.Error("a token at its TTL was reused")
	}

	// A clock that jumps backwards must not resurrect the cache.
	now = now.Add(-2 * DefaultTokenTTL)
	if third, _ := source.get(); third == second {
		t.Error("a token was reused across a backwards clock jump")
	}
}

// TestProviderTokenInvalidate covers the one refresh that is not the clock's:
// Apple saying the credential expired. A second rejection carrying the token
// that was already replaced must not throw the replacement away.
func TestProviderTokenInvalidate(t *testing.T) {
	_, pemKey := testKey(t)
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)

	source, err := newTokenSource("ABCDE12345", "TEAM123456", pemKey, 0, func() time.Time { return now })
	if err != nil {
		t.Fatalf("newTokenSource: %v", err)
	}

	first, _ := source.get()
	source.invalidate(first)

	now = now.Add(time.Second) // so the new token differs in its iat
	second, _ := source.get()
	if second == first {
		t.Fatal("invalidate did not drop the refused token")
	}

	source.invalidate(first) // a late rejection of the old token
	if again, _ := source.get(); again != second {
		t.Error("a late rejection dropped the replacement token")
	}
}

// TestProviderTokenConcurrent is a race-detector test: every send in a fan-out
// asks for the same token at the same moment.
func TestProviderTokenConcurrent(t *testing.T) {
	_, pemKey := testKey(t)
	source, err := newTokenSource("ABCDE12345", "TEAM123456", pemKey, 0, time.Now)
	if err != nil {
		t.Fatalf("newTokenSource: %v", err)
	}

	tokens := make([]string, 32)
	var wg sync.WaitGroup
	for i := range tokens {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := source.get()
			if err != nil {
				t.Errorf("get: %v", err)
			}
			tokens[i] = token
		}()
	}
	wg.Wait()

	for i, token := range tokens {
		if token != tokens[0] {
			t.Fatalf("goroutine %d got a different token; the cache is not shared", i)
		}
	}
}

func TestParsePrivateKeyRejectsWrongKeys(t *testing.T) {
	wrongCurve, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(wrongCurve)
	if err != nil {
		t.Fatalf("marshalling the key: %v", err)
	}

	tests := map[string][]byte{
		"not a PEM block": []byte("ABCDEF"),
		"not a key":       pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("nope")}),
		"wrong curve":     pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}),
	}
	for name, key := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := parsePrivateKey(key); err == nil {
				t.Error("parsePrivateKey accepted a key it should have refused")
			}
		})
	}
}

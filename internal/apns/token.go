package apns

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// DefaultTokenTTL is how long one provider JWT is reused.
//
// Apple accepts a provider token for one hour after its iat, and rejects a
// provider that mints them too often. Neither extreme works: a token per
// request is rate limited, and a token per hour expires while the last requests
// are still using it. Fifty minutes sits inside the window with ten minutes of
// margin for clock drift, which is what makes a background refresh
// unnecessary.
const DefaultTokenTTL = 50 * time.Minute

// tokenSource mints and caches the ES256 provider token.
//
// One token serves the whole process — every alert and every Live Activity —
// because Apple authenticates the provider, not the connection.
type tokenSource struct {
	keyID  string
	teamID string
	key    *ecdsa.PrivateKey
	ttl    time.Duration
	now    func() time.Time

	mu     sync.Mutex
	token  string
	issued time.Time
}

func newTokenSource(keyID, teamID string, pemKey []byte, ttl time.Duration, now func() time.Time) (*tokenSource, error) {
	key, err := parsePrivateKey(pemKey)
	if err != nil {
		return nil, err
	}
	if ttl <= 0 {
		ttl = DefaultTokenTTL
	}
	if now == nil {
		now = time.Now
	}
	return &tokenSource{keyID: keyID, teamID: teamID, key: key, ttl: ttl, now: now}, nil
}

// parsePrivateKey reads the .p8 auth key.
//
// Apple issues these as PKCS#8 PEM over the P-256 curve. Anything else is
// rejected here rather than at the first push, where it would look like a
// delivery failure instead of the configuration error it is.
func parsePrivateKey(pemKey []byte) (*ecdsa.PrivateKey, error) {
	key, err := jwt.ParseECPrivateKeyFromPEM(pemKey)
	if err != nil {
		return nil, fmt.Errorf("apns: parse the APNs auth key: %w", err)
	}
	if key.Curve != elliptic.P256() {
		return nil, errors.New("apns: the APNs auth key must be an ES256 (P-256) key")
	}
	return key, nil
}

// get returns the cached token, minting a new one when it has aged out.
func (s *tokenSource) get() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	if age := now.Sub(s.issued); s.token != "" && age >= 0 && age < s.ttl {
		return s.token, nil
	}

	token, err := s.mint(now)
	if err != nil {
		return "", err
	}
	s.token, s.issued = token, now
	return token, nil
}

// invalidate drops the cache when it still holds the token Apple refused.
//
// The comparison matters: several goroutines can be in flight against one
// token, and the second rejection to arrive must not throw away the replacement
// the first one already minted.
func (s *tokenSource) invalidate(stale string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token == stale {
		s.token, s.issued = "", time.Time{}
	}
}

// mint signs one provider token.
//
// The shape is Apple's, exactly: an ES256 JWT whose header carries the key id
// and whose claims are the team id and the moment it was issued. There is no
// exp — APNs derives expiry from iat — and no aud, sub or jti.
func (s *tokenSource) mint(now time.Time) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss": s.teamID,
		"iat": now.Unix(),
	})
	token.Header = map[string]any{"alg": "ES256", "kid": s.keyID}

	signed, err := token.SignedString(s.key)
	if err != nil {
		return "", fmt.Errorf("apns: sign the provider token: %w", err)
	}
	return signed, nil
}

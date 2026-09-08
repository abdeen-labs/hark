package db

import (
	"context"
	"time"
)

// OAuthClient is a dynamically registered OAuth client (RFC 7591).
type OAuthClient struct {
	ID           string    `db:"id"`
	Name         string    `db:"name"`
	RedirectURIs []string  `db:"redirect_uris"`
	ClientURI    *string   `db:"client_uri"`
	LogoURI      *string   `db:"logo_uri"`
	CreatedAt    time.Time `db:"created_at"`
	// LastUsedAt is stamped when one of the client's codes is exchanged. NULL
	// means the registration never completed an authorization.
	LastUsedAt *time.Time `db:"last_used_at"`
}

// OAuthClients stores registered OAuth clients.
type OAuthClients struct{ q Querier }

const oauthClientColumns = `id, name, redirect_uris, client_uri, logo_uri, created_at, last_used_at`

// CreateOAuthClientParams registers a client.
type CreateOAuthClientParams struct {
	ID           string
	Name         string
	RedirectURIs []string
	ClientURI    *string
	LogoURI      *string
	Now          time.Time
}

// Create inserts a registration.
func (s *OAuthClients) Create(ctx context.Context, p CreateOAuthClientParams) (*OAuthClient, error) {
	const q = `
		INSERT INTO oauth_clients (id, name, redirect_uris, client_uri, logo_uri, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING ` + oauthClientColumns
	return queryOne[OAuthClient](ctx, s.q, "create OAuth client", q,
		p.ID, p.Name, p.RedirectURIs, p.ClientURI, p.LogoURI, Millis(p.Now))
}

// ByID resolves a registered client_id.
func (s *OAuthClients) ByID(ctx context.Context, id string) (*OAuthClient, error) {
	const q = `SELECT ` + oauthClientColumns + ` FROM oauth_clients WHERE id = $1`
	return queryOne[OAuthClient](ctx, s.q, "load OAuth client", q, id)
}

// TouchLastUsed records that the client completed an authorization, returning
// [ErrNotFound] when the registration is gone.
func (s *OAuthClients) TouchLastUsed(ctx context.Context, id string, now time.Time) error {
	const q = `UPDATE oauth_clients SET last_used_at = $2 WHERE id = $1`
	return execOne(ctx, s.q, "touch OAuth client", q, id, Millis(now))
}

func (s *OAuthClients) Purge(ctx context.Context, now time.Time, unusedFor, idleFor time.Duration) (int64, error) {
	const q = `
		DELETE FROM oauth_clients
		WHERE (last_used_at IS NULL AND created_at <= $1)
		   OR last_used_at <= $2`
	return execAffected(ctx, s.q, "purge OAuth clients", q,
		Millis(now.Add(-unusedFor)), Millis(now.Add(-idleFor)))
}

// OAuthCode is an authorization code awaiting exchange, or already spent.
type OAuthCode struct {
	ID         string `db:"id"`
	CodeHash   string `db:"code_hash"`
	ClientID   string `db:"client_id"`
	ClientName string `db:"client_name"`
	// ClientLogoURI is the client's logo as it was at consent, kept with the
	// code because a registration may be purged before its code is exchanged.
	ClientLogoURI *string  `db:"client_logo_uri"`
	UserID        string   `db:"user_id"`
	RedirectURI   string   `db:"redirect_uri"`
	Scopes        []string `db:"scopes"`
	CodeChallenge string   `db:"code_challenge"`
	Resource      *string  `db:"resource"`
	// ExpiresAt is the code's own TTL. The token it issues has none.
	ExpiresAt time.Time `db:"expires_at"`
	// ConsumedAt is set by the guarded consume. A spent code is kept until the
	// purge so a second presentation is told it was used rather than unknown.
	ConsumedAt *time.Time `db:"consumed_at"`
	CreatedAt  time.Time  `db:"created_at"`
}

// OAuthCodes stores authorization codes.
type OAuthCodes struct{ q Querier }

const oauthCodeColumns = `id, code_hash, client_id, client_name, client_logo_uri, user_id, redirect_uri, scopes,
	code_challenge, resource, expires_at, consumed_at, created_at`

// CreateOAuthCodeParams records an approval.
type CreateOAuthCodeParams struct {
	ID            string
	CodeHash      string
	ClientID      string
	ClientName    string
	ClientLogoURI *string
	UserID        string
	RedirectURI   string
	Scopes        []string
	CodeChallenge string
	Resource      *string
	ExpiresAt     time.Time
	Now           time.Time
}

// Create inserts a code.
func (s *OAuthCodes) Create(ctx context.Context, p CreateOAuthCodeParams) (*OAuthCode, error) {
	const q = `
		INSERT INTO oauth_authorization_codes
			(id, code_hash, client_id, client_name, client_logo_uri, user_id, redirect_uri, scopes,
			 code_challenge, resource, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING ` + oauthCodeColumns
	return queryOne[OAuthCode](ctx, s.q, "create OAuth code", q,
		p.ID, p.CodeHash, p.ClientID, p.ClientName, p.ClientLogoURI, p.UserID, p.RedirectURI, p.Scopes,
		p.CodeChallenge, p.Resource, Millis(p.ExpiresAt), Millis(p.Now))
}

// ByCodeHash resolves a presented code. Spent and expired codes are returned
// rather than filtered out; the caller's guarded [OAuthCodes.Consume] decides.
func (s *OAuthCodes) ByCodeHash(ctx context.Context, hash string) (*OAuthCode, error) {
	const q = `SELECT ` + oauthCodeColumns + ` FROM oauth_authorization_codes WHERE code_hash = $1`
	return queryOne[OAuthCode](ctx, s.q, "load OAuth code", q, hash)
}

// Consume marks a code as spent, which is what authorises issuing the token.
func (s *OAuthCodes) Consume(ctx context.Context, id string, now time.Time) (bool, error) {
	const q = `
		UPDATE oauth_authorization_codes SET consumed_at = $2
		WHERE id = $1 AND consumed_at IS NULL AND expires_at > $2`
	return execMatched(ctx, s.q, "consume OAuth code", q, id, Millis(now))
}

// Purge deletes codes that expired at or before `before`, spent or not.
func (s *OAuthCodes) Purge(ctx context.Context, before time.Time) (int64, error) {
	const q = `DELETE FROM oauth_authorization_codes WHERE expires_at <= $1`
	return execAffected(ctx, s.q, "purge OAuth codes", q, Millis(before))
}

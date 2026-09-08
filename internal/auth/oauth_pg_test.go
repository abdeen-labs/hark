package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/id"
)

const registeredRedirectURI = "https://claude.ai/api/mcp/auth_callback"

func registerClient(t *testing.T, ctx context.Context, s *Service) *db.OAuthClient {
	t.Helper()
	client, err := s.RegisterOAuthClient(ctx, RegisterOAuthClientParams{
		Name:                    "  Claude  ",
		RedirectURIs:            []string{registeredRedirectURI, registeredRedirectURI, "http://127.0.0.1/cb"},
		ClientURI:               "https://claude.ai",
		GrantTypes:              []string{"authorization_code"},
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: "none",
		Scope:                   "notifications:send devices:read",
	})
	if err != nil {
		t.Fatalf("RegisterOAuthClient: %v", err)
	}
	return client
}

// approve runs consent and approval for a registered client and returns the
// code the browser would carry back.
func approve(t *testing.T, ctx context.Context, s *Service, clientID, redirectURI, scope, verifier, userID string) string {
	t.Helper()
	consent, err := s.OAuthConsent(ctx, OAuthAuthorizationRequest{
		ResponseType:        "code",
		ClientID:            clientID,
		RedirectURI:         redirectURI,
		Scope:               scope,
		State:               "state-1",
		CodeChallenge:       OAuthCodeChallenge(verifier),
		CodeChallengeMethod: "S256",
		Resource:            testResource,
	}, testResource)
	if err != nil {
		t.Fatalf("OAuthConsent: %v", err)
	}
	code, err := s.ApproveOAuth(ctx, consent, userID)
	if err != nil {
		t.Fatalf("ApproveOAuth: %v", err)
	}
	return code
}

func exchange(t *testing.T, ctx context.Context, s *Service, code, clientID, redirectURI, verifier string) (*OAuthTokenGrant, *OAuthTokenError) {
	t.Helper()
	grant, err := s.ExchangeOAuthCode(ctx, ExchangeOAuthCodeParams{
		Code:             code,
		ClientID:         clientID,
		RedirectURI:      redirectURI,
		CodeVerifier:     verifier,
		Resource:         testResource,
		ExpectedResource: testResource,
	})
	if err == nil {
		return grant, nil
	}
	var tokenErr *OAuthTokenError
	if !errors.As(err, &tokenErr) {
		t.Fatalf("ExchangeOAuthCode = %v, want an OAuthTokenError or a grant", err)
	}
	return nil, tokenErr
}

func TestOAuthEndToEnd(t *testing.T) {
	ctx, service, clock := requireService(t)
	user := seedAccount(t, ctx, service)

	client := registerClient(t, ctx, service)
	if !id.Valid(client.ID) {
		t.Errorf("client id %q is not a UUIDv7", client.ID)
	}
	if client.Name != "Claude" {
		t.Errorf("Name = %q, want it trimmed", client.Name)
	}
	if want := []string{registeredRedirectURI, "http://127.0.0.1/cb"}; !equalStrings(client.RedirectURIs, want) {
		t.Errorf("RedirectURIs = %v, want %v deduplicated in order", client.RedirectURIs, want)
	}
	if client.ClientURI == nil || *client.ClientURI != "https://claude.ai" || client.LastUsedAt != nil {
		t.Errorf("client = %+v", client)
	}

	resolved, err := service.OAuthClientByID(ctx, client.ID)
	if err != nil {
		t.Fatalf("OAuthClientByID: %v", err)
	}
	if resolved.MetadataDocument || resolved.Name != "Claude" || !equalStrings(resolved.RedirectURIs, client.RedirectURIs) {
		t.Errorf("resolved = %+v, want the registration", resolved)
	}

	verifier := strings.Repeat("v", 64)
	code := approve(t, ctx, service, client.ID, registeredRedirectURI, "notifications:send interactions:create", verifier, user.ID)
	if !ValidOAuthCode(code) {
		t.Fatalf("code %q does not have the expected shape", code)
	}
	stored, err := service.store.OAuthCodes.ByCodeHash(ctx, OAuthCodeHash(code))
	if err != nil {
		t.Fatalf("the approval left no row: %v", err)
	}
	if stored.CodeHash == code || stored.ClientName != "Claude" || stored.UserID != user.ID {
		t.Errorf("stored code = %+v", stored)
	}
	if want := clock.Now().Add(OAuthCodeTTL); !stored.ExpiresAt.Equal(db.Millis(want)) {
		t.Errorf("code expiry = %s, want %s", stored.ExpiresAt, want)
	}

	clock.Advance(time.Minute)
	grant, tokenErr := exchange(t, ctx, service, code, client.ID, registeredRedirectURI, verifier)
	if tokenErr != nil {
		t.Fatalf("ExchangeOAuthCode = %s: %s", tokenErr.Code, tokenErr.Description)
	}
	if !ValidAPIToken(grant.Secret) {
		t.Errorf("issued secret %q does not have the API-token shape", grant.Secret)
	}
	if grant.Token.Name != "Claude" {
		t.Errorf("token name = %q, want the client name", grant.Token.Name)
	}
	if want := []string{"interactions:create", "notifications:send"}; !equalStrings(grant.Scopes, want) || !equalStrings(grant.Token.Scopes, want) {
		t.Errorf("scopes = %v / %v, want %v", grant.Scopes, grant.Token.Scopes, want)
	}
	if grant.Token.ExpiresAt != nil {
		t.Errorf("token expiry = %v, want none: an OAuth-issued token lasts until revoked", grant.Token.ExpiresAt)
	}

	principal, err := service.AuthenticateAPIToken(ctx, grant.Secret)
	if err != nil {
		t.Fatalf("the OAuth-granted token does not authenticate: %v", err)
	}
	if principal.UserID() != user.ID || !principal.HasScopes("interactions:create", "notifications:send") || principal.HasScope("devices:read") {
		t.Errorf("principal = %+v", principal)
	}

	touched, err := service.store.OAuthClients.ByID(ctx, client.ID)
	if err != nil || touched.LastUsedAt == nil {
		t.Errorf("the registration's last use was not stamped: %+v, %v", touched, err)
	}

	// A code issues exactly one token.
	if _, tokenErr := exchange(t, ctx, service, code, client.ID, registeredRedirectURI, verifier); tokenErr == nil || tokenErr.Code != OAuthErrInvalidGrant {
		t.Errorf("second exchange = %+v, want invalid_grant", tokenErr)
	}
	// The token is listed like any other, under the client's name.
	tokens, err := service.ListAPITokens(ctx, user.ID)
	if err != nil || len(tokens) != 1 || tokens[0].ID != grant.Token.ID {
		t.Errorf("ListAPITokens = %+v, %v; want the granted token", tokens, err)
	}
}

func TestOAuthExchangeRefusals(t *testing.T) {
	ctx, service, clock := requireService(t)
	user := seedAccount(t, ctx, service)
	client := registerClient(t, ctx, service)
	other := registerClient(t, ctx, service)
	verifier := strings.Repeat("v", 43)

	// A wrong verifier spends the code: the right one no longer works.
	code := approve(t, ctx, service, client.ID, registeredRedirectURI, "", verifier, user.ID)
	if _, tokenErr := exchange(t, ctx, service, code, client.ID, registeredRedirectURI, strings.Repeat("w", 43)); tokenErr == nil || tokenErr.Code != OAuthErrInvalidGrant || tokenErr.Status != 400 {
		t.Errorf("wrong verifier = %+v, want 400 invalid_grant", tokenErr)
	}
	if _, tokenErr := exchange(t, ctx, service, code, client.ID, registeredRedirectURI, verifier); tokenErr == nil || tokenErr.Code != OAuthErrInvalidGrant {
		t.Errorf("exchange after a failed verifier = %+v, want invalid_grant: the code was not spent", tokenErr)
	}

	// Another registered client cannot redeem it, and trying spends it.
	code = approve(t, ctx, service, client.ID, registeredRedirectURI, "", verifier, user.ID)
	if _, tokenErr := exchange(t, ctx, service, code, other.ID, registeredRedirectURI, verifier); tokenErr == nil || tokenErr.Code != OAuthErrInvalidGrant {
		t.Errorf("other client = %+v, want invalid_grant", tokenErr)
	}
	if _, tokenErr := exchange(t, ctx, service, code, client.ID, registeredRedirectURI, verifier); tokenErr == nil || tokenErr.Code != OAuthErrInvalidGrant {
		t.Errorf("exchange after another client's attempt = %+v, want invalid_grant", tokenErr)
	}

	// A client Hark does not know is just another client the code was not
	// issued to, and trying spends it.
	code = approve(t, ctx, service, client.ID, registeredRedirectURI, "", verifier, user.ID)
	if _, tokenErr := exchange(t, ctx, service, code, id.New(), registeredRedirectURI, verifier); tokenErr == nil || tokenErr.Code != OAuthErrInvalidGrant || tokenErr.Status != 400 {
		t.Errorf("unknown client = %+v, want 400 invalid_grant", tokenErr)
	}
	if _, tokenErr := exchange(t, ctx, service, code, client.ID, registeredRedirectURI, verifier); tokenErr == nil || tokenErr.Code != OAuthErrInvalidGrant {
		t.Errorf("exchange after an unknown client's attempt = %+v, want invalid_grant: the code was not spent", tokenErr)
	}

	// The redirect URI and the resource must be the ones the request carried.
	code = approve(t, ctx, service, client.ID, registeredRedirectURI, "", verifier, user.ID)
	if _, tokenErr := exchange(t, ctx, service, code, client.ID, "http://127.0.0.1/cb", verifier); tokenErr == nil || tokenErr.Code != OAuthErrInvalidGrant {
		t.Errorf("other redirect = %+v, want invalid_grant", tokenErr)
	}
	code = approve(t, ctx, service, client.ID, registeredRedirectURI, "", verifier, user.ID)
	if _, err := service.ExchangeOAuthCode(ctx, ExchangeOAuthCodeParams{
		Code: code, ClientID: client.ID, RedirectURI: registeredRedirectURI, CodeVerifier: verifier,
		Resource: "https://other.example/mcp", ExpectedResource: testResource,
	}); err == nil {
		t.Error("a foreign resource was accepted")
	} else if tokenErr := new(OAuthTokenError); !errors.As(err, &tokenErr) || tokenErr.Code != OAuthErrInvalidGrant {
		t.Errorf("foreign resource = %v, want invalid_grant", err)
	}

	// A token request may omit the resource an authorization request named.
	code = approve(t, ctx, service, client.ID, registeredRedirectURI, "", verifier, user.ID)
	if _, err := service.ExchangeOAuthCode(ctx, ExchangeOAuthCodeParams{
		Code: code, ClientID: client.ID, RedirectURI: registeredRedirectURI, CodeVerifier: verifier,
		ExpectedResource: testResource,
	}); err != nil {
		t.Errorf("exchange without a resource = %v, want the grant", err)
	}

	code = approve(t, ctx, service, client.ID, "http://127.0.0.1:51234/cb", "", verifier, user.ID)
	if _, tokenErr := exchange(t, ctx, service, code, client.ID, "http://127.0.0.1:51235/cb", verifier); tokenErr == nil || tokenErr.Code != OAuthErrInvalidGrant {
		t.Errorf("port drift at the token endpoint = %+v, want invalid_grant", tokenErr)
	}

	// An expired code is unknown to the exchange.
	code = approve(t, ctx, service, client.ID, registeredRedirectURI, "", verifier, user.ID)
	clock.Advance(OAuthCodeTTL + time.Second)
	if _, tokenErr := exchange(t, ctx, service, code, client.ID, registeredRedirectURI, verifier); tokenErr == nil || tokenErr.Code != OAuthErrInvalidGrant {
		t.Errorf("expired code = %+v, want invalid_grant", tokenErr)
	}
	// And a fresh approval purges it.
	approve(t, ctx, service, client.ID, registeredRedirectURI, "", verifier, user.ID)
	if _, err := service.store.OAuthCodes.ByCodeHash(ctx, OAuthCodeHash(code)); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("the expired code survived the next approval: %v", err)
	}

	// An unknown but well-formed code is invalid_grant, never a 500.
	if _, tokenErr := exchange(t, ctx, service, NewOAuthCode(), client.ID, registeredRedirectURI, verifier); tokenErr == nil || tokenErr.Code != OAuthErrInvalidGrant {
		t.Errorf("unknown code = %+v, want invalid_grant", tokenErr)
	}

	// A registration purged between consent and exchange cannot mint.
	gone := registerClient(t, ctx, service)
	code = approve(t, ctx, service, gone.ID, registeredRedirectURI, "", verifier, user.ID)
	if _, err := service.store.OAuthClients.Purge(ctx, service.Now(), 0, oauthClientIdleRetention); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if _, tokenErr := exchange(t, ctx, service, code, gone.ID, registeredRedirectURI, verifier); tokenErr == nil || tokenErr.Code != OAuthErrInvalidClient || tokenErr.Status != 401 {
		t.Errorf("purged registration = %+v, want 401 invalid_client", tokenErr)
	}
}

// TestOAuthExchangeRespectsTheTokenCap burns the code rather than leaving a
// client to retry against a wall it cannot get past.
func TestOAuthExchangeRespectsTheTokenCap(t *testing.T) {
	ctx, service, _ := requireService(t)
	user := seedAccount(t, ctx, service)
	client := registerClient(t, ctx, service)
	verifier := strings.Repeat("v", 43)

	for i := range db.MaxActiveAPITokens {
		if _, _, err := service.CreateAPIToken(ctx, user.ID, CreateAPITokenParams{
			Name: "filler", Scopes: []string{"events:read"},
		}); err != nil {
			t.Fatalf("CreateAPIToken %d: %v", i, err)
		}
	}

	code := approve(t, ctx, service, client.ID, registeredRedirectURI, "", verifier, user.ID)
	_, tokenErr := exchange(t, ctx, service, code, client.ID, registeredRedirectURI, verifier)
	if tokenErr == nil || tokenErr.Code != OAuthErrInvalidGrant || tokenErr.Status != 400 {
		t.Fatalf("exchange at the cap = %+v, want 400 invalid_grant", tokenErr)
	}
	if tokenErr.Description == "" {
		t.Error("the cap refusal carries no description")
	}
	if n, err := service.store.APITokens.CountActive(ctx, user.ID, service.Now()); err != nil || n != db.MaxActiveAPITokens {
		t.Errorf("CountActive = (%d, %v), want the cap untouched", n, err)
	}
	spent, err := service.store.OAuthCodes.ByCodeHash(ctx, OAuthCodeHash(code))
	if err != nil || spent.ConsumedAt == nil {
		t.Errorf("the code was not spent by the refused exchange: %+v, %v", spent, err)
	}
}

func TestOAuthConsentAndExchangeWithADocumentClient(t *testing.T) {
	ctx, service, _ := requireService(t)
	user := seedAccount(t, ctx, service)
	transport := &documentTransport{responses: map[string]*http.Response{
		testClientID: document(http.StatusOK, validDocument, nil),
	}}
	service.UseMetadataClient(newOAuthMetadataClient(transport))
	verifier := strings.Repeat("v", 43)

	// The token endpoint fetches nothing on the caller's say-so: a code Hark
	// never issued is refused before the client is looked at.
	if _, tokenErr := exchange(t, ctx, service, NewOAuthCode(), testClientID, testRedirectURI, verifier); tokenErr == nil || tokenErr.Code != OAuthErrInvalidGrant {
		t.Fatalf("unknown code = %+v, want invalid_grant", tokenErr)
	}
	if transport.count() != 0 {
		t.Fatalf("the document was fetched %d times for a code that does not exist, want 0", transport.count())
	}

	code := approve(t, ctx, service, testClientID, testRedirectURI, "devices:read", verifier, user.ID)
	grant, tokenErr := exchange(t, ctx, service, code, testClientID, testRedirectURI, verifier)
	if tokenErr != nil {
		t.Fatalf("exchange = %s: %s", tokenErr.Code, tokenErr.Description)
	}
	if grant.Token.Name != "Example Agent" || !equalStrings(grant.Scopes, []string{"devices:read"}) {
		t.Errorf("token = %+v, want the document's name and the consented scope", grant.Token)
	}
	// The document is fetched at consent and not again at the exchange.
	if transport.count() != 1 {
		t.Errorf("the document was fetched %d times, want 1", transport.count())
	}
}

func TestRegisterOAuthClientValidates(t *testing.T) {
	ctx, service, _ := requireService(t)

	valid := []string{"https://example.com/cb"}
	for name, tc := range map[string]struct {
		p     RegisterOAuthClientParams
		field string
	}{
		"no uris":      {RegisterOAuthClientParams{}, "redirect_uris"},
		"empty uris":   {RegisterOAuthClientParams{RedirectURIs: []string{}}, "redirect_uris"},
		"too many":     {RegisterOAuthClientParams{RedirectURIs: slices.Repeat(valid, MaxOAuthRedirectURIs+1)}, "redirect_uris"},
		"plain http":   {RegisterOAuthClientParams{RedirectURIs: []string{"http://example.com/cb"}}, "redirect_uris"},
		"fragment":     {RegisterOAuthClientParams{RedirectURIs: []string{"https://example.com/cb#x"}}, "redirect_uris"},
		"one bad uri":  {RegisterOAuthClientParams{RedirectURIs: []string{"https://example.com/cb", "ftp://x/y"}}, "redirect_uris"},
		"long name":    {RegisterOAuthClientParams{RedirectURIs: valid, Name: strings.Repeat("n", MaxAPITokenNameLength+1)}, "client_name"},
		"control name": {RegisterOAuthClientParams{RedirectURIs: valid, Name: "bad\nname"}, "client_name"},
		"client_uri":   {RegisterOAuthClientParams{RedirectURIs: valid, ClientURI: "http://example.com"}, "client_uri"},
		"logo_uri":     {RegisterOAuthClientParams{RedirectURIs: valid, LogoURI: "javascript:alert(1)"}, "logo_uri"},
		"grant type":   {RegisterOAuthClientParams{RedirectURIs: valid, GrantTypes: []string{"authorization_code", "refresh_token"}}, "grant_types"},
		"response":     {RegisterOAuthClientParams{RedirectURIs: valid, ResponseTypes: []string{"token"}}, "response_types"},
		"auth method":  {RegisterOAuthClientParams{RedirectURIs: valid, TokenEndpointAuthMethod: "client_secret_post"}, "token_endpoint_auth_method"},
		"scope":        {RegisterOAuthClientParams{RedirectURIs: valid, Scope: "devices:read the:moon"}, "scope"},
	} {
		_, err := service.RegisterOAuthClient(ctx, tc.p)
		var bad *InvalidInputError
		if !errors.As(err, &bad) {
			t.Errorf("%s: RegisterOAuthClient = %v, want an InvalidInputError", name, err)
			continue
		}
		if bad.Field != tc.field {
			t.Errorf("%s: field = %q, want %q", name, bad.Field, tc.field)
		}
	}

	// The name defaults to the host of the first redirect URI.
	client, err := service.RegisterOAuthClient(ctx, RegisterOAuthClientParams{
		RedirectURIs: []string{"https://inspector.example.org/cb", "http://localhost/cb"},
	})
	if err != nil {
		t.Fatalf("RegisterOAuthClient(minimal): %v", err)
	}
	if client.Name != "inspector.example.org" || client.ClientURI != nil || client.LogoURI != nil {
		t.Errorf("minimal registration = %+v", client)
	}
}

func TestRegisterOAuthClientPurgesAbandonedRegistrations(t *testing.T) {
	ctx, service, clock := requireService(t)
	user := seedAccount(t, ctx, service)
	verifier := strings.Repeat("v", 43)

	abandoned := registerClient(t, ctx, service)
	used := registerClient(t, ctx, service)
	code := approve(t, ctx, service, used.ID, registeredRedirectURI, "", verifier, user.ID)
	if _, tokenErr := exchange(t, ctx, service, code, used.ID, registeredRedirectURI, verifier); tokenErr != nil {
		t.Fatalf("exchange: %+v", tokenErr)
	}

	clock.Advance(oauthClientUnusedRetention + time.Minute)
	registerClient(t, ctx, service)

	if _, err := service.OAuthClientByID(ctx, abandoned.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the abandoned registration survived: %v", err)
	}
	if _, err := service.OAuthClientByID(ctx, used.ID); err != nil {
		t.Errorf("the used registration was purged: %v", err)
	}

	clock.Advance(oauthClientIdleRetention)
	registerClient(t, ctx, service)
	if _, err := service.OAuthClientByID(ctx, used.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a registration idle for a year survived: %v", err)
	}
}

func TestConcurrentOAuthAndManualGrantsRespectTokenCap(t *testing.T) {
	ctx, service, _ := requireService(t)
	user := seedAccount(t, ctx, service)
	client := registerClient(t, ctx, service)
	verifier := strings.Repeat("v", 43)
	for range db.MaxActiveAPITokens - 1 {
		if _, _, err := service.CreateAPIToken(ctx, user.ID, CreateAPITokenParams{Name: "filler", Scopes: []string{"events:read"}}); err != nil {
			t.Fatal(err)
		}
	}
	const attempts = 8
	codes := make([]string, attempts/2)
	for i := range codes {
		codes[i] = approve(t, ctx, service, client.ID, registeredRedirectURI, "events:read", verifier, user.ID)
	}
	// Delay insertion so concurrent count-and-insert transactions overlap.
	_, err := schemaPool.Exec(ctx, `
 CREATE FUNCTION delay_token_insert() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN PERFORM pg_sleep(0.05); RETURN NEW; END $$;
 CREATE TRIGGER delay_token_insert BEFORE INSERT ON api_tokens
 FOR EACH ROW EXECUTE FUNCTION delay_token_insert();`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := schemaPool.Exec(context.WithoutCancel(ctx), `DROP TRIGGER delay_token_insert ON api_tokens; DROP FUNCTION delay_token_insert()`); err != nil {
			t.Error(err)
		}
	})
	start := make(chan struct{})
	results := make(chan error, attempts)
	var workers sync.WaitGroup
	for i := range attempts {
		workers.Go(func() {
			<-start
			if i%2 == 0 {
				_, err := service.ExchangeOAuthCode(ctx, ExchangeOAuthCodeParams{Code: codes[i/2], ClientID: client.ID, RedirectURI: registeredRedirectURI, CodeVerifier: verifier, Resource: testResource, ExpectedResource: testResource})
				if err != nil {
					var refusal *OAuthTokenError
					if !errors.As(err, &refusal) || refusal.Code != OAuthErrInvalidGrant {
						results <- fmt.Errorf("unexpected OAuth failure: %w", err)
						return
					}
					results <- ErrTokenLimit
					return
				}
				results <- nil
				return
			}
			_, _, err := service.CreateAPIToken(ctx, user.ID, CreateAPITokenParams{Name: "concurrent", Scopes: []string{"events:read"}})
			results <- err
		})
	}
	close(start)
	workers.Wait()
	close(results)
	granted := 0
	for err := range results {
		if err == nil {
			granted++
		} else if !errors.Is(err, ErrTokenLimit) {
			t.Error(err)
		}
	}
	if granted != 1 {
		t.Errorf("granted %d tokens for one remaining slot", granted)
	}
	if active, err := service.store.APITokens.CountActive(ctx, user.ID, service.Now()); err != nil || active != db.MaxActiveAPITokens {
		t.Errorf("active tokens = %d, %v; want %d", active, err, db.MaxActiveAPITokens)
	}
}

func TestConcurrentOAuthCodeExchangeIssuesOneToken(t *testing.T) {
	ctx, service, _ := requireService(t)
	user := seedAccount(t, ctx, service)
	client := registerClient(t, ctx, service)
	verifier := strings.Repeat("v", 43)
	code := approve(t, ctx, service, client.ID, registeredRedirectURI, "events:read", verifier, user.ID)
	const attempts = 8
	start := make(chan struct{})
	results := make(chan error, attempts)
	var workers sync.WaitGroup
	for range attempts {
		workers.Go(func() {
			<-start
			_, err := service.ExchangeOAuthCode(ctx, ExchangeOAuthCodeParams{
				Code: code, ClientID: client.ID, RedirectURI: registeredRedirectURI,
				CodeVerifier: verifier, Resource: testResource, ExpectedResource: testResource,
			})
			results <- err
		})
	}
	close(start)
	workers.Wait()
	close(results)
	granted := 0
	for err := range results {
		if err == nil {
			granted++
			continue
		}
		var refusal *OAuthTokenError
		if !errors.As(err, &refusal) || refusal.Code != OAuthErrInvalidGrant {
			t.Errorf("unexpected exchange failure: %v", err)
		}
	}
	if granted != 1 {
		t.Errorf("granted %d tokens for one authorization code", granted)
	}
	if active, err := service.store.APITokens.CountActive(ctx, user.ID, service.Now()); err != nil || active != 1 {
		t.Errorf("active tokens = %d, %v; want 1", active, err)
	}
}

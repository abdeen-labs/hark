package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/id"
	"github.com/abdeen-labs/hark/internal/mcp"
)

// form sends a form-encoded request without a credential, as a public OAuth
// client does.
func (f *fixture) form(path string, values url.Values) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode())).WithContext(f.ctx)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

func TestOAuthFlowIssuesAUsableToken(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	const redirectURI = "https://claude.ai/api/mcp/auth_callback"
	publicURL := &url.URL{Scheme: "https", Host: "hark.example.com"}
	resource := mcp.Resource(publicURL)

	var registered oauthRegisterResponse
	rec := f.expect(http.MethodPost, OAuthRegisterPath, "", `{
		"client_name": "Claude",
		"redirect_uris": ["`+redirectURI+`"],
		"grant_types": ["authorization_code"],
		"response_types": ["code"],
		"token_endpoint_auth_method": "none",
		"client_uri": "https://claude.ai",
		"logo_uri": "https://claude.ai/logo.png",
		"software_id": "ignored"
	}`, http.StatusCreated, &registered)
	assertOAuthHeaders(t, rec)
	if !id.Valid(registered.ClientID) {
		t.Errorf("client_id = %q, want a UUIDv7", registered.ClientID)
	}
	if registered.ClientIDIssuedAt <= 0 {
		t.Errorf("client_id_issued_at = %d, want Unix seconds", registered.ClientIDIssuedAt)
	}
	if registered.ClientName != "Claude" || !slices.Equal(registered.RedirectURIs, []string{redirectURI}) {
		t.Errorf("registration = %+v", registered)
	}
	if registered.LogoURI == nil || *registered.LogoURI != "https://claude.ai/logo.png" {
		t.Errorf("logo_uri = %v, want the registered logo echoed", registered.LogoURI)
	}
	if !slices.Equal(registered.GrantTypes, []string{"authorization_code"}) ||
		!slices.Equal(registered.ResponseTypes, []string{"code"}) ||
		registered.TokenEndpointAuthMethod != "none" {
		t.Errorf("registration = %+v, want the public-client shape echoed", registered)
	}
	if strings.Contains(rec.Body.String(), "client_secret") {
		t.Errorf("a client secret was issued: %s", rec.Body)
	}

	// The consent screen's half, through the same service the handler uses.
	authService := auth.New(f.store, nil)
	verifier := strings.Repeat("v", 43)
	consent, err := authService.OAuthConsent(f.ctx, auth.OAuthAuthorizationRequest{
		ResponseType:        "code",
		ClientID:            registered.ClientID,
		RedirectURI:         redirectURI,
		Scope:               "devices:read interactions:read",
		State:               "abc",
		CodeChallenge:       auth.OAuthCodeChallenge(verifier),
		CodeChallengeMethod: "S256",
		Resource:            resource,
	}, resource)
	if err != nil {
		t.Fatalf("OAuthConsent: %v", err)
	}
	code, err := authService.ApproveOAuth(f.ctx, consent, f.userID)
	if err != nil {
		t.Fatalf("ApproveOAuth: %v", err)
	}

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {registered.ClientID},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
		"resource":      {resource},
	}
	rec = f.form(OAuthTokenPath, form)
	if rec.Code != http.StatusOK {
		t.Fatalf("token exchange: status = %d, want 200: %s", rec.Code, rec.Body)
	}
	assertOAuthHeaders(t, rec)
	var granted oauthTokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &granted); err != nil {
		t.Fatalf("decode token response: %v", err)
	}
	if !auth.ValidAPIToken(granted.AccessToken) {
		t.Errorf("access_token = %q, want an API token", granted.AccessToken)
	}
	if granted.TokenType != "Bearer" {
		t.Errorf("token_type = %q, want Bearer", granted.TokenType)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &fields); err != nil {
		t.Fatalf("decode token response: %v", err)
	}
	if _, present := fields["expires_in"]; present {
		t.Errorf("the token response carries expires_in; an OAuth-issued token does not expire")
	}
	if granted.Scope != "devices:read interactions:read" {
		t.Errorf("scope = %q, want the consented scopes, sorted and space-separated", granted.Scope)
	}

	// The token is a scoped API token like any other.
	var devices deviceListResponse
	f.expect(http.MethodGet, "/devices", granted.AccessToken, "", http.StatusOK, &devices)
	rec = f.request(http.MethodPost, "/notifications", granted.AccessToken, `{"body":"hi"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("send outside the consented scopes: status = %d, want 403: %s", rec.Code, rec.Body)
	}
	if got := decodeError(t, rec); got.Error.Code != CodeInsufficientScope {
		t.Errorf("code = %q, want %q", got.Error.Code, CodeInsufficientScope)
	}

	// And it is listed under the client's name, revocable like the rest.
	var listed tokenListResponse
	f.expect(http.MethodGet, "/tokens", f.session, "", http.StatusOK, &listed)
	index := slices.IndexFunc(listed.Tokens, func(tok tokenDTO) bool { return tok.Name == "Claude" })
	if index < 0 {
		t.Fatalf("tokens = %+v, want one named after the client", listed.Tokens)
	}
	if !slices.Equal(listed.Tokens[index].Scopes, []string{"devices:read", "interactions:read"}) || listed.Tokens[index].ExpiresAt != nil {
		t.Errorf("granted token = %+v, want the consented scopes and no expiry", listed.Tokens[index])
	}
	if image := listed.Tokens[index].ImageURL; image == nil || *image != "https://claude.ai/logo.png" {
		t.Errorf("granted token image_url = %v, want the client's logo", image)
	}
	f.expect(http.MethodDelete, "/tokens/"+listed.Tokens[index].ID, f.session, "", http.StatusNoContent, nil)
	if rec := f.request(http.MethodGet, "/devices", granted.AccessToken, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("revoked OAuth token: status = %d, want 401", rec.Code)
	}

	// A code issues exactly one token.
	rec = f.form(OAuthTokenPath, form)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("second exchange: status = %d, want 400: %s", rec.Code, rec.Body)
	}
	assertOAuthHeaders(t, rec)
	if got := decodeOAuthError(t, rec); got.Error != auth.OAuthErrInvalidGrant {
		t.Errorf("second exchange: error = %q, want invalid_grant", got.Error)
	}
}

func TestOAuthTokenRefusesTheWrongVerifierAndClient(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	const redirectURI = "http://127.0.0.1/callback"
	resource := mcp.Resource(&url.URL{Scheme: "https", Host: "hark.example.com"})

	var registered oauthRegisterResponse
	f.expect(http.MethodPost, OAuthRegisterPath, "", `{"redirect_uris":["`+redirectURI+`"]}`,
		http.StatusCreated, &registered)
	if registered.ClientName != "127.0.0.1" {
		t.Errorf("client_name = %q, want the redirect host by default", registered.ClientName)
	}

	authService := auth.New(f.store, nil)
	approve := func(verifier string) string {
		t.Helper()
		consent, err := authService.OAuthConsent(f.ctx, auth.OAuthAuthorizationRequest{
			ResponseType: "code", ClientID: registered.ClientID, RedirectURI: "http://127.0.0.1:52000/callback",
			CodeChallenge: auth.OAuthCodeChallenge(verifier), CodeChallengeMethod: "S256",
		}, resource)
		if err != nil {
			t.Fatalf("OAuthConsent: %v", err)
		}
		if !consent.Loopback {
			t.Error("a 127.0.0.1 redirect was not labelled loopback")
		}
		code, err := authService.ApproveOAuth(f.ctx, consent, f.userID)
		if err != nil {
			t.Fatalf("ApproveOAuth: %v", err)
		}
		return code
	}
	tokenRequest := func(code, clientID, verifier string) url.Values {
		return url.Values{
			"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID},
			"redirect_uri": {"http://127.0.0.1:52000/callback"}, "code_verifier": {verifier},
		}
	}

	verifier := strings.Repeat("v", 43)
	code := approve(verifier)
	rec := f.form(OAuthTokenPath, tokenRequest(code, registered.ClientID, strings.Repeat("w", 43)))
	if got := decodeOAuthError(t, rec); rec.Code != http.StatusBadRequest || got.Error != auth.OAuthErrInvalidGrant {
		t.Errorf("wrong verifier: status = %d, error = %q; want 400 invalid_grant", rec.Code, got.Error)
	}
	// Spent by the failed attempt.
	rec = f.form(OAuthTokenPath, tokenRequest(code, registered.ClientID, verifier))
	if got := decodeOAuthError(t, rec); rec.Code != http.StatusBadRequest || got.Error != auth.OAuthErrInvalidGrant {
		t.Errorf("retry with the right verifier: status = %d, error = %q; want 400 invalid_grant", rec.Code, got.Error)
	}

	// A client Hark does not know is a client the code was not issued to, and
	// the attempt spends the code like any other.
	code = approve(verifier)
	rec = f.form(OAuthTokenPath, tokenRequest(code, id.New(), verifier))
	if got := decodeOAuthError(t, rec); rec.Code != http.StatusBadRequest || got.Error != auth.OAuthErrInvalidGrant {
		t.Errorf("unknown client: status = %d, error = %q; want 400 invalid_grant", rec.Code, got.Error)
	}
	rec = f.form(OAuthTokenPath, tokenRequest(code, registered.ClientID, verifier))
	if got := decodeOAuthError(t, rec); rec.Code != http.StatusBadRequest || got.Error != auth.OAuthErrInvalidGrant {
		t.Errorf("exchange after an unknown client's attempt: status = %d, error = %q; want 400 invalid_grant", rec.Code, got.Error)
	}
}

package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/id"
	"github.com/abdeen-labs/hark/internal/netpolicy"
)

// OAuth limits and protocol identifiers.
const (
	// OAuthCodeTTL is how long an authorization code may be exchanged. The
	// token it issues has no expiry of its own: the owner revokes it.
	OAuthCodeTTL = 10 * time.Minute

	// MaxOAuthRedirectURIs bounds a registration.
	MaxOAuthRedirectURIs = 10

	// MaxOAuthRedirectURILength bounds one redirect URI.
	MaxOAuthRedirectURILength = 2048

	// MaxOAuthStateLength bounds the state a client asks to have echoed back.
	MaxOAuthStateLength = 1024

	// OAuthCodeChallengeMethod is the only PKCE method accepted.
	OAuthCodeChallengeMethod = "S256"

	OAuthCodePrefix = "harkcode_"

	// PKCE verifier length bounds (RFC 7636 §4.1).
	MinOAuthCodeVerifierLength = 43
	MaxOAuthCodeVerifierLength = 128

	// oauthCodeBytes is the entropy behind an authorization code: 32 bytes
	// render as 43 base64url characters.
	oauthCodeBytes = 32

	oauthClientUnusedRetention = 24 * time.Hour
	oauthClientIdleRetention   = 365 * 24 * time.Hour

	// oauthMetadataTTL is how long a fetched client metadata document is
	// reused before it is fetched again.
	oauthMetadataTTL = 10 * time.Minute

	// oauthMetadataMaxBytes bounds a client metadata document.
	oauthMetadataMaxBytes = 64 << 10

	// oauthMetadataTimeout bounds one fetch of a client metadata document.
	oauthMetadataTimeout = 5 * time.Second

	oauthMetadataCacheSize = 256

	// oauthMetadataUserAgent identifies metadata fetches in a client's log.
	oauthMetadataUserAgent = "Hark-OAuth/1"

	// domainOAuthCode salts the stored digest of an authorization code.
	domainOAuthCode = "hark.oauth-code.v1"
)

// OAuthClient is a registered client or a client metadata document.
type OAuthClient struct {
	// ID is what the client sends as client_id: a registration's id, or the
	// URL of its metadata document.
	ID string
	// Name is shown to the owner and becomes the issued token's name.
	Name         string
	RedirectURIs []string
	ClientURI    *string
	// LogoURI is a public https image shown beside Name and carried by the
	// issued token.
	LogoURI *string
	// MetadataDocument reports that the client is identified by a document at
	// ID rather than by a registration.
	MetadataDocument bool
}

// RegisterOAuthClientParams is a dynamic registration (RFC 7591).
type RegisterOAuthClientParams struct {
	Name         string
	RedirectURIs []string
	ClientURI    string
	LogoURI      string
	// GrantTypes, ResponseTypes and TokenEndpointAuthMethod are validated when
	// present: only authorization_code, code and none are accepted.
	GrantTypes              []string
	ResponseTypes           []string
	TokenEndpointAuthMethod string
	// Scope is space-separated and informational.
	Scope string
}

// OAuthAuthorizationRequest contains the unvalidated authorization parameters.
type OAuthAuthorizationRequest struct {
	ResponseType        string
	ClientID            string
	RedirectURI         string
	Scope               string
	State               string
	CodeChallenge       string
	CodeChallengeMethod string
	Resource            string
}

// OAuthConsent contains a validated authorization request.
type OAuthConsent struct {
	Client      OAuthClient
	RedirectURI string
	// Loopback reports that RedirectURI points at the owner's own machine.
	Loopback bool
	Scopes   []string
	State    string
	// CodeChallenge is the PKCE challenge the token exchange must satisfy.
	CodeChallenge string
	// Resource is the RFC 8707 resource the request named, or nil.
	Resource *string
}

// OAuthClientError rejects an unknown client or unregistered redirect URI without redirecting.
type OAuthClientError struct{ Message string }

func (e *OAuthClientError) Error() string { return "auth: " + e.Message }

// OAuthRedirectError is returned to the client at its validated redirect URI.
type OAuthRedirectError struct {
	// Code is an RFC 6749 §4.1.2.1 error: invalid_request,
	// unsupported_response_type, invalid_scope, or invalid_target.
	Code        string
	Description string
}

func (e *OAuthRedirectError) Error() string { return "auth: " + e.Code + ": " + e.Description }

// ExchangeOAuthCodeParams is a token request (RFC 6749 §4.1.3).
type ExchangeOAuthCodeParams struct {
	Code         string
	ClientID     string
	RedirectURI  string
	CodeVerifier string
	// Resource is what the token request named, or "".
	Resource string
	// ExpectedResource is this deployment's MCP resource identifier. A request
	// that names a Resource must name this one.
	ExpectedResource string
}

// OAuthTokenGrant contains the issued token and its plaintext secret.
type OAuthTokenGrant struct {
	Secret string
	Token  *db.APIToken
	Scopes []string
}

// OAuthTokenError contains the token endpoint error code, description, and HTTP status.
type OAuthTokenError struct {
	// Code is invalid_request, invalid_client, invalid_grant, or
	// unsupported_grant_type.
	Code        string
	Description string
	Status      int
}

func (e *OAuthTokenError) Error() string { return "auth: " + e.Code + ": " + e.Description }

// The error vocabularies of RFC 6749 §4.1.2.1 and §5.2, as the consent screen
// and the token endpoint use them.
const (
	OAuthErrInvalidRequest          = "invalid_request"
	OAuthErrUnsupportedResponseType = "unsupported_response_type"
	OAuthErrInvalidScope            = "invalid_scope"
	OAuthErrInvalidTarget           = "invalid_target"
	OAuthErrAccessDenied            = "access_denied"
	OAuthErrInvalidClient           = "invalid_client"
	OAuthErrInvalidGrant            = "invalid_grant"
	OAuthErrUnsupportedGrantType    = "unsupported_grant_type"
)

// OAuthRedirectURL adds the authorization result, state, and issuer to the redirect URI.
func OAuthRedirectURL(redirectURI, issuer, state string, values url.Values) string {
	u, err := url.Parse(redirectURI)
	if err != nil {
		return redirectURI
	}
	q := u.Query()
	for key, vs := range values {
		q.Del(key)
		for _, v := range vs {
			q.Add(key, v)
		}
	}
	if state != "" {
		q.Set("state", state)
	}
	q.Set("iss", issuer)
	u.RawQuery = q.Encode()
	return u.String()
}

// NewOAuthCode returns an authorization code with 256 bits of random entropy.
func NewOAuthCode() string {
	raw := make([]byte, oauthCodeBytes)
	rand.Read(raw) //nolint:errcheck // documented to always succeed
	return OAuthCodePrefix + base64.RawURLEncoding.EncodeToString(raw)
}

// oauthCodeLength is the length of every code NewOAuthCode issues.
var oauthCodeLength = len(OAuthCodePrefix) + base64.RawURLEncoding.EncodedLen(oauthCodeBytes)

// ValidOAuthCode reports whether s has the shape of an authorization code.
// Checking the shape first means a malformed value never reaches the database.
func ValidOAuthCode(s string) bool {
	if len(s) != oauthCodeLength || !strings.HasPrefix(s, OAuthCodePrefix) {
		return false
	}
	for i := len(OAuthCodePrefix); i < len(s); i++ {
		if !base64URLChar(s[i]) {
			return false
		}
	}
	return true
}

// OAuthCodeHash returns the stored digest of an authorization code.
func OAuthCodeHash(code string) string { return digest(domainOAuthCode, code) }

// ValidOAuthCodeVerifier checks the PKCE verifier length and unreserved alphabet (RFC 7636).
func ValidOAuthCodeVerifier(v string) bool {
	if len(v) < MinOAuthCodeVerifierLength || len(v) > MaxOAuthCodeVerifierLength {
		return false
	}
	for i := 0; i < len(v); i++ {
		if !pkceChar(v[i]) {
			return false
		}
	}
	return true
}

// OAuthCodeChallenge derives the S256 PKCE challenge.
func OAuthCodeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// OAuthVerifierMatches reports whether verifier satisfies challenge, comparing
// in constant time.
func OAuthVerifierMatches(challenge, verifier string) bool {
	if !ValidOAuthCodeVerifier(verifier) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(OAuthCodeChallenge(verifier)), []byte(challenge)) == 1
}

func base64URLChar(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_'
}

func pkceChar(c byte) bool {
	return base64URLChar(c) || c == '.' || c == '~'
}

// ValidOAuthRedirectURI permits HTTPS or loopback HTTP URLs without userinfo or fragments.
func ValidOAuthRedirectURI(raw string) bool {
	if raw == "" || len(raw) > MaxOAuthRedirectURILength || strings.Contains(raw, "#") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Hostname() == "" || u.User != nil {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		return oauthLoopbackHost(u.Hostname())
	}
	return false
}

// OAuthLoopbackRedirectURI reports whether a redirect URI points at the
// owner's own machine.
func OAuthLoopbackRedirectURI(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && oauthLoopbackHost(u.Hostname())
}

func oauthLoopbackHost(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// oauthRedirectMatches requires an exact match except for a loopback redirect port.
func oauthRedirectMatches(registered []string, candidate string) bool {
	if !ValidOAuthRedirectURI(candidate) {
		return false
	}
	if slices.Contains(registered, candidate) {
		return true
	}
	c, err := url.Parse(candidate)
	if err != nil || !oauthLoopbackHost(c.Hostname()) {
		return false
	}
	for _, raw := range registered {
		r, err := url.Parse(raw)
		if err != nil {
			continue
		}
		if r.Scheme == c.Scheme && strings.EqualFold(r.Hostname(), c.Hostname()) &&
			r.EscapedPath() == c.EscapedPath() && r.RawQuery == c.RawQuery && r.ForceQuery == c.ForceQuery {
			return true
		}
	}
	return false
}

// validateOAuthRedirectURIs checks a registration's list and returns it with
// duplicates removed, first occurrence kept.
func validateOAuthRedirectURIs(raw []string) ([]string, error) {
	if len(raw) == 0 || len(raw) > MaxOAuthRedirectURIs {
		return nil, invalid("redirect_uris", fmt.Sprintf("must list 1-%d redirect URIs", MaxOAuthRedirectURIs))
	}
	out := make([]string, 0, len(raw))
	for _, uri := range raw {
		if !ValidOAuthRedirectURI(uri) {
			return nil, invalid("redirect_uris", "must hold absolute https URLs without a fragment, or http URLs on localhost, 127.0.0.1 or [::1]")
		}
		if !slices.Contains(out, uri) {
			out = append(out, uri)
		}
	}
	return out, nil
}

// validOAuthLink reports whether raw is an https URL fit to be linked: client_uri.
func validOAuthLink(raw string) bool {
	if len(raw) > MaxOAuthRedirectURILength {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != ""
}

func oauthClientName(raw string, redirectURIs []string) (string, bool) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return oauthClientNameFallback(redirectURIs), true
	}
	if len(name) > MaxAPITokenNameLength || strings.ContainsFunc(name, unicode.IsControl) {
		return "", false
	}
	return name, true
}

func oauthClientNameFallback(redirectURIs []string) string {
	for _, raw := range redirectURIs {
		if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
			return u.Hostname()
		}
	}
	return "OAuth client"
}

// RegisterOAuthClient validates and stores a public client registration.
func (s *Service) RegisterOAuthClient(ctx context.Context, p RegisterOAuthClientParams) (*db.OAuthClient, error) {
	uris, err := validateOAuthRedirectURIs(p.RedirectURIs)
	if err != nil {
		return nil, err
	}
	name, ok := oauthClientName(p.Name, uris)
	if !ok {
		return nil, invalid("client_name", fmt.Sprintf("must be 1-%d characters", MaxAPITokenNameLength))
	}
	var clientURI, logoURI *string
	if p.ClientURI != "" {
		if !validOAuthLink(p.ClientURI) {
			return nil, invalid("client_uri", "must be an https URL")
		}
		clientURI = &p.ClientURI
	}
	if p.LogoURI != "" {
		if !ValidAPITokenImageURL(p.LogoURI) {
			return nil, invalid("logo_uri", "must be a public https URL")
		}
		logoURI = &p.LogoURI
	}
	for _, grant := range p.GrantTypes {
		if grant != "authorization_code" {
			return nil, invalid("grant_types", "may list only authorization_code")
		}
	}
	for _, response := range p.ResponseTypes {
		if response != "code" {
			return nil, invalid("response_types", "may list only code")
		}
	}
	if p.TokenEndpointAuthMethod != "" && p.TokenEndpointAuthMethod != "none" {
		return nil, invalid("token_endpoint_auth_method", "must be none: clients are public")
	}
	if p.Scope != "" {
		if _, ok := db.NormalizeScopes(strings.Fields(p.Scope)); !ok {
			return nil, invalid("scope", "must name only known scopes: "+strings.Join(db.Scopes, " "))
		}
	}

	now := s.Now()

	// Opportunistic housekeeping: abandoned registrations leave with the next
	// arrival, and a failure here must not fail the request.
	_, _ = s.store.OAuthClients.Purge(ctx, now, oauthClientUnusedRetention, oauthClientIdleRetention)

	client, err := s.store.OAuthClients.Create(ctx, db.CreateOAuthClientParams{
		ID:           id.New(),
		Name:         name,
		RedirectURIs: uris,
		ClientURI:    clientURI,
		LogoURI:      logoURI,
		Now:          now,
	})
	if err != nil {
		return nil, fmt.Errorf("auth: register OAuth client: %w", err)
	}
	return client, nil
}

// OAuthClientByID resolves a registered client or fetches its HTTPS metadata document.
func (s *Service) OAuthClientByID(ctx context.Context, clientID string) (*OAuthClient, error) {
	if u, ok := oauthMetadataURL(clientID); ok {
		return s.oauthClientFromDocument(ctx, clientID, u)
	}
	if !id.Valid(clientID) {
		// Registered ids are UUIDv7s; anything else is refused on shape alone.
		return nil, ErrNotFound
	}
	row, err := s.store.OAuthClients.ByID(ctx, clientID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("auth: load OAuth client: %w", err)
	}
	return &OAuthClient{
		ID:           row.ID,
		Name:         row.Name,
		RedirectURIs: row.RedirectURIs,
		ClientURI:    row.ClientURI,
		LogoURI:      row.LogoURI,
	}, nil
}

func oauthMetadataURL(clientID string) (*url.URL, bool) {
	if len(clientID) > MaxOAuthRedirectURILength || !strings.HasPrefix(clientID, "https://") {
		return nil, false
	}
	u, err := url.Parse(clientID)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		u.Fragment != "" || strings.Contains(clientID, "#") || u.Path == "" || u.Path == "/" {
		return nil, false
	}
	return u, true
}

// oauthMetadata fetches client metadata documents and remembers them for
// oauthMetadataTTL.
type oauthMetadata struct {
	mu      sync.Mutex
	client  *http.Client
	entries map[string]oauthMetadataEntry
}

type oauthMetadataEntry struct {
	client    OAuthClient
	fetchedAt time.Time
}

func (m *oauthMetadata) use(c *http.Client) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.client = c
}

// httpClient returns the fetching client, building the production one on
// first use.
func (m *oauthMetadata) httpClient() *http.Client {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		// Environment proxies would bypass the public-address dial policy.
		transport.Proxy = nil
		transport.DialContext = (&netpolicy.Dialer{}).DialContext
		m.client = newOAuthMetadataClient(transport)
	}
	return m.client
}

func newOAuthMetadataClient(transport http.RoundTripper) *http.Client {
	return &http.Client{
		Timeout:   oauthMetadataTimeout,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (m *oauthMetadata) lookup(clientID string, now time.Time) (OAuthClient, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.entries[clientID]
	if !ok || now.Sub(entry.fetchedAt) >= oauthMetadataTTL {
		return OAuthClient{}, false
	}
	return entry.client, true
}

func (m *oauthMetadata) remember(clientID string, client OAuthClient, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries == nil {
		m.entries = make(map[string]oauthMetadataEntry)
	}
	if _, ok := m.entries[clientID]; !ok && len(m.entries) >= oauthMetadataCacheSize {
		for key, entry := range m.entries {
			if now.Sub(entry.fetchedAt) >= oauthMetadataTTL {
				delete(m.entries, key)
			}
		}
		if len(m.entries) >= oauthMetadataCacheSize {
			return
		}
	}
	m.entries[clientID] = oauthMetadataEntry{client: client, fetchedAt: now}
}

// oauthMetadataDocument is the subset of a client metadata document Hark
// reads. Everything else in the document is ignored.
type oauthMetadataDocument struct {
	ClientID                          string   `json:"client_id"`
	ClientName                        string   `json:"client_name"`
	RedirectURIs                      []string `json:"redirect_uris"`
	ClientURI                         string   `json:"client_uri"`
	LogoURI                           string   `json:"logo_uri"`
	TokenEndpointAuthMethod           string   `json:"token_endpoint_auth_method"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
}

// oauthClientFromDocument fetches bounded metadata from public addresses without redirects.
func (s *Service) oauthClientFromDocument(ctx context.Context, clientID string, u *url.URL) (*OAuthClient, error) {
	if !netpolicy.PublicHost(u.Hostname()) {
		return nil, ErrNotFound
	}

	now := s.Now()
	if client, ok := s.metadata.lookup(clientID, now); ok {
		return &client, nil
	}

	ctx, cancel := context.WithTimeout(ctx, oauthMetadataTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clientID, nil)
	if err != nil {
		return nil, ErrNotFound
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", oauthMetadataUserAgent)

	resp, err := s.metadata.httpClient().Do(req)
	if err != nil {
		return nil, ErrNotFound
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, ErrNotFound
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, oauthMetadataMaxBytes+1))
	if err != nil || len(body) > oauthMetadataMaxBytes {
		return nil, ErrNotFound
	}

	var doc oauthMetadataDocument
	if err := json.Unmarshal(body, &doc); err != nil || doc.ClientID != clientID {
		return nil, ErrNotFound
	}
	if doc.TokenEndpointAuthMethodsSupported != nil {
		// The supported-methods list takes precedence over the legacy
		// preference. Hark's token endpoint supports only public PKCE clients,
		// so "none" must be in the intersection (including for ChatGPT, whose
		// legacy preference is private_key_jwt).
		if !slices.Contains(doc.TokenEndpointAuthMethodsSupported, "none") {
			return nil, ErrNotFound
		}
	} else if doc.TokenEndpointAuthMethod != "" && doc.TokenEndpointAuthMethod != "none" {
		return nil, ErrNotFound
	}
	var uris []string
	for _, uri := range doc.RedirectURIs {
		if ValidOAuthRedirectURI(uri) && !slices.Contains(uris, uri) {
			uris = append(uris, uri)
		}
	}
	if len(uris) == 0 {
		return nil, ErrNotFound
	}
	name, ok := oauthClientName(doc.ClientName, uris)
	if !ok {
		name = oauthClientNameFallback(uris)
	}
	client := OAuthClient{
		ID:               clientID,
		Name:             name,
		RedirectURIs:     uris,
		MetadataDocument: true,
	}
	if validOAuthLink(doc.ClientURI) {
		client.ClientURI = &doc.ClientURI
	}
	if ValidAPITokenImageURL(doc.LogoURI) {
		client.LogoURI = &doc.LogoURI
	}
	s.metadata.remember(clientID, client, now)
	return &client, nil
}

// OAuthConsent validates client identity and redirect URI before the remaining parameters.
func (s *Service) OAuthConsent(ctx context.Context, req OAuthAuthorizationRequest, resource string) (*OAuthConsent, error) {
	if req.ClientID == "" {
		return nil, &OAuthClientError{Message: "The request names no client_id."}
	}
	client, err := s.OAuthClientByID(ctx, req.ClientID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, &OAuthClientError{Message: "The client_id is not a registered client or a fetchable client metadata document."}
		}
		return nil, err
	}
	if req.RedirectURI == "" {
		return nil, &OAuthClientError{Message: "The request names no redirect_uri."}
	}
	if !oauthRedirectMatches(client.RedirectURIs, req.RedirectURI) {
		return nil, &OAuthClientError{Message: "The redirect_uri is not one the client registered."}
	}

	switch req.ResponseType {
	case "code":
	case "":
		return nil, &OAuthRedirectError{Code: OAuthErrInvalidRequest, Description: "response_type is required"}
	default:
		return nil, &OAuthRedirectError{Code: OAuthErrUnsupportedResponseType, Description: "response_type must be code"}
	}
	if req.CodeChallenge == "" {
		return nil, &OAuthRedirectError{Code: OAuthErrInvalidRequest, Description: "code_challenge is required"}
	}
	challenge, err := base64.RawURLEncoding.Strict().DecodeString(req.CodeChallenge)
	if err != nil || len(challenge) != sha256.Size || len(req.CodeChallenge) != base64.RawURLEncoding.EncodedLen(sha256.Size) {
		return nil, &OAuthRedirectError{Code: OAuthErrInvalidRequest,
			Description: "code_challenge must be the unpadded base64url encoding of a SHA-256 digest"}
	}
	switch req.CodeChallengeMethod {
	case OAuthCodeChallengeMethod:
	case "":
		return nil, &OAuthRedirectError{Code: OAuthErrInvalidRequest, Description: "code_challenge_method is required"}
	default:
		return nil, &OAuthRedirectError{Code: OAuthErrInvalidRequest, Description: "code_challenge_method must be " + OAuthCodeChallengeMethod}
	}
	scopes := slices.Clone(db.Scopes)
	if fields := strings.Fields(req.Scope); len(fields) > 0 {
		normalized, ok := db.NormalizeScopes(fields)
		if !ok {
			return nil, &OAuthRedirectError{Code: OAuthErrInvalidScope, Description: "scope names something that is not a Hark scope"}
		}
		scopes = normalized
	}
	if len(req.State) > MaxOAuthStateLength {
		return nil, &OAuthRedirectError{Code: OAuthErrInvalidRequest,
			Description: fmt.Sprintf("state must be at most %d characters", MaxOAuthStateLength)}
	}
	var requested *string
	if req.Resource != "" {
		if req.Resource != resource {
			return nil, &OAuthRedirectError{Code: OAuthErrInvalidTarget, Description: "resource is not this deployment's MCP resource identifier"}
		}
		requested = &req.Resource
	}

	return &OAuthConsent{
		Client:        *client,
		RedirectURI:   req.RedirectURI,
		Loopback:      OAuthLoopbackRedirectURI(req.RedirectURI),
		Scopes:        scopes,
		State:         req.State,
		CodeChallenge: req.CodeChallenge,
		Resource:      requested,
	}, nil
}

// ApproveOAuth records the owner's consent and returns the authorization code
// the browser is sent back with. Only its digest is stored.
func (s *Service) ApproveOAuth(ctx context.Context, consent *OAuthConsent, userID string) (string, error) {
	now := s.Now()

	// Opportunistic housekeeping: expired codes leave with the next approval,
	// and a failure here must not fail the request.
	_, _ = s.store.OAuthCodes.Purge(ctx, now)

	code := NewOAuthCode()
	_, err := s.store.OAuthCodes.Create(ctx, db.CreateOAuthCodeParams{
		ID:            id.New(),
		CodeHash:      OAuthCodeHash(code),
		ClientID:      consent.Client.ID,
		ClientName:    consent.Client.Name,
		ClientLogoURI: consent.Client.LogoURI,
		UserID:        userID,
		RedirectURI:   consent.RedirectURI,
		Scopes:        consent.Scopes,
		CodeChallenge: consent.CodeChallenge,
		Resource:      consent.Resource,
		ExpiresAt:     now.Add(OAuthCodeTTL),
		Now:           now,
	})
	if err != nil {
		return "", fmt.Errorf("auth: create OAuth code: %w", err)
	}
	return code, nil
}

// ExchangeOAuthCode consumes a code and issues its token in one transaction.
// Client identity, redirect URI, PKCE, and resource are bound at consent; no metadata is fetched.
// Validation refusals after consumption commit the consumed state to prevent retries.
func (s *Service) ExchangeOAuthCode(ctx context.Context, p ExchangeOAuthCodeParams) (*OAuthTokenGrant, error) {
	switch {
	case p.Code == "":
		return nil, oauthTokenError(OAuthErrInvalidRequest, "code is required")
	case p.ClientID == "":
		return nil, oauthTokenError(OAuthErrInvalidRequest, "client_id is required")
	case p.RedirectURI == "":
		return nil, oauthTokenError(OAuthErrInvalidRequest, "redirect_uri is required")
	case p.CodeVerifier == "":
		return nil, oauthTokenError(OAuthErrInvalidRequest, "code_verifier is required")
	case !ValidOAuthCodeVerifier(p.CodeVerifier):
		return nil, oauthTokenError(OAuthErrInvalidRequest,
			fmt.Sprintf("code_verifier must be %d-%d unreserved characters",
				MinOAuthCodeVerifierLength, MaxOAuthCodeVerifierLength))
	}

	if !ValidOAuthCode(p.Code) {
		// Rejected on shape alone, without touching the database, so a
		// malformed code cannot be used to probe for real ones.
		return nil, oauthTokenError(OAuthErrInvalidGrant, "code is unknown, expired or already used")
	}

	now := s.Now()
	hash := OAuthCodeHash(p.Code)

	var (
		grant   *OAuthTokenGrant
		failure *OAuthTokenError
	)
	err := s.store.Tx(ctx, func(ctx context.Context, tx *db.Store) error {
		code, err := tx.OAuthCodes.ByCodeHash(ctx, hash)
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				failure = oauthTokenError(OAuthErrInvalidGrant, "code is unknown, expired or already used")
				return nil
			}
			return fmt.Errorf("auth: load OAuth code: %w", err)
		}
		consumed, err := tx.OAuthCodes.Consume(ctx, code.ID, now)
		if err != nil {
			return fmt.Errorf("auth: consume OAuth code: %w", err)
		}
		if !consumed {
			failure = oauthTokenError(OAuthErrInvalidGrant, "code is unknown, expired or already used")
			return nil
		}

		// Returning nil commits the consumed code on validation failures.
		switch {
		case code.ClientID != p.ClientID:
			failure = oauthTokenError(OAuthErrInvalidGrant, "code was issued to a different client")
			return nil
		case code.RedirectURI != p.RedirectURI:
			failure = oauthTokenError(OAuthErrInvalidGrant, "code was issued for a different redirect_uri")
			return nil
		case p.Resource != "" && p.Resource != oauthCodeResource(code, p.ExpectedResource):
			failure = oauthTokenError(OAuthErrInvalidGrant, "code was issued for a different resource")
			return nil
		case !OAuthVerifierMatches(code.CodeChallenge, p.CodeVerifier):
			failure = oauthTokenError(OAuthErrInvalidGrant, "code_verifier does not match the code_challenge")
			return nil
		}

		// A registration has to still exist for its code to mint; a
		// metadata-document client has no row to check.
		_, metadataClient := oauthMetadataURL(code.ClientID)
		if !metadataClient {
			if _, err := tx.OAuthClients.ByID(ctx, code.ClientID); err != nil {
				if errors.Is(err, db.ErrNotFound) {
					failure = &OAuthTokenError{Code: OAuthErrInvalidClient, Status: http.StatusUnauthorized,
						Description: "client_id names a registration that no longer exists"}
					return nil
				}
				return fmt.Errorf("auth: load OAuth client: %w", err)
			}
		}

		active, err := tx.APITokens.CountActiveForUpdate(ctx, code.UserID, now)
		if err != nil {
			return fmt.Errorf("auth: count active API tokens: %w", err)
		}
		if active >= db.MaxActiveAPITokens {
			failure = oauthTokenError(OAuthErrInvalidGrant,
				"the account already holds its maximum number of active tokens; revoke one and authorize again")
			return nil
		}

		// No expiry: the token lasts until the owner revokes it on the Tokens
		// page, which is where a client that should lose access is cut off.
		secret := NewAPIToken()
		token, err := tx.APITokens.Create(ctx, db.CreateAPITokenParams{
			ID:        id.New(),
			UserID:    code.UserID,
			Name:      code.ClientName,
			TokenHash: APITokenHash(secret),
			Prefix:    APITokenDisplayPrefix(secret),
			Scopes:    code.Scopes,
			ImageURL:  code.ClientLogoURI,
			Now:       now,
		})
		if err != nil {
			return fmt.Errorf("auth: mint OAuth-granted API token: %w", err)
		}
		if !metadataClient {
			if err := tx.OAuthClients.TouchLastUsed(ctx, code.ClientID, now); err != nil &&
				!errors.Is(err, db.ErrNotFound) {
				return fmt.Errorf("auth: touch OAuth client: %w", err)
			}
		}
		grant = &OAuthTokenGrant{Secret: secret, Token: token, Scopes: token.Scopes}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if failure != nil {
		return nil, failure
	}
	return grant, nil
}

// oauthCodeResource defaults an omitted authorization resource to this deployment.
func oauthCodeResource(code *db.OAuthCode, expected string) string {
	if code.Resource != nil {
		return *code.Resource
	}
	return expected
}

func oauthTokenError(code, description string) *OAuthTokenError {
	return &OAuthTokenError{Code: code, Description: description, Status: http.StatusBadRequest}
}

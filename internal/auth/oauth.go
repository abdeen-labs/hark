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

// OAuth constants. The flow is OAuth 2.1's authorization code grant with PKCE,
// shaped for one account: Hark is both the resource server and the
// authorization server, and the access token it issues is an ordinary API
// token. See docs/api.md § OAuth.
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

	// OAuthCodePrefix marks an authorization code. Like the other secret
	// prefixes it is not a prefix of any other, so a bearer parser can route
	// on it.
	OAuthCodePrefix = "harkcode_"

	// MinOAuthCodeVerifierLength and MaxOAuthCodeVerifierLength bound the PKCE
	// verifier and challenge (RFC 7636 §4.1, §4.2).
	MinOAuthCodeVerifierLength = 43
	MaxOAuthCodeVerifierLength = 128

	// oauthCodeBytes is the entropy behind an authorization code: 32 bytes
	// render as 43 base64url characters.
	oauthCodeBytes = 32

	// oauthClientUnusedRetention is how long a registration that never
	// completed an authorization is kept; oauthClientIdleRetention is how long
	// one is kept after its last exchange.
	oauthClientUnusedRetention = 24 * time.Hour
	oauthClientIdleRetention   = 365 * 24 * time.Hour

	// oauthMetadataTTL is how long a fetched client metadata document is
	// reused before it is fetched again.
	oauthMetadataTTL = 10 * time.Minute

	// oauthMetadataMaxBytes bounds a client metadata document.
	oauthMetadataMaxBytes = 64 << 10

	// oauthMetadataTimeout bounds one fetch of a client metadata document.
	oauthMetadataTimeout = 5 * time.Second

	// oauthMetadataCacheSize bounds the metadata cache. A full cache stops
	// remembering rather than growing: the documents it holds are still valid
	// and a flood of distinct client ids is not worth remembering.
	oauthMetadataCacheSize = 256

	// oauthMetadataUserAgent identifies metadata fetches in a client's log.
	oauthMetadataUserAgent = "Hark-OAuth/1"

	// domainOAuthCode salts the stored digest of an authorization code.
	domainOAuthCode = "hark.oauth-code.v1"
)

// OAuthClient is a client as the consent screen and the token endpoint see it,
// whether it registered or published a metadata document.
type OAuthClient struct {
	// ID is what the client sends as client_id: a registration's id, or the
	// URL of its metadata document.
	ID string
	// Name is shown to the owner and becomes the issued token's name.
	Name         string
	RedirectURIs []string
	ClientURI    *string
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

// OAuthAuthorizationRequest is the query of an authorization request, as the
// client sent it and before anything about it has been checked.
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

// OAuthConsent is a validated authorization request: what the consent screen
// shows, and what an approval turns into a code.
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

// OAuthClientError reports an authorization request the browser must not be
// redirected away from: the client is unknown, or the redirect URI is not one
// it registered. The consent screen shows Message with a 400.
type OAuthClientError struct{ Message string }

func (e *OAuthClientError) Error() string { return "auth: " + e.Message }

// OAuthRedirectError reports an authorization request whose failure is the
// client's to hear about: the consent screen redirects to the registered URI
// with Code and Description in the query.
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

// OAuthTokenGrant is a successful exchange: the token, and its plaintext, which
// exists only here.
type OAuthTokenGrant struct {
	Secret string
	Token  *db.APIToken
	Scopes []string
}

// OAuthTokenError reports a failed exchange in the vocabulary of RFC 6749
// §5.2. Status is the HTTP status the transport answers with.
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

// OAuthRedirectURL builds the URL an authorization response is delivered on:
// redirectURI with values, state and the issuer (RFC 9207) added to its query.
// It is used for both the success and the error response, so a client can
// rely on state and iss being present on either.
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

// ── Codes and PKCE ─────────────────────────────────────────────────────────

// NewOAuthCode returns a fresh authorization code: the prefix and 256 bits of
// randomness in base64url.
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

// ValidOAuthCodeVerifier reports whether v is a PKCE code verifier: 43–128
// unreserved characters (RFC 7636 §4.1). The same alphabet bounds a challenge.
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

// OAuthCodeChallenge derives the S256 challenge of a verifier: base64url,
// unpadded, of its SHA-256 (RFC 7636 §4.2).
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

// ── Redirect URIs ──────────────────────────────────────────────────────────

// ValidOAuthRedirectURI applies the rule every registered or published
// redirect URI must satisfy: absolute, no fragment, and either https with a
// host or http on a loopback host.
func ValidOAuthRedirectURI(raw string) bool {
	if raw == "" || len(raw) > MaxOAuthRedirectURILength || strings.Contains(raw, "#") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Hostname() == "" {
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

// oauthLoopbackHost is the RFC 8252 §7.3 loopback set. Hostnames are
// case-insensitive, so `LOCALHOST` is `localhost`; url.Hostname has already
// stripped the brackets from an IPv6 literal.
func oauthLoopbackHost(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// oauthRedirectMatches reports whether candidate is one of the registered
// URIs. The match is exact, except that a loopback URI may carry any port,
// because a native client binds whichever one is free (RFC 8252 §7.3).
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
			r.Path == c.Path && r.RawQuery == c.RawQuery {
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

// validOAuthLink reports whether raw is an https URL fit to be linked or
// stored: client_uri and logo_uri.
func validOAuthLink(raw string) bool {
	if len(raw) > MaxOAuthRedirectURILength {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != ""
}

// oauthClientName canonicalises a client's display name, reporting whether it
// was usable. The fallback is the host of the first redirect URI, which is
// what the owner would recognise the client by anyway.
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

// ── Registration ───────────────────────────────────────────────────────────

// RegisterOAuthClient records a dynamic registration (RFC 7591). The transport
// must rate-limit this operation: it writes a row for a caller who has proved
// nothing.
//
// Validation failures are [InvalidInputError]s whose Field is "redirect_uris"
// for a problem with the URIs and the offending field's name otherwise, which
// is the distinction the RFC's two error codes draw.
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
		if !validOAuthLink(p.LogoURI) {
			return nil, invalid("logo_uri", "must be an https URL")
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

// ── Client identity ────────────────────────────────────────────────────────

// OAuthClientByID resolves a client_id: a registration's id, or the URL of a
// client metadata document, which is fetched. Unknown, unfetchable and
// invalid are all [ErrNotFound]; which of the three it was is the log's
// business, not the caller's.
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
	}, nil
}

// oauthMetadataURL reports whether a client_id names a metadata document: an
// https URL with a host and a path beyond the root, and nothing a document
// address has no use for (userinfo, a fragment).
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
		// Proxy goes: an environment proxy would resolve and connect to the
		// target itself, outside the dial policy. Every socket is opened by the
		// netpolicy dialer, which resolves the hostname once, requires every
		// address in the answer to be public, and dials one of those literals.
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		transport.DialContext = (&netpolicy.Dialer{}).DialContext
		m.client = newOAuthMetadataClient(transport)
	}
	return m.client
}

// newOAuthMetadataClient builds the client the fetch runs on over transport:
// bounded in time, and following no redirects. A redirect is answered as the
// 3xx it is, which the fetch refuses: the document must live at the URL the
// client claims as its identity, not wherever that URL points today.
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
	ClientID                string   `json:"client_id"`
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	ClientURI               string   `json:"client_uri"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

// oauthClientFromDocument fetches and validates a client metadata document,
// from a public address only, over TLS, following no redirects, reading at
// most oauthMetadataMaxBytes, within oauthMetadataTimeout.
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
	if doc.TokenEndpointAuthMethod != "" && doc.TokenEndpointAuthMethod != "none" {
		return nil, ErrNotFound
	}
	// URIs the rules refuse are dropped rather than failing the document: a
	// client may publish schemes Hark never sends a browser to, and only the
	// ones it could are its identity here.
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
	s.metadata.remember(clientID, client, now)
	return &client, nil
}

// ── Authorization ──────────────────────────────────────────────────────────

// OAuthConsent validates an authorization request for the consent screen.
// resource is this deployment's MCP resource identifier.
//
// The client's identity and its redirect URI are judged first, and a failure
// there is an [OAuthClientError]: the browser stays on the consent screen,
// because sending it to an unverified address is exactly what the check
// exists to prevent. Every other problem is an [OAuthRedirectError], which
// the client hears about on the URI it did register.
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
	if !ValidOAuthCodeVerifier(req.CodeChallenge) {
		return nil, &OAuthRedirectError{Code: OAuthErrInvalidRequest,
			Description: fmt.Sprintf("code_challenge must be %d-%d unreserved characters",
				MinOAuthCodeVerifierLength, MaxOAuthCodeVerifierLength)}
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

// ── Token exchange ─────────────────────────────────────────────────────────

// ExchangeOAuthCode redeems an authorization code for an API token.
//
// The decision runs in one transaction, and the code is what it looks up
// first: a request that does not hold a code the owner issued costs one
// indexed read and nothing else. In particular the client is never resolved
// on the caller's say-so — a metadata document is not fetched here at all,
// because everything the exchange needs from a client, the redirect URI and
// the name the owner saw, was bound to the code at consent — so the token
// endpoint cannot be used to make this server fetch a URL of the caller's
// choosing.
//
// The guarded consume is what authorises the mint, so two requests presenting
// the same code cannot both succeed. A code is spent by its first
// presentation — the consume is committed even when a later check fails —
// because a code that survives a failed exchange is a code an attacker can
// keep trying verifiers against.
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

		// From here on the consume is kept whatever happens: returning nil
		// commits it, and failure carries the verdict out.
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

		active, err := tx.APITokens.CountActive(ctx, code.UserID, now)
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

// oauthCodeResource is the resource a token request must name, when it names
// one: the one the authorization request named, or — when it named none —
// the deployment's own, which is the only resource there is.
func oauthCodeResource(code *db.OAuthCode, expected string) string {
	if code.Resource != nil {
		return *code.Resource
	}
	return expected
}

func oauthTokenError(code, description string) *OAuthTokenError {
	return &OAuthTokenError{Code: code, Description: description, Status: http.StatusBadRequest}
}

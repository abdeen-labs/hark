package httpapi

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strings"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/mcp"
)

// The OAuth surface. Hark is the authorization server for its own MCP
// endpoint, and these are the paths the authorization server metadata
// publishes. See docs/api.md § OAuth.
const (
	// OAuthAuthorizePath is the consent screen, a dashboard page a client sends
	// the owner's browser to. It sits outside the dashboard prefix because it
	// is an address published to clients, like [DeviceVerificationPath].
	OAuthAuthorizePath = "/oauth/authorize"
	// OAuthTokenPath exchanges an authorization code for an API token.
	OAuthTokenPath = "/oauth/token"
	// OAuthRegisterPath registers a client dynamically (RFC 7591).
	OAuthRegisterPath = "/oauth/register"
	// AuthorizationServerMetadataPath publishes the RFC 8414 document.
	AuthorizationServerMetadataPath = "/.well-known/oauth-authorization-server"
)

// Rate-limit ceilings for the two OAuth endpoints an anonymous caller can
// reach: registration because it writes a row for a caller who has proved
// nothing, and the token endpoint because the code in the body is guessable
// in principle.
const (
	limitOAuthRegister = 20
	limitOAuthToken    = 120
)

// The RFC 7591 registration error codes.
const (
	oauthErrInvalidRedirectURI    = "invalid_redirect_uri"
	oauthErrInvalidClientMetadata = "invalid_client_metadata"
)

// oauthErrorResponse is the error form of RFC 6749 §5.2 and RFC 7591 §3.2.2,
// which these two endpoints use instead of the API's envelope: a client
// written against the RFCs reads `error` at the top level.
type oauthErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// oauthRoutes registers the two JSON/form OAuth endpoints. The consent screen
// is the dashboard's, mounted in server.go.
func (s *server) oauthRoutes(rt *router) {
	rt.handle(http.MethodPost, OAuthRegisterPath,
		s.rateLimit("oauth_register", limitOAuthRegister, http.HandlerFunc(s.handleOAuthRegister)))
	rt.handle(http.MethodPost, OAuthTokenPath,
		s.rateLimit("oauth_token", limitOAuthToken, http.HandlerFunc(s.handleOAuthToken)))
}

// setOAuthHeaders marks a response that carries a credential or a verdict
// about one: never cached, and readable by a client that runs in a browser.
func setOAuthHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Access-Control-Allow-Origin", "*")
}

func writeOAuthError(w http.ResponseWriter, r *http.Request, status int, code, description string) {
	setOAuthHeaders(w)
	WriteJSON(w, r, status, oauthErrorResponse{Error: code, ErrorDescription: description})
}

// oauthRegisterRequest is the RFC 7591 metadata Hark reads. Fields the RFC
// defines beyond these are ignored rather than refused, unlike the rest of
// the API: the registering client is somebody else's software, written to the
// RFC and not to this contract.
type oauthRegisterRequest struct {
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	ClientURI               string   `json:"client_uri"`
	LogoURI                 string   `json:"logo_uri"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	Scope                   string   `json:"scope"`
}

type oauthRegisterResponse struct {
	ClientID string `json:"client_id"`
	// ClientIDIssuedAt is Unix seconds, as RFC 7591 §3.2.1 defines it.
	ClientIDIssuedAt        int64    `json:"client_id_issued_at"`
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	ClientURI               *string  `json:"client_uri,omitempty"`
	LogoURI                 *string  `json:"logo_uri,omitempty"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

// handleOAuthRegister registers a public client (RFC 7591). There is no
// client secret to hand back and no way to read a registration again.
func (s *server) handleOAuthRegister(w http.ResponseWriter, r *http.Request) {
	var body oauthRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		var maxBytes *http.MaxBytesError
		var typeErr *json.UnmarshalTypeError
		switch {
		case errors.As(err, &maxBytes):
			WriteError(w, r, http.StatusRequestEntityTooLarge, CodePayloadTooLarge,
				"The request body is larger than the limit.")
		case errors.As(err, &typeErr) && typeErr.Field == "redirect_uris":
			writeOAuthError(w, r, http.StatusBadRequest, oauthErrInvalidRedirectURI,
				"redirect_uris must be an array of strings")
		default:
			writeOAuthError(w, r, http.StatusBadRequest, oauthErrInvalidClientMetadata,
				"the request body must be a JSON object")
		}
		return
	}

	client, err := s.opts.Auth.RegisterOAuthClient(r.Context(), auth.RegisterOAuthClientParams{
		Name:                    body.ClientName,
		RedirectURIs:            body.RedirectURIs,
		ClientURI:               body.ClientURI,
		LogoURI:                 body.LogoURI,
		GrantTypes:              body.GrantTypes,
		ResponseTypes:           body.ResponseTypes,
		TokenEndpointAuthMethod: body.TokenEndpointAuthMethod,
		Scope:                   body.Scope,
	})
	if err != nil {
		var invalid *auth.InvalidInputError
		switch {
		case errors.As(err, &invalid) && invalid.Field == "redirect_uris":
			writeOAuthError(w, r, http.StatusBadRequest, oauthErrInvalidRedirectURI, invalid.Message)
		case errors.As(err, &invalid):
			writeOAuthError(w, r, http.StatusBadRequest, oauthErrInvalidClientMetadata,
				invalid.Field+" "+invalid.Message)
		default:
			s.writeInternal(w, r, "registering an OAuth client failed", err)
		}
		return
	}

	setOAuthHeaders(w)
	WriteJSON(w, r, http.StatusCreated, oauthRegisterResponse{
		ClientID:                client.ID,
		ClientIDIssuedAt:        client.CreatedAt.Unix(),
		ClientName:              client.Name,
		RedirectURIs:            client.RedirectURIs,
		ClientURI:               client.ClientURI,
		LogoURI:                 client.LogoURI,
		GrantTypes:              []string{"authorization_code"},
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: "none",
	})
}

// oauthTokenResponse carries no expires_in: the token it hands over does not
// expire, and RFC 6749 §5.1 lets a server omit the field when that is so.
type oauthTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	// Scope is space-separated, as the RFC renders it.
	Scope string `json:"scope"`
}

// handleOAuthToken exchanges an authorization code for an API token (RFC 6749
// §4.1.3, with PKCE). The body is form-encoded because that is what the RFC
// says and what every client sends.
func (s *server) handleOAuthToken(w http.ResponseWriter, r *http.Request) {
	if !hasFormContentType(r) {
		writeOAuthError(w, r, http.StatusBadRequest, auth.OAuthErrInvalidRequest,
			"the request body must be application/x-www-form-urlencoded")
		return
	}
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, r, http.StatusBadRequest, auth.OAuthErrInvalidRequest,
			"the request body is not a well-formed form")
		return
	}
	form := r.PostForm

	switch form.Get("grant_type") {
	case "authorization_code":
	case "":
		writeOAuthError(w, r, http.StatusBadRequest, auth.OAuthErrInvalidRequest, "grant_type is required")
		return
	default:
		writeOAuthError(w, r, http.StatusBadRequest, auth.OAuthErrUnsupportedGrantType,
			"grant_type must be authorization_code")
		return
	}

	grant, err := s.opts.Auth.ExchangeOAuthCode(r.Context(), auth.ExchangeOAuthCodeParams{
		Code:             form.Get("code"),
		ClientID:         form.Get("client_id"),
		RedirectURI:      form.Get("redirect_uri"),
		CodeVerifier:     form.Get("code_verifier"),
		Resource:         form.Get("resource"),
		ExpectedResource: mcp.Resource(s.opts.PublicURL),
	})
	if err != nil {
		var tokenErr *auth.OAuthTokenError
		if errors.As(err, &tokenErr) {
			writeOAuthError(w, r, tokenErr.Status, tokenErr.Code, tokenErr.Description)
			return
		}
		s.writeInternal(w, r, "exchanging an OAuth code failed", err)
		return
	}

	setOAuthHeaders(w)
	WriteJSON(w, r, http.StatusOK, oauthTokenResponse{
		AccessToken: grant.Secret,
		TokenType:   "Bearer",
		Scope:       strings.Join(grant.Scopes, " "),
	})
}

func hasFormContentType(r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && strings.EqualFold(mediaType, "application/x-www-form-urlencoded")
}

// authorizationServerMetadataDocument is the RFC 8414 document, in the field
// order docs/api.md shows it.
type authorizationServerMetadataDocument struct {
	Issuer                                     string   `json:"issuer"`
	AuthorizationEndpoint                      string   `json:"authorization_endpoint"`
	TokenEndpoint                              string   `json:"token_endpoint"`
	RegistrationEndpoint                       string   `json:"registration_endpoint"`
	ScopesSupported                            []string `json:"scopes_supported"`
	ResponseTypesSupported                     []string `json:"response_types_supported"`
	ResponseModesSupported                     []string `json:"response_modes_supported"`
	GrantTypesSupported                        []string `json:"grant_types_supported"`
	TokenEndpointAuthMethodsSupported          []string `json:"token_endpoint_auth_methods_supported"`
	CodeChallengeMethodsSupported              []string `json:"code_challenge_methods_supported"`
	ClientIDMetadataDocumentSupported          bool     `json:"client_id_metadata_document_supported"`
	AuthorizationResponseIssParameterSupported bool     `json:"authorization_response_iss_parameter_supported"`
	ServiceDocumentation                       string   `json:"service_documentation"`
}

// authorizationServerMetadata serves GET /.well-known/oauth-authorization-server.
// The document depends only on the public origin, so it is rendered once.
func (s *server) authorizationServerMetadata() http.Handler {
	body := mustMarshal(authorizationServerMetadataDocument{
		Issuer:                                     s.publicPath(""),
		AuthorizationEndpoint:                      s.publicPath(OAuthAuthorizePath),
		TokenEndpoint:                              s.publicPath(OAuthTokenPath),
		RegistrationEndpoint:                       s.publicPath(OAuthRegisterPath),
		ScopesSupported:                            db.Scopes,
		ResponseTypesSupported:                     []string{"code"},
		ResponseModesSupported:                     []string{"query"},
		GrantTypesSupported:                        []string{"authorization_code"},
		TokenEndpointAuthMethodsSupported:          []string{"none"},
		CodeChallengeMethodsSupported:              []string{auth.OAuthCodeChallengeMethod},
		ClientIDMetadataDocumentSupported:          true,
		AuthorizationResponseIssParameterSupported: true,
		ServiceDocumentation:                       s.publicPath(DocsPath),
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			methodNotAllowed("GET, HEAD")(w, r)
			return
		}
		h := w.Header()
		h.Set("Content-Type", "application/json")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "public, max-age=300")
		h.Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
}

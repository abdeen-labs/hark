package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/abdeen-labs/hark/internal/db"
)

func decodeOAuthError(t *testing.T, rec *httptest.ResponseRecorder) oauthErrorResponse {
	t.Helper()
	var got oauthErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not the RFC error form: %v\n%s", err, rec.Body.String())
	}
	if got.Error == "" {
		t.Errorf("error form has no error: %s", rec.Body.String())
	}
	return got
}

// assertOAuthHeaders pins the headers every register and token response
// carries: never cached, and readable cross-origin.
func assertOAuthHeaders(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want *", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
}

// postForm sends a form-encoded request, as a token request is.
func postForm(t *testing.T, h http.Handler, target string, form url.Values, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", contentType)
	return send(t, h, req)
}

func TestAuthorizationServerMetadata(t *testing.T) {
	h := newTestServer(t, stubPinger{})

	rec := do(t, h, http.MethodGet, AuthorizationServerMetadataPath, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	for header, want := range map[string]string{
		"Content-Type":                "application/json",
		"Cache-Control":               "public, max-age=300",
		"Access-Control-Allow-Origin": "*",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}

	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for field, want := range map[string]any{
		"issuer":                                         "https://hark.example.com",
		"authorization_endpoint":                         "https://hark.example.com" + OAuthAuthorizePath,
		"token_endpoint":                                 "https://hark.example.com" + OAuthTokenPath,
		"registration_endpoint":                          "https://hark.example.com" + OAuthRegisterPath,
		"service_documentation":                          "https://hark.example.com" + DocsPath,
		"client_id_metadata_document_supported":          true,
		"authorization_response_iss_parameter_supported": true,
	} {
		if got := doc[field]; got != want {
			t.Errorf("%s = %v, want %v", field, got, want)
		}
	}
	for field, want := range map[string][]string{
		"scopes_supported":                      db.Scopes,
		"response_types_supported":              {"code"},
		"response_modes_supported":              {"query"},
		"grant_types_supported":                 {"authorization_code"},
		"token_endpoint_auth_methods_supported": {"none"},
		"code_challenge_methods_supported":      {"S256"},
	} {
		raw, ok := doc[field].([]any)
		if !ok {
			t.Errorf("%s = %v, want an array", field, doc[field])
			continue
		}
		got := make([]string, 0, len(raw))
		for _, v := range raw {
			got = append(got, v.(string))
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s = %v, want %v", field, got, want)
		}
	}

	if rec := do(t, h, http.MethodHead, AuthorizationServerMetadataPath, nil); rec.Code != http.StatusOK {
		t.Errorf("HEAD: status = %d, want 200", rec.Code)
	}
	rec = do(t, h, http.MethodPost, AuthorizationServerMetadataPath, strings.NewReader("{}"))
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") == "" {
		t.Errorf("POST: status = %d, Allow = %q; want 405 with Allow", rec.Code, rec.Header().Get("Allow"))
	}

	// Served outside the credential chain: a header the authenticator would
	// refuse changes nothing here.
	req := httptest.NewRequest(http.MethodGet, AuthorizationServerMetadataPath, nil)
	req.Header.Set("Authorization", "Bearer not-a-credential")
	if rec := send(t, h, req); rec.Code != http.StatusOK {
		t.Errorf("with a junk credential: status = %d, want 200", rec.Code)
	}

	// And the dashboard mount does not swallow it.
	withDashboard := newTestServer(t, stubPinger{}, func(o *Options) {
		o.Dashboard = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
		})
	})
	if rec := do(t, withDashboard, http.MethodGet, AuthorizationServerMetadataPath, nil); rec.Code != http.StatusOK {
		t.Errorf("with a dashboard wired: status = %d, want 200", rec.Code)
	}
}

func TestOAuthRegisterValidation(t *testing.T) {
	h := newTestServer(t, stubPinger{})

	for name, tc := range map[string]struct {
		body string
		code string
	}{
		"not JSON":               {`not json`, oauthErrInvalidClientMetadata},
		"JSON array":             {`[]`, oauthErrInvalidClientMetadata},
		"empty body":             {``, oauthErrInvalidClientMetadata},
		"no redirect_uris":       {`{"client_name":"Claude"}`, oauthErrInvalidRedirectURI},
		"empty redirect_uris":    {`{"redirect_uris":[]}`, oauthErrInvalidRedirectURI},
		"redirect_uris a string": {`{"redirect_uris":"https://example.com/cb"}`, oauthErrInvalidRedirectURI},
		"plain http redirect":    {`{"redirect_uris":["http://example.com/cb"]}`, oauthErrInvalidRedirectURI},
		"too many redirects":     {`{"redirect_uris":[` + strings.Repeat(`"https://example.com/cb",`, 10) + `"https://example.com/x"]}`, oauthErrInvalidRedirectURI},
		"bad client_uri":         {`{"redirect_uris":["https://example.com/cb"],"client_uri":"http://example.com"}`, oauthErrInvalidClientMetadata},
		"long client_name":       {`{"redirect_uris":["https://example.com/cb"],"client_name":"` + strings.Repeat("n", 81) + `"}`, oauthErrInvalidClientMetadata},
		"refresh_token grant":    {`{"redirect_uris":["https://example.com/cb"],"grant_types":["authorization_code","refresh_token"]}`, oauthErrInvalidClientMetadata},
		"token response_type":    {`{"redirect_uris":["https://example.com/cb"],"response_types":["token"]}`, oauthErrInvalidClientMetadata},
		"confidential client":    {`{"redirect_uris":["https://example.com/cb"],"token_endpoint_auth_method":"client_secret_basic"}`, oauthErrInvalidClientMetadata},
		"unknown scope":          {`{"redirect_uris":["https://example.com/cb"],"scope":"devices:read the:moon"}`, oauthErrInvalidClientMetadata},
		// Unknown RFC 7591 fields are ignored: the only complaint is about the
		// URI, not about software_id or contacts.
		"unknown fields": {`{"redirect_uris":["ftp://example.com/cb"],"software_id":"abc","contacts":["a@example.com"],"jwks":{}}`, oauthErrInvalidRedirectURI},
	} {
		t.Run(name, func(t *testing.T) {
			rec := do(t, h, http.MethodPost, OAuthRegisterPath, strings.NewReader(tc.body))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
			}
			assertOAuthHeaders(t, rec)
			if got := decodeOAuthError(t, rec); got.Error != tc.code {
				t.Errorf("error = %q, want %q", got.Error, tc.code)
			}
		})
	}

	// The content type is not policed: the body is parsed as JSON whatever
	// the client called it.
	req := httptest.NewRequest(http.MethodPost, OAuthRegisterPath, strings.NewReader(`{"redirect_uris":[]}`))
	rec := send(t, h, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("no content type: status = %d, want 400 from validation: %s", rec.Code, rec.Body)
	}
	if got := decodeOAuthError(t, rec); got.Error != oauthErrInvalidRedirectURI {
		t.Errorf("no content type: error = %q, want %q", got.Error, oauthErrInvalidRedirectURI)
	}
}

func TestOAuthTokenRequestShape(t *testing.T) {
	h := newTestServer(t, stubPinger{})
	verifier := strings.Repeat("v", 43)
	const form = "application/x-www-form-urlencoded"

	for name, tc := range map[string]struct {
		form        url.Values
		contentType string
		status      int
		code        string
	}{
		"JSON body": {url.Values{"grant_type": {"authorization_code"}}, "application/json",
			http.StatusBadRequest, "invalid_request"},
		"no content type": {url.Values{"grant_type": {"authorization_code"}}, "",
			http.StatusBadRequest, "invalid_request"},
		"no grant_type": {url.Values{"code": {"x"}}, form,
			http.StatusBadRequest, "invalid_request"},
		"client_credentials": {url.Values{"grant_type": {"client_credentials"}}, form,
			http.StatusBadRequest, "unsupported_grant_type"},
		"no code": {url.Values{"grant_type": {"authorization_code"}, "client_id": {"x"},
			"redirect_uri": {"https://example.com/cb"}, "code_verifier": {verifier}}, form,
			http.StatusBadRequest, "invalid_request"},
		"no code_verifier": {url.Values{"grant_type": {"authorization_code"}, "code": {"x"}, "client_id": {"x"},
			"redirect_uri": {"https://example.com/cb"}}, form,
			http.StatusBadRequest, "invalid_request"},
		"short code_verifier": {url.Values{"grant_type": {"authorization_code"}, "code": {"x"}, "client_id": {"x"},
			"redirect_uri": {"https://example.com/cb"}, "code_verifier": {"short"}}, form,
			http.StatusBadRequest, "invalid_request"},
		"malformed code": {url.Values{"grant_type": {"authorization_code"}, "code": {"x"}, "client_id": {"nobody"},
			"redirect_uri": {"https://example.com/cb"}, "code_verifier": {verifier}}, form,
			http.StatusBadRequest, "invalid_grant"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := postForm(t, h, OAuthTokenPath, tc.form, tc.contentType)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}
			assertOAuthHeaders(t, rec)
			if got := decodeOAuthError(t, rec); got.Error != tc.code {
				t.Errorf("error = %q, want %q", got.Error, tc.code)
			}
		})
	}

	// Parameters in the query string are not the body: RFC 6749 §4.1.3 puts
	// them in the entity, and a code in a URL ends up in a log.
	req := httptest.NewRequest(http.MethodPost, OAuthTokenPath+"?grant_type=authorization_code", strings.NewReader(""))
	req.Header.Set("Content-Type", form)
	rec := send(t, h, req)
	if got := decodeOAuthError(t, rec); rec.Code != http.StatusBadRequest || got.Error != "invalid_request" {
		t.Errorf("query-string grant_type: status = %d, error = %q; want 400 invalid_request", rec.Code, got.Error)
	}
}

// TestOAuthEndpointsAreRateLimited pins the ceilings docs/api.md publishes,
// and that the refusal is the API's standard envelope.
func TestOAuthEndpointsAreRateLimited(t *testing.T) {
	h := newTestServer(t, stubPinger{}, func(o *Options) { o.TrustedClientIPHeader = "X-Real-IP" })

	for _, tc := range []struct {
		path  string
		limit int
		send  func() *http.Request
	}{
		{OAuthRegisterPath, limitOAuthRegister, func() *http.Request {
			return httptest.NewRequest(http.MethodPost, OAuthRegisterPath, strings.NewReader("not json"))
		}},
		{OAuthTokenPath, limitOAuthToken, func() *http.Request {
			req := httptest.NewRequest(http.MethodPost, OAuthTokenPath, strings.NewReader("grant_type=x"))
			req.Header.Set("Content-Type", "application/json")
			return req
		}},
	} {
		for i := 1; i <= tc.limit; i++ {
			req := tc.send()
			req.Header.Set("X-Real-IP", "198.51.100.7")
			if rec := send(t, h, req); rec.Code != http.StatusBadRequest {
				t.Fatalf("%s request %d: status = %d, want 400 below the ceiling", tc.path, i, rec.Code)
			}
		}
		req := tc.send()
		req.Header.Set("X-Real-IP", "198.51.100.7")
		rec := send(t, h, req)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("%s past the ceiling: status = %d, want 429", tc.path, rec.Code)
		}
		if got := decodeError(t, rec); got.Error.Code != CodeRateLimited {
			t.Errorf("%s: code = %q, want %q", tc.path, got.Error.Code, CodeRateLimited)
		}
		if rec.Header().Get("Retry-After") == "" {
			t.Errorf("%s: no Retry-After on the refusal", tc.path)
		}
		// Another caller keeps its own allowance.
		other := tc.send()
		other.Header.Set("X-Real-IP", "198.51.100.8")
		if rec := send(t, h, other); rec.Code != http.StatusBadRequest {
			t.Errorf("%s from another client: status = %d, want 400", tc.path, rec.Code)
		}
	}
}

func TestOAuthPreflight(t *testing.T) {
	h := newTestServer(t, stubPinger{})
	for _, path := range []string{OAuthRegisterPath, OAuthTokenPath} {
		req := httptest.NewRequest(http.MethodOptions, path, nil)
		req.Header.Set("Origin", "https://inspector.example")
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "content-type")
		rec := send(t, h, req)
		if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "*" || rec.Header().Get("Access-Control-Allow-Methods") != "POST, OPTIONS" || rec.Header().Get("Access-Control-Allow-Headers") != "Content-Type" {
			t.Errorf("%s preflight: status=%d headers=%v", path, rec.Code, rec.Header())
		}
	}
}

func TestOAuthTokenRejectsDuplicateParameters(t *testing.T) {
	h := newTestServer(t, stubPinger{})
	for _, field := range []string{"grant_type", "code", "client_id", "redirect_uri", "code_verifier", "resource"} {
		form := url.Values{"grant_type": {"authorization_code"}, "code": {"malformed"}, "client_id": {"client"}, "redirect_uri": {"https://example.com/cb"}, "code_verifier": {strings.Repeat("v", 43)}, "resource": {"https://hark.example.com/mcp"}}
		form.Add(field, form.Get(field))
		rec := postForm(t, h, OAuthTokenPath, form, "application/x-www-form-urlencoded")
		if got := decodeOAuthError(t, rec); rec.Code != http.StatusBadRequest || got.Error != "invalid_request" {
			t.Errorf("duplicate %s: status=%d error=%q", field, rec.Code, got.Error)
		}
	}
}

func TestOAuthRegistrationRejectsTrailingJSON(t *testing.T) {
	h := newTestServer(t, stubPinger{})
	for _, suffix := range []string{" {}", " garbage"} {
		rec := do(t, h, http.MethodPost, OAuthRegisterPath, strings.NewReader(`{"redirect_uris":["https://example.com/cb"]}`+suffix))
		if got := decodeOAuthError(t, rec); rec.Code != http.StatusBadRequest || got.Error != oauthErrInvalidClientMetadata {
			t.Errorf("trailing %q: status=%d error=%q", suffix, rec.Code, got.Error)
		}
	}
}

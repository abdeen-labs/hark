package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abdeen-labs/hark/internal/db"
)

const (
	testClientID    = "https://client.example/oauth/client.json"
	testRedirectURI = "https://client.example/callback"
	testResource    = "https://hark.example.com/mcp"
)

// documentTransport answers metadata fetches from memory and counts them.
type documentTransport struct {
	mu        sync.Mutex
	responses map[string]*http.Response
	requests  []*http.Request
}

func (d *documentTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.requests = append(d.requests, r)
	resp, ok := d.responses[r.URL.String()]
	if !ok {
		return nil, errors.New("no such document")
	}
	// A response body can only be read once; hand out a fresh copy.
	body, _ := io.ReadAll(resp.Body)
	resp.Body = io.NopCloser(strings.NewReader(string(body)))
	return &http.Response{
		StatusCode: resp.StatusCode,
		Header:     resp.Header.Clone(),
		Body:       io.NopCloser(strings.NewReader(string(body))),
		Request:    r,
	}, nil
}

func (d *documentTransport) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.requests)
}

func document(status int, body string, header http.Header) *http.Response {
	if header == nil {
		header = http.Header{"Content-Type": {"application/json"}}
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}
}

const validDocument = `{
	"client_id": "` + testClientID + `",
	"client_name": "Example Agent",
	"client_uri": "https://client.example",
	"redirect_uris": ["` + testRedirectURI + `", "http://localhost:8080/cb", "custom://cb"],
	"token_endpoint_auth_method": "none",
	"grant_types": ["authorization_code"],
	"unknown_field": {"nested": true}
}`

func newDocumentService(t *testing.T, docs map[string]*http.Response) (*Service, *documentTransport, *testClock) {
	t.Helper()
	clock := &testClock{now: time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)}
	transport := &documentTransport{responses: docs}
	s := New(db.New(nil), clock.Now)
	s.UseMetadataClient(newOAuthMetadataClient(transport))
	return s, transport, clock
}

func TestRedirectURIRules(t *testing.T) {
	for uri, want := range map[string]bool{
		"https://claude.ai/api/mcp/auth_callback": true,
		"https://example.com/cb?state=x":          true,
		"http://localhost/cb":                     true,
		"http://localhost:8080/cb":                true,
		"http://127.0.0.1:1234/callback":          true,
		"http://[::1]:9000/cb":                    true,
		"http://LOCALHOST/cb":                     true,
		"http://user@localhost/cb":                false,
		"https://user:password@example.com/cb":    false,
		"https://example.com/cb#fragment":         false,
		"https://example.com/cb#":                 false,
		"http://example.com/cb":                   false,
		"http://10.0.0.1/cb":                      false,
		"http://localhost.evil.com/cb":            false,
		"custom://cb":                             false,
		"/relative/cb":                            false,
		"https:///nohost":                         false,
		"":                                        false,
		"https://example.com/" + strings.Repeat("a", MaxOAuthRedirectURILength): false,
	} {
		if got := ValidOAuthRedirectURI(uri); got != want {
			t.Errorf("ValidOAuthRedirectURI(%q) = %v, want %v", uri, got, want)
		}
	}

	registered := []string{"https://example.com/cb", "http://127.0.0.1/cb", "http://localhost:3000/cb?x=1"}
	for candidate, want := range map[string]bool{
		"https://example.com/cb":           true,
		"https://example.com/cb/":          false,
		"https://example.com/cb?extra=1":   false,
		"https://example.com:444/cb":       false,
		"http://127.0.0.1/cb":              true,
		"http://127.0.0.1:51234/cb":        true,
		"http://localhost:51234/cb?x=1":    true,
		"http://localhost:51234/cb":        false,
		"http://localhost:51234/other":     false,
		"http://127.0.0.1:51234/%63b":      false,
		"http://user@127.0.0.1:51234/cb":   false,
		"http://[::1]:51234/cb":            false,
		"https://127.0.0.1:51234/cb":       false,
		"http://127.0.0.1:51234/cb#frag":   false,
		"http://evil.example/cb":           false,
		"http://127.0.0.1.evil.example/cb": false,
	} {
		if got := oauthRedirectMatches(registered, candidate); got != want {
			t.Errorf("oauthRedirectMatches(%q) = %v, want %v", candidate, got, want)
		}
	}

	for uri, want := range map[string]bool{
		"http://localhost:8080/cb":     true,
		"http://127.0.0.1/cb":          true,
		"http://[::1]:9000/cb":         true,
		"https://example.com/cb":       false,
		"https://localhost.example/cb": false,
	} {
		if got := OAuthLoopbackRedirectURI(uri); got != want {
			t.Errorf("OAuthLoopbackRedirectURI(%q) = %v, want %v", uri, got, want)
		}
	}
}

func TestPKCE(t *testing.T) {
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	// The worked example of RFC 7636 appendix B.
	const challenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if got := OAuthCodeChallenge(verifier); got != challenge {
		t.Errorf("OAuthCodeChallenge = %q, want %q", got, challenge)
	}
	if !OAuthVerifierMatches(challenge, verifier) {
		t.Error("the RFC's verifier does not satisfy its challenge")
	}
	if OAuthVerifierMatches(challenge, verifier[:len(verifier)-1]+"X") {
		t.Error("a one-character-off verifier was accepted")
	}
	if OAuthVerifierMatches(challenge+"=", verifier) {
		t.Error("a padded challenge was accepted")
	}

	for v, want := range map[string]bool{
		verifier:                      true,
		strings.Repeat("a", 43):       true,
		strings.Repeat("a", 128):      true,
		strings.Repeat("A0-._~", 8):   true,
		strings.Repeat("a", 42):       false,
		strings.Repeat("a", 129):      false,
		strings.Repeat("a", 42) + "+": false,
		strings.Repeat("a", 42) + "/": false,
		strings.Repeat("a", 42) + " ": false,
		strings.Repeat("a", 42) + "é": false,
		"":                            false,
	} {
		if got := ValidOAuthCodeVerifier(v); got != want {
			t.Errorf("ValidOAuthCodeVerifier(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestOAuthCodeShape(t *testing.T) {
	code := NewOAuthCode()
	if !ValidOAuthCode(code) {
		t.Fatalf("NewOAuthCode() = %q does not pass its own shape check", code)
	}
	if !strings.HasPrefix(code, OAuthCodePrefix) {
		t.Errorf("code %q lacks the prefix", code)
	}
	if NewOAuthCode() == code {
		t.Error("two codes were equal")
	}
	if OAuthCodeHash(code) == code || OAuthCodeHash(code) == DeviceCodeHash(code) {
		t.Error("the code digest is not domain-separated")
	}
	for _, bad := range []string{
		"", "harkcode_", code[:len(code)-1], code + "a",
		strings.Replace(code, OAuthCodePrefix, DeviceCodePrefix, 1),
		code[:len(code)-1] + "+", code[:len(code)-1] + "=",
	} {
		if ValidOAuthCode(bad) {
			t.Errorf("ValidOAuthCode(%q) = true", bad)
		}
	}
	// No secret prefix is a prefix of another, so bearer parsing can route
	// on them in any order.
	for _, other := range []string{APITokenPrefix, SessionTokenPrefix, DeviceCodePrefix, WebhookTokenPrefix, ResponseTokenPrefix} {
		if strings.HasPrefix(OAuthCodePrefix, other) || strings.HasPrefix(other, OAuthCodePrefix) {
			t.Errorf("prefix %q collides with %q", OAuthCodePrefix, other)
		}
	}
}

func TestOAuthRedirectURL(t *testing.T) {
	got := OAuthRedirectURL("https://client.example/cb?keep=1&code=stale", "https://hark.example.com",
		"abc", url.Values{"code": {"fresh"}})
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("result %q is not a URL: %v", got, err)
	}
	q := u.Query()
	if u.Scheme != "https" || u.Host != "client.example" || u.Path != "/cb" {
		t.Errorf("redirect target = %s://%s%s, want the registered URI", u.Scheme, u.Host, u.Path)
	}
	if q.Get("keep") != "1" {
		t.Error("the registered URI's own query was dropped")
	}
	if got := q["code"]; len(got) != 1 || got[0] != "fresh" {
		t.Errorf("code = %v, want the new value only", got)
	}
	if q.Get("state") != "abc" || q.Get("iss") != "https://hark.example.com" {
		t.Errorf("state = %q, iss = %q; want both echoed", q.Get("state"), q.Get("iss"))
	}

	// No state means no state parameter, and iss is always present.
	u, _ = url.Parse(OAuthRedirectURL("http://127.0.0.1:5000/cb", "https://hark.example.com", "",
		url.Values{"error": {"access_denied"}}))
	if _, present := u.Query()["state"]; present {
		t.Error("an empty state was echoed")
	}
	if u.Query().Get("iss") == "" || u.Query().Get("error") != "access_denied" {
		t.Errorf("query = %v, want iss and error", u.Query())
	}
}

func TestClientMetadataDocument(t *testing.T) {
	s, transport, clock := newDocumentService(t, map[string]*http.Response{
		testClientID: document(http.StatusOK, validDocument, nil),
	})
	ctx := context.Background()

	client, err := s.OAuthClientByID(ctx, testClientID)
	if err != nil {
		t.Fatalf("OAuthClientByID: %v", err)
	}
	if !client.MetadataDocument || client.ID != testClientID {
		t.Errorf("client = %+v, want a metadata-document client", client)
	}
	if client.Name != "Example Agent" {
		t.Errorf("Name = %q", client.Name)
	}
	if client.ClientURI == nil || *client.ClientURI != "https://client.example" {
		t.Errorf("ClientURI = %v", client.ClientURI)
	}
	// The custom-scheme URI is dropped; the two the rules accept are kept.
	if want := []string{testRedirectURI, "http://localhost:8080/cb"}; !equalStrings(client.RedirectURIs, want) {
		t.Errorf("RedirectURIs = %v, want %v", client.RedirectURIs, want)
	}

	req := transport.requests[0]
	if req.Method != http.MethodGet || req.Header.Get("Accept") != "application/json" {
		t.Errorf("fetched with %s and Accept %q", req.Method, req.Header.Get("Accept"))
	}

	// Cached: a second resolution inside the TTL makes no request, and one
	// past it does.
	if _, err := s.OAuthClientByID(ctx, testClientID); err != nil || transport.count() != 1 {
		t.Errorf("second resolution: err = %v, requests = %d; want the cached document", err, transport.count())
	}
	clock.Advance(oauthMetadataTTL + time.Second)
	if _, err := s.OAuthClientByID(ctx, testClientID); err != nil || transport.count() != 2 {
		t.Errorf("resolution past the TTL: err = %v, requests = %d; want a fresh fetch", err, transport.count())
	}
}

func TestClientMetadataDocumentIsValidated(t *testing.T) {
	huge := `{"client_id":"` + testClientID + `","redirect_uris":["` + testRedirectURI + `"],"padding":"` +
		strings.Repeat("x", oauthMetadataMaxBytes) + `"}`
	redirected := http.Header{"Location": {"https://elsewhere.example/client.json"}}

	for name, tc := range map[string]struct {
		response *http.Response
		fetches  int
	}{
		"client_id mismatch": {document(http.StatusOK,
			`{"client_id":"https://client.example/other.json","redirect_uris":["`+testRedirectURI+`"]}`, nil), 1},
		"no redirect_uris": {document(http.StatusOK, `{"client_id":"`+testClientID+`"}`, nil), 1},
		"only unusable redirect_uris": {document(http.StatusOK,
			`{"client_id":"`+testClientID+`","redirect_uris":["custom://cb","http://example.com/cb"]}`, nil), 1},
		"confidential client": {document(http.StatusOK,
			`{"client_id":"`+testClientID+`","redirect_uris":["`+testRedirectURI+`"],"token_endpoint_auth_method":"client_secret_basic"}`, nil), 1},
		"not JSON":    {document(http.StatusOK, `<html>`, nil), 1},
		"not found":   {document(http.StatusNotFound, `{}`, nil), 1},
		"redirect":    {document(http.StatusFound, ``, redirected), 1},
		"oversized":   {document(http.StatusOK, huge, nil), 1},
		"unreachable": {nil, 1},
	} {
		t.Run(name, func(t *testing.T) {
			docs := map[string]*http.Response{}
			if tc.response != nil {
				docs[testClientID] = tc.response
			}
			s, transport, _ := newDocumentService(t, docs)
			if _, err := s.OAuthClientByID(context.Background(), testClientID); !errors.Is(err, ErrNotFound) {
				t.Errorf("OAuthClientByID = %v, want ErrNotFound", err)
			}
			if transport.count() != tc.fetches {
				t.Errorf("fetched %d times, want %d", transport.count(), tc.fetches)
			}
		})
	}

	// A refused document is not remembered: the next request tries again.
	s, transport, _ := newDocumentService(t, map[string]*http.Response{
		testClientID: document(http.StatusInternalServerError, ``, nil),
	})
	for range 2 {
		if _, err := s.OAuthClientByID(context.Background(), testClientID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("OAuthClientByID = %v, want ErrNotFound", err)
		}
	}
	if transport.count() != 2 {
		t.Errorf("a failed fetch was cached: %d requests", transport.count())
	}
}

func TestClientMetadataDocumentHostMustBePublic(t *testing.T) {
	for _, clientID := range []string{
		"https://127.0.0.1/oauth/client.json",
		"https://localhost/oauth/client.json",
		"https://[::1]/client.json",
		"https://10.0.0.5/client.json",
		"https://192.168.1.1:8443/client.json",
		"https://printer.local/client.json",
		"https://169.254.169.254/latest/meta-data",
	} {
		s, transport, _ := newDocumentService(t, map[string]*http.Response{
			clientID: document(http.StatusOK, strings.ReplaceAll(validDocument, testClientID, clientID), nil),
		})
		if _, err := s.OAuthClientByID(context.Background(), clientID); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: OAuthClientByID = %v, want ErrNotFound", clientID, err)
		}
		if transport.count() != 0 {
			t.Errorf("%s: a request was made to a non-public host", clientID)
		}
	}

	// Things that are not metadata documents at all are not fetched either.
	s, transport, _ := newDocumentService(t, nil)
	for _, clientID := range []string{
		"http://client.example/client.json",
		"https://client.example",
		"https://client.example/",
		"https://user:pw@client.example/client.json",
		"https://client.example/client.json#frag",
		"not-a-client",
		"",
	} {
		if _, err := s.OAuthClientByID(context.Background(), clientID); !errors.Is(err, ErrNotFound) {
			t.Errorf("%q: OAuthClientByID = %v, want ErrNotFound", clientID, err)
		}
	}
	if transport.count() != 0 {
		t.Errorf("%d requests were made for ids that are not document URLs", transport.count())
	}
}

func validRequest() OAuthAuthorizationRequest {
	return OAuthAuthorizationRequest{
		ResponseType:        "code",
		ClientID:            testClientID,
		RedirectURI:         testRedirectURI,
		Scope:               "notifications:send devices:read notifications:send",
		State:               "xyz",
		CodeChallenge:       OAuthCodeChallenge(strings.Repeat("v", 43)),
		CodeChallengeMethod: "S256",
		Resource:            testResource,
	}
}

func TestOAuthConsentValidatesTheRequest(t *testing.T) {
	s, _, _ := newDocumentService(t, map[string]*http.Response{
		testClientID: document(http.StatusOK, validDocument, nil),
	})
	ctx := context.Background()

	consent, err := s.OAuthConsent(ctx, validRequest(), testResource)
	if err != nil {
		t.Fatalf("OAuthConsent: %v", err)
	}
	if consent.Client.ID != testClientID || consent.RedirectURI != testRedirectURI || consent.Loopback {
		t.Errorf("consent = %+v", consent)
	}
	if want := []string{"devices:read", "notifications:send"}; !equalStrings(consent.Scopes, want) {
		t.Errorf("Scopes = %v, want %v deduplicated and sorted", consent.Scopes, want)
	}
	if consent.State != "xyz" || consent.CodeChallenge != validRequest().CodeChallenge {
		t.Errorf("state/challenge not carried: %+v", consent)
	}
	if consent.Resource == nil || *consent.Resource != testResource {
		t.Errorf("Resource = %v, want the requested one", consent.Resource)
	}

	// No scope means every scope; no resource means none recorded; a loopback
	// redirect on another port is accepted and labelled.
	req := validRequest()
	req.Scope, req.Resource, req.State = "", "", ""
	req.RedirectURI = "http://localhost:51234/cb"
	consent, err = s.OAuthConsent(ctx, req, testResource)
	if err != nil {
		t.Fatalf("OAuthConsent(minimal): %v", err)
	}
	if !equalStrings(consent.Scopes, db.Scopes) {
		t.Errorf("Scopes = %v, want every scope", consent.Scopes)
	}
	if consent.Resource != nil || !consent.Loopback {
		t.Errorf("consent = %+v, want no resource and Loopback", consent)
	}

	// Client and redirect problems stay on the page.
	for name, mutate := range map[string]func(*OAuthAuthorizationRequest){
		"no client":             func(r *OAuthAuthorizationRequest) { r.ClientID = "" },
		"unknown client":        func(r *OAuthAuthorizationRequest) { r.ClientID = "https://other.example/client.json" },
		"no redirect":           func(r *OAuthAuthorizationRequest) { r.RedirectURI = "" },
		"unregistered redirect": func(r *OAuthAuthorizationRequest) { r.RedirectURI = "https://client.example/other" },
		"redirect port drift":   func(r *OAuthAuthorizationRequest) { r.RedirectURI = "https://client.example:444/callback" },
		"dropped custom scheme": func(r *OAuthAuthorizationRequest) { r.RedirectURI = "custom://cb" },
	} {
		req := validRequest()
		mutate(&req)
		_, err := s.OAuthConsent(ctx, req, testResource)
		var clientErr *OAuthClientError
		if !errors.As(err, &clientErr) {
			t.Errorf("%s: OAuthConsent = %v, want an OAuthClientError", name, err)
		}
	}

	// Everything else is the client's to hear about.
	for name, tc := range map[string]struct {
		mutate func(*OAuthAuthorizationRequest)
		code   string
	}{
		"no response_type":            {func(r *OAuthAuthorizationRequest) { r.ResponseType = "" }, OAuthErrInvalidRequest},
		"token response_type":         {func(r *OAuthAuthorizationRequest) { r.ResponseType = "token" }, OAuthErrUnsupportedResponseType},
		"no challenge":                {func(r *OAuthAuthorizationRequest) { r.CodeChallenge = "" }, OAuthErrInvalidRequest},
		"short challenge":             {func(r *OAuthAuthorizationRequest) { r.CodeChallenge = "short" }, OAuthErrInvalidRequest},
		"long challenge":              {func(r *OAuthAuthorizationRequest) { r.CodeChallenge = strings.Repeat("a", 64) }, OAuthErrInvalidRequest},
		"verifier alphabet challenge": {func(r *OAuthAuthorizationRequest) { r.CodeChallenge = strings.Repeat("~", 43) }, OAuthErrInvalidRequest},
		"noncanonical challenge":      {func(r *OAuthAuthorizationRequest) { r.CodeChallenge = strings.Repeat("a", 43) }, OAuthErrInvalidRequest},
		"no method":                   {func(r *OAuthAuthorizationRequest) { r.CodeChallengeMethod = "" }, OAuthErrInvalidRequest},
		"plain method":                {func(r *OAuthAuthorizationRequest) { r.CodeChallengeMethod = "plain" }, OAuthErrInvalidRequest},
		"unknown scope":               {func(r *OAuthAuthorizationRequest) { r.Scope = "devices:read the:moon" }, OAuthErrInvalidScope},
		"long state":                  {func(r *OAuthAuthorizationRequest) { r.State = strings.Repeat("s", MaxOAuthStateLength+1) }, OAuthErrInvalidRequest},
		"foreign resource":            {func(r *OAuthAuthorizationRequest) { r.Resource = "https://other.example/mcp" }, OAuthErrInvalidTarget},
		"resource with slash":         {func(r *OAuthAuthorizationRequest) { r.Resource = testResource + "/" }, OAuthErrInvalidTarget},
	} {
		req := validRequest()
		tc.mutate(&req)
		_, err := s.OAuthConsent(ctx, req, testResource)
		var redirectErr *OAuthRedirectError
		if !errors.As(err, &redirectErr) {
			t.Errorf("%s: OAuthConsent = %v, want an OAuthRedirectError", name, err)
			continue
		}
		if redirectErr.Code != tc.code {
			t.Errorf("%s: code = %q, want %q", name, redirectErr.Code, tc.code)
		}
	}
}

func TestExchangeRefusesMalformedRequestsWithoutTheStore(t *testing.T) {
	s, _, _ := newDocumentService(t, map[string]*http.Response{
		testClientID: document(http.StatusOK, validDocument, nil),
	})
	ctx := context.Background()
	verifier := strings.Repeat("v", 43)

	for name, tc := range map[string]struct {
		p      ExchangeOAuthCodeParams
		code   string
		status int
	}{
		"no code": {ExchangeOAuthCodeParams{ClientID: testClientID, RedirectURI: testRedirectURI, CodeVerifier: verifier},
			OAuthErrInvalidRequest, 400},
		"no client": {ExchangeOAuthCodeParams{Code: NewOAuthCode(), RedirectURI: testRedirectURI, CodeVerifier: verifier},
			OAuthErrInvalidRequest, 400},
		"no redirect": {ExchangeOAuthCodeParams{Code: NewOAuthCode(), ClientID: testClientID, CodeVerifier: verifier},
			OAuthErrInvalidRequest, 400},
		"no verifier": {ExchangeOAuthCodeParams{Code: NewOAuthCode(), ClientID: testClientID, RedirectURI: testRedirectURI},
			OAuthErrInvalidRequest, 400},
		"short verifier": {ExchangeOAuthCodeParams{Code: NewOAuthCode(), ClientID: testClientID, RedirectURI: testRedirectURI, CodeVerifier: "short"},
			OAuthErrInvalidRequest, 400},
		"malformed code": {ExchangeOAuthCodeParams{Code: "not-a-code", ClientID: testClientID, RedirectURI: testRedirectURI, CodeVerifier: verifier},
			OAuthErrInvalidGrant, 400},
	} {
		_, err := s.ExchangeOAuthCode(ctx, tc.p)
		var tokenErr *OAuthTokenError
		if !errors.As(err, &tokenErr) {
			t.Errorf("%s: ExchangeOAuthCode = %v, want an OAuthTokenError", name, err)
			continue
		}
		if tokenErr.Code != tc.code || tokenErr.Status != tc.status {
			t.Errorf("%s: = %s %d, want %s %d", name, tokenErr.Code, tokenErr.Status, tc.code, tc.status)
		}
	}
}

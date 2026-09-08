package dashboard

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/mcp"
)

const (
	consentClientID    = "https://client.example/.well-known/oauth-client.json"
	consentRedirectURI = "https://client.example/callback"
	consentState       = "xyz-123"
	consentIssuer      = "https://hark.example.com"
)

// consentRequest is an authorization request as an MCP client sends it.
func consentRequest() url.Values {
	return url.Values{
		"response_type":         {"code"},
		"client_id":             {consentClientID},
		"redirect_uri":          {consentRedirectURI},
		"scope":                 {db.ScopeNotificationsNew + " " + db.ScopeInteractionsRead},
		"state":                 {consentState},
		"code_challenge":        {strings.Repeat("a", 43)},
		"code_challenge_method": {"S256"},
		"resource":              {consentIssuer + "/mcp"},
	}
}

// grantedConsent is what the service hands back for [consentRequest] once the
// client and everything it asked for check out.
func grantedConsent() *auth.OAuthConsent {
	return &auth.OAuthConsent{
		Client: auth.OAuthClient{
			ID:               consentClientID,
			Name:             "Claude",
			RedirectURIs:     []string{consentRedirectURI},
			ClientURI:        ptr("https://client.example"),
			MetadataDocument: true,
		},
		RedirectURI:   consentRedirectURI,
		Scopes:        []string{db.ScopeNotificationsNew, db.ScopeInteractionsRead},
		State:         consentState,
		CodeChallenge: strings.Repeat("a", 43),
		Resource:      ptr(consentIssuer + "/mcp"),
	}
}

// consentTarget is the consent screen's URL for [consentRequest].
func consentTarget() string {
	return pathOAuthAuthorize + "?" + consentRequest().Encode()
}

// clientRedirect checks that a response sends the browser to the client's
// redirect URI and returns what it carries there.
func clientRedirect(t *testing.T, rec *httptest.ResponseRecorder) url.Values {
	t.Helper()
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusSeeOther, rec.Body)
	}
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("Location is not a URL: %v", err)
	}
	base := *location
	base.RawQuery = ""
	if base.String() != consentRedirectURI {
		t.Fatalf("Location = %q, want the client's redirect URI %q", location, consentRedirectURI)
	}
	return location.Query()
}

// TestConsentSendsASignedOutBrowserToSignInKeepingTheRequest follows the whole
// detour: the request has to survive the redirect to sign-in and the sign-in
// itself, because it exists nowhere but in that query.
func TestConsentSendsASignedOutBrowserToSignInKeepingTheRequest(t *testing.T) {
	d, _ := newTestDashboard(t)

	rec := send(d, request(http.MethodGet, consentTarget(), ""))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("Location is not a URL: %v", err)
	}
	if location.Path != pathLogin {
		t.Fatalf("Location = %q, want the sign-in page", location)
	}

	next, err := url.Parse(location.Query().Get("next"))
	if err != nil {
		t.Fatalf("next is not a URL: %v", err)
	}
	if next.Path != pathOAuthAuthorize {
		t.Fatalf("next = %q, want the consent screen", next)
	}
	if got, want := next.Query(), consentRequest(); !reflect.DeepEqual(got, want) {
		t.Errorf("next carries %v, want the request %v", got, want)
	}

	// Signing in lands on that very request.
	form := "username=admin&password=hunter2&next=" + url.QueryEscape(next.String())
	rec = send(d, withCSRF(t, d, request(http.MethodPost, pathLogin, ""), form))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("sign-in: status = %d, want %d: %s", rec.Code, http.StatusSeeOther, rec.Body)
	}
	if got := rec.Header().Get("Location"); got != next.String() {
		t.Errorf("sign-in Location = %q, want %q", got, next)
	}
}

func TestConsentShowsTheRequestAndCarriesItInTheForm(t *testing.T) {
	d, service := newTestDashboard(t)
	service.consent = grantedConsent()

	rec := send(d, signedIn(http.MethodGet, consentTarget(), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}

	// The service saw the request as sent, and this deployment's resource.
	if want := []auth.OAuthAuthorizationRequest{oauthRequestFrom(consentRequest())}; !reflect.DeepEqual(service.consentRequests, want) {
		t.Errorf("OAuthConsent saw %+v, want %+v", service.consentRequests, want)
	}
	if want := mcp.Resource(d.opts.PublicURL); service.consentResource != want {
		t.Errorf("OAuthConsent resource = %q, want %q", service.consentResource, want)
	}

	body := rec.Body.String()
	for _, want := range []string{
		`data-consent`,
		`data-consent-decision`,
		`value="approve"`,
		`value="deny"`,
		`rel="noopener noreferrer"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page does not contain %q:\n%s", want, body)
		}
	}
	for name, values := range consentRequest() {
		if !strings.Contains(body, `name="`+name+`" value="`+attr(values[0])+`"`) {
			t.Errorf("the form does not carry %s=%q:\n%s", name, values[0], body)
		}
	}
	if strings.Contains(body, `name="csrf_token" value=""`) {
		t.Error("the decision form carries an empty CSRF token")
	}
}

// attr escapes a value the way html/template writes it into an attribute.
func attr(s string) string {
	return strings.NewReplacer(
		"&", "&amp;", `"`, "&#34;", "'", "&#39;", "+", "&#43;", "<", "&lt;", ">", "&gt;",
	).Replace(s)
}

// TestConsentRefusesAnUnverifiedClientWithoutRedirecting pins the one rule
// that matters most on this page: a redirect URI that could not be checked is
// never followed.
func TestConsentRefusesAnUnverifiedClientWithoutRedirecting(t *testing.T) {
	d, service := newTestDashboard(t)
	service.consentErr = &auth.OAuthClientError{Message: "redirect_uri is not one the client registered"}

	for _, req := range []*http.Request{
		signedIn(http.MethodGet, consentTarget(), ""),
		withCSRF(t, d, signedIn(http.MethodPost, pathOAuthAuthorize, ""), consentRequest().Encode()+"&decision=approve"),
	} {
		rec := send(d, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400: %s", req.Method, rec.Code, rec.Body)
		}
		if got := rec.Header().Get("Location"); got != "" {
			t.Errorf("%s: Location = %q, want no redirect", req.Method, got)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `data-notice="error"`) {
			t.Errorf("%s: the page does not carry an error banner:\n%s", req.Method, body)
		}
		if strings.Contains(body, `data-consent-decision`) {
			t.Errorf("%s: the page offers a decision on an unverified request:\n%s", req.Method, body)
		}
	}
}

func TestConsentReportsARequestProblemToTheClient(t *testing.T) {
	d, service := newTestDashboard(t)
	service.consentErr = &auth.OAuthRedirectError{Code: "invalid_scope", Description: "unknown scope"}

	rec := send(d, signedIn(http.MethodGet, consentTarget(), ""))
	query := clientRedirect(t, rec)
	for key, want := range map[string]string{
		"error":             "invalid_scope",
		"error_description": "unknown scope",
		"state":             consentState,
		"iss":               consentIssuer,
	} {
		if got := query.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if query.Has("code") {
		t.Errorf("an error response carries a code: %v", query)
	}
}

func TestConsentDecisionRequiresACSRFToken(t *testing.T) {
	d, service := newTestDashboard(t)
	service.consent = grantedConsent()

	rec := send(d, signedIn(http.MethodPost, pathOAuthAuthorize, consentRequest().Encode()+"&decision=approve"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if len(service.oauthApproved) != 0 {
		t.Errorf("a submission without a token was approved: %v", service.oauthApproved)
	}
}

func TestApprovingReturnsACodeToTheClient(t *testing.T) {
	d, service := newTestDashboard(t)
	service.consent = grantedConsent()
	service.code = "hark_oc_test"

	rec := send(d, withCSRF(t, d, signedIn(http.MethodPost, pathOAuthAuthorize, ""),
		consentRequest().Encode()+"&decision=approve"))

	query := clientRedirect(t, rec)
	for key, want := range map[string]string{
		"code":  "hark_oc_test",
		"state": consentState,
		"iss":   consentIssuer,
	} {
		if got := query.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if query.Has("error") {
		t.Errorf("an approval carries an error: %v", query)
	}

	// The decision was validated from the hidden fields, then attributed to
	// the signed-in owner.
	if want := []auth.OAuthAuthorizationRequest{oauthRequestFrom(consentRequest())}; !reflect.DeepEqual(service.consentRequests, want) {
		t.Errorf("OAuthConsent saw %+v, want %+v", service.consentRequests, want)
	}
	if want := []string{consentClientID + " by user-1"}; !reflect.DeepEqual(service.oauthApproved, want) {
		t.Errorf("approved = %v, want %v", service.oauthApproved, want)
	}
}

func TestDenyingReportsAccessDeniedToTheClient(t *testing.T) {
	d, service := newTestDashboard(t)
	service.consent = grantedConsent()

	rec := send(d, withCSRF(t, d, signedIn(http.MethodPost, pathOAuthAuthorize, ""),
		consentRequest().Encode()+"&decision=deny"))

	query := clientRedirect(t, rec)
	for key, want := range map[string]string{
		"error": "access_denied",
		"state": consentState,
		"iss":   consentIssuer,
	} {
		if got := query.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if query.Has("code") {
		t.Errorf("a denial carries a code: %v", query)
	}
	if len(service.oauthApproved) != 0 {
		t.Errorf("a denial was recorded as an approval: %v", service.oauthApproved)
	}
}

func TestADecisionThatIsNeitherIsABadRequest(t *testing.T) {
	d, service := newTestDashboard(t)
	service.consent = grantedConsent()

	rec := send(d, withCSRF(t, d, signedIn(http.MethodPost, pathOAuthAuthorize, ""),
		consentRequest().Encode()+"&decision=maybe"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Location"); got != "" {
		t.Errorf("Location = %q, want no redirect", got)
	}
	if len(service.oauthApproved) != 0 {
		t.Errorf("an unnamed decision was approved: %v", service.oauthApproved)
	}
}

// TestAnAPITokenCannotReachTheConsentScreen is the boundary the device grant
// draws too: a credential minted for an agent cannot consent to its successor.
func TestAnAPITokenCannotReachTheConsentScreen(t *testing.T) {
	d, service := newTestDashboard(t)
	service.consent = grantedConsent()

	asToken := func(req *http.Request) *http.Request {
		return req.WithContext(auth.WithPrincipal(req.Context(), &auth.Principal{
			Kind:     auth.KindAPIToken,
			User:     db.User{ID: "user-1"},
			APIToken: &db.APIToken{ID: "token-1", Scopes: db.Scopes},
		}))
	}

	for _, req := range []*http.Request{
		asToken(request(http.MethodGet, consentTarget(), "")),
		asToken(withCSRF(t, d, request(http.MethodPost, pathOAuthAuthorize, ""), consentRequest().Encode()+"&decision=approve")),
	} {
		rec := send(d, req)
		if rec.Code != http.StatusSeeOther {
			t.Errorf("%s: status = %d, want %d", req.Method, rec.Code, http.StatusSeeOther)
			continue
		}
		if location := rec.Header().Get("Location"); !strings.HasPrefix(location, pathLogin) {
			t.Errorf("%s: Location = %q, want the sign-in page", req.Method, location)
		}
	}
	if len(service.consentRequests) != 0 || len(service.oauthApproved) != 0 {
		t.Errorf("an API token reached the consent: asked %v, approved %v", service.consentRequests, service.oauthApproved)
	}
}

// TestSafeNextCarriesAnOAuthRequest covers the consent screen as a post-sign-in
// destination: a request round-trips, and everything that is not one of its
// parameters is dropped or, where it could not be safe, sent home.
// TestConsentPageLetsTheFormReachTheClient pins the one header the browser
// consults when the decision form's answer is a redirect off this origin: the
// page names the client's origin in form-action, and nothing else changes.
func TestConsentPageLetsTheFormReachTheClient(t *testing.T) {
	d, service := newTestDashboard(t)

	loopback := grantedConsent()
	loopback.RedirectURI = "http://127.0.0.1:52000/cb"
	loopback.Loopback = true
	v6 := grantedConsent()
	v6.RedirectURI = "http://[::1]:52000/cb"
	v6.Loopback = true

	for name, tc := range map[string]struct {
		consent *auth.OAuthConsent
		source  string
	}{
		"https":    {grantedConsent(), "https://client.example"},
		"loopback": {loopback, "http://127.0.0.1:52000"},
		"ipv6":     {v6, "http:"},
	} {
		service.consent = tc.consent
		rec := send(d, signedIn(http.MethodGet, consentTarget(), ""))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200: %s", name, rec.Code, rec.Body)
		}
		if got, want := rec.Header().Get("Content-Security-Policy"), policyAllowingFormTo(tc.source); got != want {
			t.Errorf("%s: Content-Security-Policy = %q, want %q", name, got, want)
		}
	}

	// A request with nothing to decide has no form, and keeps the strict policy.
	service.consent = nil
	service.consentErr = &auth.OAuthClientError{Message: "unknown client"}
	rec := send(d, signedIn(http.MethodGet, consentTarget(), ""))
	if got := rec.Header().Get("Content-Security-Policy"); got != contentSecurityPolicy {
		t.Errorf("refused request: Content-Security-Policy = %q, want the dashboard's policy", got)
	}
}

func TestFormActionSource(t *testing.T) {
	for redirectURI, want := range map[string]string{
		"https://claude.ai/api/mcp/auth_callback":        "https://claude.ai",
		"https://chatgpt.com:8443/connector/oauth/x":     "https://chatgpt.com:8443",
		"http://localhost/cb":                            "http://localhost",
		"http://127.0.0.1:52000/cb?x=1":                  "http://127.0.0.1:52000",
		"http://[::1]:52000/cb":                          "http:",
		"https://client.example/callback#frag":           "https://client.example",
		"ftp://client.example/cb":                        "",
		"https://user:pw@client.example/cb":              "https://client.example",
		"https://client.example;script-src%20%27x%27/cb": "",
		"https://client_example/cb":                      "",
		"not a url":                                      "",
	} {
		got, ok := formActionSource(redirectURI)
		if ok != (want != "") || got != want {
			t.Errorf("formActionSource(%q) = (%q, %v), want (%q, %v)", redirectURI, got, ok, want, want != "")
		}
	}
}

func TestSafeNextCarriesAnOAuthRequest(t *testing.T) {
	valid := consentTarget()
	tests := map[string]string{
		valid:                          valid,
		valid + "&evil=1":              valid,
		valid + "&next=//evil.example": valid,

		// Only the eight parameters count; a target naming none of them is not
		// a request.
		pathOAuthAuthorize:                     pathHome,
		pathOAuthAuthorize + "?evil=1":         pathHome,
		pathOAuthAuthorize + "?client_id=x":    pathOAuthAuthorize + "?client_id=x",
		pathOAuthAuthorize + "?CLIENT_ID=x":    pathHome,
		pathOAuthAuthorize + "x?client_id=x":   pathHome,
		pathOAuthAuthorize + "/../x?state=abc": pathHome,

		"https://evil.example" + pathOAuthAuthorize + "?client_id=x": pathHome,
		"//evil.example" + pathOAuthAuthorize + "?client_id=x":       pathHome,

		pathOAuthAuthorize + "?state=" + strings.Repeat("a", maxOAuthNextLength): pathHome,
		pathOAuthAuthorize + "?state=a%0Ab":                                      pathHome,
		pathOAuthAuthorize + "?state=a\nb":                                       pathHome,
		pathOAuthAuthorize + "?state=a%5Cb":                                      pathHome,
		pathOAuthAuthorize + "?state=a%zzb":                                      pathHome,
	}
	for in, want := range tests {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

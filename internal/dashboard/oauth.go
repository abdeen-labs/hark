package dashboard

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/mcp"
)

// maxOAuthNextLength accommodates URL encoding of the maximum client ID, redirect URI and state.
const maxOAuthNextLength = 32 << 10

type consentPage struct {
	view
	Request auth.OAuthAuthorizationRequest
	Consent *auth.OAuthConsent
	// ClientHost identifies the metadata document publisher.
	ClientHost   string
	RedirectHost string
}

func (d *Dashboard) showOAuthConsent(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || repeatedOAuthParameters(values) {
		d.renderError(w, r, http.StatusBadRequest, "The authorization request contains invalid or repeated parameters.")
		return
	}
	req := oauthRequestFrom(values)
	consent, ok := d.oauthConsent(w, r, p, req)
	if !ok {
		return
	}
	d.renderConsent(w, r, p, http.StatusOK, req, consent, nil)
}

// submitOAuthConsent revalidates the submitted request before issuing a code.
func (d *Dashboard) submitOAuthConsent(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	if repeatedOAuthParameters(r.PostForm) {
		d.renderError(w, r, http.StatusBadRequest, "The authorization request contains repeated parameters.")
		return
	}
	req := oauthRequestFrom(r.PostForm)
	consent, ok := d.oauthConsent(w, r, p, req)
	if !ok {
		return
	}

	switch r.PostFormValue("decision") {
	case decisionApprove:
		code, err := d.opts.Auth.ApproveOAuth(r.Context(), consent, p.UserID())
		if err != nil {
			d.fail(w, r, "approving an OAuth authorization failed", err)
			return
		}
		d.redirectToClient(w, r, consent.RedirectURI, consent.State, url.Values{"code": {code}})
	case decisionDeny:
		d.redirectToClient(w, r, consent.RedirectURI, consent.State, url.Values{"error": {"access_denied"}})
	default:
		d.renderError(w, r, http.StatusBadRequest, "The form named no decision. Use Approve or Deny.")
	}
}

// oauthConsent redirects errors only after the client and redirect URI are verified.
func (d *Dashboard) oauthConsent(
	w http.ResponseWriter, r *http.Request, p *auth.Principal,
	req auth.OAuthAuthorizationRequest,
) (*auth.OAuthConsent, bool) {
	consent, err := d.opts.Auth.OAuthConsent(r.Context(), req, mcp.Resource(d.opts.PublicURL))

	var (
		clientErr   *auth.OAuthClientError
		redirectErr *auth.OAuthRedirectError
	)
	switch {
	case err == nil:
		return consent, true
	case errors.As(err, &clientErr):
		d.renderConsent(w, r, p, http.StatusBadRequest, req, nil, &notice{
			Kind: noticeError, Message: clientErr.Message,
		})
	case errors.As(err, &redirectErr):
		d.redirectToClient(w, r, req.RedirectURI, req.State, url.Values{
			"error":             {redirectErr.Code},
			"error_description": {redirectErr.Description},
		})
	default:
		d.fail(w, r, "validating an OAuth authorization request failed", err)
	}
	return nil, false
}

func (d *Dashboard) renderConsent(
	w http.ResponseWriter, r *http.Request, p *auth.Principal, status int,
	req auth.OAuthAuthorizationRequest, consent *auth.OAuthConsent, n *notice,
) {
	page := consentPage{
		view:    d.newView(r, p, "Authorize a client", ""),
		Request: req,
		Consent: consent,
	}
	if n != nil {
		page.Notice = n
	}
	if consent != nil {
		if consent.Client.MetadataDocument {
			page.ClientHost = hostOf(consent.Client.ID)
		}
		page.RedirectHost = hostOf(consent.RedirectURI)
		if source, ok := formActionSource(consent.RedirectURI); ok {
			w.Header().Set("Content-Security-Policy", policyAllowingFormTo(source))
		}
	}
	d.render(w, r, status, tmplConsent, page)
}

// formActionSource returns a CSP source for the redirect origin. IPv6 literals
// require a scheme source because CSP host sources do not support them.
func formActionSource(redirectURI string) (string, bool) {
	u, err := url.Parse(redirectURI)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", false
	}
	host := u.Hostname()
	if strings.Contains(host, ":") {
		return u.Scheme + ":", true
	}
	if !cspHost(host) {
		return "", false
	}
	source := u.Scheme + "://" + host
	if port := u.Port(); port != "" {
		source += ":" + port
	}
	return source, true
}

// cspHost accepts only characters allowed in CSP host sources.
func cspHost(host string) bool {
	if host == "" {
		return false
	}
	for i := 0; i < len(host); i++ {
		c := host[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}

// redirectToClient includes state and the issuer (RFC 9207) in the authorization response.
func (d *Dashboard) redirectToClient(w http.ResponseWriter, r *http.Request, redirectURI, state string, values url.Values) {
	http.Redirect(w, r, auth.OAuthRedirectURL(redirectURI, d.issuer(), state, values), http.StatusSeeOther)
}

func (d *Dashboard) issuer() string {
	if d.opts.PublicURL == nil {
		return ""
	}
	return d.opts.PublicURL.Scheme + "://" + d.opts.PublicURL.Host
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

// oauthRequestFrom reads the supported authorization parameters.
func oauthRequestFrom(values url.Values) auth.OAuthAuthorizationRequest {
	return auth.OAuthAuthorizationRequest{
		ResponseType:        values.Get("response_type"),
		ClientID:            values.Get("client_id"),
		RedirectURI:         values.Get("redirect_uri"),
		Scope:               values.Get("scope"),
		State:               values.Get("state"),
		CodeChallenge:       values.Get("code_challenge"),
		CodeChallengeMethod: values.Get("code_challenge_method"),
		Resource:            values.Get("resource"),
	}
}

func oauthRequestValues(req auth.OAuthAuthorizationRequest) url.Values {
	values := url.Values{}
	for name, value := range map[string]string{
		"response_type":         req.ResponseType,
		"client_id":             req.ClientID,
		"redirect_uri":          req.RedirectURI,
		"scope":                 req.Scope,
		"state":                 req.State,
		"code_challenge":        req.CodeChallenge,
		"code_challenge_method": req.CodeChallengeMethod,
		"resource":              req.Resource,
	} {
		if value != "" {
			values.Set(name, value)
		}
	}
	return values
}

func oauthReturnTarget(req auth.OAuthAuthorizationRequest) string {
	values := oauthRequestValues(req)
	if len(values) == 0 {
		return pathOAuthAuthorize
	}
	return pathOAuthAuthorize + "?" + values.Encode()
}

// safeOAuthNext keeps supported parameters and re-encodes their values before redirecting.
func safeOAuthNext(raw, query string) string {
	if len(raw) > maxOAuthNextLength || hasUnsafeChars(raw) {
		return pathHome
	}
	values, err := url.ParseQuery(query)
	if err != nil || repeatedOAuthParameters(values) {
		return pathHome
	}

	req := oauthRequestFrom(values)
	kept := oauthRequestValues(req)
	if len(kept) == 0 {
		return pathHome
	}
	target := oauthReturnTarget(req)
	if len(target) > maxOAuthNextLength {
		return pathHome
	}
	return target
}

func repeatedOAuthParameters(values url.Values) bool {
	for _, entries := range values {
		if len(entries) > 1 {
			return true
		}
	}
	return false
}

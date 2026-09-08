package dashboard

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/mcp"
)

// The OAuth consent screen.
//
// An MCP client that holds no token of its own opens this page in the owner's
// browser with an authorization request in the query. The owner reviews who is
// asking, where the browser goes afterwards and for what access, then approves
// or denies; either answer is delivered to the client's redirect URI, with a
// code or with an error. See docs/api.md § GET /oauth/authorize.

// maxOAuthNextLength bounds a consent-screen target carried through sign-in.
// An authorization request is a few hundred bytes; this leaves room for a long
// state and a metadata-document client id without accepting a query of any
// size at all.
const maxOAuthNextLength = 4096

// consentPage is the consent screen.
type consentPage struct {
	view
	// Request is the authorization request as the client sent it, echoed into
	// the form's hidden fields so the decision is validated against the same
	// parameters the page was drawn from.
	Request auth.OAuthAuthorizationRequest
	// Consent is the validated request, or nil when there is nothing to
	// decide: the page then carries the refusal and no form.
	Consent *auth.OAuthConsent
	// ClientHost is where a metadata-document client published its document,
	// shown so the owner can weigh the name against the origin vouching for
	// it. Empty for a registered client.
	ClientHost string
	// RedirectHost is where the browser goes after the decision.
	RedirectHost string
}

// showOAuthConsent draws the screen for the request in the query.
func (d *Dashboard) showOAuthConsent(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	req := oauthRequestFrom(r.URL.Query())
	consent, ok := d.oauthConsent(w, r, p, req)
	if !ok {
		return
	}
	d.renderConsent(w, r, p, http.StatusOK, req, consent, nil)
}

// submitOAuthConsent records the owner's decision and returns the browser to
// the client.
//
// The request is validated again from the hidden fields rather than trusted: a
// registration can change and a metadata document be republished between the
// page being drawn and the button being pressed, and a code must be issued
// against what is true now.
func (d *Dashboard) submitOAuthConsent(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
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

// oauthConsent validates the request, answering both failure classes itself.
// It reports whether the handler should continue.
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
		// The client or its redirect URI could not be verified, so there is
		// nowhere the browser can safely be sent: the refusal stays here.
		d.renderConsent(w, r, p, http.StatusBadRequest, req, nil, &notice{
			Kind: noticeError, Message: clientErr.Message,
		})
	case errors.As(err, &redirectErr):
		// This class is only produced once the client and its redirect URI have
		// been verified, which is what makes following the URI safe.
		d.redirectToClient(w, r, req.RedirectURI, req.State, url.Values{
			"error":             {redirectErr.Code},
			"error_description": {redirectErr.Description},
		})
	default:
		d.fail(w, r, "validating an OAuth authorization request failed", err)
	}
	return nil, false
}

// renderConsent draws the page around a request, with the form when there is a
// consent to give and without one otherwise.
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
		// The decision form's answer is a redirect to the client, which the
		// browser holds to this page's form-action.
		if source, ok := formActionSource(consent.RedirectURI); ok {
			w.Header().Set("Content-Security-Policy", policyAllowingFormTo(source))
		}
	}
	d.render(w, r, status, tmplConsent, page)
}

// formActionSource is the CSP source expression that admits a redirect URI's
// origin: its scheme, host and port. CSP has no host-source form for an IPv6
// literal, so a loopback client on [::1] is admitted by scheme instead. A host
// outside the grammar's own alphabet is refused rather than written into a
// header, which leaves the page on the strict policy.
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

// cspHost reports whether host is made of the characters a CSP host-source
// allows: letters, digits, hyphens and dots.
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

// redirectToClient delivers an authorization response: the browser is sent to
// the client's redirect URI with values, the request's state and this
// deployment's issuer in the query.
func (d *Dashboard) redirectToClient(w http.ResponseWriter, r *http.Request, redirectURI, state string, values url.Values) {
	http.Redirect(w, r, auth.OAuthRedirectURL(redirectURI, d.issuer(), state, values), http.StatusSeeOther)
}

// issuer is the deployment's public origin — scheme and host, no path — which
// is what the authorization server metadata publishes and what every redirect
// back to a client carries as iss (RFC 9207).
func (d *Dashboard) issuer() string {
	if d.opts.PublicURL == nil {
		return ""
	}
	return d.opts.PublicURL.Scheme + "://" + d.opts.PublicURL.Host
}

// hostOf is the host of a URL, or "" when it has none.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

// oauthRequestFrom reads an authorization request out of a query or a form.
//
// These eight names are the page's whole vocabulary: what it reads on GET, what
// its hidden fields carry on POST, and the only parameters a sign-in detour
// preserves. [oauthRequestValues] is the inverse.
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

// oauthRequestValues writes a request back out as a query, absent parameters
// omitted.
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

// oauthReturnTarget is the consent screen's own URL for a request: the page
// with the request re-encoded, and nothing that was not part of it.
func oauthReturnTarget(req auth.OAuthAuthorizationRequest) string {
	values := oauthRequestValues(req)
	if len(values) == 0 {
		return pathOAuthAuthorize
	}
	return pathOAuthAuthorize + "?" + values.Encode()
}

// safeOAuthNext bounds a consent-screen target carried through sign-in.
//
// The query is rebuilt from the request it names rather than passed through,
// so what ends up in the redirect is at most eight known parameters, none
// carrying a control character, in a URL no longer than maxOAuthNextLength. A
// target naming no parameter at all is not a request, and goes home like any
// other unusable destination.
func safeOAuthNext(raw, query string) string {
	if len(raw) > maxOAuthNextLength || hasUnsafeChars(raw) {
		return pathHome
	}
	values, err := url.ParseQuery(query)
	if err != nil {
		return pathHome
	}

	req := oauthRequestFrom(values)
	kept := oauthRequestValues(req)
	if len(kept) == 0 {
		return pathHome
	}
	for _, vs := range kept {
		if hasUnsafeChars(vs[0]) {
			return pathHome
		}
	}

	target := oauthReturnTarget(req)
	if len(target) > maxOAuthNextLength {
		return pathHome
	}
	return target
}

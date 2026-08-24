package dashboard

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
)

// The device-grant approval screen.
//
// A headless client — `harkctl`, a CI job, anything that cannot hold a password
// — opens a pairing request at POST /v1/auth/device/code and is handed a short
// code and a link to this page. The owner arrives here in a browser that
// already holds a session, reads what the client is asking for, and says yes or
// no. That decision is what makes the client's next poll mint a token.
//
// The page is deliberately a *session* surface, mounted inside the same gate
// every other dashboard page runs behind. It is the same boundary the JSON
// routes draw with httpapi.RequireSession, and the reason is the same one: an
// API token that could approve a pairing request could mint its own successor,
// and a scoped credential would stop being a bound one.

// Form values. The decision is a fixed vocabulary rather than free text, so a
// submission either names one of two acts or is a lookup.
const (
	decisionApprove = "approve"
	decisionDeny    = "deny"

	// maxTypedCode bounds what is echoed back into the field after a code that
	// did not resolve. A user code is nine characters; anything longer is a
	// paste accident, and there is no reason to carry it through a redirect.
	maxTypedCode = 32
)

// authorizePage is the approval screen.
type authorizePage struct {
	view
	// Code is what the browser asked about, echoed into the form so a rejected
	// or resolved submission comes back showing which request it was.
	Code string
	// Request is the pairing request the code named, when it named one.
	Request *db.DeviceAuthorization
	// Pending reports that there is still a decision to make. It is false for a
	// request that was already approved, denied, expired or spent — all of
	// which are shown, without buttons, because "it was already denied" is a
	// more useful answer than an empty page.
	Pending bool
}

// showAuthorize draws the screen for the code in the link, or the field to type
// one into.
func (d *Dashboard) showAuthorize(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	d.renderAuthorize(w, r, p, r.URL.Query().Get("code"), http.StatusOK, nil)
}

// renderAuthorize loads the named request and draws the page around it.
//
// An absent code is not an error. The link a client prints carries one, but the
// owner may also have walked to another machine with nothing but the eight
// characters on a terminal behind them, so the page offers a field.
func (d *Dashboard) renderAuthorize(
	w http.ResponseWriter, r *http.Request, p *auth.Principal,
	rawCode string, status int, n *notice,
) {
	page := authorizePage{
		view: d.newView(r, p, "Authorize a client", ""),
		Code: clip(strings.TrimSpace(rawCode), maxTypedCode),
	}
	if n != nil {
		page.Notice = n
	}
	if page.Code == "" {
		d.render(w, r, status, tmplAuthorize, page)
		return
	}

	// A code that is not a code and a code that names nothing are one answer:
	// this page must not tell a visitor which of the two they typed.
	request, err := d.opts.Auth.DeviceGrantByUserCode(r.Context(), page.Code)
	switch {
	case err == nil:
		page.Request = request
		page.Pending = request.Pending(d.opts.Auth.Now())
	case errors.Is(err, auth.ErrNotFound):
		status = http.StatusNotFound
		if page.Notice == nil {
			page.Notice = &notice{
				Kind:    noticeError,
				Message: "No request matches that code. Codes expire after 10 minutes.",
			}
		}
	default:
		d.fail(w, r, "loading a device authorization failed", err)
		return
	}
	d.render(w, r, status, tmplAuthorize, page)
}

// submitAuthorize records the owner's decision, or looks up a typed code.
//
// The lookup shares the form because the alternative is a GET that reflects
// whatever was typed straight back into a URL. Going through the POST means the
// code is normalised on the way to the redirect, and the address bar ends up
// holding a canonical code or nothing.
func (d *Dashboard) submitAuthorize(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	code := strings.TrimSpace(r.PostFormValue("code"))

	var (
		err     error
		outcome string
	)
	switch r.PostFormValue("decision") {
	case decisionApprove:
		_, err = d.opts.Auth.ApproveDeviceGrant(r.Context(), code, p.UserID())
		outcome = "client_approved"
	case decisionDeny:
		_, err = d.opts.Auth.DenyDeviceGrant(r.Context(), code)
		outcome = "client_denied"
	default:
		// The lookup: no decision was named, so the code is all this
		// submission carried.
		d.redirectToAuthorize(w, r, code, "")
		return
	}

	switch {
	case err == nil:
		d.redirectToAuthorize(w, r, code, outcome)
	case errors.Is(err, auth.ErrNotFound), errors.Is(err, auth.ErrConflict):
		// Unknown, already decided and expired are one answer here as they are
		// on the API: there is nothing left to decide. The page is redrawn
		// rather than redirected to, because the banner is about this
		// submission and not about the request's current state.
		d.renderAuthorize(w, r, p, code, http.StatusConflict, &notice{
			Kind:    noticeError,
			Message: "That request is no longer pending. Start a new request from the client.",
		})
	default:
		d.fail(w, r, "resolving a device authorization failed", err)
	}
}

// redirectToAuthorize answers a completed submission, so a reload does not
// replay it. The code stays in the URL: the page it lands on is the record of
// what was decided.
func (d *Dashboard) redirectToAuthorize(w http.ResponseWriter, r *http.Request, code, outcome string) {
	query := url.Values{}
	if normalized, ok := auth.NormalizeUserCode(code); ok {
		query.Set("code", normalized)
	} else if code != "" {
		// Keep what was typed so the field comes back filled in and the visitor
		// can see the typo, rather than an empty form that lost their input.
		query.Set("code", clip(code, maxTypedCode))
	}
	if outcome != "" {
		query.Set("done", outcome)
	}

	target := pathAuthorize
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// clip bounds a string by bytes, on a rune boundary so the result is still
// valid UTF-8 and html/template has nothing to refuse.
func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

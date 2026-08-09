package dashboard

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/abdeen-labs/hark/internal/auth"
)

// loginPage is the sign-in form.
type loginPage struct {
	view
	// Next is where to go once signed in, carried through the form so a
	// bookmarked page survives the detour.
	Next string
	// Username is echoed back after a failure so only the password has to be
	// retyped.
	Username string
}

// showLogin draws the sign-in form, or sends an already-signed-in browser on.
func (d *Dashboard) showLogin(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.URL.Query().Get("next"))
	if auth.PrincipalFrom(r.Context()).IsSession() {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}

	token, err := d.csrf.issue(w, r)
	if err != nil {
		d.fail(w, r, "issuing a CSRF token failed", err)
		return
	}
	d.render(w, r, http.StatusOK, tmplLogin, loginPage{
		view: d.shell("Sign in", "", token, nil),
		Next: next,
	})
}

// submitLogin exchanges the form for a session cookie.
//
// It does not use [Dashboard.form]: there is no session yet, so the only gate
// that applies is the CSRF token — which is exactly the case the double-submit
// cookie exists for.
func (d *Dashboard) submitLogin(w http.ResponseWriter, r *http.Request) {
	if !d.parseForm(w, r) {
		return
	}

	now := d.opts.Auth.Now()
	if key, ok := d.allowLogin(r, now); !ok {
		retry := d.logins.retryAfter(key, now)
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
		d.renderError(w, r, http.StatusTooManyRequests,
			"Too many sign-in attempts. Try again shortly.")
		return
	}

	username := r.PostFormValue("username")
	next := safeNext(r.PostFormValue("next"))

	principal, token, err := d.opts.Auth.Login(r.Context(), username, r.PostFormValue("password"))
	if err != nil {
		if !errors.Is(err, auth.ErrInvalidCredentials) {
			d.fail(w, r, "dashboard sign-in failed", err)
			return
		}
		// One message for a wrong username and a wrong password: which of the
		// two was wrong is not something a sign-in form should teach.
		d.render(w, r, http.StatusUnauthorized, tmplLogin, loginPage{
			view: d.shell("Sign in", "", d.csrf.token(r), &notice{
				Kind: noticeError, Message: "That username and password do not match an account.",
			}),
			Next:     next,
			Username: username,
		})
		return
	}

	// A token minted before this session began does not carry into it.
	if _, err := d.csrf.rotate(w); err != nil {
		d.fail(w, r, "issuing a CSRF token failed", err)
		return
	}
	d.session.Set(w, token, principal.Session.ExpiresAt, now)
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// submitLogout retires the session behind the cookie and clears it.
//
// The cookie is cleared whatever the store says. A browser that keeps sending a
// session the owner asked to end is the worse failure of the two, and the row
// expires on its own.
func (d *Dashboard) submitLogout(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	if err := d.opts.Auth.Logout(r.Context(), p.Session.ID); err != nil {
		d.log(r).ErrorContext(r.Context(), "dashboard sign-out failed", "error", err)
	}
	d.session.Clear(w)
	d.redirect(w, r, pathLogin, "signed_out")
}

// shell builds the page frame for a view with no account behind it — sign-in
// and the error page — where [Dashboard.newView] has no principal to read.
func (d *Dashboard) shell(title, section, csrf string, n *notice) view {
	return view{
		Title:   title,
		Section: section,
		Paths:   d.paths,
		Assets:  assets,
		Version: d.opts.Version,
		CSRF:    csrf,
		Notice:  n,
	}
}

// Package dashboard is the admin UI embedded in the harkd binary.
//
// It is a server-rendered HTML surface for one person: the account owner,
// signed in with the same session cookie the API issues, looking at their own
// deliveries and managing their own credentials. It also serves the two pages
// that are addressed from outside — the device-grant approval screen and the
// published API contract — because they share this shell. There is no build
// step, no npm, and no client-side framework: the templates, two stylesheets
// and a handful of lines of JavaScript are compiled into the binary with
// embed.FS.
//
// It talks to the same layers the API does rather than to the API over HTTP:
// [Authenticator] is the slice of *auth.Service it needs, the store is read
// directly, and the test push goes through the same [push.Sender]. What it
// deliberately does not do is reimplement policy — the session cookie's flags
// come from internal/httpapi, and every page runs inside that package's
// middleware chain, so the origin gate on cookie-authenticated writes covers
// these forms too.
package dashboard

import (
	"bytes"
	"context"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/httpapi"
	"github.com/abdeen-labs/hark/internal/push"
)

// Authenticator is the part of *auth.Service the dashboard uses.
//
// It is an interface so this package depends on the five operations it needs
// rather than on the whole credential surface, and so the routing and CSRF
// tests can run without a database.
type Authenticator interface {
	// Login exchanges a username and password for a session and its token.
	Login(ctx context.Context, username, password string) (*auth.Principal, string, error)
	// Logout retires the session behind the browser's cookie.
	Logout(ctx context.Context, sessionID string) error

	ListAPITokens(ctx context.Context, userID string) ([]db.APIToken, error)
	CreateAPIToken(ctx context.Context, userID string, p auth.CreateAPITokenParams) (*db.APIToken, string, error)
	RevokeAPIToken(ctx context.Context, tokenID, userID string) error

	// The approval half of the device grant. It is the account owner's
	// decision, which is why it lives on a session surface at all: an API token
	// must never be able to approve its own successor. These are the same three
	// operations the JSON routes under /v1/auth/device/requests call.
	DeviceGrantByUserCode(ctx context.Context, userCode string) (*db.DeviceAuthorization, error)
	ApproveDeviceGrant(ctx context.Context, userCode, userID string) (*db.DeviceAuthorization, error)
	DenyDeviceGrant(ctx context.Context, userCode string) (*db.DeviceAuthorization, error)

	// Now is the server's clock, so a freshly issued cookie expires with the
	// row behind it.
	Now() time.Time
}

// Options are the dashboard's dependencies.
//
// There is no logger here: the dashboard runs inside internal/httpapi's
// middleware chain and writes through the request-scoped logger that chain
// installs, so a failed page carries the same request id as its access-log
// line.
type Options struct {
	// Auth issues the session the whole surface runs on. Required.
	Auth Authenticator
	// Store is read directly for everything the pages show. Required.
	Store *db.Store
	// Push delivers the test notification. A nil Sender becomes push.Noop,
	// which reports every send as a provider failure rather than a success.
	Push push.Sender
	// PublicURL decides the cookies' Secure and `__Host-` flags. It must be the
	// same value internal/httpapi was built with.
	PublicURL *url.URL
	// TrustedClientIPHeader names the header a trusted reverse proxy overwrites
	// with the real client address, used to give sign-in attempts a per-client
	// ceiling. Empty leaves only the process-wide one.
	TrustedClientIPHeader string
	// Version is shown in the footer.
	Version string
}

// Paths are the dashboard's own URLs, derived from [httpapi.DashboardPrefix].
//
// Templates read them from here so the mount point is written down once: a
// hardcoded href in a template is the thing that silently breaks when the
// prefix moves.
const (
	pathHome    = httpapi.DashboardPrefix
	pathLogin   = httpapi.DashboardPrefix + "/login"
	pathLogout  = httpapi.DashboardPrefix + "/logout"
	pathDevices = httpapi.DashboardPrefix + "/devices"
	pathTokens  = httpapi.DashboardPrefix + "/tokens"
	pathTest    = httpapi.DashboardPrefix + "/test"
	pathAssets  = httpapi.DashboardPrefix + "/assets"

	// pathAuthorize is the device-grant approval screen, and pathDocs the
	// published API contract. Both sit outside the dashboard's prefix because
	// they are addresses other things hand out: a CLI prints the first into a
	// terminal, and the second is a link people paste. internal/httpapi owns
	// both spellings, so the link a client is given and the page that answers
	// it cannot drift apart.
	pathAuthorize = httpapi.DeviceVerificationPath
	pathDocs      = httpapi.DocsPath
)

// Dashboard is the HTTP handler. Build it with [New].
type Dashboard struct {
	opts    Options
	mux     *http.ServeMux
	session httpapi.SessionCookie
	csrf    csrfCookie
	logins  *limiter
	paths   paths
	// docs is the API contract, rendered once at construction. It is the one
	// page with no per-request state in it at all, so it is bytes rather than a
	// template.
	docs page
}

// paths is the link table handed to every template.
type paths struct {
	Home, Login, Logout, Devices, Tokens, Test string
	Authorize, Docs                            string
}

// New builds the dashboard handler.
//
// It panics when a required dependency is missing, for the same reason
// [httpapi.New] does: that is a wiring mistake in main, and failing at
// construction is clearer than failing on the first request.
func New(opts Options) *Dashboard {
	if opts.Auth == nil {
		panic("dashboard: Options.Auth is required")
	}
	if opts.Store == nil {
		panic("dashboard: Options.Store is required")
	}
	if opts.Push == nil {
		opts.Push = push.Noop{}
	}

	session := httpapi.NewSessionCookie(opts.PublicURL)
	d := &Dashboard{
		opts:    opts,
		mux:     http.NewServeMux(),
		session: session,
		csrf:    newCSRFCookie(session),
		logins:  newLimiter(loginWindow),
		paths: paths{
			Home: pathHome, Login: pathLogin, Logout: pathLogout,
			Devices: pathDevices, Tokens: pathTokens, Test: pathTest,
			Authorize: pathAuthorize, Docs: pathDocs,
		},
	}
	d.docs = d.buildDocs()
	d.routes()
	return d
}

// routes registers every page. Reads are wrapped in [Dashboard.page], which
// requires a session; writes are wrapped in [Dashboard.form], which requires a
// session, a parsed form and a matching CSRF token.
func (d *Dashboard) routes() {
	d.mux.HandleFunc("GET "+pathHome, d.page(d.showOverview))
	// "/dashboard/" is the same place as "/dashboard"; one canonical URL keeps
	// the nav's aria-current honest.
	d.mux.HandleFunc("GET "+pathHome+"/{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, pathHome, http.StatusMovedPermanently)
	})

	d.mux.HandleFunc("GET "+pathLogin, d.showLogin)
	d.mux.HandleFunc("POST "+pathLogin, d.submitLogin)
	d.mux.HandleFunc("POST "+pathLogout, d.form(d.submitLogout))

	d.mux.HandleFunc("GET "+pathDevices, d.page(d.showDevices))
	d.mux.HandleFunc("POST "+pathDevices+"/{id}/delete", d.form(d.deleteDevice))

	d.mux.HandleFunc("GET "+pathTokens, d.page(d.showTokens))
	d.mux.HandleFunc("POST "+pathTokens, d.form(d.createToken))
	d.mux.HandleFunc("POST "+pathTokens+"/{id}/revoke", d.form(d.revokeToken))

	d.mux.HandleFunc("GET "+pathTest, d.page(d.showTest))
	d.mux.HandleFunc("POST "+pathTest, d.form(d.sendTest))

	// The device-grant approval screen. It is a dashboard page in every way
	// that matters — session, CSRF, same shell — and sits outside the prefix
	// only because a CLI prints its URL into a terminal.
	d.mux.HandleFunc("GET "+pathAuthorize, d.page(d.showAuthorize))
	d.mux.HandleFunc("POST "+pathAuthorize, d.form(d.submitAuthorize))

	// The API contract. It is the one page with no credential anywhere near
	// it, so it is registered raw: no session gate, no CSRF token, nothing to
	// read off the request at all.
	d.mux.HandleFunc("GET "+pathDocs, d.showDocs)

	d.mux.HandleFunc("GET "+pathAssets+"/{file}", d.showAsset)

	// The catch-all. It also picks up a known path reached with the wrong
	// method, which for a surface where every link and form is server-rendered
	// means a stale bookmark rather than a client worth telling apart.
	d.mux.HandleFunc("/", d.notFound)
}

// ServeHTTP dispatches, after stamping the headers every dashboard response
// carries.
//
// The content security policy is the important one: nothing on these pages is
// inline, so scripts and styles are restricted to this origin, with the one
// exception the brand needs — Geist, served by Google Fonts. Images allow any
// HTTPS origin because a service's avatar is a URL the owner supplied.
func (d *Dashboard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Security-Policy", contentSecurityPolicy)
	h.Set("Referrer-Policy", "same-origin")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")

	if r.URL.Path == "/" {
		http.Redirect(w, r, pathHome, http.StatusFound)
		return
	}
	d.mux.ServeHTTP(w, r)
}

const contentSecurityPolicy = "default-src 'none'; " +
	"style-src 'self' https://fonts.googleapis.com; " +
	"font-src https://fonts.gstatic.com; " +
	"script-src 'self'; " +
	"img-src 'self' https: data:; " +
	"form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

// handler is a page or form handler, called with the signed-in owner.
type handler func(w http.ResponseWriter, r *http.Request, p *auth.Principal)

// page gates a read: a session, and a CSRF token issued before the page that
// carries a form is rendered.
//
// An API token is turned away like an anonymous caller. It is a credential for
// an agent, and this surface manages credentials — the same boundary
// [httpapi.RequireSession] draws on the API.
func (d *Dashboard) page(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal := auth.PrincipalFrom(r.Context())
		if !principal.IsSession() {
			d.redirectToLogin(w, r)
			return
		}
		// A token minted for a browser that had none is only on the response so
		// far, so it is carried forward on the context: reading the request's
		// cookie here would hand the page an empty field and make its first
		// submission fail.
		token, err := d.csrf.issue(w, r)
		if err != nil {
			d.fail(w, r, "issuing a CSRF token failed", err)
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), csrfContextKey{}, token)), principal)
	}
}

// csrfContextKey carries the token a page was rendered with.
type csrfContextKey struct{}

// formToken is the token a rendered form must carry: the one just issued when
// this request minted it, and the browser's own otherwise.
func (d *Dashboard) formToken(r *http.Request) string {
	if token, ok := r.Context().Value(csrfContextKey{}).(string); ok {
		return token
	}
	return d.csrf.token(r)
}

// form gates a write: a session, a parsed form body, and a CSRF token that
// matches the cookie.
//
// The CSRF check is the second of two. The first is in internal/httpapi, whose
// authenticator refuses a cookie-authenticated unsafe method from a foreign
// Origin; this one does not depend on the browser sending Origin at all, and
// it also covers sign-in, where there is no session cookie yet and therefore
// nothing for the origin gate to trigger on.
func (d *Dashboard) form(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal := auth.PrincipalFrom(r.Context())
		if !principal.IsSession() {
			d.redirectToLogin(w, r)
			return
		}
		if !d.parseForm(w, r) {
			return
		}
		h(w, r, principal)
	}
}

// parseForm reads the submitted form and verifies its CSRF token, reporting
// whether the handler should continue.
func (d *Dashboard) parseForm(w http.ResponseWriter, r *http.Request) bool {
	if err := r.ParseForm(); err != nil {
		d.renderError(w, r, http.StatusBadRequest, "That form could not be read.")
		return false
	}
	if !d.csrf.verify(r) {
		// A fresh token, so the retry after this page has one that matches.
		if _, err := d.csrf.rotate(w); err != nil {
			d.fail(w, r, "issuing a CSRF token failed", err)
			return false
		}
		d.renderError(w, r, http.StatusForbidden,
			"That form was rejected because its security token did not match. Reload the page and try again.")
		return false
	}
	return true
}

// redirectToLogin sends an unauthenticated browser to sign in, remembering
// where it was going.
func (d *Dashboard) redirectToLogin(w http.ResponseWriter, r *http.Request) {
	target := pathLogin
	if next := safeNext(returnTo(r)); r.Method == http.MethodGet && next != pathHome {
		target += "?next=" + url.QueryEscape(next)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// returnTo is where the browser was heading before it was sent to sign in.
//
// The path alone is enough everywhere but the approval screen, which carries
// the one query worth keeping: the user code the client put in the link. Losing
// it would land the owner on an empty form holding a code they would have to go
// and find again.
func returnTo(r *http.Request) string {
	if r.URL.Path != pathAuthorize {
		return r.URL.Path
	}
	if code, ok := auth.NormalizeUserCode(r.URL.Query().Get("code")); ok {
		return pathAuthorize + "?code=" + code
	}
	return pathAuthorize
}

// safeNext bounds a post-sign-in redirect to this server's own pages.
//
// Anything else — an absolute URL, a scheme-relative one, a path outside the
// prefix — collapses to the home page rather than being followed, so the
// sign-in form cannot be turned into an open redirect.
func safeNext(raw string) string {
	if len(raw) > 512 || raw == pathHome {
		return pathHome
	}

	// The approval screen is the one destination outside the dashboard's
	// prefix, and the one allowed a query. Its code is re-normalised here
	// rather than passed through, so what ends up in the redirect is eight
	// characters of Crockford base32 and a hyphen — nothing a caller chose.
	if target, query, hasQuery := strings.Cut(raw, "?"); target == pathAuthorize {
		if raw, ok := strings.CutPrefix(query, "code="); hasQuery && ok {
			code, err := url.QueryUnescape(raw)
			if normalized, valid := auth.NormalizeUserCode(code); err == nil && valid {
				return pathAuthorize + "?code=" + normalized
			}
		}
		return pathAuthorize
	}

	if !strings.HasPrefix(raw, pathHome+"/") || path.Clean(raw) != raw {
		return pathHome
	}
	for _, c := range raw {
		if c < 0x20 || c == 0x7f || c == '\\' {
			return pathHome
		}
	}
	return raw
}

// redirect answers a completed write. Outcomes travel as a code from a fixed
// vocabulary rather than as a message, so nothing a caller supplies is ever
// reflected back into a page.
func (d *Dashboard) redirect(w http.ResponseWriter, r *http.Request, path, outcome string) {
	if outcome != "" {
		path += "?done=" + url.QueryEscape(outcome)
	}
	http.Redirect(w, r, path, http.StatusSeeOther)
}

// notices is the whole vocabulary of post-redirect banners.
var notices = map[string]notice{
	"device_deleted":  {Kind: noticeOK, Message: "Device unregistered."},
	"token_revoked":   {Kind: noticeOK, Message: "API token revoked. It stops working on the next request that carries it."},
	"signed_out":      {Kind: noticeOK, Message: "Signed out."},
	"client_approved": {Kind: noticeOK, Message: "Client authorized. It collects its token on its next poll — go back to the terminal it is waiting in."},
	"client_denied":   {Kind: noticeOK, Message: "Request denied. The client is told to stop polling and start again."},
}

// notice is the one banner a page can carry.
type notice struct {
	Kind    string
	Message string
}

const (
	noticeOK    = "ok"
	noticeWarn  = "warn"
	noticeError = "error"
)

// noticeFrom resolves the `done` query parameter against the fixed vocabulary.
func noticeFrom(r *http.Request) *notice {
	n, ok := notices[r.URL.Query().Get("done")]
	if !ok {
		return nil
	}
	return &n
}

// view is the shell every page renders inside.
type view struct {
	Title   string
	Section string
	Paths   paths
	Assets  assetLinks
	Version string
	// Username is empty on the sign-in page, which is the one page with no
	// account behind it.
	Username string
	CSRF     string
	Notice   *notice
}

// newView builds the frame for a signed-in page, carrying whatever banner the
// redirect that landed here asked for.
func (d *Dashboard) newView(r *http.Request, p *auth.Principal, title, section string) view {
	v := d.shell(title, section, d.formToken(r), noticeFrom(r))
	v.Username = p.User.Username
	return v
}

// render writes a page.
//
// The template runs into a buffer first: a template that fails halfway would
// otherwise leave a truncated page behind a 200, and the failure would be
// invisible.
func (d *Dashboard) render(w http.ResponseWriter, r *http.Request, status int, t *template.Template, data any) {
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
		d.log(r).ErrorContext(r.Context(), "rendering a dashboard page failed", "error", err)
		http.Error(w, "The dashboard could not render this page.", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	// Every page can show a device token fragment, a token prefix, or a
	// one-time secret. None of it belongs in a shared cache or in a back-button
	// snapshot.
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// renderError draws the standalone error page.
func (d *Dashboard) renderError(w http.ResponseWriter, r *http.Request, status int, message string) {
	d.render(w, r, status, tmplError, errorPage{
		view:    d.shell(http.StatusText(status), "", "", nil),
		Status:  status,
		Message: message,
	})
}

func (d *Dashboard) notFound(w http.ResponseWriter, r *http.Request) {
	d.renderError(w, r, http.StatusNotFound, "There is no such page.")
}

// fail logs the cause and shows the owner a page that says nothing about it.
func (d *Dashboard) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	d.log(r).ErrorContext(r.Context(), what, "error", err)
	d.renderError(w, r, http.StatusInternalServerError, "The server hit an unexpected error.")
}

// log returns the request-scoped logger internal/httpapi installed, or a
// discarding one outside that chain.
func (d *Dashboard) log(r *http.Request) *slog.Logger {
	return httpapi.LoggerFrom(r.Context())
}

// Package httpapi builds the server's HTTP handler: the middleware chain, the
// route table, and the JSON conventions every endpoint shares.
//
// Wire conventions, uniform across the whole surface:
//
//   - API endpoints are rooted directly at the deployment origin.
//   - Operational endpoints such as /healthz are not part of the API.
//   - JSON field names are snake_case.
//   - Timestamps are RFC 3339 strings in UTC, e.g. "2026-08-09T12:34:56.789Z".
//   - Identifiers are UUIDv7 strings.
//   - Errors use one envelope: {"error":{"code":"…","message":"…"}}.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"slices"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/push"
	"github.com/abdeen-labs/hark/internal/secret"
)

// DashboardPrefix is where an embedded admin UI is mounted, when one is wired
// in. It is declared here because the URL space belongs to the router:
// internal/dashboard builds its own links from this constant, which is what
// keeps the mount point and the hrefs from drifting apart.
const DashboardPrefix = "/dashboard"

// DocsPath is where the API contract is published as a page. Like /healthz,
// it describes the deployment rather than operating on account resources.
const DocsPath = "/docs"

// DocsMarkdownPath, OpenAPIPath, and LLMsPath publish public documentation.
const (
	DocsMarkdownPath = "/docs.md"
	OpenAPIPath      = "/openapi.json"
	LLMsPath         = "/llms.txt"
)

// Pinger reports whether a datastore is reachable. *pgxpool.Pool satisfies it.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Nudger is asked to do its work now. The outbound callback worker satisfies
// it: answering a question arms a row the worker owns, and telling it so is the
// difference between a caller hearing back in milliseconds and hearing back on
// the next sweep.
type Nudger interface {
	Nudge()
}

// Options are the server's dependencies.
type Options struct {
	// Logger receives the access log and handler errors. Required.
	Logger *slog.Logger
	// DB is probed by /healthz. Required.
	DB Pinger
	// Auth issues and resolves every credential. Required.
	Auth *auth.Service
	// Store is the persistence layer every non-auth route reads and writes.
	// Required.
	Store *db.Store
	// Secrets seals the credentials the server has to replay — webhook tokens,
	// ActivityKit push tokens, callback tokens — and signs the capabilities a
	// start push hands to a phone. Required.
	Secrets *secret.Keeper
	// Push delivers notifications and Live Activities. A nil Sender is replaced
	// by push.Noop, which records every send as a provider failure rather than
	// pretending it worked.
	Push push.Sender
	// RequesterRatePerMinute and AccountRatePerMinute cap how much delivery work
	// one credential, and the whole account, may ask for in a rolling minute.
	// Zero leaves each at its default.
	RequesterRatePerMinute int
	AccountRatePerMinute   int
	// PublicURL is the origin clients reach this server on. It decides whether
	// the session cookie is Secure and `__Host-` prefixed, which origin may
	// make cookie-authenticated writes, and where device-grant approval links
	// point.
	PublicURL *url.URL
	// TrustedClientIPHeader names the single header a trusted reverse proxy
	// overwrites with the real client address. Empty disables per-client rate
	// limiting rather than keying it off a value a caller can forge.
	TrustedClientIPHeader string
	// MaxRequestBytes caps request bodies. Zero disables the limit.
	MaxRequestBytes int64
	// Version identifies the running build in /healthz output.
	Version string
	// Callbacks is woken when a question that asked to be told the answer is
	// answered. Nil leaves the worker to its own schedule, which is a slower
	// callback and nothing worse.
	Callbacks Nudger
	// Dashboard is the embedded admin UI. It is mounted on the site root, on
	// [DashboardPrefix] and on [DeviceVerificationPath], inside the same
	// middleware chain as the API — so it authenticates through the same
	// session cookie and is covered by the same origin gate.
	//
	// It also serves [DocsPath], and that one is mounted *outside* the chain;
	// see [New]. Nil serves the API alone and leaves all of those paths 404.
	Dashboard http.Handler
}

// New builds the server's handler.
//
// It panics when a required dependency is missing: that is a wiring mistake in
// main, and failing at construction is clearer than failing per request.
func New(opts Options) http.Handler {
	if opts.DB == nil {
		panic("httpapi: Options.DB is required")
	}
	if opts.Auth == nil {
		panic("httpapi: Options.Auth is required")
	}
	if opts.Store == nil {
		panic("httpapi: Options.Store is required")
	}
	if opts.Secrets == nil {
		panic("httpapi: Options.Secrets is required")
	}
	if opts.Logger == nil {
		opts.Logger = discard
	}
	if opts.Push == nil {
		opts.Push = push.Noop{}
	}
	if opts.RequesterRatePerMinute <= 0 {
		opts.RequesterRatePerMinute = defaultRequesterRatePerMinute
	}
	if opts.AccountRatePerMinute <= 0 {
		opts.AccountRatePerMinute = defaultAccountRatePerMinute
	}

	s := &server{
		opts:    opts,
		cookie:  NewSessionCookie(opts.PublicURL),
		limiter: newLimiter(rateLimitWindow),
	}

	rt := newRouter()
	s.routes(rt)

	// The operational middleware every response gets, the public page
	// included: a request id to correlate by, the request-scoped logger, and
	// the net that turns a panic into a 500 instead of a dropped connection.
	base := []Middleware{
		RequestID,
		WithLogger(opts.Logger),
		Recover,
	}

	// What the API adds on top: a cap on what it will read, and the resolver
	// that turns a credential into a principal.
	api := slices.Clone(base)
	if opts.MaxRequestBytes > 0 {
		api = append(api, LimitBody(opts.MaxRequestBytes))
	}
	api = append(api, Authenticate(opts.Auth, opts.PublicURL, opts.Auth.Now))

	handler := Chain(rt.handler(), api...)
	if opts.Dashboard == nil {
		return handler
	}

	// The published contract is served outside the credential chain rather
	// than merely left unguarded inside it. Nothing about a public document
	// should rest on a middleware continuing to ignore it: no cookie is read,
	// no Authorization header is honoured, no session is slid forward, and
	// there is no principal for anything downstream to find.
	root := http.NewServeMux()
	root.Handle("/", handler)
	for _, publicDoc := range []string{DocsPath, DocsMarkdownPath, OpenAPIPath, LLMsPath} {
		root.Handle(publicDoc, Chain(opts.Dashboard, base...))
	}
	return root
}

type server struct {
	opts    Options
	cookie  SessionCookie
	limiter *limiter
}

// routes is the single place every endpoint is registered.
//
// The middleware a route is wrapped in *is* its access-control policy, so the
// policy is readable here rather than scattered through the handlers: session
// only, token only, any credential, or none.
func (s *server) routes(rt *router) {
	rt.handleFunc(http.MethodGet, "/healthz", s.handleHealth)

	// The dashboard owns the site root and its own subtree, and dispatches
	// methods itself: it answers in HTML, so the router's JSON 404 and 405 are
	// the wrong replies for anything inside it.
	//
	// The device-grant approval screen is a dashboard page too. It sits outside
	// the prefix only because its URL is one a client prints into a terminal
	// for a human to open, and /cli/authorize is what that human should see
	// there.
	if s.opts.Dashboard != nil {
		rt.mount("/{$}", s.opts.Dashboard)
		rt.mount(DashboardPrefix, s.opts.Dashboard)
		rt.mount(DashboardPrefix+"/", s.opts.Dashboard)
		rt.mount(DeviceVerificationPath, s.opts.Dashboard)
	}

	// Sign-in is public and rate limited; everything else on the auth surface
	// needs the session it issues.
	rt.handle(http.MethodPost, "/auth/login",
		s.rateLimit("login", limitLogin, http.HandlerFunc(s.handleLogin)))
	rt.handle(http.MethodPost, "/auth/logout",
		RequireAuth(http.HandlerFunc(s.handleLogout)))
	rt.handle(http.MethodGet, "/auth/session",
		RequireAuth(http.HandlerFunc(s.handleSession)))
	rt.handle(http.MethodPost, "/auth/password",
		RequireSession(http.HandlerFunc(s.handleChangePassword)))

	rt.handle(http.MethodGet, "/accounts",
		RequireAdmin(http.HandlerFunc(s.handleListAccounts)))
	rt.handle(http.MethodPost, "/accounts",
		RequireAdmin(http.HandlerFunc(s.handleProvisionAccount)))

	// Device authorization starts and polls without a credential, so both routes
	// are rate limited.
	rt.handle(http.MethodPost, "/auth/device/code",
		s.rateLimit("device_start", limitDeviceStart, http.HandlerFunc(s.handleDeviceCode)))
	rt.handle(http.MethodPost, "/auth/device/token",
		s.rateLimit("device_poll", limitDevicePoll, http.HandlerFunc(s.handleDeviceToken)))

	// The approval half is the human's, and only a session is a human.
	rt.handle(http.MethodGet, "/auth/device/requests/{user_code}",
		RequireSession(http.HandlerFunc(s.handleDeviceRequest)))
	rt.handle(http.MethodPost, "/auth/device/requests/{user_code}/approve",
		RequireSession(http.HandlerFunc(s.handleApproveDeviceRequest)))
	rt.handle(http.MethodPost, "/auth/device/requests/{user_code}/deny",
		RequireSession(http.HandlerFunc(s.handleDenyDeviceRequest)))

	// Managing credentials requires a session, so a token can never widen its
	// own authority or mint a successor. A token retires itself through
	// /auth/logout, which retires whichever credential the caller presented.
	rt.handle(http.MethodGet, "/tokens",
		RequireSession(http.HandlerFunc(s.handleListTokens)))
	rt.handle(http.MethodPost, "/tokens",
		RequireSession(http.HandlerFunc(s.handleCreateToken)))
	rt.handle(http.MethodDelete, "/tokens/{id}",
		RequireSession(http.HandlerFunc(s.handleRevokeToken)))

	// Services own the webhook credential. Creating a service mints that
	// credential and rotating it reissues one, so both are credential
	// management: session only. Editing or deleting a service touches no
	// credential, so a scoped token may do that; reading is a scoped token's
	// business too, though a token never sees the webhook URL itself
	// (§ handleListServices).
	rt.handle(http.MethodGet, "/services",
		s.scoped(db.ScopeServicesRead, s.handleListServices))
	rt.handle(http.MethodPost, "/services",
		RequireSession(http.HandlerFunc(s.handleCreateService)))
	rt.handle(http.MethodGet, "/services/{id}",
		s.scoped(db.ScopeServicesRead, s.handleGetService))
	rt.handle(http.MethodPatch, "/services/{id}",
		s.scoped(db.ScopeServicesWrite, s.handleUpdateService))
	rt.handle(http.MethodDelete, "/services/{id}",
		s.scoped(db.ScopeServicesWrite, s.handleDeleteService))
	rt.handle(http.MethodPost, "/services/{id}/webhook-token",
		RequireSession(http.HandlerFunc(s.handleRotateWebhookToken)))

	// A device registers itself as the account owner: the phone holds the
	// session, and what it registers is where that owner is reachable.
	rt.handle(http.MethodGet, "/devices",
		s.scoped(db.ScopeDevicesRead, s.handleListDevices))
	rt.handle(http.MethodPost, "/devices",
		RequireSession(http.HandlerFunc(s.handleRegisterDevice)))
	rt.handle(http.MethodGet, "/devices/{id}",
		s.scoped(db.ScopeDevicesRead, s.handleGetDevice))
	rt.handle(http.MethodDelete, "/devices/{id}",
		RequireSession(http.HandlerFunc(s.handleDeleteDevice)))
	rt.handle(http.MethodPut, "/devices/{id}/push-to-start-token",
		RequireSession(http.HandlerFunc(s.handleSetPushToStartToken)))
	rt.handle(http.MethodPut, "/devices/{id}/activity-update-token",
		RequireSession(http.HandlerFunc(s.handleRegisterUpdateToken)))

	// The widget's own path: no session, because the process reporting the token
	// may have none. The capability in the body is the credential.
	rt.handleFunc(http.MethodPut, "/activity-deliveries/{id}/update-token",
		s.handleDeliveryUpdateToken)

	rt.handle(http.MethodGet, "/events",
		s.scoped(db.ScopeEventsRead, s.handleListEvents))
	rt.handle(http.MethodDelete, "/events/{id}",
		RequireSession(http.HandlerFunc(s.handleDeleteEvent)))

	// The history is the owner's own record of what reached their phone.
	rt.handle(http.MethodGet, "/history",
		RequireSession(http.HandlerFunc(s.handleListHistory)))
	rt.handle(http.MethodDelete, "/history",
		RequireSession(http.HandlerFunc(s.handleDeleteHistory)))
	// Mount directly to avoid a ServeMux pattern conflict with /history/{id}.
	rt.mount("GET /history/sources",
		RequireSession(http.HandlerFunc(s.handleListHistorySources)))
	rt.handle(http.MethodDelete, "/history/{id}",
		RequireSession(http.HandlerFunc(s.handleDeleteHistoryItem)))

	// Sending is attributed, so it needs a token rather than a session.
	rt.handle(http.MethodPost, "/notifications",
		RequireAPIToken(RequireScopes(db.ScopeNotificationsNew)(http.HandlerFunc(s.handleSendNotification))))

	// Critical services are managed separately but use the same /hooks/{token}
	// contract and delivery pipeline as regular services.
	rt.handle(http.MethodGet, "/critical-services",
		s.scoped(db.ScopeServicesRead, s.handleListCriticalServices))
	rt.handle(http.MethodPost, "/critical-services",
		RequireSession(http.HandlerFunc(s.handleCreateCriticalService)))
	rt.handle(http.MethodGet, "/critical-services/{id}",
		s.scoped(db.ScopeServicesRead, s.handleGetCriticalService))
	rt.handle(http.MethodPatch, "/critical-services/{id}",
		s.scoped(db.ScopeServicesWrite, s.handleUpdateCriticalService))
	rt.handle(http.MethodDelete, "/critical-services/{id}",
		s.scoped(db.ScopeServicesWrite, s.handleDeleteCriticalService))
	rt.handle(http.MethodPost, "/critical-services/{id}/webhook-token",
		RequireSession(http.HandlerFunc(s.handleRotateCriticalWebhookToken)))
	rt.handle(http.MethodGet, "/critical-settings",
		RequireSession(http.HandlerFunc(s.handleGetCriticalSettings)))
	rt.handle(http.MethodPatch, "/critical-settings",
		RequireSession(http.HandlerFunc(s.handleUpdateCriticalSettings)))

	rt.handle(http.MethodPost, "/interactions",
		RequireAPIToken(RequireScopes(db.ScopeInteractionsNew, db.ScopeNotificationsNew)(
			http.HandlerFunc(s.handleCreateInteraction))))
	rt.handle(http.MethodGet, "/interactions",
		s.scoped(db.ScopeInteractionsRead, s.handleListInteractions))
	rt.handle(http.MethodGet, "/interactions/{id}",
		s.scoped(db.ScopeInteractionsRead, s.handleGetInteraction))
	rt.handle(http.MethodPost, "/interactions/{id}/cancel",
		s.scoped(db.ScopeInteractionsNew, s.handleCancelInteraction))
	// Answering is the one route whose policy is in the handler rather than in
	// a middleware, because it takes a credential from the body: the
	// notification-service extension and the Lock Screen widget both run without
	// a session and present the push's `response_token` instead. The policy is
	// "the owner's session, or that token" — an API token is refused, since an
	// agent that could answer its own question makes approval meaningless. See
	// § resolveRespondent.
	rt.handleFunc(http.MethodPost, "/interactions/{id}/response", s.handleRespondToInteraction)

	rt.handle(http.MethodGet, "/activities",
		s.scoped(db.ScopeActivitiesRead, s.handleListActivities))
	rt.handle(http.MethodPost, "/activities",
		RequireAPIToken(RequireScopes(db.ScopeActivitiesWrite)(http.HandlerFunc(s.handleStartActivity))))
	rt.handle(http.MethodGet, "/activities/{identifier}",
		s.scoped(db.ScopeActivitiesRead, s.handleGetActivity))
	rt.handle(http.MethodPatch, "/activities/{identifier}",
		RequireAPIToken(RequireScopes(db.ScopeActivitiesWrite)(http.HandlerFunc(s.handleUpdateActivity))))
	rt.handle(http.MethodPost, "/activities/{identifier}/end",
		RequireAPIToken(RequireScopes(db.ScopeActivitiesWrite)(http.HandlerFunc(s.handleEndActivity))))

	// The webhook surface. Its credential is the token in the path, so none of
	// these routes carries an auth middleware; the handler resolves the service
	// and answers 404 when it cannot.
	rt.handleFunc(http.MethodPost, "/hooks/{token}", s.handleWebhookNotify)
	rt.handleFunc(http.MethodGet, "/hooks/{token}/events/{event_id}", s.handleWebhookEvent)
	rt.handleFunc(http.MethodPost, "/hooks/{token}/events/{event_id}/cancel", s.handleWebhookCancel)
	rt.handleFunc(http.MethodPost, "/hooks/{token}/activities", s.handleWebhookStartActivity)
	rt.handleFunc(http.MethodGet, "/hooks/{token}/activities/{identifier}", s.handleWebhookGetActivity)
	rt.handleFunc(http.MethodPatch, "/hooks/{token}/activities/{identifier}", s.handleWebhookUpdateActivity)
	rt.handleFunc(http.MethodPost, "/hooks/{token}/activities/{identifier}/end", s.handleWebhookEndActivity)
}

// scoped admits the account owner's session unconditionally and an API token
// that carries every listed scope.
//
// Owner sessions pass unconditionally; API tokens require the listed scope.
func (s *server) scoped(scope string, h http.HandlerFunc) http.Handler {
	return RequireAuth(RequireScopes(scope)(h))
}

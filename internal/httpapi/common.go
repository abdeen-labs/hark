package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/id"
)

// now is the server's clock. Every handler stamps rows and compares deadlines
// with it, so a test can move time without sleeping.
func (s *server) now() time.Time { return s.opts.Auth.Now() }

// store is the persistence layer. It is required by every route added after the
// auth surface, and [New] refuses to build a handler without it.
func (s *server) store() *db.Store { return s.opts.Store }

// requester identifies the API token or webhook service that requested a push.
//
// Exactly one of the two ids is set — the database enforces it on every row a
// requester creates — so this type is how a handler carries "who is asking"
// through the shared delivery paths without either surface knowing about the
// other.
type requester struct {
	UserID    string
	TokenID   *string
	ServiceID *string
	// Name is what the requester is called in a history entry: a token's name or
	// a service's title.
	Name string
}

// tokenRequester describes an API token caller.
func tokenRequester(p *auth.Principal) requester {
	return requester{UserID: p.UserID(), TokenID: &p.APIToken.ID, Name: p.APIToken.Name}
}

// serviceRequester describes a webhook caller.
func serviceRequester(svc *db.Service) requester {
	return requester{UserID: svc.UserID, ServiceID: &svc.ID, Name: svc.Title}
}

// listQuery is the shared shape of every paged read.
type listQuery struct {
	Cursor db.Cursor
	Limit  int
}

// parseList reads `limit` and `cursor`, writing the error response itself.
//
// A cursor that did not come from this API is a validation failure rather than
// an empty page: silently starting from the top would look like the list had
// been emptied.
func (s *server) parseList(w http.ResponseWriter, r *http.Request) (listQuery, bool) {
	q := r.URL.Query()

	out := listQuery{Limit: db.DefaultPageSize}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			WriteFieldErrors(w, r, "The request query is invalid.", []FieldError{{
				Field:   "limit",
				Message: rangeMessage(1, db.MaxPageSize),
			}})
			return listQuery{}, false
		}
		out.Limit = db.ClampLimit(n)
	}

	cursor, err := db.ParseCursor(q.Get("cursor"))
	if err != nil {
		WriteFieldErrors(w, r, "The request query is invalid.", []FieldError{{
			Field:   "cursor",
			Message: "must be a next_cursor returned by this endpoint",
		}})
		return listQuery{}, false
	}
	out.Cursor = cursor
	return out, true
}

// nextCursor renders a page's continuation, or null when the page is the last.
func nextCursor[T any](page db.Page[T]) *string {
	if !page.HasMore() {
		return nil
	}
	s := page.Next.String()
	return &s
}

// writeNotFound returns the same response for missing and inaccessible rows.
func (s *server) writeNotFound(w http.ResponseWriter, r *http.Request, what string) {
	WriteError(w, r, http.StatusNotFound, CodeNotFound, "No "+what+" matches that identifier.")
}

// writeConflict answers a request that collides with current state.
func (s *server) writeConflict(w http.ResponseWriter, r *http.Request, message string) {
	WriteError(w, r, http.StatusConflict, CodeConflict, message)
}

// writeStoreError maps a store failure onto a response: a missing row becomes
// the shared 404, anything else is logged and becomes the opaque 500.
func (s *server) writeStoreError(w http.ResponseWriter, r *http.Request, what string, err error) {
	if errors.Is(err, db.ErrNotFound) {
		s.writeNotFound(w, r, what)
		return
	}
	s.writeInternal(w, r, "the "+what+" query failed", err)
}

// RequireAPIToken admits only an API token, and names why when it refuses.
//
// Notification, interaction, and Live Activity records require an API token for
// requester attribution.
func RequireAPIToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal := auth.PrincipalFrom(r.Context())
		switch {
		case principal == nil:
			writeUnauthenticated(w, r)
		case !principal.IsAPIToken():
			WriteError(w, r, http.StatusForbidden, CodeAPITokenRequired,
				"This endpoint requires an API token: every delivery it creates is attributed to one.")
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// newID mints an identifier for a new row.
func newID() string { return id.New() }

// ptr returns a pointer to v, for the many optional columns.
func ptr[T any](v T) *T { return &v }

// deref returns the value behind p, or the zero value.
func deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

// detach returns a context that keeps the request's values (the logger, the
// request id) but not its cancellation.
//
// It is used for the few writes that must not be abandoned when a client hangs
// up mid-request: once APNs has accepted a push, the row describing it has to be
// settled whether or not anyone is still listening.
func detach(ctx context.Context) context.Context { return context.WithoutCancel(ctx) }

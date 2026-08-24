package auth

import (
	"context"

	"github.com/abdeen-labs/hark/internal/db"
)

// Kind names how a request proved who it is.
type Kind string

const (
	// KindSession is a signed-in human: the dashboard's cookie, or the same
	// token presented as a bearer credential by a native client.
	KindSession Kind = "session"
	// KindAPIToken is an agent acting for the account under a scoped token.
	KindAPIToken Kind = "api_token"
)

// Principal is the resolved identity behind one request.
//
// The two kinds differ in authority, not in tenancy: both act for the single
// account. A session has full account access; an API token is limited to its
// granted scopes and cannot create tokens.
type Principal struct {
	Kind Kind
	User db.User

	// Session is set when Kind is [KindSession].
	Session *db.Session
	// APIToken is set when Kind is [KindAPIToken].
	APIToken *db.APIToken

	// Refreshed reports that resolving this request slid the session's expiry
	// forward. The transport re-issues the cookie when it sees this, so a
	// browser's copy never falls behind the row's.
	Refreshed bool
}

// UserID returns the account the principal acts for, or "" for a nil principal.
func (p *Principal) UserID() string {
	if p == nil {
		return ""
	}
	return p.User.ID
}

// IsSession reports whether the caller signed in as the account owner.
func (p *Principal) IsSession() bool { return p != nil && p.Kind == KindSession }

// IsAPIToken reports whether the caller presented an agent token.
func (p *Principal) IsAPIToken() bool { return p != nil && p.Kind == KindAPIToken }

// HasScope reports whether the principal may exercise scope.
//
// Scopes constrain API tokens only. A session is the account owner in person,
// so it satisfies every scope check; a token satisfies exactly what it was
// granted.
func (p *Principal) HasScope(scope string) bool {
	switch {
	case p == nil:
		return false
	case p.Kind == KindSession:
		return true
	case p.APIToken == nil:
		return false
	default:
		return p.APIToken.HasScope(scope)
	}
}

// HasScopes reports whether the principal may exercise all of them.
func (p *Principal) HasScopes(scopes ...string) bool {
	for _, s := range scopes {
		if !p.HasScope(s) {
			return false
		}
	}
	return true
}

type principalKey struct{}

// WithPrincipal returns a context carrying p.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the request's principal, or nil when the request is
// anonymous. Handlers behind a Require* middleware can dereference it freely.
func PrincipalFrom(ctx context.Context) *Principal {
	p, _ := ctx.Value(principalKey{}).(*Principal)
	return p
}

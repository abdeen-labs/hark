package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abdeen-labs/hark/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The credential flows are guarded UPDATEs and transactions, and none of that
// can be exercised against a fake. These tests require PostgreSQL and skip
// without it:
//
//	TEST_DATABASE_URL=postgres://hark:hark@localhost:5432/hark_test go test ./internal/auth
//
// They run inside their own schema rather than `public`, so `go test ./...`
// can run this package and internal/db against the same scratch database at
// the same time without either resetting the other's tables.
const testSchema = "hark_auth_test"

func testDatabaseURL(t *testing.T) string {
	t.Helper()
	for _, key := range []string{"TEST_DATABASE_URL", "HARK_TEST_DATABASE_URL"} {
		if v := os.Getenv(key); v != "" {
			return v
		}
	}
	t.Skip("TEST_DATABASE_URL is not set")
	return ""
}

var (
	schemaOnce sync.Once
	schemaPool *pgxpool.Pool
	schemaErr  error
)

// allTables is every table these tests write, for the per-test reset.
const allTables = `users, sessions, api_tokens, device_authorization_requests`

// requireService returns a Service over a freshly emptied schema, together with
// a clock the test controls. Expiry and sliding refresh are the whole subject
// here, and none of it should be tested by sleeping.
func requireService(t *testing.T) (context.Context, *Service, *testClock) {
	t.Helper()
	raw := testDatabaseURL(t)

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	t.Cleanup(cancel)

	schemaOnce.Do(func() {
		pool, err := db.Open(context.WithoutCancel(ctx), db.Config{
			URL:            withSearchPath(raw, testSchema),
			MaxConns:       4,
			ConnectTimeout: 10 * time.Second,
		})
		if err != nil {
			schemaErr = err
			return
		}
		// Recreate the schema so it matches the migration ledger.
		if _, err := pool.Exec(ctx,
			"DROP SCHEMA IF EXISTS "+testSchema+" CASCADE; CREATE SCHEMA "+testSchema); err != nil {
			schemaErr = err
			return
		}
		if err := db.Migrate(ctx, pool, db.Migrations(), slog.New(slog.DiscardHandler)); err != nil {
			schemaErr = err
			return
		}
		schemaPool = pool
	})
	if schemaErr != nil {
		t.Fatalf("prepare test schema: %v", schemaErr)
	}

	if _, err := schemaPool.Exec(ctx, "TRUNCATE "+allTables+" RESTART IDENTITY CASCADE"); err != nil {
		t.Fatalf("reset tables: %v", err)
	}

	clock := &testClock{now: time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)}
	return ctx, New(db.New(schemaPool), clock.Now), clock
}

// withSearchPath points a DSN at a dedicated schema. pgx forwards unrecognised
// query parameters as connection runtime parameters, which is exactly what
// search_path is.
func withSearchPath(raw, schema string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

const testPassword = "correct horse battery staple"

func seedAccount(t *testing.T, ctx context.Context, s *Service) *db.User {
	t.Helper()
	user, err := s.CreateAccount(ctx, CreateAccountParams{Username: "admin", Password: testPassword})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	return user
}

// TestCreateAccountIsSingleUser is the invariant that replaces a closed sign-up
// route: there is no second way in because there is no second account.
func TestCreateAccountIsSingleUser(t *testing.T) {
	ctx, service, _ := requireService(t)

	user := seedAccount(t, ctx, service)
	if user.Username != "admin" || user.Email != "admin@hark.local" {
		t.Errorf("seeded account = %+v, want a lowercased handle and a derived email", user)
	}
	if user.PasswordHash == nil || !strings.HasPrefix(*user.PasswordHash, "$argon2id$") {
		t.Errorf("stored password hash = %v, want an argon2id PHC string", user.PasswordHash)
	}

	_, err := service.CreateAccount(ctx, CreateAccountParams{Username: "someone", Password: testPassword})
	if !errors.Is(err, ErrAccountExists) {
		t.Errorf("second CreateAccount = %v, want ErrAccountExists", err)
	}
}

func TestCreateAccountValidates(t *testing.T) {
	ctx, service, _ := requireService(t)

	for name, p := range map[string]CreateAccountParams{
		"short username":   {Username: "ab", Password: testPassword},
		"punctuation":      {Username: "ad min!", Password: testPassword},
		"short password":   {Username: "admin", Password: "short"},
		"malformed email":  {Username: "admin", Password: testPassword, Email: "not-an-address"},
		"control password": {Username: "admin", Password: "hunter2hunter2\n"},
	} {
		_, err := service.CreateAccount(ctx, p)
		var bad *InvalidInputError
		if !errors.As(err, &bad) {
			t.Errorf("%s: CreateAccount = %v, want an InvalidInputError", name, err)
		}
	}
}

func TestLogin(t *testing.T) {
	ctx, service, clock := requireService(t)
	user := seedAccount(t, ctx, service)

	principal, token, err := service.Login(ctx, "ADMIN", testPassword)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if principal.User.ID != user.ID || !principal.IsSession() {
		t.Errorf("principal = %+v, want a session for the seeded account", principal)
	}
	if !ValidSessionToken(token) {
		t.Errorf("issued token %q does not have the session shape", token)
	}
	if want := clock.Now().Add(SessionTTL); !principal.Session.ExpiresAt.Equal(db.Millis(want)) {
		t.Errorf("expiry = %s, want %s", principal.Session.ExpiresAt, want)
	}

	// The row keeps a digest, never the token: a database dump hands over
	// nothing usable.
	if principal.Session.TokenHash == token || principal.Session.TokenHash != SessionTokenHash(token) {
		t.Errorf("stored token hash = %q, want the digest of the issued token", principal.Session.TokenHash)
	}

	resolved, err := service.AuthenticateSession(ctx, token)
	if err != nil {
		t.Fatalf("AuthenticateSession: %v", err)
	}
	if resolved.Session.ID != principal.Session.ID {
		t.Errorf("resolved session %q, want %q", resolved.Session.ID, principal.Session.ID)
	}
}

func TestLoginRejectsBadCredentials(t *testing.T) {
	ctx, service, _ := requireService(t)
	seedAccount(t, ctx, service)

	for name, credentials := range map[string][2]string{
		"wrong password":   {"admin", "not the password"},
		"unknown user":     {"someone", testPassword},
		"empty password":   {"admin", ""},
		"empty username":   {"", testPassword},
		"impossible name":  {"a", testPassword},
		"case-wrong pass":  {"admin", strings.ToUpper(testPassword)},
		"trailing space":   {"admin", testPassword + " "},
		"username as pass": {"admin", "admin"},
	} {
		_, _, err := service.Login(ctx, credentials[0], credentials[1])
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("%s: Login = %v, want ErrInvalidCredentials", name, err)
		}
	}
}

// TestSessionSlidingRefresh covers the three regimes: recent enough that
// nothing is written, idle enough that expiry slides forward, and idle past the
// TTL so the session is gone.
func TestSessionSlidingRefresh(t *testing.T) {
	ctx, service, clock := requireService(t)
	seedAccount(t, ctx, service)

	_, token, err := service.Login(ctx, "admin", testPassword)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	clock.Advance(SessionRefreshInterval / 2)
	fresh, err := service.AuthenticateSession(ctx, token)
	if err != nil {
		t.Fatalf("AuthenticateSession: %v", err)
	}
	if fresh.Refreshed {
		t.Error("a session was refreshed before the refresh interval elapsed")
	}

	clock.Advance(SessionRefreshInterval)
	slid, err := service.AuthenticateSession(ctx, token)
	if err != nil {
		t.Fatalf("AuthenticateSession: %v", err)
	}
	if !slid.Refreshed {
		t.Fatal("a session past the refresh interval was not refreshed")
	}
	if want := clock.Now().Add(SessionTTL); !slid.Session.ExpiresAt.Equal(db.Millis(want)) {
		t.Errorf("refreshed expiry = %s, want %s", slid.Session.ExpiresAt, want)
	}

	clock.Advance(SessionTTL + time.Minute)
	if _, err := service.AuthenticateSession(ctx, token); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("AuthenticateSession(idle past the TTL) = %v, want ErrInvalidCredentials", err)
	}
	// The read is what cleans up: there is no sweeper to depend on.
	if _, err := service.AuthenticateSession(ctx, token); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("the expired session was not deleted on read: %v", err)
	}
}

// TestSessionMaxLifetime checks the ceiling refresh cannot cross, so a session
// used forever does not live forever.
func TestSessionMaxLifetime(t *testing.T) {
	ctx, service, clock := requireService(t)
	seedAccount(t, ctx, service)

	_, token, err := service.Login(ctx, "admin", testPassword)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	// Use it every day right up to the ceiling; each read slides the expiry
	// forward, and none of them may push it past the ceiling.
	deadline := clock.Now().Add(SessionMaxLifetime)
	for clock.Now().Before(deadline.Add(-SessionTTL)) {
		clock.Advance(24 * time.Hour)
		if _, err := service.AuthenticateSession(ctx, token); err != nil {
			t.Fatalf("session died before its maximum lifetime at %s: %v", clock.Now(), err)
		}
	}

	clock.Advance(SessionMaxLifetime)
	if _, err := service.AuthenticateSession(ctx, token); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("a session past its maximum lifetime still resolved: %v", err)
	}
}

func TestLogout(t *testing.T) {
	ctx, service, _ := requireService(t)
	seedAccount(t, ctx, service)

	principal, token, err := service.Login(ctx, "admin", testPassword)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	if err := service.Logout(ctx, principal.Session.ID); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, err := service.AuthenticateSession(ctx, token); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("a signed-out session still resolved: %v", err)
	}
	// Idempotent: signing out twice is the same outcome as once.
	if err := service.Logout(ctx, principal.Session.ID); err != nil {
		t.Errorf("second Logout = %v, want nil", err)
	}
}

// TestChangePassword checks the property that makes it safe to offer at all:
// every other session dies, and the caller's own survives.
func TestChangePassword(t *testing.T) {
	ctx, service, _ := requireService(t)
	seedAccount(t, ctx, service)

	caller, callerToken, err := service.Login(ctx, "admin", testPassword)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	_, otherToken, err := service.Login(ctx, "admin", testPassword)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	const newPassword = "an entirely different passphrase"
	if err := service.ChangePassword(ctx, caller, "wrong", newPassword); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("ChangePassword with the wrong current password = %v, want ErrInvalidCredentials", err)
	}
	var bad *InvalidInputError
	if err := service.ChangePassword(ctx, caller, testPassword, "short"); !errors.As(err, &bad) {
		t.Fatalf("ChangePassword to a weak password = %v, want an InvalidInputError", err)
	}
	if err := service.ChangePassword(ctx, caller, testPassword, newPassword); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}

	if _, err := service.AuthenticateSession(ctx, callerToken); err != nil {
		t.Errorf("the caller's own session was signed out: %v", err)
	}
	if _, err := service.AuthenticateSession(ctx, otherToken); !errors.Is(err, ErrInvalidCredentials) {
		t.Error("another session survived a password change")
	}
	if _, _, err := service.Login(ctx, "admin", testPassword); !errors.Is(err, ErrInvalidCredentials) {
		t.Error("the old password still signs in")
	}
	if _, _, err := service.Login(ctx, "admin", newPassword); err != nil {
		t.Errorf("the new password does not sign in: %v", err)
	}
}

func TestSetPasswordSignsOutEverything(t *testing.T) {
	ctx, service, _ := requireService(t)
	seedAccount(t, ctx, service)

	_, token, err := service.Login(ctx, "admin", testPassword)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	const recovered = "a passphrase set from the command line"
	if err := service.SetPassword(ctx, "admin", recovered); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	if _, err := service.AuthenticateSession(ctx, token); !errors.Is(err, ErrInvalidCredentials) {
		t.Error("a session survived a password reset")
	}
	if _, _, err := service.Login(ctx, "admin", recovered); err != nil {
		t.Errorf("the reset password does not sign in: %v", err)
	}
	if err := service.SetPassword(ctx, "nobody", recovered); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetPassword for an unknown account = %v, want ErrNotFound", err)
	}
}

func TestAPITokenLifecycle(t *testing.T) {
	ctx, service, clock := requireService(t)
	user := seedAccount(t, ctx, service)

	token, secret, err := service.CreateAPIToken(ctx, user.ID, CreateAPITokenParams{
		Name:      "  harkctl  ",
		Scopes:    []string{"notifications:send", "interactions:create", "notifications:send"},
		ExpiresIn: 90 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	if token.Name != "harkctl" {
		t.Errorf("name = %q, want it trimmed", token.Name)
	}
	if got, want := token.Scopes, []string{"interactions:create", "notifications:send"}; !equalStrings(got, want) {
		t.Errorf("scopes = %v, want them deduplicated and sorted as %v", got, want)
	}
	if !ValidAPIToken(secret) {
		t.Errorf("issued secret %q does not have the API-token shape", secret)
	}
	if token.TokenHash == secret || token.TokenHash != APITokenHash(secret) {
		t.Errorf("stored hash = %q, want the digest of the issued secret", token.TokenHash)
	}
	if !strings.HasPrefix(secret, token.Prefix) || len(token.Prefix) != APITokenDisplayLength {
		t.Errorf("display prefix = %q, want the first %d characters of the secret", token.Prefix, APITokenDisplayLength)
	}

	principal, err := service.AuthenticateAPIToken(ctx, secret)
	if err != nil {
		t.Fatalf("AuthenticateAPIToken: %v", err)
	}
	if !principal.IsAPIToken() || principal.UserID() != user.ID {
		t.Errorf("principal = %+v, want an API token for the account", principal)
	}
	if !principal.HasScopes("interactions:create", "notifications:send") {
		t.Error("the resolved principal lost its scopes")
	}
	if principal.HasScope("activities:write") {
		t.Error("the resolved principal gained a scope it was never granted")
	}

	tokens, err := service.ListAPITokens(ctx, user.ID)
	if err != nil || len(tokens) != 1 {
		t.Fatalf("ListAPITokens = %d tokens, %v; want 1, nil", len(tokens), err)
	}

	if err := service.RevokeAPIToken(ctx, token.ID, user.ID); err != nil {
		t.Fatalf("RevokeAPIToken: %v", err)
	}
	if _, err := service.AuthenticateAPIToken(ctx, secret); !errors.Is(err, ErrInvalidCredentials) {
		t.Error("a revoked token still authenticates")
	}
	if err := service.RevokeAPIToken(ctx, token.ID, user.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoking twice = %v, want ErrNotFound", err)
	}
	// Revoked tokens stay listed so the account's credential history keeps
	// making sense.
	if tokens, _ := service.ListAPITokens(ctx, user.ID); len(tokens) != 1 || tokens[0].RevokedAt == nil {
		t.Errorf("after revocation the listing shows %+v, want the token still listed and marked revoked", tokens)
	}

	// Expiry is judged in Go so that a NULL expiry keeps meaning "never".
	never, neverSecret, err := service.CreateAPIToken(ctx, user.ID, CreateAPITokenParams{
		Name: "no expiry", Scopes: []string{"events:read"},
	})
	if err != nil {
		t.Fatalf("CreateAPIToken without an expiry: %v", err)
	}
	if never.ExpiresAt != nil {
		t.Errorf("expires_at = %v, want NULL", never.ExpiresAt)
	}
	clock.Advance(10 * 365 * 24 * time.Hour)
	if _, err := service.AuthenticateAPIToken(ctx, neverSecret); err != nil {
		t.Errorf("a token with no expiry stopped working after ten years: %v", err)
	}
}

func TestAPITokenExpires(t *testing.T) {
	ctx, service, clock := requireService(t)
	user := seedAccount(t, ctx, service)

	_, secret, err := service.CreateAPIToken(ctx, user.ID, CreateAPITokenParams{
		Name: "short-lived", Scopes: []string{"events:read"}, ExpiresIn: 2 * time.Hour,
	})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	clock.Advance(time.Hour)
	if _, err := service.AuthenticateAPIToken(ctx, secret); err != nil {
		t.Fatalf("token failed before its expiry: %v", err)
	}
	clock.Advance(2 * time.Hour)
	if _, err := service.AuthenticateAPIToken(ctx, secret); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("an expired token still authenticates: %v", err)
	}
}

func TestAPITokenValidation(t *testing.T) {
	ctx, service, _ := requireService(t)
	user := seedAccount(t, ctx, service)

	for name, p := range map[string]CreateAPITokenParams{
		"no name":       {Scopes: []string{"events:read"}},
		"long name":     {Name: strings.Repeat("n", MaxAPITokenNameLength+1), Scopes: []string{"events:read"}},
		"no scopes":     {Name: "x"},
		"unknown scope": {Name: "x", Scopes: []string{"events:read", "the:moon"}},
		"brief expiry":  {Name: "x", Scopes: []string{"events:read"}, ExpiresIn: time.Minute},
		"eternal":       {Name: "x", Scopes: []string{"events:read"}, ExpiresIn: 10 * 365 * 24 * time.Hour},
	} {
		_, _, err := service.CreateAPIToken(ctx, user.ID, p)
		var bad *InvalidInputError
		if !errors.As(err, &bad) {
			t.Errorf("%s: CreateAPIToken = %v, want an InvalidInputError", name, err)
		}
	}
}

// TestAPITokenCap verifies the maximum number of active credentials.
func TestAPITokenCap(t *testing.T) {
	ctx, service, _ := requireService(t)
	user := seedAccount(t, ctx, service)

	var lastID string
	for i := range db.MaxActiveAPITokens {
		token, _, err := service.CreateAPIToken(ctx, user.ID, CreateAPITokenParams{
			Name: "token", Scopes: []string{"events:read"},
		})
		if err != nil {
			t.Fatalf("CreateAPIToken %d: %v", i, err)
		}
		lastID = token.ID
	}

	if _, _, err := service.CreateAPIToken(ctx, user.ID, CreateAPITokenParams{
		Name: "one too many", Scopes: []string{"events:read"},
	}); !errors.Is(err, ErrTokenLimit) {
		t.Fatalf("CreateAPIToken past the cap = %v, want ErrTokenLimit", err)
	}

	// Revoking frees a slot: the cap counts active tokens, not rows.
	if err := service.RevokeAPIToken(ctx, lastID, user.ID); err != nil {
		t.Fatalf("RevokeAPIToken: %v", err)
	}
	if _, _, err := service.CreateAPIToken(ctx, user.ID, CreateAPITokenParams{
		Name: "replacement", Scopes: []string{"events:read"},
	}); err != nil {
		t.Errorf("revoking a token did not free a slot: %v", err)
	}
}

func TestDeviceGrantEndToEnd(t *testing.T) {
	ctx, service, clock := requireService(t)
	user := seedAccount(t, ctx, service)

	grant, err := service.StartDeviceGrant(ctx, StartDeviceGrantParams{
		ClientName: "harkctl",
		Scopes:     []string{"notifications:send", "interactions:create"},
	})
	if err != nil {
		t.Fatalf("StartDeviceGrant: %v", err)
	}
	if !ValidDeviceCode(grant.DeviceCode) {
		t.Errorf("device code %q does not have the expected shape", grant.DeviceCode)
	}
	if grant.Request.DeviceCodeHash == grant.DeviceCode {
		t.Error("the pairing row stores the device code in plaintext")
	}
	if _, ok := NormalizeUserCode(grant.Request.UserCode); !ok {
		t.Errorf("user code %q is not in the canonical form", grant.Request.UserCode)
	}
	if want := clock.Now().Add(DefaultAPITokenLifetime); !grant.Request.TokenExpiresAt.Equal(db.Millis(want)) {
		t.Errorf("token expiry = %s, want the default lifetime %s", grant.Request.TokenExpiresAt, want)
	}

	// The request is still awaiting a decision.
	result, err := service.PollDeviceGrant(ctx, grant.DeviceCode)
	if err != nil {
		t.Fatalf("PollDeviceGrant: %v", err)
	}
	if result.State != DeviceGrantPending || result.Interval != DevicePollInterval {
		t.Fatalf("first poll = %+v, want pending at the initial interval", result)
	}

	// Polling immediately again ratchets the interval up rather than answering.
	result, err = service.PollDeviceGrant(ctx, grant.DeviceCode)
	if err != nil {
		t.Fatalf("PollDeviceGrant: %v", err)
	}
	if result.State != DeviceGrantSlowDown || result.Interval != DevicePollInterval+DevicePollBackoffStep {
		t.Fatalf("second poll = %+v, want slow_down at a raised interval", result)
	}

	// The approval screen shows the human what they are agreeing to.
	shown, err := service.DeviceGrantByUserCode(ctx, strings.ToLower(strings.ReplaceAll(grant.Request.UserCode, "-", " ")))
	if err != nil {
		t.Fatalf("DeviceGrantByUserCode: %v", err)
	}
	if shown.ClientName != "harkctl" || shown.Status != db.DeviceAuthPending {
		t.Errorf("shown request = %+v, want the pending harkctl request", shown)
	}
	if got, want := shown.RequestedScopes, []string{"interactions:create", "notifications:send"}; !equalStrings(got, want) {
		t.Errorf("requested scopes = %v, want %v", got, want)
	}

	approved, err := service.ApproveDeviceGrant(ctx, grant.Request.UserCode, user.ID)
	if err != nil {
		t.Fatalf("ApproveDeviceGrant: %v", err)
	}
	if approved.Status != db.DeviceAuthApproved {
		t.Fatalf("status after approval = %q", approved.Status)
	}

	clock.Advance(DevicePollIntervalMax)
	result, err = service.PollDeviceGrant(ctx, grant.DeviceCode)
	if err != nil {
		t.Fatalf("PollDeviceGrant: %v", err)
	}
	if result.State != DeviceGrantIssued {
		t.Fatalf("poll after approval = %+v, want a token", result)
	}
	if !ValidAPIToken(result.Secret) {
		t.Errorf("issued secret %q does not have the API-token shape", result.Secret)
	}
	if result.Token.Name != "harkctl" {
		t.Errorf("minted token name = %q, want the client name", result.Token.Name)
	}
	if !result.Token.ExpiresAt.Equal(grant.Request.TokenExpiresAt) {
		t.Errorf("minted token expiry = %v, want the requested %v", result.Token.ExpiresAt, grant.Request.TokenExpiresAt)
	}

	principal, err := service.AuthenticateAPIToken(ctx, result.Secret)
	if err != nil {
		t.Fatalf("the device-granted token does not authenticate: %v", err)
	}
	if !principal.HasScopes("interactions:create", "notifications:send") {
		t.Errorf("the granted token lost its scopes: %+v", principal.APIToken.Scopes)
	}

	// A pairing request issues exactly one token, ever.
	clock.Advance(DevicePollIntervalMax)
	again, err := service.PollDeviceGrant(ctx, grant.DeviceCode)
	if err != nil {
		t.Fatalf("PollDeviceGrant: %v", err)
	}
	if again.State != DeviceGrantConsumed {
		t.Errorf("polling a spent request = %+v, want consumed", again)
	}
}

func TestDeviceGrantDenial(t *testing.T) {
	ctx, service, clock := requireService(t)
	seedAccount(t, ctx, service)

	grant, err := service.StartDeviceGrant(ctx, StartDeviceGrantParams{
		ClientName: "harkctl", Scopes: []string{"events:read"},
	})
	if err != nil {
		t.Fatalf("StartDeviceGrant: %v", err)
	}

	if _, err := service.DenyDeviceGrant(ctx, grant.Request.UserCode); err != nil {
		t.Fatalf("DenyDeviceGrant: %v", err)
	}

	clock.Advance(DevicePollInterval)
	result, err := service.PollDeviceGrant(ctx, grant.DeviceCode)
	if err != nil {
		t.Fatalf("PollDeviceGrant: %v", err)
	}
	if result.State != DeviceGrantDenied {
		t.Errorf("poll after denial = %+v, want denied", result)
	}

	// A decision is final: approving after a denial changes nothing.
	if _, err := service.ApproveDeviceGrant(ctx, grant.Request.UserCode, "someone"); !errors.Is(err, ErrConflict) {
		t.Errorf("approving a denied request = %v, want ErrConflict", err)
	}
}

func TestDeviceGrantExpiry(t *testing.T) {
	ctx, service, clock := requireService(t)
	user := seedAccount(t, ctx, service)

	grant, err := service.StartDeviceGrant(ctx, StartDeviceGrantParams{
		ClientName: "harkctl", Scopes: []string{"events:read"},
	})
	if err != nil {
		t.Fatalf("StartDeviceGrant: %v", err)
	}

	clock.Advance(DeviceRequestTTL + time.Second)

	result, err := service.PollDeviceGrant(ctx, grant.DeviceCode)
	if err != nil {
		t.Fatalf("PollDeviceGrant: %v", err)
	}
	if result.State != DeviceGrantExpired {
		t.Fatalf("poll after the TTL = %+v, want expired", result)
	}

	// The approval screen agrees, and approving is refused.
	shown, err := service.DeviceGrantByUserCode(ctx, grant.Request.UserCode)
	if err != nil {
		t.Fatalf("DeviceGrantByUserCode: %v", err)
	}
	if shown.Status != db.DeviceAuthExpired {
		t.Errorf("status = %q, want expired", shown.Status)
	}
	if _, err := service.ApproveDeviceGrant(ctx, grant.Request.UserCode, user.ID); !errors.Is(err, ErrConflict) {
		t.Errorf("approving an expired request = %v, want ErrConflict", err)
	}
}

func TestDeviceGrantUnknownCodes(t *testing.T) {
	ctx, service, _ := requireService(t)
	seedAccount(t, ctx, service)

	for name, code := range map[string]string{
		"malformed": "not-a-device-code",
		"empty":     "",
		"unknown":   NewDeviceCode(),
	} {
		if _, err := service.PollDeviceGrant(ctx, code); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: PollDeviceGrant = %v, want ErrNotFound", name, err)
		}
	}

	for name, code := range map[string]string{
		"malformed": "!!!!-!!!!",
		"unknown":   NewUserCode(),
	} {
		if _, err := service.DeviceGrantByUserCode(ctx, code); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: DeviceGrantByUserCode = %v, want ErrNotFound", name, err)
		}
	}
}

// TestDeviceGrantRespectsTheTokenCap burns the approval rather than leaving a
// client polling forever against a wall it cannot get past.
func TestDeviceGrantRespectsTheTokenCap(t *testing.T) {
	ctx, service, clock := requireService(t)
	user := seedAccount(t, ctx, service)

	for i := range db.MaxActiveAPITokens {
		if _, _, err := service.CreateAPIToken(ctx, user.ID, CreateAPITokenParams{
			Name: "filler", Scopes: []string{"events:read"},
		}); err != nil {
			t.Fatalf("CreateAPIToken %d: %v", i, err)
		}
	}

	grant, err := service.StartDeviceGrant(ctx, StartDeviceGrantParams{
		ClientName: "harkctl", Scopes: []string{"events:read"},
	})
	if err != nil {
		t.Fatalf("StartDeviceGrant: %v", err)
	}
	if _, err := service.ApproveDeviceGrant(ctx, grant.Request.UserCode, user.ID); err != nil {
		t.Fatalf("ApproveDeviceGrant: %v", err)
	}

	clock.Advance(DevicePollInterval)
	result, err := service.PollDeviceGrant(ctx, grant.DeviceCode)
	if err != nil {
		t.Fatalf("PollDeviceGrant: %v", err)
	}
	if result.State != DeviceGrantTokenLimit {
		t.Fatalf("poll at the cap = %+v, want token_limit", result)
	}

	clock.Advance(DevicePollInterval)
	after, err := service.PollDeviceGrant(ctx, grant.DeviceCode)
	if err != nil {
		t.Fatalf("PollDeviceGrant: %v", err)
	}
	if after.State != DeviceGrantDenied {
		t.Errorf("the burned request polls as %+v, want denied", after)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

package db

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/abdeen-labs/hark/internal/id"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The store tests need a real PostgreSQL: every interesting behaviour here is a
// guarded UPDATE, a partial unique index or a CTE, and none of that can be
// exercised against a fake. They skip when no scratch database is configured.
//
//	TEST_DATABASE_URL=postgres://hark:hark@localhost:5432/hark_test go test ./internal/db
//
// The database is reset — schema and all — before the first test runs, so point
// this at something disposable.
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

// allTables is every table the store writes, for the per-test reset.
const allTables = `users, sessions, services, devices, api_tokens,
	device_authorization_requests, events, agent_notifications, interactions,
	live_activities, live_activity_deliveries, live_activity_operations,
	live_activity_delivery_attempts`

// requireStore returns a Store against a freshly emptied schema.
func requireStore(t *testing.T) (context.Context, *Store) {
	t.Helper()
	url := testDatabaseURL(t)

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	t.Cleanup(cancel)

	schemaOnce.Do(func() {
		pool, err := Open(context.WithoutCancel(ctx), Config{URL: url, MaxConns: 4, ConnectTimeout: 10 * time.Second})
		if err != nil {
			schemaErr = err
			return
		}
		// Recreate the schema so it matches the migration ledger.
		if _, err := pool.Exec(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public"); err != nil {
			schemaErr = err
			return
		}
		if err := Migrate(ctx, pool, Migrations(), testLogger()); err != nil {
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
	return ctx, New(schemaPool)
}

func mustUser(ctx context.Context, t *testing.T, s *Store, username string) *User {
	t.Helper()
	u, err := s.Users.Create(ctx, CreateUserParams{
		ID:           id.New(),
		Username:     username,
		Email:        username + "@hark.local",
		DisplayName:  username,
		PasswordHash: ptr("salt:key"),
		Now:          time.Now(),
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u
}

func mustService(ctx context.Context, t *testing.T, s *Store, userID, title string) *Service {
	t.Helper()
	svc, err := s.Services.Create(ctx, CreateServiceParams{
		ID:              id.New(),
		UserID:          userID,
		Title:           title,
		Priority:        PriorityNormal,
		TokenHash:       id.New(),
		TokenCiphertext: "v1.iv.tag.ct",
		Now:             time.Now(),
	})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	return svc
}

func mustCriticalService(ctx context.Context, t *testing.T, s *Store, userID, title string) *Service {
	t.Helper()
	svc, err := s.Services.Create(ctx, CreateServiceParams{
		ID: id.New(), UserID: userID, Title: title, Priority: PriorityNormal,
		CriticalCapable: true, CriticalEnabled: true,
		TokenHash: id.New(), TokenCiphertext: "v1.iv.tag.ct", Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("create critical service: %v", err)
	}
	return svc
}

func mustToken(ctx context.Context, t *testing.T, s *Store, userID string) *APIToken {
	t.Helper()
	tok, err := s.APITokens.Create(ctx, CreateAPITokenParams{
		ID:        id.New(),
		UserID:    userID,
		Name:      "harkctl",
		TokenHash: id.New(),
		Prefix:    "hark_abcd",
		Scopes:    []string{ScopeActivitiesRead, ScopeNotificationsNew},
		Now:       time.Now(),
	})
	if err != nil {
		t.Fatalf("create API token: %v", err)
	}
	return tok
}

// mustDevice registers a Live-Activity-capable device.
func mustDevice(ctx context.Context, t *testing.T, s *Store, userID, token string) *Device {
	t.Helper()
	now := time.Now()
	reg, err := s.Devices.Register(ctx, RegisterDeviceParams{
		ID:                             id.New(),
		UserID:                         userID,
		APNsToken:                      token,
		Name:                           ptr("iPhone"),
		InteractionSchemaVersion:       ptr(InteractionSchemaVersion),
		LiveActivityInteractionVersion: ptr(LiveActivityInteractionVersion),
		Now:                            now,
	})
	if err != nil {
		t.Fatalf("register device: %v", err)
	}
	dev, err := s.Devices.SetPushToStartToken(ctx, SetPushToStartTokenParams{
		DeviceID:      reg.Device.ID,
		UserID:        userID,
		Ciphertext:    "v1.iv.tag.ct",
		Environment:   EnvironmentSandbox,
		SchemaVersion: LiveActivitySchemaVersion,
		Now:           now,
	})
	if err != nil {
		t.Fatalf("store push-to-start token: %v", err)
	}
	return dev
}

func props(title, status string) json.RawMessage {
	return json.RawMessage(`{"schemaVersion":1,"title":"` + title + `","status":"` + status + `"}`)
}

func TestUsersAndSessions(t *testing.T) {
	ctx, s := requireStore(t)
	now := time.Now()

	user := mustUser(ctx, t, s, "ali")
	if n, err := s.Users.Count(ctx); err != nil || n != 1 {
		t.Fatalf("Count() = (%d, %v), want (1, nil)", n, err)
	}

	// Timestamps must survive the round trip at millisecond resolution: HMACs
	// and concurrency predicates compare them.
	if !user.CreatedAt.Equal(Millis(user.CreatedAt)) {
		t.Errorf("CreatedAt = %s carries sub-millisecond precision", user.CreatedAt)
	}

	byName, err := s.Users.ByUsername(ctx, "ali")
	if err != nil || byName.ID != user.ID {
		t.Fatalf("ByUsername = (%v, %v)", byName, err)
	}
	if _, err := s.Users.ByUsername(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ByUsername(unknown) error = %v, want ErrNotFound", err)
	}

	// The welcome claim is one-shot: the successful claimant sends the push, and it is
	// never handed out again.
	claimed, err := s.Users.ClaimWelcome(ctx, user.ID, now)
	if err != nil || !claimed {
		t.Fatalf("first ClaimWelcome = (%v, %v), want (true, nil)", claimed, err)
	}
	claimed, err = s.Users.ClaimWelcome(ctx, user.ID, now.Add(time.Hour))
	if err != nil || claimed {
		t.Fatalf("second ClaimWelcome = (%v, %v), want (false, nil)", claimed, err)
	}

	session, err := s.Sessions.Create(ctx, CreateSessionParams{
		ID: id.New(), UserID: user.ID, TokenHash: "hash", ExpiresAt: now.Add(7 * 24 * time.Hour), Now: now,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	auth, err := s.Sessions.ByTokenHash(ctx, "hash")
	if err != nil {
		t.Fatalf("ByTokenHash: %v", err)
	}
	if auth.Session.ID != session.ID || auth.User.Username != "ali" {
		t.Errorf("ByTokenHash returned %+v", auth)
	}

	refreshed, err := s.Sessions.Refresh(ctx, session.ID, now.Add(14*24*time.Hour), now.Add(time.Hour))
	if err != nil || !refreshed {
		t.Fatalf("Refresh = (%v, %v)", refreshed, err)
	}
	auth, err = s.Sessions.ByTokenHash(ctx, "hash")
	if err != nil {
		t.Fatal(err)
	}
	if !auth.Session.ExpiresAt.After(session.ExpiresAt) {
		t.Error("Refresh did not extend the session")
	}

	// A second session, kept alive while everything else is signed out.
	keep, err := s.Sessions.Create(ctx, CreateSessionParams{
		ID: id.New(), UserID: user.ID, TokenHash: "keep", ExpiresAt: now.Add(time.Hour), Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.Sessions.DeleteForUser(ctx, user.ID, keep.ID)
	if err != nil || n != 1 {
		t.Fatalf("DeleteForUser = (%d, %v), want (1, nil)", n, err)
	}
	if _, err := s.Sessions.ByTokenHash(ctx, "keep"); err != nil {
		t.Errorf("the kept session was signed out: %v", err)
	}

	if n, err := s.Sessions.DeleteExpired(ctx, now.Add(2*time.Hour)); err != nil || n != 1 {
		t.Fatalf("DeleteExpired = (%d, %v), want (1, nil)", n, err)
	}
}

func TestServiceUpdateIsPartial(t *testing.T) {
	ctx, s := requireStore(t)
	user := mustUser(ctx, t, s, "ali")

	svc, err := s.Services.Create(ctx, CreateServiceParams{
		ID: id.New(), UserID: user.ID, Title: "Deploy bot",
		ImageURL: ptr("https://example.com/logo.png"), URL: ptr("https://example.com/deploys"),
		Priority: PriorityNormal, TokenHash: "hash-1", TokenCiphertext: "v1.a.b.c", Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}

	// Set one field, clear another, leave the third alone.
	updated, err := s.Services.Update(ctx, UpdateServiceParams{
		ID: svc.ID, UserID: user.ID,
		Title:    Value("Deploys"),
		ImageURL: Value[*string](nil),
		Now:      time.Now(),
	})
	if err != nil {
		t.Fatalf("update service: %v", err)
	}
	if updated.Title != "Deploys" {
		t.Errorf("Title = %q, want %q", updated.Title, "Deploys")
	}
	if updated.ImageURL != nil {
		t.Errorf("ImageURL = %v, want nil", *updated.ImageURL)
	}
	if updated.URL == nil || *updated.URL != "https://example.com/deploys" {
		t.Errorf("URL = %v, want it untouched", updated.URL)
	}
	if updated.Priority != PriorityNormal {
		t.Errorf("Priority = %q, want it untouched", updated.Priority)
	}
	if !updated.UpdatedAt.After(svc.UpdatedAt) {
		t.Error("UpdatedAt was not bumped")
	}

	if _, err := s.Services.Update(ctx, UpdateServiceParams{
		ID: svc.ID, UserID: "someone-else", Title: Value("hijacked"), Now: time.Now(),
	}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update scoped to another owner error = %v, want ErrNotFound", err)
	}

	// Rotation must invalidate the old credential immediately.
	if _, err := s.Services.RotateToken(ctx, svc.ID, user.ID, "hash-2", "v1.d.e.f", time.Now()); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if _, err := s.Services.ByTokenHash(ctx, "hash-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the rotated-away token still authenticates: %v", err)
	}
	if got, err := s.Services.ByTokenHash(ctx, "hash-2"); err != nil || got.ID != svc.ID {
		t.Errorf("ByTokenHash(new) = (%v, %v)", got, err)
	}
}

func TestServiceDeleteCascades(t *testing.T) {
	ctx, s := requireStore(t)
	user := mustUser(ctx, t, s, "ali")
	svc := mustService(ctx, t, s, user.ID, "Deploy bot")

	event, err := s.Events.Create(ctx, CreateEventParams{
		ID: id.New(), ServiceID: svc.ID, Title: "t", Body: "b",
		Priority: PriorityNormal, Status: EventProcessing, Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}

	deleted, err := s.Services.Delete(ctx, svc.ID, user.ID)
	if err != nil || !deleted {
		t.Fatalf("Delete = (%v, %v)", deleted, err)
	}
	if _, err := s.Events.ByID(ctx, event.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the service's events survived the delete: %v", err)
	}
}

func TestDeviceRegistrationUpsert(t *testing.T) {
	ctx, s := requireStore(t)
	user := mustUser(ctx, t, s, "ali")
	const token = "aabbccdd"
	now := time.Now()

	first, err := s.Devices.Register(ctx, RegisterDeviceParams{
		ID: id.New(), UserID: user.ID, APNsToken: token, Name: ptr("iPhone"),
		InteractionSchemaVersion: ptr(1), LiveActivityInteractionVersion: ptr(1), Now: now,
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if first.PreviousUserID != nil || first.OwnerChanged() {
		t.Errorf("a first registration reported a previous owner: %+v", first)
	}

	// Re-registering keeps the identity but replaces the optional fields
	// wholesale — the client sends its full state every time, so an omitted
	// name means the name is gone, not unchanged.
	second, err := s.Devices.Register(ctx, RegisterDeviceParams{
		ID: id.New(), UserID: user.ID, APNsToken: token, Now: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("re-register: %v", err)
	}
	if second.Device.ID != first.Device.ID {
		t.Errorf("id changed on re-registration: %q -> %q", first.Device.ID, second.Device.ID)
	}
	if !second.Device.CreatedAt.Equal(first.Device.CreatedAt) {
		t.Error("CreatedAt was overwritten on re-registration")
	}
	if second.Device.Name != nil {
		t.Errorf("Name = %v, want it cleared by the omission", *second.Device.Name)
	}
	if second.Device.InteractionSchemaVersion != nil {
		t.Error("the capability flag survived an omission")
	}
	if second.OwnerChanged() {
		t.Error("re-registering to the same account is not an owner change")
	}
	if !second.Device.LastSeenAt.After(first.Device.LastSeenAt) {
		t.Error("LastSeenAt was not bumped")
	}
}

func TestDeviceOwnerChangeDropsLiveActivityState(t *testing.T) {
	ctx, s := requireStore(t)
	ali := mustUser(ctx, t, s, "ali")
	sam := mustUser(ctx, t, s, "sam")
	const token = "aabbccdd"

	device := mustDevice(ctx, t, s, ali.ID, token)
	if !device.LiveActivityCapable() {
		t.Fatal("the seeded device should be Live Activity capable")
	}

	// Give it a live delivery, so the invalidation has something to act on.
	tok := mustToken(ctx, t, s, ali.ID)
	started, err := s.Activities.Start(ctx, StartActivityParams{
		ID: id.New(), UserID: ali.ID, RequesterTokenID: &tok.ID,
		SchemaVersion: LiveActivitySchemaVersion, Props: props("Deploy", "Building"),
		ExpiresAt: time.Now().Add(time.Hour), OperationID: id.New(),
		Targets: []ActivityTarget{{
			DeliveryID: id.New(), DeviceID: device.ID,
			Environment: EnvironmentSandbox, Purpose: PurposeTask,
		}},
		Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("start activity: %v", err)
	}

	moved, err := s.Devices.Register(ctx, RegisterDeviceParams{
		ID: id.New(), UserID: sam.ID, APNsToken: token, Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("re-register under a new owner: %v", err)
	}
	if !moved.OwnerChanged() {
		t.Fatalf("OwnerChanged() = false, PreviousUserID = %v", moved.PreviousUserID)
	}
	if moved.Device.PushToStartTokenCiphertext != nil ||
		moved.Device.PushToStartEnvironment != nil ||
		moved.Device.PushToStartUpdatedAt != nil ||
		moved.Device.LiveActivitySchemaVersion != nil {
		t.Errorf("push-to-start state survived an owner change: %+v", moved.Device)
	}

	n, err := s.Deliveries.FailForDevice(ctx, device.ID, ReasonOwnerChanged, time.Now())
	if err != nil || n != 1 {
		t.Fatalf("FailForDevice = (%d, %v), want (1, nil)", n, err)
	}
	delivery, err := s.Deliveries.ByID(ctx, started.Deliveries[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if delivery.Status != DeliveryFailed || delivery.LastAPNsReason == nil || *delivery.LastAPNsReason != ReasonOwnerChanged {
		t.Errorf("delivery after owner change = %+v", delivery)
	}
}

func TestDeviceTargetsAndDeactivation(t *testing.T) {
	ctx, s := requireStore(t)
	user := mustUser(ctx, t, s, "ali")
	first := mustDevice(ctx, t, s, user.ID, "aaaa")
	second := mustDevice(ctx, t, s, user.ID, "bbbb")

	targets, err := s.Devices.ListTargets(ctx, user.ID, nil)
	if err != nil || len(targets) != 2 {
		t.Fatalf("ListTargets = (%d devices, %v), want 2", len(targets), err)
	}
	// Most recently seen first, so a fan-out reaches the phone in the user's
	// hand before the one in a drawer.
	if !targets[0].LastSeenAt.After(targets[1].LastSeenAt) && targets[0].ID != second.ID {
		t.Errorf("targets are not ordered by last_seen_at desc: %v, %v", targets[0].LastSeenAt, targets[1].LastSeenAt)
	}

	filtered, err := s.Devices.ListTargets(ctx, user.ID, []string{first.ID})
	if err != nil || len(filtered) != 1 || filtered[0].ID != first.ID {
		t.Fatalf("filtered ListTargets = (%v, %v)", filtered, err)
	}
	// A device id from another account simply does not come back; the caller
	// compares counts and rejects the request.
	stray, err := s.Devices.ListByIDs(ctx, user.ID, []string{first.ID, id.New()})
	if err != nil || len(stray) != 1 {
		t.Fatalf("ListByIDs = (%d, %v), want 1", len(stray), err)
	}

	if n, err := s.Devices.Deactivate(ctx, []string{"aaaa"}); err != nil || n != 1 {
		t.Fatalf("Deactivate = (%d, %v), want (1, nil)", n, err)
	}
	if n, err := s.Devices.Deactivate(ctx, []string{"aaaa"}); err != nil || n != 0 {
		t.Fatalf("re-deactivating an inactive device = (%d, %v), want (0, nil)", n, err)
	}
	targets, err = s.Devices.ListTargets(ctx, user.ID, nil)
	if err != nil || len(targets) != 1 {
		t.Fatalf("after deactivation ListTargets = (%d, %v), want 1", len(targets), err)
	}
}

func TestAPITokenLifecycle(t *testing.T) {
	ctx, s := requireStore(t)
	user := mustUser(ctx, t, s, "ali")
	now := time.Now()

	active, err := s.APITokens.Create(ctx, CreateAPITokenParams{
		ID: id.New(), UserID: user.ID, Name: "harkctl", TokenHash: "hash-1", Prefix: "hark_aaaa",
		Scopes: []string{ScopeNotificationsNew, ScopeActivitiesRead}, Now: now,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.APITokens.Create(ctx, CreateAPITokenParams{
		ID: id.New(), UserID: user.ID, Name: "expired", TokenHash: "hash-2", Prefix: "hark_bbbb",
		Scopes: []string{ScopeEventsRead}, ExpiresAt: ptr(now.Add(-time.Minute)), Now: now,
	}); err != nil {
		t.Fatalf("create expired: %v", err)
	}

	// The cap counts what can still authenticate, so an expired token does not
	// occupy a slot.
	if n, err := s.APITokens.CountActive(ctx, user.ID, now); err != nil || n != 1 {
		t.Fatalf("CountActive = (%d, %v), want (1, nil)", n, err)
	}

	// An unknown scope is refused by the database, not just by validation.
	if _, err := s.APITokens.Create(ctx, CreateAPITokenParams{
		ID: id.New(), UserID: user.ID, Name: "bad", TokenHash: "hash-3", Prefix: "hark_cccc",
		Scopes: []string{"activities:destroy"}, Now: now,
	}); !IsCheckViolation(err) {
		t.Errorf("creating a token with an invented scope error = %v, want a CHECK violation", err)
	}

	// Expired and revoked tokens still resolve: the caller decides, so that
	// every failure looks the same from outside.
	found, err := s.APITokens.ByTokenHash(ctx, "hash-2")
	if err != nil {
		t.Fatalf("ByTokenHash(expired): %v", err)
	}
	if found.Active(now) {
		t.Error("an expired token reported itself active")
	}

	revoked, err := s.APITokens.Revoke(ctx, active.ID, user.ID, now)
	if err != nil || !revoked {
		t.Fatalf("Revoke = (%v, %v)", revoked, err)
	}
	revoked, err = s.APITokens.Revoke(ctx, active.ID, user.ID, now)
	if err != nil || revoked {
		t.Fatalf("re-revoking = (%v, %v), want (false, nil)", revoked, err)
	}
	if n, err := s.APITokens.CountActive(ctx, user.ID, now); err != nil || n != 0 {
		t.Fatalf("CountActive after revoke = (%d, %v), want (0, nil)", n, err)
	}

	// An agent can retire its own credential without a session.
	selfRevoked, err := s.APITokens.Create(ctx, CreateAPITokenParams{
		ID: id.New(), UserID: user.ID, Name: "self", TokenHash: "hash-4", Prefix: "hark_dddd",
		Scopes: []string{ScopeEventsRead}, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.APITokens.RevokeSelf(ctx, selfRevoked.ID, now); err != nil || !ok {
		t.Fatalf("RevokeSelf = (%v, %v)", ok, err)
	}
	if ok, err := s.APITokens.RevokeSelf(ctx, selfRevoked.ID, now); err != nil || ok {
		t.Fatalf("second RevokeSelf = (%v, %v), want (false, nil)", ok, err)
	}

	// The last-used stamp is throttled: an agent making many requests a second
	// must not dirty the row on each one.
	touched, err := s.APITokens.TouchLastUsed(ctx, active.ID, now, time.Minute)
	if err != nil || !touched {
		t.Fatalf("first TouchLastUsed = (%v, %v)", touched, err)
	}
	touched, err = s.APITokens.TouchLastUsed(ctx, active.ID, now.Add(30*time.Second), time.Minute)
	if err != nil || touched {
		t.Fatalf("throttled TouchLastUsed = (%v, %v), want (false, nil)", touched, err)
	}
	touched, err = s.APITokens.TouchLastUsed(ctx, active.ID, now.Add(2*time.Minute), time.Minute)
	if err != nil || !touched {
		t.Fatalf("TouchLastUsed after the interval = (%v, %v)", touched, err)
	}
}

func TestDeviceAuthorizationFlow(t *testing.T) {
	ctx, s := requireStore(t)
	user := mustUser(ctx, t, s, "ali")
	now := time.Now()

	req, err := s.DeviceAuth.Create(ctx, CreateDeviceAuthorizationParams{
		ID: id.New(), DeviceCodeHash: "code-hash", UserCode: "ABCD-EFGH", ClientName: "harkctl",
		RequestedScopes: []string{ScopeNotificationsNew}, ExpiresAt: now.Add(10 * time.Minute),
		TokenExpiresAt: now.Add(90 * 24 * time.Hour), PollIntervalSeconds: 5, Now: now,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !req.Pending(now) {
		t.Error("a fresh request should be pending")
	}

	// The browser side of the flow looks the request up by the code a person
	// typed in.
	if byCode, err := s.DeviceAuth.ByUserCode(ctx, "ABCD-EFGH"); err != nil || byCode.ID != req.ID {
		t.Fatalf("ByUserCode = (%v, %v)", byCode, err)
	}
	if _, err := s.DeviceAuth.ByUserCode(ctx, "ZZZZ-ZZZZ"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ByUserCode(unknown) error = %v, want ErrNotFound", err)
	}

	// Polling too fast widens the interval, up to the ceiling.
	slowed, err := s.DeviceAuth.SlowDown(ctx, req.ID, 5, 30, now)
	if err != nil || slowed.PollIntervalSeconds != 10 {
		t.Fatalf("SlowDown = (%v, %v), want interval 10", slowed, err)
	}
	for range 10 {
		if slowed, err = s.DeviceAuth.SlowDown(ctx, req.ID, 5, 30, now); err != nil {
			t.Fatal(err)
		}
	}
	if slowed.PollIntervalSeconds != 30 {
		t.Errorf("interval = %d, want it capped at 30", slowed.PollIntervalSeconds)
	}

	approved, err := s.DeviceAuth.Approve(ctx, "ABCD-EFGH", user.ID, now)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if approved.Status != DeviceAuthApproved || approved.ApprovedUserID == nil || *approved.ApprovedUserID != user.ID {
		t.Errorf("approved = %+v", approved)
	}
	// Approval is one-shot: a second decision cannot flip it.
	if _, err := s.DeviceAuth.Deny(ctx, "ABCD-EFGH", now); !errors.Is(err, ErrNotFound) {
		t.Errorf("denying an approved request error = %v, want ErrNotFound", err)
	}

	consumed, err := s.DeviceAuth.Consume(ctx, req.ID, now)
	if err != nil || !consumed {
		t.Fatalf("Consume = (%v, %v)", consumed, err)
	}
	// Two racing polls must not both mint a token.
	consumed, err = s.DeviceAuth.Consume(ctx, req.ID, now)
	if err != nil || consumed {
		t.Fatalf("second Consume = (%v, %v), want (false, nil)", consumed, err)
	}

	// A consumed request is past expiring.
	if _, err := s.DeviceAuth.MarkExpired(ctx, req.ID, now.Add(time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Errorf("expiring a consumed request error = %v, want ErrNotFound", err)
	}

	if n, err := s.DeviceAuth.PurgeResolved(ctx, now.Add(48*time.Hour), 24*time.Hour); err != nil || n != 1 {
		t.Fatalf("PurgeResolved = (%d, %v), want (1, nil)", n, err)
	}
}

func TestEventIdempotencyAndPaging(t *testing.T) {
	ctx, s := requireStore(t)
	user := mustUser(ctx, t, s, "ali")
	svc := mustService(ctx, t, s, user.ID, "Deploy bot")
	now := time.Now()

	first, err := s.Events.Create(ctx, CreateEventParams{
		ID: id.New(), ServiceID: svc.ID, Title: "Deploy bot", Body: "Build 4821 succeeded",
		Priority: PriorityNormal, Status: EventProcessing,
		IdempotencyKey: ptr("build-4821"), RequestHash: ptr("hash"), Now: now,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// The unique index is the race guard: the loser re-reads and replays
	// instead of pushing a second time.
	_, err = s.Events.Create(ctx, CreateEventParams{
		ID: id.New(), ServiceID: svc.ID, Title: "Deploy bot", Body: "Build 4821 succeeded",
		Priority: PriorityNormal, Status: EventProcessing,
		IdempotencyKey: ptr("build-4821"), RequestHash: ptr("hash"), Now: now,
	})
	if !IsUniqueViolation(err, "events_service_idempotency_key") {
		t.Fatalf("duplicate key error = %v, want a violation of events_service_idempotency_key", err)
	}
	replay, err := s.Events.ByIdempotencyKey(ctx, svc.ID, "build-4821")
	if err != nil || replay.ID != first.ID {
		t.Fatalf("ByIdempotencyKey = (%v, %v)", replay, err)
	}

	// Rows without a key must never collide, which is what NULLS DISTINCT
	// gives us: most events carry no key at all.
	for range 3 {
		if _, err := s.Events.Create(ctx, CreateEventParams{
			ID: id.New(), ServiceID: svc.ID, Title: "t", Body: "b",
			Priority: PriorityNormal, Status: EventAccepted, Now: now,
		}); err != nil {
			t.Fatalf("keyless event: %v", err)
		}
	}

	settled, err := s.Events.Settle(ctx, first.ID, EventPartial, 2, ptr("BadDeviceToken"))
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if settled.Status != EventPartial || settled.DeliveredCount != 2 || *settled.Error != "BadDeviceToken" {
		t.Errorf("settled = %+v", settled)
	}

	if n, err := s.Events.CountForServiceSince(ctx, svc.ID, now.Add(-time.Minute)); err != nil || n != 4 {
		t.Fatalf("CountForServiceSince = (%d, %v), want (4, nil)", n, err)
	}
	if n, err := s.Events.CountForUserSince(ctx, user.ID, now.Add(time.Minute)); err != nil || n != 0 {
		t.Fatalf("CountForUserSince outside the window = (%d, %v), want (0, nil)", n, err)
	}

	// Paging walks the whole list exactly once, with no repeats across the
	// boundary.
	page, err := s.Events.ListForUser(ctx, user.ID, Cursor{}, 3)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page.Items) != 3 || !page.HasMore() {
		t.Fatalf("first page has %d items, more=%v", len(page.Items), page.HasMore())
	}
	if page.Items[0].ServiceTitle != "Deploy bot" {
		t.Errorf("ServiceTitle = %q", page.Items[0].ServiceTitle)
	}
	next, err := s.Events.ListForUser(ctx, user.ID, page.Next, 3)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(next.Items) != 1 || next.HasMore() {
		t.Fatalf("second page has %d items, more=%v", len(next.Items), next.HasMore())
	}
	seen := map[string]bool{}
	for _, item := range append(page.Items, next.Items...) {
		if seen[item.ID] {
			t.Errorf("event %s appeared on both pages", item.ID)
		}
		seen[item.ID] = true
	}
}

func TestEventsListForService(t *testing.T) {
	ctx, s := requireStore(t)
	user := mustUser(ctx, t, s, "ali")
	svc := mustService(ctx, t, s, user.ID, "Deploy bot")
	other := mustService(ctx, t, s, user.ID, "Backups")
	now := time.Now()

	for i, serviceID := range []string{svc.ID, svc.ID, svc.ID, other.ID} {
		if _, err := s.Events.Create(ctx, CreateEventParams{
			ID: id.New(), ServiceID: serviceID, Title: "t", Body: "b",
			Priority: PriorityNormal, Status: EventAccepted,
			Now: now.Add(time.Duration(i) * time.Millisecond),
		}); err != nil {
			t.Fatalf("seed event: %v", err)
		}
	}

	// Only the service's own rows, and paging walks them exactly once.
	page, err := s.Events.ListForService(ctx, svc.ID, user.ID, Cursor{}, 2)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(page.Items) != 2 || !page.HasMore() {
		t.Fatalf("first page has %d items, more=%v", len(page.Items), page.HasMore())
	}
	next, err := s.Events.ListForService(ctx, svc.ID, user.ID, page.Next, 2)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(next.Items) != 1 || next.HasMore() {
		t.Fatalf("second page has %d items, more=%v", len(next.Items), next.HasMore())
	}
	for _, item := range append(page.Items, next.Items...) {
		if item.ServiceID != svc.ID {
			t.Errorf("event %s belongs to service %s", item.ID, item.ServiceID)
		}
	}

	// A service id alone is not enough: the wrong owner reads nothing.
	stranger := mustUser(ctx, t, s, "mallory")
	if page, err := s.Events.ListForService(ctx, svc.ID, stranger.ID, Cursor{}, 10); err != nil || len(page.Items) != 0 {
		t.Fatalf("foreign owner = (%d items, %v), want none", len(page.Items), err)
	}
}

func TestInteractionResponseIsGuarded(t *testing.T) {
	ctx, s := requireStore(t)
	user := mustUser(ctx, t, s, "ali")
	svc := mustService(ctx, t, s, user.ID, "Deploy bot")
	device := mustDevice(ctx, t, s, user.ID, "aaaa")
	now := time.Now()

	event, err := s.Events.Create(ctx, CreateEventParams{
		ID: id.New(), ServiceID: svc.ID, Title: "Deploy bot", Body: "Deploy to production?",
		Priority: PriorityNormal, Status: EventProcessing, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}

	interaction, err := s.Interactions.Create(ctx, CreateInteractionParams{
		ID: id.New(), UserID: user.ID, RequesterServiceID: &svc.ID, EventID: &event.ID,
		Title: "Deploy bot", Prompt: "Deploy to production?", Kind: InteractionApproval,
		Presentation: PresentationNotification, Choices: ChoicesFor(InteractionApproval),
		CorrelationID: ptr("deploy-4711"), ActionDigest: "digest", ResponseTokenHash: ptr("resp-hash"),
		CallbackURL: ptr("https://ci.example.com/hark"), CallbackTokenCiphertext: ptr("v1.a.b.c"),
		ExpiresAt: now.Add(15 * time.Minute), Now: now,
	})
	if err != nil {
		t.Fatalf("create interaction: %v", err)
	}
	if interaction.CallbackStatus == nil || *interaction.CallbackStatus != CallbackPending {
		t.Errorf("CallbackStatus = %v, want pending", interaction.CallbackStatus)
	}
	if interaction.CallbackNextAttemptAt == nil {
		t.Error("a callback interaction should be due immediately")
	}

	// A signed-in app reads it by id, and only its owner can.
	if mine, err := s.Interactions.ByIDForUser(ctx, interaction.ID, user.ID); err != nil || mine.ID != interaction.ID {
		t.Fatalf("ByIDForUser = (%v, %v)", mine, err)
	}
	if _, err := s.Interactions.ByIDForUser(ctx, interaction.ID, id.New()); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-account read error = %v, want ErrNotFound", err)
	}

	// The response-token lookup is the phone's credential.
	if _, err := s.Interactions.ByResponseToken(ctx, interaction.ID, "wrong"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ByResponseToken with a wrong digest error = %v, want ErrNotFound", err)
	}
	if _, err := s.Interactions.ByResponseToken(ctx, interaction.ID, "resp-hash"); err != nil {
		t.Fatalf("ByResponseToken: %v", err)
	}

	answered, err := s.Interactions.Respond(ctx, RespondParams{
		ID: interaction.ID, UserID: user.ID, Status: InteractionApproved,
		Response: ptr("approve"), DeviceID: &device.ID, TriggerCallback: true, Now: now,
	})
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	if answered.Status != InteractionApproved || answered.RespondedAt == nil ||
		answered.RespondingDeviceID == nil || *answered.RespondingDeviceID != device.ID {
		t.Errorf("answered = %+v", answered)
	}

	// Two phones racing produce exactly one winner.
	if _, err := s.Interactions.Respond(ctx, RespondParams{
		ID: interaction.ID, UserID: user.ID, Status: InteractionDenied,
		Response: ptr("deny"), DeviceID: &device.ID, Now: now,
	}); !errors.Is(err, ErrNotFound) {
		t.Errorf("second response error = %v, want ErrNotFound", err)
	}

	// Deleting the device it was answered from must not delete the history.
	if _, err := s.Devices.Delete(ctx, device.ID, user.ID); err != nil {
		t.Fatal(err)
	}
	orphaned, err := s.Interactions.ByID(ctx, interaction.ID)
	if err != nil {
		t.Fatalf("the interaction went with the device: %v", err)
	}
	if orphaned.RespondingDeviceID != nil {
		t.Error("responding_device_id should have been nulled, not cascaded")
	}
}

func TestInteractionExpiryAndCancel(t *testing.T) {
	ctx, s := requireStore(t)
	user := mustUser(ctx, t, s, "ali")
	tok := mustToken(ctx, t, s, user.ID)
	now := time.Now()

	create := func(expiresAt time.Time) *Interaction {
		t.Helper()
		i, err := s.Interactions.Create(ctx, CreateInteractionParams{
			ID: id.New(), UserID: user.ID, RequesterTokenID: &tok.ID,
			Title: "Claude Code", Prompt: "Allow once?", Kind: InteractionApproval,
			Presentation: PresentationNotification, Choices: ChoicesFor(InteractionApproval),
			ActionDigest: "digest", ExpiresAt: expiresAt, Now: now,
		})
		if err != nil {
			t.Fatalf("create interaction: %v", err)
		}
		return i
	}

	lapsed := create(now.Add(-time.Second))
	expired, err := s.Interactions.ExpireIfDue(ctx, lapsed.ID, now)
	if err != nil {
		t.Fatalf("ExpireIfDue: %v", err)
	}
	if expired.Status != InteractionExpired || expired.RespondedAt != nil || expired.CanceledAt != nil {
		t.Errorf("expired = %+v, want a bare expiry", expired)
	}
	// Expiry is idempotent; concurrent callers re-read the stored result.
	if _, err := s.Interactions.ExpireIfDue(ctx, lapsed.ID, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("second ExpireIfDue error = %v, want ErrNotFound", err)
	}
	// And an expired question can no longer be answered.
	if _, err := s.Interactions.Respond(ctx, RespondParams{
		ID: lapsed.ID, UserID: user.ID, Status: InteractionApproved, Response: ptr("approve"), Now: now,
	}); !errors.Is(err, ErrNotFound) {
		t.Errorf("responding to an expired interaction error = %v, want ErrNotFound", err)
	}

	live := create(now.Add(time.Minute))
	canceled, err := s.Interactions.CancelForToken(ctx, live.ID, tok.ID, now)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if canceled.Status != InteractionCanceled || canceled.CanceledAt == nil || canceled.RespondedAt != nil {
		t.Errorf("canceled = %+v", canceled)
	}
	if _, err := s.Interactions.CancelForToken(ctx, live.ID, tok.ID, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("re-cancelling error = %v, want ErrNotFound", err)
	}

	// Another token cannot reach it at all.
	other := mustToken(ctx, t, s, user.ID)
	if _, err := s.Interactions.ByIDForToken(ctx, live.ID, other.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-token read error = %v, want ErrNotFound", err)
	}

	// The pending inbox shows neither the expired nor the cancelled one.
	pending := create(now.Add(time.Minute))
	page, err := s.Interactions.ListPending(ctx, user.ID, now, Cursor{}, 50)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != pending.ID {
		t.Fatalf("pending inbox = %d items, want only %s", len(page.Items), pending.ID)
	}
	if page.Items[0].SourceName != "harkctl" {
		t.Errorf("SourceName = %q, want the requesting token's name", page.Items[0].SourceName)
	}
}

func TestInteractionRequesterExclusivity(t *testing.T) {
	ctx, s := requireStore(t)
	user := mustUser(ctx, t, s, "ali")
	svc := mustService(ctx, t, s, user.ID, "Deploy bot")
	tok := mustToken(ctx, t, s, user.ID)

	// An interaction belongs to exactly one requester; the database refuses
	// both and neither, so no code path can invent a third kind of asker.
	for _, tc := range []struct {
		name             string
		tokenID, service *string
	}{
		{"both", &tok.ID, &svc.ID},
		{"neither", nil, nil},
	} {
		_, err := s.Interactions.Create(ctx, CreateInteractionParams{
			ID: id.New(), UserID: user.ID, RequesterTokenID: tc.tokenID, RequesterServiceID: tc.service,
			Title: "t", Prompt: "p", Kind: InteractionApproval, Presentation: PresentationNotification,
			Choices: ChoicesFor(InteractionApproval), ActionDigest: "digest",
			ExpiresAt: time.Now().Add(time.Minute), Now: time.Now(),
		})
		if !IsCheckViolation(err) {
			t.Errorf("%s requester: error = %v, want a CHECK violation", tc.name, err)
		}
	}
}

// TestInteractionListIsScopedToTheRequestingToken pins the list filter that
// keeps one integration from reading another's questions. A nil requester is
// the session's account-wide view; a token id narrows every page — cursors
// included — to the rows that token asked, so neither another token's
// questions nor a webhook service's ever appear.
func TestInteractionListIsScopedToTheRequestingToken(t *testing.T) {
	ctx, s := requireStore(t)
	user := mustUser(ctx, t, s, "ali")
	tokA := mustToken(ctx, t, s, user.ID)
	tokB := mustToken(ctx, t, s, user.ID)
	svc := mustService(ctx, t, s, user.ID, "Deploy bot")
	base := time.Now()

	create := func(seq int, tokenID, serviceID *string) *Interaction {
		t.Helper()
		i, err := s.Interactions.Create(ctx, CreateInteractionParams{
			ID: id.New(), UserID: user.ID, RequesterTokenID: tokenID, RequesterServiceID: serviceID,
			Title: "t", Prompt: "p", Kind: InteractionApproval, Presentation: PresentationNotification,
			Choices: ChoicesFor(InteractionApproval), ActionDigest: "digest",
			ExpiresAt: base.Add(time.Hour), Now: base.Add(time.Duration(seq) * time.Second),
		})
		if err != nil {
			t.Fatalf("create interaction %d: %v", seq, err)
		}
		return i
	}

	a1 := create(1, &tokA.ID, nil)
	a2 := create(2, &tokA.ID, nil)
	create(3, &tokB.ID, nil)
	create(4, nil, &svc.ID)
	a3 := create(5, &tokA.ID, nil)
	// One of token A's questions is answered, so the pending filter has
	// something of token A's own to exclude.
	answered := create(6, &tokA.ID, nil)
	if _, err := s.Interactions.Respond(ctx, RespondParams{
		ID: answered.ID, UserID: user.ID, Status: InteractionApproved,
		Response: ptr("approve"), Now: base,
	}); err != nil {
		t.Fatalf("respond: %v", err)
	}

	now := base.Add(10 * time.Second)

	// A nil requester is the session's view: every question on the account.
	all, err := s.Interactions.List(ctx, ListInteractionsParams{UserID: user.ID, Now: now, Limit: 50})
	if err != nil {
		t.Fatalf("list account-wide: %v", err)
	}
	if len(all.Items) != 6 {
		t.Fatalf("account-wide list = %d items, want all 6", len(all.Items))
	}

	// Token A's filter is walked page by page, with a limit small enough to
	// force the cursor to carry the filter across requests.
	wantIDs := map[string]bool{a1.ID: true, a2.ID: true, a3.ID: true, answered.ID: true}
	var cursor Cursor
	pages := 0
	seen := 0
	for {
		page, err := s.Interactions.List(ctx, ListInteractionsParams{
			UserID: user.ID, RequesterTokenID: &tokA.ID, Now: now, Cursor: cursor, Limit: 2,
		})
		if err != nil {
			t.Fatalf("list for token A, page %d: %v", pages+1, err)
		}
		pages++
		for _, item := range page.Items {
			seen++
			if item.RequesterTokenID == nil || *item.RequesterTokenID != tokA.ID {
				t.Errorf("page %d leaked %s (requester token %v, service %v)",
					pages, item.ID, item.RequesterTokenID, item.RequesterServiceID)
				continue
			}
			if !wantIDs[item.ID] {
				t.Errorf("page %d repeated or invented %s", pages, item.ID)
			}
			delete(wantIDs, item.ID)
		}
		if !page.HasMore() {
			break
		}
		cursor = page.Next
	}
	if pages < 2 {
		t.Errorf("4 rows paged with limit 2 took %d page(s), want at least 2", pages)
	}
	if seen != 4 {
		t.Errorf("token A saw %d rows, want its 4", seen)
	}
	for id := range wantIDs {
		t.Errorf("token A's list is missing its own %s", id)
	}

	// The status filter composes with the requester filter.
	pending, err := s.Interactions.List(ctx, ListInteractionsParams{
		UserID: user.ID, RequesterTokenID: &tokA.ID, PendingOnly: true, Now: now, Limit: 50,
	})
	if err != nil {
		t.Fatalf("list pending for token A: %v", err)
	}
	if len(pending.Items) != 3 {
		t.Fatalf("token A's pending inbox = %d items, want 3", len(pending.Items))
	}
	for _, item := range pending.Items {
		if item.Status != InteractionPending {
			t.Errorf("pending list carries %s in status %q", item.ID, item.Status)
		}
	}

	// A token id alone is not enough: the wrong owner reads nothing, even with
	// a filter that matches real rows.
	stranger := mustUser(ctx, t, s, "mallory")
	if page, err := s.Interactions.List(ctx, ListInteractionsParams{
		UserID: stranger.ID, RequesterTokenID: &tokA.ID, Now: now, Limit: 50,
	}); err != nil || len(page.Items) != 0 {
		t.Fatalf("foreign owner = (%d items, %v), want none", len(page.Items), err)
	}
}

func TestCallbackClaimLeases(t *testing.T) {
	ctx, s := requireStore(t)
	user := mustUser(ctx, t, s, "ali")
	svc := mustService(ctx, t, s, user.ID, "Deploy bot")
	now := time.Now()

	answered, err := s.Interactions.Create(ctx, CreateInteractionParams{
		ID: id.New(), UserID: user.ID, RequesterServiceID: &svc.ID,
		Title: "t", Prompt: "p", Kind: InteractionApproval, Presentation: PresentationNotification,
		Choices: ChoicesFor(InteractionApproval), ActionDigest: "digest",
		CallbackURL: ptr("https://ci.example.com/hark"), CallbackTokenCiphertext: ptr("v1.a.b.c"),
		ExpiresAt: now.Add(time.Minute), Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}

	// A pending callback is not due until its question has been answered.
	claimed, err := s.Interactions.ClaimDueCallbacks(ctx, now, 20, time.Minute)
	if err != nil || len(claimed) != 0 {
		t.Fatalf("claim before the answer = (%d, %v), want none", len(claimed), err)
	}

	if _, err := s.Interactions.Respond(ctx, RespondParams{
		ID: answered.ID, UserID: user.ID, Status: InteractionApproved,
		Response: ptr("approve"), TriggerCallback: true, Now: now,
	}); err != nil {
		t.Fatal(err)
	}

	claimed, err = s.Interactions.ClaimDueCallbacks(ctx, now, 20, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = (%d, %v), want 1", len(claimed), err)
	}

	// The lease prevents a second worker from claiming the row while it is active:
	// until the first worker's attempt has had time to finish.
	again, err := s.Interactions.ClaimDueCallbacks(ctx, now, 20, time.Minute)
	if err != nil || len(again) != 0 {
		t.Fatalf("second claim inside the lease = (%d, %v), want none", len(again), err)
	}
	// Once the lease lapses — a crashed worker — the row comes back.
	again, err = s.Interactions.ClaimDueCallbacks(ctx, now.Add(2*time.Minute), 20, time.Minute)
	if err != nil || len(again) != 1 {
		t.Fatalf("claim after the lease = (%d, %v), want 1", len(again), err)
	}

	retry, err := s.Interactions.SettleCallback(ctx, SettleCallbackParams{
		ID: answered.ID, Attempts: 1, Status: CallbackRetrying,
		NextAttemptAt: ptr(now.Add(30 * time.Second)), LastError: ptr("HTTP 502"), Now: now,
	})
	if err != nil {
		t.Fatalf("settle retry: %v", err)
	}
	if *retry.CallbackStatus != CallbackRetrying || retry.CallbackAttempts != 1 {
		t.Errorf("retry = %+v", retry)
	}

	delivered, err := s.Interactions.SettleCallback(ctx, SettleCallbackParams{
		ID: answered.ID, Attempts: 2, Status: CallbackDelivered, Now: now,
	})
	if err != nil {
		t.Fatalf("settle delivered: %v", err)
	}
	if delivered.CallbackDeliveredAt == nil || delivered.CallbackNextAttemptAt != nil {
		t.Errorf("delivered = %+v, want a delivery stamp and no next attempt", delivered)
	}
	// A delivered callback is never claimed again.
	if claimed, err := s.Interactions.ClaimDueCallbacks(ctx, now.Add(time.Hour), 20, time.Minute); err != nil || len(claimed) != 0 {
		t.Fatalf("claim after delivery = (%d, %v), want none", len(claimed), err)
	}
}

func TestLiveActivityStartAndDeviceSlot(t *testing.T) {
	ctx, s := requireStore(t)
	user := mustUser(ctx, t, s, "ali")
	tok := mustToken(ctx, t, s, user.ID)
	device := mustDevice(ctx, t, s, user.ID, "aaaa")
	now := time.Now()

	start := func(key *string, purpose string) (*StartedActivity, error) {
		return s.Activities.Start(ctx, StartActivityParams{
			ID: id.New(), UserID: user.ID, RequesterTokenID: &tok.ID, Key: key,
			SchemaVersion: LiveActivitySchemaVersion, Props: props("Deploy", "Building"),
			ExpiresAt: now.Add(8 * time.Hour), StaleAt: ptr(now.Add(4 * time.Hour)),
			OperationID: id.New(),
			Targets: []ActivityTarget{{
				DeliveryID: id.New(), DeviceID: device.ID,
				Environment: EnvironmentSandbox, Purpose: purpose,
			}},
			Now: now,
		})
	}

	started, err := start(ptr("deploy"), PurposeTask)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if started.Activity.Status != ActivityStarting || started.Activity.Sequence != 0 {
		t.Errorf("activity = %+v", started.Activity)
	}
	if started.Activity.APNsTimestamp != Millis(now).Unix() {
		t.Errorf("APNsTimestamp = %d, want %d", started.Activity.APNsTimestamp, Millis(now).Unix())
	}
	if started.Operation.Event != OperationStart || started.Operation.Sequence != 0 {
		t.Errorf("operation = %+v", started.Operation)
	}
	if len(started.Deliveries) != 1 || started.Deliveries[0].Status != DeliveryPending {
		t.Errorf("deliveries = %+v", started.Deliveries)
	}
	// The delivery's diagnostic sequence starts below zero so that the first
	// real dispatch, at sequence 0, is distinguishable from "never attempted".
	if started.Deliveries[0].LastSequence != -1 {
		t.Errorf("LastSequence = %d, want -1", started.Deliveries[0].LastSequence)
	}

	// A second task activity cannot take a device that already hosts one.
	if _, err := start(ptr("other"), PurposeTask); !IsUniqueViolation(err, "live_activity_deliveries_one_task_per_device_key") {
		t.Fatalf("second task start error = %v, want the device-slot violation", err)
	}
	// A key already held by a live activity is refused as well.
	if _, err := start(ptr("deploy"), PurposeTask); !IsUniqueViolation(err) {
		t.Fatalf("duplicate key start error = %v, want a unique violation", err)
	}
	// But an interaction activity may sit alongside the task one: that is the
	// whole point of the purpose column being in the index predicate.
	interactive, err := start(nil, PurposeInteraction)
	if err != nil {
		t.Fatalf("interaction start alongside a task activity: %v", err)
	}

	occupancy, err := s.Deliveries.Occupancy(ctx, []string{device.ID}, now)
	if err != nil {
		t.Fatalf("occupancy: %v", err)
	}
	if len(occupancy) != 1 {
		t.Fatalf("occupancy = %d rows, want only the task delivery", len(occupancy))
	}
	if occupancy[0].ActivityID != started.Activity.ID || !occupancy[0].ActivityLive {
		t.Errorf("occupancy = %+v", occupancy[0])
	}
	_ = interactive

	// Once the blocking activity ends, its delivery stops occupying the slot
	// and the key frees up.
	ended, err := s.Activities.EndUnmetered(ctx, started.Activity.ID, started.Activity.Sequence, now, now)
	if err != nil {
		t.Fatalf("end: %v", err)
	}
	if ended.Status != ActivityEnded || ended.Sequence != 1 || ended.EndedAt == nil {
		t.Errorf("ended = %+v", ended)
	}
	if _, err := s.Deliveries.Release(ctx, []string{started.Deliveries[0].ID}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := start(ptr("deploy"), PurposeTask); err != nil {
		t.Fatalf("starting again after the slot was freed: %v", err)
	}
}

func TestLiveActivityMutationIsOptimistic(t *testing.T) {
	ctx, s := requireStore(t)
	user := mustUser(ctx, t, s, "ali")
	tok := mustToken(ctx, t, s, user.ID)
	device := mustDevice(ctx, t, s, user.ID, "aaaa")
	now := time.Now()

	started, err := s.Activities.Start(ctx, StartActivityParams{
		ID: id.New(), UserID: user.ID, RequesterTokenID: &tok.ID, Key: ptr("deploy"),
		SchemaVersion: LiveActivitySchemaVersion, Props: props("Deploy", "Building"),
		ExpiresAt: now.Add(time.Hour), OperationID: id.New(),
		Targets: []ActivityTarget{{
			DeliveryID: id.New(), DeviceID: device.ID,
			Environment: EnvironmentSandbox, Purpose: PurposeTask,
		}},
		Now: now,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	// A mutation from a stale read is refused rather than silently applied.
	if _, _, err := s.Activities.Mutate(ctx, MutateActivityParams{
		ActivityID: started.Activity.ID, ExpectedSequence: 7, Event: OperationUpdate,
		Props: props("Deploy", "Testing"), OperationID: id.New(),
		RequesterTokenID: &tok.ID, Now: now,
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale mutation error = %v, want ErrNotFound", err)
	}
	// And the refused mutation left no operation behind.
	if n, err := s.Operations.CountForTokenSince(ctx, tok.ID, now.Add(-time.Minute)); err != nil || n != 1 {
		t.Fatalf("operations after a refused mutation = (%d, %v), want just the start", n, err)
	}

	updated, op, err := s.Activities.Mutate(ctx, MutateActivityParams{
		ActivityID: started.Activity.ID, ExpectedSequence: 0, Event: OperationUpdate,
		Props: props("Deploy", "Testing"), StaleAt: Value(ptr(now.Add(2 * time.Hour))),
		OperationID: id.New(), RequesterTokenID: &tok.ID, Now: now,
	})
	if err != nil {
		t.Fatalf("mutate: %v", err)
	}
	if updated.Sequence != 1 || op.Sequence != 1 || op.Event != OperationUpdate {
		t.Errorf("updated = %+v, op = %+v", updated, op)
	}
	// Two mutations inside the same second must still produce a rising APNs
	// timestamp, or ActivityKit discards the second one.
	if updated.APNsTimestamp <= started.Activity.APNsTimestamp {
		t.Errorf("APNsTimestamp did not advance: %d -> %d", started.Activity.APNsTimestamp, updated.APNsTimestamp)
	}
	if updated.StaleAt == nil {
		t.Error("StaleAt was not written")
	}
	if updated.Status != ActivityStarting {
		t.Errorf("Status = %q, want an update to leave it alone", updated.Status)
	}

	// A settle from an earlier dispatch must not clobber fresher counts.
	if _, err := s.Activities.Settle(ctx, started.Activity.ID, 0, ActivityActive, 1, 0, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("stale settle error = %v, want ErrNotFound", err)
	}
	settled, err := s.Activities.Settle(ctx, started.Activity.ID, 1, ActivityPartial, 1, 1, now)
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if settled.Status != ActivityPartial || settled.AcceptedCount != 1 || settled.FailedCount != 1 {
		t.Errorf("settled = %+v", settled)
	}

	// Ending is a mutation like any other, but it moves the activity to a
	// terminal status and nothing may follow it.
	endedActivity, _, err := s.Activities.Mutate(ctx, MutateActivityParams{
		ActivityID: started.Activity.ID, ExpectedSequence: 1, Event: OperationEnd,
		Props: props("Deploy", "Complete"), DismissalAt: Value(ptr(now)),
		OperationID: id.New(), RequesterTokenID: &tok.ID, Now: now,
	})
	if err != nil {
		t.Fatalf("end: %v", err)
	}
	if endedActivity.Status != ActivityEnded || endedActivity.EndedAt == nil {
		t.Errorf("ended = %+v", endedActivity)
	}
	if _, _, err := s.Activities.Mutate(ctx, MutateActivityParams{
		ActivityID: started.Activity.ID, ExpectedSequence: 2, Event: OperationUpdate,
		Props: props("Deploy", "Zombie"), OperationID: id.New(), RequesterTokenID: &tok.ID, Now: now,
	}); !errors.Is(err, ErrNotFound) {
		t.Errorf("mutating a terminal activity error = %v, want ErrNotFound", err)
	}
}

func TestActivityResolveByKeyPrefersLiveThenNewest(t *testing.T) {
	ctx, s := requireStore(t)
	user := mustUser(ctx, t, s, "ali")
	tok := mustToken(ctx, t, s, user.ID)
	now := time.Now()

	start := func(at time.Time) *LiveActivity {
		t.Helper()
		started, err := s.Activities.Start(ctx, StartActivityParams{
			ID: id.New(), UserID: user.ID, RequesterTokenID: &tok.ID, Key: ptr("deploy"),
			SchemaVersion: LiveActivitySchemaVersion, Props: props("Deploy", "Building"),
			ExpiresAt: at.Add(time.Hour), OperationID: id.New(), Now: at,
		})
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		return &started.Activity
	}

	// Two finished runs and one live one, all sharing a key — which is legal
	// precisely because the uniqueness index only covers live rows.
	older := start(now.Add(-2 * time.Hour))
	if _, err := s.Activities.EndUnmetered(ctx, older.ID, older.Sequence, now, now); err != nil {
		t.Fatal(err)
	}
	newerTerminal := start(now.Add(-time.Hour))
	if _, err := s.Activities.EndUnmetered(ctx, newerTerminal.ID, newerTerminal.Sequence, now, now); err != nil {
		t.Fatal(err)
	}
	live := start(now)

	got, err := s.Activities.Resolve(ctx, ResolveParams{Identifier: "deploy", RequesterTokenID: &tok.ID})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.ID != live.ID {
		t.Errorf("resolved %s, want the live activity %s", got.ID, live.ID)
	}

	// With the live one finished, the most recent terminal row answers.
	if _, err := s.Activities.EndUnmetered(ctx, live.ID, live.Sequence, now, now); err != nil {
		t.Fatal(err)
	}
	got, err = s.Activities.Resolve(ctx, ResolveParams{Identifier: "deploy", RequesterTokenID: &tok.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != live.ID {
		t.Errorf("resolved %s, want the newest activity %s", got.ID, live.ID)
	}

	// By id, and never across requesters.
	if got, err := s.Activities.Resolve(ctx, ResolveParams{Identifier: older.ID, RequesterTokenID: &tok.ID}); err != nil || got.ID != older.ID {
		t.Errorf("resolve by id = (%v, %v)", got, err)
	}
	stranger := mustToken(ctx, t, s, user.ID)
	if _, err := s.Activities.Resolve(ctx, ResolveParams{Identifier: "deploy", RequesterTokenID: &stranger.ID}); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-requester resolve error = %v, want ErrNotFound", err)
	}

	// The key is free again now that nothing live holds it.
	if _, err := s.Activities.KeyHolder(ctx, &tok.ID, nil, "deploy"); !errors.Is(err, ErrNotFound) {
		t.Errorf("KeyHolder error = %v, want ErrNotFound once every activity ended", err)
	}
}

func TestActivityLazyExpiryFreesTheKey(t *testing.T) {
	ctx, s := requireStore(t)
	user := mustUser(ctx, t, s, "ali")
	tok := mustToken(ctx, t, s, user.ID)
	now := time.Now()

	started, err := s.Activities.Start(ctx, StartActivityParams{
		ID: id.New(), UserID: user.ID, RequesterTokenID: &tok.ID, Key: ptr("deploy"),
		SchemaVersion: LiveActivitySchemaVersion, Props: props("Deploy", "Building"),
		ExpiresAt: now.Add(-time.Second), OperationID: id.New(), Now: now,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	expired, err := s.Activities.ExpireIfDue(ctx, started.Activity.ID, now)
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	if expired.Status != ActivityExpired || expired.EndedAt == nil {
		t.Errorf("expired = %+v", expired)
	}
	if _, err := s.Activities.ExpireIfDue(ctx, started.Activity.ID, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("second expiry error = %v, want ErrNotFound", err)
	}

	// Expiring frees the partial unique index, so the key can be reused.
	if _, err := s.Activities.Start(ctx, StartActivityParams{
		ID: id.New(), UserID: user.ID, RequesterTokenID: &tok.ID, Key: ptr("deploy"),
		SchemaVersion: LiveActivitySchemaVersion, Props: props("Deploy", "Building"),
		ExpiresAt: now.Add(time.Hour), OperationID: id.New(), Now: now,
	}); err != nil {
		t.Fatalf("reusing the key of an expired activity: %v", err)
	}
}

func TestDeliveryAttemptAndTokenAssociation(t *testing.T) {
	ctx, s := requireStore(t)
	user := mustUser(ctx, t, s, "ali")
	tok := mustToken(ctx, t, s, user.ID)
	device := mustDevice(ctx, t, s, user.ID, "aaaa")
	now := time.Now()

	started, err := s.Activities.Start(ctx, StartActivityParams{
		ID: id.New(), UserID: user.ID, RequesterTokenID: &tok.ID,
		SchemaVersion: LiveActivitySchemaVersion, Props: props("Deploy", "Building"),
		ExpiresAt: now.Add(time.Hour), OperationID: id.New(),
		Targets: []ActivityTarget{{
			DeliveryID: id.New(), DeviceID: device.ID,
			Environment: EnvironmentSandbox, Purpose: PurposeTask,
		}},
		Now: now,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	delivery := started.Deliveries[0]

	accepted, err := s.Deliveries.RecordAttempt(ctx, RecordAttemptParams{
		AttemptID: id.New(), ActivityID: started.Activity.ID, DeliveryID: delivery.ID,
		DeviceID: device.ID, OperationID: started.Operation.ID, RequesterTokenID: &tok.ID,
		Event: OperationStart, Sequence: 0, Accepted: true, APNsStatus: ptr(200),
		APNsID: ptr("apns-id"), Now: now,
	})
	if err != nil {
		t.Fatalf("record accepted attempt: %v", err)
	}
	if accepted.Status != DeliveryAccepted || accepted.LastSequence != 0 {
		t.Errorf("delivery = %+v", accepted)
	}
	if accepted.LastAPNsStatus == nil || *accepted.LastAPNsStatus != 200 {
		t.Errorf("LastAPNsStatus = %v", accepted.LastAPNsStatus)
	}

	// The phone comes back with ActivityKit's identifier and an update token.
	candidates, err := s.Deliveries.AssociationCandidates(ctx, AssociationParams{
		DeviceID: device.ID, UserID: user.ID, SchemaVersion: LiveActivitySchemaVersion,
		NativeActivityID: ptr("native-1"), Limit: 2,
	})
	if err != nil {
		t.Fatalf("association candidates: %v", err)
	}
	if len(candidates) != 1 || candidates[0].ID != delivery.ID {
		t.Fatalf("candidates = %+v, want the one unassociated delivery", candidates)
	}

	associated, err := s.Deliveries.SetUpdateToken(ctx, SetUpdateTokenParams{
		DeliveryID: delivery.ID, NativeActivityID: ptr("native-1"), Ciphertext: "v1.a.b.c",
		Environment: Value(EnvironmentProduction), SchemaVersion: Value(LiveActivitySchemaVersion),
		Now: now,
	})
	if err != nil {
		t.Fatalf("set update token: %v", err)
	}
	if associated.Status != DeliveryActive || !associated.Updatable() ||
		associated.Environment != EnvironmentProduction {
		t.Errorf("associated = %+v", associated)
	}

	// A second registration finds the same ActivityKit id.
	exact, err := s.Deliveries.AssociationCandidates(ctx, AssociationParams{
		DeviceID: device.ID, UserID: user.ID, SchemaVersion: LiveActivitySchemaVersion,
		NativeActivityID: ptr("native-1"), Limit: 2,
	})
	if err != nil || len(exact) != 1 || exact[0].ID != delivery.ID {
		t.Fatalf("exact match = (%+v, %v)", exact, err)
	}

	// A registration that arrives with no ActivityKit id has nothing left to
	// attach to, because this delivery already has a token.
	none, err := s.Deliveries.AssociationCandidates(ctx, AssociationParams{
		DeviceID: device.ID, UserID: user.ID, SchemaVersion: LiveActivitySchemaVersion, Limit: 2,
	})
	if err != nil || len(none) != 0 {
		t.Fatalf("unassociated candidates = (%+v, %v), want none", none, err)
	}

	// An update APNs refuses leaves the delivery where it was: the next update
	// may well land.
	unchanged, err := s.Deliveries.RecordAttempt(ctx, RecordAttemptParams{
		AttemptID: id.New(), ActivityID: started.Activity.ID, DeliveryID: delivery.ID,
		DeviceID: device.ID, OperationID: started.Operation.ID, RequesterTokenID: &tok.ID,
		Event: OperationUpdate, Sequence: 1, Accepted: false, APNsStatus: ptr(503),
		APNsReason: ptr("ServiceUnavailable"), Now: now,
	})
	if err != nil {
		t.Fatalf("record failed update: %v", err)
	}
	if unchanged.Status != DeliveryActive {
		t.Errorf("Status = %q, want a refused update to leave it alone", unchanged.Status)
	}
	if unchanged.EndedAt != nil {
		t.Error("a non-end attempt must not stamp ended_at")
	}

	// A start APNs refuses with a dead token is conclusive: the delivery fails
	// and the device's push-to-start credential goes with it.
	dead, err := s.Deliveries.RecordAttempt(ctx, RecordAttemptParams{
		AttemptID: id.New(), ActivityID: started.Activity.ID, DeliveryID: delivery.ID,
		DeviceID: device.ID, OperationID: started.Operation.ID, RequesterTokenID: &tok.ID,
		Event: OperationStart, Sequence: 2, Accepted: false, APNsStatus: ptr(410),
		APNsReason: ptr("Unregistered"), TokenInvalid: true, Now: now,
	})
	if err != nil {
		t.Fatalf("record dead-token attempt: %v", err)
	}
	if dead.Status != DeliveryFailed || dead.UpdateTokenCiphertext != nil {
		t.Errorf("dead = %+v", dead)
	}
	refreshed, err := s.Devices.ByID(ctx, device.ID, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.LiveActivityCapable() {
		t.Error("the device kept a push-to-start token APNs has disowned")
	}

	// A synthetic failure records no HTTP status at all.
	if _, err := s.Deliveries.RecordAttempt(ctx, RecordAttemptParams{
		AttemptID: id.New(), ActivityID: started.Activity.ID, DeliveryID: delivery.ID,
		DeviceID: device.ID, OperationID: started.Operation.ID, RequesterTokenID: &tok.ID,
		Event: OperationUpdate, Sequence: 3, APNsStatus: ptr(0),
		APNsReason: ptr(ReasonProviderNotConfigured), Now: now,
	}); err != nil {
		t.Fatalf("record synthetic failure: %v", err)
	}
	attempts, err := s.Attempts.ListForActivity(ctx, started.Activity.ID, 10)
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(attempts) != 4 {
		t.Fatalf("recorded %d attempts, want 4", len(attempts))
	}
	if attempts[0].APNsStatus != nil {
		t.Errorf("a synthetic failure stored status %v, want NULL", *attempts[0].APNsStatus)
	}

}

// TestAttemptPruningCutoffBoundary pins the retention semantics the daily
// pruner relies on: DeleteBefore is strictly exclusive, so a row created
// exactly at the cutoff survives, and repeating the same delete — as two
// replicas running the pruner would — finds nothing left to remove.
func TestAttemptPruningCutoffBoundary(t *testing.T) {
	ctx, s := requireStore(t)
	user := mustUser(ctx, t, s, "ali")
	tok := mustToken(ctx, t, s, user.ID)
	device := mustDevice(ctx, t, s, user.ID, "aaaa")
	now := Millis(time.Now())

	started, err := s.Activities.Start(ctx, StartActivityParams{
		ID: id.New(), UserID: user.ID, RequesterTokenID: &tok.ID,
		SchemaVersion: LiveActivitySchemaVersion, Props: props("Deploy", "Building"),
		ExpiresAt: now.Add(time.Hour), OperationID: id.New(),
		Targets: []ActivityTarget{{
			DeliveryID: id.New(), DeviceID: device.ID,
			Environment: EnvironmentSandbox, Purpose: PurposeTask,
		}},
		Now: now,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	delivery := started.Deliveries[0]

	cutoff := now.Add(-24 * time.Hour)
	record := func(name string, at time.Time, seq int) {
		t.Helper()
		if _, err := s.Attempts.Insert(ctx, CreateAttemptParams{
			ID: id.New(), ActivityID: started.Activity.ID, DeliveryID: delivery.ID,
			OperationID: started.Operation.ID, RequesterTokenID: &tok.ID,
			Event: OperationUpdate, Sequence: seq, APNsStatus: ptr(0),
			APNsReason: ptr(ReasonProviderNotConfigured), Now: at,
		}); err != nil {
			t.Fatalf("insert the %s attempt: %v", name, err)
		}
	}
	record("older", cutoff.Add(-time.Millisecond), 0)
	record("boundary", cutoff, 1)
	record("newer", cutoff.Add(time.Millisecond), 2)

	if n, err := s.Attempts.DeleteBefore(ctx, cutoff); err != nil || n != 1 {
		t.Fatalf("DeleteBefore = (%d, %v), want (1, nil): only the strictly older row goes", n, err)
	}

	remaining, err := s.Attempts.ListForActivity(ctx, started.Activity.ID, 10)
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(remaining) != 2 {
		t.Fatalf("%d attempts remain, want the boundary and newer rows", len(remaining))
	}
	// Newest first: the row after the cutoff, then the row exactly at it.
	if remaining[0].Sequence != 2 || remaining[1].Sequence != 1 {
		t.Errorf("remaining sequences = (%d, %d), want (2, 1)",
			remaining[0].Sequence, remaining[1].Sequence)
	}
	if !remaining[1].CreatedAt.Equal(cutoff) {
		t.Errorf("the row at the cutoff was touched: created_at = %s, want %s",
			remaining[1].CreatedAt, cutoff)
	}

	// The identical delete again removes nothing: pruning is idempotent, so
	// concurrent replicas repeating a cutoff are safe.
	if n, err := s.Attempts.DeleteBefore(ctx, cutoff); err != nil || n != 0 {
		t.Fatalf("repeat DeleteBefore = (%d, %v), want (0, nil)", n, err)
	}
}

func TestDeliveryRegistrationWindow(t *testing.T) {
	ctx, s := requireStore(t)
	user := mustUser(ctx, t, s, "ali")
	tok := mustToken(ctx, t, s, user.ID)
	device := mustDevice(ctx, t, s, user.ID, "aaaa")
	now := time.Now()

	started, err := s.Activities.Start(ctx, StartActivityParams{
		ID: id.New(), UserID: user.ID, RequesterTokenID: &tok.ID,
		SchemaVersion: LiveActivitySchemaVersion, Props: props("Deploy", "Building"),
		ExpiresAt: now.Add(time.Hour), OperationID: id.New(),
		Targets: []ActivityTarget{{
			DeliveryID: id.New(), DeviceID: device.ID,
			Environment: EnvironmentSandbox, Purpose: PurposeTask,
		}},
		Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}

	reg, err := s.Deliveries.ForRegistration(ctx, started.Deliveries[0].ID, now)
	if err != nil {
		t.Fatalf("ForRegistration: %v", err)
	}
	if !reg.ActivityExpiresAt.Equal(started.Activity.ExpiresAt) {
		t.Errorf("ActivityExpiresAt = %s, want %s", reg.ActivityExpiresAt, started.Activity.ExpiresAt)
	}

	// The registration window closes with the activity: a token arriving after
	// it ended has nothing to attach to.
	if _, err := s.Activities.EndUnmetered(ctx, started.Activity.ID, 0, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Deliveries.ForRegistration(ctx, started.Deliveries[0].ID, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("ForRegistration after the activity ended = %v, want ErrNotFound", err)
	}
}

func TestFeedUnionAndDelete(t *testing.T) {
	ctx, s := requireStore(t)
	user := mustUser(ctx, t, s, "ali")
	svc := mustService(ctx, t, s, user.ID, "Deploy bot")
	tok := mustToken(ctx, t, s, user.ID)
	device := mustDevice(ctx, t, s, user.ID, "aaaa")
	base := time.Now().Add(-time.Hour)

	event, err := s.Events.Create(ctx, CreateEventParams{
		ID: id.New(), ServiceID: svc.ID, Title: "Deploy bot", Body: "Build 4821 succeeded",
		URL: ptr("https://example.com/builds/4821"), Priority: PriorityNormal,
		Status: EventAccepted, Now: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Events.Settle(ctx, event.ID, EventAccepted, 1, nil); err != nil {
		t.Fatal(err)
	}

	notification, err := s.Notifications.Create(ctx, CreateNotificationParams{
		ID: id.New(), UserID: user.ID, RequesterTokenID: tok.ID, Title: "Hark",
		Body: "Agent finished", Priority: PriorityNormal, Now: base.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Notifications.Settle(ctx, notification.ID, EventAccepted, 1); err != nil {
		t.Fatal(err)
	}

	interaction, err := s.Interactions.Create(ctx, CreateInteractionParams{
		ID: id.New(), UserID: user.ID, RequesterServiceID: &svc.ID, Title: "Deploy bot",
		Prompt: "Deploy to production?", Kind: InteractionApproval,
		Presentation: PresentationNotification, Choices: ChoicesFor(InteractionApproval),
		ActionDigest: "digest", ExpiresAt: base.Add(time.Hour), Now: base.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Interactions.Respond(ctx, RespondParams{
		ID: interaction.ID, UserID: user.ID, Status: InteractionApproved,
		Response: ptr("approve"), DeviceID: &device.ID, Now: base.Add(3 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	started, err := s.Activities.Start(ctx, StartActivityParams{
		ID: id.New(), UserID: user.ID, RequesterTokenID: &tok.ID,
		SchemaVersion: LiveActivitySchemaVersion, Props: props("Deploy", "Building"),
		ExpiresAt: base.Add(time.Hour), OperationID: id.New(), Now: base.Add(4 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}

	all, err := s.Feed.List(ctx, user.ID, FeedFilterAll, Cursor{}, 50)
	if err != nil {
		t.Fatalf("list feed: %v", err)
	}
	if len(all.Items) != 4 {
		t.Fatalf("feed has %d items, want one from each source: %+v", len(all.Items), all.Items)
	}
	// Newest first, and the answered question is placed by when it was
	// answered rather than when it was asked.
	wantOrder := []string{
		FeedSourceLiveActivity + ":" + started.Operation.ID,
		FeedSourceResponse + ":" + interaction.ID,
		FeedSourceNotification + ":" + notification.ID,
		FeedSourceEvent + ":" + event.ID,
	}
	for i, want := range wantOrder {
		if all.Items[i].ID != want {
			t.Errorf("item %d = %q, want %q", i, all.Items[i].ID, want)
		}
	}

	byID := map[string]FeedItem{}
	for _, item := range all.Items {
		byID[item.ID] = item
	}
	if got := byID[FeedSourceEvent+":"+event.ID]; got.Kind != FeedKindNotification ||
		got.SourceName != "Deploy bot" || got.DeliveredCount == nil || *got.DeliveredCount != 1 {
		t.Errorf("event item = %+v", got)
	}
	if got := byID[FeedSourceNotification+":"+notification.ID]; got.SourceName != "harkctl" ||
		got.Status == nil || *got.Status != EventAccepted {
		t.Errorf("notification item = %+v", got)
	}
	if got := byID[FeedSourceResponse+":"+interaction.ID]; got.Kind != FeedKindResponse ||
		got.Result == nil || *got.Result != InteractionApproved || got.Status != nil {
		t.Errorf("response item = %+v", got)
	}
	if got := byID[FeedSourceLiveActivity+":"+started.Operation.ID]; got.Kind != FeedKindLiveActivity ||
		got.Title != "Deploy" || got.Result == nil || *got.Result != OperationStart {
		t.Errorf("live activity item = %+v", got)
	}

	// Filters narrow the union without disturbing the ordering.
	notifications, err := s.Feed.List(ctx, user.ID, FeedFilterNotification, Cursor{}, 50)
	if err != nil || len(notifications.Items) != 2 {
		t.Fatalf("notification filter = (%d items, %v), want 2", len(notifications.Items), err)
	}
	responses, err := s.Feed.List(ctx, user.ID, FeedFilterResponse, Cursor{}, 50)
	if err != nil || len(responses.Items) != 1 {
		t.Fatalf("response filter = (%d items, %v), want 1", len(responses.Items), err)
	}
	if _, err := s.Feed.List(ctx, user.ID, "bogus", Cursor{}, 50); err == nil {
		t.Error("an unknown filter should be refused")
	}

	// Paging across the union covers every row exactly once.
	seen := map[string]bool{}
	cursor := Cursor{}
	for range 4 {
		page, err := s.Feed.List(ctx, user.ID, FeedFilterAll, cursor, 1)
		if err != nil {
			t.Fatalf("paged list: %v", err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("page returned %d items, want 1", len(page.Items))
		}
		if seen[page.Items[0].ID] {
			t.Errorf("%s appeared twice while paging", page.Items[0].ID)
		}
		seen[page.Items[0].ID] = true
		cursor = page.Next
	}
	if len(seen) != 4 {
		t.Errorf("paging visited %d of 4 items", len(seen))
	}

	// Each source deletes from its own table, and only for its owner.
	stranger := mustUser(ctx, t, s, "sam")
	if deleted, err := s.Feed.Delete(ctx, stranger.ID, FeedSourceEvent+":"+event.ID); err != nil || deleted {
		t.Errorf("cross-account delete = (%v, %v), want (false, nil)", deleted, err)
	}
	for _, feedID := range wantOrder {
		deleted, err := s.Feed.Delete(ctx, user.ID, feedID)
		if err != nil || !deleted {
			t.Errorf("Delete(%q) = (%v, %v)", feedID, deleted, err)
		}
	}
	if remaining, err := s.Feed.List(ctx, user.ID, FeedFilterAll, Cursor{}, 50); err != nil || len(remaining.Items) != 0 {
		t.Fatalf("feed after deletes = (%d items, %v), want none", len(remaining.Items), err)
	}
	if ok, err := s.Feed.Delete(ctx, user.ID, "nonsense"); err != nil || ok {
		t.Errorf("Delete on a malformed id = (%v, %v), want (false, nil)", ok, err)
	}
}

func TestPendingInteractionCannotBeDeletedFromTheFeed(t *testing.T) {
	ctx, s := requireStore(t)
	user := mustUser(ctx, t, s, "ali")
	tok := mustToken(ctx, t, s, user.ID)
	now := time.Now()

	pending, err := s.Interactions.Create(ctx, CreateInteractionParams{
		ID: id.New(), UserID: user.ID, RequesterTokenID: &tok.ID, Title: "t", Prompt: "p",
		Kind: InteractionApproval, Presentation: PresentationNotification,
		Choices: ChoicesFor(InteractionApproval), ActionDigest: "digest",
		ExpiresAt: now.Add(time.Minute), Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	// A live question must not be made to vanish from the phone by deleting a
	// history row.
	if deleted, err := s.Feed.Delete(ctx, user.ID, FeedSourceResponse+":"+pending.ID); err != nil || deleted {
		t.Errorf("deleting a pending interaction = (%v, %v), want (false, nil)", deleted, err)
	}
	if _, err := s.Interactions.ByID(ctx, pending.ID); err != nil {
		t.Errorf("the pending interaction was deleted: %v", err)
	}
}

type filteredFeed struct {
	deployEvent  string
	uptimeEvent  string
	notification string
	response     string
	operation    string
	critical     string

	uptime *Service
	token  *APIToken
}

func seedFilteredFeed(ctx context.Context, t *testing.T, s *Store, userID string) filteredFeed {
	t.Helper()
	deploy := mustService(ctx, t, s, userID, "Deploy bot")
	uptime := mustService(ctx, t, s, userID, "Uptime")
	tok := mustToken(ctx, t, s, userID)
	criticalSvc := mustCriticalService(ctx, t, s, userID, "Bedroom")
	device := mustDevice(ctx, t, s, userID, "aaaa")
	base := time.Now().Add(-time.Hour)

	deployEvent, err := s.Events.Create(ctx, CreateEventParams{
		ID: id.New(), ServiceID: deploy.ID, Title: "Deploy bot", Body: "Build 4821 succeeded",
		Priority: PriorityNormal, Status: EventAccepted, Now: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	uptimeEvent, err := s.Events.Create(ctx, CreateEventParams{
		ID: id.New(), ServiceID: uptime.ID, Title: "Uptime", Body: "hark.example.com is down",
		Priority: PriorityTimeSensitive, Status: EventAccepted, Now: base.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	notification, err := s.Notifications.Create(ctx, CreateNotificationParams{
		ID: id.New(), UserID: userID, RequesterTokenID: tok.ID, Title: "Hark",
		Body: "Agent finished", Priority: PriorityNormal, Now: base.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	interaction, err := s.Interactions.Create(ctx, CreateInteractionParams{
		ID: id.New(), UserID: userID, RequesterServiceID: &deploy.ID, Title: "Deploy bot",
		Prompt: "Deploy to production?", Kind: InteractionApproval,
		Presentation: PresentationNotification, Choices: ChoicesFor(InteractionApproval),
		ActionDigest: "digest", ExpiresAt: base.Add(time.Hour), Now: base.Add(3 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Interactions.Respond(ctx, RespondParams{
		ID: interaction.ID, UserID: userID, Status: InteractionApproved,
		Response: ptr("approve"), DeviceID: &device.ID, Now: base.Add(4 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	started, err := s.Activities.Start(ctx, StartActivityParams{
		ID: id.New(), UserID: userID, RequesterTokenID: &tok.ID,
		SchemaVersion: LiveActivitySchemaVersion, Props: props("Deploy", "Building"),
		ExpiresAt: base.Add(time.Hour), OperationID: id.New(), Now: base.Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	critical, err := s.Events.Create(ctx, CreateEventParams{
		ID: id.New(), ServiceID: criticalSvc.ID, Title: "Bedroom", Body: "Attention needed.",
		Priority: PriorityCritical, Status: EventAccepted, Now: base.Add(6 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}

	return filteredFeed{
		deployEvent:  FeedSourceEvent + ":" + deployEvent.ID,
		uptimeEvent:  FeedSourceEvent + ":" + uptimeEvent.ID,
		notification: FeedSourceNotification + ":" + notification.ID,
		response:     FeedSourceResponse + ":" + interaction.ID,
		operation:    FeedSourceLiveActivity + ":" + started.Operation.ID,
		critical:     FeedSourceEvent + ":" + critical.ID,
		uptime:       uptime,
		token:        tok,
	}
}

func TestFeedSourceAndPriorityFilters(t *testing.T) {
	ctx, s := requireStore(t)
	user := mustUser(ctx, t, s, "ali")
	feed := seedFilteredFeed(ctx, t, s, user.ID)

	list := func(f FeedFilters) []string {
		t.Helper()
		page, err := s.Feed.ListFiltered(ctx, user.ID, f, Cursor{}, 50)
		if err != nil {
			t.Fatalf("ListFiltered(%+v): %v", f, err)
		}
		ids := make([]string, 0, len(page.Items))
		for _, item := range page.Items {
			ids = append(ids, item.ID)
		}
		return ids
	}

	cases := []struct {
		name    string
		filters FeedFilters
		want    []string
	}{
		{"no filters", FeedFilters{}, []string{
			feed.critical, feed.operation, feed.response,
			feed.notification, feed.uptimeEvent, feed.deployEvent,
		}},
		{"source", FeedFilters{Source: "Deploy bot"}, []string{feed.response, feed.deployEvent}},
		{"source and kind", FeedFilters{Kind: FeedFilterNotification, Source: "Deploy bot"},
			[]string{feed.deployEvent}},
		{"token source", FeedFilters{Source: "harkctl"}, []string{feed.operation, feed.notification}},
		{"critical service", FeedFilters{Source: "Bedroom"}, []string{feed.critical}},
		{"source match is exact", FeedFilters{Source: "deploy bot"}, []string{}},
		{"unknown source", FeedFilters{Source: "Nobody"}, []string{}},
		{"priority normal", FeedFilters{Priority: PriorityNormal},
			[]string{feed.notification, feed.deployEvent}},
		{"priority time_sensitive", FeedFilters{Priority: PriorityTimeSensitive},
			[]string{feed.uptimeEvent}},
		{"priority critical", FeedFilters{Priority: PriorityCritical}, []string{feed.critical}},
		{"source and priority", FeedFilters{Source: "harkctl", Priority: PriorityNormal},
			[]string{feed.notification}},
		{"kind and priority", FeedFilters{Kind: FeedFilterResponse, Priority: PriorityNormal},
			[]string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := list(tc.filters); !slices.Equal(got, tc.want) {
				t.Errorf("ListFiltered(%+v) = %v, want %v", tc.filters, got, tc.want)
			}
		})
	}

	if _, err := s.Feed.ListFiltered(ctx, user.ID, FeedFilters{Priority: "bogus"}, Cursor{}, 50); err == nil {
		t.Error("an unknown priority should be refused")
	}
	if _, err := s.Feed.ListFiltered(ctx, user.ID, FeedFilters{Kind: "bogus"}, Cursor{}, 50); err == nil {
		t.Error("an unknown kind should be refused")
	}
}

func TestFeedSources(t *testing.T) {
	ctx, s := requireStore(t)
	user := mustUser(ctx, t, s, "ali")
	deploy := mustService(ctx, t, s, user.ID, "deploy bot")
	uptime := mustService(ctx, t, s, user.ID, "Uptime")
	mustService(ctx, t, s, user.ID, "Idle")
	zed := mustService(ctx, t, s, user.ID, "Zed")
	tok := mustToken(ctx, t, s, user.ID)
	criticalSvc := mustCriticalService(ctx, t, s, user.ID, "Bedroom")
	now := time.Now()

	for i := range 2 {
		if _, err := s.Events.Create(ctx, CreateEventParams{
			ID: id.New(), ServiceID: deploy.ID, Title: "deploy bot", Body: "hello",
			Priority: PriorityNormal, Status: EventAccepted, Now: now.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Events.Create(ctx, CreateEventParams{
		ID: id.New(), ServiceID: uptime.ID, Title: "Uptime", Body: "down",
		Priority: PriorityNormal, Status: EventAccepted, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Notifications.Create(ctx, CreateNotificationParams{
		ID: id.New(), UserID: user.ID, RequesterTokenID: tok.ID, Title: "Hark",
		Body: "Agent finished", Priority: PriorityNormal, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Events.Create(ctx, CreateEventParams{
		ID: id.New(), ServiceID: criticalSvc.ID, Title: "Bedroom", Body: "Attention needed.",
		Priority: PriorityCritical, Status: EventAccepted, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Interactions.Create(ctx, CreateInteractionParams{
		ID: id.New(), UserID: user.ID, RequesterServiceID: &zed.ID, Title: "Zed",
		Prompt: "p", Kind: InteractionApproval, Presentation: PresentationNotification,
		Choices: ChoicesFor(InteractionApproval), ActionDigest: "digest",
		ExpiresAt: now.Add(time.Hour), Now: now,
	}); err != nil {
		t.Fatal(err)
	}

	stranger := mustUser(ctx, t, s, "sam")
	strangerSvc := mustService(ctx, t, s, stranger.ID, "Stranger bot")
	if _, err := s.Events.Create(ctx, CreateEventParams{
		ID: id.New(), ServiceID: strangerSvc.ID, Title: "Stranger bot", Body: "hi",
		Priority: PriorityNormal, Status: EventAccepted, Now: now,
	}); err != nil {
		t.Fatal(err)
	}

	sources, err := s.Feed.Sources(ctx, user.ID)
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	if want := []string{"Bedroom", "deploy bot", "harkctl", "Uptime"}; !slices.Equal(sources, want) {
		t.Errorf("Sources = %v, want %v", sources, want)
	}

	strangerSources, err := s.Feed.Sources(ctx, stranger.ID)
	if err != nil {
		t.Fatalf("Sources for the stranger: %v", err)
	}
	if want := []string{"Stranger bot"}; !slices.Equal(strangerSources, want) {
		t.Errorf("stranger Sources = %v, want %v", strangerSources, want)
	}

	empty := mustUser(ctx, t, s, "kim")
	if sources, err := s.Feed.Sources(ctx, empty.ID); err != nil || len(sources) != 0 {
		t.Errorf("Sources for an empty history = (%v, %v), want none", sources, err)
	}
}

func TestFeedDeleteAll(t *testing.T) {
	ctx, s := requireStore(t)
	user := mustUser(ctx, t, s, "ali")
	feed := seedFilteredFeed(ctx, t, s, user.ID)
	base := time.Now().Add(-time.Minute)

	askedEvent, err := s.Events.Create(ctx, CreateEventParams{
		ID: id.New(), ServiceID: feed.uptime.ID, Title: "Uptime", Body: "Restart the probe?",
		Priority: PriorityNormal, Status: EventAccepted, Now: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	askedPending, err := s.Interactions.Create(ctx, CreateInteractionParams{
		ID: id.New(), UserID: user.ID, RequesterServiceID: &feed.uptime.ID, EventID: &askedEvent.ID,
		Title: "Uptime", Prompt: "Restart the probe?", Kind: InteractionApproval,
		Presentation: PresentationNotification, Choices: ChoicesFor(InteractionApproval),
		ActionDigest: "digest", ExpiresAt: base.Add(time.Hour), Now: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	freePending, err := s.Interactions.Create(ctx, CreateInteractionParams{
		ID: id.New(), UserID: user.ID, RequesterTokenID: &feed.token.ID, Title: "t", Prompt: "p",
		Kind: InteractionApproval, Presentation: PresentationNotification,
		Choices: ChoicesFor(InteractionApproval), ActionDigest: "digest",
		ExpiresAt: base.Add(time.Hour), Now: base,
	})
	if err != nil {
		t.Fatal(err)
	}

	stranger := mustUser(ctx, t, s, "sam")
	strangerSvc := mustService(ctx, t, s, stranger.ID, "Stranger bot")
	if _, err := s.Events.Create(ctx, CreateEventParams{
		ID: id.New(), ServiceID: strangerSvc.ID, Title: "Stranger bot", Body: "hi",
		Priority: PriorityNormal, Status: EventAccepted, Now: base,
	}); err != nil {
		t.Fatal(err)
	}

	remaining := func() []string {
		t.Helper()
		page, err := s.Feed.ListFiltered(ctx, user.ID, FeedFilters{}, Cursor{}, 50)
		if err != nil {
			t.Fatalf("list feed: %v", err)
		}
		ids := make([]string, 0, len(page.Items))
		for _, item := range page.Items {
			ids = append(ids, item.ID)
		}
		slices.Sort(ids)
		return ids
	}
	sorted := func(ids ...string) []string {
		slices.Sort(ids)
		return ids
	}
	askedFeedID := FeedSourceEvent + ":" + askedEvent.ID

	if err := s.Feed.DeleteAll(ctx, user.ID, FeedFilters{Priority: "bogus"}); err == nil {
		t.Error("an unknown priority should be refused")
	}
	if err := s.Feed.DeleteAll(ctx, user.ID, FeedFilters{Kind: "bogus"}); err == nil {
		t.Error("an unknown kind should be refused")
	}
	if got := remaining(); len(got) != 7 {
		t.Fatalf("feed after refused deletes has %d items, want 7: %v", len(got), got)
	}

	if err := s.Feed.DeleteAll(ctx, user.ID, FeedFilters{Kind: FeedFilterResponse}); err != nil {
		t.Fatalf("DeleteAll(response): %v", err)
	}
	if got, want := remaining(), sorted(feed.deployEvent, feed.uptimeEvent, askedFeedID,
		feed.notification, feed.operation, feed.critical); !slices.Equal(got, want) {
		t.Fatalf("after deleting responses = %v, want %v", got, want)
	}
	if err := s.Feed.DeleteAll(ctx, user.ID, FeedFilters{Source: "Deploy bot"}); err != nil {
		t.Fatalf("DeleteAll(Deploy bot): %v", err)
	}
	if got, want := remaining(), sorted(feed.uptimeEvent, askedFeedID,
		feed.notification, feed.operation, feed.critical); !slices.Equal(got, want) {
		t.Fatalf("after deleting the Deploy bot slice = %v, want %v", got, want)
	}
	if err := s.Feed.DeleteAll(ctx, user.ID, FeedFilters{Priority: PriorityTimeSensitive}); err != nil {
		t.Fatalf("DeleteAll(time_sensitive): %v", err)
	}
	if got, want := remaining(), sorted(askedFeedID,
		feed.notification, feed.operation, feed.critical); !slices.Equal(got, want) {
		t.Fatalf("after deleting time_sensitive = %v, want %v", got, want)
	}
	if err := s.Feed.DeleteAll(ctx, user.ID, FeedFilters{Priority: PriorityCritical}); err != nil {
		t.Fatalf("DeleteAll(critical): %v", err)
	}
	if got, want := remaining(), sorted(askedFeedID,
		feed.notification, feed.operation); !slices.Equal(got, want) {
		t.Fatalf("after deleting critical = %v, want %v", got, want)
	}

	if _, err := s.Interactions.ByID(ctx, askedPending.ID); err != nil {
		t.Fatalf("the webhook's pending question was deleted early: %v", err)
	}
	if _, err := s.Interactions.ByID(ctx, freePending.ID); err != nil {
		t.Fatalf("the agent's pending question was deleted early: %v", err)
	}

	if err := s.Feed.DeleteAll(ctx, user.ID, FeedFilters{}); err != nil {
		t.Fatalf("DeleteAll: %v", err)
	}
	if got := remaining(); len(got) != 0 {
		t.Fatalf("feed after DeleteAll = %v, want none", got)
	}
	if _, err := s.Interactions.ByID(ctx, askedPending.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the webhook's pending question = %v, want ErrNotFound after its event went", err)
	}
	if _, err := s.Interactions.ByID(ctx, freePending.ID); err != nil {
		t.Errorf("the free-standing pending question was deleted: %v", err)
	}

	if err := s.Feed.DeleteAll(ctx, user.ID, FeedFilters{}); err != nil {
		t.Fatalf("repeat DeleteAll: %v", err)
	}
	strangerFeed, err := s.Feed.List(ctx, stranger.ID, FeedFilterAll, Cursor{}, 50)
	if err != nil || len(strangerFeed.Items) != 1 {
		t.Fatalf("stranger's feed = (%d items, %v), want their one event", len(strangerFeed.Items), err)
	}
}

func TestTransactionRollsBack(t *testing.T) {
	ctx, s := requireStore(t)
	user := mustUser(ctx, t, s, "ali")
	boom := errors.New("boom")

	err := s.Tx(ctx, func(ctx context.Context, tx *Store) error {
		if _, err := tx.Services.Create(ctx, CreateServiceParams{
			ID: id.New(), UserID: user.ID, Title: "doomed", Priority: PriorityNormal,
			TokenHash: "hash", TokenCiphertext: "v1.a.b.c", Now: time.Now(),
		}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Tx error = %v, want boom", err)
	}
	services, err := s.Services.ListForUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 0 {
		t.Errorf("the failed transaction left %d services behind", len(services))
	}

	// A Store already bound to a transaction runs a nested Tx inline, so a
	// composite helper can be reused inside a larger unit of work.
	if err := s.Tx(ctx, func(ctx context.Context, tx *Store) error {
		return tx.Tx(ctx, func(ctx context.Context, inner *Store) error {
			_, err := inner.Services.Create(ctx, CreateServiceParams{
				ID: id.New(), UserID: user.ID, Title: "kept", Priority: PriorityNormal,
				TokenHash: "hash", TokenCiphertext: "v1.a.b.c", Now: time.Now(),
			})
			return err
		})
	}); err != nil {
		t.Fatalf("nested Tx: %v", err)
	}
	services, err = s.Services.ListForUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 1 {
		t.Errorf("nested transaction wrote %d services, want 1", len(services))
	}
}

func TestActivityLookupsAreScopedToOneRequester(t *testing.T) {
	ctx, s := requireStore(t)
	user := mustUser(ctx, t, s, "ali")
	svc := mustService(ctx, t, s, user.ID, "Deploy bot")
	tok := mustToken(ctx, t, s, user.ID)
	now := time.Now()

	fromService, err := s.Activities.Start(ctx, StartActivityParams{
		ID: id.New(), UserID: user.ID, RequesterServiceID: &svc.ID, Key: ptr("deploy"),
		SchemaVersion: LiveActivitySchemaVersion, Props: props("Deploy", "Building"),
		IdempotencyKey: ptr("run-1"), RequestHash: ptr("hash"),
		ExpiresAt: now.Add(time.Hour), OperationID: id.New(), Now: now,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	// The same key and the same idempotency key are free for another
	// requester: scoping is per requester, not per account.
	if _, err := s.Activities.Start(ctx, StartActivityParams{
		ID: id.New(), UserID: user.ID, RequesterTokenID: &tok.ID, Key: ptr("deploy"),
		SchemaVersion: LiveActivitySchemaVersion, Props: props("Deploy", "Building"),
		IdempotencyKey: ptr("run-1"), RequestHash: ptr("hash"),
		ExpiresAt: now.Add(time.Hour), OperationID: id.New(), Now: now,
	}); err != nil {
		t.Fatalf("a second requester could not reuse the key: %v", err)
	}

	got, err := s.Activities.ByIdempotencyKey(ctx, nil, &svc.ID, "run-1")
	if err != nil || got.ID != fromService.Activity.ID {
		t.Fatalf("service-scoped idempotency lookup = (%v, %v)", got, err)
	}
	// A token must never resolve a service's row, or an idempotent retry would
	// replay somebody else's outcome.
	stranger := mustToken(ctx, t, s, user.ID)
	if _, err := s.Activities.ByIdempotencyKey(ctx, &stranger.ID, nil, "run-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-requester idempotency lookup error = %v, want ErrNotFound", err)
	}
	if _, err := s.Operations.ByIdempotencyKey(ctx, &stranger.ID, nil, "run-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-requester operation lookup error = %v, want ErrNotFound", err)
	}
	if op, err := s.Operations.ByIdempotencyKey(ctx, nil, &svc.ID, "run-1"); err != nil ||
		op.ID != fromService.Operation.ID {
		t.Fatalf("service-scoped operation lookup = (%v, %v)", op, err)
	}
}

func TestInteractiveActivitiesAreHiddenFromOrdinarySurfaces(t *testing.T) {
	ctx, s := requireStore(t)
	user := mustUser(ctx, t, s, "ali")
	tok := mustToken(ctx, t, s, user.ID)
	now := time.Now()

	interaction, err := s.Interactions.Create(ctx, CreateInteractionParams{
		ID: id.New(), UserID: user.ID, RequesterTokenID: &tok.ID, Title: "Claude Code",
		Prompt: "Allow once?", Kind: InteractionApproval, Presentation: PresentationLiveActivity,
		PrimaryLabel: ptr("Approve"), SecondaryLabel: ptr("Deny"),
		Choices: ChoicesFor(InteractionApproval), ActionDigest: "digest",
		ExpiresAt: now.Add(15 * time.Minute), Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}

	started, err := s.Activities.Start(ctx, StartActivityParams{
		ID: id.New(), UserID: user.ID, RequesterTokenID: &tok.ID, InteractionID: &interaction.ID,
		SchemaVersion: LiveActivitySchemaVersion, Props: props("Claude Code", "Needs your OK"),
		ExpiresAt: interaction.ExpiresAt, StaleAt: &interaction.ExpiresAt,
		OperationID: id.New(), Now: now,
	})
	if err != nil {
		t.Fatalf("start interactive activity: %v", err)
	}

	// The requester's own listings do not show it: it is driven by the
	// question, and letting a requester patch it directly would put the Lock
	// Screen out of step with what it is asking.
	page, err := s.Activities.ListForToken(ctx, tok.ID, Cursor{}, 50)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("ListForToken = (%d items, %v), want none", len(page.Items), err)
	}
	if _, err := s.Activities.Resolve(ctx, ResolveParams{
		Identifier: started.Activity.ID, RequesterTokenID: &tok.ID,
	}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Resolve without IncludeInteractive = %v, want ErrNotFound", err)
	}
	if got, err := s.Activities.Resolve(ctx, ResolveParams{
		Identifier: started.Activity.ID, RequesterTokenID: &tok.ID, IncludeInteractive: true,
	}); err != nil || got.ID != started.Activity.ID {
		t.Errorf("Resolve with IncludeInteractive = (%v, %v)", got, err)
	}
	live, err := s.Activities.ListLiveForUser(ctx, user.ID, now, 20)
	if err != nil || len(live) != 0 {
		t.Fatalf("ListLiveForUser = (%d items, %v), want none", len(live), err)
	}

	// Nor does it consume the requester's Live Activity budget.
	if n, err := s.Operations.CountForTokenSince(ctx, tok.ID, now.Add(-time.Minute)); err != nil || n != 0 {
		t.Fatalf("CountForTokenSince = (%d, %v), want (0, nil)", n, err)
	}

	// It is reachable the only way it should be: through its interaction.
	byInteraction, err := s.Activities.ByInteractionID(ctx, interaction.ID)
	if err != nil || byInteraction.ID != started.Activity.ID {
		t.Fatalf("ByInteractionID = (%v, %v)", byInteraction, err)
	}

	// And it exists only to present that question: deleting the answered
	// interaction from the history takes the activity with it.
	if _, err := s.Interactions.Respond(ctx, RespondParams{
		ID: interaction.ID, UserID: user.ID, Status: InteractionApproved,
		Response: ptr("approve"), Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	deleted, err := s.Interactions.Delete(ctx, interaction.ID, user.ID)
	if err != nil || !deleted {
		t.Fatalf("Delete = (%v, %v)", deleted, err)
	}
	if _, err := s.Activities.ByID(ctx, started.Activity.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the interaction's activity survived the delete: %v", err)
	}
}

func TestWebhookInteractionSurface(t *testing.T) {
	ctx, s := requireStore(t)
	user := mustUser(ctx, t, s, "ali")
	svc := mustService(ctx, t, s, user.ID, "Deploy bot")
	other := mustService(ctx, t, s, user.ID, "Other bot")
	now := time.Now()

	event, err := s.Events.Create(ctx, CreateEventParams{
		ID: id.New(), ServiceID: svc.ID, Title: "Deploy bot", Body: "Deploy to production?",
		Priority: PriorityNormal, Status: EventProcessing, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Interactions.Create(ctx, CreateInteractionParams{
		ID: id.New(), UserID: user.ID, RequesterServiceID: &svc.ID, EventID: &event.ID,
		Title: "Deploy bot", Prompt: "Deploy to production?", Kind: InteractionApproval,
		Presentation: PresentationNotification, Choices: ChoicesFor(InteractionApproval),
		ActionDigest: "digest", ExpiresAt: now.Add(15 * time.Minute), Now: now,
	}); err != nil {
		t.Fatal(err)
	}

	found, err := s.Interactions.ByEventForService(ctx, event.ID, svc.ID)
	if err != nil || found.EventID == nil || *found.EventID != event.ID {
		t.Fatalf("ByEventForService = (%v, %v)", found, err)
	}
	// One webhook credential must not read another's event response.
	if _, err := s.Interactions.ByEventForService(ctx, event.ID, other.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-service read error = %v, want ErrNotFound", err)
	}

	// A service may cancel its own question even once the deadline has passed:
	// the interaction shown on the device is being withdrawn, and the outcome is
	// the same either way.
	canceled, err := s.Interactions.CancelForEvent(ctx, event.ID, svc.ID, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("CancelForEvent past the deadline: %v", err)
	}
	if canceled.Status != InteractionCanceled {
		t.Errorf("status = %q", canceled.Status)
	}
	if _, err := s.Interactions.CancelForEvent(ctx, event.ID, svc.ID, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("re-cancelling error = %v, want ErrNotFound", err)
	}

	// The rate-limit counters see the service's own work and the account's.
	if n, err := s.Interactions.CountForServiceSince(ctx, svc.ID, now.Add(-time.Minute)); err != nil || n != 1 {
		t.Fatalf("CountForServiceSince = (%d, %v), want (1, nil)", n, err)
	}
	if n, err := s.Interactions.CountForUserSince(ctx, user.ID, now.Add(-time.Minute)); err != nil || n != 1 {
		t.Fatalf("CountForUserSince = (%d, %v), want (1, nil)", n, err)
	}

	tok := mustToken(ctx, t, s, user.ID)
	if _, err := s.Notifications.Create(ctx, CreateNotificationParams{
		ID: id.New(), UserID: user.ID, RequesterTokenID: tok.ID, Title: "Hark", Body: "done",
		Priority: PriorityNormal, IdempotencyKey: ptr("k"), RequestHash: ptr("h"), Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Notifications.ByIdempotencyKey(ctx, tok.ID, "k"); err != nil || got.Title != "Hark" {
		t.Fatalf("ByIdempotencyKey = (%v, %v)", got, err)
	}
	if n, err := s.Notifications.CountForTokenSince(ctx, tok.ID, now.Add(-time.Minute)); err != nil || n != 1 {
		t.Fatalf("CountForTokenSince = (%d, %v), want (1, nil)", n, err)
	}
	if n, err := s.Notifications.CountForUserSince(ctx, user.ID, now.Add(-time.Minute)); err != nil || n != 1 {
		t.Fatalf("CountForUserSince = (%d, %v), want (1, nil)", n, err)
	}
	if n, err := s.Interactions.CountForTokenSince(ctx, tok.ID, now.Add(-time.Minute)); err != nil || n != 0 {
		t.Fatalf("CountForTokenSince = (%d, %v), want (0, nil)", n, err)
	}
}

// TestLiveActivityInteractionDelivery covers the card that presents a question:
// it is started against the interaction, it occupies a delivery of its own
// purpose, and ending it takes the activity's live count to zero.
func TestLiveActivityInteractionDelivery(t *testing.T) {
	ctx, s := requireStore(t)
	user := mustUser(ctx, t, s, "ali")
	tok := mustToken(ctx, t, s, user.ID)
	device := mustDevice(ctx, t, s, user.ID, "aaaa")
	now := time.Now()

	interaction, err := s.Interactions.Create(ctx, CreateInteractionParams{
		ID: id.New(), UserID: user.ID, RequesterTokenID: &tok.ID, Title: "Claude Code",
		Prompt: "Allow once?", Kind: InteractionApproval, Presentation: PresentationLiveActivity,
		Choices: ChoicesFor(InteractionApproval), ActionDigest: "digest",
		ExpiresAt: now.Add(15 * time.Minute), Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := s.Activities.Start(ctx, StartActivityParams{
		ID: id.New(), UserID: user.ID, RequesterTokenID: &tok.ID, InteractionID: &interaction.ID,
		SchemaVersion: LiveActivitySchemaVersion, Props: props("Claude Code", "Needs your OK"),
		ExpiresAt: interaction.ExpiresAt, OperationID: id.New(),
		Targets: []ActivityTarget{{
			DeliveryID: id.New(), DeviceID: device.ID,
			Environment: EnvironmentSandbox, Purpose: PurposeInteraction,
		}},
		Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	delivery := started.Deliveries[0]
	if delivery.DeviceID != device.ID || delivery.Purpose != PurposeInteraction {
		t.Fatalf("delivery = %+v, want the interaction delivery for %s", delivery, device.ID)
	}
	if act, err := s.Activities.ByInteractionID(ctx, interaction.ID); err != nil || act.ID != started.Activity.ID {
		t.Fatalf("ByInteractionID = (%v, %v), want the started activity", act, err)
	}

	live, err := s.Deliveries.ListForActivity(ctx, started.Activity.ID, LiveStatuses())
	if err != nil || len(live) != 1 {
		t.Fatalf("ListForActivity(live) = (%d, %v), want 1", len(live), err)
	}
	if n, err := s.Activities.CountLiveDeliveries(ctx, started.Activity.ID); err != nil || n != 1 {
		t.Fatalf("CountLiveDeliveries = (%d, %v), want (1, nil)", n, err)
	}

	if _, err := s.Deliveries.End(ctx, EndParams{
		DeliveryID: delivery.ID, Sequence: 1, APNsStatus: ptr(200), Now: now,
	}); err != nil {
		t.Fatalf("End: %v", err)
	}
	if n, err := s.Activities.CountLiveDeliveries(ctx, started.Activity.ID); err != nil || n != 0 {
		t.Fatalf("CountLiveDeliveries after the end = (%d, %v), want (0, nil)", n, err)
	}
	all, err := s.Deliveries.ListForActivity(ctx, started.Activity.ID, nil)
	if err != nil || len(all) != 1 || all[0].Status != DeliveryEnded {
		t.Fatalf("ListForActivity(all) = (%+v, %v)", all, err)
	}

	loaded, err := s.Activities.ByIDs(ctx, []string{started.Activity.ID, id.New()})
	if err != nil || len(loaded) != 1 {
		t.Fatalf("ByIDs = (%d, %v), want 1", len(loaded), err)
	}
}

func TestRemainingMutations(t *testing.T) {
	ctx, s := requireStore(t)
	user := mustUser(ctx, t, s, "ali")
	device := mustDevice(ctx, t, s, user.ID, "aaaa")
	now := time.Now()

	if err := s.Users.SetPassword(ctx, user.ID, "salt:newkey", now); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	reloaded, err := s.Users.ByID(ctx, user.ID)
	if err != nil || reloaded.PasswordHash == nil || *reloaded.PasswordHash != "salt:newkey" ||
		reloaded.PasswordUpdatedAt == nil {
		t.Fatalf("after SetPassword: %+v (%v)", reloaded, err)
	}
	if err := s.Users.SetPassword(ctx, id.New(), "x", now); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetPassword on an unknown user = %v, want ErrNotFound", err)
	}

	session, err := s.Sessions.Create(ctx, CreateSessionParams{
		ID: id.New(), UserID: user.ID, TokenHash: "hash", ExpiresAt: now.Add(time.Hour), Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if deleted, err := s.Sessions.Delete(ctx, session.ID); err != nil || !deleted {
		t.Fatalf("Delete = (%v, %v)", deleted, err)
	}
	if deleted, err := s.Sessions.Delete(ctx, session.ID); err != nil || deleted {
		t.Fatalf("re-deleting = (%v, %v), want (false, nil)", deleted, err)
	}

	if ok, err := s.Devices.DeactivateByID(ctx, device.ID, now); err != nil || !ok {
		t.Fatalf("DeactivateByID = (%v, %v)", ok, err)
	}
	if ok, err := s.Devices.DeactivateByID(ctx, device.ID, now); err != nil || ok {
		t.Fatalf("re-deactivating = (%v, %v), want (false, nil)", ok, err)
	}
	if deleted, err := s.Devices.Delete(ctx, device.ID, user.ID); err != nil || !deleted {
		t.Fatalf("Delete device = (%v, %v)", deleted, err)
	}

	req, err := s.DeviceAuth.Create(ctx, CreateDeviceAuthorizationParams{
		ID: id.New(), DeviceCodeHash: "hash", UserCode: "ABCD-EFGH", ClientName: "harkctl",
		RequestedScopes: []string{ScopeEventsRead}, ExpiresAt: now.Add(10 * time.Minute),
		TokenExpiresAt: now.Add(24 * time.Hour), PollIntervalSeconds: 5, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeviceAuth.RecordPoll(ctx, req.ID, now); err != nil {
		t.Fatalf("RecordPoll: %v", err)
	}
	polled, err := s.DeviceAuth.ByDeviceCodeHash(ctx, "hash")
	if err != nil || polled.LastPolledAt == nil {
		t.Fatalf("after RecordPoll: %+v (%v)", polled, err)
	}
	// A request that ran out of time is retired on the next read of it.
	expired, err := s.DeviceAuth.MarkExpired(ctx, req.ID, now.Add(time.Hour))
	if err != nil || expired.Status != DeviceAuthExpired || expired.ResolvedAt == nil {
		t.Fatalf("MarkExpired = (%+v, %v)", expired, err)
	}
	// Burning an approved grant the account has no room for.
	denied, err := s.DeviceAuth.DenyByID(ctx, req.ID, now)
	if err != nil || denied.Status != DeviceAuthDenied {
		t.Fatalf("DenyByID = (%+v, %v)", denied, err)
	}
}

func TestCriticalServiceLifecycle(t *testing.T) {
	ctx, s := requireStore(t)
	user := mustUser(ctx, t, s, "ali")

	// Account and service settings start enabled and can be changed independently.
	if !user.CriticalAlertsEnabled {
		t.Error("a fresh account should have critical alerts enabled")
	}
	if err := s.Users.SetCriticalAlertsEnabled(ctx, user.ID, false, time.Now()); err != nil {
		t.Fatalf("SetCriticalAlertsEnabled: %v", err)
	}
	if reloaded, err := s.Users.ByID(ctx, user.ID); err != nil || reloaded.CriticalAlertsEnabled {
		t.Fatalf("after disabling: %+v (%v)", reloaded, err)
	}
	if err := s.Users.SetCriticalAlertsEnabled(ctx, id.New(), true, time.Now()); !errors.Is(err, ErrNotFound) {
		t.Errorf("toggling an unknown user = %v, want ErrNotFound", err)
	}

	source := mustCriticalService(ctx, t, s, user.ID, "Home Assistant")
	if !source.CriticalEnabled {
		t.Errorf("fresh critical service = %+v, want critical delivery enabled", source)
	}
	source, err := s.Services.Update(ctx, UpdateServiceParams{
		ID: source.ID, UserID: user.ID, CriticalCapable: true,
		CriticalEnabled: Value(false), Now: time.Now(),
	})
	if err != nil || source.CriticalEnabled {
		t.Errorf("disabling a critical service = (%+v, %v), want success", source, err)
	}

	svc := mustCriticalService(ctx, t, s, user.ID, "Kitchen")

	if got, err := s.Services.CriticalByID(ctx, svc.ID, user.ID); err != nil || got.ID != svc.ID {
		t.Fatalf("CriticalByID = (%v, %v)", got, err)
	}
	if _, err := s.Services.CriticalByID(ctx, svc.ID, id.New()); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-account read error = %v, want ErrNotFound", err)
	}
	if _, err := s.Services.ByID(ctx, svc.ID, user.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("critical service leaked into regular lookup: %v", err)
	}

	second := mustCriticalService(ctx, t, s, user.ID, "Basement")
	listed, err := s.Services.ListCriticalForUser(ctx, user.ID)
	if err != nil || len(listed) != 3 {
		t.Fatalf("ListForUser = (%d, %v), want 3", len(listed), err)
	}

	// Partial updates leave unset fields unchanged.
	updated, err := s.Services.Update(ctx, UpdateServiceParams{
		ID: svc.ID, UserID: user.ID, CriticalCapable: true,
		ImageURL: Value(ptr("https://example.com/kitchen.png")),
		URL:      Value(ptr("hark-test://kitchen")), CriticalEnabled: Value(true), Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("update source: %v", err)
	}
	if !updated.CriticalEnabled || updated.Title != "Kitchen" || updated.ImageURL == nil || updated.URL == nil {
		t.Errorf("updated = %+v, want service defaults and the toggle saved", updated)
	}
	if !updated.UpdatedAt.After(svc.UpdatedAt) {
		t.Error("UpdatedAt was not bumped")
	}
	renamed, err := s.Services.Update(ctx, UpdateServiceParams{
		ID: svc.ID, UserID: user.ID, CriticalCapable: true,
		Title: Value("Hallway"), Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("rename source: %v", err)
	}
	if renamed.Title != "Hallway" || !renamed.CriticalEnabled {
		t.Errorf("renamed = %+v, want the name changed and the toggle untouched", renamed)
	}
	if _, err := s.Services.Update(ctx, UpdateServiceParams{
		ID: svc.ID, UserID: id.New(), CriticalCapable: true,
		Title: Value("hijacked"), Now: time.Now(),
	}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update scoped to another owner error = %v, want ErrNotFound", err)
	}

	deleted, err := s.Services.DeleteCritical(ctx, second.ID, user.ID)
	if err != nil || !deleted {
		t.Fatalf("Delete = (%v, %v)", deleted, err)
	}
	if deleted, err := s.Services.DeleteCritical(ctx, second.ID, user.ID); err != nil || deleted {
		t.Fatalf("re-deleting = (%v, %v), want (false, nil)", deleted, err)
	}
}

func TestCriticalServiceDeleteCascades(t *testing.T) {
	ctx, s := requireStore(t)
	user := mustUser(ctx, t, s, "ali")
	svc := mustCriticalService(ctx, t, s, user.ID, "Front door")
	now := time.Now()

	event, err := s.Events.Create(ctx, CreateEventParams{
		ID: id.New(), ServiceID: svc.ID, Title: "t", Body: "b",
		Priority: PriorityCritical, Status: EventAccepted,
		IdempotencyKey: ptr("alert-1"), RequestHash: ptr("hash"), Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}

	deleted, err := s.Services.DeleteCritical(ctx, svc.ID, user.ID)
	if err != nil || !deleted {
		t.Fatalf("Delete = (%v, %v)", deleted, err)
	}
	if _, err := s.Events.ByID(ctx, event.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the critical service's events survived the delete: %v", err)
	}
}

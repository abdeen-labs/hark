package dashboard

// The services pages against a real store: create, read back, edit, rotate,
// delete. Set TEST_DATABASE_URL (or HARK_TEST_DATABASE_URL) to run:
//
//	TEST_DATABASE_URL=postgres://hark:hark@localhost:5432/hark_test go test ./internal/dashboard
//
// The auth service stays the fake — these tests are about the handlers and the
// store, and the principal is injected the way internal/httpapi's authenticator
// would have left it.

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/id"
)

// dashSchema is this package's own schema, for the same reason every other
// PG-testing package holds one: parallel packages must not reset each other's
// tables.
const dashSchema = "hark_dashboard_test"

var (
	dashSchemaOnce sync.Once
	dashPool       *pgxpool.Pool
	dashSchemaErr  error
)

func newPGDashboard(t *testing.T) (*Dashboard, *db.Store, string) {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("HARK_TEST_DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	t.Cleanup(cancel)

	dashSchemaOnce.Do(func() {
		pool, err := db.Open(context.WithoutCancel(ctx), db.Config{
			URL: dashSearchPath(dsn), MaxConns: 4, ConnectTimeout: 10 * time.Second,
		})
		if err != nil {
			dashSchemaErr = err
			return
		}
		if _, err := pool.Exec(ctx,
			"DROP SCHEMA IF EXISTS "+dashSchema+" CASCADE; CREATE SCHEMA "+dashSchema); err != nil {
			dashSchemaErr = err
			return
		}
		if err := db.Migrate(ctx, pool, db.Migrations(), slog.New(slog.DiscardHandler)); err != nil {
			dashSchemaErr = err
			return
		}
		dashPool = pool
	})
	if dashSchemaErr != nil {
		t.Fatalf("prepare test schema: %v", dashSchemaErr)
	}
	if _, err := dashPool.Exec(ctx, "TRUNCATE users, services RESTART IDENTITY CASCADE"); err != nil {
		t.Fatalf("reset tables: %v", err)
	}

	store := db.New(dashPool)
	user, err := store.Users.Create(ctx, db.CreateUserParams{
		ID: id.New(), Username: "owner", PasswordHash: ptr("not-a-real-hash"),
		Email: "owner@hark.local", Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("create the account: %v", err)
	}

	d := New(Options{
		Auth:      &fakeAuth{now: time.Now()},
		Store:     store,
		Secrets:   testKeeper(),
		PublicURL: &url.URL{Scheme: "https", Host: "hark.example.com"},
	})
	return d, store, user.ID
}

func dashSearchPath(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	q.Set("search_path", dashSchema)
	u.RawQuery = q.Encode()
	return u.String()
}

// asOwner injects userID's session principal, the way the API middleware would.
func asOwner(req *http.Request, userID string) *http.Request {
	return req.WithContext(auth.WithPrincipal(req.Context(), &auth.Principal{
		Kind:    auth.KindSession,
		User:    db.User{ID: userID, Username: "owner"},
		Session: &db.Session{ID: "session-1"},
	}))
}

func TestServiceLifecycleThroughTheDashboard(t *testing.T) {
	d, store, userID := newPGDashboard(t)
	ctx := t.Context()

	// Create. The redirect lands on the new service's own page.
	form := "title=CI&priority=time_sensitive&image_url=https://example.com/logo.png&url=https://example.com/run"
	rec := send(d, withCSRF(t, d, asOwner(signedIn(http.MethodPost, pathServices, ""), userID), form))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create: status = %d, want %d: %s", rec.Code, http.StatusSeeOther, rec.Body)
	}
	location := rec.Header().Get("Location")
	if !strings.HasPrefix(location, pathServices+"/") || !strings.HasSuffix(location, "?done=service_created") {
		t.Fatalf("create: Location = %q, want the new service's page", location)
	}
	svcID := strings.TrimSuffix(strings.TrimPrefix(location, pathServices+"/"), "?done=service_created")

	svc, err := store.Services.ByID(ctx, svcID, userID)
	if err != nil {
		t.Fatalf("load the created service: %v", err)
	}
	if svc.Title != "CI" || svc.Priority != db.PriorityTimeSensitive ||
		svc.ImageURL == nil || svc.URL == nil {
		t.Errorf("created service = %+v, want the submitted defaults", svc)
	}

	// The service page shows the webhook URL read back from the ciphertext.
	rec = send(d, asOwner(signedIn(http.MethodGet, location, ""), userID))
	if rec.Code != http.StatusOK {
		t.Fatalf("show: status = %d: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "/v1/hooks/"+auth.WebhookTokenPrefix) {
		t.Errorf("the service page does not show the webhook URL:\n%s", rec.Body)
	}

	// So does the list.
	rec = send(d, asOwner(signedIn(http.MethodGet, pathServices, ""), userID))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `data-service="`+svcID+`"`) {
		t.Fatalf("list: status = %d, or the service is missing:\n%s", rec.Code, rec.Body)
	}

	// Edit: retitle, clear the tap URL, keep the avatar.
	form = "title=CD&priority=normal&image_url=https://example.com/logo.png&url="
	rec = send(d, withCSRF(t, d, asOwner(signedIn(http.MethodPost, pathServices+"/"+svcID, ""), userID), form))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("update: status = %d: %s", rec.Code, rec.Body)
	}
	svc, err = store.Services.ByID(ctx, svcID, userID)
	if err != nil {
		t.Fatalf("reload the service: %v", err)
	}
	if svc.Title != "CD" || svc.Priority != db.PriorityNormal || svc.URL != nil || svc.ImageURL == nil {
		t.Errorf("updated service = %+v, want title CD, normal, no URL, avatar kept", svc)
	}

	// Rotate: the credential changes, and the old hash is gone with it.
	before := svc.TokenHash
	rec = send(d, withCSRF(t, d, asOwner(signedIn(http.MethodPost, pathServices+"/"+svcID+"/rotate", ""), userID), ""))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("rotate: status = %d: %s", rec.Code, rec.Body)
	}
	svc, err = store.Services.ByID(ctx, svcID, userID)
	if err != nil {
		t.Fatalf("reload the service: %v", err)
	}
	if svc.TokenHash == before {
		t.Error("rotate did not change the webhook credential")
	}

	// Delete.
	rec = send(d, withCSRF(t, d, asOwner(signedIn(http.MethodPost, pathServices+"/"+svcID+"/delete", ""), userID), ""))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("delete: status = %d: %s", rec.Code, rec.Body)
	}
	if _, err := store.Services.ByID(ctx, svcID, userID); err == nil {
		t.Error("the service still exists after delete")
	}
}

func TestServiceCreateRejectsABadForm(t *testing.T) {
	d, _, userID := newPGDashboard(t)

	form := "title=&priority=normal&image_url=http://example.com/logo.png"
	rec := send(d, withCSRF(t, d, asOwner(signedIn(http.MethodPost, pathServices, ""), userID), form))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusUnprocessableEntity, rec.Body)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `data-notice="error"`) {
		t.Errorf("the page does not carry an error banner:\n%s", body)
	}
	// The rejected value comes back so the owner can fix it in place.
	if !strings.Contains(body, `value="http://example.com/logo.png"`) {
		t.Errorf("the form lost the submitted avatar URL:\n%s", body)
	}
}

func TestServicePagesAnswer404ForAForeignService(t *testing.T) {
	d, store, userID := newPGDashboard(t)
	ctx := t.Context()

	other, err := store.Users.Create(ctx, db.CreateUserParams{
		ID: id.New(), Username: "intruder", PasswordHash: ptr("not-a-real-hash"),
		Email: "intruder@hark.local", Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("create the second account: %v", err)
	}
	svc, err := store.Services.Create(ctx, db.CreateServiceParams{
		ID: id.New(), UserID: userID, Title: "CI", Priority: db.PriorityNormal,
		TokenHash: "hash-1", TokenCiphertext: "sealed", Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("create the service: %v", err)
	}

	rec := send(d, asOwner(signedIn(http.MethodGet, pathServices+"/"+svc.ID, ""), other.ID))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET a foreign service: status = %d, want 404", rec.Code)
	}
	rec = send(d, withCSRF(t, d, asOwner(signedIn(http.MethodPost, pathServices+"/"+svc.ID+"/rotate", ""), other.ID), ""))
	if rec.Code != http.StatusNotFound {
		t.Errorf("rotate a foreign service: status = %d, want 404", rec.Code)
	}
}

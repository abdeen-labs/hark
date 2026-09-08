package db

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func requireSchemaPostgres(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	cfg, err := pgxpool.ParseConfig(testDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.NewWithConfig(ctx, cfg.Copy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(ctx, "DROP SCHEMA IF EXISTS hark_schema_test CASCADE; CREATE SCHEMA hark_schema_test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.WithoutCancel(ctx), "DROP SCHEMA hark_schema_test CASCADE") })
	cfg.ConnConfig.RuntimeParams["search_path"] = "hark_schema_test"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return ctx, pool
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestInitializeSchemaAppliesInOrderAndPreservesData(t *testing.T) {
	ctx, pool := requireSchemaPostgres(t)
	files := fstest.MapFS{
		"002_child.sql":  {Data: []byte("CREATE TABLE child (parent_id text REFERENCES parent(id));")},
		"001_parent.sql": {Data: []byte("CREATE TABLE parent (id text PRIMARY KEY);")},
	}
	if err := initializeSchema(ctx, pool, files, testLogger()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO parent VALUES ('retained'); INSERT INTO child VALUES ('retained')"); err != nil {
		t.Fatal(err)
	}
	if err := initializeSchema(ctx, pool, files, testLogger()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM child WHERE parent_id = 'retained'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("restart did not preserve data: count=%d, err=%v", count, err)
	}
}

func TestInitializeSchemaRejectsChangedDefinition(t *testing.T) {
	ctx, pool := requireSchemaPostgres(t)
	original := fstest.MapFS{"001_probe.sql": {Data: []byte("CREATE TABLE probe (id text PRIMARY KEY);")}}
	if err := initializeSchema(ctx, pool, original, testLogger()); err != nil {
		t.Fatal(err)
	}
	changed := fstest.MapFS{
		"001_probe.sql": original["001_probe.sql"],
		"002_extra.sql": {Data: []byte("CREATE TABLE extra (id text);")},
	}
	if err := initializeSchema(ctx, pool, changed, testLogger()); !errors.Is(err, ErrSchemaMismatch) {
		t.Fatalf("changed definition: %v, want ErrSchemaMismatch", err)
	}
	var extra *string
	if err := pool.QueryRow(ctx, "SELECT to_regclass('extra')::text").Scan(&extra); err != nil || extra != nil {
		t.Fatalf("changed schema was applied: table=%v, err=%v", extra, err)
	}
}

func TestInitializeSchemaRollsBackEntireDefinition(t *testing.T) {
	ctx, pool := requireSchemaPostgres(t)
	files := fstest.MapFS{
		"001_probe.sql":  {Data: []byte("CREATE TABLE probe (id text PRIMARY KEY);")},
		"002_broken.sql": {Data: []byte("SELECT this_does_not_exist();")},
	}
	if err := initializeSchema(ctx, pool, files, testLogger()); err == nil {
		t.Fatal("broken schema accepted")
	}
	for _, name := range []string{"probe", "hark_schema"} {
		var table *string
		if err := pool.QueryRow(ctx, "SELECT to_regclass($1)::text", name).Scan(&table); err != nil || table != nil {
			t.Fatalf("partial initialization: %s=%v, err=%v", name, table, err)
		}
	}
	delete(files, "002_broken.sql")
	if err := initializeSchema(ctx, pool, files, testLogger()); err != nil {
		t.Fatal(err)
	}
}

func TestInitializeSchemaConcurrentStartup(t *testing.T) {
	ctx, pool := requireSchemaPostgres(t)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if err := InitializeSchema(ctx, pool, testLogger()); err != nil {
				t.Errorf("initialize: %v", err)
			}
		})
	}
	wg.Wait()
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM hark_schema").Scan(&count); err != nil || count != 1 {
		t.Fatalf("fingerprint count=%d, err=%v", count, err)
	}
}

func TestSchemaRejectsEmptyRequiredArrays(t *testing.T) {
	ctx, pool := requireSchemaPostgres(t)
	if err := InitializeSchema(ctx, pool, testLogger()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, username, email, display_name, created_at, updated_at)
		VALUES ('user', 'user', 'user@example.com', 'User', now(), now());
		INSERT INTO api_tokens (id, user_id, name, token_hash, prefix, scopes, created_at)
		VALUES ('token', 'user', 'Token', 'hash', 'prefix', ARRAY['interactions:create'], now());
		INSERT INTO device_authorization_requests
			(id, device_code_hash, user_code, client_name, requested_scopes,
			 expires_at, token_expires_at, poll_interval_seconds, created_at)
		VALUES ('grant', 'hash', 'CODE', 'Client', ARRAY['interactions:create'], now(), now(), 5, now());
		INSERT INTO interactions
			(id, user_id, requester_token_id, title, prompt, kind, choices,
			 action_digest, expires_at, created_at)
		VALUES ('interaction', 'user', 'token', 'Title', 'Prompt', 'approval',
			ARRAY['approve', 'deny'], 'digest', now(), now());
	`); err != nil {
		t.Fatal(err)
	}
	for name, query := range map[string]string{
		"token scopes":   "UPDATE api_tokens SET scopes = '{}'::text[]",
		"grant scopes":   "UPDATE device_authorization_requests SET requested_scopes = '{}'::text[]",
		"answer choices": "UPDATE interactions SET choices = '{}'::text[]",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := pool.Exec(ctx, query)
			var constraint *pgconn.PgError
			if !errors.As(err, &constraint) || constraint.Code != "23514" {
				t.Fatalf("empty array error = %v, want check violation", err)
			}
		})
	}
}

func TestOpenFailsFastWhenUnreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	// Port 1 is reserved and never listening.
	_, err := Open(ctx, Config{
		URL:            "postgres://hark:hunter2@127.0.0.1:1/hark",
		ConnectTimeout: 2 * time.Second,
	})
	if err == nil {
		t.Fatal("Open succeeded against an unreachable database")
	}
	if got := err.Error(); !strings.Contains(got, "cannot reach PostgreSQL") {
		t.Errorf("error = %q, want a clear unreachable message", got)
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("error leaks the database password: %v", err)
	}
}

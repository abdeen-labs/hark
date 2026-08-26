package db

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// requirePostgres returns a pool against the scratch database named by
// TEST_DATABASE_URL, skipping when it is unset — see [testDatabaseURL]. The
// tests below create and drop their own tables:
//
//	TEST_DATABASE_URL=postgres://hark:hark@localhost:5432/hark_test go test ./internal/db
func requirePostgres(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	url := testDatabaseURL(t)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)

	pool, err := Open(ctx, Config{URL: url, MaxConns: 4, ConnectTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(pool.Close)

	reset := []string{
		"DROP TABLE IF EXISTS schema_migrations",
		"DROP TABLE IF EXISTS migrate_probe",
	}
	for _, stmt := range reset {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("reset (%s): %v", stmt, err)
		}
	}
	t.Cleanup(func() {
		for _, stmt := range reset {
			_, _ = pool.Exec(context.WithoutCancel(ctx), stmt)
		}
	})
	return ctx, pool
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestMigrateAppliesAndIsIdempotent(t *testing.T) {
	ctx, pool := requirePostgres(t)

	fsys := fstest.MapFS{
		"0001_create_probe.sql": {Data: []byte("CREATE TABLE migrate_probe (id text PRIMARY KEY);")},
		"0002_add_column.sql":   {Data: []byte("ALTER TABLE migrate_probe ADD COLUMN label text;")},
	}

	if err := Migrate(ctx, pool, fsys, testLogger()); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}

	var applied int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&applied); err != nil {
		t.Fatalf("count: %v", err)
	}
	if applied != 2 {
		t.Fatalf("applied %d migrations, want 2", applied)
	}

	// The column from 0002 must exist.
	var columns int
	const q = `SELECT count(*) FROM information_schema.columns
	           WHERE table_name = 'migrate_probe' AND column_name = 'label'`
	if err := pool.QueryRow(ctx, q).Scan(&columns); err != nil {
		t.Fatalf("column check: %v", err)
	}
	if columns != 1 {
		t.Error("migration 0002 did not add the label column")
	}

	// Re-running must be a no-op rather than an error.
	if err := Migrate(ctx, pool, fsys, testLogger()); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
}

func TestMigrateRejectsEditedMigration(t *testing.T) {
	ctx, pool := requirePostgres(t)

	original := fstest.MapFS{"0001_create_probe.sql": {Data: []byte("CREATE TABLE migrate_probe (id text PRIMARY KEY);")}}
	if err := Migrate(ctx, pool, original, testLogger()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	edited := fstest.MapFS{"0001_create_probe.sql": {Data: []byte("CREATE TABLE migrate_probe (id text PRIMARY KEY, extra text);")}}
	err := Migrate(ctx, pool, edited, testLogger())
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("err = %v, want ErrChecksumMismatch", err)
	}
}

func TestMigrateRollsBackAFailedMigration(t *testing.T) {
	ctx, pool := requirePostgres(t)

	fsys := fstest.MapFS{
		"0001_create_probe.sql": {Data: []byte("CREATE TABLE migrate_probe (id text PRIMARY KEY);")},
		"0002_broken.sql":       {Data: []byte("ALTER TABLE migrate_probe ADD COLUMN label text; SELECT this_does_not_exist();")},
	}
	if err := Migrate(ctx, pool, fsys, testLogger()); err == nil {
		t.Fatal("Migrate succeeded, want the broken migration to fail")
	}

	var applied int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&applied); err != nil {
		t.Fatalf("count: %v", err)
	}
	if applied != 1 {
		t.Fatalf("ledger has %d rows, want only the successful migration", applied)
	}

	var columns int
	const q = `SELECT count(*) FROM information_schema.columns
	           WHERE table_name = 'migrate_probe' AND column_name = 'label'`
	if err := pool.QueryRow(ctx, q).Scan(&columns); err != nil {
		t.Fatalf("column check: %v", err)
	}
	if columns != 0 {
		t.Error("the failed migration's DDL was not rolled back")
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

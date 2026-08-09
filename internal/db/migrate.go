package db

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// migrationsFS embeds internal/db/migrations. The `all:` prefix takes
// dot-prefixed files too, so the directory stays embeddable even when the only
// thing in it is a placeholder; [LoadMigrations] reads nothing but .sql files.
//
//go:embed all:migrations
var migrationsFS embed.FS

// Migrations is the migration set compiled into the binary.
func Migrations() fs.FS {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic("db: embedded migrations directory is missing: " + err.Error())
	}
	return sub
}

// ledgerTable records which migrations have been applied. It is created by the
// runner itself rather than by a migration, because it has to exist before the
// first migration can be selected.
const ledgerTable = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    bigint      PRIMARY KEY,
    name       text        NOT NULL,
    checksum   text        NOT NULL,
    applied_at timestamptz NOT NULL DEFAULT now()
)`

// lockKey serialises concurrent migrators. Any constant works as long as every
// replica uses the same one; this is the low 63 bits of fnv1a("hark.schema").
const lockKey int64 = 0x4861726b53636865

// Migration is one ordered .sql file.
type Migration struct {
	Version  int64
	Name     string // file name without the numeric prefix or the .sql suffix
	File     string
	SQL      string
	Checksum string // sha256 hex of SQL, recorded so edits to applied files are caught
}

// String renders "0003_add_devices" for logs and error messages.
func (m Migration) String() string { return fmt.Sprintf("%04d_%s", m.Version, m.Name) }

var migrationName = regexp.MustCompile(`^(\d+)_([a-z0-9_]+)\.sql$`)

// LoadMigrations reads and orders every .sql file in fsys. File names must look
// like `0001_create_user.sql`: a decimal version, an underscore, a lowercase
// snake_case name. Non-.sql files are ignored so bookkeeping files can live
// alongside migrations.
func LoadMigrations(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("db: read migrations: %w", err)
	}

	var out []Migration
	seen := make(map[int64]string)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || path.Ext(name) != ".sql" {
			continue
		}
		m := migrationName.FindStringSubmatch(name)
		if m == nil {
			return nil, fmt.Errorf("db: migration %q must be named <version>_<snake_case_name>.sql", name)
		}
		version, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("db: migration %q has an invalid version", name)
		}
		if prev, dup := seen[version]; dup {
			return nil, fmt.Errorf("db: migrations %q and %q share version %d", prev, name, version)
		}
		seen[version] = name

		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("db: read migration %q: %w", name, err)
		}
		if strings.TrimSpace(string(body)) == "" {
			return nil, fmt.Errorf("db: migration %q is empty", name)
		}
		sum := sha256.Sum256(body)
		out = append(out, Migration{
			Version:  version,
			Name:     m[2],
			File:     name,
			SQL:      string(body),
			Checksum: hex.EncodeToString(sum[:]),
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// ErrChecksumMismatch reports a migration whose file changed after it was
// applied. Editing applied migrations makes environments silently diverge, so
// this is always fatal.
var ErrChecksumMismatch = errors.New("db: applied migration has changed on disk")

// Migrate applies every pending migration in version order.
//
// Each migration runs inside its own transaction together with the ledger
// insert, so a failure leaves the database on the last fully applied version.
// A PostgreSQL advisory lock is held for the duration so that concurrently
// starting replicas do not race.
func Migrate(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS, log *slog.Logger) error {
	migrations, err := LoadMigrations(fsys)
	if err != nil {
		return err
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("db: acquire migration connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", lockKey); err != nil {
		return fmt.Errorf("db: acquire migration lock: %w", err)
	}
	defer func() {
		// Best effort: releasing the session lock also happens implicitly when
		// the connection returns to the pool and is eventually closed.
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", lockKey)
	}()

	if _, err := conn.Exec(ctx, ledgerTable); err != nil {
		return fmt.Errorf("db: create schema_migrations: %w", err)
	}

	applied, err := appliedMigrations(ctx, conn)
	if err != nil {
		return err
	}

	pending := 0
	for _, m := range migrations {
		if checksum, ok := applied[m.Version]; ok {
			if checksum != m.Checksum {
				return fmt.Errorf("%w: %s", ErrChecksumMismatch, m)
			}
			continue
		}
		if err := applyMigration(ctx, conn, m); err != nil {
			return err
		}
		pending++
		log.Info("applied migration", "version", m.Version, "name", m.Name)
	}

	if pending == 0 {
		log.Debug("schema is up to date", "version", maxVersion(applied), "migrations", len(migrations))
	}
	return nil
}

func appliedMigrations(ctx context.Context, conn *pgxpool.Conn) (map[int64]string, error) {
	rows, err := conn.Query(ctx, "SELECT version, checksum FROM schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("db: read schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int64]string)
	for rows.Next() {
		var version int64
		var checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			return nil, fmt.Errorf("db: scan schema_migrations: %w", err)
		}
		applied[version] = checksum
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: read schema_migrations: %w", err)
	}
	return applied, nil
}

func applyMigration(ctx context.Context, conn *pgxpool.Conn, m Migration) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("db: begin migration %s: %w", m, err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if _, err := tx.Exec(ctx, m.SQL); err != nil {
		return fmt.Errorf("db: apply migration %s: %w", m, err)
	}
	const insert = `INSERT INTO schema_migrations (version, name, checksum) VALUES ($1, $2, $3)`
	if _, err := tx.Exec(ctx, insert, m.Version, m.Name, m.Checksum); err != nil {
		return fmt.Errorf("db: record migration %s: %w", m, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("db: commit migration %s: %w", m, err)
	}
	return nil
}

func maxVersion(applied map[int64]string) int64 {
	var highest int64
	for v := range applied {
		if v > highest {
			highest = v
		}
	}
	return highest
}

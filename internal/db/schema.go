package db

import (
	"context"
	"crypto/sha256"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema/*.sql
var schemaFS embed.FS

var ErrSchemaMismatch = errors.New("db: database schema does not match this binary; use a fresh database")

// InitializeSchema creates the complete schema in an empty database. Restarts
// verify its fingerprint without modifying tables or data.
func InitializeSchema(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	files, err := fs.Sub(schemaFS, "schema")
	if err != nil {
		return fmt.Errorf("db: open embedded schema: %w", err)
	}
	return initializeSchema(ctx, pool, files, log)
}

func readSchema(files fs.FS) (string, string, error) {
	names, err := fs.Glob(files, "*.sql")
	if err != nil {
		return "", "", fmt.Errorf("db: list schema files: %w", err)
	}
	if len(names) == 0 {
		return "", "", errors.New("db: schema has no SQL files")
	}
	var sql strings.Builder
	for _, name := range names {
		body, err := fs.ReadFile(files, name)
		if err != nil {
			return "", "", fmt.Errorf("db: read schema file %s: %w", name, err)
		}
		if strings.TrimSpace(string(body)) == "" {
			return "", "", fmt.Errorf("db: schema file %s is empty", name)
		}
		fmt.Fprintf(&sql, "-- %s\n%s\n", name, body)
	}
	definition := sql.String()
	return definition, fmt.Sprintf("%x", sha256.Sum256([]byte(definition))), nil
}

func initializeSchema(ctx context.Context, pool *pgxpool.Pool, files fs.FS, log *slog.Logger) error {
	definition, checksum, err := readSchema(files)
	if err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("db: begin schema initialization: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	// Transaction-scoped locking releases on rollback, including cancellation.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", int64(0x4861726b53636865)); err != nil {
		return fmt.Errorf("db: lock schema initialization: %w", err)
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS hark_schema (
		singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
		checksum text NOT NULL
	)`); err != nil {
		return fmt.Errorf("db: create schema fingerprint: %w", err)
	}
	var existing string
	err = tx.QueryRow(ctx, "SELECT checksum FROM hark_schema WHERE singleton").Scan(&existing)
	switch {
	case err == nil:
		if existing != checksum {
			return ErrSchemaMismatch
		}
	case errors.Is(err, pgx.ErrNoRows):
		if _, err := tx.Exec(ctx, definition); err != nil {
			return fmt.Errorf("db: initialize schema: %w", err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO hark_schema (checksum) VALUES ($1)", checksum); err != nil {
			return fmt.Errorf("db: record schema fingerprint: %w", err)
		}
	default:
		return fmt.Errorf("db: read schema fingerprint: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("db: commit schema initialization: %w", err)
	}
	log.Debug("database schema ready", "checksum", checksum)
	return nil
}

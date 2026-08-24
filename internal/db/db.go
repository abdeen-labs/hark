// Package db owns the PostgreSQL connection pool and the embedded schema
// migrations.
//
// Callers provide a [Config] and receive a *pgxpool.Pool; this package has no
// dependency on higher server layers.
package db

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Config describes the connection pool. Zero values fall back to pgx defaults
// or to the values already encoded in the DSN.
type Config struct {
	// URL is a postgres:// or postgresql:// DSN.
	URL string

	MaxConns        int32
	MinConns        int32
	ConnectTimeout  time.Duration
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
}

// Open builds the pool and verifies that the database is reachable.
//
// It returns an error rather than a lazily-connecting pool so that a
// misconfigured or unreachable database fails the process at boot, with the
// DSN password redacted from every message.
func Open(ctx context.Context, cfg Config) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		// pgx includes the complete DSN and password in parse errors, so return a
		// sanitized error.
		return nil, errors.New("db: DATABASE_URL is not a valid PostgreSQL connection string")
	}

	if cfg.MaxConns > 0 {
		poolCfg.MaxConns = cfg.MaxConns
	}
	if cfg.MinConns > 0 {
		poolCfg.MinConns = cfg.MinConns
	}
	if cfg.MaxConnLifetime > 0 {
		poolCfg.MaxConnLifetime = cfg.MaxConnLifetime
	}
	if cfg.MaxConnIdleTime > 0 {
		poolCfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	}
	if cfg.ConnectTimeout > 0 {
		poolCfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("db: create pool for %s: %w", Redact(cfg.URL), err)
	}

	pingCtx := ctx
	if cfg.ConnectTimeout > 0 {
		var cancel context.CancelFunc
		pingCtx, cancel = context.WithTimeout(ctx, cfg.ConnectTimeout)
		defer cancel()
	}
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: cannot reach PostgreSQL at %s: %w", Redact(cfg.URL), err)
	}
	return pool, nil
}

// Redact replaces the password in a connection URL so it can be logged.
func Redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<invalid url>"
	}
	if u.User != nil {
		if _, ok := u.User.Password(); ok {
			u.User = url.UserPassword(u.User.Username(), "xxxxx")
		}
	}
	u.RawQuery = ""
	return u.String()
}

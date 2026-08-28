package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Querier is the slice of pgx that the stores use. Both *pgxpool.Pool and
// pgx.Tx satisfy it, which is what lets a [Store] be re-bound to a transaction
// without any of the queries knowing.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Store is the persistence layer: one sub-store per domain, all sharing the
// same connection or transaction.
//
// Nothing in here decides policy. Stores run the exact statement they are
// asked for, including the conditional updates that carry the concurrency
// rules (a guarded UPDATE that matches no row returns [ErrNotFound] and the
// caller decides what that means). Every method that depends on the current
// time takes it as a parameter, so behaviour around expiry and rate windows is
// testable without a clock.
type Store struct {
	// pool is nil for a Store bound to a transaction, which is how [Store.Tx]
	// recognises that it is already inside one.
	pool *pgxpool.Pool
	q    Querier

	Users         *Users
	Sessions      *Sessions
	Services      *Services
	Devices       *Devices
	APITokens     *APITokens
	DeviceAuth    *DeviceAuthorizations
	Events        *Events
	Notifications *Notifications
	Interactions  *Interactions
	Activities    *Activities
	Deliveries    *Deliveries
	Operations    *Operations
	Attempts      *Attempts
	Feed          *Feed
}

// New returns a Store backed by pool.
func New(pool *pgxpool.Pool) *Store {
	s := newStore(pool)
	s.pool = pool
	return s
}

func newStore(q Querier) *Store {
	s := &Store{
		q:             q,
		Users:         &Users{q},
		Sessions:      &Sessions{q},
		Services:      &Services{q},
		Devices:       &Devices{q},
		APITokens:     &APITokens{q},
		DeviceAuth:    &DeviceAuthorizations{q},
		Events:        &Events{q},
		Notifications: &Notifications{q},
		Interactions:  &Interactions{q},
		Operations:    &Operations{q},
		Attempts:      &Attempts{q},
	}
	// The stores whose operations span more than one table keep a handle on
	// the owning Store. Reaching back is what lets Activities.Start behave
	// identically whether it is called on its own or from inside a larger unit
	// of work, and what lets the feed delete route to the table its composite
	// id names.
	s.Activities = &Activities{q: q, store: s}
	s.Deliveries = &Deliveries{q: q, store: s}
	s.Feed = &Feed{q: q, store: s}
	return s
}

// Tx runs fn inside a database transaction, committing when it returns nil and
// rolling back on any error or panic. The Store handed to fn is bound to the
// transaction; the receiver is not, so a query issued on the outer Store from
// inside fn runs outside the transaction.
//
// Calling Tx on a Store that is already bound to a transaction runs fn inline
// on the same transaction rather than opening a nested one, so a composite
// helper can be reused both standalone and as part of a larger unit of work.
func (s *Store) Tx(ctx context.Context, fn func(context.Context, *Store) error) error {
	if s.pool == nil {
		return fn(ctx, s)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("db: begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()

	if err := fn(ctx, newStore(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("db: commit transaction: %w", err)
	}
	committed = true
	return nil
}

// Errors returned by every store. Callers branch on these with errors.Is.
var (
	// ErrNotFound reports that a query matched no row. Guarded updates return
	// it too: "no row matched" and "the row is no longer in the state this
	// update requires" are the same outcome at the SQL level, and the caller
	// re-reads to tell them apart.
	ErrNotFound = errors.New("db: no matching row")

	// ErrConflict reports a unique-constraint violation. Use
	// [IsUniqueViolation] when the specific constraint decides the response,
	// which it does for every idempotency race.
	ErrConflict = errors.New("db: unique constraint violated")
)

// IsUniqueViolation reports whether err is a unique-constraint violation. With
// one or more constraint names it additionally requires the violated
// constraint to be one of them, which is how an idempotency-key race is told
// apart from, say, a duplicate Live Activity key.
func IsUniqueViolation(err error, constraints ...string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != uniqueViolationCode {
		return false
	}
	if len(constraints) == 0 {
		return true
	}
	for _, name := range constraints {
		if pgErr.ConstraintName == name {
			return true
		}
	}
	return false
}

// ConstraintName returns the constraint a violation names, or "" when err is
// not a constraint violation.
func ConstraintName(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.ConstraintName
	}
	return ""
}

const (
	uniqueViolationCode = "23505"
	checkViolationCode  = "23514"
)

// IsCheckViolation reports whether err is a CHECK-constraint violation, which
// in this schema means the application tried to write a value the domain rules
// forbid (an unknown enum member, a negative counter, two requesters at once).
func IsCheckViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == checkViolationCode
}

// Millis truncates t to millisecond resolution in UTC.
//
// Every timestamp the store writes goes through it: the API exposes
// milliseconds, HMACs are computed over millisecond values, and optimistic
// concurrency compares stored timestamps for equality. Writing microseconds
// would make a value that round-trips through a client stop matching the row
// it came from.
func Millis(t time.Time) time.Time { return t.UTC().Truncate(time.Millisecond) }

func millisPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := Millis(*t)
	return &v
}

// Set is an optional value for a partial update: the zero Set leaves a column
// alone, while a Set built with [Value] writes it. Set[*string] therefore
// distinguishes the three cases a PATCH needs — absent, set to a value, and
// explicitly cleared to NULL.
type Set[T any] struct {
	value T
	set   bool
}

// Value marks v as present.
func Value[T any](v T) Set[T] { return Set[T]{value: v, set: true} }

// Get returns the value and whether it was set.
func (s Set[T]) Get() (T, bool) { return s.value, s.set }

// IsSet reports whether the field participates in the update.
func (s Set[T]) IsSet() bool { return s.set }

// args expands to the (present, value) pair the CASE WHEN ... THEN ... ELSE
// column END statements take.
func (s Set[T]) args() (bool, T) { return s.set, s.value }

// queryOne runs a query expected to produce exactly one row.
func queryOne[T any](ctx context.Context, q Querier, op, sql string, args ...any) (*T, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("db: %s: %w", op, err)
	}
	row, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByName[T])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("db: %s: %w", op, ErrNotFound)
		}
		return nil, fmt.Errorf("db: %s: %w", op, err)
	}
	return row, nil
}

// queryAll runs a query and collects every row.
func queryAll[T any](ctx context.Context, q Querier, op, sql string, args ...any) ([]T, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("db: %s: %w", op, err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByName[T])
	if err != nil {
		return nil, fmt.Errorf("db: %s: %w", op, err)
	}
	return out, nil
}

// queryValue runs a query returning a single scalar.
func queryValue[T any](ctx context.Context, q Querier, op, sql string, args ...any) (T, error) {
	var v T
	if err := q.QueryRow(ctx, sql, args...).Scan(&v); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return v, fmt.Errorf("db: %s: %w", op, ErrNotFound)
		}
		return v, fmt.Errorf("db: %s: %w", op, err)
	}
	return v, nil
}

// execAffected runs a statement and reports how many rows it touched.
func execAffected(ctx context.Context, q Querier, op, sql string, args ...any) (int64, error) {
	tag, err := q.Exec(ctx, sql, args...)
	if err != nil {
		return 0, fmt.Errorf("db: %s: %w", op, err)
	}
	return tag.RowsAffected(), nil
}

// execOne runs a guarded statement that must touch exactly one row, returning
// [ErrNotFound] when the guard did not match.
func execOne(ctx context.Context, q Querier, op, sql string, args ...any) error {
	n, err := execAffected(ctx, q, op, sql, args...)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("db: %s: %w", op, ErrNotFound)
	}
	return nil
}

// execMatched runs a guarded statement and reports whether it matched, for the
// callers that treat "did not match" as an ordinary outcome rather than an
// error — a welcome notification already claimed, a token already revoked.
func execMatched(ctx context.Context, q Querier, op, sql string, args ...any) (bool, error) {
	n, err := execAffected(ctx, q, op, sql, args...)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

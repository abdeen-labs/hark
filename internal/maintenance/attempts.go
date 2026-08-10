// Package maintenance runs the background chores the data model expects but no
// request ever performs.
//
// Its one worker prunes the APNs delivery-attempt audit trail. Every push
// appends an attempt row and nothing reads one back at request time, so the
// store's DeleteBefore is a promise the server has to keep on a schedule or the
// table becomes the largest in the database with no benefit.
package maintenance

import (
	"context"
	"log/slog"
	"time"
)

// Interval is how often the pruner runs after its pass at startup. Attempts
// accrue one row per push, so daily keeps the table small without the delete
// ever being large enough to need batching.
const Interval = 24 * time.Hour

// attemptDeleter is the one store capability the pruner needs. *db.Attempts
// satisfies it; tests substitute a fake.
type attemptDeleter interface {
	DeleteBefore(context.Context, time.Time) (int64, error)
}

// Options are the pruner's dependencies.
type Options struct {
	// Attempts deletes attempt rows older than a cutoff. Required.
	Attempts attemptDeleter
	// RetentionDays is how many whole days of rows to keep. Required and
	// positive; configuration guarantees 1-3650.
	RetentionDays int
	// Logger receives outcomes. Nil discards them.
	Logger *slog.Logger
	// Interval overrides the production tick, for tests. Zero or negative uses
	// [Interval].
	Interval time.Duration
	// Now is the clock, for tests. Nil uses time.Now.
	Now func() time.Time
}

// AttemptPruner deletes APNs delivery-attempt rows older than the retention
// window, once at startup and then daily.
type AttemptPruner struct {
	attempts  attemptDeleter
	retention time.Duration
	log       *slog.Logger
	interval  time.Duration
	now       func() time.Time
}

// NewAttemptPruner builds a pruner. It panics on a missing or non-positive
// requirement, which is a wiring mistake in main rather than anything a running
// server can recover from.
func NewAttemptPruner(opts Options) *AttemptPruner {
	if opts.Attempts == nil {
		panic("maintenance: Options.Attempts is required")
	}
	if opts.RetentionDays <= 0 {
		panic("maintenance: Options.RetentionDays must be positive")
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Interval <= 0 {
		opts.Interval = Interval
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &AttemptPruner{
		attempts: opts.Attempts,
		// Converted from days exactly once, here. A retention day is a fixed
		// 24 hours: the cutoff is a sliding window over UTC instants, not a
		// calendar boundary.
		retention: time.Duration(opts.RetentionDays) * 24 * time.Hour,
		log:       opts.Logger,
		interval:  opts.Interval,
		now:       opts.Now,
	}
}

// Run prunes once immediately, then once per interval until ctx is canceled.
//
// Only this goroutine touches the store, so passes never overlap themselves: a
// deletion still in flight delays the next tick rather than racing it, and a
// tick that fires meanwhile is simply dropped. An error is logged and waits for
// the next interval; a replica running the same loop is safe because the delete
// is strict and idempotent.
func (p *AttemptPruner) Run(ctx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		_, _ = p.RunOnce(ctx)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// RunOnce performs one pruning pass and reports what it deleted. Run ignores
// the results; they exist so tests can drive passes deterministically.
func (p *AttemptPruner) RunOnce(ctx context.Context) (int64, error) {
	cutoff := p.now().Add(-p.retention)
	deleted, err := p.attempts.DeleteBefore(ctx, cutoff)
	if err != nil {
		// A canceled pass is shutdown, not a failure worth a log line. Anything
		// else — most plausibly an unreachable database — is reported and
		// retried on the next interval; the error carries no secrets because
		// the store never puts credentials in one.
		if ctx.Err() == nil {
			p.log.ErrorContext(ctx, "pruning APNs delivery attempts failed", "error", err)
		}
		return 0, err
	}
	if deleted > 0 {
		p.log.InfoContext(ctx, "pruned APNs delivery attempts",
			"deleted", deleted, "cutoff", cutoff)
	}
	return deleted, nil
}

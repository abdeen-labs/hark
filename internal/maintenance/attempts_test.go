package maintenance

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// deleterFunc adapts a closure to the store capability the pruner needs.
type deleterFunc func(context.Context, time.Time) (int64, error)

func (f deleterFunc) DeleteBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return f(ctx, cutoff)
}

// TestRunOncePrunesExactlyRetentionAgo pins the cutoff arithmetic: the pass
// deletes everything strictly older than now minus the configured days, to the
// nanosecond.
func TestRunOncePrunesExactlyRetentionAgo(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	var got time.Time
	p := NewAttemptPruner(Options{
		Attempts: deleterFunc(func(_ context.Context, cutoff time.Time) (int64, error) {
			got = cutoff
			return 3, nil
		}),
		RetentionDays: 30,
		Now:           func() time.Time { return now },
	})

	deleted, err := p.RunOnce(t.Context())
	if err != nil || deleted != 3 {
		t.Fatalf("RunOnce = (%d, %v), want (3, nil)", deleted, err)
	}
	if want := now.Add(-30 * 24 * time.Hour); !got.Equal(want) {
		t.Errorf("cutoff = %s, want %s", got, want)
	}
}

// TestRunOnceLogsOnlyWhenRowsWereRemoved keeps the quiet days quiet: an empty
// pass says nothing, a pass that deleted rows reports the count and cutoff.
func TestRunOnceLogsOnlyWhenRowsWereRemoved(t *testing.T) {
	var mu sync.Mutex
	var buf strings.Builder
	log := slog.New(slog.NewTextHandler(lockedWriter{&mu, &buf}, nil))

	var deleted atomic.Int64
	p := NewAttemptPruner(Options{
		Attempts: deleterFunc(func(context.Context, time.Time) (int64, error) {
			return deleted.Load(), nil
		}),
		RetentionDays: 1,
		Logger:        log,
	})

	if _, err := p.RunOnce(t.Context()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	mu.Lock()
	if out := buf.String(); out != "" {
		t.Errorf("an empty pass logged:\n%s", out)
	}
	mu.Unlock()

	deleted.Store(7)
	if _, err := p.RunOnce(t.Context()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	mu.Lock()
	out := buf.String()
	mu.Unlock()
	if !strings.Contains(out, "deleted=7") || !strings.Contains(out, "cutoff=") {
		t.Errorf("a deleting pass did not report count and cutoff:\n%s", out)
	}
}

// TestRunPrunesImmediatelyAndStopsOnCancel covers both ends of the lifecycle:
// the first pass happens on entry rather than a day later, and cancellation
// returns promptly while the ticker still has an hour to go.
func TestRunPrunesImmediatelyAndStopsOnCancel(t *testing.T) {
	ran := make(chan struct{}, 1)
	p := NewAttemptPruner(Options{
		Attempts: deleterFunc(func(context.Context, time.Time) (int64, error) {
			select {
			case ran <- struct{}{}:
			default:
			}
			return 0, nil
		}),
		RetentionDays: 1,
		Interval:      time.Hour,
	})

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()

	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("Run never pruned on startup")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop on cancellation")
	}
}

// TestRunStopsWhileADeletionIsInFlight cancels mid-delete: the pass unblocks
// through its context, and the loop exits instead of waiting out the tick.
func TestRunStopsWhileADeletionIsInFlight(t *testing.T) {
	entered := make(chan struct{})
	p := NewAttemptPruner(Options{
		Attempts: deleterFunc(func(ctx context.Context, _ time.Time) (int64, error) {
			close(entered) // the hour-long interval guarantees a single call
			<-ctx.Done()
			return 0, ctx.Err()
		}),
		RetentionDays: 1,
		Interval:      time.Hour,
	})

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the deletion never started")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop while a deletion was blocked")
	}
}

// TestRunRetriesAfterAnError checks that a failed pass is a log line and a
// wait, not the end of the worker: the next tick prunes again.
func TestRunRetriesAfterAnError(t *testing.T) {
	var calls atomic.Int64
	outcomes := make(chan error)
	p := NewAttemptPruner(Options{
		Attempts: deleterFunc(func(ctx context.Context, _ time.Time) (int64, error) {
			var err error
			if calls.Add(1) == 1 {
				err = errors.New("the database is down")
			}
			select {
			case outcomes <- err:
			case <-ctx.Done():
			}
			if err != nil {
				return 0, err
			}
			return 1, nil
		}),
		RetentionDays: 1,
		Interval:      time.Millisecond,
	})

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()

	select {
	case err := <-outcomes:
		if err == nil {
			t.Fatal("the first pass should have failed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the first pass never ran")
	}
	select {
	case err := <-outcomes:
		if err != nil {
			t.Fatalf("the pass after the error failed too: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the worker never pruned again after an error")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
}

// TestRunNeverOverlapsItself blocks one deletion across many ticks and checks
// that no second deletion starts until it returns.
func TestRunNeverOverlapsItself(t *testing.T) {
	var inFlight, calls atomic.Int64
	var overlapped atomic.Bool
	release := make(chan struct{})
	p := NewAttemptPruner(Options{
		Attempts: deleterFunc(func(ctx context.Context, _ time.Time) (int64, error) {
			if inFlight.Add(1) > 1 {
				overlapped.Store(true)
			}
			defer inFlight.Add(-1)
			calls.Add(1)
			select {
			case <-release:
			case <-ctx.Done():
			}
			return 0, nil
		}),
		RetentionDays: 1,
		Interval:      time.Millisecond,
	})

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()

	// Hold the first deletion open across dozens of ticks, then let everything
	// through and wait for a later pass to prove the loop kept going.
	time.Sleep(20 * time.Millisecond)
	close(release)
	for deadline := time.Now().Add(2 * time.Second); calls.Load() < 2 && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}

	if overlapped.Load() {
		t.Error("two deletions ran at once")
	}
	if calls.Load() < 2 {
		t.Error("the ticks behind the blocked deletion never pruned")
	}
}

// TestTwoWorkersRepeatTheSameCutoffSafely runs two independent pruners over the
// same clock: both compute the identical cutoff and both passes succeed. The
// database test owns the row semantics that make the repeat a no-op.
func TestTwoWorkersRepeatTheSameCutoffSafely(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	cutoffs := make([]time.Time, 2)
	for i := range cutoffs {
		p := NewAttemptPruner(Options{
			Attempts: deleterFunc(func(_ context.Context, cutoff time.Time) (int64, error) {
				cutoffs[i] = cutoff
				if i == 1 {
					return 0, nil // the first replica already deleted the rows
				}
				return 5, nil
			}),
			RetentionDays: 7,
			Now:           func() time.Time { return now },
		})
		if _, err := p.RunOnce(t.Context()); err != nil {
			t.Fatalf("worker %d: RunOnce: %v", i, err)
		}
	}
	if !cutoffs[0].Equal(cutoffs[1]) {
		t.Errorf("cutoffs differ: %s vs %s", cutoffs[0], cutoffs[1])
	}
}

// lockedWriter serializes handler writes so a test can read the buffer without
// racing the goroutine that logs.
type lockedWriter struct {
	mu *sync.Mutex
	w  *strings.Builder
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

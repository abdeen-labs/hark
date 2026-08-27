// Package callbacks delivers the answer to a question back to the webhook
// caller that asked it.
//
// A webhook may ask its question with a callback URL and a bearer token
// ([`POST /hooks/{token}`] in the API contract). When the question is
// answered, the row becomes pending and this worker sends the callback.
//
// The worker owns nothing: the claim, the retry schedule and the terminal
// states all live in the store, so two processes running it deliver each answer
// once and a process that dies mid-flight loses only the lease.
package callbacks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/netpolicy"
	"github.com/abdeen-labs/hark/internal/secret"
)

const (
	// Interval is how often the worker looks for due callbacks on its own. An
	// answer normally goes out well before this: answering nudges the worker.
	Interval = 30 * time.Second
	// Concurrency is how many callbacks one pass claims, and exactly how many
	// it delivers at once. The claim is a lease, so the two are one number on
	// purpose: a pass that claimed more rows than it could start would leave
	// the rest leased and idle, and a slow batch would outlive its own lease —
	// which is how a second worker ends up delivering the same answer twice.
	// Four clears a realistic backlog quickly while keeping a slow receiver
	// from tying up more than four connections. A pass that fills every slot
	// runs again immediately.
	Concurrency = 4
	// RequestTimeout bounds one delivery attempt.
	RequestTimeout = 10 * time.Second
	// SettleTimeout bounds recording what happened to an attempt. It is named
	// separately from RequestTimeout because the two spend the same lease back
	// to back: the budget a claimed row consumes is their sum, not their max.
	SettleTimeout = 10 * time.Second
	// leaseDuration is how long a claimed row is hidden from other workers. It
	// comfortably exceeds one whole delivery — request plus settlement — so a
	// row still in flight is never handed to a second worker; a process that
	// dies holding one loses at most this long.
	leaseDuration = 60 * time.Second
	// userAgent identifies these requests in a receiver's access log.
	userAgent = "Hark-Callbacks/1"
	// maxDrainBytes bounds what is read from a response before the connection
	// is returned to the pool. Nothing in the body is used; a receiver that
	// answers with a megabyte is not worth reading it for.
	maxDrainBytes = 64 << 10
	// timestampLayout is the API's canonical timestamp rendering — RFC 3339,
	// UTC, three fractional digits — because a callback body is Hark JSON like
	// any other. It is spelled out here rather than imported: the worker has no
	// business depending on the HTTP layer.
	timestampLayout = "2006-01-02T15:04:05.000Z"
)

// The lease inequality, enforced at compile time: a claimed row must finish
// one request and the settle that records it strictly inside the lease, with
// room to spare, or a slow-but-alive worker would watch a replica reclaim rows
// it is still delivering. This conversion refuses to compile unless the lease
// is more than double the worst-case delivery path — the doubling is the
// margin. Anyone changing one of these constants changes this line's mind too.
const _ = uint64(leaseDuration - 2*(RequestTimeout+SettleTimeout) - 1)

// backoff is the delay before attempt n+1, indexed by the attempt that just
// failed. After these retries, the callback is marked permanently failed.
var backoff = []time.Duration{
	30 * time.Second,
	2 * time.Minute,
	10 * time.Minute,
	time.Hour,
}

// Options are the worker's dependencies.
type Options struct {
	// Store reads and settles the callback rows. Required.
	Store *db.Store
	// Secrets decrypts the bearer token the caller supplied. Required.
	Secrets *secret.Keeper
	// Logger receives delivery outcomes. Nil discards them.
	Logger *slog.Logger
	// Client sends the requests. Nil builds the production client: it refuses
	// redirects, ignores proxy environment variables, and opens sockets only
	// to public addresses approved by [netpolicy] at dial time. A non-nil
	// Client is a trusted override — tests use one to reach loopback
	// receivers — and bypasses that dial policy entirely.
	Client *http.Client
	// Now is the clock, for tests. Nil uses time.Now. Deliveries run
	// concurrently, so it is called from more than one goroutine.
	Now func() time.Time
}

// Worker delivers answers to the callers that asked for them.
type Worker struct {
	store   *db.Store
	secrets *secret.Keeper
	log     *slog.Logger
	client  *http.Client
	now     func() time.Time

	// The timing knobs live on the worker rather than being read straight from
	// the constants so package tests can shrink them deterministically. New
	// fills them from the constants; production has no other constructor.
	//
	// claimLimit is both how many rows one pass claims and how many deliveries
	// it runs at once — never split those meanings. A claim is a lease, and a
	// row claimed into a queue burns its lease standing still.
	claimLimit int
	// requestTimeout bounds one outbound request and settleTimeout bounds
	// recording its outcome. Their sum is the lease budget one row spends, so
	// lease must stay strictly larger with margin — the compile-time check on
	// the constants pins that for production values.
	requestTimeout time.Duration
	settleTimeout  time.Duration
	lease          time.Duration

	// nudge carries at most one pending wake-up: a burst of answers is one
	// extra pass, not one pass each.
	nudge chan struct{}
}

// New builds a worker. It panics on a missing dependency, which is a wiring
// mistake in main rather than anything a running server can recover from.
func New(opts Options) *Worker {
	if opts.Store == nil {
		panic("callbacks: Options.Store is required")
	}
	if opts.Secrets == nil {
		panic("callbacks: Options.Secrets is required")
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Client == nil {
		// The default transport's pooling and timeouts are kept by cloning it —
		// never by mutating the shared global — and then two things change.
		// Proxy goes: an environment proxy would resolve and connect to the
		// target itself, outside the dial policy below. And every socket is
		// opened by the netpolicy dialer, which resolves the hostname once,
		// requires every address in the answer to be public, and dials one of
		// those vetted literals. The TLS handshake still verifies the
		// still verifies the certificate against the URL's hostname, even
		// though the socket underneath was dialed by IP.
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		transport.DialContext = (&netpolicy.Dialer{}).DialContext
		opts.Client = &http.Client{
			Timeout:   RequestTimeout,
			Transport: transport,
			// A redirect is not followed: the bearer token is the receiver's own
			// credential, and a 302 to somewhere else would hand it over.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return &Worker{
		store:          opts.Store,
		secrets:        opts.Secrets,
		log:            opts.Logger,
		client:         opts.Client,
		now:            opts.Now,
		claimLimit:     Concurrency,
		requestTimeout: RequestTimeout,
		settleTimeout:  SettleTimeout,
		lease:          leaseDuration,
		nudge:          make(chan struct{}, 1),
	}
}

// Nudge asks for a pass as soon as possible. It never blocks: a wake-up is
// already pending or one is not needed.
func (w *Worker) Nudge() {
	select {
	case w.nudge <- struct{}{}:
	default:
	}
}

// Run delivers due callbacks until ctx is canceled.
//
// It sweeps once on entry, then on every tick and every nudge. Only this
// goroutine claims, so passes cannot overlap with themselves; the concurrency
// inside a pass belongs to RunOnce, which waits for every delivery it starts.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(Interval)
	defer ticker.Stop()

	for {
		w.drain(ctx)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-w.nudge:
		}
	}
}

// drain runs passes until one comes back short, so a backlog larger than one
// pass does not have to wait a tick per pass.
func (w *Worker) drain(ctx context.Context) {
	for ctx.Err() == nil {
		delivered, err := w.RunOnce(ctx)
		if err != nil {
			// A database that is unreachable is a condition to report and retry
			// on the next tick, not to spin on.
			w.log.ErrorContext(ctx, "claiming due callbacks failed", "error", err)
			return
		}
		// A short pass means the backlog is gone. The max guards a zeroed-out
		// test limit: a worker with no slots must idle here, not spin.
		if delivered < max(w.claimLimit, 1) {
			return
		}
	}
}

// RunOnce claims at most one pass's worth of due callbacks and delivers them
// concurrently, reporting how many deliveries it started. The claim is sized
// to the slots that can start right now, so nothing it returns ever waits
// leased behind a busy slot. It returns only claim errors; each delivery
// records its own outcome, and RunOnce does not return until every delivery
// it started has settled.
func (w *Worker) RunOnce(ctx context.Context) (int, error) {
	if w.claimLimit < 1 {
		// No slots means nothing to claim. Returning zero rather than asking
		// the store for a zero-row batch keeps a misconfigured worker inert
		// instead of erroring or spinning.
		return 0, nil
	}
	due, err := w.store.Interactions.ClaimDueCallbacks(ctx, w.now(), w.claimLimit, w.lease)
	if err != nil {
		return 0, fmt.Errorf("callbacks: claim: %w", err)
	}
	var wg sync.WaitGroup
	started := 0
	for _, in := range due {
		if ctx.Err() != nil {
			// Claimed work that did not start keeps its lease for a later pass.
			// Do not count it as handled during this canceled pass.
			break
		}
		started++
		wg.Go(func() { w.deliverOne(ctx, in) })
	}
	wg.Wait()
	return started, nil
}

// deliverOne posts one answer and records what happened to it.
func (w *Worker) deliverOne(ctx context.Context, in db.Interaction) {
	attempts := in.CallbackAttempts + 1
	err := w.post(ctx, in)

	settle := db.SettleCallbackParams{ID: in.ID, Attempts: attempts, Now: w.now()}
	switch {
	case err == nil:
		settle.Status = db.CallbackDelivered
	case attempts <= len(backoff):
		next := w.now().Add(backoff[attempts-1])
		settle.Status = db.CallbackRetrying
		settle.NextAttemptAt = &next
		settle.LastError = ptr(err.Error())
	default:
		settle.Status = db.CallbackFailed
		settle.LastError = ptr(err.Error())
	}

	// The settle runs on a context of its own: the request has already been
	// made, and losing the record of it to a shutdown would deliver it twice.
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), w.settleTimeout)
	defer cancel()
	if _, err := w.store.Interactions.SettleCallback(settleCtx, settle); err != nil {
		w.log.ErrorContext(ctx, "recording a callback attempt failed",
			"interaction_id", in.ID, "error", err)
		return
	}

	switch settle.Status {
	case db.CallbackDelivered:
		w.log.InfoContext(ctx, "callback delivered",
			"interaction_id", in.ID, "attempts", attempts)
	case db.CallbackFailed:
		w.log.WarnContext(ctx, "callback permanently failed",
			"interaction_id", in.ID, "attempts", attempts, "error", err)
	default:
		w.log.InfoContext(ctx, "callback attempt failed, will retry",
			"interaction_id", in.ID, "attempts", attempts, "error", err)
	}
}

// payload is what a receiver is sent. It is the answer and enough context to
// match it to the work that asked for it — never the prompt, the account or
// anything about the phone that answered.
type payload struct {
	Type          string  `json:"type"`
	InteractionID string  `json:"interaction_id"`
	EventID       *string `json:"event_id"`
	CorrelationID *string `json:"correlation_id"`
	Kind          string  `json:"kind"`
	Status        string  `json:"status"`
	// Action is the choice that was taken, or "reply" for a free-text answer.
	Action string `json:"action"`
	// Text is the reply body, and null for every other kind.
	Text        *string `json:"text"`
	RespondedAt *string `json:"responded_at"`
}

// eventType names what happened, in the vocabulary of the API this belongs to.
const eventType = "interaction.answered"

// post delivers one callback, returning nil only for a 2xx.
func (w *Worker) post(ctx context.Context, in db.Interaction) error {
	if in.CallbackURL == nil || in.CallbackTokenCiphertext == nil {
		// ClaimDueCallbacks filters these out; a row that gets here anyway is
		// unsendable and must not be retried forever.
		return errors.New("callback is not configured")
	}
	token, err := w.secrets.Decrypt(secret.PurposeCallbackToken, *in.CallbackTokenCiphertext)
	if err != nil {
		// The cause is not wrapped into the stored error: it is the owner who
		// reads that, and a decryption failure means the root key changed, not
		// anything the receiver can act on.
		return errors.New("the stored callback token could not be decrypted")
	}

	body, err := json.Marshal(newPayload(in))
	if err != nil {
		return fmt.Errorf("building the callback body failed: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, w.requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, *in.CallbackURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("the callback URL is not usable: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("the request failed: %w", err)
	}
	defer func() {
		// Drained before closing so the connection can be reused: a receiver
		// that answers every callback keeps one connection rather than one per
		// answer.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrainBytes))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("the receiver answered %s", resp.Status)
	}
	return nil
}

func newPayload(in db.Interaction) payload {
	p := payload{
		Type:          eventType,
		InteractionID: in.ID,
		EventID:       in.EventID,
		CorrelationID: in.CorrelationID,
		Kind:          in.Kind,
		Status:        in.Status,
		Action:        deref(in.Response),
	}
	if in.Kind == db.InteractionReply {
		// A reply's response is the text the person typed, so the two fields say
		// different things: what was done, and what was said.
		p.Action = db.InteractionReply
		p.Text = in.Response
	}
	if in.RespondedAt != nil {
		p.RespondedAt = ptr(in.RespondedAt.UTC().Format(timestampLayout))
	}
	return p
}

func ptr[T any](v T) *T { return &v }

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

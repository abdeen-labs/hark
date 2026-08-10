package callbacks

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/id"
	"github.com/abdeen-labs/hark/internal/secret"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests run the worker against a real PostgreSQL, because the claim is a
// lease taken with FOR UPDATE SKIP LOCKED and the retry schedule is a stored
// column — neither survives being faked. The receiver is an httptest server,
// which is the one dependency that is genuinely external.
//
//	TEST_DATABASE_URL=postgres://hark:hark@localhost:5432/hark_test go test ./internal/callbacks
//
// The schema is this package's own: `go test ./...` runs packages in parallel,
// and two of them resetting one schema means each keeps pulling the tables out
// from under the other.

const testSchema = "hark_callbacks_test"

// testTables is every table these tests write, for the per-test reset.
const testTables = `users, services, events, interactions`

var (
	schemaOnce sync.Once
	schemaPool *pgxpool.Pool
	schemaErr  error
)

func requireStore(t *testing.T) (context.Context, *db.Store) {
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

	schemaOnce.Do(func() {
		pool, err := db.Open(context.WithoutCancel(ctx), db.Config{
			URL: withSearchPath(dsn, testSchema), MaxConns: 4, ConnectTimeout: 10 * time.Second,
		})
		if err != nil {
			schemaErr = err
			return
		}
		// Drop rather than trust whatever the last run left behind: the
		// migration ledger and the schema have to agree.
		if _, err := pool.Exec(ctx,
			"DROP SCHEMA IF EXISTS "+testSchema+" CASCADE; CREATE SCHEMA "+testSchema); err != nil {
			schemaErr = err
			return
		}
		if err := db.Migrate(ctx, pool, db.Migrations(), slog.New(slog.DiscardHandler)); err != nil {
			schemaErr = err
			return
		}
		schemaPool = pool
	})
	if schemaErr != nil {
		t.Fatalf("prepare test schema: %v", schemaErr)
	}
	if _, err := schemaPool.Exec(ctx, "TRUNCATE "+testTables+" RESTART IDENTITY CASCADE"); err != nil {
		t.Fatalf("reset tables: %v", err)
	}
	return ctx, db.New(schemaPool)
}

// withSearchPath points a DSN at a dedicated schema. pgx forwards unrecognised
// query parameters as connection runtime parameters, which is what search_path
// is.
func withSearchPath(raw, schema string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

var testKeeper = secret.NewKeeper([]byte("callback-test-root-key-long-enough"))

// fixture is the single account and service the worker tests share. It is
// split from the questions themselves because Hark is single-user — CreateFirst
// refuses a second account — while the concurrency tests need several answered
// questions under the one that exists.
type fixture struct {
	user    db.User
	service db.Service
}

func newFixture(ctx context.Context, t *testing.T, s *db.Store) *fixture {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)

	user, err := s.Users.CreateFirst(ctx, db.CreateUserParams{
		ID: id.New(), Username: "owner", Email: "owner@hark.local",
		DisplayName: "Owner", Now: now,
	})
	if err != nil {
		t.Fatalf("create the account: %v", err)
	}
	service, err := s.Services.Create(ctx, db.CreateServiceParams{
		ID: id.New(), UserID: user.ID, Title: "CI",
		Priority: db.PriorityNormal, TokenHash: "hash", TokenCiphertext: "sealed", Now: now,
	})
	if err != nil {
		t.Fatalf("create the service: %v", err)
	}
	return &fixture{user: *user, service: *service}
}

// answered creates a question that asked to be told the answer, and answers
// it — which is the state the worker picks rows up in. Each call makes its own
// event, because an event carries at most one interaction.
func (f *fixture) answered(ctx context.Context, t *testing.T, s *db.Store, callbackURL, token string) db.Interaction {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)

	event, err := s.Events.Create(ctx, db.CreateEventParams{
		ID: id.New(), ServiceID: f.service.ID, Title: "CI", Body: "Deploy?",
		Priority: db.PriorityNormal, Status: db.EventAccepted, Now: now,
	})
	if err != nil {
		t.Fatalf("create the event: %v", err)
	}

	sealed, err := testKeeper.Encrypt(secret.PurposeCallbackToken, token)
	if err != nil {
		t.Fatalf("seal the callback token: %v", err)
	}
	in, err := s.Interactions.Create(ctx, db.CreateInteractionParams{
		ID: id.New(), UserID: f.user.ID, RequesterServiceID: &f.service.ID, EventID: &event.ID,
		Title: "CI", Prompt: "Deploy to production?", Kind: db.InteractionApproval,
		Presentation: db.PresentationNotification, Choices: db.ChoicesFor(db.InteractionApproval),
		CorrelationID: ptr("deploy-4821"), ActionDigest: "digest",
		CallbackURL: &callbackURL, CallbackTokenCiphertext: &sealed,
		ExpiresAt: now.Add(time.Hour), Now: now,
	})
	if err != nil {
		t.Fatalf("ask the question: %v", err)
	}

	answered, err := s.Interactions.Respond(ctx, db.RespondParams{
		ID: in.ID, UserID: f.user.ID, Status: db.InteractionApproved,
		Response: ptr("approve"), TriggerCallback: true, Now: now,
	})
	if err != nil {
		t.Fatalf("answer the question: %v", err)
	}
	return *answered
}

// answeredWithCallback is the single-question shorthand the original tests use.
func answeredWithCallback(ctx context.Context, t *testing.T, s *db.Store, callbackURL, token string) db.Interaction {
	t.Helper()
	return newFixture(ctx, t, s).answered(ctx, t, s, callbackURL, token)
}

func newWorker(s *db.Store, client *http.Client) *Worker {
	return New(Options{Store: s, Secrets: testKeeper, Client: client})
}

// testWorker builds a worker with an explicit slot count and short guard
// timeouts. The guards never elapse in a passing run — the tests coordinate
// with channels, not clocks — they only stop a broken run from taking the full
// production ten seconds to fail. The lease stays a full minute on purpose:
// these tests hold requests open, and the lease existing to protect held work
// is exactly what they are checking.
func testWorker(s *db.Store, client *http.Client, slots int) *Worker {
	w := New(Options{Store: s, Secrets: testKeeper, Client: client})
	w.claimLimit = slots
	w.requestTimeout = 2 * time.Second
	w.settleTimeout = 2 * time.Second
	w.lease = time.Minute
	return w
}

// runResult carries a RunOnce return value out of a goroutine.
type runResult struct {
	n   int
	err error
}

// TestConcurrentDeliveriesAreBounded pins both halves of the slot invariant:
// one pass starts exactly claimLimit deliveries at once — never more — and the
// rows beyond the limit were never claimed at all, so the next pass picks them
// up rather than finding them leased and idle.
func TestConcurrentDeliveriesAreBounded(t *testing.T) {
	ctx, store := requireStore(t)

	const slots = 2
	const rows = 3

	var (
		mu       sync.Mutex
		inflight int
		peak     int
		hits     int
	)
	started := make(chan struct{}, rows)
	release := make(chan struct{})
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Drained so a failure path that cancels these requests is noticed by
		// the server and the handler's context escape below actually fires.
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		inflight++
		hits++
		if inflight > peak {
			peak = inflight
		}
		mu.Unlock()
		started <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
		}
		mu.Lock()
		inflight--
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()

	fx := newFixture(ctx, t, store)
	created := make([]db.Interaction, 0, rows)
	for range rows {
		created = append(created, fx.answered(ctx, t, store, receiver.URL, "a-secret-bearer-token"))
	}

	worker := testWorker(store, receiver.Client(), slots)

	first := make(chan runResult, 1)
	go func() {
		n, err := worker.RunOnce(ctx)
		first <- runResult{n, err}
	}()

	// Exactly `slots` deliveries begin...
	for i := range slots {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatalf("delivery %d never started", i+1)
		}
	}
	// ...and none beyond them while both slots are held. The quiet window is
	// the "never more" assertion: a third request here would mean a row was
	// claimed with nowhere to run.
	select {
	case <-started:
		t.Fatal("a delivery beyond the slot count started before release")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	select {
	case r := <-first:
		if r.err != nil || r.n != slots {
			t.Fatalf("first pass = (%d, %v), want %d deliveries started", r.n, r.err, slots)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunOnce did not return after its requests were released")
	}

	// The remainder was never claimed, so the next pass takes it immediately.
	if n, err := worker.RunOnce(ctx); err != nil || n != rows-slots {
		t.Fatalf("second pass = (%d, %v), want the remaining %d", n, err, rows-slots)
	}
	if n, err := worker.RunOnce(ctx); err != nil || n != 0 {
		t.Fatalf("third pass = (%d, %v), want an empty queue", n, err)
	}

	for _, in := range created {
		stored, err := store.Interactions.ByID(ctx, in.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.CallbackStatus == nil || *stored.CallbackStatus != db.CallbackDelivered {
			t.Errorf("interaction %s = %v, want delivered", in.ID, stored.CallbackStatus)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if peak != slots {
		t.Errorf("peak in-flight requests = %d, want exactly %d", peak, slots)
	}
	if hits != rows {
		t.Errorf("receiver saw %d requests, want %d", hits, rows)
	}
}

// TestLeasedRowsAreNotReclaimedByASecondWorker holds worker A's requests open —
// well inside its lease — and shows worker B a queue with nothing due. This is
// the regression the slot-sized claim exists for: work in flight is invisible
// to a replica until the lease says the holder is dead.
func TestLeasedRowsAreNotReclaimedByASecondWorker(t *testing.T) {
	ctx, store := requireStore(t)

	const rows = 2

	var (
		mu    sync.Mutex
		perID = map[string]int{}
	)
	started := make(chan struct{}, rows)
	release := make(chan struct{})
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			InteractionID string `json:"interaction_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		// Drained past the decoded value for the same close-detection reason as
		// the other held-request tests.
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		perID[body.InteractionID]++
		mu.Unlock()
		started <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()

	fx := newFixture(ctx, t, store)
	created := make([]db.Interaction, 0, rows)
	for range rows {
		created = append(created, fx.answered(ctx, t, store, receiver.URL, "a-secret-bearer-token"))
	}

	workerA := testWorker(store, receiver.Client(), rows)
	workerB := testWorker(store, receiver.Client(), rows)

	aDone := make(chan runResult, 1)
	go func() {
		n, err := workerA.RunOnce(ctx)
		aDone <- runResult{n, err}
	}()

	for i := range rows {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatalf("worker A's delivery %d never started", i+1)
		}
	}

	// A is mid-flight and its lease is live, so B has nothing to take.
	if n, err := workerB.RunOnce(ctx); err != nil || n != 0 {
		t.Fatalf("worker B = (%d, %v), want zero claims while A holds the lease", n, err)
	}

	close(release)
	select {
	case r := <-aDone:
		if r.err != nil || r.n != rows {
			t.Fatalf("worker A = (%d, %v), want %d deliveries", r.n, r.err, rows)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker A did not return after its requests were released")
	}

	for _, in := range created {
		stored, err := store.Interactions.ByID(ctx, in.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.CallbackStatus == nil || *stored.CallbackStatus != db.CallbackDelivered {
			t.Errorf("interaction %s = %v, want delivered", in.ID, stored.CallbackStatus)
		}
		if stored.CallbackAttempts != 1 {
			t.Errorf("interaction %s took %d attempts, want 1", in.ID, stored.CallbackAttempts)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	for interactionID, n := range perID {
		if n != 1 {
			t.Errorf("interaction %s was delivered %d times, want once", interactionID, n)
		}
	}
	if len(perID) != rows {
		t.Errorf("the receiver saw %d distinct interactions, want %d", len(perID), rows)
	}
}

// TestCanceledRunSettlesStartedRowsAndKeepsLeases cancels the pass while its
// requests are in flight. RunOnce must still return, and every row it started
// must be settled — the settle context outlives the cancellation on purpose —
// with a retry on the schedule rather than a row stranded mid-lease.
func TestCanceledRunSettlesStartedRowsAndKeepsLeases(t *testing.T) {
	ctx, store := requireStore(t)

	const rows = 2

	started := make(chan struct{}, rows)
	receiver := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		// Drained first: with the body unread the server never watches for the
		// client hanging up, so r.Context() would outlive the abandoned request
		// and Close would wait forever on a handler nobody is coming back for.
		_, _ = io.Copy(io.Discard, r.Body)
		started <- struct{}{}
		// Held until the canceled client abandons the request; nothing is ever
		// delivered in this test.
		<-r.Context().Done()
	}))
	defer receiver.Close()

	fx := newFixture(ctx, t, store)
	created := make([]db.Interaction, 0, rows)
	for range rows {
		created = append(created, fx.answered(ctx, t, store, receiver.URL, "a-secret-bearer-token"))
	}

	worker := testWorker(store, receiver.Client(), rows)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	res := make(chan runResult, 1)
	go func() {
		n, err := worker.RunOnce(runCtx)
		res <- runResult{n, err}
	}()

	for i := range rows {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatalf("delivery %d never started", i+1)
		}
	}
	cancel()

	var got runResult
	select {
	case got = <-res:
	case <-time.After(5 * time.Second):
		t.Fatal("RunOnce did not return after its context was canceled")
	}
	if got.err != nil || got.n != rows {
		t.Fatalf("RunOnce = (%d, %v), want every started row accounted for", got.n, got.err)
	}

	for _, in := range created {
		stored, err := store.Interactions.ByID(ctx, in.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.CallbackStatus == nil || *stored.CallbackStatus != db.CallbackRetrying {
			t.Errorf("interaction %s = %v, want retrying", in.ID, stored.CallbackStatus)
		}
		if stored.CallbackAttempts != 1 {
			t.Errorf("interaction %s recorded %d attempts, want 1", in.ID, stored.CallbackAttempts)
		}
		if stored.CallbackNextAttemptAt == nil {
			t.Errorf("interaction %s has no retry scheduled", in.ID)
		}
		if stored.CallbackLastError == nil || *stored.CallbackLastError == "" {
			t.Errorf("interaction %s recorded no failure", in.ID)
		}
	}
}

func TestCallbackIsDeliveredOnce(t *testing.T) {
	ctx, store := requireStore(t)

	type received struct {
		auth        string
		contentType string
		userAgent   string
		body        map[string]any
	}
	var (
		mu   sync.Mutex
		hits []received
	)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		hits = append(hits, received{
			auth:        r.Header.Get("Authorization"),
			contentType: r.Header.Get("Content-Type"),
			userAgent:   r.Header.Get("User-Agent"),
			body:        body,
		})
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()

	in := answeredWithCallback(ctx, t, store, receiver.URL, "a-secret-bearer-token")
	worker := newWorker(store, receiver.Client())

	if n, err := worker.RunOnce(ctx); err != nil || n != 1 {
		t.Fatalf("RunOnce = (%d, %v), want one row claimed", n, err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(hits) != 1 {
		t.Fatalf("receiver got %d requests, want 1", len(hits))
	}
	got := hits[0]
	if got.auth != "Bearer a-secret-bearer-token" {
		t.Errorf("Authorization = %q, want the caller's own token", got.auth)
	}
	if got.contentType != "application/json" {
		t.Errorf("Content-Type = %q", got.contentType)
	}
	if got.userAgent != userAgent {
		t.Errorf("User-Agent = %q, want %q", got.userAgent, userAgent)
	}
	if got.body["type"] != eventType || got.body["status"] != db.InteractionApproved ||
		got.body["action"] != "approve" || got.body["correlation_id"] != "deploy-4821" {
		t.Errorf("body = %+v", got.body)
	}
	if got.body["interaction_id"] != in.ID {
		t.Errorf("body names interaction %v, want %s", got.body["interaction_id"], in.ID)
	}
	if got.body["text"] != nil {
		t.Errorf("text = %v, want null for an approval", got.body["text"])
	}
	if _, ok := got.body["responded_at"].(string); !ok {
		t.Errorf("responded_at = %v, want a timestamp", got.body["responded_at"])
	}

	stored, err := store.Interactions.ByID(ctx, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.CallbackStatus == nil || *stored.CallbackStatus != db.CallbackDelivered {
		t.Fatalf("callback status = %v, want delivered", stored.CallbackStatus)
	}
	if stored.CallbackAttempts != 1 || stored.CallbackDeliveredAt == nil ||
		stored.CallbackNextAttemptAt != nil || stored.CallbackLastError != nil {
		t.Errorf("settled row = %+v", stored)
	}

	// A delivered row is out of the worker's query for good.
	if n, err := worker.RunOnce(ctx); err != nil || n != 0 {
		t.Fatalf("second pass = (%d, %v), want nothing left to do", n, err)
	}
}

func TestCallbackRetriesThenFails(t *testing.T) {
	ctx, store := requireStore(t)

	var attempts int
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer receiver.Close()

	in := answeredWithCallback(ctx, t, store, receiver.URL, "a-secret-bearer-token")
	worker := newWorker(store, receiver.Client())

	// Each pass is made due by hand, because the point of the schedule is that
	// the next attempt is *not* immediate.
	for want := 1; want <= len(backoff); want++ {
		if n, err := worker.RunOnce(ctx); err != nil || n != 1 {
			t.Fatalf("attempt %d: RunOnce = (%d, %v)", want, n, err)
		}
		stored, err := store.Interactions.ByID(ctx, in.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.CallbackStatus == nil || *stored.CallbackStatus != db.CallbackRetrying {
			t.Fatalf("attempt %d: status = %v, want retrying", want, stored.CallbackStatus)
		}
		if stored.CallbackAttempts != want {
			t.Errorf("attempt %d: attempts = %d", want, stored.CallbackAttempts)
		}
		if stored.CallbackLastError == nil || *stored.CallbackLastError == "" {
			t.Errorf("attempt %d: no failure was recorded", want)
		}
		if stored.CallbackNextAttemptAt == nil {
			t.Fatalf("attempt %d: no retry was scheduled", want)
		}
		// Nothing is due yet, so an immediate pass claims nothing.
		if n, err := worker.RunOnce(ctx); err != nil || n != 0 {
			t.Fatalf("attempt %d: an undue row was claimed: (%d, %v)", want, n, err)
		}
		makeDue(ctx, t, store, in.ID)
	}

	// One more failure exhausts the schedule.
	if n, err := worker.RunOnce(ctx); err != nil || n != 1 {
		t.Fatalf("final RunOnce = (%d, %v)", n, err)
	}
	stored, err := store.Interactions.ByID(ctx, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.CallbackStatus == nil || *stored.CallbackStatus != db.CallbackFailed {
		t.Fatalf("status = %v, want failed", stored.CallbackStatus)
	}
	if stored.CallbackNextAttemptAt != nil {
		t.Error("a permanently failed callback is still scheduled")
	}
	if attempts != len(backoff)+1 {
		t.Errorf("the receiver was called %d times, want %d", attempts, len(backoff)+1)
	}

	// And it is never claimed again.
	makeDue(ctx, t, store, in.ID)
	if n, err := worker.RunOnce(ctx); err != nil || n != 0 {
		t.Fatalf("a failed callback was retried: (%d, %v)", n, err)
	}
}

func TestCallbackDoesNotFollowRedirects(t *testing.T) {
	ctx, store := requireStore(t)

	var leaked bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked = true
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer elsewhere.Close()

	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusFound)
	}))
	defer receiver.Close()

	in := answeredWithCallback(ctx, t, store, receiver.URL, "a-secret-bearer-token")
	worker := New(Options{Store: store, Secrets: testKeeper})

	if _, err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if leaked {
		t.Error("the bearer token was handed to the redirect target")
	}
	stored, err := store.Interactions.ByID(ctx, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.CallbackStatus == nil || *stored.CallbackStatus != db.CallbackRetrying {
		t.Errorf("a redirect settled as %v, want a failed attempt", stored.CallbackStatus)
	}
}

// TestRunDeliversOnANudge covers the loop main actually starts: a nudge has to
// produce a delivery without waiting out the sweep interval, which is 30
// seconds and would otherwise be the latency of every answer.
func TestRunDeliversOnANudge(t *testing.T) {
	ctx, store := requireStore(t)

	delivered := make(chan struct{}, 1)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case delivered <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	in := answeredWithCallback(ctx, t, store, receiver.URL, "a-secret-bearer-token")
	worker := newWorker(store, receiver.Client())

	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.Run(runCtx)
	}()

	// The sweep on entry is enough on its own here; the nudge proves the wake-up
	// path does not block or deadlock when a pass is already under way.
	worker.Nudge()
	worker.Nudge()

	select {
	case <-delivered:
	case <-time.After(10 * time.Second):
		stop()
		t.Fatal("the worker delivered nothing within 10s")
	}

	// The receiver has been called, but the row is settled after the response is
	// read — so wait for the record rather than cancelling into it, which would
	// abort the very request that just succeeded.
	deadline := time.Now().Add(10 * time.Second)
	var status string
	for time.Now().Before(deadline) {
		stored, err := store.Interactions.ByID(ctx, in.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.CallbackStatus != nil {
			status = *stored.CallbackStatus
			if status == db.CallbackDelivered {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	stop()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after its context was canceled")
	}

	if status != db.CallbackDelivered {
		t.Errorf("callback status = %q, want %q", status, db.CallbackDelivered)
	}
}

func TestUnansweredQuestionIsNotCalledBack(t *testing.T) {
	ctx, store := requireStore(t)

	var called bool
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	now := time.Now().UTC()
	in := answeredWithCallback(ctx, t, store, receiver.URL, "a-secret-bearer-token")
	// Put it back to pending: a question nobody has answered has no answer to
	// report, however due its row looks.
	if _, err := schemaPool.Exec(ctx,
		"UPDATE interactions SET status = 'pending', responded_at = NULL, callback_next_attempt_at = $2 WHERE id = $1",
		in.ID, now); err != nil {
		t.Fatal(err)
	}

	if n, err := newWorker(store, receiver.Client()).RunOnce(ctx); err != nil || n != 0 {
		t.Fatalf("RunOnce = (%d, %v), want nothing claimed", n, err)
	}
	if called {
		t.Error("an unanswered question produced a callback")
	}
}

// makeDue brings a scheduled retry forward so a test does not have to wait for
// it.
func makeDue(ctx context.Context, t *testing.T, _ *db.Store, id string) {
	t.Helper()
	if _, err := schemaPool.Exec(ctx,
		"UPDATE interactions SET callback_next_attempt_at = now() - interval '1 second' WHERE id = $1",
		id); err != nil {
		t.Fatalf("make the retry due: %v", err)
	}
}

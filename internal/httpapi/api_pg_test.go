package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/secret"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests drive the real handler over a real PostgreSQL, because that is
// where the behaviour is: guarded updates, partial unique indexes, idempotency
// races and cascade deletes cannot be exercised against a fake store. The push
// transport is faked — it is the one dependency that is genuinely external —
// which is exactly the seam the push.Sender interface exists to provide.
//
//	TEST_DATABASE_URL=postgres://hark:hark@localhost:5432/hark_test go test ./internal/httpapi
//
// The schema is dropped and rebuilt before the first test runs, so point the URL
// at something disposable.
//
// It is a schema of this package's own — `go test ./...` runs packages in
// parallel, and two of them resetting one schema means each keeps pulling the
// tables out from under the other.

// testSchema is where these tests live. internal/db owns `public` and
// internal/auth owns its own; nothing may share.
const testSchema = "hark_api_test"

var (
	apiSchemaOnce sync.Once
	apiPool       *pgxpool.Pool
	apiSchemaErr  error
)

const apiTables = `users, sessions, services, devices, api_tokens,
	device_authorization_requests, events, agent_notifications, interactions,
	live_activities, live_activity_deliveries, live_activity_operations,
	live_activity_delivery_attempts`

// fixture is one server, one account, and the two credentials a test drives it
// with.
type fixture struct {
	t       *testing.T
	ctx     context.Context
	handler http.Handler
	store   *db.Store
	sender  *fakeSender
	nudger  *fakeNudger

	userID  string
	session string
	token   string
}

// fakeNudger stands in for the outbound callback worker, which the server only
// ever tells to get on with it.
type fakeNudger struct{ count atomic.Int64 }

func (n *fakeNudger) Nudge() { n.count.Add(1) }

type fixtureOptions struct {
	requesterRate int
	accountRate   int
}

func newFixture(t *testing.T, opts fixtureOptions) *fixture {
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

	apiSchemaOnce.Do(func() {
		pool, err := db.Open(context.WithoutCancel(ctx), db.Config{
			URL: withSearchPath(dsn, testSchema), MaxConns: 4, ConnectTimeout: 10 * time.Second,
		})
		if err != nil {
			apiSchemaErr = err
			return
		}
		// Drop rather than trust whatever the last run left behind: the
		// migration ledger and the schema have to agree.
		if _, err := pool.Exec(ctx,
			"DROP SCHEMA IF EXISTS "+testSchema+" CASCADE; CREATE SCHEMA "+testSchema); err != nil {
			apiSchemaErr = err
			return
		}
		if err := db.Migrate(ctx, pool, db.Migrations(), slog.New(slog.DiscardHandler)); err != nil {
			apiSchemaErr = err
			return
		}
		apiPool = pool
	})
	if apiSchemaErr != nil {
		t.Fatalf("prepare test schema: %v", apiSchemaErr)
	}
	if _, err := apiPool.Exec(ctx, "TRUNCATE "+apiTables+" RESTART IDENTITY CASCADE"); err != nil {
		t.Fatalf("reset tables: %v", err)
	}

	store := db.New(apiPool)
	authService := auth.New(store, nil)
	sender := newFakeSender()
	nudger := &fakeNudger{}

	f := &fixture{
		t:      t,
		ctx:    ctx,
		store:  store,
		sender: sender,
		nudger: nudger,
		handler: New(Options{
			Logger:                 slog.New(slog.NewTextHandler(io.Discard, nil)),
			DB:                     apiPool,
			Auth:                   authService,
			Store:                  store,
			Secrets:                secret.NewKeeper([]byte("test-root-key-long-enough-for-real")),
			Push:                   sender,
			PublicURL:              &url.URL{Scheme: "https", Host: "hark.example.com"},
			MaxRequestBytes:        64 << 10,
			RequesterRatePerMinute: opts.requesterRate,
			AccountRatePerMinute:   opts.accountRate,
			Version:                "test",
			Callbacks:              nudger,
		}),
	}

	user, err := authService.CreateAccount(ctx, auth.CreateAccountParams{
		Username: "owner",
		Password: "correct horse battery staple",
	})
	if err != nil {
		t.Fatalf("create the account: %v", err)
	}
	f.userID = user.ID

	_, sessionToken, err := authService.Login(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	f.session = sessionToken

	_, apiSecret, err := authService.CreateAPIToken(ctx, user.ID, auth.CreateAPITokenParams{
		Name:   "harkctl",
		Scopes: db.Scopes,
	})
	if err != nil {
		t.Fatalf("mint an API token: %v", err)
	}
	f.token = apiSecret
	return f
}

// withSearchPath points a DSN at a dedicated schema. pgx forwards unrecognised
// query parameters as connection runtime parameters, which is exactly what
// search_path is.
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

// request sends one request with the given credential ("" for none).
func (f *fixture) request(method, path, credential, body string) *httptest.ResponseRecorder {
	f.t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader).WithContext(f.ctx)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

// expect sends a request, asserts the status, and decodes the body into out.
func (f *fixture) expect(method, path, credential, body string, want int, out any) *httptest.ResponseRecorder {
	f.t.Helper()

	rec := f.request(method, path, credential, body)
	if rec.Code != want {
		f.t.Fatalf("%s %s: status = %d, want %d: %s", method, path, rec.Code, want, rec.Body)
	}
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			f.t.Fatalf("%s %s: decode: %v\n%s", method, path, err, rec.Body)
		}
	}
	return rec
}

// header sends a request carrying one extra header, which idempotency needs.
func (f *fixture) withHeader(method, path, credential, body, key, value string, want int, out any) {
	f.t.Helper()

	req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(f.ctx)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+credential)
	req.Header.Set(key, value)

	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != want {
		f.t.Fatalf("%s %s: status = %d, want %d: %s", method, path, rec.Code, want, rec.Body)
	}
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			f.t.Fatalf("%s %s: decode: %v\n%s", method, path, err, rec.Body)
		}
	}
}

// registerDevice registers a phone that can do everything.
func (f *fixture) registerDevice(apnsToken string) deviceDTO {
	f.t.Helper()

	var created deviceResponse
	f.expect(http.MethodPost, "/v1/devices", f.session, `{
		"apns_token": "`+apnsToken+`",
		"name": "Test iPhone",
		"interaction_schema_version": 1,
		"live_activity_interaction_version": 1
	}`, http.StatusCreated, &created)

	f.expect(http.MethodPut, "/v1/devices/"+created.Device.ID+"/push-to-start-token", f.session,
		`{"token":"`+strings.Repeat("ab", 32)+`","environment":"sandbox"}`, http.StatusNoContent, nil)
	f.sender.reset()
	return created.Device
}

// createService creates a webhook source and returns its ingest URL path.
func (f *fixture) createService(title string) (serviceDTO, string) {
	f.t.Helper()

	var created createdServiceResponse
	f.expect(http.MethodPost, "/v1/services", f.session,
		`{"title":"`+title+`","url":"https://example.com/app"}`, http.StatusCreated, &created)

	hook := strings.TrimPrefix(created.WebhookURL, "https://hark.example.com")
	if hook == created.WebhookURL {
		f.t.Fatalf("webhook URL is not on the public origin: %q", created.WebhookURL)
	}
	return created.Service, hook
}

// TestWebhookDeliveryRecordsAndSurfacesTheEvent walks the whole ingest path: a
// service is created, a phone registers, a webhook arrives, and the delivery
// shows up everywhere the owner looks for it.
func TestWebhookDeliveryRecordsAndSurfacesTheEvent(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	device := f.registerDevice(strings.Repeat("a1", 32))
	service, hook := f.createService("Deploy bot")

	var sent webhookNotifyResponse
	f.expect(http.MethodPost, hook, "", `{"body":"Build 4821 succeeded"}`, http.StatusCreated, &sent)

	if sent.Event.Status != db.EventAccepted || sent.Event.DeliveredCount != 1 {
		t.Fatalf("event = %+v, want one accepted delivery", sent.Event)
	}
	if sent.Response != nil {
		t.Errorf("response = %+v, want none for a plain notification", sent.Response)
	}

	// The alert inherits the service's defaults for everything the webhook left
	// out, which is the whole point of a service having them.
	alert := f.sender.lastAlert(t)
	if alert.Title != "Deploy bot" || alert.Body != "Build 4821 succeeded" {
		t.Errorf("alert = %+v, want the service title and the request body", alert)
	}
	if alert.URL == nil || *alert.URL != "https://example.com/app" {
		t.Errorf("alert URL = %v, want the service default", alert.URL)
	}
	if alert.Target.DeviceID != device.ID {
		t.Errorf("alert went to %q, want %q", alert.Target.DeviceID, device.ID)
	}

	var events eventListResponse
	f.expect(http.MethodGet, "/v1/events", f.session, "", http.StatusOK, &events)
	if len(events.Events) != 1 || events.Events[0].ServiceName != service.Title {
		t.Fatalf("events = %+v, want one from %q", events.Events, service.Title)
	}
	if events.NextCursor != nil {
		t.Errorf("next_cursor = %v, want null on a single-page list", *events.NextCursor)
	}

	var history historyListResponse
	f.expect(http.MethodGet, "/v1/history", f.session, "", http.StatusOK, &history)
	if len(history.Items) != 1 || history.Items[0].Kind != db.FeedKindNotification {
		t.Fatalf("history = %+v, want one notification", history.Items)
	}

	f.expect(http.MethodDelete, "/v1/history/"+history.Items[0].ID, f.session, "", http.StatusNoContent, nil)
	f.expect(http.MethodGet, "/v1/events", f.session, "", http.StatusOK, &events)
	if len(events.Events) != 0 {
		t.Errorf("events after deleting the history entry = %+v, want none", events.Events)
	}
}

// TestWebhookQuestionIsAnsweredWithThePushCredential covers the path that has no
// session at all: the phone answers with the one-shot credential the push
// carried, and the webhook caller polls for the answer.
func TestWebhookQuestionIsAnsweredWithThePushCredential(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	device := f.registerDevice(strings.Repeat("b2", 32))
	_, hook := f.createService("Release")

	var asked webhookNotifyResponse
	f.expect(http.MethodPost, hook, "", `{
		"body": "Deploy to production?",
		"response": {
			"kind": "approval",
			"correlation_id": "deploy-4821",
			"callback": {"url": "https://ci.example.com/hark", "token": "a-secret-bearer-token"}
		}
	}`, http.StatusCreated, &asked)

	if asked.Response == nil || asked.Response.Status != db.InteractionPending {
		t.Fatalf("response = %+v, want a pending question", asked.Response)
	}

	alert := f.sender.lastAlert(t)
	if alert.Interaction == nil {
		t.Fatal("the alert carried no question")
	}
	credential := alert.Interaction.ResponseToken
	if credential == "" || !auth.ValidResponseToken(credential) {
		t.Fatalf("response token = %q, want a well-formed credential", credential)
	}

	answer := `{"action":"approve","device_id":"` + device.ID + `","action_digest":"` +
		alert.Interaction.ActionDigest + `","response_token":"` + credential + `"}`

	var answered interactionReadResponse
	f.expect(http.MethodPost, "/v1/interactions/"+alert.Interaction.ID+"/response", "", answer,
		http.StatusOK, &answered)
	if answered.Interaction.Status != db.InteractionApproved {
		t.Fatalf("status = %q, want approved", answered.Interaction.Status)
	}

	// The question asked to be told the answer, so the outbound worker is woken
	// rather than left to find the armed row on its next sweep.
	if f.nudger.count.Load() == 0 {
		t.Error("answering a question with a callback did not wake the callback worker")
	}

	// The same answer from the same phone is the same outcome, not a conflict:
	// a notification action that is tapped twice must not report an error.
	f.expect(http.MethodPost, "/v1/interactions/"+alert.Interaction.ID+"/response", "", answer,
		http.StatusOK, nil)

	// A different answer is a real conflict.
	rec := f.request(http.MethodPost, "/v1/interactions/"+alert.Interaction.ID+"/response", "",
		strings.ReplaceAll(answer, `"approve"`, `"deny"`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("changed answer: status = %d, want 409: %s", rec.Code, rec.Body)
	}

	// And a wrong credential is indistinguishable from an unknown question.
	rec = f.request(http.MethodPost, "/v1/interactions/"+alert.Interaction.ID+"/response", "",
		strings.ReplaceAll(answer, credential, auth.NewResponseToken()))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("wrong credential: status = %d, want 404: %s", rec.Code, rec.Body)
	}

	// The webhook caller polls its own delivery for the outcome.
	var polled webhookEventResponse
	f.expect(http.MethodGet, hook+"/events/"+asked.Event.ID, "", "", http.StatusOK, &polled)
	if polled.Response == nil || polled.Response.Status != db.InteractionApproved {
		t.Fatalf("polled response = %+v, want approved", polled.Response)
	}
	if polled.Response.Action == nil || *polled.Response.Action != "approve" {
		t.Errorf("action = %v, want approve", polled.Response.Action)
	}
	if polled.Response.CorrelationID == nil || *polled.Response.CorrelationID != "deploy-4821" {
		t.Errorf("correlation_id = %v, want it echoed back", polled.Response.CorrelationID)
	}
}

// TestALockScreenQuestionFallsBackToANotification covers the phone that cannot
// draw a card: no push-to-start token, so nothing can be started on it.
//
// The question is asked anyway, as an ordinary notification. Dropping it would
// leave an agent blocked on an answer nobody was ever shown, and the plainer
// surface carries the same buttons and the same credential.
func TestALockScreenQuestionFallsBackToANotification(t *testing.T) {
	f := newFixture(t, fixtureOptions{})

	// Registered and pushable, but with no push-to-start token — which is what
	// a Live Activity needs and a notification does not.
	var created deviceResponse
	f.expect(http.MethodPost, "/v1/devices", f.session, `{
		"apns_token": "`+strings.Repeat("e5", 32)+`",
		"interaction_schema_version": 1
	}`, http.StatusCreated, &created)
	f.sender.reset()

	var asked interactionResponse
	f.expect(http.MethodPost, "/v1/interactions", f.token, `{
		"title": "deploy-bot",
		"prompt": "Deploy to production?",
		"kind": "approval",
		"presentation": "live_activity"
	}`, http.StatusCreated, &asked)

	if asked.ActivityID != nil {
		t.Fatalf("activity_id = %v, want null: no device could show a card", *asked.ActivityID)
	}
	if asked.Accepted != 1 {
		t.Fatalf("accepted = %d, want 1: the question should have gone out as a notification", asked.Accepted)
	}
	if asked.Message == nil || !strings.Contains(*asked.Message, "notification instead") {
		t.Errorf("message = %v, want it to say the question was sent as a notification", asked.Message)
	}

	// And what went out is answerable: a category-bearing alert carrying the
	// one-shot credential.
	alert := f.sender.lastAlert(t)
	if alert.Interaction == nil {
		t.Fatal("the fallback alert carried no question")
	}
	if alert.Interaction.ID != asked.Interaction.ID {
		t.Errorf("the alert asks %q, want %q", alert.Interaction.ID, asked.Interaction.ID)
	}
	if !auth.ValidResponseToken(alert.Interaction.ResponseToken) {
		t.Errorf("response token = %q, want a well-formed credential", alert.Interaction.ResponseToken)
	}

	answer := `{"action":"approve","device_id":"` + created.Device.ID +
		`","action_digest":"` + alert.Interaction.ActionDigest +
		`","response_token":"` + alert.Interaction.ResponseToken + `"}`
	var answered interactionReadResponse
	f.expect(http.MethodPost, "/v1/interactions/"+asked.Interaction.ID+"/response", "", answer,
		http.StatusOK, &answered)
	if answered.Interaction.Status != db.InteractionApproved {
		t.Fatalf("status = %q, want approved", answered.Interaction.Status)
	}
}

// TestOnlyTheOwnerMayAnswerAQuestion covers the whole authorization set of the
// answer route against a real question: the API token that asked it is refused,
// and the owner's session is not.
//
// The refusal is the point. An agent that can approve its own request has not
// asked for approval, it has announced an intention — and the token used here
// carries every scope, so nothing narrower would have saved it.
func TestOnlyTheOwnerMayAnswerAQuestion(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	device := f.registerDevice(strings.Repeat("d4", 32))

	var asked interactionResponse
	f.expect(http.MethodPost, "/v1/interactions", f.token,
		`{"title":"deploy-bot","prompt":"Deploy to production?","kind":"approval"}`,
		http.StatusCreated, &asked)

	answer := `{"action":"approve","device_id":"` + device.ID +
		`","action_digest":"` + asked.Interaction.ActionDigest + `"}`
	path := "/v1/interactions/" + asked.Interaction.ID + "/response"

	rec := f.request(http.MethodPost, path, f.token, answer)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("the asking token answered its own question: status = %d, want 403: %s", rec.Code, rec.Body)
	}
	if got := decodeError(t, rec); got.Error.Code != CodeSessionRequired {
		t.Errorf("code = %q, want %q", got.Error.Code, CodeSessionRequired)
	}

	// Refused before anything was written: the question is still waiting.
	var current interactionReadResponse
	f.expect(http.MethodGet, path[:len(path)-len("/response")], f.session, "", http.StatusOK, &current)
	if current.Interaction.Status != db.InteractionPending {
		t.Fatalf("status = %q, want it still pending after the refusal", current.Interaction.Status)
	}

	var answered interactionReadResponse
	f.expect(http.MethodPost, path, f.session, answer, http.StatusOK, &answered)
	if answered.Interaction.Status != db.InteractionApproved {
		t.Fatalf("status = %q, want approved", answered.Interaction.Status)
	}
}

// TestAPITokenInteractionIsolation pins the requester boundary: an API token
// lists, reads, long-polls and cancels only the questions it asked itself,
// while the owner's session keeps the account-wide inbox. A foreign id — the
// other token's question, a webhook service's, or one that never existed — is
// the same 404 everywhere, so the surface confirms nothing it does not own.
func TestAPITokenInteractionIsolation(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	f.registerDevice(strings.Repeat("a7", 32))

	// A second, independently authenticated agent credential.
	var minted createTokenResponse
	f.expect(http.MethodPost, "/v1/tokens", f.session,
		`{"name":"other-agent","scopes":["interactions:create","interactions:read","notifications:send"]}`,
		http.StatusCreated, &minted)
	tokenB := minted.Secret

	// A webhook service asks one question too.
	_, hook := f.createService("Release")
	var hookAsked webhookNotifyResponse
	f.expect(http.MethodPost, hook, "", `{"body":"Deploy to production?","response":{"kind":"approval"}}`,
		http.StatusCreated, &hookAsked)
	if hookAsked.Response == nil {
		t.Fatal("the webhook carried a response block but produced no question")
	}
	hookID := hookAsked.Response.InteractionID

	ask := func(credential, prompt string) string {
		t.Helper()
		var asked interactionResponse
		f.expect(http.MethodPost, "/v1/interactions", credential,
			`{"title":"agent","prompt":"`+prompt+`","kind":"approval"}`,
			http.StatusCreated, &asked)
		return asked.Interaction.ID
	}
	bID := ask(tokenB, "B's question")
	a1 := ask(f.token, "A's first")
	a2 := ask(f.token, "A's second")
	a3 := ask(f.token, "A's third")

	// Token A's list is its own questions only, and the cursor keeps the
	// filter: page two continues token A's rows, never drifting account-wide.
	var first interactionListResponse
	f.expect(http.MethodGet, "/v1/interactions?limit=2", f.token, "", http.StatusOK, &first)
	if len(first.Interactions) != 2 || first.NextCursor == nil {
		t.Fatalf("page 1 = %d items, next_cursor %v; want 2 items and a cursor", len(first.Interactions), first.NextCursor)
	}
	var second interactionListResponse
	f.expect(http.MethodGet, "/v1/interactions?limit=2&cursor="+*first.NextCursor, f.token, "",
		http.StatusOK, &second)
	if len(second.Interactions) != 1 || second.NextCursor != nil {
		t.Fatalf("page 2 = %d items, next_cursor %v; want token A's last item and no cursor", len(second.Interactions), second.NextCursor)
	}
	mine := map[string]bool{a1: true, a2: true, a3: true}
	for _, item := range append(first.Interactions, second.Interactions...) {
		if !mine[item.ID] {
			t.Errorf("token A's list leaked %s; it asked only %v", item.ID, []string{a1, a2, a3})
		}
		delete(mine, item.ID)
	}
	for id := range mine {
		t.Errorf("token A's list is missing its own %s", id)
	}

	// Token B sees exactly its one question.
	var bList interactionListResponse
	f.expect(http.MethodGet, "/v1/interactions", tokenB, "", http.StatusOK, &bList)
	if len(bList.Interactions) != 1 || bList.Interactions[0].ID != bID {
		t.Fatalf("token B's list = %+v, want only its own question", bList.Interactions)
	}

	// The session's inbox is account-wide: both tokens' questions and the
	// webhook service's.
	var inbox interactionListResponse
	f.expect(http.MethodGet, "/v1/interactions", f.session, "", http.StatusOK, &inbox)
	if len(inbox.Interactions) != 5 {
		t.Fatalf("session inbox = %d items, want all 5", len(inbox.Interactions))
	}

	// Another requester's question reads as if it did not exist.
	for _, foreign := range []string{bID, hookID} {
		rec := f.request(http.MethodGet, "/v1/interactions/"+foreign, f.token, "")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("token A read %s: status = %d, want 404: %s", foreign, rec.Code, rec.Body)
		}
		if got := decodeError(t, rec); got.Error.Code != CodeNotFound {
			t.Errorf("code = %q, want %q", got.Error.Code, CodeNotFound)
		}
	}

	// A long poll on a foreign id refuses immediately: no waiting out the
	// clock, and no revealing whether the question is pending or answered.
	start := time.Now()
	rec := f.request(http.MethodGet, "/v1/interactions/"+bID+"?wait_seconds=10", f.token, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("foreign long poll: status = %d, want 404: %s", rec.Code, rec.Body)
	}
	if got := decodeError(t, rec); got.Error.Code != CodeNotFound {
		t.Errorf("code = %q, want %q", got.Error.Code, CodeNotFound)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("foreign long poll took %v, want an immediate refusal", elapsed)
	}

	// Token A cannot cancel B's question, and the refusal changes nothing.
	rec = f.request(http.MethodPost, "/v1/interactions/"+bID+"/cancel", f.token, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("foreign cancel: status = %d, want 404: %s", rec.Code, rec.Body)
	}
	if got := decodeError(t, rec); got.Error.Code != CodeNotFound {
		t.Errorf("code = %q, want %q", got.Error.Code, CodeNotFound)
	}
	var current interactionReadResponse
	f.expect(http.MethodGet, "/v1/interactions/"+bID, f.session, "", http.StatusOK, &current)
	if current.Interaction.Status != db.InteractionPending {
		t.Fatalf("B's question = %q after A's refused cancel, want still pending", current.Interaction.Status)
	}

	// Within its own reach the token keeps full authority: read and withdraw.
	f.expect(http.MethodGet, "/v1/interactions/"+a3, f.token, "", http.StatusOK, &current)
	f.expect(http.MethodPost, "/v1/interactions/"+a3+"/cancel", f.token, "", http.StatusOK, &current)
	if current.Interaction.Status != db.InteractionCanceled {
		t.Fatalf("token A canceling its own question = %q, want canceled", current.Interaction.Status)
	}

	// The session's authority is unchanged: it reads and cancels either
	// token's question, and the webhook service's.
	f.expect(http.MethodGet, "/v1/interactions/"+hookID, f.session, "", http.StatusOK, &current)
	f.expect(http.MethodGet, "/v1/interactions/"+a1, f.session, "", http.StatusOK, &current)
	f.expect(http.MethodPost, "/v1/interactions/"+bID+"/cancel", f.session, "", http.StatusOK, &current)
	if current.Interaction.Status != db.InteractionCanceled {
		t.Fatalf("session canceling B's question = %q, want canceled", current.Interaction.Status)
	}
	f.expect(http.MethodPost, "/v1/interactions/"+a2+"/cancel", f.session, "", http.StatusOK, &current)
	if current.Interaction.Status != db.InteractionCanceled {
		t.Fatalf("session canceling A's question = %q, want canceled", current.Interaction.Status)
	}
}

// TestInteractionDigestBindsTheAnswerToTheQuestion pins the guard that stops a
// phone showing a stale prompt from answering the one that replaced it.
func TestInteractionDigestBindsTheAnswerToTheQuestion(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	device := f.registerDevice(strings.Repeat("c3", 32))

	var asked interactionResponse
	f.expect(http.MethodPost, "/v1/interactions", f.token,
		`{"title":"Claude Code","prompt":"Run the migration?","kind":"approval"}`,
		http.StatusCreated, &asked)
	if asked.Accepted != 1 {
		t.Fatalf("accepted = %d, want 1", asked.Accepted)
	}

	rec := f.request(http.MethodPost, "/v1/interactions/"+asked.Interaction.ID+"/response", f.session,
		`{"action":"approve","device_id":"`+device.ID+`","action_digest":"stale"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale digest: status = %d, want 409: %s", rec.Code, rec.Body)
	}
	if got := decodeError(t, rec); got.Error.Code != CodeDigestMismatch {
		t.Errorf("code = %q, want %q", got.Error.Code, CodeDigestMismatch)
	}

	// An answer the kind does not accept is a validation failure, not a
	// conflict: the caller sent something this question never offered.
	rec = f.request(http.MethodPost, "/v1/interactions/"+asked.Interaction.ID+"/response", f.session,
		`{"action":"yes","device_id":"`+device.ID+`","action_digest":"`+asked.Interaction.ActionDigest+`"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("wrong action: status = %d, want 422: %s", rec.Code, rec.Body)
	}

	var inbox interactionListResponse
	f.expect(http.MethodGet, "/v1/interactions", f.session, "", http.StatusOK, &inbox)
	if len(inbox.Interactions) != 1 || inbox.Interactions[0].SourceName != "harkctl" {
		t.Fatalf("inbox = %+v, want one question from harkctl", inbox.Interactions)
	}

	f.expect(http.MethodPost, "/v1/interactions/"+asked.Interaction.ID+"/response", f.session,
		`{"action":"approve","device_id":"`+device.ID+`","action_digest":"`+asked.Interaction.ActionDigest+`"}`,
		http.StatusOK, nil)

	// Answered questions leave the inbox and enter the history.
	f.expect(http.MethodGet, "/v1/interactions", f.session, "", http.StatusOK, &inbox)
	if len(inbox.Interactions) != 0 {
		t.Errorf("inbox after answering = %+v, want empty", inbox.Interactions)
	}
	var history historyListResponse
	f.expect(http.MethodGet, "/v1/history?kind=response", f.session, "", http.StatusOK, &history)
	if len(history.Items) != 1 || history.Items[0].Kind != db.FeedKindResponse {
		t.Fatalf("history = %+v, want one answered question", history.Items)
	}
}

// TestLiveActivityOccupiesOneDeviceSlot covers the invariant that shapes the
// whole Live Activity surface: a phone shows one ordinary activity at a time, so
// a second start either refuses or replaces.
func TestLiveActivityOccupiesOneDeviceSlot(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	device := f.registerDevice(strings.Repeat("d4", 32))

	var started activityResponse
	f.expect(http.MethodPost, "/v1/activities", f.token,
		`{"key":"deploy","title":"Deploy","status":"Building","progress":0.25}`,
		http.StatusCreated, &started)
	if started.Accepted != 1 || started.Activity.Status != db.ActivityActive {
		t.Fatalf("start = %+v, want one accepted delivery and an active activity", started)
	}
	if push := f.sender.lastActivity(t); push.Start == nil || push.Start.RegistrationToken == "" {
		t.Fatalf("start push carried no registration capability: %+v", push)
	}

	// A second start on the same phone is refused, and says how to proceed.
	rec := f.request(http.MethodPost, "/v1/activities", f.token,
		`{"key":"other","title":"Tests","status":"Running"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second start: status = %d, want 409: %s", rec.Code, rec.Body)
	}
	got := decodeError(t, rec)
	if got.Error.Code != CodeActivityConflict {
		t.Errorf("code = %q, want %q", got.Error.Code, CodeActivityConflict)
	}
	if !strings.Contains(got.Error.Message, started.Activity.ID) {
		t.Errorf("message does not name the blocking activity: %q", got.Error.Message)
	}

	// With replace it takes the slot, and reports what it displaced.
	var replaced activityResponse
	f.expect(http.MethodPost, "/v1/activities", f.token,
		`{"key":"other","title":"Tests","status":"Running","replace":true}`,
		http.StatusCreated, &replaced)
	if replaced.Replaced == nil || *replaced.Replaced != 1 {
		t.Fatalf("replaced = %v, want 1", replaced.Replaced)
	}

	var reread activityReadResponse
	f.expect(http.MethodGet, "/v1/activities/"+started.Activity.ID, f.session, "", http.StatusOK, &reread)
	if reread.Activity.Status != db.ActivityEnded {
		t.Errorf("displaced activity = %q, want ended", reread.Activity.Status)
	}

	// An update cannot reach a phone that has not reported its per-activity
	// token yet — the common case, and one the caller is told about rather than
	// left to infer from a zero count.
	var updated activityResponse
	f.expect(http.MethodPatch, "/v1/activities/other", f.token,
		`{"status":"Passing","progress":0.9}`, http.StatusOK, &updated)
	if updated.Accepted != 0 || updated.Message == nil || !strings.Contains(*updated.Message, "MissingUpdateToken") {
		t.Fatalf("update = %+v, want a MissingUpdateToken message", updated)
	}

	// Once the phone reports the token, updates land.
	f.expect(http.MethodPut, "/v1/devices/"+device.ID+"/activity-update-token", f.session,
		`{"update_token":"`+strings.Repeat("cd", 32)+`","native_activity_id":"native-1","environment":"sandbox"}`,
		http.StatusOK, nil)

	stale := updated.Activity.Sequence
	f.expect(http.MethodPatch, "/v1/activities/other", f.token,
		`{"status":"Green","if_sequence":`+strconv.Itoa(stale)+`}`, http.StatusOK, &updated)
	if updated.Accepted != 1 {
		t.Fatalf("update after registration = %+v, want one accepted delivery", updated)
	}

	// The sequence is the concurrency token: a stale one is refused rather than
	// silently overwriting somebody else's change.
	rec = f.request(http.MethodPatch, "/v1/activities/other", f.token,
		`{"status":"Stale","if_sequence":`+strconv.Itoa(stale)+`}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale sequence: status = %d, want 409: %s", rec.Code, rec.Body)
	}
	if got := decodeError(t, rec); got.Error.Code != CodeSequenceConflict {
		t.Errorf("code = %q, want %q", got.Error.Code, CodeSequenceConflict)
	}

	var ended activityResponse
	f.expect(http.MethodPost, "/v1/activities/other/end", f.token,
		`{"status":"Done","dismiss_after_seconds":30}`, http.StatusOK, &ended)
	if ended.Activity.Status != db.ActivityEnded || ended.Accepted != 1 {
		t.Fatalf("end = %+v, want an ended activity with one accepted delivery", ended)
	}

	// Ending frees the key: the same handle starts a new run.
	f.expect(http.MethodPost, "/v1/activities", f.token,
		`{"key":"other","title":"Tests","status":"Running"}`, http.StatusCreated, nil)
}

// TestIdempotencyKeyReplaysRatherThanResends is what makes a retry safe. The row
// is written before anything is sent, so a duplicate replays the stored outcome
// instead of pushing a second copy to somebody's Lock Screen.
func TestIdempotencyKeyReplaysRatherThanResends(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	f.registerDevice(strings.Repeat("e5", 32))

	const body = `{"body":"Build 4821 succeeded","title":"CI"}`

	var first notificationResponse
	f.withHeader(http.MethodPost, "/v1/notifications", f.token, body,
		IdempotencyKeyHeader, "build-4821", http.StatusCreated, &first)
	if first.Notification.AcceptedCount != 1 || first.Replayed {
		t.Fatalf("first send = %+v, want one accepted and replayed false", first)
	}

	var second notificationResponse
	f.withHeader(http.MethodPost, "/v1/notifications", f.token, body,
		IdempotencyKeyHeader, "build-4821", http.StatusOK, &second)
	if !second.Replayed || second.Notification.ID != first.Notification.ID {
		t.Fatalf("replay = %+v, want the first notification back", second)
	}
	if len(f.sender.alerts) != 1 {
		t.Errorf("sent %d alerts, want exactly one for two identical requests", len(f.sender.alerts))
	}

	// The same key with a different payload is a client bug: answering with the
	// first request's outcome would be a lie about what was delivered.
	f.withHeader(http.MethodPost, "/v1/notifications", f.token, `{"body":"Something else"}`,
		IdempotencyKeyHeader, "build-4821", http.StatusConflict, nil)

	// An empty key is refused rather than treated as absent, because a caller
	// that computed one and got "" is about to get the double-send it was trying
	// to avoid.
	f.withHeader(http.MethodPost, "/v1/notifications", f.token, body,
		IdempotencyKeyHeader, "   ", http.StatusBadRequest, nil)
}

// TestDeliveryQuotaRefusesWithRetryAfter covers the ceiling that bounds a
// runaway agent. It is counted from the rows that were written rather than from
// an in-memory counter, so a restart hands nobody a fresh allowance.
func TestDeliveryQuotaRefusesWithRetryAfter(t *testing.T) {
	f := newFixture(t, fixtureOptions{requesterRate: 1, accountRate: 100})
	f.registerDevice(strings.Repeat("f6", 32))

	f.expect(http.MethodPost, "/v1/notifications", f.token, `{"body":"one"}`, http.StatusCreated, nil)

	rec := f.request(http.MethodPost, "/v1/notifications", f.token, `{"body":"two"}`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429: %s", rec.Code, rec.Body)
	}
	if got := decodeError(t, rec); got.Error.Code != CodeRateLimited {
		t.Errorf("code = %q, want %q", got.Error.Code, CodeRateLimited)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("a rate-limited response carries no Retry-After")
	}
}

// TestWebhookCredentialIsScopedToItsService checks the two things a webhook URL
// must never do: work after rotation, and reach another service's activities.
func TestWebhookCredentialIsScopedToItsService(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	f.registerDevice(strings.Repeat("a7", 32))
	service, hook := f.createService("Deploy bot")

	f.expect(http.MethodPost, hook, "", `{"body":"before rotation"}`, http.StatusCreated, nil)

	var rotated createdServiceResponse
	f.expect(http.MethodPost, "/v1/services/"+service.ID+"/webhook-token", f.session, "",
		http.StatusCreated, &rotated)

	if rec := f.request(http.MethodPost, hook, "", `{"body":"after rotation"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("the old webhook URL still works: status = %d", rec.Code)
	}

	newHook := strings.TrimPrefix(rotated.WebhookURL, "https://hark.example.com")
	f.expect(http.MethodPost, newHook, "", `{"body":"after rotation"}`, http.StatusCreated, nil)

	// A token may read services, but never the URL that can send as one.
	var listed serviceListResponse
	f.expect(http.MethodGet, "/v1/services", f.token, "", http.StatusOK, &listed)
	if len(listed.Services) != 1 || listed.Services[0].WebhookURL != nil {
		t.Fatalf("services for a token = %+v, want the webhook URL withheld", listed.Services)
	}
	f.expect(http.MethodGet, "/v1/services", f.session, "", http.StatusOK, &listed)
	if listed.Services[0].WebhookURL == nil {
		t.Error("the owner cannot see their own webhook URL")
	}
}

// TestStaleDeviceIsDeactivatedByItsOwnFailure covers the pruning rule: a token
// APNs has permanently rejected stops being pushed to, without the row being
// deleted, so history keeps resolving.
func TestStaleDeviceIsDeactivatedByItsOwnFailure(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	token := strings.Repeat("b8", 32)
	device := f.registerDevice(token)
	f.sender.stale[token] = true

	var sent notificationResponse
	f.expect(http.MethodPost, "/v1/notifications", f.token, `{"body":"knock knock"}`,
		http.StatusCreated, &sent)
	if sent.Notification.AcceptedCount != 0 || sent.Message == nil {
		t.Fatalf("send = %+v, want nothing accepted and a message saying so", sent)
	}

	var listed deviceListResponse
	f.expect(http.MethodGet, "/v1/devices", f.session, "", http.StatusOK, &listed)
	if len(listed.Devices) != 1 || listed.Devices[0].ID != device.ID {
		t.Fatalf("devices = %+v, want the row kept", listed.Devices)
	}
	if listed.Devices[0].Active {
		t.Error("a device APNs rejected is still marked active")
	}

	// The history has to say which of the two problems this was. A phone that
	// was there and refused the push is a delivery failure; "no devices" would
	// send the owner off to register a phone they already have.
	var history historyListResponse
	f.expect(http.MethodGet, "/v1/history", f.session, "", http.StatusOK, &history)
	if len(history.Items) != 1 {
		t.Fatalf("history = %+v, want the one send", history.Items)
	}
	if got := history.Items[0].Status; got == nil || *got != db.EventFailed {
		t.Errorf("history status = %v, want %q", got, db.EventFailed)
	}
}

// TestInteractionOnTheLockScreenEndsWhenItIsAnswered covers the interactive
// presentation end to end: the question becomes a card, and answering it takes
// the card down.
func TestInteractionOnTheLockScreenEndsWhenItIsAnswered(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	device := f.registerDevice(strings.Repeat("c9", 32))

	var asked interactionResponse
	f.expect(http.MethodPost, "/v1/interactions", f.token, `{
		"title": "Claude Code",
		"prompt": "Allow Bash?",
		"kind": "approval",
		"presentation": "live_activity",
		"primary_label": "Allow",
		"secondary_label": "Deny"
	}`, http.StatusCreated, &asked)

	if asked.ActivityID == nil || asked.Accepted != 1 {
		t.Fatalf("ask = %+v, want a Live Activity with one accepted delivery", asked)
	}
	start := f.sender.lastActivity(t)
	if start.Start == nil || start.Start.Interaction == nil {
		t.Fatalf("the start push carried no question: %+v", start)
	}
	if start.Start.Interaction.ResponseToken == "" {
		t.Error("the Lock Screen buttons were given no way to answer")
	}

	// The activity presenting a question is not an ordinary activity: it is
	// hidden from the activity surfaces, because it is shown as the question.
	var activities activityListResponse
	f.expect(http.MethodGet, "/v1/activities", f.session, "", http.StatusOK, &activities)
	if len(activities.Activities) != 0 {
		t.Errorf("activities = %+v, want the interaction's card hidden", activities.Activities)
	}

	// The widget reports the per-activity update token the start push produced,
	// using the capability that push carried rather than a session — a Lock
	// Screen card outlives the app that started it. Until this lands nothing can
	// be pushed to the card, so it is the step that makes ending it observable.
	f.expect(http.MethodPut, "/v1/activity-deliveries/"+start.DeliveryID+"/update-token", "",
		`{"registration_token":"`+start.Start.RegistrationToken+`",`+
			`"update_token":"`+strings.Repeat("ef", 32)+`","native_activity_id":"native-q1"}`,
		http.StatusNoContent, nil)

	f.sender.reset()
	f.expect(http.MethodPost, "/v1/interactions/"+asked.Interaction.ID+"/response", f.session,
		`{"action":"approve","device_id":"`+device.ID+`","action_digest":"`+asked.Interaction.ActionDigest+`"}`,
		http.StatusOK, nil)

	end := f.sender.lastActivity(t)
	if end.Event != "end" {
		t.Fatalf("after answering, the card got a %q push, want an end", end.Event)
	}
	var state activityState
	if err := json.Unmarshal(end.State, &state); err != nil {
		t.Fatalf("the end push carried an undecodable state: %v", err)
	}
	if state.Status != "Approved" || state.Interaction == nil || state.Interaction.State != db.InteractionApproved {
		t.Errorf("end state = %+v, want it to show the answer", state)
	}
}

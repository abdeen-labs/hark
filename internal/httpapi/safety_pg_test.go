package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/abdeen-labs/hark/internal/db"
)

// createSafetySource creates a source through the API.
func (f *fixture) createSafetySource(kind, name string) safetySourceDTO {
	f.t.Helper()

	var created safetySourceResponse
	f.expect(http.MethodPost, "/v1/safety-sources", f.session,
		`{"kind":"`+kind+`","name":"`+name+`"}`, http.StatusCreated, &created)
	return created.Source
}

// enableCritical enables Critical Alerts for a source.
func (f *fixture) enableCritical(sourceID string) {
	f.t.Helper()
	f.expect(http.MethodPatch, "/v1/safety-sources/"+sourceID, f.session,
		`{"critical_enabled":true}`, http.StatusOK, nil)
}

// reportSafety sends and decodes one report.
func (f *fixture) reportSafety(sourceID, state string, want int) safetyEventResponse {
	f.t.Helper()

	var out safetyEventResponse
	f.expect(http.MethodPost, "/v1/safety-events", f.token,
		`{"source_id":"`+sourceID+`","state":"`+state+`"}`, want, &out)
	return out
}

func TestSafetySourceLifecycle(t *testing.T) {
	f := newFixture(t, fixtureOptions{})

	src := f.createSafetySource(db.SafetyKindSmoke, "Kitchen")
	if src.Kind != db.SafetyKindSmoke || src.CriticalEnabled {
		t.Fatalf("created = %+v, want the kind echoed and critical delivery off", src)
	}

	// Critical Alerts cannot be enabled during creation.
	rec := f.request(http.MethodPost, "/v1/safety-sources", f.session,
		`{"kind":"smoke","name":"Garage","critical_enabled":true}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create with critical_enabled: status = %d, want 400: %s", rec.Code, rec.Body)
	}

	rec = f.request(http.MethodPost, "/v1/safety-sources", f.session, `{"kind":"fire","name":"Garage"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("create with an unknown kind: status = %d, want 422: %s", rec.Code, rec.Body)
	}

	second := f.createSafetySource(db.SafetyKindWaterLeak, "Basement")
	var listed safetySourceListResponse
	f.expect(http.MethodGet, "/v1/safety-sources", f.session, "", http.StatusOK, &listed)
	if len(listed.Sources) != 2 || listed.Sources[0].ID != second.ID {
		t.Fatalf("sources = %+v, want both, newest first", listed.Sources)
	}

	var got safetySourceResponse
	f.expect(http.MethodGet, "/v1/safety-sources/"+src.ID, f.session, "", http.StatusOK, &got)
	if got.Source.ID != src.ID {
		t.Fatalf("get = %+v, want %s", got.Source, src.ID)
	}

	f.expect(http.MethodPatch, "/v1/safety-sources/"+src.ID, f.session,
		`{"name":"Hallway"}`, http.StatusOK, &got)
	if got.Source.Name != "Hallway" || got.Source.CriticalEnabled {
		t.Fatalf("renamed = %+v, want the toggle untouched", got.Source)
	}
	f.expect(http.MethodPatch, "/v1/safety-sources/"+src.ID, f.session,
		`{"critical_enabled":true}`, http.StatusOK, &got)
	if !got.Source.CriticalEnabled || got.Source.Name != "Hallway" {
		t.Fatalf("toggled = %+v, want the name untouched", got.Source)
	}

	// An empty PATCH changes nothing.
	rec = f.request(http.MethodPatch, "/v1/safety-sources/"+src.ID, f.session, `{}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty patch: status = %d, want 422: %s", rec.Code, rec.Body)
	}

	f.expect(http.MethodDelete, "/v1/safety-sources/"+second.ID, f.session, "", http.StatusNoContent, nil)
	rec = f.request(http.MethodGet, "/v1/safety-sources/"+second.ID, f.session, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete: status = %d, want 404: %s", rec.Code, rec.Body)
	}
}

func TestSafetySettingsToggle(t *testing.T) {
	f := newFixture(t, fixtureOptions{})

	var settings safetySettingsResponse
	f.expect(http.MethodGet, "/v1/safety-settings", f.session, "", http.StatusOK, &settings)
	if !settings.CriticalAlertsEnabled {
		t.Fatal("a fresh account should have critical alerts enabled")
	}

	f.expect(http.MethodPatch, "/v1/safety-settings", f.session,
		`{"critical_alerts_enabled":false}`, http.StatusOK, &settings)
	if settings.CriticalAlertsEnabled {
		t.Fatal("the PATCH did not echo the new value")
	}
	f.expect(http.MethodGet, "/v1/safety-settings", f.session, "", http.StatusOK, &settings)
	if settings.CriticalAlertsEnabled {
		t.Fatal("the toggle did not persist")
	}

	// The account setting is required.
	rec := f.request(http.MethodPatch, "/v1/safety-settings", f.session, `{}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty patch: status = %d, want 422: %s", rec.Code, rec.Body)
	}
}

func TestSafetyAuthBoundaries(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	src := f.createSafetySource(db.SafetyKindSmoke, "Kitchen")

	// Sessions cannot report safety events.
	rec := f.request(http.MethodPost, "/v1/safety-events", f.session,
		`{"source_id":"`+src.ID+`","state":"active"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("session report: status = %d, want 403: %s", rec.Code, rec.Body)
	}
	if got := decodeError(t, rec); got.Error.Code != CodeAPITokenRequired {
		t.Errorf("code = %q, want %q", got.Error.Code, CodeAPITokenRequired)
	}

	// Reporting requires safety:report.
	var minted createTokenResponse
	f.expect(http.MethodPost, "/v1/tokens", f.session,
		`{"name":"narrow","scopes":["notifications:send"]}`, http.StatusCreated, &minted)
	rec = f.request(http.MethodPost, "/v1/safety-events", minted.Secret,
		`{"source_id":"`+src.ID+`","state":"active"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unscoped report: status = %d, want 403: %s", rec.Code, rec.Body)
	}
	if got := decodeError(t, rec); got.Error.Code != CodeInsufficientScope {
		t.Errorf("code = %q, want %q", got.Error.Code, CodeInsufficientScope)
	}

	// Source configuration and setup tests require a session.
	sessionOnly := []struct{ method, path, body string }{
		{http.MethodGet, "/v1/safety-sources", ""},
		{http.MethodPost, "/v1/safety-sources", `{"kind":"smoke","name":"Garage"}`},
		{http.MethodGet, "/v1/safety-sources/" + src.ID, ""},
		{http.MethodPatch, "/v1/safety-sources/" + src.ID, `{"critical_enabled":true}`},
		{http.MethodDelete, "/v1/safety-sources/" + src.ID, ""},
		{http.MethodPost, "/v1/safety-sources/" + src.ID + "/test", ""},
		{http.MethodGet, "/v1/safety-settings", ""},
		{http.MethodPatch, "/v1/safety-settings", `{"critical_alerts_enabled":false}`},
	}
	for _, route := range sessionOnly {
		rec := f.request(route.method, route.path, f.token, route.body)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s with a token: status = %d, want 403: %s", route.method, route.path, rec.Code, rec.Body)
			continue
		}
		if got := decodeError(t, rec); got.Error.Code != CodeSessionRequired {
			t.Errorf("%s %s: code = %q, want %q", route.method, route.path, got.Error.Code, CodeSessionRequired)
		}
	}
}

func TestSafetyReportComposesTheAlert(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	device := f.registerDevice(strings.Repeat("a1", 32))
	src := f.createSafetySource(db.SafetyKindSmoke, "Kitchen")
	f.enableCritical(src.ID)

	sent := f.reportSafety(src.ID, db.SafetyStateActive, http.StatusCreated)
	if sent.Event.Status != db.EventAccepted || sent.Event.DeliveredCount != 1 || sent.Replayed {
		t.Fatalf("report = %+v, want one accepted delivery", sent)
	}
	if sent.Event.SourceID != src.ID || sent.Event.SourceName != src.Name {
		t.Errorf("event = %+v, want it attributed to the source", sent.Event)
	}
	if sent.Event.State != db.SafetyStateActive || sent.Event.Priority != db.PriorityCritical {
		t.Errorf("event = %+v, want an active critical alert", sent.Event)
	}
	if sent.Event.Title == "" || sent.Event.Body == "" {
		t.Error("the server composed an empty alert")
	}

	wantTitle, wantBody := db.SafetyAlertContent(src.Kind, src.Name, db.SafetyStateActive)
	alert := f.sender.lastAlert(t)
	if alert.Title != wantTitle || alert.Body != wantBody {
		t.Errorf("alert = %+v, want the db-composed content", alert)
	}
	if alert.Priority != db.PriorityCritical {
		t.Errorf("alert priority = %q, want %q", alert.Priority, db.PriorityCritical)
	}
	if alert.ThreadKey != "safety-"+src.ID {
		t.Errorf("thread key = %q, want the per-source thread", alert.ThreadKey)
	}
	if alert.SourceID != src.ID || alert.RecordID != sent.Event.ID {
		t.Errorf("alert = %+v, want it tied to the source and the event row", alert)
	}
	if alert.Target.DeviceID != device.ID {
		t.Errorf("alert went to %q, want %q", alert.Target.DeviceID, device.ID)
	}

	// Resolved events use normal priority.
	resolved := f.reportSafety(src.ID, db.SafetyStateResolved, http.StatusCreated)
	if resolved.Event.Status != db.EventAccepted || resolved.Event.Priority != db.PriorityNormal {
		t.Fatalf("resolved = %+v, want an accepted normal-priority delivery", resolved.Event)
	}
	if got := f.sender.lastAlert(t); got.Priority != db.PriorityNormal || got.Title == alert.Title {
		t.Errorf("resolved alert = %+v, want normal priority and its own title", got)
	}
}

func TestSafetyDowngradeMatrix(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	f.registerDevice(strings.Repeat("b2", 32))

	sourceOff := f.createSafetySource(db.SafetyKindSmoke, "Kitchen")
	sent := f.reportSafety(sourceOff.ID, db.SafetyStateActive, http.StatusCreated)
	if sent.Event.Status != db.EventAccepted {
		t.Fatalf("report = %+v, want it delivered despite the downgrade", sent.Event)
	}
	if got := f.sender.lastAlert(t).Priority; got != db.PriorityTimeSensitive {
		t.Errorf("source toggle off: alert priority = %q, want %q", got, db.PriorityTimeSensitive)
	}

	globalOff := f.createSafetySource(db.SafetyKindPanic, "Wristband")
	f.enableCritical(globalOff.ID)
	f.expect(http.MethodPatch, "/v1/safety-settings", f.session,
		`{"critical_alerts_enabled":false}`, http.StatusOK, nil)
	sent = f.reportSafety(globalOff.ID, db.SafetyStateActive, http.StatusCreated)
	if sent.Event.Priority != db.PriorityTimeSensitive {
		t.Errorf("global toggle off: event priority = %q, want %q", sent.Event.Priority, db.PriorityTimeSensitive)
	}
	if got := f.sender.lastAlert(t).Priority; got != db.PriorityTimeSensitive {
		t.Errorf("global toggle off: alert priority = %q, want %q", got, db.PriorityTimeSensitive)
	}

	bothOn := f.createSafetySource(db.SafetyKindIntrusion, "Front door")
	f.enableCritical(bothOn.ID)
	f.expect(http.MethodPatch, "/v1/safety-settings", f.session,
		`{"critical_alerts_enabled":true}`, http.StatusOK, nil)
	sent = f.reportSafety(bothOn.ID, db.SafetyStateActive, http.StatusCreated)
	if sent.Event.Priority != db.PriorityCritical {
		t.Errorf("both toggles on: event priority = %q, want %q", sent.Event.Priority, db.PriorityCritical)
	}
	if got := f.sender.lastAlert(t).Priority; got != db.PriorityCritical {
		t.Errorf("both toggles on: alert priority = %q, want %q", got, db.PriorityCritical)
	}
}

func TestSafetyReportCoalescesRepeatedAlarms(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	f.registerDevice(strings.Repeat("c3", 32))
	src := f.createSafetySource(db.SafetyKindSmoke, "Kitchen")

	first := f.reportSafety(src.ID, db.SafetyStateActive, http.StatusCreated)
	if first.Event.Status != db.EventAccepted {
		t.Fatalf("first report = %+v, want it delivered", first.Event)
	}

	second := f.reportSafety(src.ID, db.SafetyStateActive, http.StatusCreated)
	if second.Event.Status != db.SafetyCoalesced || second.Event.DeliveredCount != 0 {
		t.Fatalf("second report = %+v, want it coalesced with nothing delivered", second.Event)
	}
	if second.Message == nil {
		t.Error("a coalesced report carried no explanation")
	}
	if len(f.sender.alerts) != 1 {
		t.Fatalf("sent %d alerts, want the repeat suppressed", len(f.sender.alerts))
	}

	// Resolved events are not coalesced with active events.
	resolved := f.reportSafety(src.ID, db.SafetyStateResolved, http.StatusCreated)
	if resolved.Event.Status != db.EventAccepted {
		t.Fatalf("resolved = %+v, want it delivered", resolved.Event)
	}
	if len(f.sender.alerts) != 2 {
		t.Fatalf("sent %d alerts, want the resolution pushed", len(f.sender.alerts))
	}

	// A resolved event does not reset the active-event window.
	refire := f.reportSafety(src.ID, db.SafetyStateActive, http.StatusCreated)
	if refire.Event.Status != db.SafetyCoalesced || len(f.sender.alerts) != 2 {
		t.Fatalf("re-fire = %+v with %d alerts, want it coalesced", refire.Event, len(f.sender.alerts))
	}
}

func TestSafetyHourlyCapRateLimits(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	f.registerDevice(strings.Repeat("d4", 32))
	src := f.createSafetySource(db.SafetyKindWaterLeak, "Basement")

	// Resolved events fill the remaining hourly allowance without coalescing.
	if sent := f.reportSafety(src.ID, db.SafetyStateActive, http.StatusCreated); sent.Event.Status != db.EventAccepted {
		t.Fatalf("report 1 = %+v, want it delivered", sent.Event)
	}
	for i := 2; i <= safetyHourlyCap; i++ {
		if sent := f.reportSafety(src.ID, db.SafetyStateResolved, http.StatusCreated); sent.Event.Status != db.EventAccepted {
			t.Fatalf("report %d = %+v, want it delivered", i, sent.Event)
		}
	}
	if len(f.sender.alerts) != safetyHourlyCap {
		t.Fatalf("sent %d alerts, want %d", len(f.sender.alerts), safetyHourlyCap)
	}

	over := f.reportSafety(src.ID, db.SafetyStateResolved, http.StatusCreated)
	if over.Event.Status != db.SafetyRateLimited || over.Event.DeliveredCount != 0 {
		t.Fatalf("report over the cap = %+v, want it rate-limited with nothing delivered", over.Event)
	}
	if over.Message == nil {
		t.Error("a rate-limited report carried no explanation")
	}
	if len(f.sender.alerts) != safetyHourlyCap {
		t.Fatalf("sent %d alerts, want the overflow suppressed", len(f.sender.alerts))
	}
}

func TestSafetySetupTest(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	f.registerDevice(strings.Repeat("e5", 32))
	src := f.createSafetySource(db.SafetyKindSmoke, "Kitchen")
	f.enableCritical(src.ID)

	var sent safetyTestResponse
	f.expect(http.MethodPost, "/v1/safety-sources/"+src.ID+"/test", f.session, "",
		http.StatusCreated, &sent)
	if sent.Event.State != db.SafetyStateTest || sent.Event.Status != db.EventAccepted {
		t.Fatalf("test = %+v, want an accepted test event", sent.Event)
	}
	// Both settings are enabled.
	if sent.Event.Priority != db.PriorityCritical {
		t.Errorf("test priority = %q, want %q", sent.Event.Priority, db.PriorityCritical)
	}
	if got := f.sender.lastAlert(t).Priority; got != db.PriorityCritical {
		t.Errorf("alert priority = %q, want %q", got, db.PriorityCritical)
	}

	// Tests are throttled per source.
	rec := f.request(http.MethodPost, "/v1/safety-sources/"+src.ID+"/test", f.session, "")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second test: status = %d, want 429: %s", rec.Code, rec.Body)
	}
	if got := decodeError(t, rec); got.Error.Code != CodeRateLimited {
		t.Errorf("code = %q, want %q", got.Error.Code, CodeRateLimited)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("a throttled test carries no Retry-After")
	}
	if len(f.sender.alerts) != 1 {
		t.Fatalf("sent %d alerts, want the repeat suppressed", len(f.sender.alerts))
	}

	// A disabled source setting downgrades the test.
	plain := f.createSafetySource(db.SafetyKindPanic, "Wristband")
	f.expect(http.MethodPost, "/v1/safety-sources/"+plain.ID+"/test", f.session, "",
		http.StatusCreated, &sent)
	if sent.Event.Priority != db.PriorityTimeSensitive {
		t.Errorf("downgraded test priority = %q, want %q", sent.Event.Priority, db.PriorityTimeSensitive)
	}

	rec = f.request(http.MethodPost, "/v1/safety-sources/unknown/test", f.session, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("test on an unknown source: status = %d, want 404: %s", rec.Code, rec.Body)
	}
}

func TestSafetyReportValidation(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	f.registerDevice(strings.Repeat("f6", 32))
	src := f.createSafetySource(db.SafetyKindSmoke, "Kitchen")

	// Setup tests use their session-only endpoint.
	rec := f.request(http.MethodPost, "/v1/safety-events", f.token,
		`{"source_id":"`+src.ID+`","state":"test"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("state test: status = %d, want 422: %s", rec.Code, rec.Body)
	}
	got := decodeError(t, rec)
	if len(got.Error.Fields) != 1 || got.Error.Fields[0].Field != "state" {
		t.Errorf("fields = %+v, want one entry naming state", got.Error.Fields)
	}

	// Unknown sources produce a source_id field error.
	rec = f.request(http.MethodPost, "/v1/safety-events", f.token,
		`{"source_id":"`+newID()+`","state":"active"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown source: status = %d, want 422: %s", rec.Code, rec.Body)
	}
	got = decodeError(t, rec)
	if len(got.Error.Fields) != 1 || got.Error.Fields[0].Field != "source_id" {
		t.Errorf("fields = %+v, want one entry naming source_id", got.Error.Fields)
	}
	if len(f.sender.alerts) != 0 {
		t.Fatalf("sent %d alerts, want none for refused reports", len(f.sender.alerts))
	}

	// Reporters cannot supply alert text.
	rec = f.request(http.MethodPost, "/v1/safety-events", f.token,
		`{"source_id":"`+src.ID+`","state":"active","title":"EVACUATE"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("caller-supplied title: status = %d, want 400: %s", rec.Code, rec.Body)
	}
}

func TestSafetyReportIdempotency(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	f.registerDevice(strings.Repeat("a7", 32))
	src := f.createSafetySource(db.SafetyKindSmoke, "Kitchen")

	body := `{"source_id":"` + src.ID + `","state":"active"}`

	var first safetyEventResponse
	f.withHeader(http.MethodPost, "/v1/safety-events", f.token, body,
		IdempotencyKeyHeader, "alarm-1", http.StatusCreated, &first)
	if first.Event.Status != db.EventAccepted || first.Replayed {
		t.Fatalf("first report = %+v, want one accepted delivery", first)
	}

	var second safetyEventResponse
	f.withHeader(http.MethodPost, "/v1/safety-events", f.token, body,
		IdempotencyKeyHeader, "alarm-1", http.StatusOK, &second)
	if !second.Replayed || second.Event.ID != first.Event.ID {
		t.Fatalf("replay = %+v, want the first event back", second)
	}
	if len(f.sender.alerts) != 1 {
		t.Errorf("sent %d alerts, want exactly one for two identical reports", len(f.sender.alerts))
	}

	// Reusing a key for a different payload is a conflict.
	f.withHeader(http.MethodPost, "/v1/safety-events", f.token,
		`{"source_id":"`+src.ID+`","state":"resolved"}`,
		IdempotencyKeyHeader, "alarm-1", http.StatusConflict, nil)

	// Replaying a coalesced report does not send it.
	var coalesced safetyEventResponse
	f.withHeader(http.MethodPost, "/v1/safety-events", f.token, body,
		IdempotencyKeyHeader, "alarm-2", http.StatusCreated, &coalesced)
	if coalesced.Event.Status != db.SafetyCoalesced {
		t.Fatalf("second alarm = %+v, want it coalesced", coalesced.Event)
	}
	var replayed safetyEventResponse
	f.withHeader(http.MethodPost, "/v1/safety-events", f.token, body,
		IdempotencyKeyHeader, "alarm-2", http.StatusOK, &replayed)
	if !replayed.Replayed || replayed.Event.Status != db.SafetyCoalesced {
		t.Fatalf("replayed = %+v, want the stored coalesced outcome", replayed)
	}
	if len(f.sender.alerts) != 1 {
		t.Errorf("sent %d alerts, want the replay to push nothing", len(f.sender.alerts))
	}
}

func TestSafetyEventsEnterTheHistory(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	f.registerDevice(strings.Repeat("b8", 32))
	src := f.createSafetySource(db.SafetyKindCarbonMonoxide, "Bedroom")

	sent := f.reportSafety(src.ID, db.SafetyStateActive, http.StatusCreated)

	var history historyListResponse
	f.expect(http.MethodGet, "/v1/history", f.session, "", http.StatusOK, &history)
	if len(history.Items) != 1 {
		t.Fatalf("history = %+v, want the one safety event", history.Items)
	}
	item := history.Items[0]
	if item.ID != db.FeedSourceSafetyEvent+":"+sent.Event.ID || item.Kind != db.FeedKindNotification {
		t.Fatalf("item = %+v, want a notification-kind safety row", item)
	}
	if item.SourceName != src.Name || item.Title != sent.Event.Title {
		t.Errorf("item = %+v, want the source's name and the composed title", item)
	}
	if item.Status == nil || *item.Status != db.EventAccepted ||
		item.Priority == nil || *item.Priority != sent.Event.Priority {
		t.Errorf("item = %+v, want the delivery fields populated", item)
	}

	f.expect(http.MethodDelete, "/v1/history/"+item.ID, f.session, "", http.StatusNoContent, nil)
	f.expect(http.MethodGet, "/v1/history", f.session, "", http.StatusOK, &history)
	if len(history.Items) != 0 {
		t.Errorf("history after the delete = %+v, want empty", history.Items)
	}
}

func TestSafetyReportsConsumeDeliveryQuota(t *testing.T) {
	f := newFixture(t, fixtureOptions{requesterRate: 1, accountRate: 100})
	f.registerDevice(strings.Repeat("c9", 32))
	src := f.createSafetySource(db.SafetyKindSmoke, "Kitchen")

	f.reportSafety(src.ID, db.SafetyStateActive, http.StatusCreated)

	rec := f.request(http.MethodPost, "/v1/safety-events", f.token,
		`{"source_id":"`+src.ID+`","state":"resolved"}`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second report: status = %d, want 429: %s", rec.Code, rec.Body)
	}
	if got := decodeError(t, rec); got.Error.Code != CodeRateLimited {
		t.Errorf("code = %q, want %q", got.Error.Code, CodeRateLimited)
	}
}

func TestSafetyReportsCountAgainstTheAccountCeiling(t *testing.T) {
	f := newFixture(t, fixtureOptions{requesterRate: 100, accountRate: 1})
	f.registerDevice(strings.Repeat("d1", 32))
	src := f.createSafetySource(db.SafetyKindSmoke, "Kitchen")

	f.reportSafety(src.ID, db.SafetyStateActive, http.StatusCreated)

	rec := f.request(http.MethodPost, "/v1/notifications", f.token, `{"body":"hello"}`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("notification after a safety report: status = %d, want 429: %s", rec.Code, rec.Body)
	}
}

func TestCriticalPriorityIsRefusedEverywhere(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	f.registerDevice(strings.Repeat("e2", 32))
	service, hook := f.createService("Deploy bot")

	refused := []struct{ method, path, credential, body string }{
		{http.MethodPost, "/v1/notifications", f.token, `{"body":"hi","priority":"critical"}`},
		{http.MethodPost, hook, "", `{"body":"hi","priority":"critical"}`},
		{http.MethodPost, "/v1/interactions", f.token,
			`{"title":"t","prompt":"p","kind":"approval","priority":"critical"}`},
		{http.MethodPost, "/v1/services", f.session, `{"title":"New","priority":"critical"}`},
		{http.MethodPatch, "/v1/services/" + service.ID, f.session, `{"priority":"critical"}`},
	}
	for _, route := range refused {
		rec := f.request(route.method, route.path, route.credential, route.body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s %s: status = %d, want 422: %s", route.method, route.path, rec.Code, rec.Body)
			continue
		}
		got := decodeError(t, rec)
		if len(got.Error.Fields) != 1 || got.Error.Fields[0].Field != "priority" {
			t.Errorf("%s %s: fields = %+v, want one entry naming priority", route.method, route.path, got.Error.Fields)
		}
	}
	if len(f.sender.alerts) != 0 {
		t.Fatalf("sent %d alerts, want none for refused priorities", len(f.sender.alerts))
	}

	// Normal and Time Sensitive remain valid.
	for _, priority := range db.Priorities {
		var sent notificationResponse
		f.expect(http.MethodPost, "/v1/notifications", f.token,
			`{"body":"hi","priority":"`+priority+`"}`, http.StatusCreated, &sent)
		if got := f.sender.lastAlert(t).Priority; got != priority {
			t.Errorf("alert priority = %q, want %q", got, priority)
		}
	}
}

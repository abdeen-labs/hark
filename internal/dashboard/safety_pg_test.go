package dashboard

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/id"
	"github.com/abdeen-labs/hark/internal/push"
)

// mustSafetySource creates a source and optionally enables Critical Alerts.
func mustSafetySource(t *testing.T, store *db.Store, userID string, critical bool) db.SafetySource {
	t.Helper()
	src, err := store.SafetySources.Create(t.Context(), db.CreateSafetySourceParams{
		ID: id.New(), UserID: userID, Name: "Kitchen", Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("create a safety source: %v", err)
	}
	src, err = store.SafetySources.Update(t.Context(), db.UpdateSafetySourceParams{
		ID: src.ID, UserID: userID, Kind: db.Value(db.SafetyKindSmoke), Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("classify the safety source: %v", err)
	}
	if !critical {
		return *src
	}
	src, err = store.SafetySources.Update(t.Context(), db.UpdateSafetySourceParams{
		ID: src.ID, UserID: userID, CriticalEnabled: db.Value(true), Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("enable critical delivery: %v", err)
	}
	return *src
}

// safetyFeedItem finds the one safety event in the account's history.
func safetyFeedItem(t *testing.T, store *db.Store, userID string) db.FeedItem {
	t.Helper()
	feed, err := store.Feed.List(t.Context(), userID, db.FeedFilterAll, db.Cursor{}, 10)
	if err != nil {
		t.Fatalf("list the feed: %v", err)
	}
	for _, item := range feed.Items {
		if strings.HasPrefix(item.ID, db.FeedSourceSafetyEvent+":") {
			return item
		}
	}
	t.Fatalf("no safety event in the feed: %+v", feed.Items)
	return db.FeedItem{}
}

func TestSafetySourceLifecycleThroughTheDashboard(t *testing.T) {
	d, store, userID := newPGDashboard(t)
	ctx := t.Context()

	form := "name=Home+Assistant"
	rec := send(d, withCSRF(t, d, asOwner(signedIn(http.MethodPost, pathSafety, ""), userID), form))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create: status = %d, want %d: %s", rec.Code, http.StatusSeeOther, rec.Body)
	}
	if want := pathSafety + "?done=safety_created"; rec.Header().Get("Location") != want {
		t.Fatalf("create: Location = %q, want %q", rec.Header().Get("Location"), want)
	}
	sources, err := store.SafetySources.ListForUser(ctx, userID)
	if err != nil || len(sources) != 1 {
		t.Fatalf("sources = %v, %v; want the one just created", sources, err)
	}
	src := sources[0]
	if src.Kind != db.SafetyKindGeneral || src.Name != "Home Assistant" || src.CriticalEnabled {
		t.Errorf("created source = %+v, want a general Home Assistant source with critical off", src)
	}

	rec = send(d, asOwner(signedIn(http.MethodGet, pathSafety, ""), userID))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `data-safety-source="`+src.ID+`"`) {
		t.Fatalf("list: status = %d, or the source is missing:\n%s", rec.Code, rec.Body)
	}

	form = "name=Home+Assistant&kind=" + db.SafetyKindGeneral + "&critical_enabled=on"
	rec = send(d, withCSRF(t, d, asOwner(signedIn(http.MethodPost, pathSafety+"/"+src.ID, ""), userID), form))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("enable general source: status = %d, want %d: %s", rec.Code, http.StatusUnprocessableEntity, rec.Body)
	}

	form = "name=Home+Assistant&kind=" + db.SafetyKindWaterLeak + "&critical_enabled=on"
	rec = send(d, withCSRF(t, d, asOwner(signedIn(http.MethodPost, pathSafety+"/"+src.ID, ""), userID), form))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("update: status = %d: %s", rec.Code, rec.Body)
	}
	reloaded, err := store.SafetySources.ByID(ctx, src.ID, userID)
	if err != nil {
		t.Fatalf("reload the source: %v", err)
	}
	if reloaded.Name != "Home Assistant" || !reloaded.CriticalEnabled || reloaded.Kind != db.SafetyKindWaterLeak {
		t.Errorf("updated source = %+v, want Home Assistant classified for critical water-leak alerts", reloaded)
	}

	rec = send(d, withCSRF(t, d, asOwner(signedIn(http.MethodPost, pathSafety+"/"+src.ID, ""), userID), "name=Home+Assistant&kind="+db.SafetyKindWaterLeak))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("update off: status = %d: %s", rec.Code, rec.Body)
	}
	reloaded, err = store.SafetySources.ByID(ctx, src.ID, userID)
	if err != nil {
		t.Fatalf("reload the source: %v", err)
	}
	if reloaded.CriticalEnabled {
		t.Error("unchecking the critical box did not disable critical delivery")
	}

	rec = send(d, withCSRF(t, d, asOwner(signedIn(http.MethodPost, pathSafety+"/"+src.ID+"/delete", ""), userID), ""))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("delete: status = %d: %s", rec.Code, rec.Body)
	}
	if _, err := store.SafetySources.ByID(ctx, src.ID, userID); err == nil {
		t.Error("the source still exists after delete")
	}
}

func TestSafetySettingsToggleThroughTheDashboard(t *testing.T) {
	d, store, userID := newPGDashboard(t)
	ctx := t.Context()

	rec := send(d, withCSRF(t, d, asOwner(signedIn(http.MethodPost, pathSafety+"/settings", ""), userID), ""))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("disable: status = %d: %s", rec.Code, rec.Body)
	}
	if want := pathSafety + "?done=safety_settings_saved"; rec.Header().Get("Location") != want {
		t.Errorf("disable: Location = %q, want %q", rec.Header().Get("Location"), want)
	}
	user, err := store.Users.ByID(ctx, userID)
	if err != nil {
		t.Fatalf("reload the account: %v", err)
	}
	if user.CriticalAlertsEnabled {
		t.Error("the global toggle is still on after an empty form")
	}

	rec = send(d, withCSRF(t, d, asOwner(signedIn(http.MethodPost, pathSafety+"/settings", ""), userID), "critical_alerts_enabled=on"))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("enable: status = %d: %s", rec.Code, rec.Body)
	}
	user, err = store.Users.ByID(ctx, userID)
	if err != nil {
		t.Fatalf("reload the account: %v", err)
	}
	if !user.CriticalAlertsEnabled {
		t.Error("the global toggle is still off after checking the box")
	}
}

func TestSafetyCreateRejectsABadForm(t *testing.T) {
	d, store, userID := newPGDashboard(t)

	rec := send(d, withCSRF(t, d, asOwner(signedIn(http.MethodPost, pathSafety, ""), userID), "name="))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusUnprocessableEntity, rec.Body)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `data-notice="error"`) {
		t.Errorf("the page does not carry an error banner:\n%s", body)
	}
	if sources, err := store.SafetySources.ListForUser(t.Context(), userID); err != nil || len(sources) != 0 {
		t.Errorf("sources = %v, %v; want none created", sources, err)
	}
}

func TestSafetyPagesAnswer404ForAForeignSource(t *testing.T) {
	d, store, userID := newPGDashboard(t)

	other, err := store.Users.Create(t.Context(), db.CreateUserParams{
		ID: id.New(), Username: "intruder", PasswordHash: ptr("not-a-real-hash"),
		Email: "intruder@hark.local", Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("create the second account: %v", err)
	}
	src := mustSafetySource(t, store, userID, false)

	for _, target := range []string{
		pathSafety + "/" + src.ID,
		pathSafety + "/" + src.ID + "/test",
		pathSafety + "/" + src.ID + "/delete",
	} {
		rec := send(d, withCSRF(t, d, asOwner(signedIn(http.MethodPost, target, ""), other.ID), "name=Mine"))
		if rec.Code != http.StatusNotFound {
			t.Errorf("POST %s as a stranger: status = %d, want 404", target, rec.Code)
		}
	}
	if _, err := store.SafetySources.ByID(t.Context(), src.ID, userID); err != nil {
		t.Errorf("the source did not survive the stranger's requests: %v", err)
	}
}

func TestSafetyTestSendsACriticalAlertAndRecordsIt(t *testing.T) {
	d, store, userID, sender := newSendTestDashboard(t, push.AlertResult{Accepted: 1})
	src := mustSafetySource(t, store, userID, true)
	device := mustDashDevice(t, store, userID, strings.Repeat("ab", 32))

	rec := send(d, withCSRF(t, d, asOwner(signedIn(http.MethodPost, pathSafety+"/"+src.ID+"/test", ""), userID), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `data-notice="ok"`) {
		t.Errorf("the page does not carry an ok banner:\n%s", rec.Body)
	}

	if len(sender.sent) != 1 || len(sender.sent[0]) != 1 {
		t.Fatalf("fan-outs = %v, want one alert to the one device", sender.sent)
	}
	alert := sender.sent[0][0]
	if alert.Target.DeviceID != device.ID || alert.Target.Token != device.APNsToken {
		t.Errorf("alert target = %+v, want the registered device", alert.Target)
	}
	if alert.Priority != db.PriorityCritical {
		t.Errorf("alert priority = %q, want %q", alert.Priority, db.PriorityCritical)
	}
	if alert.SourceID != src.ID || alert.SourceName != src.Name || alert.ThreadKey != "safety-"+src.ID {
		t.Errorf("alert = %+v, want it attributed to the source", alert)
	}

	// Setup tests appear in history.
	item := safetyFeedItem(t, store, userID)
	if item.SourceName != src.Name || item.Title != alert.Title {
		t.Errorf("feed item = %+v, want the pushed alert", item)
	}
	if item.Status == nil || *item.Status != db.EventAccepted {
		t.Errorf("feed status = %v, want %q", item.Status, db.EventAccepted)
	}
	if item.Priority == nil || *item.Priority != db.PriorityCritical {
		t.Errorf("feed priority = %v, want %q", item.Priority, db.PriorityCritical)
	}
	if alert.RecordID != strings.TrimPrefix(item.ID, db.FeedSourceSafetyEvent+":") {
		t.Errorf("alert record = %q, want the feed row %q", alert.RecordID, item.ID)
	}
}

func TestSafetyTestDowngradesWhenCriticalIsOff(t *testing.T) {
	d, store, userID, sender := newSendTestDashboard(t, push.AlertResult{Accepted: 1})
	src := mustSafetySource(t, store, userID, false)
	mustDashDevice(t, store, userID, strings.Repeat("ab", 32))

	rec := send(d, withCSRF(t, d, asOwner(signedIn(http.MethodPost, pathSafety+"/"+src.ID+"/test", ""), userID), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	// A disabled source setting downgrades the test.
	if !strings.Contains(rec.Body.String(), `data-notice="warn"`) {
		t.Errorf("the page does not carry a warning banner:\n%s", rec.Body)
	}
	if len(sender.sent) != 1 || len(sender.sent[0]) != 1 {
		t.Fatalf("fan-outs = %v, want the downgraded push to go out", sender.sent)
	}
	if got := sender.sent[0][0].Priority; got != db.PriorityTimeSensitive {
		t.Errorf("alert priority = %q, want %q", got, db.PriorityTimeSensitive)
	}
}

func TestSafetyTestIsThrottled(t *testing.T) {
	d, store, userID, sender := newSendTestDashboard(t, push.AlertResult{Accepted: 1})
	src := mustSafetySource(t, store, userID, true)
	mustDashDevice(t, store, userID, strings.Repeat("ab", 32))

	rec := send(d, withCSRF(t, d, asOwner(signedIn(http.MethodPost, pathSafety+"/"+src.ID+"/test", ""), userID), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("first test: status = %d: %s", rec.Code, rec.Body)
	}

	rec = send(d, withCSRF(t, d, asOwner(signedIn(http.MethodPost, pathSafety+"/"+src.ID+"/test", ""), userID), ""))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("second test: status = %d, want %d: %s", rec.Code, http.StatusSeeOther, rec.Body)
	}
	if want := pathSafety + "?done=safety_test_limited"; rec.Header().Get("Location") != want {
		t.Errorf("second test: Location = %q, want %q", rec.Header().Get("Location"), want)
	}
	if len(sender.sent) != 1 {
		t.Errorf("the transport was called %d times, want the throttle to stop the second", len(sender.sent))
	}
	count, err := store.SafetyEvents.CountPushedForSourceStateSince(t.Context(),
		src.ID, db.SafetyStateTest, time.Now().Add(-time.Hour))
	if err != nil || count != 1 {
		t.Errorf("recorded tests = %d, %v; want the throttled attempt to leave no row", count, err)
	}
}

func TestSafetyTestWarnsWhenNoDeviceIsRegistered(t *testing.T) {
	d, store, userID, sender := newSendTestDashboard(t, push.AlertResult{})
	src := mustSafetySource(t, store, userID, true)

	rec := send(d, withCSRF(t, d, asOwner(signedIn(http.MethodPost, pathSafety+"/"+src.ID+"/test", ""), userID), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `data-notice="warn"`) {
		t.Errorf("the page does not carry a warning banner:\n%s", rec.Body)
	}
	if len(sender.sent) != 0 {
		t.Errorf("a push went out with no device to receive it: %v", sender.sent)
	}
	// The attempt is still recorded.
	item := safetyFeedItem(t, store, userID)
	if item.Status == nil || *item.Status != db.EventNoDevices {
		t.Errorf("feed status = %v, want %q", item.Status, db.EventNoDevices)
	}
}

func TestSafetyTestRetiresTheDevicesAPNsDisowned(t *testing.T) {
	stale := strings.Repeat("ab", 32)
	d, store, userID, sender := newSendTestDashboard(t, push.AlertResult{
		Accepted:    1,
		Failures:    []string{"APNs request failed: Unregistered"},
		StaleTokens: []string{stale},
	})
	src := mustSafetySource(t, store, userID, true)
	dead := mustDashDevice(t, store, userID, stale)
	healthy := mustDashDevice(t, store, userID, strings.Repeat("cd", 32))

	rec := send(d, withCSRF(t, d, asOwner(signedIn(http.MethodPost, pathSafety+"/"+src.ID+"/test", ""), userID), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if len(sender.sent) != 1 || len(sender.sent[0]) != 2 {
		t.Fatalf("fan-outs = %v, want one push to both devices", sender.sent)
	}

	reloaded, err := store.Devices.ByID(t.Context(), dead.ID, userID)
	if err != nil {
		t.Fatalf("reload the stale device: %v", err)
	}
	if reloaded.Active {
		t.Error("the stale device is still active")
	}
	reloaded, err = store.Devices.ByID(t.Context(), healthy.ID, userID)
	if err != nil {
		t.Fatalf("reload the healthy device: %v", err)
	}
	if !reloaded.Active {
		t.Error("the healthy device was deactivated alongside the stale one")
	}

	item := safetyFeedItem(t, store, userID)
	if item.Status == nil || *item.Status != db.EventPartial {
		t.Errorf("feed status = %v, want %q", item.Status, db.EventPartial)
	}
}

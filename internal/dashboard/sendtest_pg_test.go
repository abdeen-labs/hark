package dashboard

// The send-a-test page against a real store, on the same schema and skip rule
// as services_pg_test.go. The push transport is a fake: these tests are about
// what the handler asks of it and what the page says about the answer, not
// about APNs.

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/id"
	"github.com/abdeen-labs/hark/internal/push"
)

// fakeSender stands in for the APNs transport. It keeps every fan-out it was
// handed and answers each with the same configured result, so a test picks the
// outcome — accepted, partly failed, token disowned — without credentials.
type fakeSender struct {
	result push.AlertResult
	sent   [][]push.Alert
}

func (f *fakeSender) SendAlerts(_ context.Context, alerts []push.Alert) push.AlertResult {
	f.sent = append(f.sent, alerts)
	return f.result
}

func (f *fakeSender) SendActivity(context.Context, push.ActivityEvent) push.ActivityResult {
	return push.ActivityResult{}
}

// newSendTestDashboard is newPGDashboard with the push transport swapped for a
// fake, which is the one dependency this page adds over the others.
func newSendTestDashboard(t *testing.T, result push.AlertResult) (*Dashboard, *db.Store, string, *fakeSender) {
	t.Helper()
	d, store, userID := newPGDashboard(t)
	sender := &fakeSender{result: result}
	d.opts.Push = sender
	return d, store, userID, sender
}

// mustDashDevice registers one active device under the token, as the app's
// registration call would have.
func mustDashDevice(t *testing.T, store *db.Store, userID, apnsToken string) db.Device {
	t.Helper()
	reg, err := store.Devices.Register(t.Context(), db.RegisterDeviceParams{
		ID: id.New(), UserID: userID, APNsToken: apnsToken, Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("register a device: %v", err)
	}
	return reg.Device
}

// postTest submits the form with a valid CSRF token, as the page's own form
// would.
func postTest(t *testing.T, d *Dashboard, userID, form string) *http.Request {
	t.Helper()
	return withCSRF(t, d, asOwner(signedIn(http.MethodPost, pathTest, ""), userID), form)
}

func TestSendTestFansOutToEveryActiveDevice(t *testing.T) {
	d, store, userID, sender := newSendTestDashboard(t, push.AlertResult{Accepted: 2})
	first := mustDashDevice(t, store, userID, strings.Repeat("ab", 32))
	second := mustDashDevice(t, store, userID, strings.Repeat("cd", 32))

	rec := send(d, postTest(t, d, userID, "body=Ping&priority="+db.PriorityTimeSensitive))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `<span class="mono">2</span> of <span class="mono">2</span> messages accepted`) {
		t.Errorf("the result plate does not show 2 of 2 accepted:\n%s", body)
	}
	if !strings.Contains(body, "APNs accepted every message.") {
		t.Errorf("the page does not carry the all-accepted banner:\n%s", body)
	}

	if len(sender.sent) != 1 {
		t.Fatalf("the transport was called %d times, want 1", len(sender.sent))
	}
	alerts := sender.sent[0]
	if len(alerts) != 2 {
		t.Fatalf("the fan-out holds %d alerts, want one per device", len(alerts))
	}
	targets := map[string]string{}
	for _, alert := range alerts {
		targets[alert.Target.DeviceID] = alert.Target.Token
		// The form's title was empty, so the alert falls back to the product
		// name rather than going out blank.
		if alert.Title != defaultTestTitle || alert.Body != "Ping" || alert.Priority != db.PriorityTimeSensitive {
			t.Errorf("alert = %+v, want the submitted body and priority under the default title", alert)
		}
		if alert.SourceID != testSource || alert.ThreadKey != testSource {
			t.Errorf("alert source = %q thread = %q, want %q", alert.SourceID, alert.ThreadKey, testSource)
		}
	}
	for _, device := range []db.Device{first, second} {
		if targets[device.ID] != device.APNsToken {
			t.Errorf("device %s was pushed token %q, want its own %q", device.ID, targets[device.ID], device.APNsToken)
		}
	}
}

func TestSendTestTargetsOnlyTheChosenDevice(t *testing.T) {
	d, store, userID, sender := newSendTestDashboard(t, push.AlertResult{Accepted: 1})
	chosen := mustDashDevice(t, store, userID, strings.Repeat("ab", 32))
	mustDashDevice(t, store, userID, strings.Repeat("cd", 32))

	rec := send(d, postTest(t, d, userID, "body=Ping&device_id="+chosen.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if len(sender.sent) != 1 || len(sender.sent[0]) != 1 {
		t.Fatalf("fan-outs = %v, want one alert to the chosen device", sender.sent)
	}
	if got := sender.sent[0][0].Target.DeviceID; got != chosen.ID {
		t.Errorf("the alert went to %q, want %q", got, chosen.ID)
	}
}

func TestSendTestRequiresABody(t *testing.T) {
	d, store, userID, sender := newSendTestDashboard(t, push.AlertResult{Accepted: 1})
	mustDashDevice(t, store, userID, strings.Repeat("ab", 32))

	// Whitespace only, so the rejection covers the trim as well as the check.
	rec := send(d, postTest(t, d, userID, "title=Hi&body=++"))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusUnprocessableEntity, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "A body is required.") {
		t.Errorf("the page does not say what was missing:\n%s", rec.Body)
	}
	if len(sender.sent) != 0 {
		t.Errorf("a push went out despite the rejected form: %v", sender.sent)
	}
}

func TestSendTestWarnsWhenNoDeviceIsRegistered(t *testing.T) {
	d, _, userID, sender := newSendTestDashboard(t, push.AlertResult{})

	rec := send(d, postTest(t, d, userID, "body=Ping"))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusUnprocessableEntity, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "No active device is registered to send to.") {
		t.Errorf("the page does not explain that there is nothing to send to:\n%s", rec.Body)
	}
	if len(sender.sent) != 0 {
		t.Errorf("a push went out with no device to receive it: %v", sender.sent)
	}
}

func TestSendTestRetiresTheDevicesAPNsDisowned(t *testing.T) {
	stale := strings.Repeat("ab", 32)
	d, store, userID, sender := newSendTestDashboard(t, push.AlertResult{
		Accepted:    1,
		Failures:    []string{"APNs request failed: Unregistered"},
		StaleTokens: []string{stale},
	})
	dead := mustDashDevice(t, store, userID, stale)
	healthy := mustDashDevice(t, store, userID, strings.Repeat("cd", 32))

	rec := send(d, postTest(t, d, userID, "body=Ping"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if len(sender.sent) != 1 || len(sender.sent[0]) != 2 {
		t.Fatalf("fan-outs = %v, want one push to both devices", sender.sent)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `<span class="mono">1</span> of <span class="mono">2</span> messages accepted`) {
		t.Errorf("the result plate does not show 1 of 2 accepted:\n%s", body)
	}
	// The provider's own words reach the page: this reader owns the account.
	if !strings.Contains(body, "APNs request failed: Unregistered") {
		t.Errorf("the failure reason is not shown:\n%s", body)
	}
	if !strings.Contains(body, "APNs accepted some of the messages.") {
		t.Errorf("the page does not carry the partial-acceptance banner:\n%s", body)
	}

	// The disowned token retires its device — and only its device.
	reloaded, err := store.Devices.ByID(t.Context(), dead.ID, userID)
	if err != nil {
		t.Fatalf("reload the stale device: %v", err)
	}
	if reloaded.Active {
		t.Error("the device APNs disowned is still active")
	}
	reloaded, err = store.Devices.ByID(t.Context(), healthy.ID, userID)
	if err != nil {
		t.Fatalf("reload the healthy device: %v", err)
	}
	if !reloaded.Active {
		t.Error("the healthy device was deactivated alongside the stale one")
	}
}

package apns

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/abdeen-labs/hark/internal/push"
)

func newTestSender(t *testing.T, fake *fakeAPNs, adjust func(*Config)) *Sender {
	t.Helper()
	return NewSender(newTestClient(t, fake, adjust))
}

// alertsFor addresses the same notification to n devices.
func alertsFor(n int) []push.Alert {
	alerts := make([]push.Alert, n)
	for i := range alerts {
		alert := sampleAlert()
		alert.Target = push.Target{
			DeviceID: "device-" + strconv.Itoa(i),
			Token:    "token-" + strconv.Itoa(i),
		}
		alerts[i] = alert
	}
	return alerts
}

func TestSendAlertsAccepted(t *testing.T) {
	fake := newFakeAPNs(t)
	sender := newTestSender(t, fake, nil)

	result := sender.SendAlerts(context.Background(), alertsFor(3))
	if result.Accepted != 3 {
		t.Errorf("accepted = %d, want 3", result.Accepted)
	}
	if len(result.Failures) != 0 {
		t.Errorf("failures = %v, want none", result.Failures)
	}
	if len(result.StaleTokens) != 0 {
		t.Errorf("stale tokens = %v, want none", result.StaleTokens)
	}
	if fake.count() != 3 {
		t.Errorf("APNs saw %d requests, want 3 — one per device", fake.count())
	}
}

func TestSendAlertsEmpty(t *testing.T) {
	fake := newFakeAPNs(t)
	sender := newTestSender(t, fake, nil)

	result := sender.SendAlerts(context.Background(), nil)
	if result.Accepted != 0 || len(result.Failures) != 0 {
		t.Errorf("result = %+v, want the zero value", result)
	}
	if fake.count() != 0 {
		t.Error("a fan-out with no devices opened a connection")
	}
}

// TestSendAlertsPartial is the ordinary mixed outcome: one phone is gone, the
// rest are fine, and the fan-out settles all of them.
func TestSendAlertsPartial(t *testing.T) {
	fake := newFakeAPNs(t)
	fake.answerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("apns-id", "8B7A2F0C-3D5E-4A1B-9C6D-2E4F6A8B0C1D")
		switch {
		case strings.HasSuffix(r.URL.Path, "token-1"):
			w.WriteHeader(http.StatusGone)
			_, _ = w.Write([]byte(`{"reason":"Unregistered"}`))
		case strings.HasSuffix(r.URL.Path, "token-2"):
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"reason":"TooManyRequests"}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	})
	sender := newTestSender(t, fake, nil)

	result := sender.SendAlerts(context.Background(), alertsFor(4))
	if result.Accepted != 2 {
		t.Errorf("accepted = %d, want 2", result.Accepted)
	}

	if len(result.Failures) != 2 {
		t.Errorf("failures = %v, want two", result.Failures)
	}

	// Only the permanently dead token is pruned. A rate-limited push is a
	// perfectly good device that Apple asked us to slow down about.
	if !slices.Equal(result.StaleTokens, []string{"token-1"}) {
		t.Errorf("stale tokens = %v, want [token-1]", result.StaleTokens)
	}
}

// TestSendAlertsPrunesDeadTokens covers every answer that means "this token
// will never work again", and the two that do not.
func TestSendAlertsPrunesDeadTokens(t *testing.T) {
	tests := []struct {
		name   string
		status int
		reason string
		prune  bool
	}{
		{"unregistered", 410, ReasonUnregistered, true},
		{"gone without a reason", 410, "", true},
		{"bad device token", 400, ReasonBadDeviceToken, true},
		{"expired token", 400, ReasonExpiredToken, true},
		{"wrong topic", 400, ReasonDeviceTokenNotForTopic, false},
		{"payload too large", 413, ReasonPayloadTooLarge, false},
		{"apple is down", 503, "ServiceUnavailable", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeAPNs(t)
			fake.answer(tt.status, tt.reason)
			sender := newTestSender(t, fake, nil)

			result := sender.SendAlerts(context.Background(), alertsFor(1))
			if result.Accepted != 0 {
				t.Fatalf("accepted = %d, want 0", result.Accepted)
			}
			if pruned := len(result.StaleTokens) == 1; pruned != tt.prune {
				t.Errorf("pruned = %v, want %v (stale tokens %v)", pruned, tt.prune, result.StaleTokens)
			}
		})
	}
}

// TestSendAlertsConcurrency checks the fan-out is bounded and complete: more
// messages than workers still delivers every one of them.
func TestSendAlertsConcurrency(t *testing.T) {
	fake := newFakeAPNs(t)
	sender := newTestSender(t, fake, nil)

	const devices = alertConcurrency * 3
	result := sender.SendAlerts(context.Background(), alertsFor(devices))
	if result.Accepted != devices {
		t.Errorf("accepted = %d, want %d", result.Accepted, devices)
	}

	// Every device was addressed exactly once.
	seen := map[string]int{}
	for _, request := range fake.recorded() {
		seen[request.Path]++
	}
	if len(seen) != devices {
		t.Errorf("%d distinct devices were pushed to, want %d", len(seen), devices)
	}
	for path, n := range seen {
		if n != 1 {
			t.Errorf("%s was pushed to %d times", path, n)
		}
	}
}

// TestSendAlertsTransportFailure checks the failure string a caller records
// when there was no response at all — and that it never quotes the underlying
// error, which carries the request URL and therefore a device token.
func TestSendAlertsTransportFailure(t *testing.T) {
	fake := newFakeAPNs(t)
	fake.server.Close() // nothing is listening any more
	sender := newTestSender(t, fake, nil)

	result := sender.SendAlerts(context.Background(), alertsFor(1))
	if result.Accepted != 0 {
		t.Fatalf("accepted = %d, want 0", result.Accepted)
	}
	if len(result.Failures) != 1 {
		t.Fatalf("failures = %v, want one", result.Failures)
	}
	if strings.Contains(result.Failures[0], "token-0") {
		t.Errorf("the failure string leaks the device token: %q", result.Failures[0])
	}
	if len(result.StaleTokens) != 0 {
		t.Error("a transport failure retired a device token")
	}
}

func TestSendAlertsBadPayload(t *testing.T) {
	fake := newFakeAPNs(t)
	sender := newTestSender(t, fake, nil)

	alert := sampleAlert()
	alert.Body = strings.Repeat("x", MaxPayloadBytes)

	result := sender.SendAlerts(context.Background(), []push.Alert{alert})
	if fake.count() != 0 {
		t.Error("an oversized payload was sent to APNs")
	}
	if len(result.Failures) != 1 {
		t.Errorf("failures = %v, want one", result.Failures)
	}
}

func startEvent() push.ActivityEvent {
	event := sampleActivity(push.EventStart)
	event.Start = &push.ActivityStart{
		Banner:               push.ActivityBanner{Title: "Deploy", Body: "Building"},
		TokenRegistrationURL: "https://hark.example/activity-deliveries/lad/update-token",
		RegistrationToken:    "registration-token",
	}
	return event
}

func TestSendActivityAccepted(t *testing.T) {
	fake := newFakeAPNs(t)
	sender := newTestSender(t, fake, nil)

	result := sender.SendActivity(context.Background(), startEvent())
	if !result.Accepted {
		t.Fatalf("result = %+v, want accepted", result)
	}
	if result.APNsStatus == nil || *result.APNsStatus != 200 {
		t.Errorf("apns status = %v, want 200", result.APNsStatus)
	}
	if result.Reason != nil {
		t.Errorf("reason = %v, want nil on success", *result.Reason)
	}
	if result.APNsID == nil || *result.APNsID == "" {
		t.Error("the apns-id was not recorded")
	}
	if result.TokenInvalid {
		t.Error("an accepted push retired its token")
	}

	got := fake.last(t)
	if got.Headers.Get("apns-push-type") != "liveactivity" {
		t.Errorf("apns-push-type = %q", got.Headers.Get("apns-push-type"))
	}
	if want := "dev.abdeen.hark.push-type.liveactivity"; got.Headers.Get("apns-topic") != want {
		t.Errorf("apns-topic = %q, want %q", got.Headers.Get("apns-topic"), want)
	}
	if got.Path != "/3/device/0a1b2c" {
		t.Errorf("path = %q, want the activity push token", got.Path)
	}
}

// TestSendActivityEnvironmentMismatch checks the refusal that happens before a
// connection is opened. An ActivityKit token minted against sandbox is not a
// token this production process can use, and pretending otherwise would look
// like a dead token instead of a build talking to the wrong host.
func TestSendActivityEnvironmentMismatch(t *testing.T) {
	fake := newFakeAPNs(t)
	sender := newTestSender(t, fake, nil)

	event := startEvent()
	event.Environment = EnvironmentProduction

	result := sender.SendActivity(context.Background(), event)
	if result.Accepted {
		t.Fatal("a mismatched environment was accepted")
	}
	if result.Reason == nil || *result.Reason != push.ReasonEnvironmentMismatch {
		t.Errorf("reason = %v, want %q", result.Reason, push.ReasonEnvironmentMismatch)
	}
	if result.APNsStatus != nil {
		t.Errorf("apns status = %v, want nil — no call was made", *result.APNsStatus)
	}
	if fake.count() != 0 {
		t.Error("a mismatched environment still opened a connection")
	}
}

func TestSendActivityDeadToken(t *testing.T) {
	fake := newFakeAPNs(t)
	fake.answer(http.StatusGone, ReasonUnregistered)
	sender := newTestSender(t, fake, nil)

	result := sender.SendActivity(context.Background(), startEvent())
	if result.Accepted {
		t.Fatal("a 410 was accepted")
	}
	if !result.TokenInvalid {
		t.Error("a 410 Unregistered did not retire the push token")
	}
	if result.Reason == nil || *result.Reason != ReasonUnregistered {
		t.Errorf("reason = %v, want %q", result.Reason, ReasonUnregistered)
	}
	if result.APNsStatus == nil || *result.APNsStatus != http.StatusGone {
		t.Errorf("apns status = %v, want 410", result.APNsStatus)
	}
}

// TestSendActivityWrongTopic is the asymmetry worth stating out loud: a topic
// Apple refuses indicts this server's bundle id, not the phone's token, so
// nothing is pruned for it.
func TestSendActivityWrongTopic(t *testing.T) {
	fake := newFakeAPNs(t)
	fake.answer(http.StatusBadRequest, ReasonDeviceTokenNotForTopic)
	sender := newTestSender(t, fake, nil)

	result := sender.SendActivity(context.Background(), startEvent())
	if result.TokenInvalid {
		t.Error("a topic mismatch retired the push token")
	}
	if result.Reason == nil || *result.Reason != ReasonDeviceTokenNotForTopic {
		t.Errorf("reason = %v", result.Reason)
	}
}

func TestSendActivityOversizedPayload(t *testing.T) {
	fake := newFakeAPNs(t)
	sender := newTestSender(t, fake, nil)

	event := startEvent()
	event.State = json.RawMessage(`{"detail":"` + strings.Repeat("x", MaxPayloadBytes) + `"}`)

	result := sender.SendActivity(context.Background(), event)
	if result.Accepted {
		t.Fatal("an oversized payload was accepted")
	}
	if result.Reason == nil || *result.Reason != ReasonPayloadTooLarge {
		t.Errorf("reason = %v, want %q", result.Reason, ReasonPayloadTooLarge)
	}
	if result.APNsStatus != nil {
		t.Error("an oversized payload reported an APNs status")
	}
	if fake.count() != 0 {
		t.Error("an oversized payload was sent to APNs")
	}
}

// TestSendActivityRefusalWithoutReason checks the fallback: a delivery whose
// log says nothing at all is worse than one that says only that it failed.
func TestSendActivityRefusalWithoutReason(t *testing.T) {
	fake := newFakeAPNs(t)
	fake.answer(http.StatusServiceUnavailable, "")
	sender := newTestSender(t, fake, nil)

	result := sender.SendActivity(context.Background(), startEvent())
	if result.Reason == nil || *result.Reason != push.ReasonDeliveryFailed {
		t.Errorf("reason = %v, want %q", result.Reason, push.ReasonDeliveryFailed)
	}
}

func TestSendActivityEvents(t *testing.T) {
	for _, event := range []string{push.EventUpdate, push.EventEnd} {
		t.Run(event, func(t *testing.T) {
			fake := newFakeAPNs(t)
			sender := newTestSender(t, fake, nil)

			result := sender.SendActivity(context.Background(), sampleActivity(event))
			if !result.Accepted {
				t.Fatalf("result = %+v, want accepted", result)
			}

			var payload struct {
				APS struct {
					Event string `json:"event"`
				} `json:"aps"`
			}
			if err := json.Unmarshal(fake.last(t).Body, &payload); err != nil {
				t.Fatalf("decoding the delivered payload: %v", err)
			}
			if payload.APS.Event != event {
				t.Errorf("aps.event = %q, want %q", payload.APS.Event, event)
			}
		})
	}
}

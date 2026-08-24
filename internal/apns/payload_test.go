package apns

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/abdeen-labs/hark/internal/push"
)

// decodePayload reads an encoded payload as the generic document received by a
// device.
func decodePayload(t *testing.T, encoded []byte) map[string]any {
	t.Helper()

	var out map[string]any
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatalf("decoding the payload: %v\n%s", err, encoded)
	}
	return out
}

func object(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()

	value, ok := parent[key]
	if !ok {
		t.Fatalf("%q is missing from %v", key, keysOf(parent))
	}
	child, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%q is %T, want an object", key, value)
	}
	return child
}

func keysOf(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func ptr[T any](v T) *T { return &v }

func sampleAlert() push.Alert {
	return push.Alert{
		Target:     push.Target{DeviceID: "0198f3a1-2b4c-7d8e-9f01-23456789abcd", Token: "0a1b2c"},
		Title:      "Acme CRM",
		Body:       "New sign-up",
		Priority:   "normal",
		ThreadKey:  "service-0198f3a1-2b4c-7d8e-9f01-000000000001",
		SourceID:   "0198f3a1-2b4c-7d8e-9f01-000000000001",
		SourceName: "Acme CRM",
		RecordID:   "0198f3a1-2b4c-7d8e-9f01-000000000002",
	}
}

// TestBuildAlert pins the whole shape of an ordinary notification: Apple's
// envelope and Hark's data, and nothing else in either.
func TestBuildAlert(t *testing.T) {
	alert := sampleAlert()
	alert.ImageURL = ptr("https://example.com/a.png")
	alert.URL = ptr("things:///show?id=abc")

	encoded, err := buildAlert(alert)
	if err != nil {
		t.Fatalf("buildAlert: %v", err)
	}
	payload := decodePayload(t, encoded)

	if len(payload) != 2 {
		t.Errorf("top-level keys are %v, want exactly aps and hark", keysOf(payload))
	}

	aps := object(t, payload, "aps")
	body := object(t, aps, "alert")
	if body["title"] != "Acme CRM" || body["body"] != "New sign-up" {
		t.Errorf("aps.alert = %v", body)
	}
	if aps["sound"] != "default" {
		t.Errorf("aps.sound = %v, want \"default\"", aps["sound"])
	}
	if aps["mutable-content"] != float64(1) {
		t.Errorf("aps.mutable-content = %v, want 1", aps["mutable-content"])
	}
	if aps["thread-id"] != alert.ThreadKey {
		t.Errorf("aps.thread-id = %v, want %q", aps["thread-id"], alert.ThreadKey)
	}
	if _, present := aps["interruption-level"]; present {
		t.Error("aps.interruption-level is present on a normal alert")
	}
	if _, present := aps["category"]; present {
		t.Error("aps.category is present on an alert that asks nothing")
	}
	// The badge belongs to the client: it counts unanswered questions, and it
	// knows the count sooner than any push does.
	if _, present := aps["badge"]; present {
		t.Error("aps.badge is present")
	}

	hark := object(t, payload, "hark")
	want := map[string]any{
		"schema_version": float64(PayloadSchemaVersion),
		"device_id":      alert.Target.DeviceID,
		"record_id":      alert.RecordID,
		"thread_key":     alert.ThreadKey,
		"url":            "things:///show?id=abc",
	}
	for key, value := range want {
		if hark[key] != value {
			t.Errorf("hark.%s = %v, want %v", key, hark[key], value)
		}
	}
	source := object(t, hark, "source")
	if source["id"] != alert.SourceID || source["name"] != alert.SourceName {
		t.Errorf("hark.source = %v", source)
	}
	if source["image_url"] != "https://example.com/a.png" {
		t.Errorf("hark.source.image_url = %v", source["image_url"])
	}
	if _, present := hark["question"]; present {
		t.Error("hark.question is present on an alert that asks nothing")
	}
}

// TestBuildAlertOmitsAbsentFields checks that "there is none" is an absent key
// rather than a null or an empty string a client has to special-case.
func TestBuildAlertOmitsAbsentFields(t *testing.T) {
	encoded, err := buildAlert(sampleAlert())
	if err != nil {
		t.Fatalf("buildAlert: %v", err)
	}

	hark := object(t, decodePayload(t, encoded), "hark")
	if _, present := hark["url"]; present {
		t.Error("hark.url is present with no tap destination")
	}
	if _, present := object(t, hark, "source")["image_url"]; present {
		t.Error("hark.source.image_url is present with no avatar")
	}
}

// TestBuildAlertPriorities covers the mapping onto Apple's sound and
// interruption level. The header priority is not here: every alert is sent at
// 10, which the client test asserts.
func TestBuildAlertPriorities(t *testing.T) {
	tests := []struct {
		priority string
		level    any
		sound    any
	}{
		{"normal", nil, "default"},
		{"", nil, "default"},
		{"time_sensitive", "time-sensitive", "default"},
		{"critical", "critical", map[string]any{
			"critical": float64(1), "name": "default", "volume": float64(1),
		}},
	}

	for _, tt := range tests {
		t.Run("priority="+tt.priority, func(t *testing.T) {
			alert := sampleAlert()
			alert.Priority = tt.priority

			encoded, err := buildAlert(alert)
			if err != nil {
				t.Fatalf("buildAlert: %v", err)
			}
			aps := object(t, decodePayload(t, encoded), "aps")

			level, present := aps["interruption-level"]
			switch {
			case tt.level == nil && present:
				t.Errorf("aps.interruption-level = %v, want it omitted", level)
			case tt.level != nil && level != tt.level:
				t.Errorf("aps.interruption-level = %v, want %v", level, tt.level)
			}

			switch want := tt.sound.(type) {
			case string:
				if aps["sound"] != want {
					t.Errorf("aps.sound = %v, want %q", aps["sound"], want)
				}
			case map[string]any:
				sound := object(t, aps, "sound")
				for k, v := range want {
					if sound[k] != v {
						t.Errorf("aps.sound.%s = %v, want %v", k, sound[k], v)
					}
				}
			}
		})
	}
}

func TestBuildAlertRejectsUnknownPriority(t *testing.T) {
	alert := sampleAlert()
	alert.Priority = "urgent"

	if _, err := buildAlert(alert); err == nil {
		t.Error("buildAlert accepted an unknown priority")
	}
}

// TestBuildAlertQuestion covers the variant that draws answer buttons.
func TestBuildAlertQuestion(t *testing.T) {
	expires := time.Date(2026, 8, 9, 13, 0, 0, 0, time.UTC)
	alert := sampleAlert()
	alert.Title = "Release"
	alert.Body = "Deploy production?"
	alert.Interaction = &push.AlertInteraction{
		ID:             "0198f3a1-2b4c-7d8e-9f01-000000000003",
		Kind:           "approval",
		ActionDigest:   strings.Repeat("a", 64),
		ResponseToken:  "TG9uZ0Vub3VnaFRvTG9va0xpa2VBUmVhbFRva2VuMDAw",
		PrimaryLabel:   "Ship it",
		SecondaryLabel: "Hold",
		ExpiresAt:      expires,
	}

	encoded, err := buildAlert(alert)
	if err != nil {
		t.Fatalf("buildAlert: %v", err)
	}
	payload := decodePayload(t, encoded)

	if category := object(t, payload, "aps")["category"]; category != CategoryApproval {
		t.Errorf("aps.category = %v, want %q", category, CategoryApproval)
	}

	question := object(t, object(t, payload, "hark"), "question")
	want := map[string]any{
		"id":              alert.Interaction.ID,
		"kind":            "approval",
		"category":        CategoryApproval,
		"action_digest":   alert.Interaction.ActionDigest,
		"response_token":  alert.Interaction.ResponseToken,
		"primary_label":   "Ship it",
		"secondary_label": "Hold",
		"expires_at":      "2026-08-09T13:00:00.000Z",
	}
	for key, value := range want {
		if question[key] != value {
			t.Errorf("hark.question.%s = %v, want %v", key, question[key], value)
		}
	}
}

func TestAlertQuestionCategories(t *testing.T) {
	tests := map[string]string{
		"approval":  CategoryApproval,
		"yes_no":    CategoryYesNo,
		"reply":     CategoryReply,
		"something": CategoryReply,
	}

	for kind, want := range tests {
		t.Run(kind, func(t *testing.T) {
			alert := sampleAlert()
			alert.Interaction = &push.AlertInteraction{ID: "q", Kind: kind, ActionDigest: "d"}

			encoded, err := buildAlert(alert)
			if err != nil {
				t.Fatalf("buildAlert: %v", err)
			}
			payload := decodePayload(t, encoded)
			if got := object(t, payload, "aps")["category"]; got != want {
				t.Errorf("aps.category = %v, want %q", got, want)
			}
			if got := object(t, object(t, payload, "hark"), "question")["category"]; got != want {
				t.Errorf("hark.question.category = %v, want %q", got, want)
			}
		})
	}
}

// TestAlertPayloadCarriesNoOwnerIdentity is the privacy guard. A push travels
// through Apple and lands on a lock screen; it names the sender and the row,
// never the account.
func TestAlertPayloadCarriesNoOwnerIdentity(t *testing.T) {
	alert := sampleAlert()
	alert.Interaction = &push.AlertInteraction{ID: "q", Kind: "yes_no", ActionDigest: "d"}

	encoded, err := buildAlert(alert)
	if err != nil {
		t.Fatalf("buildAlert: %v", err)
	}
	for _, forbidden := range []string{"user_id", "user", "password", "webhook", "secret"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Errorf("the payload contains %q:\n%s", forbidden, encoded)
		}
	}
}

func TestBuildAlertRefusesOversizedPayload(t *testing.T) {
	alert := sampleAlert()
	alert.Body = strings.Repeat("x", MaxPayloadBytes)

	_, err := buildAlert(alert)
	if err == nil {
		t.Fatal("buildAlert accepted a payload over the limit")
	}
	if !strings.Contains(err.Error(), "4096") {
		t.Errorf("the error does not name the limit: %v", err)
	}
	if got := buildReason(err); got != ReasonPayloadTooLarge {
		t.Errorf("buildReason = %q, want %q", got, ReasonPayloadTooLarge)
	}
}

func sampleActivity(event string) push.ActivityEvent {
	return push.ActivityEvent{
		Event:       event,
		PushToken:   "0a1b2c",
		Environment: EnvironmentSandbox,
		ActivityID:  "0198f3c2-1a5d-7b90-8c34-6e7f8a9b0c1d",
		DeliveryID:  "0198f3c2-1a5d-7b90-8c34-000000000001",
		Target:      push.Target{DeviceID: "0198f3a1-2b4c-7d8e-9f01-23456789abcd"},
		State:       json.RawMessage(`{"schema_version":1,"title":"Deploy","status":"Building"}`),
		Timestamp:   1786000000,
	}
}

// TestBuildActivityStart pins the one event that carries attributes: a start is
// the only chance to tell the phone where to report its update token.
func TestBuildActivityStart(t *testing.T) {
	stale := time.Unix(1786014400, 900_000_000).UTC()
	event := sampleActivity(push.EventStart)
	event.StaleAt = &stale
	event.Start = &push.ActivityStart{
		Banner:               push.ActivityBanner{Title: "Deploy", Body: "Building"},
		TokenRegistrationURL: "https://hark.example/v1/activity-deliveries/lad/update-token",
		RegistrationToken:    "registration-token",
	}

	encoded, err := buildActivity(event)
	if err != nil {
		t.Fatalf("buildActivity: %v", err)
	}
	payload := decodePayload(t, encoded)
	if len(payload) != 1 {
		t.Errorf("top-level keys are %v, want only aps", keysOf(payload))
	}

	aps := object(t, payload, "aps")
	if aps["event"] != "start" {
		t.Errorf("aps.event = %v", aps["event"])
	}
	if aps["timestamp"] != float64(1786000000) {
		t.Errorf("aps.timestamp = %v", aps["timestamp"])
	}
	if aps["attributes-type"] != ActivityAttributesType {
		t.Errorf("aps.attributes-type = %v, want %q", aps["attributes-type"], ActivityAttributesType)
	}
	if aps["input-push-token"] != float64(1) {
		t.Errorf("aps.input-push-token = %v, want 1", aps["input-push-token"])
	}
	// Fractional seconds are dropped, not rounded: a stale date that arrives a
	// moment early is better than a card that outlives its own deadline.
	if aps["stale-date"] != float64(1786014400) {
		t.Errorf("aps.stale-date = %v, want 1786014400", aps["stale-date"])
	}
	if _, present := aps["dismissal-date"]; present {
		t.Error("aps.dismissal-date is present on a start")
	}

	banner := object(t, aps, "alert")
	if banner["title"] != "Deploy" || banner["body"] != "Building" {
		t.Errorf("aps.alert = %v", banner)
	}

	// The content state is delivered verbatim: what a requester wrote is what
	// the widget renders, with no translation in between.
	state := object(t, aps, "content-state")
	if state["title"] != "Deploy" || state["status"] != "Building" {
		t.Errorf("aps.content-state = %v", state)
	}
	if state["schema_version"] != float64(1) {
		t.Errorf("aps.content-state.schema_version = %v", state["schema_version"])
	}

	attributes := object(t, aps, "attributes")
	want := map[string]any{
		"schema_version":           float64(PayloadSchemaVersion),
		"delivery_id":              event.DeliveryID,
		"device_id":                event.Target.DeviceID,
		"token_registration_url":   event.Start.TokenRegistrationURL,
		"token_registration_token": event.Start.RegistrationToken,
	}
	for key, value := range want {
		if attributes[key] != value {
			t.Errorf("aps.attributes.%s = %v, want %v", key, attributes[key], value)
		}
	}
	if _, present := attributes["question"]; present {
		t.Error("aps.attributes.question is present on a card that asks nothing")
	}
}

func TestBuildActivityStartWithQuestion(t *testing.T) {
	event := sampleActivity(push.EventStart)
	event.Start = &push.ActivityStart{
		Banner:               push.ActivityBanner{Title: "Release", Body: "Waiting for you"},
		TokenRegistrationURL: "https://hark.example/v1/activity-deliveries/lad/update-token",
		RegistrationToken:    "registration-token",
		Interaction: &push.ActivityInteraction{
			ID:            "0198f3a1-2b4c-7d8e-9f01-000000000003",
			ResponseToken: "response-token",
			ActionDigest:  strings.Repeat("b", 64),
		},
	}

	encoded, err := buildActivity(event)
	if err != nil {
		t.Fatalf("buildActivity: %v", err)
	}

	attributes := object(t, object(t, decodePayload(t, encoded), "aps"), "attributes")
	question := object(t, attributes, "question")
	if question["id"] != event.Start.Interaction.ID {
		t.Errorf("question.id = %v", question["id"])
	}
	if question["response_token"] != "response-token" {
		t.Errorf("question.response_token = %v", question["response_token"])
	}
	if question["action_digest"] != event.Start.Interaction.ActionDigest {
		t.Errorf("question.action_digest = %v", question["action_digest"])
	}
}

func TestBuildActivityStartNeedsAttributes(t *testing.T) {
	if _, err := buildActivity(sampleActivity(push.EventStart)); err == nil {
		t.Error("buildActivity accepted a start with no attributes")
	}
}

// TestBuildActivityUpdate checks that an update carries the state and the dates
// and nothing that only a start may set.
func TestBuildActivityUpdate(t *testing.T) {
	stale := time.Unix(1786014460, 0).UTC()
	event := sampleActivity(push.EventUpdate)
	event.StaleAt = &stale
	// A dismissal date that somehow reached an update is dropped: it would ask
	// iOS to remove a card that is still being driven.
	dismissal := time.Unix(1786000060, 0).UTC()
	event.DismissalAt = &dismissal

	encoded, err := buildActivity(event)
	if err != nil {
		t.Fatalf("buildActivity: %v", err)
	}
	aps := object(t, decodePayload(t, encoded), "aps")

	if aps["event"] != "update" {
		t.Errorf("aps.event = %v", aps["event"])
	}
	if aps["stale-date"] != float64(1786014460) {
		t.Errorf("aps.stale-date = %v", aps["stale-date"])
	}
	for _, key := range []string{"attributes", "attributes-type", "alert", "input-push-token", "dismissal-date"} {
		if _, present := aps[key]; present {
			t.Errorf("aps.%s is present on an update", key)
		}
	}
}

func TestBuildActivityEnd(t *testing.T) {
	dismissal := time.Unix(1786000300, 0).UTC()
	stale := time.Unix(1786014700, 0).UTC()
	event := sampleActivity(push.EventEnd)
	event.DismissalAt = &dismissal
	event.StaleAt = &stale

	encoded, err := buildActivity(event)
	if err != nil {
		t.Fatalf("buildActivity: %v", err)
	}
	aps := object(t, decodePayload(t, encoded), "aps")

	if aps["event"] != "end" {
		t.Errorf("aps.event = %v", aps["event"])
	}
	if aps["dismissal-date"] != float64(1786000300) {
		t.Errorf("aps.dismissal-date = %v", aps["dismissal-date"])
	}
	if aps["stale-date"] != float64(1786014700) {
		t.Errorf("aps.stale-date = %v", aps["stale-date"])
	}
	for _, key := range []string{"attributes", "attributes-type", "alert", "input-push-token"} {
		if _, present := aps[key]; present {
			t.Errorf("aps.%s is present on an end", key)
		}
	}
}

func TestBuildActivityOmitsAbsentDates(t *testing.T) {
	encoded, err := buildActivity(sampleActivity(push.EventEnd))
	if err != nil {
		t.Fatalf("buildActivity: %v", err)
	}

	aps := object(t, decodePayload(t, encoded), "aps")
	for _, key := range []string{"stale-date", "dismissal-date"} {
		if _, present := aps[key]; present {
			t.Errorf("aps.%s is present with no such time", key)
		}
	}
}

func TestBuildActivityRejectsUnknownEvent(t *testing.T) {
	if _, err := buildActivity(sampleActivity("restart")); err == nil {
		t.Error("buildActivity accepted an unknown event")
	}
}

func TestBuildActivityNeedsState(t *testing.T) {
	event := sampleActivity(push.EventUpdate)
	event.State = nil

	if _, err := buildActivity(event); err == nil {
		t.Error("buildActivity accepted an empty content state")
	}
}

func TestBuildActivityRefusesOversizedPayload(t *testing.T) {
	event := sampleActivity(push.EventUpdate)
	event.State = json.RawMessage(`{"detail":"` + strings.Repeat("x", MaxPayloadBytes) + `"}`)

	_, err := buildActivity(event)
	if err == nil {
		t.Fatal("buildActivity accepted a payload over the limit")
	}
	if !strings.Contains(err.Error(), "4096") {
		t.Errorf("the error does not name the limit: %v", err)
	}
	if got := buildReason(err); got != ReasonPayloadTooLarge {
		t.Errorf("buildReason = %q, want %q", got, ReasonPayloadTooLarge)
	}
}

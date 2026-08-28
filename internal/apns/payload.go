package apns

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/abdeen-labs/hark/internal/push"
)

// The `aps` dictionary uses Apple's hyphenated keys and values, including
// `mutable-content`, `interruption-level`, `content-state`, and
// `input-push-token`.
//
// Hark-specific notification data lives under one top-level `hark` key to avoid
// duplicate values. Its keys use snake_case, are versioned by
// [PayloadSchemaVersion], and are documented in docs/api.md under "Push
// payloads."

// PayloadSchemaVersion versions the Hark half of every payload. A client
// announces the version it understands when it registers; the server does not
// send a device a payload it cannot read.
const PayloadSchemaVersion = 1

// ActivityAttributesType names the Swift type ActivityKit reconstructs on the
// phone. It has to match the client's `ActivityAttributes` conformance exactly.
const ActivityAttributesType = "HarkActivityAttributes"

// Notification categories control the answer buttons displayed by iOS. The
// client registers these identifiers and actions at launch.
//
// Each is "hark." plus the question's kind plus a version, so the mapping is
// mechanical rather than a table anyone has to remember. The version is there
// because changing a category's actions while old notifications are still on a
// Lock Screen would relabel them.
const (
	CategoryApproval = "hark.approval.v1"
	CategoryYesNo    = "hark.yes_no.v1"
	CategoryReply    = "hark.reply.v1"
)

// Apple's interruption levels. `passive` and `active` are never sent: an alert
// Hark delivers is always something the owner asked to be told about, and
// `active` is the default anyway.
const (
	levelTimeSensitive = "time-sensitive"
	levelCritical      = "critical"
)

// Hark's priorities, as the API spells them.
const (
	priorityNormal        = "normal"
	priorityTimeSensitive = "time_sensitive"
	priorityCritical      = "critical"
)

// errPayloadTooLarge is what an over-cap payload resolves to. It carries the
// measurement so the log line is useful; the reason a caller sees is the flat
// PayloadTooLarge.
var errPayloadTooLarge = errors.New("payload exceeds the APNs limit")

// alertPayload is one notification.
type alertPayload struct {
	APS  apsAlert  `json:"aps"`
	Hark alertData `json:"hark"`
}

// apsAlert is Apple's half of a notification.
type apsAlert struct {
	Alert          alertText `json:"alert"`
	Sound          any       `json:"sound"`
	MutableContent int       `json:"mutable-content"`
	// InterruptionLevel is omitted for an ordinary alert, which is what Apple
	// reads as the default level.
	InterruptionLevel string `json:"interruption-level,omitempty"`
	// Category is set only on a question, and is what draws its buttons.
	Category string `json:"category,omitempty"`
	// ThreadID groups a sender's notifications into one conversation.
	ThreadID string `json:"thread-id,omitempty"`
	// No badge is ever sent: the count of unanswered questions is the client's
	// to keep, and it knows sooner than a push does.
}

type alertText struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// criticalSound is what bypasses a silent switch when the app carries the
// critical-alert entitlement and the owner has granted it. The server's two
// switches control whether this payload is emitted; they do not infer the
// entitlement or permission state of an individual device.
type criticalSound struct {
	Critical int     `json:"critical"`
	Name     string  `json:"name"`
	Volume   float64 `json:"volume"`
}

// alertData is Hark's half: who sent this, what row it came from, and what
// happens when it is tapped or answered.
type alertData struct {
	SchemaVersion int `json:"schema_version"`
	// DeviceID is the phone this copy was addressed to. The notification-service
	// extension and the Lock Screen both have to name it when they answer a
	// question, and neither can be relied on to have read it from local storage.
	DeviceID string `json:"device_id"`
	// RecordID is the row behind this notification — an event, a notification,
	// or a question — so a client can open the history entry it belongs to.
	RecordID string `json:"record_id"`
	// ThreadKey is the conversation this belongs to. It is the same string as
	// aps.thread-id, repeated here because the client groups its own inbox by
	// it too.
	ThreadKey string `json:"thread_key"`
	// URL is the tap destination: a web URL, a universal link, or a custom app
	// scheme. Omitted when the sender named none.
	URL string `json:"url,omitempty"`
	// Source is the sender as the phone should show it.
	Source alertSource `json:"source"`
	// Question is present exactly when this notification asks something.
	Question *alertQuestion `json:"question,omitempty"`
}

type alertSource struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// ImageURL is the sender's avatar, a public HTTPS URL the
	// notification-service extension downloads to draw a communication-style
	// notification. Omitted when the sender has none.
	ImageURL string `json:"image_url,omitempty"`
}

// alertQuestion is everything the phone needs to answer without a session.
type alertQuestion struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Category is the same identifier as aps.category. A client that decodes
	// this object rather than reading the category off the notification gets
	// the same answer.
	Category string `json:"category"`
	// ActionDigest binds an answer to the exact question that was displayed.
	// The client echoes it back, which is what stops a stale notification
	// answering the question that replaced it.
	ActionDigest string `json:"action_digest"`
	// ResponseToken is a one-shot credential. It exists in plaintext only
	// inside this payload, and it is why the extension can answer at all.
	ResponseToken string `json:"response_token,omitempty"`
	// PrimaryLabel and SecondaryLabel override the labels the registered
	// category carries. Omitted when the sender accepted the defaults.
	PrimaryLabel   string `json:"primary_label,omitempty"`
	SecondaryLabel string `json:"secondary_label,omitempty"`
	// ExpiresAt is when answering stops working, so a client can retire a
	// prompt it is still showing.
	ExpiresAt string `json:"expires_at"`
}

// buildAlert encodes one notification.
func buildAlert(alert push.Alert) ([]byte, error) {
	aps := apsAlert{
		Alert:          alertText{Title: alert.Title, Body: alert.Body},
		Sound:          "default",
		MutableContent: 1,
		ThreadID:       alert.ThreadKey,
	}
	switch alert.Priority {
	case priorityCritical:
		aps.Sound = criticalSound{Critical: 1, Name: "default", Volume: 1}
		aps.InterruptionLevel = levelCritical
	case priorityTimeSensitive:
		aps.InterruptionLevel = levelTimeSensitive
	case priorityNormal, "":
		// The default level, which Apple reads from its absence.
	default:
		return nil, fmt.Errorf("apns: unknown alert priority %q", alert.Priority)
	}

	data := alertData{
		SchemaVersion: PayloadSchemaVersion,
		DeviceID:      alert.Target.DeviceID,
		RecordID:      alert.RecordID,
		ThreadKey:     alert.ThreadKey,
		URL:           deref(alert.URL),
		Source: alertSource{
			ID:       alert.SourceID,
			Name:     alert.SourceName,
			ImageURL: deref(alert.ImageURL),
		},
	}
	if q := alert.Interaction; q != nil {
		category := categoryFor(q.Kind)
		aps.Category = category
		data.Question = &alertQuestion{
			ID:             q.ID,
			Kind:           q.Kind,
			Category:       category,
			ActionDigest:   q.ActionDigest,
			ResponseToken:  q.ResponseToken,
			PrimaryLabel:   q.PrimaryLabel,
			SecondaryLabel: q.SecondaryLabel,
			ExpiresAt:      formatTime(q.ExpiresAt),
		}
	}

	return encodePayload(alertPayload{APS: aps, Hark: data})
}

// categoryFor maps a question's kind onto the category the client registered.
// An unknown kind uses the reply category so the user can enter free text.
func categoryFor(kind string) string {
	switch kind {
	case "approval":
		return CategoryApproval
	case "yes_no":
		return CategoryYesNo
	default:
		return CategoryReply
	}
}

// activityPayload is one Live Activity push. Everything in it is Apple's except
// the content state and the attributes, which are Hark's documents.
type activityPayload struct {
	APS apsActivity `json:"aps"`
}

type apsActivity struct {
	// Timestamp is the activity's monotonic counter, in epoch seconds.
	// ActivityKit discards content states that are not newer than the current
	// state, so this value increases monotonically.
	Timestamp int64 `json:"timestamp"`
	// Event is start, update or end.
	Event string `json:"event"`
	// ContentState is Hark's state document, delivered unchanged. The API and
	// widget use the same JSON structure.
	ContentState json.RawMessage `json:"content-state"`

	// The rest exists only on a start: an activity's attributes are immutable
	// for its lifetime, and the banner announces its arrival.
	AttributesType string              `json:"attributes-type,omitempty"`
	Attributes     *activityAttributes `json:"attributes,omitempty"`
	Alert          *alertText          `json:"alert,omitempty"`
	// InputPushToken asks ActivityKit to hand the app a per-activity update
	// token. Without it the card can be created and never changed again.
	InputPushToken int `json:"input-push-token,omitempty"`

	// StaleDate is when the widget should render itself as out of date. It may
	// appear on any event.
	StaleDate *int64 `json:"stale-date,omitempty"`
	// DismissalDate is when iOS should take the card off the screen. An end
	// carries it; anything else that did would be asking iOS to remove a card
	// that is still being driven.
	DismissalDate *int64 `json:"dismissal-date,omitempty"`
}

// activityAttributes is what ActivityKit keeps for the life of the activity. It
// is the widget's only route back to the server, so everything a Lock Screen
// button needs has to be here — the widget extension has no session and no
// access to the app's keychain.
type activityAttributes struct {
	SchemaVersion int    `json:"schema_version"`
	DeliveryID    string `json:"delivery_id"`
	DeviceID      string `json:"device_id"`
	// TokenRegistrationURL is where the phone reports the per-activity update
	// token, and TokenRegistrationToken is the capability authorising it.
	// Without this exchange the activity can be started but never updated or
	// ended by the server.
	TokenRegistrationURL   string `json:"token_registration_url"`
	TokenRegistrationToken string `json:"token_registration_token"`
	// Question is set when the card presents one, and is what its buttons
	// answer with.
	Question *activityQuestion `json:"question,omitempty"`
}

type activityQuestion struct {
	ID            string `json:"id"`
	ActionDigest  string `json:"action_digest"`
	ResponseToken string `json:"response_token"`
}

// buildActivity encodes one Live Activity event.
func buildActivity(event push.ActivityEvent) ([]byte, error) {
	if len(event.State) == 0 {
		return nil, errors.New("apns: the Live Activity content state is empty")
	}

	aps := apsActivity{
		Timestamp:    event.Timestamp,
		Event:        event.Event,
		ContentState: event.State,
		StaleDate:    epochSeconds(event.StaleAt),
	}

	switch event.Event {
	case push.EventStart:
		start := event.Start
		if start == nil {
			return nil, errors.New("apns: a Live Activity start needs its attributes")
		}
		aps.AttributesType = ActivityAttributesType
		aps.InputPushToken = 1
		aps.Alert = &alertText{Title: start.Banner.Title, Body: start.Banner.Body}
		aps.Attributes = &activityAttributes{
			SchemaVersion:          PayloadSchemaVersion,
			DeliveryID:             event.DeliveryID,
			DeviceID:               event.Target.DeviceID,
			TokenRegistrationURL:   start.TokenRegistrationURL,
			TokenRegistrationToken: start.RegistrationToken,
		}
		if q := start.Interaction; q != nil {
			aps.Attributes.Question = &activityQuestion{
				ID:            q.ID,
				ActionDigest:  q.ActionDigest,
				ResponseToken: q.ResponseToken,
			}
		}
	case push.EventEnd:
		aps.DismissalDate = epochSeconds(event.DismissalAt)
	case push.EventUpdate:
		// Nothing beyond the state and the dates.
	default:
		return nil, fmt.Errorf("apns: unknown Live Activity event %q", event.Event)
	}

	return encodePayload(activityPayload{APS: aps})
}

// encodePayload serialises a payload and enforces Apple's size limit.
//
// The check happens here rather than at the connection because an oversized
// payload is a fact about the request, not about the network: failing before
// the round trip records the same delivery failure a 413 would, seconds
// earlier and without spending Apple's time.
func encodePayload(payload any) ([]byte, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("apns: encode the payload: %w", err)
	}
	if len(encoded) > MaxPayloadBytes {
		return nil, fmt.Errorf("%w: %d bytes, over the %d-byte limit",
			errPayloadTooLarge, len(encoded), MaxPayloadBytes)
	}
	return encoded, nil
}

// epochSeconds renders an optional instant the way ActivityKit reads dates.
func epochSeconds(t *time.Time) *int64 {
	if t == nil {
		return nil
	}
	seconds := t.Unix()
	return &seconds
}

// formatTime renders an instant the way every other timestamp in this API is
// rendered: RFC 3339, UTC, milliseconds, literal Z.
func formatTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

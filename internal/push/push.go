// Package push defines the API-facing push transport interface.
//
// The API layer decides *what* to deliver and records the outcome; a [Sender]
// decides how to reach Apple. [Noop] makes the API testable without APNs
// credentials by returning a synthetic failure for each send. This exercises
// failure paths such as event status
// `failed`, `accepted: 0`, the reason strings a caller sees — are the ones that
// run in tests and in a development deployment.
//
// The API layer also handles:
//
//   - Decryption. Push tokens are stored encrypted; the API layer owns the key,
//     so a Sender receives plaintext and never a key.
//   - Bookkeeping. Attempt rows, delivery status, counter settlement and stale
//     device pruning are persistence, and a Sender has no database.
package push

import (
	"context"
	"encoding/json"
	"time"
)

// Synthetic reasons recorded when no APNs call was made. They sit in the same
// namespace as Apple's reason strings (`BadDeviceToken`, `Unregistered`, …) so
// callers can read one delivery-reason field.
const (
	// ReasonNotConfigured means no APNs credentials are configured.
	ReasonNotConfigured = "ProviderNotConfigured"
	// ReasonMissingPushToStartToken means the device never handed over an
	// ActivityKit push-to-start token, so no activity can be created on it.
	ReasonMissingPushToStartToken = "MissingPushToStartToken"
	// ReasonMissingUpdateToken means the phone has not yet reported the update
	// token for this delivery, so it cannot be updated or ended. This is the
	// common one: a start that APNs accepted is not yet a delivery that can be
	// driven.
	ReasonMissingUpdateToken = "MissingUpdateToken"
	// ReasonInteractionTerminal means the question the activity presents was
	// answered, canceled or expired before the start push went out.
	ReasonInteractionTerminal = "InteractionTerminal"
	// ReasonEnvironmentMismatch means the delivery's APNs environment is not the
	// one this process is configured for.
	ReasonEnvironmentMismatch = "EnvironmentMismatch"
	// ReasonDeliveryFailed is the fallback when a send failed without a reason.
	ReasonDeliveryFailed = "DeliveryFailed"
)

// Live Activity events, matching db.Operation*.
const (
	EventStart  = "start"
	EventUpdate = "update"
	EventEnd    = "end"
)

// Target is the device one push is addressed to.
type Target struct {
	// DeviceID is Hark's id for the device, for logging and for pruning.
	DeviceID string
	// Token is the raw APNs device token, lowercase hex.
	Token string
}

// Alert is one ordinary notification for one device.
//
// It is the shape of every alert Hark sends: a webhook event, an agent
// notification, an interaction delivered as a notification action, and the
// welcome sequence. What differs between them is which optional fields are set,
// not the envelope.
type Alert struct {
	Target Target

	// Title and Body are what the phone shows.
	Title string
	Body  string
	// Priority is one of db.Priority*: normal, time_sensitive, critical.
	Priority string

	// ImageURL is the sender's avatar, already validated as a public HTTPS URL.
	ImageURL *string
	// URL is the tap destination: a web URL or an app deep link.
	URL *string

	// ThreadKey groups related alerts into one conversation on the phone. Alerts
	// from one service, or one agent title, share a key.
	ThreadKey string
	// SourceID and SourceName identify the sender to the client: a service id
	// and title, or an API token id and name.
	SourceID   string
	SourceName string
	// RecordID is the id of the row this alert came from — an event, an agent
	// notification, or a synthetic id for the welcome sequence — so a client can
	// correlate a notification with the history entry it belongs to.
	RecordID string

	// Interaction is set when the alert asks a question, which makes the phone
	// render answer actions instead of a plain notification.
	Interaction *AlertInteraction
}

// AlertInteraction is the question half of an interaction alert.
type AlertInteraction struct {
	ID   string
	Kind string
	// ActionDigest binds an answer to the exact question that was displayed. The
	// client echoes it back when responding.
	ActionDigest string
	// ResponseToken is a one-shot credential that lets the phone answer without
	// a session — from the notification-service extension, or from a Live
	// Activity button. It exists in plaintext only inside this payload.
	ResponseToken string
	// PrimaryLabel and SecondaryLabel override the default action titles.
	PrimaryLabel   string
	SecondaryLabel string
	ExpiresAt      time.Time
}

// AlertResult is the aggregate of one fan-out.
type AlertResult struct {
	// Accepted counts the messages APNs took. It is not proof of display, and
	// every response that reports it says so.
	Accepted int
	// Failures are human-readable descriptions, one per failed message. They are
	// shown only to the account owner: a provider error can embed a device
	// token.
	Failures []string
	// StaleTokens are the APNs tokens the provider reported as permanently
	// invalid. The caller deactivates the devices holding them.
	StaleTokens []string
}

// ActivityEvent is one Live Activity push to one device.
type ActivityEvent struct {
	// Event is EventStart, EventUpdate or EventEnd.
	Event string

	// PushToken is the plaintext ActivityKit token: the device's push-to-start
	// token for a start, the delivery's update token otherwise. The caller has
	// already decrypted it and has already turned "there is none" into a
	// synthetic failure, so this is never empty.
	PushToken string
	// Environment is the APNs environment the token was minted in. A sender that
	// is configured for the other one refuses without opening a connection.
	Environment string

	ActivityID string
	DeliveryID string
	Target     Target

	// State is the ActivityKit content-state document, already validated. It is
	// delivered verbatim, so its serialisation is the caller's choice.
	State json.RawMessage
	// Timestamp is the activity's monotonic APNs timestamp, in epoch seconds.
	// ActivityKit discards content states that are not newer than the current
	// state, so this value increases monotonically.
	Timestamp int64
	// StaleAt is when the phone should render the card as stale. DismissalAt is
	// when it should disappear, and applies to EventEnd only.
	StaleAt     *time.Time
	DismissalAt *time.Time

	// Start is set only for EventStart: the immutable attributes ActivityKit
	// keeps for the life of the activity.
	Start *ActivityStart
}

// ActivityStart carries the attributes only a start push can set.
type ActivityStart struct {
	// Banner is what iOS announces when the card appears. ActivityKit wants one
	// on a start, and it belongs to the caller rather than to the transport
	// because deciding what it says — whether it repeats the activity's own
	// words or withholds them — is a reading of the state document, and the
	// state document is the caller's.
	Banner ActivityBanner

	// TokenRegistrationURL is where the phone reports the per-activity update
	// token, and RegistrationToken is the capability that authorises it. Without
	// this exchange the activity can be created but never updated or ended.
	TokenRegistrationURL string
	RegistrationToken    string

	// Interaction is set when the activity presents a question on the Lock
	// Screen, so the widget's buttons can answer it.
	Interaction *ActivityInteraction
}

// ActivityBanner is the notification announcing an activity's arrival. It is
// not a separate notification: it carries no sound, no category and no thread.
type ActivityBanner struct {
	Title string
	Body  string
}

// ActivityInteraction is what a Lock Screen button needs to answer.
type ActivityInteraction struct {
	ID string
	// ResponseToken is the same one-shot credential an interaction alert
	// carries.
	ResponseToken string
	ActionDigest  string
}

// ActivityResult is the outcome of one Live Activity push.
type ActivityResult struct {
	// Accepted reports whether APNs took the push.
	Accepted bool
	// APNsStatus is the HTTP status, or nil when no call was made.
	APNsStatus *int
	// Reason is APNs' reason string, or one of the synthetic Reason* constants.
	Reason *string
	// APNsID is the provider's message id, for support requests.
	APNsID *string
	// TokenInvalid reports that the push token will never work again, so the
	// caller drops it instead of retrying forever.
	TokenInvalid bool
}

// Sender delivers pushes.
//
// Both methods report failure in their result rather than as an error: a
// fan-out where one device is unreachable is a normal outcome that must still
// settle the other devices, and every failure has to be recorded against the
// row it belongs to.
type Sender interface {
	// SendAlerts delivers one alert per target and returns the aggregate.
	SendAlerts(ctx context.Context, alerts []Alert) AlertResult
	// SendActivity delivers one Live Activity event to one delivery.
	SendActivity(ctx context.Context, event ActivityEvent) ActivityResult
}

// Noop is a Sender that reaches nothing.
//
// It is what runs until the APNs transport is wired in, and what runs in any
// deployment without credentials. Every send is reported as a
// ProviderNotConfigured failure rather than a success, so nothing upstream ever
// records a delivery that did not happen.
type Noop struct{}

// SendAlerts reports every alert as unreachable.
func (Noop) SendAlerts(_ context.Context, alerts []Alert) AlertResult {
	if len(alerts) == 0 {
		return AlertResult{}
	}
	return AlertResult{Failures: []string{"APNs request failed: " + ReasonNotConfigured}}
}

// SendActivity reports the push as unreachable.
func (Noop) SendActivity(context.Context, ActivityEvent) ActivityResult {
	reason := ReasonNotConfigured
	return ActivityResult{Reason: &reason}
}

// Welcome messages. They are the first thing a new device sees, and they exist
// to prove the round trip works before the owner has configured anything.
const (
	welcomeFirstBody  = "Welcome to Hark — this iPhone is registered."
	welcomeSecondBody = "Create a service in the dashboard and point any webhook at it."

	// WelcomeSource is the source id the welcome alerts carry. It is not a real
	// service, and a client must not try to resolve it.
	WelcomeSource = "hark-welcome"
)

// WelcomeAlerts builds the one-shot sequence sent to the first device an
// account registers. publicURL is the dashboard's origin, without a trailing
// slash.
func WelcomeAlerts(publicURL string, target Target) []Alert {
	dashboard := publicURL + "/dashboard"
	base := Alert{
		Target:     target,
		Title:      "Hark",
		Priority:   "normal",
		ThreadKey:  WelcomeSource,
		SourceID:   WelcomeSource,
		SourceName: "Hark",
	}

	first, second := base, base
	first.Body, first.RecordID, first.URL = welcomeFirstBody, WelcomeSource+"-1", &publicURL
	second.Body, second.RecordID, second.URL = welcomeSecondBody, WelcomeSource+"-2", &dashboard
	return []Alert{first, second}
}

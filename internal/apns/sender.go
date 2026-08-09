package apns

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/abdeen-labs/hark/internal/push"
)

// alertConcurrency is how many alerts are in flight at once.
//
// The cap is not about Apple — one HTTP/2 connection multiplexes far more than
// this — it is about the shape of a fan-out: an account has a handful of
// devices, and ten workers means the slowest phone never holds up the rest
// while the pool stays small enough to reason about.
const alertConcurrency = 10

// Sender is the [push.Sender] backed by APNs.
type Sender struct {
	client *Client
	log    *slog.Logger
}

// NewSender wraps a client as the transport the API layer talks to.
func NewSender(client *Client) *Sender {
	return &Sender{client: client, log: client.log}
}

var _ push.Sender = (*Sender)(nil)

// SendAlerts delivers one notification per device.
//
// Every message is independent: one unreachable phone neither aborts the others
// nor is retried. What the caller gets back is a count of what APNs took, one
// human-readable line per failure for the owner's delivery log, and the tokens
// that will never work again.
func (s *Sender) SendAlerts(ctx context.Context, alerts []push.Alert) push.AlertResult {
	if len(alerts) == 0 {
		return push.AlertResult{}
	}

	responses := make([]Response, len(alerts))
	workers := min(alertConcurrency, len(alerts))

	var next atomic.Int64
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= len(alerts) {
					return
				}
				responses[i] = s.sendAlert(ctx, alerts[i])
			}
		}()
	}
	wg.Wait()

	// Failures are reported in the order the alerts were given rather than the
	// order they finished. Nothing downstream depends on completion order, and
	// a stable list is one an owner can compare across two sends.
	var result push.AlertResult
	for i, response := range responses {
		if response.Accepted() {
			result.Accepted++
			continue
		}
		result.Failures = append(result.Failures, failureText(response))
		if tokenIsDead(response) {
			result.StaleTokens = append(result.StaleTokens, alerts[i].Target.Token)
		}
		s.log.WarnContext(ctx, "an alert was not accepted",
			"device_id", alerts[i].Target.DeviceID,
			"status", response.Status, "reason", response.Reason)
	}
	return result
}

// sendAlert builds and delivers one notification.
func (s *Sender) sendAlert(ctx context.Context, alert push.Alert) Response {
	payload, err := buildAlert(alert)
	if err != nil {
		s.log.ErrorContext(ctx, "building an alert payload failed",
			"device_id", alert.Target.DeviceID, "error", err)
		return Response{Reason: buildReason(err)}
	}

	expiration := expirationDoNotRetain
	return s.client.Push(ctx, Request{
		Token:    alert.Target.Token,
		PushType: pushTypeAlert,
		Topic:    s.client.alertTopic(),
		// Every alert is priority 10 and expires immediately. A notification
		// Hark sends is worth showing now or not at all: APNs storing one for
		// later means a phone that was off buzzing about a deploy that finished
		// an hour ago.
		Priority:   priorityImmediate,
		Expiration: &expiration,
		Payload:    payload,
		DeviceID:   alert.Target.DeviceID,
	})
}

// SendActivity delivers one Live Activity event to one delivery.
func (s *Sender) SendActivity(ctx context.Context, event push.ActivityEvent) push.ActivityResult {
	// An ActivityKit token is only valid in the environment it was minted in,
	// and this process talks to exactly one. Pushing to the other one fails in
	// a way that looks like a dead token, so it is refused before a connection
	// is opened.
	if event.Environment != "" && event.Environment != s.client.environment {
		return failedActivity(push.ReasonEnvironmentMismatch)
	}

	payload, err := buildActivity(event)
	if err != nil {
		s.log.ErrorContext(ctx, "building a Live Activity payload failed",
			"delivery_id", event.DeliveryID, "event", event.Event, "error", err)
		return failedActivity(buildReason(err))
	}

	response := s.client.Push(ctx, Request{
		Token:    event.PushToken,
		PushType: pushTypeLiveActivity,
		Topic:    s.client.activityTopic(),
		// Every Live Activity push is priority 10. Apple suggests 5 for
		// frequent low-urgency updates, but Hark's updates are driven by an
		// agent reporting that something actually changed, and a progress bar
		// that lags behind the work is worse than no progress bar.
		Priority: priorityImmediate,
		Payload:  payload,
		DeviceID: event.Target.DeviceID,
	})

	result := push.ActivityResult{
		Accepted:     response.Accepted(),
		TokenInvalid: tokenIsDead(response),
	}
	if response.Status != 0 {
		status := response.Status
		result.APNsStatus = &status
	}
	if response.APNsID != "" {
		id := response.APNsID
		result.APNsID = &id
	}
	switch {
	case result.Accepted:
		// A success has nothing to explain.
	case response.Reason != "":
		reason := response.Reason
		result.Reason = &reason
	default:
		// APNs refused without saying why. Recording the refusal without a
		// reason would leave a delivery whose log says nothing at all.
		reason := push.ReasonDeliveryFailed
		result.Reason = &reason
	}
	if !result.Accepted {
		s.log.WarnContext(ctx, "a Live Activity push was not accepted",
			"delivery_id", event.DeliveryID, "event", event.Event,
			"status", response.Status, "reason", response.Reason)
	}
	return result
}

func failedActivity(reason string) push.ActivityResult {
	return push.ActivityResult{Reason: &reason}
}

// buildReason names why a payload never reached the network.
func buildReason(err error) string {
	if errors.Is(err, errPayloadTooLarge) {
		return ReasonPayloadTooLarge
	}
	return push.ReasonDeliveryFailed
}

// tokenIsDead reports that a push token will never work again, whatever it is
// addressed to.
//
// One rule serves alerts and Live Activities. The three reasons below say the
// same thing in three ways — the app was removed, the token was never valid,
// the token has aged out — and a 410 says it without a reason at all.
//
// DeviceTokenNotForTopic is deliberately not here. It indicts this server's
// bundle id rather than the phone's token, and treating a topic typo as a dead
// token would deactivate every device in the account for a mistake none of them
// made. It is logged as the configuration error it is instead.
func tokenIsDead(response Response) bool {
	if response.Status == http.StatusGone {
		return true
	}
	switch response.Reason {
	case ReasonUnregistered, ReasonBadDeviceToken, ReasonExpiredToken:
		return true
	default:
		return false
	}
}

// failureText renders one failed send for the owner's delivery log.
//
// These strings are shown to the account owner and to nobody else: a provider
// error names the status and reason of a push to a specific phone, which is
// more than the holder of an API token or a webhook URL is entitled to know.
func failureText(response Response) string {
	if response.Status > 0 {
		if response.Reason != "" {
			return "APNs " + strconv.Itoa(response.Status) + " " + response.Reason
		}
		return "APNs " + strconv.Itoa(response.Status)
	}
	if response.Reason != "" {
		return "APNs request failed: " + response.Reason
	}
	return "APNs request failed: unknown error"
}

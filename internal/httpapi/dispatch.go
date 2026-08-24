package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/push"
	"github.com/abdeen-labs/hark/internal/secret"
)

// dismissalOnResolve is how long an interaction's Live Activity stays on screen
// after the question is answered. Long enough to read the outcome, short enough
// that a Lock Screen does not collect answered questions.
const dismissalOnResolve = 30 * time.Second

// activityDispatch is one fan-out of one operation over an activity's
// deliveries.
type activityDispatch struct {
	Activity   db.LiveActivity
	Operation  db.LiveActivityOperation
	Requester  requester
	Deliveries []db.LiveActivityDelivery
	// Devices is needed for a start, which pushes to the device's
	// push-to-start token rather than to a per-activity one.
	Devices map[string]db.Device
	// Interaction is set when the activity presents a question, so a start can
	// be abandoned if the question was already answered.
	Interaction *db.Interaction
	// ResponseToken is the plaintext credential the Lock Screen buttons will
	// present. It exists only inside the request that created the question, so
	// it is passed down rather than read back.
	ResponseToken string
}

// dispatchOutcome is what one fan-out achieved.
type dispatchOutcome struct {
	Accepted int
	Failed   int
	// Reasons are the distinct failure reasons, in first-seen order. Unlike
	// alert failures these are shown to the caller: they name a state the caller
	// can act on (a device that never reported its token, a question that was
	// already answered) and carry no device identifiers.
	Reasons []string
}

// message renders the reasons for a response, or nil when everything landed.
func (o dispatchOutcome) message() *string {
	if len(o.Reasons) == 0 {
		return nil
	}
	return failureSummary(o.Reasons)
}

// dispatchActivity pushes one operation to every delivery and records what
// happened to each.
//
// Deliveries run sequentially. An attempt row is written only after its push
// returns.
func (s *server) dispatchActivity(r *http.Request, d activityDispatch) dispatchOutcome {
	var outcome dispatchOutcome
	for _, delivery := range d.Deliveries {
		result := s.sendOneActivity(r, d, delivery)
		if result.Accepted {
			outcome.Accepted++
		} else {
			outcome.Failed++
			if result.Reason != nil && !slices.Contains(outcome.Reasons, *result.Reason) {
				outcome.Reasons = append(outcome.Reasons, *result.Reason)
			}
		}

		// The attempt is recorded on a detached context: once a push has been
		// made, losing the record of it because the caller hung up would leave a
		// delivery whose state disagrees with the phone's.
		if _, err := s.store().Deliveries.RecordAttempt(detach(r.Context()), db.RecordAttemptParams{
			AttemptID:          newID(),
			ActivityID:         d.Activity.ID,
			DeliveryID:         delivery.ID,
			DeviceID:           delivery.DeviceID,
			OperationID:        d.Operation.ID,
			RequesterTokenID:   d.Requester.TokenID,
			RequesterServiceID: d.Requester.ServiceID,
			Event:              d.Operation.Event,
			Sequence:           d.Activity.Sequence,
			Accepted:           result.Accepted,
			APNsStatus:         result.APNsStatus,
			APNsReason:         result.Reason,
			APNsID:             result.APNsID,
			TokenInvalid:       result.TokenInvalid,
			Now:                s.now(),
		}); err != nil {
			LoggerFrom(r.Context()).ErrorContext(r.Context(), "recording a delivery attempt failed",
				"delivery_id", delivery.ID, "error", err)
		}
	}
	return outcome
}

// sendOneActivity delivers one event to one device, or explains why it did not.
//
// The short-circuits below are the common case in practice, not the exception:
// an activity that APNs accepted is not yet an activity that can be driven,
// because the phone has to report its per-activity update token first.
func (s *server) sendOneActivity(r *http.Request, d activityDispatch, delivery db.LiveActivityDelivery) push.ActivityResult {
	event := d.Operation.Event

	// A question that was answered while its start was being prepared must not
	// then appear on a Lock Screen.
	if event == db.OperationStart && d.Interaction != nil && !d.Interaction.Live(s.now()) {
		return failedActivity(push.ReasonInteractionTerminal)
	}

	ciphertext, missing := activityPushToken(d, delivery, event)
	if missing != "" {
		return failedActivity(missing)
	}
	token, err := s.opts.Secrets.Decrypt(secret.PurposeActivityToken, ciphertext)
	if err != nil {
		LoggerFrom(r.Context()).ErrorContext(r.Context(), "a stored push token could not be decrypted",
			"delivery_id", delivery.ID, "error", err)
		return failedActivity(push.ReasonDeliveryFailed)
	}

	event2 := push.ActivityEvent{
		Event:       event,
		PushToken:   token,
		Environment: delivery.Environment,
		ActivityID:  d.Activity.ID,
		DeliveryID:  delivery.ID,
		Target:      push.Target{DeviceID: delivery.DeviceID},
		State:       d.Operation.Props,
		Timestamp:   d.Activity.APNsTimestamp,
		StaleAt:     d.Activity.StaleAt,
	}
	if event == db.OperationEnd {
		event2.DismissalAt = d.Activity.DismissalAt
	}
	if event == db.OperationStart {
		event2.Start = &push.ActivityStart{
			Banner:               activityBanner(d.Operation.Props),
			TokenRegistrationURL: s.publicPath(APIPrefix + "/activity-deliveries/" + delivery.ID + "/update-token"),
			RegistrationToken:    s.registrationToken(delivery.ID, d.Activity.ID, d.Activity.ExpiresAt),
		}
		if d.Interaction != nil {
			event2.Start.Interaction = &push.ActivityInteraction{
				ID:            d.Interaction.ID,
				ResponseToken: d.ResponseToken,
				ActionDigest:  d.Interaction.ActionDigest,
			}
		}
	}
	return s.opts.Push.SendActivity(r.Context(), event2)
}

// activityBanner is what iOS announces when a card first appears.
//
// A private activity says only that something started. The redaction is of the
// banner alone: the state document delivered alongside it still carries the
// real title and status, because what a locked screen shows is the widget's
// decision and not the server's.
func activityBanner(props json.RawMessage) push.ActivityBanner {
	state, err := decodeActivityState(props)
	if err != nil || state.PrivacyMode != privacyStandard {
		return push.ActivityBanner{
			Title: "An agent is at work",
			Body:  "Open Hark to follow along.",
		}
	}
	return push.ActivityBanner{Title: state.Title, Body: state.Status}
}

// activityPushToken picks the credential this event needs, or names the one that
// is missing.
func activityPushToken(d activityDispatch, delivery db.LiveActivityDelivery, event string) (ciphertext, missing string) {
	if event == db.OperationStart {
		device, ok := d.Devices[delivery.DeviceID]
		if !ok || device.PushToStartTokenCiphertext == nil || *device.PushToStartTokenCiphertext == "" {
			return "", push.ReasonMissingPushToStartToken
		}
		return *device.PushToStartTokenCiphertext, ""
	}
	if !delivery.Updatable() {
		return "", push.ReasonMissingUpdateToken
	}
	return *delivery.UpdateTokenCiphertext, ""
}

func failedActivity(reason string) push.ActivityResult {
	return push.ActivityResult{Reason: &reason}
}

// settleActivity records what the fan-out achieved and what it implies about the
// activity's status.
//
// A settle that arrives after a newer mutation is dropped by the store's
// sequence guard rather than overwriting newer counts. The current stored state
// is returned.
func (s *server) settleActivity(r *http.Request, act db.LiveActivity, op db.LiveActivityOperation, outcome dispatchOutcome) db.LiveActivity {
	ctx := detach(r.Context())

	status := act.Status
	switch op.Event {
	case db.OperationEnd:
		// The end already moved the activity; only the counts are open.
		status = db.ActivityEnded
	case db.OperationStart:
		switch {
		case outcome.Accepted == 0:
			status = db.ActivityFailed
		case outcome.Failed > 0:
			status = db.ActivityPartial
		default:
			status = db.ActivityActive
		}
	default:
		switch {
		case outcome.Accepted > 0 && outcome.Failed > 0:
			status = db.ActivityPartial
		case outcome.Accepted > 0:
			status = db.ActivityActive
		default:
			// A delivery waiting for its update token may accept a later push. Mark
			// the activity failed only when no live delivery remains.
			live, err := s.store().Activities.CountLiveDeliveries(ctx, act.ID)
			if err != nil {
				LoggerFrom(r.Context()).ErrorContext(r.Context(), "counting live deliveries failed", "error", err)
			} else if live == 0 {
				status = db.ActivityFailed
			}
		}
	}

	if _, err := s.store().Operations.Settle(ctx, op.ID, outcome.Accepted, outcome.Failed); err != nil {
		LoggerFrom(r.Context()).ErrorContext(r.Context(), "settling a Live Activity operation failed", "error", err)
	}
	settled, err := s.store().Activities.Settle(ctx, act.ID, act.Sequence, status, outcome.Accepted, outcome.Failed, s.now())
	if err != nil {
		if !errors.Is(err, db.ErrNotFound) {
			LoggerFrom(r.Context()).ErrorContext(r.Context(), "settling a Live Activity failed", "error", err)
		}
		act.Status = status
		act.AcceptedCount, act.FailedCount = outcome.Accepted, outcome.Failed
		return act
	}
	return *settled
}

// expireActivityIfDue retires an activity whose deadline has passed.
//
// Expiry is lazy: there is no sweeper, and every read that can act on an
// activity runs this first. Losing the race is not a failure — it means someone
// else expired it — so the current row is re-read and used.
func (s *server) expireActivityIfDue(ctx context.Context, act *db.LiveActivity) *db.LiveActivity {
	now := s.now()
	if !act.Live() || act.ExpiresAt.After(now) {
		return act
	}

	expired, err := s.store().Activities.ExpireIfDue(ctx, act.ID, now)
	switch {
	case err == nil:
		return expired
	case errors.Is(err, db.ErrNotFound):
		if fresh, err := s.store().Activities.ByID(ctx, act.ID); err == nil {
			return fresh
		}
	}
	return act
}

// expireInteractionIfDue retires a question whose deadline has passed, and ends
// the Live Activity presenting it.
func (s *server) expireInteractionIfDue(r *http.Request, in *db.Interaction) *db.Interaction {
	now := s.now()
	if in.Status != db.InteractionPending || in.ExpiresAt.After(now) {
		return in
	}

	expired, err := s.store().Interactions.ExpireIfDue(r.Context(), in.ID, now)
	switch {
	case err == nil:
		s.resolveInteractionActivity(r, *expired)
		return expired
	case errors.Is(err, db.ErrNotFound):
		if fresh, err := s.store().Interactions.ByID(r.Context(), in.ID); err == nil {
			return fresh
		}
	}
	return in
}

// takeOver ends the activities occupying what a start has asked for.
//
// `replace: true` ends activities holding the device slot or key. Automatic end
// operations are not recorded or charged against rate limits; only the new start
// is charged.
//
// A delivery is released even when its end push fails so an unreachable
// activity cannot permanently occupy the device slot.
func (s *server) takeOver(r *http.Request, activityIDs []string) int {
	replaced := 0
	for _, activityID := range activityIDs {
		act, err := s.store().Activities.ByID(r.Context(), activityID)
		if err != nil || !act.Live() {
			continue
		}

		ended, err := s.store().Activities.EndUnmetered(r.Context(), act.ID, act.Sequence, s.now(), s.now())
		if err != nil {
			// Someone else moved it first, which is the outcome this wanted.
			if !errors.Is(err, db.ErrNotFound) {
				LoggerFrom(r.Context()).ErrorContext(r.Context(), "ending a replaced Live Activity failed",
					"activity_id", act.ID, "error", err)
			}
			continue
		}
		replaced++

		deliveries, err := s.store().Deliveries.ListForActivity(r.Context(), ended.ID, db.LiveStatuses())
		if err != nil {
			LoggerFrom(r.Context()).ErrorContext(r.Context(), "listing deliveries of a replaced Live Activity failed",
				"activity_id", ended.ID, "error", err)
			continue
		}
		for _, delivery := range deliveries {
			result := s.endReplacedDelivery(r, *ended, delivery)
			if _, err := s.store().Deliveries.End(detach(r.Context()), db.EndParams{
				DeliveryID: delivery.ID,
				Sequence:   ended.Sequence,
				APNsStatus: result.APNsStatus,
				APNsReason: result.Reason,
				APNsID:     result.APNsID,
				Now:        s.now(),
			}); err != nil {
				LoggerFrom(r.Context()).ErrorContext(r.Context(), "releasing a replaced delivery failed",
					"delivery_id", delivery.ID, "error", err)
			}
		}
	}
	return replaced
}

// endReplacedDelivery pushes the silent end of a replaced activity.
func (s *server) endReplacedDelivery(r *http.Request, act db.LiveActivity, delivery db.LiveActivityDelivery) push.ActivityResult {
	if !delivery.Updatable() {
		return failedActivity(push.ReasonMissingUpdateToken)
	}
	token, err := s.opts.Secrets.Decrypt(secret.PurposeActivityToken, *delivery.UpdateTokenCiphertext)
	if err != nil {
		return failedActivity(push.ReasonDeliveryFailed)
	}
	return s.opts.Push.SendActivity(r.Context(), push.ActivityEvent{
		Event:       db.OperationEnd,
		PushToken:   token,
		Environment: delivery.Environment,
		ActivityID:  act.ID,
		DeliveryID:  delivery.ID,
		Target:      push.Target{DeviceID: delivery.DeviceID},
		State:       act.Props,
		Timestamp:   act.APNsTimestamp,
		StaleAt:     act.StaleAt,
		DismissalAt: act.DismissalAt,
	})
}

// startInteractionActivity puts a question on the Lock Screen.
//
// It returns the activity's id, or nil when no device could show one. Nil is
// not an error: the caller falls back to delivering the question as an ordinary
// notification, which is a plainer surface but still one the owner can answer
// from. See § handleCreateInteraction.
func (s *server) startInteractionActivity(r *http.Request, in db.Interaction, style, responseToken string, devices []db.Device) (*string, dispatchOutcome) {
	capable := make([]db.Device, 0, len(devices))
	for _, d := range devices {
		if d.InteractiveLiveActivityCapable() {
			capable = append(capable, d)
		}
	}
	if len(capable) == 0 {
		return nil, dispatchOutcome{}
	}

	activityID := newID()
	state, err := encodeActivityState(interactionState(activityID, in, style, s.now()))
	if err != nil {
		s.logError(r, "building an interactive Live Activity state failed", err)
		return nil, dispatchOutcome{}
	}

	targets := make([]db.ActivityTarget, 0, len(capable))
	for _, d := range capable {
		targets = append(targets, db.ActivityTarget{
			DeliveryID:  newID(),
			DeviceID:    d.ID,
			Environment: deref(d.PushToStartEnvironment),
			// An interaction delivery sits outside the one-card-per-device rule:
			// a question is worth showing even while a task is running.
			Purpose: db.PurposeInteraction,
		})
	}

	started, err := s.store().Activities.Start(r.Context(), db.StartActivityParams{
		ID:                 activityID,
		UserID:             in.UserID,
		RequesterTokenID:   in.RequesterTokenID,
		RequesterServiceID: in.RequesterServiceID,
		InteractionID:      &in.ID,
		SchemaVersion:      activityStateVersion,
		Props:              state,
		// The card lives exactly as long as the question does.
		ExpiresAt:   in.ExpiresAt,
		StaleAt:     &in.ExpiresAt,
		OperationID: newID(),
		Targets:     targets,
		Now:         s.now(),
	})
	if err != nil {
		s.logError(r, "starting an interactive Live Activity failed", err)
		return nil, dispatchOutcome{}
	}

	outcome := s.dispatchActivity(r, activityDispatch{
		Activity:      started.Activity,
		Operation:     started.Operation,
		Requester:     requester{UserID: in.UserID, TokenID: in.RequesterTokenID, ServiceID: in.RequesterServiceID},
		Deliveries:    started.Deliveries,
		Devices:       deviceIndex(capable),
		Interaction:   &in,
		ResponseToken: responseToken,
	})
	s.settleActivity(r, started.Activity, started.Operation, outcome)
	return &started.Activity.ID, outcome
}

// resolveInteractionActivity ends the Live Activity presenting a question that
// has just reached a terminal state.
//
// It runs inline rather than in the background. The alternative — a goroutine
// with a detached context — can be lost to a shutdown, and a Lock Screen still
// asking a question that was answered minutes ago is worse than a response that
// took an extra moment.
func (s *server) resolveInteractionActivity(r *http.Request, in db.Interaction) {
	act, err := s.store().Activities.ByInteractionID(r.Context(), in.ID)
	if err != nil {
		if !errors.Is(err, db.ErrNotFound) {
			s.logError(r, "loading an interaction's Live Activity failed", err)
		}
		return
	}
	if !act.Live() {
		return
	}

	state, err := decodeActivityState(act.Props)
	if err != nil {
		s.logError(r, "decoding a Live Activity state failed", err)
		return
	}
	resolved, err := encodeActivityState(resolvedInteractionState(state, in, s.now()))
	if err != nil {
		s.logError(r, "encoding a resolved Live Activity state failed", err)
		return
	}

	dismissal := s.now().Add(dismissalOnResolve)
	ended, op, err := s.store().Activities.Mutate(r.Context(), db.MutateActivityParams{
		ActivityID:         act.ID,
		ExpectedSequence:   act.Sequence,
		Event:              db.OperationEnd,
		Props:              resolved,
		DismissalAt:        db.Value(&dismissal),
		OperationID:        newID(),
		RequesterTokenID:   act.RequesterTokenID,
		RequesterServiceID: act.RequesterServiceID,
		Now:                s.now(),
	})
	if err != nil {
		if !errors.Is(err, db.ErrNotFound) {
			s.logError(r, "ending an interaction's Live Activity failed", err)
		}
		return
	}

	deliveries, err := s.store().Deliveries.ListForActivity(r.Context(), ended.ID, db.LiveStatuses())
	if err != nil {
		s.logError(r, "listing an interaction activity's deliveries failed", err)
		return
	}
	outcome := s.dispatchActivity(r, activityDispatch{
		Activity:   *ended,
		Operation:  *op,
		Requester:  requester{UserID: act.UserID, TokenID: act.RequesterTokenID, ServiceID: act.RequesterServiceID},
		Deliveries: deliveries,
	})
	s.settleActivity(r, *ended, *op, outcome)
}

// logError records a failure that must not become the caller's problem: the
// request has already succeeded, and the work that failed was a consequence of
// it rather than the thing that was asked for.
func (s *server) logError(r *http.Request, what string, err error) {
	LoggerFrom(r.Context()).ErrorContext(r.Context(), what, "error", err)
}

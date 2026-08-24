package db

import (
	"context"
	"time"
)

// Delivery statuses.
const (
	// DeliveryPending means the start push has not been attempted yet.
	DeliveryPending = "pending"
	// DeliveryAccepted means APNs took the push but the phone has not
	// confirmed the activity exists.
	DeliveryAccepted = "accepted"
	// DeliveryActive means the phone reported back with an update token, so
	// the activity can be updated and ended.
	DeliveryActive = "active"
	// DeliveryFailed means the push was rejected, or the device moved to
	// another account.
	DeliveryFailed = "failed"
	// DeliveryEnded means the activity is no longer on this device.
	DeliveryEnded = "ended"
)

// liveDeliveryStatuses are the statuses that occupy a device slot.
var liveDeliveryStatuses = []string{DeliveryPending, DeliveryAccepted, DeliveryActive}

// LiveActivityDelivery is one activity as it exists on one device.
type LiveActivityDelivery struct {
	ID         string `db:"id"`
	ActivityID string `db:"activity_id"`
	DeviceID   string `db:"device_id"`
	Purpose    string `db:"purpose"`
	Status     string `db:"status"`
	// Environment is copied from the device at insert. An ActivityKit token is
	// only valid against the APNs environment it was issued in, so pushing to
	// the wrong one fails silently.
	Environment   string `db:"environment"`
	SchemaVersion int    `db:"schema_version"`
	// NativeActivityID is ActivityKit's own identifier, reported by the phone.
	// It is how a later token registration finds the delivery it belongs to.
	NativeActivityID *string `db:"native_activity_id"`
	// UpdateTokenCiphertext is required for every push after the start.
	// Without it an activity can be created but never changed or ended.
	UpdateTokenCiphertext *string    `db:"update_token_ciphertext"`
	UpdateTokenUpdatedAt  *time.Time `db:"update_token_updated_at"`
	// Diagnostics from the most recent APNs attempt.
	LastEvent      *string    `db:"last_event"`
	LastSequence   int        `db:"last_sequence"`
	LastAPNsStatus *int       `db:"last_apns_status"`
	LastAPNsReason *string    `db:"last_apns_reason"`
	LastAPNsID     *string    `db:"last_apns_id"`
	LastAttemptAt  *time.Time `db:"last_attempt_at"`
	CreatedAt      time.Time  `db:"created_at"`
	UpdatedAt      time.Time  `db:"updated_at"`
	EndedAt        *time.Time `db:"ended_at"`
}

// Live reports whether the delivery still occupies its device.
func (d LiveActivityDelivery) Live() bool {
	return d.Status == DeliveryPending || d.Status == DeliveryAccepted || d.Status == DeliveryActive
}

// Updatable reports whether an update or end push can reach this delivery.
func (d LiveActivityDelivery) Updatable() bool {
	return d.UpdateTokenCiphertext != nil && *d.UpdateTokenCiphertext != ""
}

// DeviceOccupancy is a delivery holding a device slot, with just enough of its
// activity to decide whether it is a real blocker or a leftover to release.
type DeviceOccupancy struct {
	LiveActivityDelivery
	ActivityStatus             string    `db:"activity_status"`
	ActivityKey                *string   `db:"activity_key"`
	ActivitySequence           int       `db:"activity_sequence"`
	ActivityAPNsTimestamp      int64     `db:"activity_apns_timestamp"`
	ActivityExpiresAt          time.Time `db:"activity_expires_at"`
	ActivityRequesterTokenID   *string   `db:"activity_requester_token_id"`
	ActivityRequesterServiceID *string   `db:"activity_requester_service_id"`
	// ActivityLive is false for a delivery whose activity has already finished
	// or lapsed: it is not a blocker, it is a row to clean up.
	ActivityLive bool `db:"activity_live"`
}

// Deliveries stores per-device Live Activity state.
type Deliveries struct {
	q     Querier
	store *Store
}

const deliveryColumns = `id, activity_id, device_id, purpose, status, environment, schema_version,
	native_activity_id, update_token_ciphertext, update_token_updated_at, last_event, last_sequence,
	last_apns_status, last_apns_reason, last_apns_id, last_attempt_at, created_at, updated_at, ended_at`

const deliveryColumnsQualified = `d.id, d.activity_id, d.device_id, d.purpose, d.status,
	d.environment, d.schema_version, d.native_activity_id, d.update_token_ciphertext,
	d.update_token_updated_at, d.last_event, d.last_sequence, d.last_apns_status,
	d.last_apns_reason, d.last_apns_id, d.last_attempt_at, d.created_at, d.updated_at, d.ended_at`

// InsertMany creates the deliveries of a start, one per target device.
//
// A violation of live_activity_deliveries_one_task_per_device_key means another
// activity claimed a target device first; the caller either reports the
// conflict or takes over.
func (s *Deliveries) InsertMany(ctx context.Context, activityID string, schemaVersion int, targets []ActivityTarget, now time.Time) ([]LiveActivityDelivery, error) {
	if len(targets) == 0 {
		return nil, nil
	}
	const q = `
		INSERT INTO live_activity_deliveries
			(id, activity_id, device_id, purpose, status, environment, schema_version,
			 created_at, updated_at)
		SELECT t.id, $1, t.device_id, t.purpose, 'pending', t.environment, $2, $3, $3
		FROM unnest($4::text[], $5::text[], $6::text[], $7::text[])
		     AS t(id, device_id, purpose, environment)
		RETURNING ` + deliveryColumns

	ids := make([]string, len(targets))
	deviceIDs := make([]string, len(targets))
	purposes := make([]string, len(targets))
	environments := make([]string, len(targets))
	for i, t := range targets {
		ids[i], deviceIDs[i], purposes[i], environments[i] = t.DeliveryID, t.DeviceID, t.Purpose, t.Environment
	}
	return queryAll[LiveActivityDelivery](ctx, s.q, "create Live Activity deliveries", q,
		activityID, schemaVersion, Millis(now), ids, deviceIDs, purposes, environments)
}

// ByID loads one delivery.
func (s *Deliveries) ByID(ctx context.Context, id string) (*LiveActivityDelivery, error) {
	const q = `SELECT ` + deliveryColumns + ` FROM live_activity_deliveries WHERE id = $1`
	return queryOne[LiveActivityDelivery](ctx, s.q, "load Live Activity delivery", q, id)
}

// ListForActivity returns an activity's deliveries. With statuses the result is
// restricted to them; an update dispatch usually wants only the live ones.
func (s *Deliveries) ListForActivity(ctx context.Context, activityID string, statuses []string) ([]LiveActivityDelivery, error) {
	const q = `
		SELECT ` + deliveryColumns + ` FROM live_activity_deliveries
		WHERE activity_id = $1 AND ($2::text[] IS NULL OR status = ANY($2))
		ORDER BY created_at, id`
	return queryAll[LiveActivityDelivery](ctx, s.q, "list Live Activity deliveries", q,
		activityID, nullIfEmpty(statuses))
}

// LiveStatuses returns the delivery statuses that occupy a device.
func LiveStatuses() []string { return append([]string(nil), liveDeliveryStatuses...) }

// Occupancy reports which deliveries currently hold the given devices.
//
// Only task deliveries can block: an interaction activity is allowed to sit
// alongside one. Rows whose activity is no longer live come back with
// ActivityLive false — they are not blockers, they are leftovers for the caller
// to release with [Deliveries.Release].
func (s *Deliveries) Occupancy(ctx context.Context, deviceIDs []string, now time.Time) ([]DeviceOccupancy, error) {
	if len(deviceIDs) == 0 {
		return nil, nil
	}
	const q = `
		SELECT ` + deliveryColumnsQualified + `,
		       a.status               AS activity_status,
		       a.key                  AS activity_key,
		       a.sequence             AS activity_sequence,
		       a.apns_timestamp       AS activity_apns_timestamp,
		       a.expires_at           AS activity_expires_at,
		       a.requester_token_id   AS activity_requester_token_id,
		       a.requester_service_id AS activity_requester_service_id,
		       (a.status IN ('starting', 'active', 'partial') AND a.expires_at > $2) AS activity_live
		FROM live_activity_deliveries d
		JOIN live_activities a ON a.id = d.activity_id
		WHERE d.device_id = ANY($1)
		  AND d.purpose = 'task'
		  AND d.status IN ('pending', 'accepted', 'active')
		  AND a.interaction_id IS NULL
		ORDER BY d.created_at, d.id`
	return queryAll[DeviceOccupancy](ctx, s.q, "scan device occupancy", q, deviceIDs, Millis(now))
}

// Release frees delivery slots without pushing anything. It is the lazy
// cleanup for deliveries whose activity already finished: the phone stopped
// showing them long ago, only the row lingered.
func (s *Deliveries) Release(ctx context.Context, ids []string, now time.Time) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	const q = `
		UPDATE live_activity_deliveries
		SET status = 'ended', ended_at = $2, updated_at = $2
		WHERE id = ANY($1) AND status IN ('pending', 'accepted', 'active')`
	return execAffected(ctx, s.q, "release deliveries", q, ids, Millis(now))
}

// EndParams releases a delivery as part of a takeover.
type EndParams struct {
	DeliveryID string
	// Sequence is the synthetic sequence of the end that was pushed.
	Sequence   int
	APNsStatus *int
	APNsReason *string
	APNsID     *string
	Now        time.Time
}

// End releases a delivery after an end push, whether or not the push worked.
//
// The delivery is released even when the end push fails so an unreachable
// activity cannot permanently occupy the device slot.
func (s *Deliveries) End(ctx context.Context, p EndParams) (*LiveActivityDelivery, error) {
	const q = `
		UPDATE live_activity_deliveries SET
			status                  = 'ended',
			last_event              = 'end',
			last_sequence           = $2,
			last_apns_status        = nullif($3::integer, 0),
			last_apns_reason        = $4,
			last_apns_id            = $5,
			last_attempt_at         = $6,
			updated_at              = $6,
			ended_at                = $6,
			update_token_ciphertext = NULL,
			update_token_updated_at = NULL
		WHERE id = $1
		RETURNING ` + deliveryColumns
	return queryOne[LiveActivityDelivery](ctx, s.q, "end delivery", q,
		p.DeliveryID, p.Sequence, p.APNsStatus, p.APNsReason, p.APNsID, Millis(p.Now))
}

// FailForDevice invalidates every live delivery on a device.
//
// It runs when an APNs token turns up registered to a different account: the
// activities the previous owner started must not keep receiving updates on a
// phone that now belongs to someone else, and their update tokens are dropped
// so nothing can push to them again.
func (s *Deliveries) FailForDevice(ctx context.Context, deviceID, reason string, now time.Time) (int64, error) {
	const q = `
		UPDATE live_activity_deliveries SET
			status                  = 'failed',
			update_token_ciphertext = NULL,
			update_token_updated_at = NULL,
			last_apns_reason        = $2,
			updated_at              = $3
		WHERE device_id = $1 AND status IN ('pending', 'accepted', 'active')`
	return execAffected(ctx, s.q, "invalidate device deliveries", q, deviceID, reason, Millis(now))
}

// RecordAttemptParams records one APNs call.
type RecordAttemptParams struct {
	AttemptID          string
	ActivityID         string
	DeliveryID         string
	DeviceID           string
	OperationID        string
	RequesterTokenID   *string
	RequesterServiceID *string
	Event              string
	Sequence           int
	// Accepted is whether APNs took the push. A synthetic failure — no HTTP
	// call was made at all — is Accepted false with a reason and no status.
	Accepted   bool
	APNsStatus *int
	APNsReason *string
	APNsID     *string
	// TokenInvalid means APNs reported the push token as permanently
	// unusable, so it is dropped rather than retried forever.
	TokenInvalid bool
	Now          time.Time
}

// RecordAttempt writes the audit row and the delivery's new state together.
//
// Splitting them would let a push be recorded without its consequence, or a
// delivery move without the evidence of why. When APNs reports the token as
// permanently invalid the update token is dropped too, and for a start — where
// the token that failed was the device's push-to-start token — the device's
// copy is cleared as well, because it will never work again.
func (s *Deliveries) RecordAttempt(ctx context.Context, p RecordAttemptParams) (*LiveActivityDelivery, error) {
	var delivery *LiveActivityDelivery
	err := s.store.Tx(ctx, func(ctx context.Context, tx *Store) error {
		if _, err := tx.Attempts.Insert(ctx, CreateAttemptParams{
			ID:                 p.AttemptID,
			ActivityID:         p.ActivityID,
			DeliveryID:         p.DeliveryID,
			OperationID:        p.OperationID,
			RequesterTokenID:   p.RequesterTokenID,
			RequesterServiceID: p.RequesterServiceID,
			Event:              p.Event,
			Sequence:           p.Sequence,
			APNsStatus:         p.APNsStatus,
			APNsReason:         p.APNsReason,
			APNsID:             p.APNsID,
			Now:                p.Now,
		}); err != nil {
			return err
		}

		updated, err := tx.Deliveries.applyAttempt(ctx, p)
		if err != nil {
			return err
		}
		if p.TokenInvalid && p.Event == OperationStart && p.DeviceID != "" {
			if err := tx.Devices.ClearPushToStartToken(ctx, p.DeviceID); err != nil {
				return err
			}
		}
		delivery = updated
		return nil
	})
	if err != nil {
		return nil, err
	}
	return delivery, nil
}

func (s *Deliveries) applyAttempt(ctx context.Context, p RecordAttemptParams) (*LiveActivityDelivery, error) {
	// An update that APNs refused leaves the status alone: the delivery may
	// still be perfectly alive and the next update may well land. Only a start
	// failure is conclusive, because nothing was ever created.
	const q = `
		UPDATE live_activity_deliveries SET
			status = CASE
				WHEN $2 = 'end'   THEN 'ended'
				WHEN $3::boolean  THEN 'accepted'
				WHEN $2 = 'start' THEN 'failed'
				ELSE status END,
			last_event       = $2,
			last_sequence    = $4,
			last_apns_status = nullif($5::integer, 0),
			last_apns_reason = $6,
			last_apns_id     = $7,
			last_attempt_at  = $8,
			updated_at       = $8,
			ended_at         = CASE WHEN $2 = 'end' THEN $8 ELSE ended_at END,
			update_token_ciphertext = CASE WHEN $9::boolean THEN NULL ELSE update_token_ciphertext END,
			update_token_updated_at = CASE WHEN $9::boolean THEN NULL ELSE update_token_updated_at END
		WHERE id = $1
		RETURNING ` + deliveryColumns
	return queryOne[LiveActivityDelivery](ctx, s.q, "record delivery attempt", q,
		p.DeliveryID, p.Event, p.Accepted, p.Sequence, p.APNsStatus, p.APNsReason, p.APNsID,
		Millis(p.Now), p.TokenInvalid)
}

// AssociationParams narrows the deliveries an update-token registration could
// belong to.
type AssociationParams struct {
	DeviceID      string
	UserID        string
	SchemaVersion int
	// ActivityID, when the client knows which activity it is registering for.
	ActivityID *string
	// NativeActivityID is ActivityKit's identifier. When the client sends one,
	// an exact match is authoritative; otherwise the search is for a delivery
	// that has not been associated yet.
	NativeActivityID *string
	// Limit is how many candidates to fetch. Callers ask for two: one is an
	// answer, two is an ambiguity they must refuse rather than guess at.
	Limit int
}

// AssociationCandidates finds the delivery an update token belongs to.
//
// The phone cannot tell the server which Hark activity it just created — it
// only knows ActivityKit's own identifier — so the delivery has to be inferred.
// An exact match on a previously reported native id wins; failing that, the
// search is narrowed to deliveries that are still waiting to be associated.
// Callers must treat two candidates as ambiguous and refuse, because guessing
// would attach the token to the wrong activity and silently break both.
func (s *Deliveries) AssociationCandidates(ctx context.Context, p AssociationParams) ([]LiveActivityDelivery, error) {
	const q = `
		SELECT ` + deliveryColumnsQualified + `
		FROM live_activity_deliveries d
		JOIN live_activities a ON a.id = d.activity_id
		WHERE d.device_id = $1
		  AND a.user_id = $2
		  AND a.schema_version = $3
		  AND d.schema_version = $3
		  AND a.status IN ('starting', 'active', 'partial')
		  AND ($4::text IS NULL OR a.id = $4)
		  AND CASE
		        WHEN $5::text IS NOT NULL AND $6::boolean THEN d.native_activity_id = $5
		        WHEN $5::text IS NOT NULL                 THEN d.native_activity_id IS NULL
		        ELSE d.update_token_ciphertext IS NULL
		      END
		ORDER BY d.created_at DESC, d.id DESC
		LIMIT $7`

	limit := max(p.Limit, 1)
	if p.NativeActivityID != nil {
		exact, err := queryAll[LiveActivityDelivery](ctx, s.q, "match delivery by native id", q,
			p.DeviceID, p.UserID, p.SchemaVersion, p.ActivityID, p.NativeActivityID, true, limit)
		if err != nil || len(exact) > 0 {
			return exact, err
		}
	}
	return queryAll[LiveActivityDelivery](ctx, s.q, "match unassociated delivery", q,
		p.DeviceID, p.UserID, p.SchemaVersion, p.ActivityID, p.NativeActivityID, false, limit)
}

// DeliveryRegistration is a delivery that may still accept an update token,
// with the activity deadline the registration credential is bound to.
type DeliveryRegistration struct {
	LiveActivityDelivery
	ActivityExpiresAt time.Time `db:"activity_expires_at"`
}

// ForRegistration loads a delivery that an unauthenticated token registration
// may target: one whose activity is still running and unexpired. The caller
// then verifies the credential the start push carried before writing anything.
func (s *Deliveries) ForRegistration(ctx context.Context, deliveryID string, now time.Time) (*DeliveryRegistration, error) {
	const q = `
		SELECT ` + deliveryColumnsQualified + `, a.expires_at AS activity_expires_at
		FROM live_activity_deliveries d
		JOIN live_activities a ON a.id = d.activity_id
		WHERE d.id = $1
		  AND d.status IN ('pending', 'accepted', 'active')
		  AND a.status IN ('starting', 'active', 'partial')
		  AND a.expires_at > $2`
	return queryOne[DeliveryRegistration](ctx, s.q, "load delivery for registration", q,
		deliveryID, Millis(now))
}

// SetUpdateTokenParams associates an ActivityKit update token.
type SetUpdateTokenParams struct {
	DeliveryID       string
	NativeActivityID *string
	Ciphertext       string
	// Environment and SchemaVersion are set only by the authenticated
	// registration path, which is the one that knows them. The unauthenticated
	// path leaves the values the start recorded alone.
	Environment   Set[string]
	SchemaVersion Set[int]
	Now           time.Time
}

// SetUpdateToken records the token that makes a delivery updatable, and marks
// it active: the phone has confirmed the activity exists.
func (s *Deliveries) SetUpdateToken(ctx context.Context, p SetUpdateTokenParams) (*LiveActivityDelivery, error) {
	const q = `
		UPDATE live_activity_deliveries SET
			native_activity_id      = coalesce($2, native_activity_id),
			update_token_ciphertext = $3,
			update_token_updated_at = $4,
			status                  = 'active',
			environment             = CASE WHEN $5::boolean THEN $6::text    ELSE environment    END,
			schema_version          = CASE WHEN $7::boolean THEN $8::integer ELSE schema_version END,
			updated_at              = $4
		WHERE id = $1
		RETURNING ` + deliveryColumns

	envSet, env := p.Environment.args()
	versionSet, version := p.SchemaVersion.args()
	return queryOne[LiveActivityDelivery](ctx, s.q, "store update token", q,
		p.DeliveryID, p.NativeActivityID, p.Ciphertext, Millis(p.Now),
		envSet, env, versionSet, version)
}

package db

import (
	"context"
	"time"
)

// Device capability constants. Each version column is a capability flag rather
// than a schema number in practice: the client sets it to the version it
// implements, and the server only ever compares it for equality with the
// version it knows how to talk to.
const (
	// PlatformIOS is the only platform Hark pushes to.
	PlatformIOS = "ios"
	// LiveActivitySchemaVersion is the ActivityKit content-state version this
	// server emits.
	LiveActivitySchemaVersion = 1
	// InteractionSchemaVersion marks a device that understands
	// notification-action interactions.
	InteractionSchemaVersion = 1
	// LiveActivityInteractionVersion marks a device that understands
	// interactive Live Activities.
	LiveActivityInteractionVersion = 1

	// EnvironmentSandbox and EnvironmentProduction are the two APNs
	// environments a device token can belong to. Pushing a token to the wrong
	// one silently fails, so the environment travels with the token.
	EnvironmentSandbox    = "sandbox"
	EnvironmentProduction = "production"
)

// Device is one registered iOS device, keyed by its APNs token.
type Device struct {
	ID     string `db:"id"`
	UserID string `db:"user_id"`
	// APNsToken is the raw push address, lowercase hex.
	APNsToken string  `db:"apns_token"`
	Platform  string  `db:"platform"`
	Name      *string `db:"name"`
	// Active goes false when APNs reports the token permanently invalid. The
	// row survives so history keeps resolving; a re-registration revives it.
	Active bool `db:"active"`
	// The push-to-start token lets the server create a Live Activity on this
	// device without the app running.
	PushToStartTokenCiphertext *string    `db:"push_to_start_token_ciphertext"`
	PushToStartEnvironment     *string    `db:"push_to_start_environment"`
	PushToStartUpdatedAt       *time.Time `db:"push_to_start_updated_at"`
	LiveActivitySchemaVersion  *int       `db:"live_activity_schema_version"`
	// Capability flags reported at registration.
	InteractionSchemaVersion       *int      `db:"interaction_schema_version"`
	LiveActivityInteractionVersion *int      `db:"live_activity_interaction_version"`
	CreatedAt                      time.Time `db:"created_at"`
	LastSeenAt                     time.Time `db:"last_seen_at"`
}

// Pushable reports whether an ordinary alert can be sent to this device.
func (d Device) Pushable() bool { return d.Active && d.Platform == PlatformIOS }

// LiveActivityCapable reports whether a Live Activity can be started on this
// device: it needs a push-to-start token, a schema version this server speaks,
// and a known APNs environment.
func (d Device) LiveActivityCapable() bool {
	if !d.Pushable() || d.PushToStartTokenCiphertext == nil || *d.PushToStartTokenCiphertext == "" {
		return false
	}
	if d.LiveActivitySchemaVersion == nil || *d.LiveActivitySchemaVersion != LiveActivitySchemaVersion {
		return false
	}
	if d.PushToStartEnvironment == nil {
		return false
	}
	return *d.PushToStartEnvironment == EnvironmentSandbox || *d.PushToStartEnvironment == EnvironmentProduction
}

// InteractionCapable reports whether this device can answer a notification
// action, which is what an interaction delivered as an alert requires.
func (d Device) InteractionCapable() bool {
	return d.Pushable() && d.InteractionSchemaVersion != nil &&
		*d.InteractionSchemaVersion == InteractionSchemaVersion
}

// InteractiveLiveActivityCapable reports whether this device can present an
// interaction inside a Live Activity and answer it from the Lock Screen.
func (d Device) InteractiveLiveActivityCapable() bool {
	return d.LiveActivityCapable() && d.LiveActivityInteractionVersion != nil &&
		*d.LiveActivityInteractionVersion == LiveActivityInteractionVersion
}

// Devices stores registered devices.
type Devices struct{ q Querier }

const deviceColumns = `id, user_id, apns_token, platform, name, active,
	push_to_start_token_ciphertext, push_to_start_environment, push_to_start_updated_at,
	live_activity_schema_version, interaction_schema_version, live_activity_interaction_version,
	created_at, last_seen_at`

// RegisterDeviceParams registers or refreshes a device.
//
// The optional fields are a full replace, not a merge: a request that omits
// deviceName or a capability version clears the stored value. The client sends
// its complete current state on every registration, so anything omitted is
// genuinely absent rather than unchanged.
type RegisterDeviceParams struct {
	// ID is used only on the insert path; a re-registration keeps the id the
	// device already has.
	ID                             string
	UserID                         string
	APNsToken                      string
	Name                           *string
	InteractionSchemaVersion       *int
	LiveActivityInteractionVersion *int
	Now                            time.Time
}

// RegisteredDevice is the outcome of a registration.
type RegisteredDevice struct {
	Device Device
	// PreviousUserID is the owner the token had before this registration, or
	// nil when the token was unseen. A different value means the device moved
	// between accounts and its Live Activity state must be invalidated — see
	// [Deliveries.FailForDevice].
	PreviousUserID *string
}

// OwnerChanged reports whether the device token was previously registered to a
// different account.
func (r RegisteredDevice) OwnerChanged() bool {
	return r.PreviousUserID != nil && *r.PreviousUserID != r.Device.UserID
}

type registeredDeviceRow struct {
	Device
	PreviousUserID *string `db:"previous_user_id"`
}

// Register upserts a device on its APNs token.
//
// When the token turns out to belong to a different account, the push-to-start
// state is dropped in the same statement: those credentials were issued to the
// previous owner's app installation and must not be usable by the new one.
func (s *Devices) Register(ctx context.Context, p RegisterDeviceParams) (*RegisteredDevice, error) {
	const q = `
		WITH prior AS (
			SELECT user_id FROM devices WHERE apns_token = $3
		), upserted AS (
			INSERT INTO devices (id, user_id, apns_token, platform, name, active,
			                     interaction_schema_version, live_activity_interaction_version,
			                     created_at, last_seen_at)
			VALUES ($1, $2, $3, 'ios', $4, true, $5, $6, $7, $7)
			ON CONFLICT (apns_token) DO UPDATE SET
				user_id                           = EXCLUDED.user_id,
				name                              = EXCLUDED.name,
				interaction_schema_version        = EXCLUDED.interaction_schema_version,
				live_activity_interaction_version = EXCLUDED.live_activity_interaction_version,
				active                            = true,
				last_seen_at                      = EXCLUDED.last_seen_at,
				push_to_start_token_ciphertext    = CASE WHEN devices.user_id = EXCLUDED.user_id
				                                         THEN devices.push_to_start_token_ciphertext END,
				push_to_start_environment         = CASE WHEN devices.user_id = EXCLUDED.user_id
				                                         THEN devices.push_to_start_environment END,
				push_to_start_updated_at          = CASE WHEN devices.user_id = EXCLUDED.user_id
				                                         THEN devices.push_to_start_updated_at END,
				live_activity_schema_version      = CASE WHEN devices.user_id = EXCLUDED.user_id
				                                         THEN devices.live_activity_schema_version END
			RETURNING ` + deviceColumns + `
		)
		SELECT upserted.*, prior.user_id AS previous_user_id
		FROM upserted LEFT JOIN prior ON true`

	row, err := queryOne[registeredDeviceRow](ctx, s.q, "register device", q,
		p.ID, p.UserID, p.APNsToken, p.Name,
		p.InteractionSchemaVersion, p.LiveActivityInteractionVersion, Millis(p.Now))
	if err != nil {
		return nil, err
	}
	return &RegisteredDevice{Device: row.Device, PreviousUserID: row.PreviousUserID}, nil
}

// ByID loads a device the caller owns.
func (s *Devices) ByID(ctx context.Context, id, userID string) (*Device, error) {
	const q = `SELECT ` + deviceColumns + ` FROM devices WHERE id = $1 AND user_id = $2`
	return queryOne[Device](ctx, s.q, "load device", q, id, userID)
}

// ListForUser returns every device on the account, most recently seen first.
func (s *Devices) ListForUser(ctx context.Context, userID string) ([]Device, error) {
	const q = `SELECT ` + deviceColumns + ` FROM devices
		WHERE user_id = $1 ORDER BY last_seen_at DESC, id DESC`
	return queryAll[Device](ctx, s.q, "list devices", q, userID)
}

// ListTargets returns the devices a push may go to, most recently seen first.
//
// With ids, the result is restricted to them — and the caller must compare the
// returned count against the requested count, because a request naming a
// device that is not on the account is a client error, not a silent no-op.
// Without ids, every active iOS device is returned.
func (s *Devices) ListTargets(ctx context.Context, userID string, ids []string) ([]Device, error) {
	const q = `
		SELECT ` + deviceColumns + ` FROM devices
		WHERE user_id = $1
		  AND platform = 'ios'
		  AND active
		  AND ($2::text[] IS NULL OR id = ANY($2))
		ORDER BY last_seen_at DESC, id DESC`
	return queryAll[Device](ctx, s.q, "list push targets", q, userID, nullIfEmpty(ids))
}

// ListByIDs returns the named devices on the account regardless of state, so a
// caller can tell "device is not yours" from "device is inactive".
func (s *Devices) ListByIDs(ctx context.Context, userID string, ids []string) ([]Device, error) {
	const q = `SELECT ` + deviceColumns + ` FROM devices
		WHERE user_id = $1 AND id = ANY($2) ORDER BY last_seen_at DESC, id DESC`
	return queryAll[Device](ctx, s.q, "load devices", q, userID, ids)
}

// SetPushToStartTokenParams stores an ActivityKit push-to-start token.
type SetPushToStartTokenParams struct {
	DeviceID      string
	UserID        string
	Ciphertext    string
	Environment   string
	SchemaVersion int
	Now           time.Time
}

// SetPushToStartToken records the credential required to start Live Activities
// on an active device.
func (s *Devices) SetPushToStartToken(ctx context.Context, p SetPushToStartTokenParams) (*Device, error) {
	const q = `
		UPDATE devices SET
			push_to_start_token_ciphertext = $3,
			push_to_start_environment      = $4,
			live_activity_schema_version   = $5,
			push_to_start_updated_at       = $6,
			last_seen_at                   = $6
		WHERE id = $1 AND user_id = $2 AND active
		RETURNING ` + deviceColumns
	return queryOne[Device](ctx, s.q, "store push-to-start token", q,
		p.DeviceID, p.UserID, p.Ciphertext, p.Environment, p.SchemaVersion, Millis(p.Now))
}

// ClearPushToStartToken drops the push-to-start credential. It runs when APNs
// rejects a start push as permanently undeliverable: keeping a token APNs has
// disowned would make the device look capable when it is not.
func (s *Devices) ClearPushToStartToken(ctx context.Context, id string) error {
	const q = `
		UPDATE devices SET
			push_to_start_token_ciphertext = NULL,
			push_to_start_environment      = NULL,
			push_to_start_updated_at       = NULL,
			live_activity_schema_version   = NULL
		WHERE id = $1`
	_, err := execAffected(ctx, s.q, "clear push-to-start token", q, id)
	return err
}

// Deactivate marks devices inactive by APNs token. It runs after a fan-out,
// over the tokens APNs reported as permanently invalid. Rows are never deleted
// here: history keeps referring to them.
func (s *Devices) Deactivate(ctx context.Context, apnsTokens []string) (int64, error) {
	if len(apnsTokens) == 0 {
		return 0, nil
	}
	const q = `UPDATE devices SET active = false WHERE apns_token = ANY($1) AND active`
	return execAffected(ctx, s.q, "deactivate devices", q, apnsTokens)
}

// DeactivateByID marks one device inactive.
func (s *Devices) DeactivateByID(ctx context.Context, id string, now time.Time) (bool, error) {
	const q = `UPDATE devices SET active = false, last_seen_at = $2 WHERE id = $1 AND active`
	return execMatched(ctx, s.q, "deactivate device", q, id, Millis(now))
}

// Delete unregisters a device by id.
//
// The cascade removes the device's Live Activity deliveries without sending an
// end push, so an activity may remain on screen without a server record.
// Interactions the device answered lose their
// responding_device_id.
func (s *Devices) Delete(ctx context.Context, id, userID string) (bool, error) {
	const q = `DELETE FROM devices WHERE id = $1 AND user_id = $2`
	return execMatched(ctx, s.q, "delete device", q, id, userID)
}

// nullIfEmpty turns an empty slice into a NULL parameter so a query can use
// "$n IS NULL OR col = ANY($n)" for an optional filter.
func nullIfEmpty(v []string) []string {
	if len(v) == 0 {
		return nil
	}
	return v
}

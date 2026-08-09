package db

import (
	"context"
	"time"
)

// Device-authorization statuses. pending is the only non-terminal one.
const (
	DeviceAuthPending  = "pending"
	DeviceAuthApproved = "approved"
	DeviceAuthDenied   = "denied"
	DeviceAuthExpired  = "expired"
	DeviceAuthConsumed = "consumed"
)

// DeviceAuthorization is a CLI pairing request: the OAuth device flow, shaped
// for one account.
//
// The device code is stored only as a digest — the CLI holds the plaintext and
// presents it when polling — while the user code is short and readable because
// a person types it into a browser.
type DeviceAuthorization struct {
	ID              string   `db:"id"`
	DeviceCodeHash  string   `db:"device_code_hash"`
	UserCode        string   `db:"user_code"`
	ClientName      string   `db:"client_name"`
	RequestedScopes []string `db:"requested_scopes"`
	Status          string   `db:"status"`
	// ApprovedUserID is set on approval and cleared on denial.
	ApprovedUserID *string `db:"approved_user_id"`
	// ExpiresAt is the request's own TTL; TokenExpiresAt becomes the expiry of
	// the token it issues, which is much longer.
	ExpiresAt      time.Time `db:"expires_at"`
	TokenExpiresAt time.Time `db:"token_expires_at"`
	// PollIntervalSeconds is what the client was told to wait. It grows when
	// the client ignores it.
	PollIntervalSeconds int        `db:"poll_interval_seconds"`
	LastPolledAt        *time.Time `db:"last_polled_at"`
	ResolvedAt          *time.Time `db:"resolved_at"`
	CreatedAt           time.Time  `db:"created_at"`
}

// Pending reports whether the request is still awaiting a decision at now.
func (d DeviceAuthorization) Pending(now time.Time) bool {
	return d.Status == DeviceAuthPending && d.ExpiresAt.After(now)
}

// DeviceAuthorizations stores CLI pairing requests.
type DeviceAuthorizations struct{ q Querier }

const deviceAuthColumns = `id, device_code_hash, user_code, client_name, requested_scopes,
	status, approved_user_id, expires_at, token_expires_at, poll_interval_seconds,
	last_polled_at, resolved_at, created_at`

// CreateDeviceAuthorizationParams starts a pairing request.
type CreateDeviceAuthorizationParams struct {
	ID                  string
	DeviceCodeHash      string
	UserCode            string
	ClientName          string
	RequestedScopes     []string
	ExpiresAt           time.Time
	TokenExpiresAt      time.Time
	PollIntervalSeconds int
	Now                 time.Time
}

// Create inserts a pairing request. A unique violation means the freshly
// generated device code or user code collided with a live one; the caller
// retries with a new pair rather than failing the client.
func (s *DeviceAuthorizations) Create(ctx context.Context, p CreateDeviceAuthorizationParams) (*DeviceAuthorization, error) {
	const q = `
		INSERT INTO device_authorization_requests
			(id, device_code_hash, user_code, client_name, requested_scopes, status,
			 expires_at, token_expires_at, poll_interval_seconds, created_at)
		VALUES ($1, $2, $3, $4, $5, 'pending', $6, $7, $8, $9)
		RETURNING ` + deviceAuthColumns
	return queryOne[DeviceAuthorization](ctx, s.q, "create device authorization", q,
		p.ID, p.DeviceCodeHash, p.UserCode, p.ClientName, p.RequestedScopes,
		Millis(p.ExpiresAt), Millis(p.TokenExpiresAt), p.PollIntervalSeconds, Millis(p.Now))
}

// ByDeviceCodeHash resolves a polling CLI's credential.
func (s *DeviceAuthorizations) ByDeviceCodeHash(ctx context.Context, hash string) (*DeviceAuthorization, error) {
	const q = `SELECT ` + deviceAuthColumns + ` FROM device_authorization_requests WHERE device_code_hash = $1`
	return queryOne[DeviceAuthorization](ctx, s.q, "load device authorization", q, hash)
}

// ByUserCode resolves the code a person typed into the browser.
func (s *DeviceAuthorizations) ByUserCode(ctx context.Context, userCode string) (*DeviceAuthorization, error) {
	const q = `SELECT ` + deviceAuthColumns + ` FROM device_authorization_requests WHERE user_code = $1`
	return queryOne[DeviceAuthorization](ctx, s.q, "load device authorization", q, userCode)
}

// RecordPoll stamps a poll without changing anything else.
func (s *DeviceAuthorizations) RecordPoll(ctx context.Context, id string, now time.Time) error {
	const q = `UPDATE device_authorization_requests SET last_polled_at = $2 WHERE id = $1`
	return execOne(ctx, s.q, "record poll", q, id, Millis(now))
}

// SlowDown widens the poll interval for a client that polled too soon, up to a
// ceiling, and returns the new value so it can be handed back in the response.
func (s *DeviceAuthorizations) SlowDown(ctx context.Context, id string, step, max int, now time.Time) (*DeviceAuthorization, error) {
	const q = `
		UPDATE device_authorization_requests
		SET poll_interval_seconds = least(poll_interval_seconds + $2, $3), last_polled_at = $4
		WHERE id = $1
		RETURNING ` + deviceAuthColumns
	return queryOne[DeviceAuthorization](ctx, s.q, "slow down polling", q, id, step, max, Millis(now))
}

// Approve records the account owner's consent. It is guarded on the request
// still being pending and unexpired, so an approval cannot resurrect a request
// that already timed out or was denied in another tab.
func (s *DeviceAuthorizations) Approve(ctx context.Context, userCode, userID string, now time.Time) (*DeviceAuthorization, error) {
	const q = `
		UPDATE device_authorization_requests
		SET status = 'approved', approved_user_id = $2, resolved_at = $3
		WHERE user_code = $1 AND status = 'pending' AND expires_at > $3
		RETURNING ` + deviceAuthColumns
	return queryOne[DeviceAuthorization](ctx, s.q, "approve device authorization", q,
		userCode, userID, Millis(now))
}

// Deny records a refusal, clearing any approver.
func (s *DeviceAuthorizations) Deny(ctx context.Context, userCode string, now time.Time) (*DeviceAuthorization, error) {
	const q = `
		UPDATE device_authorization_requests
		SET status = 'denied', approved_user_id = NULL, resolved_at = $2
		WHERE user_code = $1 AND status = 'pending' AND expires_at > $2
		RETURNING ` + deviceAuthColumns
	return queryOne[DeviceAuthorization](ctx, s.q, "deny device authorization", q, userCode, Millis(now))
}

// DenyByID refuses an already-approved request by id. This is the token-cap
// path: consent existed, but the account cannot hold another token, so the
// grant is burned rather than left to be retried forever.
func (s *DeviceAuthorizations) DenyByID(ctx context.Context, id string, now time.Time) (*DeviceAuthorization, error) {
	const q = `
		UPDATE device_authorization_requests
		SET status = 'denied', resolved_at = $2
		WHERE id = $1 AND status <> 'consumed'
		RETURNING ` + deviceAuthColumns
	return queryOne[DeviceAuthorization](ctx, s.q, "deny device authorization", q, id, Millis(now))
}

// MarkExpired lazily retires a request whose TTL has passed. There is no expiry
// job: any read that could act on a request expires it first.
func (s *DeviceAuthorizations) MarkExpired(ctx context.Context, id string, now time.Time) (*DeviceAuthorization, error) {
	const q = `
		UPDATE device_authorization_requests
		SET status = 'expired', resolved_at = coalesce(resolved_at, $2), last_polled_at = $2
		WHERE id = $1 AND status IN ('pending', 'approved') AND expires_at <= $2
		RETURNING ` + deviceAuthColumns
	return queryOne[DeviceAuthorization](ctx, s.q, "expire device authorization", q, id, Millis(now))
}

// Consume marks an approved request as spent, which is what authorises issuing
// the token. It is guarded so that two racing polls cannot both issue one.
func (s *DeviceAuthorizations) Consume(ctx context.Context, id string, now time.Time) (bool, error) {
	const q = `
		UPDATE device_authorization_requests
		SET status = 'consumed', resolved_at = $2
		WHERE id = $1 AND status = 'approved' AND expires_at > $2`
	return execMatched(ctx, s.q, "consume device authorization", q, id, Millis(now))
}

// PurgeResolved deletes requests that are long past use: expired ones, and
// resolved ones older than retention. Called opportunistically when a new
// request starts, so the table stays small without a scheduled job.
func (s *DeviceAuthorizations) PurgeResolved(ctx context.Context, now time.Time, retention time.Duration) (int64, error) {
	const q = `
		DELETE FROM device_authorization_requests
		WHERE expires_at <= $1
		   OR (status IN ('denied', 'expired', 'consumed') AND resolved_at <= $1)`
	return execAffected(ctx, s.q, "purge device authorizations", q, Millis(now.Add(-retention)))
}

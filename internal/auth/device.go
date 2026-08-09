package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/id"
)

// Device-grant constants.
//
// The shape is OAuth 2.0's device authorization grant (RFC 8628): a headless
// client asks for a pair of codes, shows the human-readable one, and polls
// while the human approves it in a browser that already holds a session.
const (
	// DeviceRequestTTL is how long a pairing request stays approvable. Long
	// enough to walk to another machine, short enough that an abandoned code on
	// a terminal scrollback is worthless by the time anyone reads it.
	DeviceRequestTTL = 10 * time.Minute

	// DevicePollInterval is the pace the client is told to poll at.
	DevicePollInterval = 5 * time.Second

	// DevicePollBackoffStep and DevicePollIntervalMax bound the ratchet applied
	// to a client that polls faster than it was told. The interval only ever
	// grows: a client cannot win back its old pace by behaving.
	DevicePollBackoffStep = 5 * time.Second
	DevicePollIntervalMax = 30 * time.Second

	// devicePollSlack absorbs clock and network jitter, so a client that
	// honestly waited its interval is not punished for arriving a hair early.
	devicePollSlack = 250 * time.Millisecond

	// deviceRequestRetention is how long resolved requests are kept before the
	// opportunistic purge removes them. They are kept at all only so that a CLI
	// polling after a decision gets a precise answer instead of "unknown code".
	deviceRequestRetention = 24 * time.Hour

	// deviceCodeAttempts is how many fresh (device code, user code) pairs are
	// tried before giving up. A collision needs the short user code to repeat
	// among the live requests; three tries make a spurious failure impossible
	// in practice.
	deviceCodeAttempts = 3

	// MaxDeviceClientNameLength bounds the name the human is shown on the
	// approval screen.
	MaxDeviceClientNameLength = 80
)

// StartDeviceGrantParams opens a pairing request.
type StartDeviceGrantParams struct {
	// ClientName is shown to the human approving the request and becomes the
	// issued token's name. Required.
	ClientName string
	// Scopes are what the client is asking for. The human sees this list.
	Scopes []string
	// TokenExpiresIn is the lifetime of the token this request would issue, not
	// of the request itself. Zero uses [DefaultAPITokenLifetime].
	TokenExpiresIn time.Duration
}

// DeviceGrant is a freshly opened pairing request together with the device code
// that only the requesting client ever sees.
type DeviceGrant struct {
	Request    *db.DeviceAuthorization
	DeviceCode string
}

// StartDeviceGrant opens a pairing request. It requires no credential — that is
// the point of the flow — so the transport must rate limit it.
func (s *Service) StartDeviceGrant(ctx context.Context, p StartDeviceGrantParams) (*DeviceGrant, error) {
	name := strings.TrimSpace(p.ClientName)
	if name == "" || len(name) > MaxDeviceClientNameLength {
		return nil, invalid("client_name", fmt.Sprintf("must be 1-%d characters", MaxDeviceClientNameLength))
	}
	scopes, err := validateScopes(p.Scopes)
	if err != nil {
		return nil, err
	}
	tokenLifetime := p.TokenExpiresIn
	if tokenLifetime == 0 {
		tokenLifetime = DefaultAPITokenLifetime
	}
	if tokenLifetime, err = validateLifetime("token_expires_in_seconds", tokenLifetime); err != nil {
		return nil, err
	}

	now := s.Now()

	// Opportunistic housekeeping: the table stays small without a scheduled
	// job, and a failure here must not fail the request.
	_, _ = s.store.DeviceAuth.PurgeResolved(ctx, now, deviceRequestRetention)

	for range deviceCodeAttempts {
		deviceCode := NewDeviceCode()
		request, err := s.store.DeviceAuth.Create(ctx, db.CreateDeviceAuthorizationParams{
			ID:                  id.New(),
			DeviceCodeHash:      DeviceCodeHash(deviceCode),
			UserCode:            NewUserCode(),
			ClientName:          name,
			RequestedScopes:     scopes,
			ExpiresAt:           now.Add(DeviceRequestTTL),
			TokenExpiresAt:      now.Add(tokenLifetime),
			PollIntervalSeconds: int(DevicePollInterval.Seconds()),
			Now:                 now,
		})
		switch {
		case err == nil:
			return &DeviceGrant{Request: request, DeviceCode: deviceCode}, nil
		case db.IsUniqueViolation(err):
			// Either code collided. Discard both and try an entirely new pair
			// rather than reusing the half that was fine.
			continue
		default:
			return nil, fmt.Errorf("auth: create device authorization: %w", err)
		}
	}
	return nil, ErrUnavailable
}

// DeviceGrantState is the outcome of one poll.
type DeviceGrantState string

const (
	// DeviceGrantPending means nobody has decided yet. Keep polling.
	DeviceGrantPending DeviceGrantState = "pending"
	// DeviceGrantSlowDown means the client polled early; the interval grew.
	DeviceGrantSlowDown DeviceGrantState = "slow_down"
	// DeviceGrantDenied means the human refused.
	DeviceGrantDenied DeviceGrantState = "denied"
	// DeviceGrantExpired means nobody decided in time.
	DeviceGrantExpired DeviceGrantState = "expired"
	// DeviceGrantConsumed means this request already issued its token.
	DeviceGrantConsumed DeviceGrantState = "consumed"
	// DeviceGrantTokenLimit means the account cannot hold another token, so the
	// approval was burned rather than left to be retried forever.
	DeviceGrantTokenLimit DeviceGrantState = "token_limit"
	// DeviceGrantIssued means the poll succeeded and Secret holds the token.
	DeviceGrantIssued DeviceGrantState = "issued"
)

// DeviceGrantResult is what one poll produced.
type DeviceGrantResult struct {
	State DeviceGrantState
	// Interval is the pace the client should poll at from now on. Meaningful
	// for [DeviceGrantPending] and [DeviceGrantSlowDown].
	Interval time.Duration
	// Secret and Token are set only for [DeviceGrantIssued].
	Secret string
	Token  *db.APIToken
}

// PollDeviceGrant advances a pairing request one step.
//
// The whole decision runs in one transaction, so two clients polling the same
// device code cannot both be issued a token: exactly one wins the guarded
// consume and the other is told the request is spent.
func (s *Service) PollDeviceGrant(ctx context.Context, deviceCode string) (*DeviceGrantResult, error) {
	if !ValidDeviceCode(deviceCode) {
		// Rejected on shape alone, without touching the database, so a
		// malformed code cannot be used to probe for real ones.
		return nil, ErrNotFound
	}

	now := s.Now()
	hash := DeviceCodeHash(deviceCode)

	var result *DeviceGrantResult
	err := s.store.Tx(ctx, func(ctx context.Context, tx *db.Store) error {
		request, err := tx.DeviceAuth.ByDeviceCodeHash(ctx, hash)
		if err != nil {
			return translate(err)
		}

		if !request.ExpiresAt.After(now) && request.Status != db.DeviceAuthConsumed {
			if _, err := tx.DeviceAuth.MarkExpired(ctx, request.ID, now); err != nil &&
				!errors.Is(err, db.ErrNotFound) {
				return fmt.Errorf("auth: expire device authorization: %w", err)
			}
			result = &DeviceGrantResult{State: DeviceGrantExpired}
			return nil
		}

		interval := time.Duration(request.PollIntervalSeconds) * time.Second
		if request.LastPolledAt != nil && now.Sub(*request.LastPolledAt) < interval-devicePollSlack {
			slowed, err := tx.DeviceAuth.SlowDown(ctx, request.ID,
				int(DevicePollBackoffStep.Seconds()), int(DevicePollIntervalMax.Seconds()), now)
			if err != nil {
				return fmt.Errorf("auth: slow down device polling: %w", err)
			}
			result = &DeviceGrantResult{
				State:    DeviceGrantSlowDown,
				Interval: time.Duration(slowed.PollIntervalSeconds) * time.Second,
			}
			return nil
		}
		if err := tx.DeviceAuth.RecordPoll(ctx, request.ID, now); err != nil {
			return fmt.Errorf("auth: record device poll: %w", err)
		}

		switch request.Status {
		case db.DeviceAuthPending:
			result = &DeviceGrantResult{State: DeviceGrantPending, Interval: interval}
			return nil
		case db.DeviceAuthDenied:
			result = &DeviceGrantResult{State: DeviceGrantDenied}
			return nil
		case db.DeviceAuthExpired:
			result = &DeviceGrantResult{State: DeviceGrantExpired}
			return nil
		case db.DeviceAuthConsumed:
			result = &DeviceGrantResult{State: DeviceGrantConsumed}
			return nil
		}

		if request.ApprovedUserID == nil {
			// Approved with nobody attached is not a state the store can
			// produce; treat it as never approved rather than issuing a token
			// with no owner.
			return fmt.Errorf("auth: device authorization %s is approved without an approver", request.ID)
		}
		userID := *request.ApprovedUserID

		active, err := tx.APITokens.CountActive(ctx, userID, now)
		if err != nil {
			return fmt.Errorf("auth: count active API tokens: %w", err)
		}
		if active >= db.MaxActiveAPITokens {
			if _, err := tx.DeviceAuth.DenyByID(ctx, request.ID, now); err != nil &&
				!errors.Is(err, db.ErrNotFound) {
				return fmt.Errorf("auth: retire device authorization: %w", err)
			}
			result = &DeviceGrantResult{State: DeviceGrantTokenLimit}
			return nil
		}

		consumed, err := tx.DeviceAuth.Consume(ctx, request.ID, now)
		if err != nil {
			return fmt.Errorf("auth: consume device authorization: %w", err)
		}
		if !consumed {
			result = &DeviceGrantResult{State: DeviceGrantConsumed}
			return nil
		}

		secret := NewAPIToken()
		expiresAt := request.TokenExpiresAt
		token, err := tx.APITokens.Create(ctx, db.CreateAPITokenParams{
			ID:        id.New(),
			UserID:    userID,
			Name:      request.ClientName,
			TokenHash: APITokenHash(secret),
			Prefix:    APITokenDisplayPrefix(secret),
			Scopes:    request.RequestedScopes,
			ExpiresAt: &expiresAt,
			Now:       now,
		})
		if err != nil {
			return fmt.Errorf("auth: mint device-granted API token: %w", err)
		}
		result = &DeviceGrantResult{State: DeviceGrantIssued, Secret: secret, Token: token}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// DeviceGrantByUserCode loads a pairing request for the approval screen,
// expiring it lazily when its time has passed.
//
// There is no ownership check because there is exactly one account: every
// pending request is by definition this user's. Growing a second account would
// make that false, and this is the line that would have to change.
func (s *Service) DeviceGrantByUserCode(ctx context.Context, userCode string) (*db.DeviceAuthorization, error) {
	code, ok := NormalizeUserCode(userCode)
	if !ok {
		return nil, ErrNotFound
	}

	request, err := s.store.DeviceAuth.ByUserCode(ctx, code)
	if err != nil {
		return nil, translate(err)
	}

	now := s.Now()
	stillOpen := request.Status == db.DeviceAuthPending || request.Status == db.DeviceAuthApproved
	if stillOpen && !request.ExpiresAt.After(now) {
		expired, err := s.store.DeviceAuth.MarkExpired(ctx, request.ID, now)
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				return request, nil
			}
			return nil, fmt.Errorf("auth: expire device authorization: %w", err)
		}
		return expired, nil
	}
	return request, nil
}

// ApproveDeviceGrant records the account owner's consent.
//
// The update is guarded on the request still being pending and unexpired, so an
// approval cannot resurrect a request that timed out or was denied in another
// tab. [ErrConflict] covers all of those alike.
func (s *Service) ApproveDeviceGrant(ctx context.Context, userCode, userID string) (*db.DeviceAuthorization, error) {
	return s.resolveDeviceGrant(ctx, userCode, func(ctx context.Context, code string, now time.Time) (*db.DeviceAuthorization, error) {
		return s.store.DeviceAuth.Approve(ctx, code, userID, now)
	})
}

// DenyDeviceGrant records a refusal.
func (s *Service) DenyDeviceGrant(ctx context.Context, userCode string) (*db.DeviceAuthorization, error) {
	return s.resolveDeviceGrant(ctx, userCode, func(ctx context.Context, code string, now time.Time) (*db.DeviceAuthorization, error) {
		return s.store.DeviceAuth.Deny(ctx, code, now)
	})
}

func (s *Service) resolveDeviceGrant(
	ctx context.Context,
	userCode string,
	apply func(context.Context, string, time.Time) (*db.DeviceAuthorization, error),
) (*db.DeviceAuthorization, error) {
	code, ok := NormalizeUserCode(userCode)
	if !ok {
		return nil, ErrNotFound
	}
	request, err := apply(ctx, code, s.Now())
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			// The guard matched nothing: unknown code, already resolved, or
			// expired. All three mean "there is nothing here to decide".
			return nil, ErrConflict
		}
		return nil, fmt.Errorf("auth: resolve device authorization: %w", err)
	}
	return request, nil
}

package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/abdeen-labs/hark/internal/db"
)

// Delivery ceilings, per rolling minute.
//
// These are not a billing meter; they are the blast radius of a script in a
// loop. One runaway agent should not be able to empty an account's push budget
// or bury its owner's phone, so there is a ceiling per credential and a larger
// one for everything together.
const (
	defaultRequesterRatePerMinute = 300
	defaultAccountRatePerMinute   = 1500

	// rateWindow is the width of the count. It is computed from the rows that
	// were written rather than from a counter, so restarts do not reset limits
	// and multiple processes share one budget.
	rateWindow = time.Minute
)

// checkQuota refuses a delivery request that would exceed either ceiling.
//
// It is a pre-check: the count is taken before the row is written, so the limit
// is what may be *started* in a window. Both counts run against the same
// instant, which is also the instant the row will carry.
func (s *server) checkQuota(w http.ResponseWriter, r *http.Request, req requester) bool {
	since := s.now().Add(-rateWindow)

	requesterCount, err := s.countRequesterWork(r.Context(), req, since)
	if err != nil {
		s.writeInternal(w, r, "counting recent deliveries failed", err)
		return false
	}
	if requesterCount >= s.opts.RequesterRatePerMinute {
		s.writeQuotaExceeded(w, r, "This credential has reached its delivery limit for the minute.")
		return false
	}

	accountCount, err := s.countAccountWork(r.Context(), req.UserID, since)
	if err != nil {
		s.writeInternal(w, r, "counting recent deliveries failed", err)
		return false
	}
	if accountCount >= s.opts.AccountRatePerMinute {
		s.writeQuotaExceeded(w, r, "This account has reached its delivery limit for the minute.")
		return false
	}
	return true
}

func (s *server) writeQuotaExceeded(w http.ResponseWriter, r *http.Request, message string) {
	writeRetryAfter(w, rateWindow)
	WriteError(w, r, http.StatusTooManyRequests, CodeRateLimited, message)
}

// countRequesterWork counts what one credential has asked for in the window.
//
// A token is charged for its notifications, its questions and its Live Activity
// operations; a service for its webhook deliveries and its Live Activity
// operations. Automatic work, such as ending an activity after an answer or
// replacement, is not counted against the requester's quota.
func (s *server) countRequesterWork(ctx context.Context, req requester, since time.Time) (int, error) {
	store := s.store()
	if req.TokenID != nil {
		operations, err := store.Operations.CountForTokenSince(ctx, *req.TokenID, since)
		if err != nil {
			return 0, err
		}
		interactions, err := store.Interactions.CountForTokenSince(ctx, *req.TokenID, since)
		if err != nil {
			return 0, err
		}
		notifications, err := store.Notifications.CountForTokenSince(ctx, *req.TokenID, since)
		if err != nil {
			return 0, err
		}
		return operations + interactions + notifications, nil
	}

	events, err := store.Events.CountForServiceSince(ctx, deref(req.ServiceID), since)
	if err != nil {
		return 0, err
	}
	operations, err := store.Operations.CountForServiceSince(ctx, deref(req.ServiceID), since)
	if err != nil {
		return 0, err
	}
	return events + operations, nil
}

// countAccountWork counts everything the account has asked for in the window,
// whichever credential asked.
func (s *server) countAccountWork(ctx context.Context, userID string, since time.Time) (int, error) {
	store := s.store()

	events, err := store.Events.CountForUserSince(ctx, userID, since)
	if err != nil {
		return 0, err
	}
	interactions, err := store.Interactions.CountForUserSince(ctx, userID, since)
	if err != nil {
		return 0, err
	}
	notifications, err := store.Notifications.CountForUserSince(ctx, userID, since)
	if err != nil {
		return 0, err
	}
	operations, err := store.Operations.CountForUserSince(ctx, userID, since)
	if err != nil {
		return 0, err
	}
	return events + interactions + notifications + operations, nil
}

// selectTargets resolves the devices one send should reach.
//
// An explicit device id outside the account returns a validation error. Valid
// ids are then filtered to devices that can receive the push; an empty result is
// allowed.
func (s *server) selectTargets(w http.ResponseWriter, r *http.Request, userID string, ids []string) ([]db.Device, bool) {
	if len(ids) == 0 {
		devices, err := s.store().Devices.ListTargets(r.Context(), userID, nil)
		if err != nil {
			s.writeInternal(w, r, "listing push targets failed", err)
			return nil, false
		}
		return devices, true
	}

	named, err := s.store().Devices.ListByIDs(r.Context(), userID, ids)
	if err != nil {
		s.writeInternal(w, r, "loading the named devices failed", err)
		return nil, false
	}
	if len(named) != len(ids) {
		WriteFieldErrors(w, r, "The request body is invalid.", []FieldError{{
			Field:   "device_ids",
			Message: "names a device that is not registered to this account",
		}})
		return nil, false
	}

	out := make([]db.Device, 0, len(named))
	for _, d := range named {
		if d.Pushable() {
			out = append(out, d)
		}
	}
	return out, true
}

package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
)

// The device grant is OAuth 2.0's device authorization grant (RFC 8628) in this
// API's own dress: snake_case JSON, this service's error envelope, and honest
// HTTP status codes instead of the RFC's blanket 400. The machine-readable
// `code` on each error carries the RFC's vocabulary, so a client written
// against RFC 8628 needs no new concepts — only a different place to read them
// from.

// deviceCodeRequest opens a pairing request.
type deviceCodeRequest struct {
	ClientName string   `json:"client_name"`
	Scopes     []string `json:"scopes"`
	// TokenExpiresInSeconds is the lifetime of the token this pairing would
	// issue, not of the pairing request. Absent or null takes the default.
	TokenExpiresInSeconds *int64 `json:"token_expires_in_seconds"`
}

type deviceCodeResponse struct {
	// DeviceCode is the client's half of the grant. It never leaves the client
	// and is never shown to the human.
	DeviceCode string `json:"device_code"`
	// UserCode is the half the human reads aloud or types into the browser.
	UserCode string `json:"user_code"`
	// VerificationURI is where the human goes; VerificationURIComplete carries
	// the code already filled in, for clients that can open a browser.
	VerificationURI         string    `json:"verification_uri"`
	VerificationURIComplete string    `json:"verification_uri_complete"`
	ExpiresAt               Timestamp `json:"expires_at"`
	ExpiresInSeconds        int       `json:"expires_in_seconds"`
	// IntervalSeconds is the pace to poll at. Honour it: polling faster only
	// makes the server raise it.
	IntervalSeconds int `json:"interval_seconds"`
}

// DeviceVerificationPath is the dashboard page a human lands on to approve a
// pairing request. It is part of the contract because the server hands clients
// a link to it, and it is exported for the same reason [DashboardPrefix] is:
// the URL space belongs to the router, and internal/dashboard serves this path
// rather than inventing its own.
const DeviceVerificationPath = "/cli/authorize"

// handleDeviceCode opens an unauthenticated, rate-limited pairing request.
func (s *server) handleDeviceCode(w http.ResponseWriter, r *http.Request) {
	var body deviceCodeRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	tokenExpiresIn, ok := parseSeconds(w, r, "token_expires_in_seconds", body.TokenExpiresInSeconds)
	if !ok {
		return
	}

	grant, err := s.opts.Auth.StartDeviceGrant(r.Context(), auth.StartDeviceGrantParams{
		ClientName:     body.ClientName,
		Scopes:         body.Scopes,
		TokenExpiresIn: tokenExpiresIn,
	})
	if err != nil {
		s.writeAuthError(w, r, "starting a device authorization failed", err)
		return
	}

	verification := s.publicPath(DeviceVerificationPath)
	complete := verification + "?code=" + url.QueryEscape(grant.Request.UserCode)

	WriteJSON(w, r, http.StatusCreated, deviceCodeResponse{
		DeviceCode:              grant.DeviceCode,
		UserCode:                grant.Request.UserCode,
		VerificationURI:         verification,
		VerificationURIComplete: complete,
		ExpiresAt:               Timestamp(grant.Request.ExpiresAt),
		ExpiresInSeconds:        int(auth.DeviceRequestTTL.Seconds()),
		IntervalSeconds:         grant.Request.PollIntervalSeconds,
	})
}

type deviceTokenRequest struct {
	DeviceCode string `json:"device_code"`
}

type deviceTokenResponse struct {
	// AccessToken is the minted API token's secret, returned exactly once.
	AccessToken string   `json:"access_token"`
	Token       tokenDTO `json:"token"`
}

// handleDeviceToken advances a pairing request one step.
//
// Every non-success outcome carries both an accurate status and the RFC's
// machine code, and the retryable ones carry Retry-After. A client only needs
// two rules: retry when Retry-After is present, stop otherwise.
func (s *server) handleDeviceToken(w http.ResponseWriter, r *http.Request) {
	var body deviceTokenRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	result, err := s.opts.Auth.PollDeviceGrant(r.Context(), body.DeviceCode)
	if err != nil {
		if errors.Is(err, auth.ErrNotFound) {
			WriteError(w, r, http.StatusNotFound, CodeNotFound,
				"No pending authorization matches that device code.")
			return
		}
		s.writeAuthError(w, r, "polling a device authorization failed", err)
		return
	}

	switch result.State {
	case auth.DeviceGrantIssued:
		WriteJSON(w, r, http.StatusOK, deviceTokenResponse{
			AccessToken: result.Secret,
			Token:       newTokenDTO(*result.Token),
		})
	case auth.DeviceGrantPending:
		writeRetryAfter(w, result.Interval)
		WriteError(w, r, http.StatusBadRequest, CodeAuthorizationPending,
			"This request is still awaiting approval. Keep polling.")
	case auth.DeviceGrantSlowDown:
		writeRetryAfter(w, result.Interval)
		WriteError(w, r, http.StatusTooManyRequests, CodeSlowDown,
			"Polling faster than the interval. The interval has been raised; use Retry-After.")
	case auth.DeviceGrantDenied:
		WriteError(w, r, http.StatusForbidden, CodeAccessDenied,
			"The request was denied. Start a new authorization.")
	case auth.DeviceGrantExpired:
		WriteError(w, r, http.StatusGone, CodeExpiredToken,
			"The request expired before it was approved. Start a new authorization.")
	case auth.DeviceGrantConsumed:
		WriteError(w, r, http.StatusConflict, CodeInvalidGrant,
			"This request already issued its token and cannot issue another.")
	case auth.DeviceGrantTokenLimit:
		WriteError(w, r, http.StatusConflict, CodeTokenLimitReached,
			"This account already holds the maximum number of active API tokens; the request was cancelled.")
	default:
		s.writeInternal(w, r, "unhandled device grant state", errors.New(string(result.State)))
	}
}

// deviceRequestDTO omits the client-side device code from the approval screen.
type deviceRequestDTO struct {
	UserCode       string    `json:"user_code"`
	ClientName     string    `json:"client_name"`
	Scopes         []string  `json:"scopes"`
	Status         string    `json:"status"`
	ExpiresAt      Timestamp `json:"expires_at"`
	TokenExpiresAt Timestamp `json:"token_expires_at"`
}

type deviceRequestResponse struct {
	Request deviceRequestDTO `json:"request"`
}

func newDeviceRequestDTO(d db.DeviceAuthorization) deviceRequestDTO {
	return deviceRequestDTO{
		UserCode:       d.UserCode,
		ClientName:     d.ClientName,
		Scopes:         d.RequestedScopes,
		Status:         d.Status,
		ExpiresAt:      Timestamp(d.ExpiresAt),
		TokenExpiresAt: Timestamp(d.TokenExpiresAt),
	}
}

// handleDeviceRequest describes a pairing request so the dashboard can show the
// human what they are about to authorize: which client asked, what it wants,
// and how long the token it receives would live.
func (s *server) handleDeviceRequest(w http.ResponseWriter, r *http.Request) {
	request, err := s.opts.Auth.DeviceGrantByUserCode(r.Context(), r.PathValue("user_code"))
	if err != nil {
		s.writeDeviceRequestError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	WriteJSON(w, r, http.StatusOK, deviceRequestResponse{Request: newDeviceRequestDTO(*request)})
}

// handleApproveDeviceRequest records owner approval. It is session-only because
// the next poll creates an API token.
func (s *server) handleApproveDeviceRequest(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFrom(r.Context())

	request, err := s.opts.Auth.ApproveDeviceGrant(r.Context(), r.PathValue("user_code"), principal.UserID())
	if err != nil {
		s.writeDeviceRequestError(w, r, err)
		return
	}
	WriteJSON(w, r, http.StatusOK, deviceRequestResponse{Request: newDeviceRequestDTO(*request)})
}

// handleDenyDeviceRequest refuses a pairing request.
func (s *server) handleDenyDeviceRequest(w http.ResponseWriter, r *http.Request) {
	request, err := s.opts.Auth.DenyDeviceGrant(r.Context(), r.PathValue("user_code"))
	if err != nil {
		s.writeDeviceRequestError(w, r, err)
		return
	}
	WriteJSON(w, r, http.StatusOK, deviceRequestResponse{Request: newDeviceRequestDTO(*request)})
}

func (s *server) writeDeviceRequestError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrNotFound):
		WriteError(w, r, http.StatusNotFound, CodeNotFound,
			"No authorization request matches that code.")
	case errors.Is(err, auth.ErrConflict):
		// One answer for unknown, already-decided and expired alike: all three
		// mean there is nothing here left to decide.
		WriteError(w, r, http.StatusConflict, CodeConflict,
			"That authorization request is no longer awaiting a decision.")
	default:
		s.writeInternal(w, r, "resolving a device authorization failed", err)
	}
}

// publicPath renders an absolute URL for a path on the public origin.
func (s *server) publicPath(path string) string {
	if s.opts.PublicURL == nil {
		return path
	}
	joined := *s.opts.PublicURL
	joined.Path = strings.TrimRight(joined.Path, "/") + path
	return joined.String()
}

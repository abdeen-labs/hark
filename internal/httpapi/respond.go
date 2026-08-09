package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// Error codes returned in the error envelope. They are stable, machine-readable
// strings; clients branch on these, never on the human-readable message.
const (
	CodeBadRequest       = "bad_request"
	CodeValidation       = "validation_failed"
	CodeUnauthorized     = "unauthorized"
	CodeNotFound         = "not_found"
	CodeMethodNotAllow   = "method_not_allowed"
	CodeConflict         = "conflict"
	CodePayloadTooLarge  = "payload_too_large"
	CodeUnsupportedMedia = "unsupported_media_type"
	CodeRateLimited      = "rate_limited"
	CodeInternal         = "internal_error"
	CodeUnavailable      = "service_unavailable"
)

// Authorization codes. These narrow the generic 401/403/409 so a client can
// tell "sign in again" from "this credential is the wrong kind" from "you asked
// for something this credential was never granted".
const (
	// CodeSessionRequired: the endpoint manages credentials, so an API token
	// may not call it however broad its scopes.
	CodeSessionRequired = "session_required"
	// CodeInsufficientScope: a valid token missing a scope the route declares.
	CodeInsufficientScope = "insufficient_scope"
	// CodeOriginNotAllowed: a cookie-authenticated state-changing request from
	// a foreign origin.
	CodeOriginNotAllowed = "origin_not_allowed"
	// CodeTokenLimitReached: the account already holds the maximum number of
	// active API tokens.
	CodeTokenLimitReached = "token_limit_reached"
	// CodeAPITokenRequired: the endpoint attributes what it creates to a
	// credential, so a session cannot call it.
	CodeAPITokenRequired = "api_token_required"
)

// Delivery codes. These narrow a 409 or a 502 on the surfaces that send pushes,
// so a caller can retry the ones worth retrying and stop on the ones that are
// not.
const (
	// CodeActivityConflict: a Live Activity already occupies the device or the
	// key this request asked for. Retry with `replace: true`, or a free key.
	CodeActivityConflict = "activity_conflict"
	// CodeSequenceConflict: the activity moved on since the sequence the caller
	// read. Re-read it and reapply.
	CodeSequenceConflict = "sequence_conflict"
	// CodeDigestMismatch: the answer refers to a different version of the
	// question than the one stored. The phone is showing something stale.
	CodeDigestMismatch = "action_digest_mismatch"
)

// Device-grant codes. The vocabulary is RFC 8628's, carried in this API's
// envelope rather than in the RFC's form-encoded body.
const (
	CodeAuthorizationPending = "authorization_pending"
	CodeSlowDown             = "slow_down"
	CodeAccessDenied         = "access_denied"
	CodeExpiredToken         = "expired_token"
	CodeInvalidGrant         = "invalid_grant"
)

// ErrorResponse is the single error envelope used by every endpoint.
//
//	{"error": {"code": "not_found", "message": "No such service."}}
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// ErrorBody carries the machine code, the human message, and optionally the
// per-field details of a validation failure.
type ErrorBody struct {
	Code    string       `json:"code"`
	Message string       `json:"message"`
	Fields  []FieldError `json:"fields,omitempty"`
}

// FieldError names one invalid request field.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// WriteJSON serialises v as the response body.
//
// The body is encoded before any header is written, so an encoding failure
// still produces a well-formed 500 instead of a truncated 200.
func WriteJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		LoggerFrom(r.Context()).ErrorContext(r.Context(), "encoding response failed", "error", err)
		writeRaw(w, http.StatusInternalServerError, mustMarshal(ErrorResponse{ErrorBody{
			Code:    CodeInternal,
			Message: "The server could not encode its response.",
		}}))
		return
	}
	writeRaw(w, status, body)
}

// WriteError writes the standard error envelope.
func WriteError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	WriteJSON(w, r, status, ErrorResponse{ErrorBody{Code: code, Message: message}})
}

// WriteFieldErrors writes a 422 listing every invalid field.
func WriteFieldErrors(w http.ResponseWriter, r *http.Request, message string, fields []FieldError) {
	WriteJSON(w, r, http.StatusUnprocessableEntity, ErrorResponse{ErrorBody{
		Code:    CodeValidation,
		Message: message,
		Fields:  fields,
	}})
}

func writeRaw(w http.ResponseWriter, status int, body []byte) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func mustMarshal(v any) []byte {
	body, err := json.Marshal(v)
	if err != nil {
		// Unreachable: every value passed here is a plain struct of strings.
		return []byte(`{"error":{"code":"internal_error","message":"Internal server error."}}`)
	}
	return body
}

// notFound answers any unrouted path.
func notFound(w http.ResponseWriter, r *http.Request) {
	WriteError(w, r, http.StatusNotFound, CodeNotFound, "No route matches "+r.Method+" "+r.URL.Path+".")
}

// methodNotAllowed answers a known path reached with an unsupported method.
func methodNotAllowed(allow string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", allow)
		WriteError(w, r, http.StatusMethodNotAllowed, CodeMethodNotAllow,
			r.Method+" is not supported here; allowed methods are "+allow+".")
	}
}

// discard is a logger used when a request carries none, so helpers never
// dereference a nil logger.
var discard = slog.New(slog.DiscardHandler)

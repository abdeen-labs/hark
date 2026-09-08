package mcp

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/abdeen-labs/hark/internal/auth"
)

// The API's error envelope and the codes used here. internal/httpapi imports
// this package, so its definitions cannot be imported back; these mirror them.
const (
	codeUnauthorized     = "unauthorized"
	codeMethodNotAllowed = "method_not_allowed"
	codeNotFound         = "not_found"
	codeValidation       = "validation_failed"
	codeUnavailable      = "service_unavailable"
)

// requestIDHeader is the API's correlation header (httpapi.RequestIDHeader).
const requestIDHeader = "X-Request-Id"

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func envelope(code, message string) []byte {
	body, err := json.Marshal(errorEnvelope{errorBody{Code: code, Message: message}})
	if err != nil {
		// Unreachable: the envelope is two strings.
		return []byte(`{"error":{"code":"internal_error","message":"Internal server error."}}`)
	}
	return body
}

func writeEnvelope(w http.ResponseWriter, status int, code, message string) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(envelope(code, message))
}

// Handler serves POST /mcp: the bearer gate, then the Streamable HTTP
// transport.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.serve)
}

// serve checks the token on every request. The transport is stateless, so
// there is no session for a revoked token to hide behind: the next request
// after revocation is a 401.
func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeEnvelope(w, http.StatusMethodNotAllowed, codeMethodNotAllowed,
			r.Method+" is not supported here; allowed methods are "+http.MethodPost+".")
		return
	}

	// Only an API token is admitted. A session token is refused before any
	// lookup: everything a tool creates is attributed to a token, so there is
	// no principal a session could act as here.
	secret, ok := bearerSecret(r.Header.Get("Authorization"))
	if !ok || !strings.HasPrefix(secret, auth.APITokenPrefix) {
		s.unauthorized(w, "Present an API token as `Authorization: Bearer hark_…`; "+
			"a session token cannot use this endpoint.")
		return
	}
	if _, err := s.opts.Resolver.AuthenticateAPIToken(r.Context(), secret); err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			s.unauthorized(w, "That token is not valid. It may have expired or been revoked.")
			return
		}
		s.opts.Logger.ErrorContext(r.Context(), "resolving credentials failed", "error", err)
		writeEnvelope(w, http.StatusServiceUnavailable, codeUnavailable,
			"Credentials could not be checked right now.")
		return
	}

	// The correlation id the surrounding middleware assigned is on the
	// response so far. Carrying it on the request lets a tool hand it to the
	// loopback call, so both access-log lines share one id.
	if rid := w.Header().Get(requestIDHeader); rid != "" {
		r.Header.Set(requestIDHeader, rid)
	}
	s.transport.ServeHTTP(w, r)
}

func (s *Server) unauthorized(w http.ResponseWriter, message string) {
	w.Header().Set("WWW-Authenticate", s.challenge)
	writeEnvelope(w, http.StatusUnauthorized, codeUnauthorized, message)
}

func bearerSecret(header string) (string, bool) {
	scheme, secret, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	secret = strings.TrimSpace(secret)
	return secret, secret != ""
}

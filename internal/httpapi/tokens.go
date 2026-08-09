package httpapi

import (
	"net/http"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
)

// tokenDTO is an API token as every response renders it. The secret is not in
// here and never will be: it exists only in the body of the request that
// created it.
type tokenDTO struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scopes     []string   `json:"scopes"`
	ExpiresAt  *Timestamp `json:"expires_at"`
	LastUsedAt *Timestamp `json:"last_used_at"`
	RevokedAt  *Timestamp `json:"revoked_at"`
	CreatedAt  Timestamp  `json:"created_at"`
}

func newTokenDTO(t db.APIToken) tokenDTO {
	return tokenDTO{
		ID:         t.ID,
		Name:       t.Name,
		Prefix:     t.Prefix,
		Scopes:     t.Scopes,
		ExpiresAt:  TimestampPtr(t.ExpiresAt),
		LastUsedAt: TimestampPtr(t.LastUsedAt),
		RevokedAt:  TimestampPtr(t.RevokedAt),
		CreatedAt:  Timestamp(t.CreatedAt),
	}
}

type tokenListResponse struct {
	Tokens []tokenDTO `json:"tokens"`
}

// handleListTokens returns every token on the account, newest first. Revoked
// and expired ones stay listed: a credential's history is worth keeping
// visible, and hiding a revoked token makes "did I already revoke that?"
// unanswerable.
func (s *server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFrom(r.Context())

	tokens, err := s.opts.Auth.ListAPITokens(r.Context(), principal.UserID())
	if err != nil {
		s.writeInternal(w, r, "listing API tokens failed", err)
		return
	}

	out := make([]tokenDTO, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, newTokenDTO(t))
	}
	WriteJSON(w, r, http.StatusOK, tokenListResponse{Tokens: out})
}

type createTokenRequest struct {
	Name   string   `json:"name"`
	Scopes []string `json:"scopes"`
	// ExpiresInSeconds is null or absent for a token that never expires.
	ExpiresInSeconds *int64 `json:"expires_in_seconds"`
}

type createTokenResponse struct {
	Token tokenDTO `json:"token"`
	// Secret is returned exactly once. It is not stored and cannot be shown
	// again; losing it means minting a new token.
	Secret string `json:"secret"`
}

// handleCreateToken mints an agent credential.
func (s *server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var body createTokenRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	expiresIn, ok := parseSeconds(w, r, "expires_in_seconds", body.ExpiresInSeconds)
	if !ok {
		return
	}

	principal := auth.PrincipalFrom(r.Context())
	token, secret, err := s.opts.Auth.CreateAPIToken(r.Context(), principal.UserID(), auth.CreateAPITokenParams{
		Name:      body.Name,
		Scopes:    body.Scopes,
		ExpiresIn: expiresIn,
	})
	if err != nil {
		s.writeAuthError(w, r, "creating an API token failed", err)
		return
	}

	WriteJSON(w, r, http.StatusCreated, createTokenResponse{
		Token:  newTokenDTO(*token),
		Secret: secret,
	})
}

// handleRevokeToken retires one of the account's tokens. Revocation takes
// effect on the next request carrying it.
func (s *server) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFrom(r.Context())

	err := s.opts.Auth.RevokeAPIToken(r.Context(), r.PathValue("id"), principal.UserID())
	if err != nil {
		s.writeAuthError(w, r, "revoking an API token failed", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

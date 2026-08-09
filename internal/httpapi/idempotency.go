package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// IdempotencyKeyHeader lets a caller retry a create without creating twice.
const IdempotencyKeyHeader = "Idempotency-Key"

// maxIdempotencyKeyLen bounds the stored key.
const maxIdempotencyKeyLen = 200

// idempotencyKey reads the header, writing the error response itself.
//
// A key is optional. Present but empty is a client bug rather than an absence —
// a caller that meant to send one and computed an empty string would otherwise
// get exactly the double-send it was trying to prevent.
func idempotencyKey(w http.ResponseWriter, r *http.Request) (*string, bool) {
	raw, present := r.Header[http.CanonicalHeaderKey(IdempotencyKeyHeader)]
	if !present {
		return nil, true
	}
	key := strings.TrimSpace(strings.Join(raw, ""))
	if key == "" || len(key) > maxIdempotencyKeyLen {
		WriteError(w, r, http.StatusBadRequest, CodeBadRequest,
			"Idempotency-Key must be 1-"+strconv.Itoa(maxIdempotencyKeyLen)+" characters.")
		return nil, false
	}
	return &key, true
}

// requestHash fingerprints a validated request body.
//
// It is computed after validation, so defaults are materialised and every
// string is already trimmed — two requests that mean the same thing hash the
// same way even when they were not spelled identically. The hash is only ever
// compared against one this server produced, so the encoding just has to be
// deterministic, which struct-field order makes it.
func requestHash(payload any) (string, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// replayDecision is what to do about a request that carried a key already seen.
type replayDecision int

const (
	// replayNone means the key is new: carry on and create.
	replayNone replayDecision = iota
	// replayStored means the same request arrived twice: answer with what the
	// first one produced, without sending anything again.
	replayStored
	// replayConflict means the key was reused for a different payload, which is
	// a client bug — the key is supposed to identify the request, and answering
	// with the first one's outcome would be a lie.
	replayConflict
)

// classifyReplay compares a stored request hash against this request's.
func classifyReplay(stored *string, current string) replayDecision {
	switch {
	case stored == nil || *stored != current:
		return replayConflict
	default:
		return replayStored
	}
}

// writeIdempotencyConflict answers a key reused with a different payload.
func (s *server) writeIdempotencyConflict(w http.ResponseWriter, r *http.Request) {
	s.writeConflict(w, r,
		"This Idempotency-Key was already used with a different request body. Use a new key.")
}

package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
)

// Answering a question is the account owner's act, and who the route listens to
// is the whole of its security: an agent that could answer the questions it asks
// turns every approval prompt into a formality.
//
// The refusals below are decided before any row is read, so they need no
// database. The two credentials that *are* admitted are exercised end to end in
// api_pg_test.go: the session in TestOnlyTheOwnerMayAnswerAQuestion, and the
// push credential in TestWebhookQuestionIsAnsweredWithThePushCredential.

const respondPath = "/v1/interactions/0198f3e4-0d22-7063-b1c8-6e9f0a1b2c3d/response"

// respondBody is a well-formed answer, so every rejection below is the
// authorization gate rather than validation.
const respondBody = `{"action":"approve","device_id":"0198f3a1-2b4c-7d8e-9f01-23456789abcd",` +
	`"action_digest":"6f1c"}`

func TestAnsweringWithNoCredentialIsUnauthorized(t *testing.T) {
	rec := do(t, newTestServer(t, stubPinger{}), http.MethodPost, respondPath, strings.NewReader(respondBody))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", rec.Code, rec.Body)
	}
	if got := decodeError(t, rec); got.Error.Code != CodeUnauthorized {
		t.Errorf("code = %q, want %q", got.Error.Code, CodeUnauthorized)
	}
	if rec.Header().Get("WWW-Authenticate") == "" {
		t.Error("a 401 from this route carries no WWW-Authenticate")
	}
}

// TestAnAPITokenMayNotAnswerAQuestion is the regression guard for an escalation:
// treating any authenticated principal as the owner would let the agent that
// asked "may I deploy?" approve itself.
func TestAnAPITokenMayNotAnswerAQuestion(t *testing.T) {
	req := newRequest(t, http.MethodPost, respondPath, respondBody)
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithPrincipal(req.Context(), &auth.Principal{
		Kind:     auth.KindAPIToken,
		User:     db.User{ID: "0198f3a1-2b4c-7d8e-9f01-000000000001"},
		APIToken: &db.APIToken{ID: "0198f3a1-2b4c-7d8e-9f01-000000000002", Scopes: db.Scopes},
	}))

	rec := send(t, newTestServer(t, stubPinger{}), req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body)
	}
	if got := decodeError(t, rec); got.Error.Code != CodeSessionRequired {
		t.Errorf("code = %q, want %q", got.Error.Code, CodeSessionRequired)
	}
	// The refusal happens before the lookup, so it cannot say whether the
	// question exists.
	if strings.Contains(rec.Body.String(), "0198f3e4") {
		t.Errorf("the refusal names the question: %s", rec.Body)
	}
}

// TestAMalformedResponseTokenIsIndistinguishableFromAnUnknownQuestion keeps the
// route from becoming an oracle: a value that is not a credential at all must
// not be told apart from one that simply does not open this question.
func TestAMalformedResponseTokenIsIndistinguishableFromAnUnknownQuestion(t *testing.T) {
	body := `{"action":"approve","device_id":"0198f3a1-2b4c-7d8e-9f01-23456789abcd",` +
		`"action_digest":"6f1c","response_token":"not-a-credential"}`

	rec := do(t, newTestServer(t, stubPinger{}), http.MethodPost, respondPath, strings.NewReader(body))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body)
	}
	if got := decodeError(t, rec); got.Error.Code != CodeNotFound {
		t.Errorf("code = %q, want %q", got.Error.Code, CodeNotFound)
	}
}

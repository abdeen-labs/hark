package dashboard

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
)

// pendingGrant is a pairing request waiting on the owner, as the store would
// hand one back.
func pendingGrant(now time.Time) *db.DeviceAuthorization {
	return &db.DeviceAuthorization{
		ID:              "0198f3a1-2b4c-7d8e-9f01-000000000009",
		UserCode:        "ABCD-EFGH",
		ClientName:      "harkctl on studio",
		RequestedScopes: []string{db.ScopeNotificationsNew, db.ScopeInteractionsNew},
		Status:          db.DeviceAuthPending,
		ExpiresAt:       now.Add(10 * time.Minute),
		TokenExpiresAt:  now.Add(90 * 24 * time.Hour),
	}
}

// TestApprovalPageSendsASignedOutBrowserToSignInKeepingTheCode is the reason
// the page can be linked from a terminal at all: the owner follows the link,
// signs in, and lands back on the same request rather than on an empty form
// holding a code they would have to go and find again.
func TestApprovalPageSendsASignedOutBrowserToSignInKeepingTheCode(t *testing.T) {
	d, _ := newTestDashboard(t)

	rec := send(d, request(http.MethodGet, pathAuthorize+"?code=abcd+efgh", ""))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusSeeOther)
	}

	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("Location is not a URL: %v", err)
	}
	if location.Path != pathLogin {
		t.Fatalf("Location = %q, want the sign-in page", location)
	}
	// The code comes back canonicalised, not as it was typed.
	if got := location.Query().Get("next"); got != pathAuthorize+"?code=ABCD-EFGH" {
		t.Errorf("next = %q, want the approval page with the normalized code", got)
	}
}

func TestApprovalPageShowsWhatTheClientIsAskingFor(t *testing.T) {
	d, service := newTestDashboard(t)
	service.grant = pendingGrant(service.now)

	rec := send(d, signedIn(http.MethodGet, pathAuthorize+"?code=ABCD-EFGH", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}

	body := rec.Body.String()
	for _, want := range []string{
		"harkctl on studio",      // who is asking
		db.ScopeNotificationsNew, // and for what
		"ABCD-EFGH",              // which request this is
		`value="approve"`,        // the two decisions
		`value="deny"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page does not show %q:\n%s", want, body)
		}
	}
	// A page that renders a form has to carry the token that form needs.
	if strings.Contains(body, `name="csrf_token" value=""`) {
		t.Error("the decision form carries an empty CSRF token")
	}
}

func TestApprovalPageOffersAFieldWhenTheLinkCarriesNoCode(t *testing.T) {
	d, _ := newTestDashboard(t)

	rec := send(d, signedIn(http.MethodGet, pathAuthorize, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `name="code"`) {
		t.Errorf("the page offers no way to enter a code:\n%s", rec.Body)
	}
}

func TestAnUnknownCodeIsANotFoundPage(t *testing.T) {
	d, _ := newTestDashboard(t)

	rec := send(d, signedIn(http.MethodGet, pathAuthorize+"?code=ZZZZ-ZZZZ", ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body)
	}
	// It still renders the page, with the field, rather than the bare error
	// screen: the visitor's next move is to try another code.
	if !strings.Contains(rec.Body.String(), `name="code"`) {
		t.Errorf("the 404 does not let the owner try again:\n%s", rec.Body)
	}
}

func TestApprovingAndDenyingGoThroughTheDeviceGrant(t *testing.T) {
	tests := map[string]struct {
		decision string
		outcome  string
		recorded func(*fakeAuth) []string
	}{
		"approve": {decisionApprove, "client_approved", func(f *fakeAuth) []string { return f.approved }},
		"deny":    {decisionDeny, "client_denied", func(f *fakeAuth) []string { return f.denied }},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			d, service := newTestDashboard(t)
			service.grant = pendingGrant(service.now)

			req := withCSRF(t, d, signedIn(http.MethodPost, pathAuthorize, ""),
				"code=ABCD-EFGH&decision="+tc.decision)
			rec := send(d, req)

			if rec.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusSeeOther, rec.Body)
			}
			recorded := tc.recorded(service)
			if len(recorded) != 1 || !strings.HasPrefix(recorded[0], "ABCD-EFGH") {
				t.Fatalf("the device grant recorded %v, want one decision on ABCD-EFGH", recorded)
			}

			location, err := url.Parse(rec.Header().Get("Location"))
			if err != nil {
				t.Fatalf("Location is not a URL: %v", err)
			}
			if location.Path != pathAuthorize {
				t.Errorf("Location = %q, want the approval page", location)
			}
			if got := location.Query().Get("done"); got != tc.outcome {
				t.Errorf("done = %q, want %q", got, tc.outcome)
			}
			if _, known := notices[tc.outcome]; !known {
				t.Errorf("%q has no banner in the notice vocabulary", tc.outcome)
			}
		})
	}
}

// TestApprovingUserIsTheSignedInOwner pins what the approval is attributed to:
// the token the client collects is minted for whoever said yes.
func TestApprovingUserIsTheSignedInOwner(t *testing.T) {
	d, service := newTestDashboard(t)
	service.grant = pendingGrant(service.now)

	send(d, withCSRF(t, d, signedIn(http.MethodPost, pathAuthorize, ""),
		"code=ABCD-EFGH&decision=approve"))

	if len(service.approved) != 1 || !strings.HasSuffix(service.approved[0], "by user-1") {
		t.Fatalf("approved = %v, want it attributed to the signed-in owner", service.approved)
	}
}

// TestADecisionOnASettledRequestIsAConflict covers the second tab: unknown,
// already-answered and expired are one answer, because all three mean there is
// nothing here left to decide.
func TestADecisionOnASettledRequestIsAConflict(t *testing.T) {
	d, service := newTestDashboard(t)
	service.grant = pendingGrant(service.now)
	service.decideErr = auth.ErrConflict

	rec := send(d, withCSRF(t, d, signedIn(http.MethodPost, pathAuthorize, ""),
		"code=ABCD-EFGH&decision=approve"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "no longer awaiting a decision") {
		t.Errorf("the page does not say why:\n%s", rec.Body)
	}
}

// TestATypedCodeIsLookedUpThroughThePost keeps what a visitor types out of a
// GET they could be tricked into repeating, and canonicalises it on the way.
func TestATypedCodeIsLookedUpThroughThePost(t *testing.T) {
	d, service := newTestDashboard(t)
	service.grant = pendingGrant(service.now)

	rec := send(d, withCSRF(t, d, signedIn(http.MethodPost, pathAuthorize, ""), "code=abcd+efgh"))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusSeeOther, rec.Body)
	}
	if want := pathAuthorize + "?code=ABCD-EFGH"; rec.Header().Get("Location") != want {
		t.Errorf("Location = %q, want %q", rec.Header().Get("Location"), want)
	}
	if len(service.approved) != 0 || len(service.denied) != 0 {
		t.Error("a lookup decided something")
	}
}

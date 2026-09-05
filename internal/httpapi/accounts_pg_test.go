package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
)

func TestAccountProvisioningAccessAndSignIn(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	const body = `{"username":"Alice","password":"alice initial password","display_name":"Alice Example"}`
	var created accountResponse
	rec := f.expect(http.MethodPost, "/accounts", f.session, body, http.StatusCreated, &created)
	if created.User.Role != db.RoleUser || created.User.Username != "alice" || created.User.Email != "alice@hark.local" {
		t.Fatalf("account = %+v", created.User)
	}
	if strings.Contains(rec.Body.String(), "password") || strings.Contains(rec.Body.String(), "argon2") {
		t.Fatal("response exposed a password or hash")
	}
	if rec.Header().Get("Cache-Control") != "no-store" || len(rec.Result().Cookies()) != 0 {
		t.Fatal("provisioning must disable caching and preserve the admin session")
	}
	var login loginResponse
	f.expect(http.MethodPost, "/auth/login", "", `{"username":"ALICE","password":"alice initial password"}`, http.StatusOK, &login)
	if login.User.Role != db.RoleUser || login.User.ID != created.User.ID {
		t.Fatal("wrong login identity")
	}

	for _, route := range []struct{ method, body string }{
		{http.MethodGet, ""}, {http.MethodPost, `{"username":"mallory","password":"another initial password"}`},
	} {
		for _, caller := range []struct {
			name, token string
			status      int
			code        string
		}{
			{"anonymous", "", http.StatusUnauthorized, CodeUnauthorized},
			{"regular user", login.Token, http.StatusForbidden, CodeAdminRequired},
			{"admin API token", f.token, http.StatusForbidden, CodeSessionRequired},
		} {
			t.Run(route.method+"/"+caller.name, func(t *testing.T) {
				rec := f.expect(route.method, "/accounts", caller.token, route.body, caller.status, nil)
				if got := decodeError(t, rec).Error.Code; got != caller.code {
					t.Errorf("code = %q, want %q", got, caller.code)
				}
			})
		}
	}

	// Unknown role fields cannot promote a new user, even when sent by the admin.
	f.expect(http.MethodPost, "/accounts", f.session,
		`{"username":"anotheradmin","password":"another initial password","role":"admin"}`, http.StatusBadRequest, nil)
	f.expect(http.MethodPost, "/accounts", f.session, body, http.StatusUnprocessableEntity, nil)
	f.expect(http.MethodPost, "/accounts", f.session,
		`{"username":"bob","password":"another initial password","email":"ALICE@HARK.LOCAL"}`, http.StatusUnprocessableEntity, nil)
	f.expect(http.MethodPost, "/accounts", f.session,
		`{"username":"bob","password":"short"}`, http.StatusUnprocessableEntity, nil)
	var accounts accountsResponse
	f.expect(http.MethodGet, "/accounts", f.session, "", http.StatusOK, &accounts)
	if len(accounts.Users) != 2 || accounts.Users[0].Role != db.RoleAdmin {
		t.Fatalf("accounts = %+v", accounts.Users)
	}

	// A newly provisioned account has its own resource namespace.
	var service serviceResponse
	f.expect(http.MethodPost, "/services", f.session, `{"title":"Admin private service"}`, http.StatusCreated, &service)
	f.expect(http.MethodGet, "/services/"+service.Service.ID, login.Token, "", http.StatusNotFound, nil)
	f.expect(http.MethodPost, "/services", login.Token, `{"title":"Alice service"}`, http.StatusCreated, &service)
	f.expect(http.MethodGet, "/services/"+service.Service.ID, f.session, "", http.StatusNotFound, nil)

	// The API's existing cookie-origin gate also covers account provisioning.
	req := httptest.NewRequest(http.MethodPost, "/accounts", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://foreign.example")
	req.AddCookie(&http.Cookie{Name: NewSessionCookie(testPublicURL()).Name(), Value: f.session})
	rec = send(t, f.handler, req)
	if rec.Code != http.StatusForbidden || decodeError(t, rec).Error.Code != CodeOriginNotAllowed {
		t.Fatalf("foreign origin: %d %s", rec.Code, rec.Body)
	}
}

func TestRequireAdminUsesRoleAndCredentialKind(t *testing.T) {
	called := false
	h := RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	for name, p := range map[string]*auth.Principal{
		"regular user named admin": {Kind: auth.KindSession, User: db.User{Username: "admin", Role: db.RoleUser}},
		"missing role":             {Kind: auth.KindSession},
		"admin token":              {Kind: auth.KindAPIToken, User: db.User{Role: db.RoleAdmin}},
	} {
		t.Run(name, func(t *testing.T) {
			if rec := serveWithPrincipal(h, p); rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d", rec.Code)
			}
			if called {
				t.Fatal("restricted handler was called")
			}
		})
	}
	serveWithPrincipal(h, &auth.Principal{Kind: auth.KindSession, User: db.User{Username: "custom-owner", Role: db.RoleAdmin}})
	if !called {
		t.Fatal("admin session was refused")
	}
}

package dashboard

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
)

func adminRequest(method, path string) *http.Request {
	req := signedIn(method, path, "")
	auth.PrincipalFrom(req.Context()).User.Role = db.RoleAdmin
	return req
}

func TestAccountsPageRequiresAdminAndCSRF(t *testing.T) {
	d, _ := newTestDashboard(t)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		if rec := send(d, request(method, pathAccounts, "")); rec.Code != http.StatusSeeOther {
			t.Errorf("anonymous %s = %d", method, rec.Code)
		}
		req := signedIn(method, pathAccounts, "")
		if method == http.MethodPost {
			req = withCSRF(t, d, req, "username=alice&password=initial-password")
		}
		if rec := send(d, req); rec.Code != http.StatusForbidden {
			t.Errorf("regular user %s = %d", method, rec.Code)
		}
	}
	if rec := send(d, adminRequest(http.MethodPost, pathAccounts)); rec.Code != http.StatusForbidden {
		t.Errorf("missing CSRF = %d", rec.Code)
	}
	if rec := send(d, adminRequest(http.MethodGet, pathAccounts)); rec.Code != http.StatusOK {
		t.Errorf("admin page = %d: %s", rec.Code, rec.Body)
	}
	for _, isAdmin := range []bool{false, true} {
		req := signedIn(http.MethodGet, pathTokens, "")
		if isAdmin {
			auth.PrincipalFrom(req.Context()).User.Role = db.RoleAdmin
		}
		body := send(d, req).Body.String()
		if strings.Contains(body, `href="`+pathAccounts+`"`) != isAdmin {
			t.Errorf("accounts navigation visibility for admin=%v is wrong", isAdmin)
		}
	}
}

func TestProvisionAccountThroughDashboard(t *testing.T) {
	d, store, userID := newPGDashboard(t)
	ctx := t.Context()
	if _, err := dashPool.Exec(ctx, "UPDATE users SET role = 'admin' WHERE id = $1", userID); err != nil {
		t.Fatal(err)
	}
	service := auth.New(store, nil)
	d.opts.Auth = service
	owner, err := store.Users.ByID(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	admin := &auth.Principal{Kind: auth.KindSession, User: *owner, Session: &db.Session{ID: "owner-session"}}
	post := func(form string) *http.Request {
		req := request(http.MethodPost, pathAccounts, "")
		req = req.WithContext(auth.WithPrincipal(req.Context(), admin))
		return withCSRF(t, d, req, form)
	}
	form := url.Values{"username": {"Alice"}, "display_name": {"Alice Example"}, "password": {"initial alice password"}, "role": {"admin"}}.Encode()
	rec := send(d, post(form))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != pathAccounts+"?done=account_created" {
		t.Fatalf("provision: %d %s", rec.Code, rec.Body)
	}
	user, err := store.Users.ByUsername(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if user.Role != db.RoleUser {
		t.Fatalf("role = %q, want user", user.Role)
	}
	if _, _, err := service.Login(ctx, "alice", "initial alice password"); err != nil {
		t.Fatal(err)
	}
	rec = send(d, post(form))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Username is already in use") {
		t.Fatalf("duplicate: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "initial alice password") || strings.Contains(rec.Body.String(), "argon2id") {
		t.Fatal("password or hash was echoed")
	}
	if !strings.Contains(rec.Body.String(), `value="Alice"`) {
		t.Fatal("username was not retained after validation")
	}
	get := request(http.MethodGet, pathAccounts, "")
	get = get.WithContext(auth.WithPrincipal(get.Context(), admin))
	rec = send(d, get)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Alice Example") {
		t.Fatalf("directory: %d %s", rec.Code, rec.Body)
	}
	if count, err := store.Users.Count(ctx); err != nil || count != 2 {
		t.Fatalf("account count = %d, err = %v", count, err)
	}
}

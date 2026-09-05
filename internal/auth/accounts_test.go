package auth

import (
	"errors"
	"testing"

	"github.com/abdeen-labs/hark/internal/db"
)

func TestAccountManagementRequiresAdminSession(t *testing.T) {
	service := New(db.New(nil), nil)
	for name, actor := range map[string]*Principal{
		"anonymous":                nil,
		"regular user named admin": {Kind: KindSession, User: db.User{Username: "admin", Role: db.RoleUser}},
		"missing role":             {Kind: KindSession, User: db.User{Username: "admin"}},
		"admin API token":          {Kind: KindAPIToken, User: db.User{Role: db.RoleAdmin}, APIToken: &db.APIToken{Scopes: db.Scopes}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := service.ProvisionAccount(t.Context(), actor, CreateAccountParams{}); !errors.Is(err, ErrAdminRequired) {
				t.Errorf("ProvisionAccount = %v, want ErrAdminRequired", err)
			}
			if _, err := service.ListAccounts(t.Context(), actor); !errors.Is(err, ErrAdminRequired) {
				t.Errorf("ListAccounts = %v, want ErrAdminRequired", err)
			}
		})
	}
}

func TestProvisionedAccountCanSignInButCannotProvision(t *testing.T) {
	ctx, service, _ := requireService(t)
	seedAccount(t, ctx, service)
	_, adminToken, err := service.Login(ctx, "admin", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := service.AuthenticateSession(ctx, adminToken)
	if err != nil {
		t.Fatal(err)
	}
	user, err := service.ProvisionAccount(ctx, admin, CreateAccountParams{
		Username: "  Alice  ", Password: testPassword,
		Email: " ALICE@EXAMPLE.COM ", DisplayName: " Alice Example ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if user.Role != db.RoleUser || user.Username != "alice" || user.Email != "alice@example.com" || user.DisplayName != "Alice Example" {
		t.Fatalf("unexpected provisioned account: %+v", user)
	}
	if user.PasswordHash == nil || VerifyPassword(*user.PasswordHash, testPassword) != nil {
		t.Fatal("password was not hashed correctly")
	}
	_, token, err := service.Login(ctx, "ALICE", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	actor, err := service.AuthenticateSession(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if actor.IsAdmin() || actor.UserID() != user.ID {
		t.Fatal("wrong session identity")
	}
	if _, err := service.ProvisionAccount(ctx, actor, CreateAccountParams{Username: "mallory", Password: testPassword}); !errors.Is(err, ErrAdminRequired) {
		t.Fatalf("user provisioning = %v, want ErrAdminRequired", err)
	}
	if _, err := service.ListAccounts(ctx, actor); !errors.Is(err, ErrAdminRequired) {
		t.Fatalf("user directory access = %v, want ErrAdminRequired", err)
	}
	users, err := service.ListAccounts(ctx, admin)
	if err != nil || len(users) != 2 {
		t.Fatalf("accounts = %v, err = %v", users, err)
	}

	for name, params := range map[string]CreateAccountParams{
		"username": {Username: "ALICE", Password: testPassword, Email: "other@example.com"},
		"email":    {Username: "bob", Password: testPassword, Email: "ALICE@EXAMPLE.COM"},
		"password": {Username: "charlie", Password: "short"},
	} {
		_, err := service.ProvisionAccount(ctx, admin, params)
		var invalid *InvalidInputError
		if !errors.As(err, &invalid) || invalid.Field != name {
			t.Errorf("%s: error = %v, want field validation", name, err)
		}
	}
	if users, err := service.ListAccounts(ctx, admin); err != nil || len(users) != 2 {
		t.Fatalf("count after rejected requests = %d, err = %v", len(users), err)
	}
}

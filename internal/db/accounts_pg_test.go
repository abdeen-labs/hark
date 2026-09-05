package db

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abdeen-labs/hark/internal/id"
)

func TestConcurrentBootstrapCreatesOneAdmin(t *testing.T) {
	ctx, store := requireStore(t)
	var wins atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range 4 {
		wg.Go(func() {
			<-start
			username := fmt.Sprintf("owner%d", i)
			user, err := store.Users.CreateFirst(ctx, CreateUserParams{
				ID: id.New(), Username: username, Email: username + "@hark.local", Now: time.Now(),
			})
			switch {
			case err == nil:
				wins.Add(1)
				if user.Role != RoleAdmin {
					t.Errorf("role = %q, want admin", user.Role)
				}
			case !errors.Is(err, ErrNotFound):
				t.Errorf("bootstrap: %v", err)
			}
		})
	}
	close(start)
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("bootstrap winners = %d, want 1", wins.Load())
	}
	if users, err := store.Users.List(ctx); err != nil || len(users) != 1 {
		t.Fatalf("account count = %d, err = %v", len(users), err)
	}
}

func TestAccountRoleMigrationPreservesExistingOwner(t *testing.T) {
	ctx, _ := requireStore(t)
	tx, err := schemaPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if _, err := tx.Exec(ctx, "CREATE SCHEMA hark_role_upgrade_test; SET LOCAL search_path TO hark_role_upgrade_test"); err != nil {
		t.Fatal(err)
	}
	migrations, err := LoadMigrations(Migrations())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, migrations[0].SQL); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO users (id, username, email, display_name, password_hash, created_at, updated_at)
		VALUES ('existing-owner', 'custom-owner', 'owner@example.com', 'Owner', 'existing-hash', now(), now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, migrations[1].SQL); err != nil {
		t.Fatal(err)
	}
	store := newStore(tx)
	owner, err := store.Users.ByID(ctx, "existing-owner")
	if err != nil {
		t.Fatal(err)
	}
	if owner.Role != RoleAdmin || owner.Username != "custom-owner" || owner.PasswordHash == nil || *owner.PasswordHash != "existing-hash" {
		t.Fatalf("migration changed the owner incorrectly: %+v", owner)
	}
	user := mustUser(ctx, t, store, "new-user")
	if user.Role != RoleUser {
		t.Fatalf("new role = %q, want user", user.Role)
	}
	if _, err := tx.Exec(ctx, "UPDATE users SET role = 'admin' WHERE id = $1", user.ID); !IsUniqueViolation(err, "users_one_admin_key") {
		t.Fatalf("second admin = %v, want unique violation", err)
	}
}

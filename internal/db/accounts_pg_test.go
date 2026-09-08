package db

import (
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

func TestFreshSchemaAccountRoles(t *testing.T) {
	ctx, store := requireStore(t)
	owner, err := store.Users.CreateFirst(ctx, CreateUserParams{
		ID: id.New(), Username: "owner", Email: "owner@hark.local", Now: time.Now(),
	})
	if err != nil || owner.Role != RoleAdmin {
		t.Fatalf("bootstrap owner=%+v, err=%v", owner, err)
	}
	user := mustUser(ctx, t, store, "regular-user")
	if user.Role != RoleUser {
		t.Fatalf("new role=%q, want user", user.Role)
	}
	if _, err := schemaPool.Exec(ctx, "UPDATE users SET role = 'admin' WHERE id = $1", user.ID); !IsUniqueViolation(err, "users_one_admin_key") {
		t.Fatalf("second admin=%v, want unique violation", err)
	}
}

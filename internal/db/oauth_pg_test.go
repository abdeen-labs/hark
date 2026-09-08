package db

import (
	"errors"
	"testing"
	"time"

	"github.com/abdeen-labs/hark/internal/id"
)

func TestOAuthClientsLifecycleAndPurge(t *testing.T) {
	ctx, s := requireStore(t)
	now := time.Now()

	client, err := s.OAuthClients.Create(ctx, CreateOAuthClientParams{
		ID:           id.New(),
		Name:         "Claude",
		RedirectURIs: []string{"https://claude.ai/api/mcp/auth_callback"},
		ClientURI:    ptr("https://claude.ai"),
		Now:          now,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if client.LastUsedAt != nil {
		t.Errorf("LastUsedAt = %v on a fresh registration, want nil", client.LastUsedAt)
	}
	if _, err := s.OAuthClients.Create(ctx, CreateOAuthClientParams{
		ID: id.New(), Name: "empty", RedirectURIs: []string{}, Now: now,
	}); !IsCheckViolation(err) {
		t.Errorf("creating a client with no redirect URIs error = %v, want a CHECK violation", err)
	}

	loaded, err := s.OAuthClients.ByID(ctx, client.ID)
	if err != nil || loaded.Name != "Claude" || len(loaded.RedirectURIs) != 1 {
		t.Fatalf("ByID = (%+v, %v)", loaded, err)
	}
	if _, err := s.OAuthClients.ByID(ctx, id.New()); !errors.Is(err, ErrNotFound) {
		t.Errorf("ByID(unknown) error = %v, want ErrNotFound", err)
	}

	// A registration that never completes an authorization leaves after a day;
	// one that did is kept, until a year of idleness.
	unused, err := s.OAuthClients.Create(ctx, CreateOAuthClientParams{
		ID: id.New(), Name: "abandoned", RedirectURIs: []string{"https://example.com/cb"}, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.OAuthClients.TouchLastUsed(ctx, client.ID, now.Add(time.Hour)); err != nil {
		t.Fatalf("TouchLastUsed: %v", err)
	}
	if err := s.OAuthClients.TouchLastUsed(ctx, id.New(), now); !errors.Is(err, ErrNotFound) {
		t.Errorf("TouchLastUsed(unknown) error = %v, want ErrNotFound", err)
	}

	if n, err := s.OAuthClients.Purge(ctx, now.Add(12*time.Hour), 24*time.Hour, 365*24*time.Hour); err != nil || n != 0 {
		t.Fatalf("Purge inside the grace period = (%d, %v), want (0, nil)", n, err)
	}
	if n, err := s.OAuthClients.Purge(ctx, now.Add(25*time.Hour), 24*time.Hour, 365*24*time.Hour); err != nil || n != 1 {
		t.Fatalf("Purge after a day = (%d, %v), want the unused registration gone", n, err)
	}
	if _, err := s.OAuthClients.ByID(ctx, unused.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the unused registration survived the purge: %v", err)
	}
	if _, err := s.OAuthClients.ByID(ctx, client.ID); err != nil {
		t.Errorf("the used registration was purged: %v", err)
	}
	if n, err := s.OAuthClients.Purge(ctx, now.Add(366*24*time.Hour), 24*time.Hour, 365*24*time.Hour); err != nil || n != 1 {
		t.Fatalf("Purge after a year idle = (%d, %v), want the used registration gone", n, err)
	}
}

func TestOAuthCodesConsumeIsGuarded(t *testing.T) {
	ctx, s := requireStore(t)
	user := mustUser(ctx, t, s, "ali")
	now := time.Now()

	create := func(hash string, expiresAt time.Time) *OAuthCode {
		t.Helper()
		code, err := s.OAuthCodes.Create(ctx, CreateOAuthCodeParams{
			ID: id.New(), CodeHash: hash, ClientID: "https://claude.ai/.well-known/oauth-client.json",
			ClientName: "Claude", UserID: user.ID, RedirectURI: "https://claude.ai/api/mcp/auth_callback",
			Scopes: []string{ScopeNotificationsNew, ScopeDevicesRead}, CodeChallenge: "challenge",
			Resource:  ptr("https://hark.example.com/mcp"),
			ExpiresAt: expiresAt, Now: now,
		})
		if err != nil {
			t.Fatalf("create code: %v", err)
		}
		return code
	}

	live := create("hash-live", now.Add(10*time.Minute))
	if live.ConsumedAt != nil {
		t.Errorf("ConsumedAt = %v on a fresh code, want nil", live.ConsumedAt)
	}

	// The scope constraint is the database's, not only the validator's.
	if _, err := s.OAuthCodes.Create(ctx, CreateOAuthCodeParams{
		ID: id.New(), CodeHash: "hash-bad", ClientID: "c", ClientName: "c", UserID: user.ID,
		RedirectURI: "https://example.com/cb", Scopes: []string{"devices:destroy"}, CodeChallenge: "x",
		ExpiresAt: now.Add(time.Minute), Now: now,
	}); !IsCheckViolation(err) {
		t.Errorf("creating a code with an invented scope error = %v, want a CHECK violation", err)
	}
	// A code's digest is unique: the same plaintext cannot be issued twice.
	if _, err := s.OAuthCodes.Create(ctx, CreateOAuthCodeParams{
		ID: id.New(), CodeHash: "hash-live", ClientID: "c", ClientName: "c", UserID: user.ID,
		RedirectURI: "https://example.com/cb", Scopes: []string{ScopeDevicesRead}, CodeChallenge: "x",
		ExpiresAt: now.Add(time.Minute), Now: now,
	}); !IsUniqueViolation(err, "oauth_authorization_codes_code_hash_key") {
		t.Errorf("duplicate code hash error = %v, want a unique violation", err)
	}

	loaded, err := s.OAuthCodes.ByCodeHash(ctx, "hash-live")
	if err != nil || loaded.ID != live.ID || loaded.UserID != user.ID {
		t.Fatalf("ByCodeHash = (%+v, %v)", loaded, err)
	}
	if loaded.Resource == nil || *loaded.Resource != "https://hark.example.com/mcp" {
		t.Errorf("Resource = %v, want it round-tripped", loaded.Resource)
	}
	if _, err := s.OAuthCodes.ByCodeHash(ctx, "hash-unknown"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ByCodeHash(unknown) error = %v, want ErrNotFound", err)
	}

	// Exactly one presentation wins.
	consumed, err := s.OAuthCodes.Consume(ctx, live.ID, now.Add(time.Minute))
	if err != nil || !consumed {
		t.Fatalf("Consume = (%v, %v), want (true, nil)", consumed, err)
	}
	consumed, err = s.OAuthCodes.Consume(ctx, live.ID, now.Add(2*time.Minute))
	if err != nil || consumed {
		t.Fatalf("second Consume = (%v, %v), want (false, nil)", consumed, err)
	}
	spent, err := s.OAuthCodes.ByCodeHash(ctx, "hash-live")
	if err != nil || spent.ConsumedAt == nil {
		t.Fatalf("after consume ByCodeHash = (%+v, %v), want ConsumedAt set", spent, err)
	}

	// An expired code cannot be consumed, and the purge removes it.
	expired := create("hash-expired", now.Add(-time.Second))
	if consumed, err := s.OAuthCodes.Consume(ctx, expired.ID, now); err != nil || consumed {
		t.Fatalf("Consume(expired) = (%v, %v), want (false, nil)", consumed, err)
	}
	if n, err := s.OAuthCodes.Purge(ctx, now); err != nil || n != 1 {
		t.Fatalf("Purge = (%d, %v), want (1, nil)", n, err)
	}
	if _, err := s.OAuthCodes.ByCodeHash(ctx, "hash-live"); err != nil {
		t.Errorf("the unexpired code was purged: %v", err)
	}
	if n, err := s.OAuthCodes.Purge(ctx, now.Add(time.Hour)); err != nil || n != 1 {
		t.Fatalf("Purge after expiry = (%d, %v), want (1, nil)", n, err)
	}

	// Deleting the account takes its codes with it.
	remaining := create("hash-cascade", now.Add(10*time.Minute))
	if _, err := s.q.Exec(ctx, "DELETE FROM users WHERE id = $1", user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OAuthCodes.ByCodeHash(ctx, remaining.CodeHash); !errors.Is(err, ErrNotFound) {
		t.Errorf("a code survived its account's deletion: %v", err)
	}
}

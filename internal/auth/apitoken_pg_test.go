package auth

import (
	"errors"
	"strings"
	"testing"

	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/id"
)

func TestSetAPITokenImage(t *testing.T) {
	ctx, service, _ := requireService(t)
	user := seedAccount(t, ctx, service)
	token, _, err := service.CreateAPIToken(ctx, user.ID, CreateAPITokenParams{
		Name: "deploy-bot", Scopes: []string{db.ScopeNotificationsNew},
	})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	padded := "  https://example.com/bot.png  "
	set, err := service.SetAPITokenImage(ctx, token.ID, user.ID, &padded)
	if err != nil || set.ImageURL == nil || *set.ImageURL != "https://example.com/bot.png" {
		t.Fatalf("SetAPITokenImage = (%+v, %v), want the trimmed URL stored", set, err)
	}

	for _, bad := range []string{
		"",
		"   ",
		"http://example.com/bot.png",
		"https://localhost/bot.png",
		"https://10.0.0.1/bot.png",
		"javascript:alert(1)",
		"https://" + strings.Repeat("a", MaxAPITokenImageURLLength) + ".example/bot.png",
	} {
		_, err := service.SetAPITokenImage(ctx, token.ID, user.ID, &bad)
		var invalid *InvalidInputError
		if !errors.As(err, &invalid) || invalid.Field != "image_url" {
			t.Errorf("SetAPITokenImage(%q) = %v, want an image_url InvalidInputError", bad, err)
		}
	}
	kept, err := service.ListAPITokens(ctx, user.ID)
	if err != nil || len(kept) != 1 || kept[0].ImageURL == nil || *kept[0].ImageURL != "https://example.com/bot.png" {
		t.Errorf("a refused picture changed the token: %+v, %v", kept, err)
	}

	cleared, err := service.SetAPITokenImage(ctx, token.ID, user.ID, nil)
	if err != nil || cleared.ImageURL != nil {
		t.Fatalf("SetAPITokenImage(nil) = (%+v, %v), want the picture removed", cleared, err)
	}
	if _, err := service.SetAPITokenImage(ctx, id.New(), user.ID, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetAPITokenImage(unknown) = %v, want ErrNotFound", err)
	}
}

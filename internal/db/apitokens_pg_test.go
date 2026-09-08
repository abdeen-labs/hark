package db

import (
	"errors"
	"testing"

	"github.com/abdeen-labs/hark/internal/id"
)

func TestAPITokenPictureIsSetAndCleared(t *testing.T) {
	ctx, s := requireStore(t)
	owner := mustUser(ctx, t, s, "owner")
	other := mustUser(ctx, t, s, "other")
	token := mustToken(ctx, t, s, owner.ID)
	if token.ImageURL != nil {
		t.Fatalf("a hand-made token = %+v, want no picture", token)
	}

	set, err := s.APITokens.SetImage(ctx, token.ID, owner.ID, ptr("https://example.com/bot.png"))
	if err != nil || set.ImageURL == nil || *set.ImageURL != "https://example.com/bot.png" {
		t.Fatalf("SetImage = (%+v, %v)", set, err)
	}
	if _, err := s.APITokens.SetImage(ctx, token.ID, other.ID, ptr("https://example.com/x.png")); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetImage by another account error = %v, want ErrNotFound", err)
	}
	cleared, err := s.APITokens.SetImage(ctx, token.ID, owner.ID, nil)
	if err != nil || cleared.ImageURL != nil {
		t.Fatalf("SetImage(nil) = (%+v, %v), want the picture cleared", cleared, err)
	}
	if _, err := s.APITokens.SetImage(ctx, id.New(), owner.ID, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetImage(unknown) error = %v, want ErrNotFound", err)
	}
}

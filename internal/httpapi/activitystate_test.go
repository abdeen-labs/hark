package httpapi

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/abdeen-labs/hark/internal/db"
)

// TestActivityBanner verifies the banner redaction applied by privacy mode.
//
// The redaction is of the banner alone. The state document delivered alongside
// it still carries the real title and status, because what a locked screen
// shows is the widget's decision — the server's job is only to stop iOS
// announcing the words out loud when it puts the card up.
func TestActivityBanner(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)

	standard, err := encodeActivityState(activityState{
		ActivityID:  "act",
		Title:       "Deploy",
		Status:      "Building",
		UpdatedAt:   Timestamp(now),
		Symbol:      symbolTerminal,
		PrivacyMode: privacyStandard,
		Style:       styleStandard,
	})
	if err != nil {
		t.Fatalf("encodeActivityState: %v", err)
	}

	banner := activityBanner(standard)
	if banner.Title != "Deploy" || banner.Body != "Building" {
		t.Errorf("banner = %+v, want the activity's own words", banner)
	}

	private, err := encodeActivityState(activityState{
		ActivityID:  "act",
		Title:       "Rotate the production keys",
		Status:      "Step 2 of 4",
		UpdatedAt:   Timestamp(now),
		Symbol:      symbolTerminal,
		PrivacyMode: "private",
		Style:       styleStandard,
	})
	if err != nil {
		t.Fatalf("encodeActivityState: %v", err)
	}

	banner = activityBanner(private)
	if banner.Title == "Rotate the production keys" || banner.Body == "Step 2 of 4" {
		t.Errorf("banner = %+v, want the activity's words withheld", banner)
	}
	if banner.Title == "" || banner.Body == "" {
		t.Errorf("banner = %+v, want something for iOS to announce", banner)
	}

	// A state this build cannot read is treated as private. Announcing a title
	// parsed out of a document we do not understand is the one mistake that
	// cannot be taken back.
	unreadable := activityBanner(json.RawMessage(`not json`))
	if unreadable != banner {
		t.Errorf("banner = %+v, want the withheld one", unreadable)
	}
}

// TestInteractionBannerIsAnnounced guards the case a private default would
// break: a question on the Lock Screen has to say what it is asking.
func TestInteractionBannerIsAnnounced(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	state := interactionState("act", db.Interaction{
		ID:     "int",
		Kind:   db.InteractionApproval,
		Title:  "Release",
		Prompt: "Deploy production?",
	}, styleApproval, now)

	encoded, err := encodeActivityState(state)
	if err != nil {
		t.Fatalf("encodeActivityState: %v", err)
	}
	if banner := activityBanner(encoded); banner.Title != "Release" {
		t.Errorf("banner = %+v, want the question's title", banner)
	}
}

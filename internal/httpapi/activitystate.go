package httpapi

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/abdeen-labs/hark/internal/db"
)

// The Live Activity content state, and the vocabulary it is built from.
//
// This document is what a phone renders. It is stored as-is, delivered as-is,
// and re-validated whenever it is merged, so the shape below is simultaneously
// the API's request vocabulary, the stored representation, and the push
// payload. Keeping them one thing is what makes a partial update expressible:
// the server merges into the last state it sent rather than into a translation
// of it.
const (
	// activityStateVersion is the content-state schema this server emits. A
	// device announces the version it understands at registration, and one that
	// speaks a different version is simply not sent Live Activities.
	activityStateVersion = db.LiveActivitySchemaVersion

	// defaultAccentColor is Hark's own red.
	defaultAccentColor = "#E64949"
)

// Symbols name the glyph the widget draws.
var (
	activitySymbols = []string{"terminal", "code", "build", "success", "warning"}

	// activityPrivacyModes decide whether the *banner* announcing a start
	// repeats the activity's title, or says nothing about it. The content state
	// always carries the real values: what the widget shows on a locked screen
	// is the widget's decision, not the server's.
	activityPrivacyModes = []string{"standard", "private"}

	// activityStyles are the layouts a requester may ask for.
	activityStyles = []string{"standard", "ring", "hero", "terminal", "steps"}

	// interactiveStyles present a question with two buttons. They are not
	// available to an ordinary activity: the buttons only mean something when an
	// interaction is behind them, and a style with no question would render a
	// pair of controls that do nothing.
	interactiveStyles = []string{"approval", "shell", "verdict", "signal"}
)

const (
	symbolTerminal = "terminal"
	symbolSuccess  = "success"
	symbolWarning  = "warning"

	privacyStandard = "standard"
	styleStandard   = "standard"
	styleApproval   = "approval"
)

// activityState is the content-state document.
type activityState struct {
	SchemaVersion int    `json:"schema_version"`
	ActivityID    string `json:"activity_id"`
	Title         string `json:"title"`
	Status        string `json:"status"`
	// Detail and Progress are omitted rather than nulled when absent: the widget
	// lays itself out differently with and without them, and "present but empty"
	// is a third state nobody wants to render.
	Detail    *string   `json:"detail,omitempty"`
	Progress  *float64  `json:"progress,omitempty"`
	UpdatedAt Timestamp `json:"updated_at"`

	Symbol      string `json:"symbol"`
	PrivacyMode string `json:"privacy_mode"`
	AccentColor string `json:"accent_color"`
	Style       string `json:"style"`

	// Interaction is present exactly when the activity presents a question.
	Interaction *activityInteractionState `json:"interaction,omitempty"`
}

// activityInteractionState is the question a Lock Screen button answers.
type activityInteractionState struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Prompt string `json:"prompt"`
	// The labels and the actions travel together so the widget never has to map
	// one to the other: a button shows PrimaryLabel and posts PrimaryAction.
	PrimaryLabel    string `json:"primary_label"`
	SecondaryLabel  string `json:"secondary_label"`
	PrimaryAction   string `json:"primary_action"`
	SecondaryAction string `json:"secondary_action"`
	// State is the interaction's status, so a widget that is still on screen
	// after the answer can show what the answer was.
	State string `json:"state"`
}

// encodeActivityState serialises a state document for storage and delivery.
func encodeActivityState(state activityState) (json.RawMessage, error) {
	state.SchemaVersion = activityStateVersion
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("httpapi: encode Live Activity state: %w", err)
	}
	return encoded, nil
}

// decodeActivityState reads a stored state document back.
func decodeActivityState(raw json.RawMessage) (activityState, error) {
	var state activityState
	if err := json.Unmarshal(raw, &state); err != nil {
		return activityState{}, fmt.Errorf("httpapi: decode Live Activity state: %w", err)
	}
	return state, nil
}

// interactionState builds the content state for an activity that presents a
// question.
//
// The labels default from the kind rather than from the style, because what a
// button says has to match what answering it does: an approval answers
// approve/deny, and a yes/no question answers yes/no.
func interactionState(activityID string, in db.Interaction, style string, now time.Time) activityState {
	kind, primary, secondary := db.InteractionApproval, "approve", "deny"
	primaryLabel, secondaryLabel := "Approve", "Deny"
	if in.Kind != db.InteractionApproval {
		kind, primary, secondary = db.InteractionYesNo, "yes", "no"
		primaryLabel, secondaryLabel = "Yes", "No"
	}
	if in.PrimaryLabel != nil {
		primaryLabel = *in.PrimaryLabel
	}
	if in.SecondaryLabel != nil {
		secondaryLabel = *in.SecondaryLabel
	}

	prompt := in.Prompt
	return activityState{
		SchemaVersion: activityStateVersion,
		ActivityID:    activityID,
		Title:         in.Title,
		Status:        "Waiting for you",
		Detail:        &prompt,
		UpdatedAt:     Timestamp(now),
		Symbol:        symbolWarning,
		PrivacyMode:   privacyStandard,
		AccentColor:   defaultAccentColor,
		Style:         style,
		Interaction: &activityInteractionState{
			ID:              in.ID,
			Kind:            kind,
			Prompt:          in.Prompt,
			PrimaryLabel:    primaryLabel,
			SecondaryLabel:  secondaryLabel,
			PrimaryAction:   primary,
			SecondaryAction: secondary,
			State:           db.InteractionPending,
		},
	}
}

// resolvedInteractionState rewrites an activity's state to show how its question
// ended.
//
// The card stays on screen for a moment after the answer, so it has to say what
// happened rather than freezing on the question. Anything that is not an answer
// — canceled, or a status this build does not know — reads as canceled, which is
// the honest summary of "no answer was sent".
func resolvedInteractionState(state activityState, in db.Interaction, now time.Time) activityState {
	var status, detail, symbol string
	switch in.Status {
	case db.InteractionApproved:
		status, detail, symbol = "Approved", "The agent has your answer.", symbolSuccess
	case db.InteractionYes:
		status, detail, symbol = "Yes", "The agent has your answer.", symbolSuccess
	case db.InteractionDenied:
		status, detail, symbol = "Denied", "The agent has your answer.", symbolWarning
	case db.InteractionNo:
		status, detail, symbol = "No", "The agent has your answer.", symbolWarning
	case db.InteractionReplied:
		status, detail, symbol = "Replied", "The agent has your answer.", symbolSuccess
	case db.InteractionExpired:
		status, detail, symbol = "Expired", "No answer made it back in time.", symbolWarning
	default:
		status, detail, symbol = "Canceled", "Nothing went back to the agent.", symbolWarning
	}

	state.Status, state.Detail, state.Symbol = status, &detail, symbol
	state.UpdatedAt = Timestamp(now)
	if state.Interaction != nil {
		state.Interaction.State = in.Status
	}
	return state
}

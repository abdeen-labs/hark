package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/push"
)

// alertContent is one notification, before it is addressed to any device.
type alertContent struct {
	Title    string
	Body     string
	ImageURL *string
	URL      *string
	Priority string
	// SourceID and SourceName say who is sending: a service, or an API token.
	SourceID   string
	SourceName string
	// RecordID is the row this alert came from, so the phone can tie a
	// notification to the history entry it belongs to.
	RecordID string
	// ThreadKey groups related alerts on the phone.
	ThreadKey string
	// Interaction turns the alert into a question with answer actions.
	Interaction *push.AlertInteraction
}

// fanOut delivers one alert to every device and settles what the outcome
// implies about those devices.
//
// A device whose token APNs has permanently rejected is deactivated here rather
// than by the caller: every send path would otherwise have to remember to do it,
// and the one that forgot would keep pushing into the void forever.
func (s *server) fanOut(r *http.Request, content alertContent, devices []db.Device) push.AlertResult {
	if len(devices) == 0 {
		return push.AlertResult{}
	}

	alerts := make([]push.Alert, 0, len(devices))
	for _, d := range devices {
		alerts = append(alerts, push.Alert{
			Target:      push.Target{DeviceID: d.ID, Token: d.APNsToken},
			Title:       content.Title,
			Body:        content.Body,
			Priority:    content.Priority,
			ImageURL:    content.ImageURL,
			URL:         content.URL,
			ThreadKey:   content.ThreadKey,
			SourceID:    content.SourceID,
			SourceName:  content.SourceName,
			RecordID:    content.RecordID,
			Interaction: content.Interaction,
		})
	}

	result := s.opts.Push.SendAlerts(r.Context(), alerts)
	if len(result.StaleTokens) > 0 {
		if _, err := s.store().Devices.Deactivate(detach(r.Context()), result.StaleTokens); err != nil {
			LoggerFrom(r.Context()).ErrorContext(r.Context(), "deactivating stale devices failed", "error", err)
		}
	}
	return result
}

// deliveryStatus maps a fan-out onto the status its record carries.
//
// The distinction that matters is between "nothing to send to" and "nothing got
// through": the first is an account that has not finished setting itself up, and
// the second is a delivery problem.
func deliveryStatus(attempted, accepted int) string {
	switch {
	case attempted == 0:
		return db.EventNoDevices
	case accepted == 0:
		return db.EventFailed
	case accepted < attempted:
		return db.EventPartial
	default:
		return db.EventAccepted
	}
}

// failureSummary joins the provider's failure descriptions for the owner-facing
// delivery log, or nil when everything landed. The store truncates it.
func failureSummary(failures []string) *string {
	if len(failures) == 0 {
		return nil
	}
	joined := strings.Join(failures, "; ")
	return &joined
}

// threadKey groups a sender's alerts into one conversation on the phone.
//
// Grouping by sender *and* title rather than by sender alone is what keeps a
// chatty agent from collapsing every unrelated message into one thread: two
// different titles read as two different conversations, which is how a person
// thinks about them.
func threadKey(sourceID, title string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(title))))
	return sourceID + "-" + hex.EncodeToString(sum[:])[:10]
}

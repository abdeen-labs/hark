package dashboard

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/id"
	"github.com/abdeen-labs/hark/internal/push"
)

// safetyTestInterval limits setup tests for each source.
const safetyTestInterval = 10 * time.Minute

// safetyPage contains the safety settings and configured sources.
type safetyPage struct {
	view
	CriticalAlertsEnabled bool
	Sources               []db.SafetySource
	Kinds                 []string
	Form                  safetyForm
}

// safetyForm preserves a rejected create form.
type safetyForm struct {
	Name string
}

func safetyFormFrom(r *http.Request) safetyForm {
	return safetyForm{
		Name: strings.TrimSpace(r.PostFormValue("name")),
	}
}

func (f safetyForm) validate() *notice {
	var problems []string
	if n := utf8.RuneCountInString(f.Name); n < 1 || n > 80 {
		problems = append(problems, "The name must be 1-80 characters.")
	}
	if problems == nil {
		return nil
	}
	return &notice{Kind: noticeError, Message: strings.Join(problems, " ")}
}

func (d *Dashboard) showSafety(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	d.renderSafety(w, r, p, http.StatusOK, safetyForm{}, nil)
}

func (d *Dashboard) renderSafety(
	w http.ResponseWriter, r *http.Request, p *auth.Principal,
	status int, form safetyForm, n *notice,
) {
	user, err := d.opts.Store.Users.ByID(r.Context(), p.UserID())
	if err != nil {
		d.fail(w, r, "loading the safety settings failed", err)
		return
	}
	sources, err := d.opts.Store.SafetySources.ListForUser(r.Context(), p.UserID())
	if err != nil {
		d.fail(w, r, "listing safety sources failed", err)
		return
	}

	page := safetyPage{
		view:                  d.newView(r, p, "Critical Alerts", "safety"),
		CriticalAlertsEnabled: user.CriticalAlertsEnabled,
		Sources:               sources,
		Kinds:                 db.CriticalSafetyKinds,
		Form:                  form,
	}
	if n != nil {
		page.Notice = n
	}
	d.render(w, r, status, tmplSafety, page)
}

func (d *Dashboard) createSafetySource(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	form := safetyFormFrom(r)
	if n := form.validate(); n != nil {
		d.renderSafety(w, r, p, http.StatusUnprocessableEntity, form, n)
		return
	}

	_, err := d.opts.Store.SafetySources.Create(r.Context(), db.CreateSafetySourceParams{
		ID: id.New(), UserID: p.UserID(), Name: form.Name, Now: d.opts.Auth.Now(),
	})
	if err != nil {
		d.fail(w, r, "creating a safety source failed", err)
		return
	}
	d.redirect(w, r, pathSafety, "safety_created")
}

// updateSafetySource saves the editable fields shown in a source row.
func (d *Dashboard) updateSafetySource(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	src, err := d.opts.Store.SafetySources.ByID(r.Context(), r.PathValue("id"), p.UserID())
	if errors.Is(err, db.ErrNotFound) {
		d.renderError(w, r, http.StatusNotFound, "No alert source matches that identifier.")
		return
	}
	if err != nil {
		d.fail(w, r, "loading the alert source failed", err)
		return
	}

	name := strings.TrimSpace(r.PostFormValue("name"))
	if n := utf8.RuneCountInString(name); n < 1 || n > 80 {
		d.renderSafety(w, r, p, http.StatusUnprocessableEntity, safetyForm{},
			&notice{Kind: noticeError, Message: "The name must be 1-80 characters."})
		return
	}
	kind := r.PostFormValue("kind")
	if kind == "" {
		kind = src.Kind
	}
	if !db.ValidSafetyKind(kind) {
		d.renderSafety(w, r, p, http.StatusUnprocessableEntity, safetyForm{},
			&notice{Kind: noticeError, Message: "Choose a valid alert type."})
		return
	}
	critical := r.PostFormValue("critical_enabled") != ""
	if critical && !db.SafetyKindAllowsCritical(kind) {
		d.renderSafety(w, r, p, http.StatusUnprocessableEntity, safetyForm{},
			&notice{Kind: noticeError, Message: "Choose a safety alert type before enabling Critical Alerts."})
		return
	}

	_, err = d.opts.Store.SafetySources.Update(r.Context(), db.UpdateSafetySourceParams{
		ID:              r.PathValue("id"),
		UserID:          p.UserID(),
		Kind:            db.Value(kind),
		Name:            db.Value(name),
		CriticalEnabled: db.Value(critical),
		Now:             d.opts.Auth.Now(),
	})
	switch {
	case errors.Is(err, db.ErrNotFound):
		d.renderError(w, r, http.StatusNotFound, "No alert source matches that identifier.")
	case err != nil:
		d.fail(w, r, "updating a safety source failed", err)
	default:
		d.redirect(w, r, pathSafety, "safety_updated")
	}
}

func (d *Dashboard) deleteSafetySource(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	deleted, err := d.opts.Store.SafetySources.Delete(r.Context(), r.PathValue("id"), p.UserID())
	switch {
	case err != nil:
		d.fail(w, r, "deleting a safety source failed", err)
	case !deleted:
		d.renderError(w, r, http.StatusNotFound, "No alert source matches that identifier.")
	default:
		d.redirect(w, r, pathSafety, "safety_deleted")
	}
}

func (d *Dashboard) saveSafetySettings(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	enabled := r.PostFormValue("critical_alerts_enabled") != ""
	if err := d.opts.Store.Users.SetCriticalAlertsEnabled(r.Context(),
		p.UserID(), enabled, d.opts.Auth.Now()); err != nil {
		d.fail(w, r, "saving the safety settings failed", err)
		return
	}
	d.redirect(w, r, pathSafety, "safety_settings_saved")
}

// sendSafetyTest sends and records a setup test for one source.
func (d *Dashboard) sendSafetyTest(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	user, err := d.opts.Store.Users.ByID(r.Context(), p.UserID())
	if err != nil {
		d.fail(w, r, "loading the safety settings failed", err)
		return
	}

	var (
		src       *db.SafetySource
		event     *db.SafetyEvent
		throttled bool
	)
	err = d.opts.Store.Tx(r.Context(), func(ctx context.Context, store *db.Store) error {
		src, err = store.SafetySources.ByIDForUpdate(ctx, r.PathValue("id"), p.UserID())
		if err != nil {
			return err
		}

		now := d.opts.Auth.Now()
		recent, err := store.SafetyEvents.CountPushedForSourceStateSince(ctx,
			src.ID, db.SafetyStateTest, now.Add(-safetyTestInterval))
		if err != nil {
			return err
		}
		if recent > 0 {
			throttled = true
			return nil
		}

		title, body := db.SafetyAlertContent(src.Kind, src.Name, db.SafetyStateTest)
		event, err = store.SafetyEvents.Create(ctx, db.CreateSafetyEventParams{
			ID:       id.New(),
			SourceID: src.ID,
			State:    db.SafetyStateTest,
			Title:    title,
			Body:     body,
			Priority: db.SafetyAlertPriority(db.SafetyStateTest, src.Kind, user.CriticalAlertsEnabled, src.CriticalEnabled),
			Status:   db.EventProcessing,
			Now:      now,
		})
		return err
	})
	switch {
	case errors.Is(err, db.ErrNotFound):
		d.renderError(w, r, http.StatusNotFound, "No alert source matches that identifier.")
		return
	case err != nil:
		d.fail(w, r, "recording a safety test failed", err)
		return
	}
	if throttled {
		d.redirect(w, r, pathSafety, "safety_test_limited")
		return
	}

	devices, err := d.opts.Store.Devices.ListTargets(r.Context(), p.UserID(), nil)
	if err != nil {
		d.fail(w, r, "listing push targets failed", err)
		return
	}
	if len(devices) == 0 {
		d.settleSafetyEvent(r, event, db.EventNoDevices, 0, nil)
		d.renderSafety(w, r, p, http.StatusOK, safetyForm{}, &notice{
			Kind:    noticeWarn,
			Message: "No active device is registered.",
		})
		return
	}

	alerts := make([]push.Alert, 0, len(devices))
	for _, device := range devices {
		alerts = append(alerts, push.Alert{
			Target:     push.Target{DeviceID: device.ID, Token: device.APNsToken},
			Title:      event.Title,
			Body:       event.Body,
			Priority:   event.Priority,
			ThreadKey:  "safety-" + src.ID,
			SourceID:   src.ID,
			SourceName: src.Name,
			RecordID:   event.ID,
		})
	}

	sent := d.opts.Push.SendAlerts(r.Context(), alerts)
	if len(sent.StaleTokens) > 0 {
		ctx := context.WithoutCancel(r.Context())
		if _, err := d.opts.Store.Devices.Deactivate(ctx, sent.StaleTokens); err != nil {
			d.log(r).ErrorContext(r.Context(), "deactivating stale devices failed", "error", err)
		}
	}

	status := db.EventAccepted
	switch {
	case sent.Accepted == 0:
		status = db.EventFailed
	case sent.Accepted < len(alerts):
		status = db.EventPartial
	}
	var failure *string
	if len(sent.Failures) > 0 {
		joined := strings.Join(sent.Failures, "; ")
		failure = &joined
	}
	settled := d.settleSafetyEvent(r, event, status, sent.Accepted, failure)
	d.renderSafety(w, r, p, http.StatusOK, safetyForm{}, safetyTestNotice(settled))
}

// settleSafetyEvent records a send independently of request cancellation.
func (d *Dashboard) settleSafetyEvent(r *http.Request, e *db.SafetyEvent, status string, delivered int, failure *string) *db.SafetyEvent {
	settled, err := d.opts.Store.SafetyEvents.Settle(context.WithoutCancel(r.Context()), e.ID, status, delivered, failure)
	if err != nil {
		d.log(r).ErrorContext(r.Context(), "settling a safety event failed",
			"safety_event_id", e.ID, "error", err)
		return e
	}
	return settled
}

// safetyTestNotice summarizes a setup test result.
func safetyTestNotice(event *db.SafetyEvent) *notice {
	switch {
	case event.DeliveredCount == 0:
		return &notice{Kind: noticeError, Message: "APNs accepted no notifications."}
	case event.Priority == db.PriorityCritical:
		return &notice{Kind: noticeOK, Message: "Test sent as a Critical Alert."}
	default:
		return &notice{Kind: noticeWarn, Message: "Test sent as a Time Sensitive notification because Critical Alerts are off."}
	}
}

package dashboard

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/push"
)

// How much of each list the overview shows. It is a glance, not an archive:
// everything here is paged in full on the API, and a dashboard that tries to be
// the archive is a dashboard nobody reads.
const (
	overviewActivities = 10
	overviewHistory    = 25
)

// errorPage is the standalone error view.
type errorPage struct {
	view
	Status  int
	Message string
}

// overviewPage is the landing page: what is on the Lock Screen now, and what
// has reached the account recently.
type overviewPage struct {
	view
	Stats      overviewStats
	Activities []db.ActivityListItem
	History    []db.FeedItem
}

// overviewStats are the counts along the top.
type overviewStats struct {
	Devices, ActiveDevices int
	Tokens, ActiveTokens   int
	LiveActivities         int
}

func (d *Dashboard) showOverview(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	ctx, userID, now := r.Context(), p.UserID(), d.opts.Auth.Now()

	devices, err := d.opts.Store.Devices.ListForUser(ctx, userID)
	if err != nil {
		d.fail(w, r, "listing devices failed", err)
		return
	}
	tokens, err := d.opts.Auth.ListAPITokens(ctx, userID)
	if err != nil {
		d.fail(w, r, "listing API tokens failed", err)
		return
	}
	activities, err := d.opts.Store.Activities.ListLiveForUser(ctx, userID, now, overviewActivities)
	if err != nil {
		d.fail(w, r, "listing live activities failed", err)
		return
	}
	history, err := d.opts.Store.Feed.List(ctx, userID, db.FeedFilterAll, db.Cursor{}, overviewHistory)
	if err != nil {
		d.fail(w, r, "listing history failed", err)
		return
	}

	page := overviewPage{
		view:       d.newView(r, p, "Overview", "overview"),
		Activities: activities,
		History:    history.Items,
		Stats: overviewStats{
			Devices:        len(devices),
			Tokens:         len(tokens),
			LiveActivities: len(activities),
		},
	}
	for _, device := range devices {
		if device.Pushable() {
			page.Stats.ActiveDevices++
		}
	}
	for _, token := range tokens {
		if token.Active(now) {
			page.Stats.ActiveTokens++
		}
	}
	d.render(w, r, http.StatusOK, tmplOverview, page)
}

// devicesPage lists the phones registered to the account.
type devicesPage struct {
	view
	Devices []db.Device
}

func (d *Dashboard) showDevices(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	devices, err := d.opts.Store.Devices.ListForUser(r.Context(), p.UserID())
	if err != nil {
		d.fail(w, r, "listing devices failed", err)
		return
	}
	d.render(w, r, http.StatusOK, tmplDevices, devicesPage{
		view:    d.newView(r, p, "Devices", "devices"),
		Devices: devices,
	})
}

// deleteDevice unregisters a phone.
//
// The row is deleted rather than deactivated, which takes its Live Activity
// deliveries with it and sends no end push — the same trade POST
// /v1/devices/{id} makes, and for the same reason: "this is not my phone any
// more" has to stop the pushes.
func (d *Dashboard) deleteDevice(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	deleted, err := d.opts.Store.Devices.Delete(r.Context(), r.PathValue("id"), p.UserID())
	switch {
	case err != nil:
		d.fail(w, r, "deleting a device failed", err)
	case !deleted:
		d.renderError(w, r, http.StatusNotFound, "No device matches that identifier.")
	default:
		d.redirect(w, r, pathDevices, "device_deleted")
	}
}

// tokensPage lists agent credentials and mints new ones.
type tokensPage struct {
	view
	Tokens []db.APIToken
	Scopes []string
	// Secret is the plaintext of a token minted by this very request. It is
	// rendered once and never stored, which is why this page answers a POST
	// directly instead of redirecting: a redirect would either lose it or put
	// it in a URL.
	Secret string
	Form   tokenForm
	Now    time.Time
}

// tokenForm is the create form's state, kept so a rejected submission comes
// back filled in.
type tokenForm struct {
	Name      string
	Scopes    []string
	ExpiresIn string
}

// lifetimes are the expiry choices the form offers, in seconds. "never" is the
// absent value, which [auth.CreateAPITokenParams] spells as a zero duration.
var lifetimes = map[string]time.Duration{
	"never": 0,
	"30d":   30 * 24 * time.Hour,
	"90d":   90 * 24 * time.Hour,
	"365d":  365 * 24 * time.Hour,
}

func (d *Dashboard) showTokens(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	d.renderTokens(w, r, p, http.StatusOK, tokenForm{ExpiresIn: "90d"}, "", nil)
}

// renderTokens draws the page in each of its three states: freshly loaded, just
// after a mint, and after a rejected submission.
func (d *Dashboard) renderTokens(
	w http.ResponseWriter, r *http.Request, p *auth.Principal,
	status int, form tokenForm, secret string, n *notice,
) {
	tokens, err := d.opts.Auth.ListAPITokens(r.Context(), p.UserID())
	if err != nil {
		d.fail(w, r, "listing API tokens failed", err)
		return
	}

	page := tokensPage{
		view:   d.newView(r, p, "API tokens", "tokens"),
		Tokens: tokens,
		Scopes: db.Scopes,
		Secret: secret,
		Form:   form,
		Now:    d.opts.Auth.Now(),
	}
	if n != nil {
		page.Notice = n
	}
	d.render(w, r, status, tmplTokens, page)
}

func (d *Dashboard) createToken(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	form := tokenForm{
		Name:      strings.TrimSpace(r.PostFormValue("name")),
		Scopes:    r.PostForm["scopes"],
		ExpiresIn: r.PostFormValue("expires_in"),
	}
	lifetime, known := lifetimes[form.ExpiresIn]
	if !known {
		form.ExpiresIn = "never"
	}

	token, secret, err := d.opts.Auth.CreateAPIToken(r.Context(), p.UserID(), auth.CreateAPITokenParams{
		Name:      form.Name,
		Scopes:    form.Scopes,
		ExpiresIn: lifetime,
	})
	if err != nil {
		d.renderTokens(w, r, p, tokenErrorStatus(err), form, "", tokenNotice(err))
		return
	}

	d.renderTokens(w, r, p, http.StatusOK, tokenForm{ExpiresIn: "90d"}, secret, &notice{
		Kind:    noticeOK,
		Message: "Created " + token.Name + ". Copy the secret now — it is not stored and cannot be shown again.",
	})
}

// tokenNotice turns a mint failure into something a person can act on.
func tokenNotice(err error) *notice {
	var invalid *auth.InvalidInputError
	switch {
	case errors.As(err, &invalid):
		return &notice{Kind: noticeError, Message: titleCase(invalid.Field) + " " + invalid.Message + "."}
	case errors.Is(err, auth.ErrTokenLimit):
		return &notice{Kind: noticeError, Message: "This account already holds the maximum number of active API tokens. Revoke one first."}
	default:
		return &notice{Kind: noticeError, Message: "The token could not be created."}
	}
}

func tokenErrorStatus(err error) int {
	var invalid *auth.InvalidInputError
	switch {
	case errors.As(err, &invalid):
		return http.StatusUnprocessableEntity
	case errors.Is(err, auth.ErrTokenLimit):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

func (d *Dashboard) revokeToken(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	err := d.opts.Auth.RevokeAPIToken(r.Context(), r.PathValue("id"), p.UserID())
	switch {
	case err == nil:
		d.redirect(w, r, pathTokens, "token_revoked")
	case errors.Is(err, auth.ErrNotFound):
		// An unknown id and an already-revoked token are the same answer here,
		// as they are on the API: the token cannot be acted on.
		d.renderError(w, r, http.StatusNotFound, "No API token matches that identifier.")
	default:
		d.fail(w, r, "revoking an API token failed", err)
	}
}

// testPage sends one notification to prove the round trip works.
type testPage struct {
	view
	Devices    []db.Device
	Priorities []string
	Form       testForm
	Result     *testResult
}

type testForm struct {
	Title    string
	Body     string
	DeviceID string
	Priority string
}

// testResult is what the push transport reported.
type testResult struct {
	Attempted int
	Accepted  int
	// Failures are the provider's own descriptions. They are shown because this
	// page has exactly one reader — the account owner — and a failed test push
	// whose reason is hidden is not a test.
	Failures []string
}

// testSource identifies a dashboard test push to the client. It is not a
// service and not a token, and nothing resolves it.
const testSource = "hark-dashboard"

// defaultTestTitle is the sender name a test push carries when the form leaves
// the field empty.
const defaultTestTitle = "Hark"

func (d *Dashboard) showTest(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	d.renderTest(w, r, p, http.StatusOK, testForm{
		Body:     "Test notification from the Hark dashboard.",
		Priority: db.PriorityNormal,
	}, nil, nil)
}

func (d *Dashboard) renderTest(
	w http.ResponseWriter, r *http.Request, p *auth.Principal,
	status int, form testForm, result *testResult, n *notice,
) {
	devices, err := d.opts.Store.Devices.ListTargets(r.Context(), p.UserID(), nil)
	if err != nil {
		d.fail(w, r, "listing push targets failed", err)
		return
	}

	page := testPage{
		view:       d.newView(r, p, "Send a test", "test"),
		Devices:    devices,
		Priorities: db.Priorities,
		Form:       form,
		Result:     result,
	}
	if n != nil {
		page.Notice = n
	}
	d.render(w, r, status, tmplTest, page)
}

// sendTest pushes one alert through the same [push.Sender] the API uses.
//
// Nothing is written to the history: an agent notification is attributed to the
// API token that asked for it, and a session is a person rather than a
// requester. This is a diagnostic — "can this account reach that phone" — and
// the answer is on the page rather than in the log.
func (d *Dashboard) sendTest(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	form := testForm{
		Title:    strings.TrimSpace(r.PostFormValue("title")),
		Body:     strings.TrimSpace(r.PostFormValue("body")),
		DeviceID: r.PostFormValue("device_id"),
		Priority: r.PostFormValue("priority"),
	}
	if !db.ValidPriority(form.Priority) {
		form.Priority = db.PriorityNormal
	}
	if form.Body == "" {
		d.renderTest(w, r, p, http.StatusUnprocessableEntity, form, nil,
			&notice{Kind: noticeError, Message: "A body is required."})
		return
	}

	var ids []string
	if form.DeviceID != "" {
		ids = []string{form.DeviceID}
	}
	devices, err := d.opts.Store.Devices.ListTargets(r.Context(), p.UserID(), ids)
	if err != nil {
		d.fail(w, r, "listing push targets failed", err)
		return
	}
	if len(devices) == 0 {
		d.renderTest(w, r, p, http.StatusUnprocessableEntity, form, nil, &notice{
			Kind:    noticeWarn,
			Message: "No active device is registered to send to.",
		})
		return
	}

	title := form.Title
	if title == "" {
		title = defaultTestTitle
	}
	alerts := make([]push.Alert, 0, len(devices))
	for _, device := range devices {
		alerts = append(alerts, push.Alert{
			Target:     push.Target{DeviceID: device.ID, Token: device.APNsToken},
			Title:      title,
			Body:       form.Body,
			Priority:   form.Priority,
			ThreadKey:  testSource,
			SourceID:   testSource,
			SourceName: "Dashboard",
			RecordID:   testSource,
		})
	}

	sent := d.opts.Push.SendAlerts(r.Context(), alerts)
	if len(sent.StaleTokens) > 0 {
		// A token APNs has permanently rejected is dead everywhere, not just
		// for this push, so the device is retired here as it would be on any
		// other send path — on a context the caller's browser cannot cancel,
		// because losing that record would keep pushing into the void.
		ctx := context.WithoutCancel(r.Context())
		if _, err := d.opts.Store.Devices.Deactivate(ctx, sent.StaleTokens); err != nil {
			d.log(r).ErrorContext(r.Context(), "deactivating stale devices failed", "error", err)
		}
	}

	result := &testResult{Attempted: len(alerts), Accepted: sent.Accepted, Failures: sent.Failures}
	d.renderTest(w, r, p, http.StatusOK, form, result, testNotice(*result))
}

func testNotice(result testResult) *notice {
	switch {
	case result.Accepted == 0:
		return &notice{Kind: noticeError, Message: "APNs accepted nothing."}
	case result.Accepted < result.Attempted:
		return &notice{Kind: noticeWarn, Message: "APNs accepted some of the messages."}
	default:
		return &notice{Kind: noticeOK, Message: "APNs accepted every message. That is not proof a phone showed one."}
	}
}

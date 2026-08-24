package dashboard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/push"
)

// Maximum item counts shown in the overview. Full lists are available through
// history and the API.
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

// loadOverview assembles data shared by the full page and live fragment. It
// returns false after writing an error response.
func (d *Dashboard) loadOverview(w http.ResponseWriter, r *http.Request, p *auth.Principal) (overviewPage, bool) {
	ctx, userID, now := r.Context(), p.UserID(), d.opts.Auth.Now()

	devices, err := d.opts.Store.Devices.ListForUser(ctx, userID)
	if err != nil {
		d.fail(w, r, "listing devices failed", err)
		return overviewPage{}, false
	}
	tokens, err := d.opts.Auth.ListAPITokens(ctx, userID)
	if err != nil {
		d.fail(w, r, "listing API tokens failed", err)
		return overviewPage{}, false
	}
	activities, err := d.opts.Store.Activities.ListLiveForUser(ctx, userID, now, overviewActivities)
	if err != nil {
		d.fail(w, r, "listing live activities failed", err)
		return overviewPage{}, false
	}
	history, err := d.opts.Store.Feed.List(ctx, userID, db.FeedFilterAll, db.Cursor{}, overviewHistory)
	if err != nil {
		d.fail(w, r, "listing history failed", err)
		return overviewPage{}, false
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
	return page, true
}

func (d *Dashboard) showOverview(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	page, ok := d.loadOverview(w, r, p)
	if !ok {
		return
	}
	d.render(w, r, http.StatusOK, tmplOverview, page)
}

// liveOverview answers the overview's poll with the page's dynamic half, bare.
//
// The ETag is a digest of the rendered bytes, and the script sends it back as
// If-None-Match, so a poll that would change nothing costs a 304 and no swap.
// Relative timestamps change the digest as they update, which refreshes their
// displayed age without a full page reload.
func (d *Dashboard) liveOverview(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	page, ok := d.loadOverview(w, r, p)
	if !ok {
		return
	}

	var buf bytes.Buffer
	if err := tmplOverview.ExecuteTemplate(&buf, "overview-live", page); err != nil {
		d.fail(w, r, "rendering the overview fragment failed", err)
		return
	}
	sum := sha256.Sum256(buf.Bytes())
	etag := `"` + hex.EncodeToString(sum[:])[:16] + `"`

	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	// The browser's cache stays out of it: the script holds the previous tag
	// and sends it itself, so no-store never has anything stale to serve.
	h.Set("Cache-Control", "no-store")
	h.Set("ETag", etag)
	if match := r.Header.Get("If-None-Match"); strings.Contains(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = buf.WriteTo(w)
}

// historyPageSize is the number of archive entries shown per page.
const historyPageSize = 50

// historyPage is the account's full archive, paged.
type historyPage struct {
	view
	Items  []db.FeedItem
	Filter string
	// Older is the next page's URL, and Newest the way back to the top of the
	// current filter. Either is empty when there is nowhere to go.
	Older  string
	Newest string
}

// historyURL builds an archive page URL and omits default query values.
func historyURL(filter string, after db.Cursor) string {
	q := url.Values{}
	if filter != db.FeedFilterAll {
		q.Set("kind", filter)
	}
	if !after.IsZero() {
		q.Set("after", after.String())
	}
	if len(q) == 0 {
		return pathHistory
	}
	return pathHistory + "?" + q.Encode()
}

func (d *Dashboard) showHistory(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	query := r.URL.Query()
	filter := query.Get("kind")
	if filter == "" {
		filter = db.FeedFilterAll
	}
	if !db.ValidFeedFilter(filter) {
		d.renderError(w, r, http.StatusNotFound, "There is no such filter.")
		return
	}
	cursor, err := db.ParseCursor(query.Get("after"))
	if err != nil {
		// Cursors only ever come from this page's own Older link, so a bad one
		// is a mangled paste rather than anything worth explaining.
		d.renderError(w, r, http.StatusNotFound, "That page address is not valid.")
		return
	}

	feed, err := d.opts.Store.Feed.List(r.Context(), p.UserID(), filter, cursor, historyPageSize)
	if err != nil {
		d.fail(w, r, "listing history failed", err)
		return
	}

	page := historyPage{
		view:   d.newView(r, p, "History", "history"),
		Items:  feed.Items,
		Filter: filter,
	}
	if feed.HasMore() {
		page.Older = historyURL(filter, feed.Next)
	}
	if !cursor.IsZero() {
		page.Newest = historyURL(filter, db.Cursor{})
	}
	d.render(w, r, http.StatusOK, tmplHistory, page)
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
		Message: "Created " + token.Name + ". Copy the token now; it won't be shown again.",
	})
}

// tokenNotice turns a mint failure into something a person can act on.
func tokenNotice(err error) *notice {
	var invalid *auth.InvalidInputError
	switch {
	case errors.As(err, &invalid):
		return &notice{Kind: noticeError, Message: titleCase(invalid.Field) + " " + invalid.Message + "."}
	case errors.Is(err, auth.ErrTokenLimit):
		return &notice{Kind: noticeError, Message: "Token limit reached. Revoke a token before creating another."}
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
// Test notifications are diagnostic and are not written to account history.
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
		return &notice{Kind: noticeError, Message: "APNs accepted no notifications."}
	case result.Accepted < result.Attempted:
		return &notice{Kind: noticeWarn, Message: "APNs accepted some notifications."}
	default:
		return &notice{Kind: noticeOK, Message: "APNs accepted all notifications."}
	}
}

package dashboard

import (
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/httpapi"
	"github.com/abdeen-labs/hark/internal/id"
	"github.com/abdeen-labs/hark/internal/secret"
)

// servicesPage lists webhook sources and creates new ones.
type servicesPage struct {
	view
	Services   []serviceRow
	Priorities []string
	Form       serviceForm
}

// serviceRow includes the decrypted webhook URL. A nil URL means decryption
// failed; the owner can rotate the credential.
type serviceRow struct {
	db.Service
	WebhookURL *string
}

// servicePage is one service: its credential, its defaults, what it delivered
// lately, and the two destructive actions.
type servicePage struct {
	view
	Service    db.Service
	WebhookURL *string
	Priorities []string
	Form       serviceForm
	Deliveries []db.EventListItem
}

// serviceDeliveries is how much of the service's log its own page shows: proof
// the hookup works and what it said last, not the archive.
const serviceDeliveries = 15

// serviceForm is the create and edit form's state, kept so a rejected
// submission comes back filled in. The zero value of every field means
// "absent": an empty optional URL clears the column, which is what a form —
// unlike a PATCH body — can honestly express.
type serviceForm struct {
	Title    string
	ImageURL string
	URL      string
	Priority string
}

func serviceFormFrom(r *http.Request) serviceForm {
	form := serviceForm{
		Title:    strings.TrimSpace(r.PostFormValue("title")),
		ImageURL: strings.TrimSpace(r.PostFormValue("image_url")),
		URL:      strings.TrimSpace(r.PostFormValue("url")),
		Priority: r.PostFormValue("priority"),
	}
	if !db.ValidPriority(form.Priority) {
		// The form renders a closed select, so an unknown member is tampering
		// rather than a typo worth reporting.
		form.Priority = db.PriorityNormal
	}
	return form
}

// validate reports all form problems using the API's shared URL rules.
func (f serviceForm) validate() *notice {
	var problems []string
	// 80 is the API's title limit, mirrored by the template's maxlength.
	if n := utf8.RuneCountInString(f.Title); n < 1 || n > 80 {
		problems = append(problems, "The title must be 1-80 characters.")
	}
	if f.ImageURL != "" && !httpapi.ValidAvatarURL(f.ImageURL) {
		problems = append(problems, "The avatar must be a public HTTPS URL.")
	}
	if f.URL != "" && !httpapi.ValidTapURL(f.URL) {
		problems = append(problems, "The tap destination must be a web URL or an app deep link.")
	}
	if problems == nil {
		return nil
	}
	return &notice{Kind: noticeError, Message: strings.Join(problems, " ")}
}

func (f serviceForm) imageURL() *string {
	if f.ImageURL == "" {
		return nil
	}
	return &f.ImageURL
}

func (f serviceForm) linkURL() *string {
	if f.URL == "" {
		return nil
	}
	return &f.URL
}

// formFor pre-fills the edit form with a service's stored defaults.
func formFor(svc db.Service) serviceForm {
	form := serviceForm{Title: svc.Title, Priority: svc.Priority}
	if svc.ImageURL != nil {
		form.ImageURL = *svc.ImageURL
	}
	if svc.URL != nil {
		form.URL = *svc.URL
	}
	return form
}

// hookURL renders the public ingest URL for a plaintext webhook token — the
// same spelling internal/httpapi hands to API callers.
func (d *Dashboard) hookURL(token string) string {
	p := "/hooks/" + token
	if d.opts.PublicURL == nil {
		return p
	}
	joined := *d.opts.PublicURL
	joined.Path = strings.TrimRight(joined.Path, "/") + p
	return joined.String()
}

// webhookURL reads a service's ingest URL back out of its ciphertext, or nil
// when the stored form will not open under this key.
func (d *Dashboard) webhookURL(r *http.Request, svc db.Service) *string {
	token, err := d.opts.Secrets.Decrypt(secret.PurposeWebhookToken, svc.TokenCiphertext)
	if err != nil {
		d.log(r).WarnContext(r.Context(), "webhook token could not be decrypted",
			"service_id", svc.ID, "error", err)
		return nil
	}
	url := d.hookURL(token)
	return &url
}

func (d *Dashboard) showServices(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	d.renderServices(w, r, p, http.StatusOK, serviceForm{Priority: db.PriorityNormal}, nil)
}

func (d *Dashboard) renderServices(
	w http.ResponseWriter, r *http.Request, p *auth.Principal,
	status int, form serviceForm, n *notice,
) {
	services, err := d.opts.Store.Services.ListForUser(r.Context(), p.UserID())
	if err != nil {
		d.fail(w, r, "listing services failed", err)
		return
	}

	rows := make([]serviceRow, 0, len(services))
	for _, svc := range services {
		rows = append(rows, serviceRow{Service: svc, WebhookURL: d.webhookURL(r, svc)})
	}

	page := servicesPage{
		view:       d.newView(r, p, "Services", "services"),
		Services:   rows,
		Priorities: db.Priorities,
		Form:       form,
	}
	if n != nil {
		page.Notice = n
	}
	d.render(w, r, status, tmplServices, page)
}

// createService mints a webhook source and its credential, then lands on the
// service's own page, where the fresh URL is waiting to be copied.
func (d *Dashboard) createService(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	form := serviceFormFrom(r)
	if n := form.validate(); n != nil {
		d.renderServices(w, r, p, http.StatusUnprocessableEntity, form, n)
		return
	}

	token := auth.NewWebhookToken()
	ciphertext, err := d.opts.Secrets.Encrypt(secret.PurposeWebhookToken, token)
	if err != nil {
		d.fail(w, r, "sealing a webhook token failed", err)
		return
	}

	svc, err := d.opts.Store.Services.Create(r.Context(), db.CreateServiceParams{
		ID:              id.New(),
		UserID:          p.UserID(),
		Title:           form.Title,
		ImageURL:        form.imageURL(),
		URL:             form.linkURL(),
		Priority:        form.Priority,
		TokenHash:       auth.WebhookTokenHash(token),
		TokenCiphertext: ciphertext,
		Now:             d.opts.Auth.Now(),
	})
	if err != nil {
		d.fail(w, r, "creating a service failed", err)
		return
	}
	d.redirect(w, r, pathServices+"/"+svc.ID, "service_created")
}

func (d *Dashboard) showService(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	svc, err := d.opts.Store.Services.ByID(r.Context(), r.PathValue("id"), p.UserID())
	switch {
	case errors.Is(err, db.ErrNotFound):
		d.renderError(w, r, http.StatusNotFound, "No service matches that identifier.")
	case err != nil:
		d.fail(w, r, "loading a service failed", err)
	default:
		d.renderService(w, r, p, http.StatusOK, *svc, formFor(*svc), nil)
	}
}

func (d *Dashboard) renderService(
	w http.ResponseWriter, r *http.Request, p *auth.Principal,
	status int, svc db.Service, form serviceForm, n *notice,
) {
	deliveries, err := d.opts.Store.Events.ListForService(
		r.Context(), svc.ID, p.UserID(), db.Cursor{}, serviceDeliveries)
	if err != nil {
		d.fail(w, r, "listing a service's deliveries failed", err)
		return
	}

	page := servicePage{
		view:       d.newView(r, p, svc.Title, "services"),
		Service:    svc,
		WebhookURL: d.webhookURL(r, svc),
		Priorities: db.Priorities,
		Form:       form,
		Deliveries: deliveries.Items,
	}
	if n != nil {
		page.Notice = n
	}
	d.render(w, r, status, tmplService, page)
}

// updateService replaces a service's defaults with what the form shows.
//
// Every field is written, not patched: the form renders the current values, so
// what comes back is the whole truth as the owner last saw it, and an emptied
// optional field means "clear it".
func (d *Dashboard) updateService(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	form := serviceFormFrom(r)
	if n := form.validate(); n != nil {
		svc, err := d.opts.Store.Services.ByID(r.Context(), r.PathValue("id"), p.UserID())
		switch {
		case errors.Is(err, db.ErrNotFound):
			d.renderError(w, r, http.StatusNotFound, "No service matches that identifier.")
		case err != nil:
			d.fail(w, r, "loading a service failed", err)
		default:
			d.renderService(w, r, p, http.StatusUnprocessableEntity, *svc, form, n)
		}
		return
	}

	_, err := d.opts.Store.Services.Update(r.Context(), db.UpdateServiceParams{
		ID:       r.PathValue("id"),
		UserID:   p.UserID(),
		Title:    db.Value(form.Title),
		ImageURL: db.Value(form.imageURL()),
		URL:      db.Value(form.linkURL()),
		Priority: db.Value(form.Priority),
		Now:      d.opts.Auth.Now(),
	})
	switch {
	case errors.Is(err, db.ErrNotFound):
		d.renderError(w, r, http.StatusNotFound, "No service matches that identifier.")
	case err != nil:
		d.fail(w, r, "updating a service failed", err)
	default:
		d.redirect(w, r, pathServices+"/"+r.PathValue("id"), "service_updated")
	}
}

// rotateWebhookToken replaces a service's credential. The previous URL stops
// working immediately — the same no-grace trade POST
// /services/{id}/webhook-token makes, and for the same reason: a rotation
// is what an owner reaches for when the URL has leaked.
func (d *Dashboard) rotateWebhookToken(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	token := auth.NewWebhookToken()
	ciphertext, err := d.opts.Secrets.Encrypt(secret.PurposeWebhookToken, token)
	if err != nil {
		d.fail(w, r, "sealing a webhook token failed", err)
		return
	}

	_, err = d.opts.Store.Services.RotateToken(r.Context(),
		r.PathValue("id"), p.UserID(), auth.WebhookTokenHash(token), ciphertext, d.opts.Auth.Now())
	switch {
	case errors.Is(err, db.ErrNotFound):
		d.renderError(w, r, http.StatusNotFound, "No service matches that identifier.")
	case err != nil:
		d.fail(w, r, "rotating a webhook token failed", err)
	default:
		d.redirect(w, r, pathServices+"/"+r.PathValue("id"), "webhook_rotated")
	}
}

func (d *Dashboard) deleteService(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	deleted, err := d.opts.Store.Services.Delete(r.Context(), r.PathValue("id"), p.UserID())
	switch {
	case err != nil:
		d.fail(w, r, "deleting a service failed", err)
	case !deleted:
		d.renderError(w, r, http.StatusNotFound, "No service matches that identifier.")
	default:
		d.redirect(w, r, pathServices, "service_deleted")
	}
}

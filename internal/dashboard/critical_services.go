package dashboard

import (
	"errors"
	"net/http"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/id"
	"github.com/abdeen-labs/hark/internal/secret"
)

// criticalServicesPage is the separate management flow for services that are
// allowed to request Critical priority. Their webhook contract and delivery
// records are otherwise identical to regular services.
type criticalServicesPage struct {
	view
	CriticalAlertsEnabled bool
	Services              []serviceRow
	Priorities            []string
	Form                  serviceForm
}

func (d *Dashboard) showCriticalServices(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	d.renderCriticalServices(w, r, p, http.StatusOK, serviceForm{
		Priority: db.PriorityNormal, CriticalEnabled: true,
	}, nil)
}

func (d *Dashboard) renderCriticalServices(
	w http.ResponseWriter, r *http.Request, p *auth.Principal,
	status int, form serviceForm, n *notice,
) {
	user, err := d.opts.Store.Users.ByID(r.Context(), p.UserID())
	if err != nil {
		d.fail(w, r, "loading the Critical Alert setting failed", err)
		return
	}
	services, err := d.opts.Store.Services.ListCriticalForUser(r.Context(), p.UserID())
	if err != nil {
		d.fail(w, r, "listing critical services failed", err)
		return
	}
	rows := make([]serviceRow, 0, len(services))
	for _, svc := range services {
		rows = append(rows, serviceRow{Service: svc, WebhookURL: d.webhookURL(r, svc)})
	}
	page := criticalServicesPage{
		view:                  d.newView(r, p, "Critical Alerts", "critical-services"),
		CriticalAlertsEnabled: user.CriticalAlertsEnabled,
		Services:              rows, Priorities: db.CriticalPriorities, Form: form,
	}
	if n != nil {
		page.Notice = n
	}
	d.render(w, r, status, tmplCriticalServices, page)
}

func (d *Dashboard) createCriticalService(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	form := serviceFormFrom(r, true)
	if n := form.validate(); n != nil {
		d.renderCriticalServices(w, r, p, http.StatusUnprocessableEntity, form, n)
		return
	}
	token := auth.NewWebhookToken()
	ciphertext, err := d.opts.Secrets.Encrypt(secret.PurposeWebhookToken, token)
	if err != nil {
		d.fail(w, r, "sealing a webhook token failed", err)
		return
	}
	svc, err := d.opts.Store.Services.Create(r.Context(), db.CreateServiceParams{
		ID: id.New(), UserID: p.UserID(), Title: form.Title,
		ImageURL: form.imageURL(), URL: form.linkURL(), Priority: form.Priority,
		CriticalCapable: true, CriticalEnabled: form.CriticalEnabled,
		TokenHash: auth.WebhookTokenHash(token), TokenCiphertext: ciphertext,
		Now: d.opts.Auth.Now(),
	})
	if err != nil {
		d.fail(w, r, "creating a critical service failed", err)
		return
	}
	d.redirect(w, r, pathCriticalServices+"/"+svc.ID, "critical_service_created")
}

func (d *Dashboard) showCriticalService(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	svc, err := d.opts.Store.Services.CriticalByID(r.Context(), r.PathValue("id"), p.UserID())
	switch {
	case errors.Is(err, db.ErrNotFound):
		d.renderError(w, r, http.StatusNotFound, "No critical service matches that identifier.")
	case err != nil:
		d.fail(w, r, "loading a critical service failed", err)
	default:
		d.renderCriticalService(w, r, p, http.StatusOK, *svc, formFor(*svc), nil)
	}
}

func (d *Dashboard) renderCriticalService(
	w http.ResponseWriter, r *http.Request, p *auth.Principal,
	status int, svc db.Service, form serviceForm, n *notice,
) {
	deliveries, err := d.opts.Store.Events.ListForService(
		r.Context(), svc.ID, p.UserID(), db.Cursor{}, serviceDeliveries)
	if err != nil {
		d.fail(w, r, "listing a critical service's deliveries failed", err)
		return
	}
	page := servicePage{
		view: d.newView(r, p, svc.Title, "critical-services"), Service: svc,
		WebhookURL: d.webhookURL(r, svc), Priorities: db.CriticalPriorities,
		Form: form, Deliveries: deliveries.Items, BasePath: pathCriticalServices,
		BackLabel: "All critical services", Critical: true,
	}
	if n != nil {
		page.Notice = n
	}
	d.render(w, r, status, tmplService, page)
}

func (d *Dashboard) updateCriticalService(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	form := serviceFormFrom(r, true)
	if n := form.validate(); n != nil {
		svc, err := d.opts.Store.Services.CriticalByID(r.Context(), r.PathValue("id"), p.UserID())
		switch {
		case errors.Is(err, db.ErrNotFound):
			d.renderError(w, r, http.StatusNotFound, "No critical service matches that identifier.")
		case err != nil:
			d.fail(w, r, "loading a critical service failed", err)
		default:
			d.renderCriticalService(w, r, p, http.StatusUnprocessableEntity, *svc, form, n)
		}
		return
	}
	_, err := d.opts.Store.Services.Update(r.Context(), db.UpdateServiceParams{
		ID: r.PathValue("id"), UserID: p.UserID(), CriticalCapable: true,
		Title: db.Value(form.Title), ImageURL: db.Value(form.imageURL()),
		URL: db.Value(form.linkURL()), Priority: db.Value(form.Priority),
		CriticalEnabled: db.Value(form.CriticalEnabled), Now: d.opts.Auth.Now(),
	})
	switch {
	case errors.Is(err, db.ErrNotFound):
		d.renderError(w, r, http.StatusNotFound, "No critical service matches that identifier.")
	case err != nil:
		d.fail(w, r, "updating a critical service failed", err)
	default:
		d.redirect(w, r, pathCriticalServices+"/"+r.PathValue("id"), "critical_service_updated")
	}
}

func (d *Dashboard) rotateCriticalWebhookToken(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	token := auth.NewWebhookToken()
	ciphertext, err := d.opts.Secrets.Encrypt(secret.PurposeWebhookToken, token)
	if err != nil {
		d.fail(w, r, "sealing a webhook token failed", err)
		return
	}
	_, err = d.opts.Store.Services.RotateCriticalToken(r.Context(), r.PathValue("id"), p.UserID(),
		auth.WebhookTokenHash(token), ciphertext, d.opts.Auth.Now())
	switch {
	case errors.Is(err, db.ErrNotFound):
		d.renderError(w, r, http.StatusNotFound, "No critical service matches that identifier.")
	case err != nil:
		d.fail(w, r, "rotating a critical webhook token failed", err)
	default:
		d.redirect(w, r, pathCriticalServices+"/"+r.PathValue("id"), "webhook_rotated")
	}
}

func (d *Dashboard) deleteCriticalService(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	deleted, err := d.opts.Store.Services.DeleteCritical(r.Context(), r.PathValue("id"), p.UserID())
	switch {
	case err != nil:
		d.fail(w, r, "deleting a critical service failed", err)
	case !deleted:
		d.renderError(w, r, http.StatusNotFound, "No critical service matches that identifier.")
	default:
		d.redirect(w, r, pathCriticalServices, "critical_service_deleted")
	}
}

func (d *Dashboard) saveCriticalAlertSetting(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	enabled := r.PostFormValue("critical_alerts_enabled") != ""
	if err := d.opts.Store.Users.SetCriticalAlertsEnabled(r.Context(), p.UserID(), enabled, d.opts.Auth.Now()); err != nil {
		d.fail(w, r, "saving the Critical Alert setting failed", err)
		return
	}
	d.redirect(w, r, pathCriticalServices, "critical_setting_saved")
}

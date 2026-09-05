package dashboard

import (
	"errors"
	"net/http"
	"strings"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
)

// admin adds the role check to the normal session and CSRF gates.
func (d *Dashboard) admin(h handler) handler {
	return func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		if !p.IsAdmin() {
			d.renderError(w, r, http.StatusForbidden, "Only the administrator can manage accounts.")
			return
		}
		h(w, r, p)
	}
}

type accountForm struct {
	Username, DisplayName, Email string
}

type accountsPage struct {
	view
	Users []db.User
	Form  accountForm
}

func (d *Dashboard) showAccounts(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	d.renderAccounts(w, r, p, http.StatusOK, accountForm{}, nil)
}

func (d *Dashboard) renderAccounts(w http.ResponseWriter, r *http.Request, p *auth.Principal, status int, form accountForm, n *notice) {
	users, err := d.opts.Auth.ListAccounts(r.Context(), p)
	if err != nil {
		d.fail(w, r, "listing accounts failed", err)
		return
	}
	page := accountsPage{view: d.newView(r, p, "Accounts", "accounts"), Users: users, Form: form}
	if n != nil {
		page.Notice = n
	}
	d.render(w, r, status, tmplAccounts, page)
}

func (d *Dashboard) provisionAccount(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	// Passwords are never retained in the view or echoed after validation errors.
	form := accountForm{
		Username:    strings.TrimSpace(r.PostFormValue("username")),
		DisplayName: strings.TrimSpace(r.PostFormValue("display_name")),
		Email:       strings.TrimSpace(r.PostFormValue("email")),
	}
	_, err := d.opts.Auth.ProvisionAccount(r.Context(), p, auth.CreateAccountParams{
		Username: form.Username, DisplayName: form.DisplayName, Email: form.Email,
		Password: r.PostFormValue("password"),
	})
	var invalid *auth.InvalidInputError
	switch {
	case err == nil:
		d.redirect(w, r, pathAccounts, "account_created")
	case errors.As(err, &invalid):
		d.renderAccounts(w, r, p, http.StatusUnprocessableEntity, form, &notice{
			Kind: noticeError, Message: titleCase(invalid.Field) + " " + invalid.Message + ".",
		})
	case errors.Is(err, auth.ErrAdminRequired):
		d.renderError(w, r, http.StatusForbidden, "Only the administrator can manage accounts.")
	default:
		d.fail(w, r, "provisioning an account failed", err)
	}
}

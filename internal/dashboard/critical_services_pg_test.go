package dashboard

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/id"
	"github.com/abdeen-labs/hark/internal/secret"
)

func mustCriticalService(t *testing.T, d *Dashboard, store *db.Store, userID, priority string, enabled bool) db.Service {
	t.Helper()
	token := auth.NewWebhookToken()
	ciphertext, err := d.opts.Secrets.Encrypt(secret.PurposeWebhookToken, token)
	if err != nil {
		t.Fatalf("encrypt webhook token: %v", err)
	}
	svc, err := store.Services.Create(t.Context(), db.CreateServiceParams{
		ID: id.New(), UserID: userID, Title: "Home Assistant", Priority: priority,
		CriticalCapable: true, CriticalEnabled: enabled,
		TokenHash: auth.WebhookTokenHash(token), TokenCiphertext: ciphertext, Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("create critical service: %v", err)
	}
	return *svc
}

func TestCriticalServiceLifecycleThroughTheDashboard(t *testing.T) {
	d, store, userID := newPGDashboard(t)
	form := "title=Home+Assistant&priority=normal&image_url=https%3A%2F%2Fexample.com%2Flogo.png&url=hark-test%3A%2F%2Fhome&critical_enabled=on"
	rec := send(d, withCSRF(t, d, asOwner(signedIn(http.MethodPost, pathCriticalServices, ""), userID), form))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create: status = %d: %s", rec.Code, rec.Body)
	}
	location := rec.Header().Get("Location")
	if !strings.HasPrefix(location, pathCriticalServices+"/") || !strings.HasSuffix(location, "?done=critical_service_created") {
		t.Fatalf("create location = %q", location)
	}
	id := strings.TrimSuffix(strings.TrimPrefix(location, pathCriticalServices+"/"), "?done=critical_service_created")
	svc, err := store.Services.CriticalByID(t.Context(), id, userID)
	if err != nil {
		t.Fatalf("load created critical service: %v", err)
	}
	if svc.Title != "Home Assistant" || svc.Priority != db.PriorityNormal || !svc.CriticalEnabled || svc.ImageURL == nil || svc.URL == nil {
		t.Fatalf("created critical service = %+v", svc)
	}
	if _, err := store.Services.ByID(t.Context(), id, userID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("regular service lookup error = %v, want not found", err)
	}

	rec = send(d, asOwner(signedIn(http.MethodGet, location, ""), userID))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "/hooks/"+auth.WebhookTokenPrefix) {
		t.Fatalf("detail: status = %d or webhook missing: %s", rec.Code, rec.Body)
	}
	rec = send(d, asOwner(signedIn(http.MethodGet, pathCriticalServices, ""), userID))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `data-critical-service="`+id+`"`) {
		t.Fatalf("list: status = %d or service missing: %s", rec.Code, rec.Body)
	}

	form = "title=Front+Door&priority=critical&image_url=https%3A%2F%2Fexample.com%2Fdoor.png&url=&critical_enabled=on"
	rec = send(d, withCSRF(t, d, asOwner(signedIn(http.MethodPost, pathCriticalServices+"/"+id, ""), userID), form))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("update: status = %d: %s", rec.Code, rec.Body)
	}
	svc, err = store.Services.CriticalByID(t.Context(), id, userID)
	if err != nil || svc.Title != "Front Door" || svc.Priority != db.PriorityCritical || !svc.CriticalEnabled || svc.URL != nil {
		t.Fatalf("updated critical service = %+v, err = %v", svc, err)
	}

	before := svc.TokenHash
	rec = send(d, withCSRF(t, d, asOwner(signedIn(http.MethodPost, pathCriticalServices+"/"+id+"/rotate", ""), userID), ""))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("rotate: status = %d: %s", rec.Code, rec.Body)
	}
	svc, _ = store.Services.CriticalByID(t.Context(), id, userID)
	if svc.TokenHash == before {
		t.Fatal("rotate did not replace the webhook token")
	}

	rec = send(d, withCSRF(t, d, asOwner(signedIn(http.MethodPost, pathCriticalServices+"/"+id+"/delete", ""), userID), ""))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("delete: status = %d: %s", rec.Code, rec.Body)
	}
	if _, err := store.Services.CriticalByID(t.Context(), id, userID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("deleted service error = %v, want not found", err)
	}
}

func TestCriticalAccountSwitchThroughTheDashboard(t *testing.T) {
	d, store, userID := newPGDashboard(t)
	for _, tc := range []struct {
		form string
		want bool
	}{{"", false}, {"critical_alerts_enabled=on", true}} {
		rec := send(d, withCSRF(t, d, asOwner(signedIn(http.MethodPost, pathCriticalServices+"/settings", ""), userID), tc.form))
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("save setting: status = %d: %s", rec.Code, rec.Body)
		}
		user, err := store.Users.ByID(t.Context(), userID)
		if err != nil || user.CriticalAlertsEnabled != tc.want {
			t.Fatalf("critical setting = %v, err = %v; want %v", user.CriticalAlertsEnabled, err, tc.want)
		}
	}
}

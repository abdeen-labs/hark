package dashboard

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
)

// TestCriticalServiceLifecycleThroughTheDashboard walks a critical-capable
// service through the same services pages a regular one uses.
func TestCriticalServiceLifecycleThroughTheDashboard(t *testing.T) {
	d, store, userID := newPGDashboard(t)
	form := "title=Home+Assistant&priority=normal&image_url=https%3A%2F%2Fexample.com%2Flogo.png&url=hark-test%3A%2F%2Fhome&critical_enabled=on"
	rec := send(d, withCSRF(t, d, asOwner(signedIn(http.MethodPost, pathServices, ""), userID), form))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create: status = %d: %s", rec.Code, rec.Body)
	}
	location := rec.Header().Get("Location")
	if !strings.HasPrefix(location, pathServices+"/") || !strings.HasSuffix(location, "?done=service_created") {
		t.Fatalf("create location = %q", location)
	}
	id := strings.TrimSuffix(strings.TrimPrefix(location, pathServices+"/"), "?done=service_created")
	svc, err := store.Services.CriticalByID(t.Context(), id, userID)
	if err != nil {
		t.Fatalf("load created critical service: %v", err)
	}
	if svc.Title != "Home Assistant" || svc.Priority != db.PriorityNormal || !svc.CriticalEnabled || svc.ImageURL == nil || svc.URL == nil {
		t.Fatalf("created critical service = %+v", svc)
	}
	// The API's regular-service resource still does not see it.
	if _, err := store.Services.ByID(t.Context(), id, userID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("regular service lookup error = %v, want not found", err)
	}

	rec = send(d, asOwner(signedIn(http.MethodGet, location, ""), userID))
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "/hooks/"+auth.WebhookTokenPrefix) {
		t.Fatalf("detail: status = %d or webhook missing: %s", rec.Code, body)
	}
	// Its page offers the Critical priority and the service switch.
	if !strings.Contains(body, `value="`+db.PriorityCritical+`"`) || !strings.Contains(body, `name="critical_enabled"`) {
		t.Fatalf("detail page lacks the critical controls:\n%s", body)
	}
	rec = send(d, asOwner(signedIn(http.MethodGet, pathServices, ""), userID))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `data-service="`+id+`"`) {
		t.Fatalf("list: status = %d or service missing: %s", rec.Code, rec.Body)
	}

	form = "title=Front+Door&priority=critical&image_url=https%3A%2F%2Fexample.com%2Fdoor.png&url=&critical_enabled=on"
	rec = send(d, withCSRF(t, d, asOwner(signedIn(http.MethodPost, pathServices+"/"+id, ""), userID), form))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("update: status = %d: %s", rec.Code, rec.Body)
	}
	svc, err = store.Services.CriticalByID(t.Context(), id, userID)
	if err != nil || svc.Title != "Front Door" || svc.Priority != db.PriorityCritical || !svc.CriticalEnabled || svc.URL != nil {
		t.Fatalf("updated critical service = %+v, err = %v", svc, err)
	}

	// Unchecking the service switch turns Critical delivery off without
	// touching the capability.
	form = "title=Front+Door&priority=normal&image_url=&url="
	rec = send(d, withCSRF(t, d, asOwner(signedIn(http.MethodPost, pathServices+"/"+id, ""), userID), form))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("update off: status = %d: %s", rec.Code, rec.Body)
	}
	svc, err = store.Services.CriticalByID(t.Context(), id, userID)
	if err != nil || !svc.CriticalCapable || svc.CriticalEnabled {
		t.Fatalf("service after switching off = %+v, err = %v", svc, err)
	}

	before := svc.TokenHash
	rec = send(d, withCSRF(t, d, asOwner(signedIn(http.MethodPost, pathServices+"/"+id+"/rotate", ""), userID), ""))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("rotate: status = %d: %s", rec.Code, rec.Body)
	}
	svc, _ = store.Services.CriticalByID(t.Context(), id, userID)
	if svc.TokenHash == before {
		t.Fatal("rotate did not replace the webhook token")
	}

	rec = send(d, withCSRF(t, d, asOwner(signedIn(http.MethodPost, pathServices+"/"+id+"/delete", ""), userID), ""))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("delete: status = %d: %s", rec.Code, rec.Body)
	}
	if _, err := store.Services.CriticalByID(t.Context(), id, userID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("deleted service error = %v, want not found", err)
	}
}

// TestRegularServiceRejectsCriticalPriority pins the capability down: a
// service created without the switch cannot pick Critical as its default.
func TestRegularServiceRejectsCriticalPriority(t *testing.T) {
	d, store, userID := newPGDashboard(t)
	rec := send(d, withCSRF(t, d, asOwner(signedIn(http.MethodPost, pathServices, ""), userID), "title=CI&priority=normal"))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create: status = %d: %s", rec.Code, rec.Body)
	}
	location := rec.Header().Get("Location")
	id := strings.TrimSuffix(strings.TrimPrefix(location, pathServices+"/"), "?done=service_created")

	rec = send(d, asOwner(signedIn(http.MethodGet, location, ""), userID))
	if body := rec.Body.String(); strings.Contains(body, `value="`+db.PriorityCritical+`"`) || strings.Contains(body, `name="critical_enabled"`) {
		t.Fatalf("a regular service's page offers critical controls:\n%s", body)
	}

	form := "title=CI&priority=critical&image_url=&url=&critical_enabled=on"
	rec = send(d, withCSRF(t, d, asOwner(signedIn(http.MethodPost, pathServices+"/"+id, ""), userID), form))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("update: status = %d: %s", rec.Code, rec.Body)
	}
	svc, err := store.Services.ByID(t.Context(), id, userID)
	if err != nil || svc.Priority != db.PriorityNormal || svc.CriticalCapable || svc.CriticalEnabled {
		t.Fatalf("regular service after a critical update = %+v, err = %v", svc, err)
	}
}

func TestCriticalAccountSwitchThroughTheDashboard(t *testing.T) {
	d, store, userID := newPGDashboard(t)
	for _, tc := range []struct {
		form string
		want bool
	}{{"", false}, {"critical_alerts_enabled=on", true}} {
		rec := send(d, withCSRF(t, d, asOwner(signedIn(http.MethodPost, pathServices+"/critical", ""), userID), tc.form))
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("save setting: status = %d: %s", rec.Code, rec.Body)
		}
		user, err := store.Users.ByID(t.Context(), userID)
		if err != nil || user.CriticalAlertsEnabled != tc.want {
			t.Fatalf("critical setting = %v, err = %v; want %v", user.CriticalAlertsEnabled, err, tc.want)
		}
	}
}

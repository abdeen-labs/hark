package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/abdeen-labs/hark/internal/db"
)

func (f *fixture) createCriticalService(title, priority string) (criticalServiceDTO, string) {
	f.t.Helper()
	var created createdCriticalServiceResponse
	f.expect(http.MethodPost, "/critical-services", f.session,
		`{"title":"`+title+`","priority":"`+priority+`","image_url":"https://example.com/avatar.png","url":"hark-test://open","critical_enabled":true}`,
		http.StatusCreated, &created)
	hook := strings.TrimPrefix(created.WebhookURL, "https://hark.example.com")
	if hook == created.WebhookURL {
		f.t.Fatalf("critical webhook URL is not on the public origin: %q", created.WebhookURL)
	}
	return created.Service, hook
}

func TestCriticalServiceLifecycle(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	created, hook := f.createCriticalService("Home Assistant", db.PriorityNormal)
	if created.Priority != db.PriorityNormal || !created.CriticalEnabled || created.WebhookURL == nil {
		t.Fatalf("created service = %+v", created)
	}

	var listed criticalServiceListResponse
	f.expect(http.MethodGet, "/critical-services", f.session, "", http.StatusOK, &listed)
	if len(listed.Services) != 1 || listed.Services[0].ID != created.ID {
		t.Fatalf("listed services = %+v", listed.Services)
	}

	var got criticalServiceResponse
	f.expect(http.MethodPatch, "/critical-services/"+created.ID, f.session,
		`{"title":"Front Door","image_url":"https://example.com/door.png","url":"https://example.com/door","priority":"critical","critical_enabled":false}`,
		http.StatusOK, &got)
	if got.Service.Title != "Front Door" || got.Service.Priority != db.PriorityCritical || got.Service.CriticalEnabled ||
		got.Service.ImageURL == nil || *got.Service.ImageURL != "https://example.com/door.png" {
		t.Fatalf("updated service = %+v", got.Service)
	}

	// Critical and regular management flows are deliberately disjoint.
	if rec := f.request(http.MethodGet, "/services/"+created.ID, f.session, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("critical service through regular endpoint: status = %d: %s", rec.Code, rec.Body)
	}

	var rotated createdCriticalServiceResponse
	f.expect(http.MethodPost, "/critical-services/"+created.ID+"/webhook-token", f.session, "",
		http.StatusCreated, &rotated)
	if rotated.WebhookURL == "" || strings.HasSuffix(rotated.WebhookURL, hook) {
		t.Fatalf("rotated webhook URL = %q", rotated.WebhookURL)
	}
	if rec := f.request(http.MethodPost, hook, "", `{"body":"old URL"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("old webhook URL: status = %d, want 404: %s", rec.Code, rec.Body)
	}

	f.expect(http.MethodDelete, "/critical-services/"+created.ID, f.session, "", http.StatusNoContent, nil)
	if rec := f.request(http.MethodGet, "/critical-services/"+created.ID, f.session, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("deleted critical service: status = %d, want 404: %s", rec.Code, rec.Body)
	}
}

func TestCriticalServiceUsesTheFullWebhookPipeline(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	device := f.registerDevice(strings.Repeat("c1", 32))
	service, hook := f.createCriticalService("Home Assistant", db.PriorityNormal)

	var sent webhookNotifyResponse
	f.expect(http.MethodPost, hook, "", `{
		"title":"Garage Door",
		"body":"Opened",
		"image_url":"https://example.com/garage.png",
		"url":"hark-test://garage",
		"device_ids":["`+device.ID+`"]
	}`, http.StatusCreated, &sent)
	alert := f.sender.lastAlert(t)
	if alert.Priority != db.PriorityNormal ||
		alert.Title != "Garage Door" || alert.Body != "Opened" ||
		alert.ImageURL == nil || *alert.ImageURL != "https://example.com/garage.png" ||
		alert.URL == nil || *alert.URL != "hark-test://garage" || alert.Target.DeviceID != device.ID {
		t.Fatalf("normal critical-service alert = %+v; event = %+v", alert, sent.Event)
	}

	f.expect(http.MethodPost, hook, "", `{"body":"Heads up","priority":"time_sensitive"}`,
		http.StatusCreated, &sent)
	if got := f.sender.lastAlert(t).Priority; got != db.PriorityTimeSensitive {
		t.Fatalf("time-sensitive priority = %q", got)
	}

	f.expect(http.MethodPost, hook, "", `{"body":"Wake up","priority":"critical"}`,
		http.StatusCreated, &sent)
	if got := f.sender.lastAlert(t).Priority; got != db.PriorityCritical {
		t.Fatalf("critical priority = %q", got)
	}

	var history historyListResponse
	f.expect(http.MethodGet, "/history?source="+strings.ReplaceAll(service.Title, " ", "+"), f.session, "",
		http.StatusOK, &history)
	if len(history.Items) != 3 {
		t.Fatalf("critical service history = %+v, want three ordinary delivery records", history.Items)
	}
}

func TestCriticalPriorityRequiresBothSwitches(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	f.registerDevice(strings.Repeat("c2", 32))
	service, hook := f.createCriticalService("Bedroom", db.PriorityCritical)

	post := func(want string) {
		t.Helper()
		var sent webhookNotifyResponse
		f.expect(http.MethodPost, hook, "", `{"body":"Alert"}`, http.StatusCreated, &sent)
		if got := f.sender.lastAlert(t).Priority; got != want {
			t.Fatalf("priority = %q; want %q", got, want)
		}
	}

	post(db.PriorityCritical)
	f.expect(http.MethodPatch, "/critical-services/"+service.ID, f.session,
		`{"critical_enabled":false}`, http.StatusOK, nil)
	post(db.PriorityTimeSensitive)
	f.expect(http.MethodPatch, "/critical-services/"+service.ID, f.session,
		`{"critical_enabled":true}`, http.StatusOK, nil)
	f.expect(http.MethodPatch, "/critical-settings", f.session,
		`{"critical_alerts_enabled":false}`, http.StatusOK, nil)
	post(db.PriorityTimeSensitive)

	// The switches gate only Critical. Explicit Normal and Time Sensitive remain exact.
	for requested, want := range map[string]string{
		db.PriorityNormal: db.PriorityNormal, db.PriorityTimeSensitive: db.PriorityTimeSensitive,
	} {
		var sent webhookNotifyResponse
		f.expect(http.MethodPost, hook, "", `{"body":"Still works","priority":"`+requested+`"}`,
			http.StatusCreated, &sent)
		if got := f.sender.lastAlert(t).Priority; got != want {
			t.Fatalf("requested %q with switches off: got %q", requested, got)
		}
	}
}

func TestRegularServiceRejectsCriticalPriority(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	_, hook := f.createService("CI")
	rec := f.request(http.MethodPost, hook, "", `{"body":"Nope","priority":"critical"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("regular webhook critical priority: status = %d, want 422: %s", rec.Code, rec.Body)
	}
}

func TestCriticalServiceCredentialBoundaries(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	service, _ := f.createCriticalService("Kitchen", db.PriorityNormal)
	for _, route := range []struct{ method, path, body string }{
		{http.MethodPost, "/critical-services", `{"title":"Garage"}`},
		{http.MethodPost, "/critical-services/" + service.ID + "/webhook-token", ""},
		{http.MethodGet, "/critical-settings", ""},
		{http.MethodPatch, "/critical-settings", `{"critical_alerts_enabled":false}`},
	} {
		if rec := f.request(route.method, route.path, f.token, route.body); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s with API token: status = %d, want 403: %s", route.method, route.path, rec.Code, rec.Body)
		}
	}

	// Read/write scope behavior exactly matches regular service management.
	f.expect(http.MethodGet, "/critical-services", f.token, "", http.StatusOK, nil)
	f.expect(http.MethodGet, "/critical-services/"+service.ID, f.token, "", http.StatusOK, nil)
	f.expect(http.MethodPatch, "/critical-services/"+service.ID, f.token,
		`{"priority":"time_sensitive"}`, http.StatusOK, nil)
	f.expect(http.MethodDelete, "/critical-services/"+service.ID, f.token, "", http.StatusNoContent, nil)
}

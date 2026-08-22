package dashboard

// The history page and the overview's live fragment against a real store, on
// the same schema and skip rule as services_pg_test.go.

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/id"
)

// seedEvents writes n settled deliveries for one service, a millisecond apart
// so the feed's (created_at, id) order is deterministic.
func seedEvents(t *testing.T, store *db.Store, serviceID string, n int) {
	t.Helper()
	base := time.Now().Add(-time.Duration(n) * time.Millisecond)
	for i := range n {
		if _, err := store.Events.Create(t.Context(), db.CreateEventParams{
			ID: id.New(), ServiceID: serviceID,
			Title: "Build", Body: "outcome " + strings.Repeat("x", i%3),
			Priority: db.PriorityNormal, Status: db.EventAccepted,
			Now: base.Add(time.Duration(i) * time.Millisecond),
		}); err != nil {
			t.Fatalf("seed event %d: %v", i, err)
		}
	}
}

func mustDashService(t *testing.T, store *db.Store, userID, title string) *db.Service {
	t.Helper()
	svc, err := store.Services.Create(t.Context(), db.CreateServiceParams{
		ID: id.New(), UserID: userID, Title: title, Priority: db.PriorityNormal,
		TokenHash: "hash-" + title, TokenCiphertext: "sealed", Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	return svc
}

func TestHistoryPagesThroughTheArchive(t *testing.T) {
	d, store, userID := newPGDashboard(t)
	svc := mustDashService(t, store, userID, "CI")
	seedEvents(t, store, svc.ID, historyPageSize+5)

	// The first page is full and links onward but not back.
	rec := send(d, asOwner(signedIn(http.MethodGet, pathHistory, ""), userID))
	if rec.Code != http.StatusOK {
		t.Fatalf("first page: status = %d: %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if got := strings.Count(body, `class="feed__item"`); got != historyPageSize {
		t.Errorf("first page shows %d items, want %d", got, historyPageSize)
	}
	target, found := pagerLink(body, "next")
	if !found {
		t.Fatalf("the first page has no rel=next link:\n%s", body)
	}
	if _, back := pagerLink(body, "prev"); back {
		t.Errorf("the first page has a rel=prev link:\n%s", body)
	}

	// Follow the link the page actually rendered.
	rec = send(d, asOwner(signedIn(http.MethodGet, target, ""), userID))
	if rec.Code != http.StatusOK {
		t.Fatalf("second page: status = %d: %s", rec.Code, rec.Body)
	}
	body = rec.Body.String()
	if got := strings.Count(body, `class="feed__item"`); got != 5 {
		t.Errorf("second page shows %d items, want 5", got)
	}
	if _, onward := pagerLink(body, "next"); onward {
		t.Errorf("the last page has a rel=next link:\n%s", body)
	}
	if _, back := pagerLink(body, "prev"); !back {
		t.Errorf("the last page has no rel=prev link:\n%s", body)
	}
}

// pagerLink returns the href of the page's rel=next or rel=prev link.
func pagerLink(body, rel string) (string, bool) {
	_, anchor, found := strings.Cut(body, `rel="`+rel+`"`)
	if !found {
		return "", false
	}
	head := body[:len(body)-len(anchor)]
	start := strings.LastIndex(head, `href="`)
	if start < 0 {
		return "", false
	}
	href, _, _ := strings.Cut(head[start+len(`href="`):], `"`)
	return strings.ReplaceAll(href, "&amp;", "&"), true
}

func TestHistoryFiltersByKind(t *testing.T) {
	d, store, userID := newPGDashboard(t)
	svc := mustDashService(t, store, userID, "CI")
	seedEvents(t, store, svc.ID, 3)

	rec := send(d, asOwner(signedIn(http.MethodGet, pathHistory+"?kind=notification", ""), userID))
	if rec.Code != http.StatusOK || strings.Count(rec.Body.String(), `class="feed__item"`) != 3 {
		t.Errorf("kind=notification: status = %d, items = %d, want 3",
			rec.Code, strings.Count(rec.Body.String(), `class="feed__item"`))
	}

	// Nothing has been answered, so the response filter is the empty state.
	rec = send(d, asOwner(signedIn(http.MethodGet, pathHistory+"?kind=response", ""), userID))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `class="feed__item"`) {
		t.Errorf("kind=response: status = %d, want 200 and no items", rec.Code)
	}
}

// TestLiveOverviewRevalidates pins the poll's economy: the same state answers
// 304 to the tag it minted, and new state mints a new tag.
func TestLiveOverviewRevalidates(t *testing.T) {
	d, store, userID := newPGDashboard(t)
	svc := mustDashService(t, store, userID, "CI")
	seedEvents(t, store, svc.ID, 2)

	rec := send(d, asOwner(signedIn(http.MethodGet, pathLiveOverview, ""), userID))
	if rec.Code != http.StatusOK {
		t.Fatalf("fragment: status = %d: %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if strings.Contains(body, "<!DOCTYPE html>") {
		t.Errorf("the fragment carries the whole page:\n%s", body)
	}
	if got := strings.Count(body, `class="feed__item"`); got != 2 {
		t.Errorf("fragment shows %d items, want 2", got)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("the fragment carries no ETag")
	}

	req := asOwner(signedIn(http.MethodGet, pathLiveOverview, ""), userID)
	req.Header.Set("If-None-Match", etag)
	if rec = send(d, req); rec.Code != http.StatusNotModified {
		t.Fatalf("unchanged fragment: status = %d, want 304", rec.Code)
	}

	// A delivery lands; the same tag no longer matches.
	seedEvents(t, store, svc.ID, 1)
	req = asOwner(signedIn(http.MethodGet, pathLiveOverview, ""), userID)
	req.Header.Set("If-None-Match", etag)
	rec = send(d, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("changed fragment: status = %d, want 200", rec.Code)
	}
	if rec.Header().Get("ETag") == etag {
		t.Error("the tag did not change with the content")
	}
}

func TestServicePageShowsItsOwnDeliveries(t *testing.T) {
	d, store, userID := newPGDashboard(t)
	svc := mustDashService(t, store, userID, "CI")
	noise := mustDashService(t, store, userID, "Backups")
	seedEvents(t, store, svc.ID, 2)
	seedEvents(t, store, noise.ID, 4)

	rec := send(d, asOwner(signedIn(http.MethodGet, pathServices+"/"+svc.ID, ""), userID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	// Only this service's rows.
	if got := strings.Count(rec.Body.String(), `data-delivery="`); got != 2 {
		t.Errorf("the page shows %d delivery rows, want the service's own 2", got)
	}
}

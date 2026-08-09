package dashboard

import (
	"net/http"
	"strings"
	"testing"
)

func TestTheContractIsServedToAnybody(t *testing.T) {
	d, _ := newTestDashboard(t)

	// No session, no CSRF cookie, nothing: the page exists so a client author
	// can read it before they have an account on this deployment at all.
	rec := send(d, request(http.MethodGet, pathDocs, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want HTML", got)
	}
	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "public") {
		t.Errorf("Cache-Control = %q, want a publicly cacheable page", got)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Errorf("the public page set cookies: %v", rec.Result().Cookies())
	}
}

func TestTheContractRendersTheMarkdown(t *testing.T) {
	d, _ := newTestDashboard(t)
	body := send(d, request(http.MethodGet, pathDocs, "")).Body.String()

	for what, want := range map[string]string{
		"the document's title":   "Hark HTTP API",
		"GFM tables":             "<table>",
		"code blocks":            "<pre>",
		"heading anchors":        `<h3 id="post-v1authlogin"`,
		"the outline":            `href="#authentication"`,
		"the Axis stylesheet":    assets.CSS,
		"the contract's own CSS": assets.Docs,
		"a link back":            pathHome,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("%s: the page does not contain %q", what, want)
		}
	}

	// Raw HTML in the source must be escaped rather than forwarded. The
	// renderer is configured without WithUnsafe, and this is the assertion
	// that notices if that ever changes.
	if strings.Contains(body, "<script>alert") {
		t.Errorf("the renderer passed through raw HTML:\n%s", body)
	}
}

// TestTheContractOutlineFollowsTheDocument keeps the sidebar honest: every
// entry has to point at a heading the page actually renders.
func TestTheContractOutlineFollowsTheDocument(t *testing.T) {
	if len(contract.Contents) < 10 {
		t.Fatalf("the outline has %d entries, want the document's sections and endpoints",
			len(contract.Contents))
	}

	body := string(contract.Body)
	for _, heading := range contract.Contents {
		if heading.ID == "" || heading.Text == "" {
			t.Errorf("outline entry %+v is incomplete", heading)
			continue
		}
		if heading.Level < tocMinLevel || heading.Level > tocMaxLevel {
			t.Errorf("outline entry %+v is at a level the sidebar does not style", heading)
		}
		if !strings.Contains(body, `id="`+heading.ID+`"`) {
			t.Errorf("the outline links #%s, which the page does not define", heading.ID)
		}
		// The outline shows plain text: a heading written as `GET /v1/events`
		// must not carry its backticks into the link.
		if strings.Contains(heading.Text, "`") {
			t.Errorf("outline entry %q was not flattened", heading.Text)
		}
	}
}

// TestTheContractsOwnCrossLinksResolve is the reason the ids are generated
// rather than hand-written: docs/api.md is full of "#post-v1tokens" anchors, and
// a renderer whose ids differ would publish a page of dead links.
func TestTheContractsOwnCrossLinksResolve(t *testing.T) {
	body := string(contract.Body)

	for _, anchor := range []string{
		"pagination", "errors", "authentication", "dashboard", "push-payloads",
		"post-v1authlogin", "post-v1tokens", "post-v1interactions",
		"post-v1interactionsidresponse", "get-v1events", "post-v1hookstoken",
		"put-v1activity-deliveriesidupdate-token", "the-answer-callback",
	} {
		if !strings.Contains(body, `id="`+anchor+`"`) {
			t.Errorf("the document links to #%s, which no heading defines", anchor)
		}
	}
}

func TestTheContractIsRevalidatedRatherThanRefetched(t *testing.T) {
	d, _ := newTestDashboard(t)

	rec := send(d, request(http.MethodGet, pathDocs, ""))
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("the page carries no ETag")
	}

	req := request(http.MethodGet, pathDocs, "")
	req.Header.Set("If-None-Match", etag)
	again := send(d, req)
	if again.Code != http.StatusNotModified {
		t.Fatalf("conditional GET: status = %d, want 304", again.Code)
	}
	if again.Body.Len() != 0 {
		t.Errorf("a 304 carried a body: %s", again.Body)
	}
}

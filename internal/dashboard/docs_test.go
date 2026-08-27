package dashboard

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	contractdocs "github.com/abdeen-labs/hark/docs"
)

func TestTheContractIsServedToAnybody(t *testing.T) {
	d, _ := newTestDashboard(t)

	// Public documentation does not require session or CSRF cookies.
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
		"GFM tables":             "<table>",
		"code blocks":            "<pre>",
		"the Axis stylesheet":    assets.CSS,
		"the contract's own CSS": assets.Docs,
		"the docs behavior":      assets.DocsJS,
		"search":                 `data-docs-search`,
		"the OpenAPI link":       pathOpenAPI,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("%s: the page does not contain %q", what, want)
		}
	}

	// Raw HTML in the Markdown source must be escaped.
	if strings.Contains(body, "<script>alert") {
		t.Errorf("the renderer passed through raw HTML:\n%s", body)
	}
}

func TestMachineReadableContractsArePublicAndVerbatim(t *testing.T) {
	d, _ := newTestDashboard(t)

	for _, tt := range []struct {
		path        string
		contentType string
		body        string
	}{
		{pathDocsMD, "text/markdown; charset=utf-8", contractdocs.APIContract},
		{pathOpenAPI, "application/vnd.oai.openapi+json;version=3.1", contractdocs.OpenAPIContract},
		{pathLLMs, "text/plain; charset=utf-8", contractdocs.LLMsContract},
	} {
		rec := send(d, request(http.MethodGet, tt.path, ""))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: status = %d, want 200", tt.path, rec.Code)
			continue
		}
		if got := rec.Header().Get("Content-Type"); got != tt.contentType {
			t.Errorf("GET %s: Content-Type = %q, want %q", tt.path, got, tt.contentType)
		}
		if got := rec.Body.String(); got != tt.body {
			t.Errorf("GET %s did not serve the embedded contract verbatim", tt.path)
		}
		if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "public") {
			t.Errorf("GET %s: Cache-Control = %q, want public", tt.path, got)
		}
		if rec.Header().Get("ETag") == "" {
			t.Errorf("GET %s has no ETag", tt.path)
		}
	}

	var spec any
	if err := json.Unmarshal([]byte(contractdocs.OpenAPIContract), &spec); err != nil {
		t.Fatalf("served OpenAPI contract is invalid JSON: %v", err)
	}
}

// TestTheContractOutlineFollowsTheDocument verifies that each outline entry
// points to a rendered heading.
func TestTheContractOutlineFollowsTheDocument(t *testing.T) {
	if len(contract.Contents) == 0 {
		t.Fatal("the rendered contract has no outline")
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
		// The outline shows plain text: a heading written as `GET /events`
		// must not carry its backticks into the link.
		if strings.Contains(heading.Text, "`") {
			t.Errorf("outline entry %q was not flattened", heading.Text)
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

package mcp

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

func TestSearchAndFetchResultsMatchOutputSchemas(t *testing.T) {
	h := newHarness(t)
	searchFixtures(h)
	h.api.answer(http.MethodGet, "/services/s1", http.StatusOK, `{"service":`+serviceRecord+`}`)
	session := h.connect(t, nil)
	list, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"search", map[string]any{"query": "deploy"}},
		{"search", map[string]any{"query": "no match"}},
		{"fetch", map[string]any{"id": "service:s1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := callTool(t, session, tc.name, tc.args)
			if res.IsError {
				t.Fatal(textOf(t, res))
			}
			body, err := json.Marshal(toolNamed(t, list, tc.name).OutputSchema)
			if err != nil {
				t.Fatal(err)
			}
			var schema jsonschema.Schema
			if err := json.Unmarshal(body, &schema); err != nil {
				t.Fatal(err)
			}
			resolved, err := schema.Resolve(nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := resolved.Validate(asJSON(t, res.StructuredContent)); err != nil {
				t.Fatalf("result does not match outputSchema: %v", err)
			}
			if err := resolved.Validate(map[string]any{}); err == nil {
				t.Error("outputSchema accepts an empty result")
			}
		})
	}
}

const (
	serviceRecord  = `{"id":"s1","title":"Deploy bot","image_url":null,"url":null,"priority":"normal","webhook_url":null}`
	eventRecord    = `{"id":"e1","service_id":"s1","service_name":"Deploy bot","title":"Deploy bot","body":"Build 4821 succeeded","status":"accepted"}`
	questionRecord = `{"id":"q1","title":"Claude Code","prompt":"Deploy v42 to production?","kind":"approval","status":"pending"}`
	activityRecord = `{"id":"a1","key":"nightly","status":"active","state":{"title":"Nightly build","status":"Running","detail":"step 3 of 7"}}`
)

func searchFixtures(h *harness) {
	h.api.answer(http.MethodGet, "/services", http.StatusOK, `{"services":[`+serviceRecord+`,{"id":"s2","title":"Backups"}]}`)
	h.api.answer(http.MethodGet, "/devices", http.StatusForbidden, `{"error":{"code":"insufficient_scope","message":"This endpoint requires devices:read."}}`)
	h.api.answer(http.MethodGet, "/events", http.StatusOK, `{"events":[`+eventRecord+`,{"id":"e2","title":"Backups","body":"Snapshot done","status":"accepted"}],"next_cursor":"more"}`)
	h.api.answer(http.MethodGet, "/interactions", http.StatusOK, `{"interactions":[`+questionRecord+`],"next_cursor":null}`)
	h.api.answer(http.MethodGet, "/activities", http.StatusOK, `{"activities":[`+activityRecord+`],"next_cursor":null}`)
}

func resultIDs(t *testing.T, structured any) []string {
	t.Helper()
	results, _ := asJSON(t, structured)["results"].([]any)
	var ids []string
	for _, r := range results {
		m, _ := r.(map[string]any)
		id, _ := m["id"].(string)
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func TestSearchLooksAcrossEveryReadableKind(t *testing.T) {
	h := newHarness(t)
	searchFixtures(h)
	session := h.connect(t, nil)

	res := callTool(t, session, "search", map[string]any{"query": "DEPLOY"})
	if res.IsError {
		t.Fatalf("isError with %s", textOf(t, res))
	}
	if got, want := resultIDs(t, res.StructuredContent), []string{"event:e1", "question:q1", "service:s1"}; !slices.Equal(got, want) {
		t.Errorf("result ids = %q, want %q", got, want)
	}
	if text := textOf(t, res); !slices.Equal(resultIDs(t, parseJSON(t, text)), resultIDs(t, res.StructuredContent)) {
		t.Error("the text block and structuredContent differ")
	}

	results, _ := asJSON(t, res.StructuredContent)["results"].([]any)
	for _, r := range results {
		m, _ := r.(map[string]any)
		id, _ := m["id"].(string)
		u, _ := m["url"].(string)
		title, _ := m["title"].(string)
		var want string
		switch id {
		case "service:s1":
			want = "https://hark.example.com/dashboard/services/s1"
		default:
			want = "https://hark.example.com/dashboard/history"
		}
		if u != want {
			t.Errorf("%s: url = %q, want %q", id, u, want)
		}
		if title == "" {
			t.Errorf("%s: title is empty", id)
		}
	}

	// Every kind was read once, paged kinds at their newest 100, and the
	// kind the token cannot read was skipped rather than failing the search.
	want := map[string]string{
		"/services":     "",
		"/devices":      "",
		"/events":       "limit=100",
		"/interactions": "limit=100&status=all",
		"/activities":   "limit=100&status=all",
	}
	seen := map[string]string{}
	for _, call := range h.api.recorded() {
		if call.Method != http.MethodGet {
			t.Errorf("search made a %s", call.Method)
		}
		seen[call.Path] = call.Query.Encode()
	}
	for path, query := range want {
		got, ok := seen[path]
		if !ok || got != query {
			t.Errorf("GET %s?%s (read %v), want ?%s", path, got, ok, query)
		}
	}
}

func TestSearchMatchesTheDocumentedFields(t *testing.T) {
	h := newHarness(t)
	searchFixtures(h)
	session := h.connect(t, nil)

	for query, want := range map[string][]string{
		"step 3":    {"activity:a1"},
		"nightly":   {"activity:a1"},
		"pending":   {"question:q1"},
		"snapshot":  {"event:e2"},
		"backups":   {"event:e2", "service:s2"},
		"no match!": {},
	} {
		res := callTool(t, session, "search", map[string]any{"query": query})
		if res.IsError {
			t.Fatalf("%q: isError with %s", query, textOf(t, res))
		}
		got := resultIDs(t, res.StructuredContent)
		if got == nil {
			got = []string{}
		}
		if !slices.Equal(got, want) {
			t.Errorf("%q: result ids = %q, want %q", query, got, want)
		}
	}
}

func TestSearchSurfacesAnUnexpectedRefusal(t *testing.T) {
	h := newHarness(t)
	searchFixtures(h)
	h.api.answer(http.MethodGet, "/events", http.StatusServiceUnavailable, `{"error":{"code":"service_unavailable","message":"PostgreSQL is unreachable."}}`)
	session := h.connect(t, nil)

	res := callTool(t, session, "search", map[string]any{"query": "deploy"})
	if !res.IsError {
		t.Fatal("a 503 while searching did not become a tool error")
	}
	if code := errorCode(t, textOf(t, res)); code != codeUnavailable {
		t.Errorf("error.code = %q, want %q", code, codeUnavailable)
	}
}

func TestFetchReadsOneRecord(t *testing.T) {
	h := newHarness(t)
	h.api.answer(http.MethodGet, "/services/s1", http.StatusOK, `{"service":`+serviceRecord+`}`)
	h.api.answer(http.MethodGet, "/devices/d1", http.StatusOK, `{"device":{"id":"d1","name":"Ali's iPhone","active":true}}`)
	h.api.answer(http.MethodGet, "/interactions/q1", http.StatusOK, `{"interaction":`+questionRecord+`}`)
	h.api.answer(http.MethodGet, "/activities/a1", http.StatusOK, `{"activity":`+activityRecord+`}`)
	session := h.connect(t, nil)

	cases := []struct {
		id, path, url, record string
	}{
		{"service:s1", "/services/s1", "https://hark.example.com/dashboard/services/s1", serviceRecord},
		{"device:d1", "/devices/d1", "https://hark.example.com/dashboard/devices", `{"id":"d1","name":"Ali's iPhone","active":true}`},
		{"question:q1", "/interactions/q1", "https://hark.example.com/dashboard/history", questionRecord},
		{"activity:a1", "/activities/a1", "https://hark.example.com/dashboard/history", activityRecord},
	}
	for i, c := range cases {
		res := callTool(t, session, "fetch", map[string]any{"id": c.id})
		if res.IsError {
			t.Fatalf("%s: isError with %s", c.id, textOf(t, res))
		}
		call := h.api.recorded()[i]
		if call.Method != http.MethodGet || call.Path != c.path {
			t.Errorf("%s: read %s %s, want GET %s", c.id, call.Method, call.Path, c.path)
		}

		doc := asJSON(t, res.StructuredContent)
		if doc["id"] != c.id {
			t.Errorf("%s: id = %v", c.id, doc["id"])
		}
		if doc["url"] != c.url {
			t.Errorf("%s: url = %v, want %s", c.id, doc["url"], c.url)
		}
		if title, _ := doc["title"].(string); title == "" {
			t.Errorf("%s: title is empty", c.id)
		}
		metadata, _ := doc["metadata"].(map[string]any)
		kind, _, _ := stringsCut(c.id)
		if metadata["kind"] != kind {
			t.Errorf("%s: metadata.kind = %v, want %s", c.id, metadata["kind"], kind)
		}
		text, _ := doc["text"].(string)
		var got, want any
		if err := json.Unmarshal([]byte(text), &got); err != nil {
			t.Fatalf("%s: text is not JSON: %v", c.id, err)
		}
		_ = json.Unmarshal([]byte(c.record), &want)
		if !equalJSON(got, want) {
			t.Errorf("%s: text = %s, want the record as rendered", c.id, text)
		}
		if textOf(t, res) == "" {
			t.Errorf("%s: no text block", c.id)
		}
	}
}

func stringsCut(id string) (kind, rest string, ok bool) {
	for i := range id {
		if id[i] == ':' {
			return id[:i], id[i+1:], true
		}
	}
	return "", id, false
}

func equalJSON(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

func TestFetchWalksTheEventListForAnEvent(t *testing.T) {
	h := newHarness(t)
	h.api.handle(http.MethodGet, "/events", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("cursor") {
		case "":
			canned(http.StatusOK, `{"events":[{"id":"e9","title":"Other"}],"next_cursor":"page2"}`)(w, r)
		case "page2":
			canned(http.StatusOK, `{"events":[`+eventRecord+`],"next_cursor":null}`)(w, r)
		default:
			canned(http.StatusUnprocessableEntity, `{"error":{"code":"validation_failed","message":"bad cursor"}}`)(w, r)
		}
	})
	session := h.connect(t, nil)

	res := callTool(t, session, "fetch", map[string]any{"id": "event:e1"})
	if res.IsError {
		t.Fatalf("isError with %s", textOf(t, res))
	}
	calls := h.api.recorded()
	if len(calls) != 2 {
		t.Fatalf("API calls = %d, want two pages", len(calls))
	}
	if q := calls[0].Query; q.Get("limit") != "100" || q.Has("cursor") {
		t.Errorf("first page query = %s", q.Encode())
	}
	if q := calls[1].Query; q.Get("cursor") != "page2" || q.Get("limit") != "100" {
		t.Errorf("second page query = %s", q.Encode())
	}
	doc := asJSON(t, res.StructuredContent)
	if doc["id"] != "event:e1" || doc["url"] != "https://hark.example.com/dashboard/history" {
		t.Errorf("document = %v", doc)
	}
	metadata, _ := doc["metadata"].(map[string]any)
	if metadata["kind"] != "event" {
		t.Errorf("metadata.kind = %v", metadata["kind"])
	}

	missing := callTool(t, session, "fetch", map[string]any{"id": "event:nope"})
	if !missing.IsError {
		t.Fatal("an unknown event was found")
	}
	if code := errorCode(t, textOf(t, missing)); code != codeNotFound {
		t.Errorf("error.code = %q, want %q", code, codeNotFound)
	}
}

func TestFetchRefusesAMalformedIdAndRelaysARefusal(t *testing.T) {
	h := newHarness(t)
	h.api.answer(http.MethodGet, "/services/s1", http.StatusForbidden, `{"error":{"code":"insufficient_scope","message":"This endpoint requires services:read."}}`)
	session := h.connect(t, nil)

	for _, id := range []string{"s1", "widget:s1", "service:", ""} {
		res := callTool(t, session, "fetch", map[string]any{"id": id})
		if !res.IsError {
			t.Errorf("%q was accepted", id)
			continue
		}
		if code := errorCode(t, textOf(t, res)); code != codeValidation {
			t.Errorf("%q: error.code = %q, want %q", id, code, codeValidation)
		}
	}
	if calls := h.api.recorded(); len(calls) != 0 {
		t.Errorf("API calls = %d, want none for malformed ids", len(calls))
	}

	res := callTool(t, session, "fetch", map[string]any{"id": "service:s1"})
	if !res.IsError {
		t.Fatal("a 403 read did not become a tool error")
	}
	if code := errorCode(t, textOf(t, res)); code != "insufficient_scope" {
		t.Errorf("error.code = %q, want the endpoint's", code)
	}
}

func TestSearchURLsFollowThePublicOrigin(t *testing.T) {
	for _, kind := range recordKinds {
		u := kind.url("https://hark.example.com", "x1")
		parsed, err := url.Parse(u)
		if err != nil || parsed.Scheme != "https" || parsed.Host != "hark.example.com" {
			t.Errorf("%s: url = %q", kind.name, u)
		}
	}
}

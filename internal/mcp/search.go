package mcp

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/url"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type searchArgs struct {
	Query string `json:"query" jsonschema:"Text to look for in titles, bodies, prompts, names and statuses. Case does not matter."`
}

type fetchArgs struct {
	ID string `json:"id" jsonschema:"A result id from search: kind:id, with kind one of service, device, event, question, activity."`
}

// Search and fetch response types are documented in docs/api.md.
type searchHit struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

type searchDocument struct {
	Results []searchHit `json:"results"`
}

type fetchDocument struct {
	ID       string            `json:"id"`
	Title    string            `json:"title"`
	Text     string            `json:"text"`
	URL      string            `json:"url"`
	Metadata map[string]string `json:"metadata"`
}

// searchPageSize is how far into each paged kind search looks: the newest
// page of the largest size the endpoints allow.
const searchPageSize = "100"

// maxFetchPages bounds the walk fetch makes through a kind that has no
// single-read endpoint.
const maxFetchPages = 10

// recordKind is one kind of record search covers, and how fetch reads one.
type recordKind struct {
	name string
	// list is the endpoint search reads; listQuery asks a paged endpoint for
	// its newest page.
	list      string
	listQuery url.Values
	// items names the array in the list response; item names the object in a
	// single read.
	items string
	item  string
	// get builds the single-read path. Nil means the kind has no single-read
	// endpoint and fetch walks the list instead.
	get func(id string) string
	url func(origin, id string) string
	// title and fields read a record as the endpoint rendered it.
	title  func(rec map[string]any) string
	fields func(rec map[string]any) []string
}

var recordKinds = []recordKind{
	{
		name:  "service",
		list:  "/services",
		items: "services",
		item:  "service",
		get:   func(id string) string { return "/services/" + url.PathEscape(id) },
		url:   func(origin, id string) string { return origin + dashboardPrefix + "/services/" + id },
		title: func(rec map[string]any) string { return str(rec, "title") },
		fields: func(rec map[string]any) []string {
			return []string{str(rec, "title")}
		},
	},
	{
		name:  "device",
		list:  "/devices",
		items: "devices",
		item:  "device",
		get:   func(id string) string { return "/devices/" + url.PathEscape(id) },
		url:   func(origin, _ string) string { return origin + dashboardPrefix + "/devices" },
		title: func(rec map[string]any) string { return label(str(rec, "name"), "") },
		fields: func(rec map[string]any) []string {
			return []string{str(rec, "name")}
		},
	},
	{
		name:      "event",
		list:      "/events",
		listQuery: url.Values{"limit": {searchPageSize}},
		items:     "events",
		url:       historyURL,
		title:     func(rec map[string]any) string { return label(str(rec, "title"), str(rec, "body")) },
		fields: func(rec map[string]any) []string {
			return []string{str(rec, "title"), str(rec, "body"), str(rec, "service_name"), str(rec, "status")}
		},
	},
	{
		name:      "question",
		list:      "/interactions",
		listQuery: url.Values{"status": {"all"}, "limit": {searchPageSize}},
		items:     "interactions",
		item:      "interaction",
		get:       func(id string) string { return "/interactions/" + url.PathEscape(id) },
		url:       historyURL,
		title:     func(rec map[string]any) string { return label(str(rec, "title"), str(rec, "prompt")) },
		fields: func(rec map[string]any) []string {
			return []string{str(rec, "title"), str(rec, "prompt"), str(rec, "status"), str(rec, "kind")}
		},
	},
	{
		name:      "activity",
		list:      "/activities",
		listQuery: url.Values{"status": {"all"}, "limit": {searchPageSize}},
		items:     "activities",
		item:      "activity",
		get:       func(id string) string { return "/activities/" + url.PathEscape(id) },
		url:       historyURL,
		title: func(rec map[string]any) string {
			state := nested(rec, "state")
			return label(str(state, "title"), str(state, "status"))
		},
		fields: func(rec map[string]any) []string {
			state := nested(rec, "state")
			return []string{
				str(rec, "key"), str(rec, "status"),
				str(state, "title"), str(state, "status"), str(state, "detail"),
			}
		},
	},
}

func historyURL(origin, _ string) string { return origin + dashboardPrefix + "/history" }

func kindNamed(name string) *recordKind {
	for i := range recordKinds {
		if recordKinds[i].name == name {
			return &recordKinds[i]
		}
	}
	return nil
}

func (s *Server) search(ctx context.Context, req *sdk.CallToolRequest, in searchArgs) (*sdk.CallToolResult, any, error) {
	needle := strings.ToLower(strings.TrimSpace(in.Query))
	header := headerOf(req)
	hits := []searchHit{}
	for i := range recordKinds {
		kind := &recordKinds[i]
		resp, err := s.call(ctx, header, apiCall{method: http.MethodGet, path: kind.list, query: kind.listQuery})
		if err != nil {
			return nil, nil, err
		}
		// Skip records outside the token's read scopes.
		if resp.status == http.StatusForbidden {
			continue
		}
		if !resp.ok() {
			return result(resp), nil, nil
		}
		items, err := listItems(resp.body, kind.items)
		if err != nil {
			return nil, nil, err
		}
		for _, raw := range items {
			rec := decodeRecord(raw)
			if !matches(needle, kind.fields(rec)) {
				continue
			}
			id := str(rec, "id")
			hits = append(hits, searchHit{
				ID:    kind.name + ":" + id,
				Title: kind.title(rec),
				URL:   kind.url(s.origin, id),
			})
		}
	}
	return document(searchDocument{Results: hits})
}

func (s *Server) fetch(ctx context.Context, req *sdk.CallToolRequest, in fetchArgs) (*sdk.CallToolResult, any, error) {
	kindName, id, found := strings.Cut(in.ID, ":")
	kind := kindNamed(kindName)
	if !found || kind == nil || id == "" {
		return toolError(codeValidation,
			"id must be kind:id, with kind one of service, device, event, question, activity."), nil, nil
	}
	header := headerOf(req)

	var raw json.RawMessage
	if kind.get != nil {
		resp, err := s.call(ctx, header, apiCall{method: http.MethodGet, path: kind.get(id)})
		if err != nil {
			return nil, nil, err
		}
		if !resp.ok() {
			return result(resp), nil, nil
		}
		raw, err = objectField(resp.body, kind.item)
		if err != nil {
			return nil, nil, err
		}
	} else {
		var refusal *sdk.CallToolResult
		var err error
		raw, refusal, err = s.findInList(ctx, header, kind, id)
		if err != nil {
			return nil, nil, err
		}
		if refusal != nil {
			return refusal, nil, nil
		}
	}

	rec := decodeRecord(raw)
	return document(fetchDocument{
		ID:       in.ID,
		Title:    kind.title(rec),
		Text:     string(raw),
		URL:      kind.url(s.origin, id),
		Metadata: map[string]string{"kind": kind.name},
	})
}

// findInList walks a kind's list, newest first, until it meets the id or runs
// out of pages to look at.
func (s *Server) findInList(ctx context.Context, header http.Header, kind *recordKind, id string) (json.RawMessage, *sdk.CallToolResult, error) {
	query := maps.Clone(kind.listQuery)
	for range maxFetchPages {
		resp, err := s.call(ctx, header, apiCall{method: http.MethodGet, path: kind.list, query: query})
		if err != nil {
			return nil, nil, err
		}
		if !resp.ok() {
			return nil, result(resp), nil
		}
		items, err := listItems(resp.body, kind.items)
		if err != nil {
			return nil, nil, err
		}
		for _, raw := range items {
			if str(decodeRecord(raw), "id") == id {
				return raw, nil, nil
			}
		}
		next := nextCursor(resp.body)
		if next == "" {
			break
		}
		query.Set("cursor", next)
	}
	return nil, toolError(codeNotFound, "No "+kind.name+" matches that id."), nil
}

// listItems returns the records of a list response, untouched.
func listItems(body []byte, key string) ([]json.RawMessage, error) {
	var res map[string]json.RawMessage
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, err
	}
	var items []json.RawMessage
	if raw, ok := res[key]; ok {
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, err
		}
	}
	return items, nil
}

// objectField returns one member of a response object, untouched.
func objectField(body []byte, key string) (json.RawMessage, error) {
	var res map[string]json.RawMessage
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, err
	}
	return res[key], nil
}

func nextCursor(body []byte) string {
	var res struct {
		NextCursor *string `json:"next_cursor"`
	}
	if err := json.Unmarshal(body, &res); err != nil || res.NextCursor == nil {
		return ""
	}
	return *res.NextCursor
}

func decodeRecord(raw json.RawMessage) map[string]any {
	var rec map[string]any
	if err := json.Unmarshal(raw, &rec); err != nil || rec == nil {
		return map[string]any{}
	}
	return rec
}

func str(rec map[string]any, key string) string {
	v, _ := rec[key].(string)
	return v
}

func nested(rec map[string]any, key string) map[string]any {
	v, _ := rec[key].(map[string]any)
	if v == nil {
		return map[string]any{}
	}
	return v
}

// label joins a record's two identifying strings, tolerating either being
// absent.
func label(primary, secondary string) string {
	switch {
	case secondary == "":
		return primary
	case primary == "":
		return secondary
	default:
		return primary + ": " + secondary
	}
}

// matches reports whether any field contains the lower-cased needle. An
// empty needle matches everything.
func matches(needle string, fields []string) bool {
	if needle == "" {
		return true
	}
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), needle) {
			return true
		}
	}
	return false
}

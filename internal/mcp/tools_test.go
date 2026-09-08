package mcp

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestToolsListPublishesTheFifteenTools(t *testing.T) {
	h := newHarness(t)
	session := h.connect(t, nil)
	list, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	required := map[string][]string{
		"send_notification":    {"body"},
		"ask_question":         {"kind", "prompt", "title"},
		"get_question":         {"id"},
		"list_questions":       {},
		"cancel_question":      {"id"},
		"start_live_activity":  {"status", "title"},
		"update_live_activity": {"identifier"},
		"end_live_activity":    {"identifier"},
		"get_live_activity":    {"identifier"},
		"list_live_activities": {},
		"list_devices":         {},
		"list_services":        {},
		"list_webhook_events":  {},
		"search":               {"query"},
		"fetch":                {"id"},
	}
	readOnlyTools := []string{
		"get_question", "list_questions", "get_live_activity", "list_live_activities",
		"list_devices", "list_services", "list_webhook_events", "search", "fetch",
	}

	var names []string
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
		want, known := required[tool.Name]
		if !known {
			t.Errorf("unexpected tool %q", tool.Name)
			continue
		}
		schema := asJSON(t, tool.InputSchema)
		if got := stringsOf(schema["required"]); !slices.Equal(got, want) {
			t.Errorf("%s: required = %q, want %q", tool.Name, got, want)
		}
		if schema["type"] != "object" {
			t.Errorf("%s: inputSchema.type = %v, want object", tool.Name, schema["type"])
		}
		if tool.Title == "" || tool.Description == "" {
			t.Errorf("%s: title or description is empty", tool.Name)
		}
		if tool.Annotations == nil {
			t.Errorf("%s: no annotations", tool.Name)
			continue
		}
		if want := slices.Contains(readOnlyTools, tool.Name); tool.Annotations.ReadOnlyHint != want {
			t.Errorf("%s: readOnlyHint = %v, want %v", tool.Name, tool.Annotations.ReadOnlyHint, want)
		}
		mutatesExisting := slices.Contains([]string{"start_live_activity", "update_live_activity", "end_live_activity", "cancel_question"}, tool.Name)
		if !tool.Annotations.ReadOnlyHint && (tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint != mutatesExisting) {
			t.Errorf("%s: destructiveHint must reflect changes to existing state", tool.Name)
		}
		if tool.Name == "end_live_activity" && tool.Annotations.IdempotentHint {
			t.Error("end_live_activity can resolve a reused key to a new activity and is not unconditionally idempotent")
		}
		if tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Errorf("%s: openWorldHint is not false", tool.Name)
		}
	}
	slices.Sort(names)
	wantNames := make([]string, 0, len(required))
	for name := range required {
		wantNames = append(wantNames, name)
	}
	slices.Sort(wantNames)
	if !slices.Equal(names, wantNames) {
		t.Errorf("tools = %q, want %q", names, wantNames)
	}

	// The fields an update may clear admit null; a plain field does not.
	update := toolNamed(t, list, "update_live_activity")
	props, _ := asJSON(t, update.InputSchema)["properties"].(map[string]any)
	for _, field := range []string{"detail", "progress"} {
		prop, _ := props[field].(map[string]any)
		if types := stringsOf(prop["type"]); !slices.Contains(types, "null") {
			t.Errorf("update_live_activity.%s type = %v, want to include null", field, prop["type"])
		}
	}
	title, _ := props["title"].(map[string]any)
	if title["type"] != "string" {
		t.Errorf("update_live_activity.title type = %v, want string", title["type"])
	}
}

func toolNamed(t *testing.T, list *sdk.ListToolsResult, name string) *sdk.Tool {
	t.Helper()
	for _, tool := range list.Tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("no tool named %q", name)
	return nil
}

// stringsOf reads a JSON array of strings, or a lone string, sorted.
func stringsOf(v any) []string {
	var out []string
	switch v := v.(type) {
	case []any:
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
	case string:
		out = append(out, v)
	}
	slices.Sort(out)
	if out == nil {
		out = []string{}
	}
	return out
}

func TestSendNotificationForwardsThePost(t *testing.T) {
	h := newHarness(t)
	const created = `{"notification":{"id":"n1","title":"Deploy","body":"v42 is live.","priority":"normal","accepted_count":1},"replayed":false,"message":null}`
	h.api.answer(http.MethodPost, "/notifications", http.StatusCreated, created)
	session := h.connect(t, nil)

	res := callTool(t, session, "send_notification", map[string]any{
		"title":           "Deploy",
		"body":            "v42 is live.",
		"idempotency_key": "deploy-42",
	})
	if res.IsError {
		t.Fatalf("isError with %s", textOf(t, res))
	}

	calls := h.api.recorded()
	if len(calls) != 1 {
		t.Fatalf("API calls = %d, want 1", len(calls))
	}
	call := calls[0]
	if call.Method != http.MethodPost || call.Path != "/notifications" {
		t.Errorf("forwarded %s %s, want POST /notifications", call.Method, call.Path)
	}
	for name, want := range map[string]string{
		"Authorization":   "Bearer " + testToken,
		"Idempotency-Key": "deploy-42",
		"Content-Type":    "application/json",
		"Accept":          "application/json",
	} {
		if got := call.Header.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(call.Body, &body); err != nil {
		t.Fatalf("body %s: %v", call.Body, err)
	}
	if _, leaked := body["idempotency_key"]; leaked {
		t.Error("idempotency_key reached the endpoint's body")
	}
	if string(body["body"]) != `"v42 is live."` || string(body["title"]) != `"Deploy"` {
		t.Errorf("body = %s", call.Body)
	}

	if got := textOf(t, res); got != created {
		t.Errorf("text = %s, want the response body", got)
	}
	structured := asJSON(t, res.StructuredContent)
	notification, _ := structured["notification"].(map[string]any)
	if notification["id"] != "n1" {
		t.Errorf("structuredContent = %v, want the response", structured)
	}
}

func TestANullArgumentIsForwardedAsNull(t *testing.T) {
	h := newHarness(t)
	h.api.answer(http.MethodPatch, "/activities/deploy", http.StatusOK, `{"activity":{"id":"a1","key":"deploy","sequence":2}}`)
	session := h.connect(t, nil)

	res := callTool(t, session, "update_live_activity", map[string]any{
		"identifier": "deploy",
		"status":     "Done",
		"detail":     nil,
		"progress":   nil,
	})
	if res.IsError {
		t.Fatalf("isError with %s", textOf(t, res))
	}
	calls := h.api.recorded()
	if len(calls) != 1 {
		t.Fatalf("API calls = %d, want 1", len(calls))
	}
	if calls[0].Method != http.MethodPatch || calls[0].Path != "/activities/deploy" {
		t.Errorf("forwarded %s %s, want PATCH /activities/deploy", calls[0].Method, calls[0].Path)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatalf("body %s: %v", calls[0].Body, err)
	}
	for _, field := range []string{"detail", "progress"} {
		if got, present := body[field]; !present || string(got) != "null" {
			t.Errorf("%s = %s (present %v), want an explicit null", field, got, present)
		}
	}
	if _, leaked := body["identifier"]; leaked {
		t.Error("identifier reached the endpoint's body")
	}
	if string(body["status"]) != `"Done"` {
		t.Errorf("status = %s, want a body field to survive", body["status"])
	}
}

func TestAnAPIRefusalIsAToolError(t *testing.T) {
	h := newHarness(t)
	const refusal = `{"error":{"code":"validation_failed","message":"The request body is invalid.","fields":[{"field":"body","message":"must be 1-2000 characters"}]}}`
	h.api.answer(http.MethodPost, "/notifications", http.StatusUnprocessableEntity, refusal)
	session := h.connect(t, nil)

	res := callTool(t, session, "send_notification", map[string]any{"body": ""})
	if !res.IsError {
		t.Fatal("a 422 did not become a tool error")
	}
	text := textOf(t, res)
	if code := errorCode(t, text); code != "validation_failed" {
		t.Errorf("error.code = %q, want validation_failed", code)
	}
	env, _ := parseJSON(t, text)["error"].(map[string]any)
	if fields, _ := env["fields"].([]any); len(fields) != 1 {
		t.Errorf("error.fields = %v, want the endpoint's", env["fields"])
	}
}

func TestAnArgumentTheSchemaRejectsNeverReachesTheEndpoint(t *testing.T) {
	h := newHarness(t)
	h.api.answer(http.MethodPost, "/notifications", http.StatusCreated, `{"notification":{"id":"n1"}}`)
	session := h.connect(t, nil)

	for name, args := range map[string]map[string]any{
		"unknown field": {"body": "x", "bogus": 1},
		"wrong type":    {"body": 42},
		"missing body":  {"title": "x"},
	} {
		t.Run(name, func(t *testing.T) {
			res := callTool(t, session, "send_notification", args)
			if !res.IsError {
				t.Error("the call succeeded")
			}
		})
	}
	if calls := h.api.recorded(); len(calls) != 0 {
		t.Errorf("API calls = %d, want none", len(calls))
	}
}

func TestAskQuestionWaitsForTheAnswer(t *testing.T) {
	const created = `{"interaction":{"id":"q1","status":"pending","response":null},"accepted":1,"activity_id":null,"replayed":false,"message":null}`
	const answered = `{"interaction":{"id":"q1","status":"approved","response":"approve"}}`
	ask := map[string]any{
		"title":           "Claude Code",
		"prompt":          "Deploy the release?",
		"kind":            "approval",
		"wait_seconds":    5,
		"idempotency_key": "deploy-1",
	}

	t.Run("answered in time", func(t *testing.T) {
		h := newHarness(t)
		h.api.answer(http.MethodPost, "/interactions", http.StatusCreated, created)
		h.api.answer(http.MethodGet, "/interactions/q1", http.StatusOK, answered)
		session := h.connect(t, nil)

		res := callTool(t, session, "ask_question", ask)
		if res.IsError {
			t.Fatalf("isError with %s", textOf(t, res))
		}
		calls := h.api.recorded()
		if len(calls) != 2 {
			t.Fatalf("API calls = %d, want the create and the read", len(calls))
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(calls[0].Body, &body); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"wait_seconds", "idempotency_key"} {
			if _, leaked := body[key]; leaked {
				t.Errorf("%s reached the endpoint's body", key)
			}
		}
		if calls[0].Header.Get("Idempotency-Key") != "deploy-1" {
			t.Errorf("Idempotency-Key = %q", calls[0].Header.Get("Idempotency-Key"))
		}
		read := calls[1]
		if read.Method != http.MethodGet || read.Path != "/interactions/q1" || read.Query.Get("wait_seconds") != "5" {
			t.Errorf("follow-up = %s %s?%s, want GET /interactions/q1?wait_seconds=5", read.Method, read.Path, read.Query.Encode())
		}
		if read.Header.Get("Authorization") != "Bearer "+testToken {
			t.Error("the follow-up read did not carry the token")
		}

		structured := asJSON(t, res.StructuredContent)
		interaction, _ := structured["interaction"].(map[string]any)
		if interaction["status"] != "approved" {
			t.Errorf("interaction.status = %v, want the fresh state", interaction["status"])
		}
		if structured["accepted"] != float64(1) {
			t.Errorf("accepted = %v, want the create response's fields kept", structured["accepted"])
		}
	})

	t.Run("no read scope", func(t *testing.T) {
		h := newHarness(t)
		h.api.answer(http.MethodPost, "/interactions", http.StatusCreated, created)
		h.api.answer(http.MethodGet, "/interactions/q1", http.StatusForbidden,
			`{"error":{"code":"insufficient_scope","message":"This endpoint requires interactions:read."}}`)
		session := h.connect(t, nil)

		res := callTool(t, session, "ask_question", ask)
		if res.IsError {
			t.Fatalf("a refused wait failed the tool: %s", textOf(t, res))
		}
		interaction, _ := asJSON(t, res.StructuredContent)["interaction"].(map[string]any)
		if interaction["status"] != "pending" {
			t.Errorf("interaction.status = %v, want the create response", interaction["status"])
		}
	})

	t.Run("no wait", func(t *testing.T) {
		h := newHarness(t)
		h.api.answer(http.MethodPost, "/interactions", http.StatusCreated, created)
		session := h.connect(t, nil)

		callTool(t, session, "ask_question", map[string]any{"title": "T", "prompt": "P", "kind": "yes_no"})
		if calls := h.api.recorded(); len(calls) != 1 {
			t.Errorf("API calls = %d, want the create alone", len(calls))
		}
	})

	t.Run("replayed", func(t *testing.T) {
		h := newHarness(t)
		h.api.answer(http.MethodPost, "/interactions", http.StatusOK, created)
		h.api.answer(http.MethodGet, "/interactions/q1", http.StatusOK, answered)
		session := h.connect(t, nil)

		res := callTool(t, session, "ask_question", ask)
		if calls := h.api.recorded(); len(calls) != 2 {
			t.Errorf("API calls = %d, want the replay and requested read", len(calls))
		}
		interaction, _ := asJSON(t, res.StructuredContent)["interaction"].(map[string]any)
		if res.IsError || interaction["status"] != "approved" {
			t.Errorf("replayed question = %s, want the current answer", textOf(t, res))
		}
	})

	t.Run("wait out of range", func(t *testing.T) {
		h := newHarness(t)
		session := h.connect(t, nil)

		res := callTool(t, session, "ask_question", map[string]any{
			"title": "T", "prompt": "P", "kind": "yes_no", "wait_seconds": 26,
		})
		if !res.IsError {
			t.Fatal("wait_seconds 26 was accepted")
		}
		if code := errorCode(t, textOf(t, res)); code != codeValidation {
			t.Errorf("error.code = %q, want %q", code, codeValidation)
		}
		if calls := h.api.recorded(); len(calls) != 0 {
			t.Errorf("API calls = %d, want none", len(calls))
		}
	})
}

func TestReadsCarryTheirQueryParameters(t *testing.T) {
	h := newHarness(t)
	h.api.answer(http.MethodGet, "/interactions", http.StatusOK, `{"interactions":[],"next_cursor":null}`)
	h.api.answer(http.MethodGet, "/interactions/q1", http.StatusOK, `{"interaction":{"id":"q1"}}`)
	h.api.answer(http.MethodGet, "/activities", http.StatusOK, `{"activities":[],"next_cursor":null}`)
	h.api.answer(http.MethodGet, "/events", http.StatusOK, `{"events":[],"next_cursor":null}`)
	h.api.answer(http.MethodGet, "/activities/deploy", http.StatusOK, `{"activity":{"id":"a1"}}`)
	session := h.connect(t, nil)

	cases := []struct {
		tool  string
		args  map[string]any
		path  string
		query string
	}{
		{"list_questions", map[string]any{"status": "all", "limit": 5, "cursor": "abc"}, "/interactions", "cursor=abc&limit=5&status=all"},
		{"list_questions", map[string]any{}, "/interactions", ""},
		{"get_question", map[string]any{"id": "q1", "wait_seconds": 10}, "/interactions/q1", "wait_seconds=10"},
		{"get_question", map[string]any{"id": "q1"}, "/interactions/q1", ""},
		{"list_live_activities", map[string]any{"status": "all", "limit": 100}, "/activities", "limit=100&status=all"},
		{"list_webhook_events", map[string]any{"limit": 3, "cursor": "xyz"}, "/events", "cursor=xyz&limit=3"},
		{"get_live_activity", map[string]any{"identifier": "deploy"}, "/activities/deploy", ""},
	}
	for i, c := range cases {
		res := callTool(t, session, c.tool, c.args)
		if res.IsError {
			t.Fatalf("%s: isError with %s", c.tool, textOf(t, res))
		}
		call := h.api.recorded()[i]
		if call.Method != http.MethodGet || call.Path != c.path || call.Query.Encode() != c.query {
			t.Errorf("%s(%v) = %s %s?%s, want GET %s?%s", c.tool, c.args, call.Method, call.Path, call.Query.Encode(), c.path, c.query)
		}
		if len(call.Body) != 0 || call.Header.Get("Content-Type") != "" {
			t.Errorf("%s sent a body", c.tool)
		}
	}
}

func TestCancelQuestionPostsWithoutABody(t *testing.T) {
	h := newHarness(t)
	h.api.answer(http.MethodPost, "/interactions/q1/cancel", http.StatusOK, `{"interaction":{"id":"q1","status":"canceled"}}`)
	session := h.connect(t, nil)

	res := callTool(t, session, "cancel_question", map[string]any{"id": "q1"})
	if res.IsError {
		t.Fatalf("isError with %s", textOf(t, res))
	}
	calls := h.api.recorded()
	if len(calls) != 1 {
		t.Fatalf("API calls = %d, want 1", len(calls))
	}
	if calls[0].Method != http.MethodPost || calls[0].Path != "/interactions/q1/cancel" {
		t.Errorf("forwarded %s %s", calls[0].Method, calls[0].Path)
	}
	if len(calls[0].Body) != 0 || calls[0].Header.Get("Content-Type") != "" {
		t.Errorf("cancel sent a body %q", calls[0].Body)
	}
}

func TestActivityToolsAddressTheIdentifierInThePath(t *testing.T) {
	h := newHarness(t)
	h.api.answer(http.MethodPost, "/activities", http.StatusCreated, `{"activity":{"id":"a1","sequence":0}}`)
	h.api.answer(http.MethodPost, "/activities/a1/end", http.StatusOK, `{"activity":{"id":"a1","status":"ended"}}`)
	session := h.connect(t, nil)

	callTool(t, session, "start_live_activity", map[string]any{
		"title": "Deploy", "status": "Building", "key": "deploy", "idempotency_key": "start-1",
	})
	callTool(t, session, "end_live_activity", map[string]any{
		"identifier": "a1", "status": "Complete", "dismiss_after_seconds": 60, "idempotency_key": "end-1",
	})

	calls := h.api.recorded()
	if len(calls) != 2 {
		t.Fatalf("API calls = %d, want 2", len(calls))
	}
	start := calls[0]
	var startBody map[string]json.RawMessage
	if err := json.Unmarshal(start.Body, &startBody); err != nil {
		t.Fatal(err)
	}
	if string(startBody["status"]) != `"Building"` || string(startBody["key"]) != `"deploy"` {
		t.Errorf("start body = %s, want status and key kept", start.Body)
	}
	if _, leaked := startBody["idempotency_key"]; leaked || start.Header.Get("Idempotency-Key") != "start-1" {
		t.Error("the start's idempotency key did not travel as a header")
	}

	end := calls[1]
	if end.Method != http.MethodPost || end.Path != "/activities/a1/end" {
		t.Errorf("end = %s %s, want POST /activities/a1/end", end.Method, end.Path)
	}
	var endBody map[string]json.RawMessage
	if err := json.Unmarshal(end.Body, &endBody); err != nil {
		t.Fatal(err)
	}
	if _, leaked := endBody["identifier"]; leaked {
		t.Error("identifier reached the end body")
	}
	if string(endBody["dismiss_after_seconds"]) != "60" || string(endBody["status"]) != `"Complete"` {
		t.Errorf("end body = %s", end.Body)
	}
	if end.Header.Get("Idempotency-Key") != "end-1" {
		t.Errorf("Idempotency-Key = %q", end.Header.Get("Idempotency-Key"))
	}
}

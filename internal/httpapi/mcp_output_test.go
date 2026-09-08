package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/mcp"
)

type schemaTokenResolver struct{}

func (schemaTokenResolver) AuthenticateAPIToken(context.Context, string) (*auth.Principal, error) {
	return &auth.Principal{}, nil
}

// Exercise the published schemas against the actual response DTOs without a
// database. This catches drift in field names, nullability and optional fields.
func TestMCPOutputSchemasMatchAPIResponses(t *testing.T) {
	server := mcp.New(mcp.Options{
		API: http.NotFoundHandler(), Resolver: schemaTokenResolver{},
		PublicURL: &url.URL{Scheme: "https", Host: "hark.example.com"},
	})
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	req.Header.Set("Authorization", "Bearer hark_schema_test")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	var listed struct {
		Result struct {
			Tools []struct {
				Name         string             `json:"name"`
				OutputSchema *jsonschema.Schema `json:"outputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil || len(listed.Result.Tools) != 15 {
		t.Fatalf("tools/list: %v: %s", err, rec.Body)
	}
	schemas := map[string]*jsonschema.Schema{}
	for _, tool := range listed.Result.Tools {
		schemas[tool.Name] = tool.OutputSchema
	}

	question := interactionDTO{Choices: []string{}}
	answer := "approve"
	answered := question
	answered.Status = "approved"
	answered.Response = &answer
	answered.RespondedAt = new(Timestamp)
	state, err := encodeActivityState(activityState{})
	if err != nil {
		t.Fatal(err)
	}
	activity := activityDTO{State: state}
	detail, progress, replaced := "Done", 1.0, 1
	fullState, err := encodeActivityState(activityState{
		Detail: &detail, Progress: &progress, Interaction: &activityInteractionState{},
	})
	if err != nil {
		t.Fatal(err)
	}
	fullActivity := activityDTO{State: fullState, Key: &detail, EndedAt: new(Timestamp)}

	cases := []struct {
		tool  string
		value any
	}{
		{"send_notification", notificationResponse{}},
		{"ask_question", interactionResponse{Interaction: question}},
		{"ask_question", interactionResponse{Interaction: answered, ActivityID: &detail}},
		{"get_question", interactionReadResponse{Interaction: question}},
		{"cancel_question", interactionReadResponse{Interaction: question}},
		{"list_questions", interactionListResponse{Interactions: []interactionListItemDTO{{interactionDTO: question}}}},
		{"list_questions", interactionListResponse{Interactions: []interactionListItemDTO{}, NextCursor: &detail}},
		{"start_live_activity", activityResponse{Activity: activity}},
		{"start_live_activity", activityResponse{Activity: fullActivity, Replaced: &replaced}},
		{"update_live_activity", activityResponse{Activity: fullActivity}},
		{"end_live_activity", activityResponse{Activity: fullActivity}},
		{"get_live_activity", activityReadResponse{Activity: activity}},
		{"list_live_activities", activityListResponse{Activities: []activityListItemDTO{{activityDTO: activity}}}},
		{"list_devices", deviceListResponse{Devices: []deviceDTO{{}}}},
		{"list_services", serviceListResponse{Services: []serviceDTO{{}}}},
		{"list_webhook_events", eventListResponse{Events: []eventDTO{{}}}},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			schema := schemas[tc.tool]
			if schema == nil {
				t.Fatal("missing outputSchema")
			}
			resolved, err := schema.Resolve(nil)
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			var value any
			if err := json.Unmarshal(body, &value); err != nil {
				t.Fatal(err)
			}
			if err := resolved.Validate(value); err != nil {
				t.Fatalf("API response does not match outputSchema: %v\n%s", err, body)
			}
			assertOutputFieldsDocumented(t, schema, value)
			if err := resolved.Validate(map[string]any{}); err == nil {
				t.Error("outputSchema accepts an empty result with no response envelope")
			}
		})
	}
}

// Validation permits additive fields, but the contract test also requires that
// every field the current API emits is documented for models to discover.
func assertOutputFieldsDocumented(t *testing.T, schema *jsonschema.Schema, value any) {
	t.Helper()
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			property := schema.Properties[key]
			if property == nil {
				t.Errorf("response field %q has no output schema", key)
				continue
			}
			assertOutputFieldsDocumented(t, property, child)
		}
	case []any:
		if schema.Items == nil {
			t.Fatal("array has no item schema")
		}
		for _, child := range value {
			assertOutputFieldsDocumented(t, schema.Items, child)
		}
	}
}

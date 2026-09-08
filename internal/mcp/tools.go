package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// instructions is what a client shows the model about this server.
const instructions = "Hark reaches the owner's iPhone. " +
	"Use send_notification for a one-way alert that needs no reply. " +
	"Use ask_question when you need the owner's decision before continuing: " +
	"set wait_seconds (up to 25) to block for the answer in the same call, " +
	"or poll with get_question. " +
	"Use start_live_activity, update_live_activity and end_live_activity to show " +
	"the progress of a long-running job on the Lock Screen. " +
	"Use search and fetch to look up services, devices, webhook events, questions " +
	"and Live Activities; the list_* tools read them directly. " +
	"Every result is the API's own JSON. An error result carries the API's error " +
	"envelope, whose code says what to correct."

// maxWaitSeconds is the longest a read may hold for an answer, as
// GET /interactions/{id} allows.
const maxWaitSeconds = 25

// Argument sets. A field without omitempty is a required argument; a pointer
// field admits an explicit null, which the endpoint reads as "remove". The
// descriptions carry the enum values and ranges the endpoint enforces, since
// the endpoint is what validates them.

type sendNotificationArgs struct {
	Body           string   `json:"body" jsonschema:"The notification text, 1-2000 characters."`
	Title          string   `json:"title,omitempty" jsonschema:"Shown as the sender, 1-80 characters. Defaults to Hark."`
	ImageURL       string   `json:"image_url,omitempty" jsonschema:"Public HTTPS URL of an image to show."`
	URL            string   `json:"url,omitempty" jsonschema:"Where a tap on the notification goes."`
	Priority       string   `json:"priority,omitempty" jsonschema:"normal (default) or time_sensitive."`
	DeviceIDs      []string `json:"device_ids,omitempty" jsonschema:"Device ids to send to, 1-50. Absent means every reachable device."`
	IdempotencyKey string   `json:"idempotency_key,omitempty" jsonschema:"1-200 characters. Repeating a call with the same key and arguments returns the first outcome instead of sending again."`
}

type askQuestionArgs struct {
	Title            string   `json:"title" jsonschema:"Who is asking, 1-80 characters."`
	Prompt           string   `json:"prompt" jsonschema:"The question, 1-2000 characters; at most 240 for a Lock Screen card."`
	Kind             string   `json:"kind" jsonschema:"approval (answered approve or deny), yes_no (yes or no), or reply (free text)."`
	Presentation     string   `json:"presentation,omitempty" jsonschema:"notification (default) or live_activity, a Lock Screen card that can be answered without unlocking."`
	Style            string   `json:"style,omitempty" jsonschema:"Lock Screen layout: approval (default), shell, verdict or signal. Requires presentation live_activity."`
	PrimaryLabel     string   `json:"primary_label,omitempty" jsonschema:"Label of the first button, 1-24 characters. Requires presentation live_activity."`
	SecondaryLabel   string   `json:"secondary_label,omitempty" jsonschema:"Label of the second button, 1-24 characters. Requires presentation live_activity."`
	ImageURL         string   `json:"image_url,omitempty" jsonschema:"Public HTTPS URL of an image to show. Not available on a Lock Screen card."`
	URL              string   `json:"url,omitempty" jsonschema:"Where a tap on the notification goes. Not available on a Lock Screen card."`
	Priority         string   `json:"priority,omitempty" jsonschema:"normal (default) or time_sensitive."`
	DeviceIDs        []string `json:"device_ids,omitempty" jsonschema:"Device ids to ask, 1-50. Absent means every reachable device."`
	ExpiresInSeconds int      `json:"expires_in_seconds,omitempty" jsonschema:"How long the question can be answered, 30-86400 (default 900); at most 28800 for a Lock Screen card."`
	IdempotencyKey   string   `json:"idempotency_key,omitempty" jsonschema:"1-200 characters. Repeating a call with the same key and arguments returns the first outcome instead of asking again."`
	WaitSeconds      int      `json:"wait_seconds,omitempty" jsonschema:"0-25 (default 0). Hold the call open this long for the answer; the result's interaction is the freshest state. Needs the interactions:read scope."`
}

type getQuestionArgs struct {
	ID          string `json:"id" jsonschema:"The question's id."`
	WaitSeconds int    `json:"wait_seconds,omitempty" jsonschema:"0-25 (default 0). Hold the call open until the question is answered or the wait runs out."`
}

type listQuestionsArgs struct {
	Status string `json:"status,omitempty" jsonschema:"pending (default): still awaiting an answer. all: every question, newest first."`
	Limit  int    `json:"limit,omitempty" jsonschema:"Items per page, 1-100 (default 20)."`
	Cursor string `json:"cursor,omitempty" jsonschema:"The next_cursor of the previous page."`
}

type cancelQuestionArgs struct {
	ID string `json:"id" jsonschema:"The question's id."`
}

type startLiveActivityArgs struct {
	Key               string   `json:"key,omitempty" jsonschema:"A stable handle for this run, 1-100 characters, free again once it ends. Later calls may address the activity by it."`
	Replace           bool     `json:"replace,omitempty" jsonschema:"End activities that conflict on device slot or key instead of failing. Default false."`
	Title             string   `json:"title" jsonschema:"1-80 characters."`
	Status            string   `json:"status" jsonschema:"The current status line, 1-60 characters."`
	Detail            string   `json:"detail,omitempty" jsonschema:"A second line, 1-240 characters."`
	Progress          float64  `json:"progress,omitempty" jsonschema:"0.0-1.0."`
	Symbol            string   `json:"symbol,omitempty" jsonschema:"terminal (default), code, build, success or warning."`
	PrivacyMode       string   `json:"privacy_mode,omitempty" jsonschema:"standard (default) or private, which redacts the banner announcing the start."`
	AccentColor       string   `json:"accent_color,omitempty" jsonschema:"#RRGGBB, default #E64949."`
	Style             string   `json:"style,omitempty" jsonschema:"standard (default), ring, hero, terminal or steps."`
	DeviceIDs         []string `json:"device_ids,omitempty" jsonschema:"Device ids to show it on, 1-50. Absent means every capable device."`
	ExpiresInSeconds  int      `json:"expires_in_seconds,omitempty" jsonschema:"60-28800 (default 28800)."`
	StaleAfterSeconds int      `json:"stale_after_seconds,omitempty" jsonschema:"0-28800 (default 14400). When the card is shown as stale."`
	IdempotencyKey    string   `json:"idempotency_key,omitempty" jsonschema:"1-200 characters. Repeating a call with the same key and arguments returns the first outcome instead of starting again."`
}

type updateLiveActivityArgs struct {
	Identifier        string   `json:"identifier" jsonschema:"The activity's id or key."`
	Title             string   `json:"title,omitempty" jsonschema:"1-80 characters."`
	Status            string   `json:"status,omitempty" jsonschema:"The current status line, 1-60 characters."`
	Detail            *string  `json:"detail,omitempty" jsonschema:"A second line, 1-240 characters. null removes it."`
	Progress          *float64 `json:"progress,omitempty" jsonschema:"0.0-1.0. null removes it."`
	Symbol            string   `json:"symbol,omitempty" jsonschema:"terminal, code, build, success or warning."`
	PrivacyMode       string   `json:"privacy_mode,omitempty" jsonschema:"standard or private."`
	AccentColor       string   `json:"accent_color,omitempty" jsonschema:"#RRGGBB."`
	Style             string   `json:"style,omitempty" jsonschema:"standard, ring, hero, terminal or steps."`
	StaleAfterSeconds int      `json:"stale_after_seconds,omitempty" jsonschema:"0-28800. Restarts the staleness window; omitted, the existing duration is measured again from now."`
	IfSequence        int      `json:"if_sequence,omitempty" jsonschema:"Apply only if the activity is still at this sequence, as the last read reported it."`
	IdempotencyKey    string   `json:"idempotency_key,omitempty" jsonschema:"1-200 characters. Repeating a call with the same key and arguments returns the first outcome instead of updating again."`
}

type endLiveActivityArgs struct {
	Identifier          string   `json:"identifier" jsonschema:"The activity's id or key."`
	Status              string   `json:"status,omitempty" jsonschema:"The final status line, 1-60 characters (default Complete)."`
	Detail              *string  `json:"detail,omitempty" jsonschema:"A second line, 1-240 characters. null removes it."`
	Progress            *float64 `json:"progress,omitempty" jsonschema:"0.0-1.0. null removes it."`
	Symbol              string   `json:"symbol,omitempty" jsonschema:"terminal, code, build, success (default) or warning."`
	AccentColor         string   `json:"accent_color,omitempty" jsonschema:"#RRGGBB. Omitted keeps the current colour."`
	DismissAfterSeconds int      `json:"dismiss_after_seconds,omitempty" jsonschema:"0-14400 (default 0). How long the finished card stays on screen."`
	IfSequence          int      `json:"if_sequence,omitempty" jsonschema:"Apply only if the activity is still at this sequence, as the last read reported it."`
	IdempotencyKey      string   `json:"idempotency_key,omitempty" jsonschema:"1-200 characters. Repeating a call with the same key and arguments returns the first outcome instead of ending again."`
}

type getLiveActivityArgs struct {
	Identifier string `json:"identifier" jsonschema:"The activity's id or key. A key prefers the running activity, then the newest."`
}

type listLiveActivitiesArgs struct {
	Status string `json:"status,omitempty" jsonschema:"live (default): on a Lock Screen right now. all: including finished ones."`
	Limit  int    `json:"limit,omitempty" jsonschema:"Items per page, 1-100 (default 20)."`
	Cursor string `json:"cursor,omitempty" jsonschema:"The next_cursor of the previous page."`
}

type listWebhookEventsArgs struct {
	Limit  int    `json:"limit,omitempty" jsonschema:"Items per page, 1-100 (default 20)."`
	Cursor string `json:"cursor,omitempty" jsonschema:"The next_cursor of the previous page."`
}

type noArgs struct{}

func ptr(b bool) *bool { return &b }

// Annotations. Nothing here deletes: a cancel or an end settles a record that
// stays readable, so no tool is marked destructive.
func readOnly() *sdk.ToolAnnotations {
	return &sdk.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(false)}
}

func additive() *sdk.ToolAnnotations {
	return &sdk.ToolAnnotations{DestructiveHint: ptr(false), OpenWorldHint: ptr(false)}
}

func settling() *sdk.ToolAnnotations {
	return &sdk.ToolAnnotations{DestructiveHint: ptr(false), IdempotentHint: true, OpenWorldHint: ptr(false)}
}

func (s *Server) addTools(server *sdk.Server) {
	sdk.AddTool(server, &sdk.Tool{
		Name:  "send_notification",
		Title: "Send a notification",
		Description: "Send a one-shot push notification to the owner's iPhone. " +
			"Use it for one-way alerts that need no reply: a job finished, a threshold was crossed, " +
			"something needs a look. Returns the notification and how many devices accepted it.",
		Annotations: additive(),
	}, s.sendNotification)

	sdk.AddTool(server, &sdk.Tool{
		Name:  "ask_question",
		Title: "Ask a question",
		Description: "Ask the owner a question on their iPhone and, optionally, wait for the answer. " +
			"Use it when you need a decision before continuing: kind approval is answered approve or deny, " +
			"yes_no is answered yes or no, reply takes free text. Set wait_seconds to block for the answer " +
			"in the same call; otherwise poll with get_question. The result's interaction carries status " +
			"and response, and expires_at says when to stop waiting.",
		Annotations: additive(),
	}, s.askQuestion)

	sdk.AddTool(server, &sdk.Tool{
		Name:  "get_question",
		Title: "Read a question",
		Description: "Read one question by id, optionally holding the call open until it is answered. " +
			"Use it to poll for an answer after ask_question, or to check whether a question is still pending. " +
			"Only questions this token asked are visible.",
		Annotations: readOnly(),
	}, s.getQuestion)

	sdk.AddTool(server, &sdk.Tool{
		Name:  "list_questions",
		Title: "List questions",
		Description: "List the questions this token asked, newest first: pending ones by default, " +
			"or every question with status all. Paged; pass next_cursor back as cursor.",
		Annotations: readOnly(),
	}, s.listQuestions)

	sdk.AddTool(server, &sdk.Tool{
		Name:  "cancel_question",
		Title: "Cancel a question",
		Description: "Withdraw a pending question this token asked. Use it when the decision is no longer needed. " +
			"A question that is already answered, canceled or expired cannot be canceled.",
		Annotations: settling(),
	}, s.cancelQuestion)

	sdk.AddTool(server, &sdk.Tool{
		Name:  "start_live_activity",
		Title: "Start a Live Activity",
		Description: "Start a Live Activity: an updatable Lock Screen card for a long-running job such as a deploy, " +
			"a build or a test run. A phone shows one at a time, so pass replace to end whatever is showing, " +
			"and a key to address the activity by name in later calls. Returns the activity and its sequence.",
		Annotations: additive(),
	}, s.startLiveActivity)

	sdk.AddTool(server, &sdk.Tool{
		Name:  "update_live_activity",
		Title: "Update a Live Activity",
		Description: "Change a running Live Activity's title, status, detail or progress and push the change. " +
			"Every field is optional but at least one is required; send detail or progress as null to remove them. " +
			"Use if_sequence to refuse the update when the activity moved on since you read it.",
		Annotations: additive(),
	}, s.updateLiveActivity)

	sdk.AddTool(server, &sdk.Tool{
		Name:  "end_live_activity",
		Title: "End a Live Activity",
		Description: "Finish a Live Activity with its final state and push it. Use it when the job it tracked is done; " +
			"the finished card stays on screen for dismiss_after_seconds. The activity remains readable in history.",
		Annotations: settling(),
	}, s.endLiveActivity)

	sdk.AddTool(server, &sdk.Tool{
		Name:  "get_live_activity",
		Title: "Read a Live Activity",
		Description: "Read one Live Activity by id or key, with its current state, sequence and delivery counts. " +
			"Use it before an update that should only apply to the state you last saw.",
		Annotations: readOnly(),
	}, s.getLiveActivity)

	sdk.AddTool(server, &sdk.Tool{
		Name:  "list_live_activities",
		Title: "List Live Activities",
		Description: "List Live Activities, newest first: the ones on a Lock Screen right now by default, " +
			"or every one with status all. Paged; pass next_cursor back as cursor.",
		Annotations: readOnly(),
	}, s.listLiveActivities)

	sdk.AddTool(server, &sdk.Tool{
		Name:  "list_devices",
		Title: "List devices",
		Description: "List the iPhones registered on the account with their capabilities. " +
			"Use it to pick device_ids for a targeted send, or to see whether a device can show Live Activities.",
		Annotations: readOnly(),
	}, s.listDevices)

	sdk.AddTool(server, &sdk.Tool{
		Name:  "list_services",
		Title: "List services",
		Description: "List the webhook services configured on the account with their defaults. " +
			"Webhook credentials are never included.",
		Annotations: readOnly(),
	}, s.listServices)

	sdk.AddTool(server, &sdk.Tool{
		Name:  "list_webhook_events",
		Title: "List webhook events",
		Description: "List webhook deliveries, newest first, with their delivery status and any error. " +
			"Paged; pass next_cursor back as cursor.",
		Annotations: readOnly(),
	}, s.listWebhookEvents)

	sdk.AddTool(server, &sdk.Tool{
		Name:  "search",
		Title: "Search",
		Description: "Search services, devices, webhook events, questions and Live Activities by title, body, " +
			"prompt, name or status, case-insensitively. Looks at the newest 100 of each kind and skips kinds " +
			"this token cannot read. Returns ids of the form kind:id to pass to fetch.",
		Annotations: readOnly(),
	}, s.search)

	sdk.AddTool(server, &sdk.Tool{
		Name:  "fetch",
		Title: "Fetch",
		Description: "Read one record found by search, by its kind:id. Returns the record's JSON as text, " +
			"with a title, a dashboard URL and its kind.",
		Annotations: readOnly(),
	}, s.fetch)
}

// forward makes one call carrying the model's arguments as the body, less the
// keys the adapter consumed itself.
func (s *Server) forward(ctx context.Context, req *sdk.CallToolRequest, method, path, idempotencyKey string, consumed ...string) (*sdk.CallToolResult, any, error) {
	body, err := bodyWithout(req.Params.Arguments, consumed...)
	if err != nil {
		return nil, nil, err
	}
	resp, err := s.call(ctx, headerOf(req), apiCall{method: method, path: path, body: body, idempotencyKey: idempotencyKey})
	if err != nil {
		return nil, nil, err
	}
	return result(resp), nil, nil
}

// read makes one call with no body.
func (s *Server) read(ctx context.Context, req *sdk.CallToolRequest, method, path string, query url.Values) (*sdk.CallToolResult, any, error) {
	resp, err := s.call(ctx, headerOf(req), apiCall{method: method, path: path, query: query})
	if err != nil {
		return nil, nil, err
	}
	return result(resp), nil, nil
}

func (s *Server) sendNotification(ctx context.Context, req *sdk.CallToolRequest, in sendNotificationArgs) (*sdk.CallToolResult, any, error) {
	return s.forward(ctx, req, http.MethodPost, "/notifications", in.IdempotencyKey, "idempotency_key")
}

func (s *Server) askQuestion(ctx context.Context, req *sdk.CallToolRequest, in askQuestionArgs) (*sdk.CallToolResult, any, error) {
	if in.WaitSeconds < 0 || in.WaitSeconds > maxWaitSeconds {
		return toolError(codeValidation, "wait_seconds must be between 0 and "+strconv.Itoa(maxWaitSeconds)+"."), nil, nil
	}
	body, err := bodyWithout(req.Params.Arguments, "idempotency_key", "wait_seconds")
	if err != nil {
		return nil, nil, err
	}
	header := headerOf(req)
	created, err := s.call(ctx, header, apiCall{
		method:         http.MethodPost,
		path:           "/interactions",
		body:           body,
		idempotencyKey: in.IdempotencyKey,
	})
	if err != nil {
		return nil, nil, err
	}
	if created.status != http.StatusCreated || in.WaitSeconds == 0 {
		return result(created), nil, nil
	}
	id := interactionID(created.body)
	if id == "" {
		return result(created), nil, nil
	}

	// The question is already out. Nothing about the wait may fail the tool:
	// a token without interactions:read gets the question as created, and
	// can still be told the answer through a callback or by the owner.
	fresh, err := s.call(ctx, header, apiCall{
		method: http.MethodGet,
		path:   "/interactions/" + url.PathEscape(id),
		query:  url.Values{"wait_seconds": {strconv.Itoa(in.WaitSeconds)}},
	})
	if err != nil || !fresh.ok() {
		return result(created), nil, nil
	}
	merged, ok := withInteraction(created.body, fresh.body)
	if !ok {
		return result(created), nil, nil
	}
	return result(&apiResponse{status: created.status, body: merged}), nil, nil
}

func (s *Server) getQuestion(ctx context.Context, req *sdk.CallToolRequest, in getQuestionArgs) (*sdk.CallToolResult, any, error) {
	if strings.TrimSpace(in.ID) == "" {
		return toolError(codeValidation, "id is required."), nil, nil
	}
	query := url.Values{}
	if in.WaitSeconds != 0 {
		query.Set("wait_seconds", strconv.Itoa(in.WaitSeconds))
	}
	return s.read(ctx, req, http.MethodGet, "/interactions/"+url.PathEscape(in.ID), query)
}

func (s *Server) listQuestions(ctx context.Context, req *sdk.CallToolRequest, in listQuestionsArgs) (*sdk.CallToolResult, any, error) {
	return s.read(ctx, req, http.MethodGet, "/interactions", listQuery(in.Status, in.Limit, in.Cursor))
}

func (s *Server) cancelQuestion(ctx context.Context, req *sdk.CallToolRequest, in cancelQuestionArgs) (*sdk.CallToolResult, any, error) {
	if strings.TrimSpace(in.ID) == "" {
		return toolError(codeValidation, "id is required."), nil, nil
	}
	return s.read(ctx, req, http.MethodPost, "/interactions/"+url.PathEscape(in.ID)+"/cancel", nil)
}

func (s *Server) startLiveActivity(ctx context.Context, req *sdk.CallToolRequest, in startLiveActivityArgs) (*sdk.CallToolResult, any, error) {
	return s.forward(ctx, req, http.MethodPost, "/activities", in.IdempotencyKey, "idempotency_key")
}

func (s *Server) updateLiveActivity(ctx context.Context, req *sdk.CallToolRequest, in updateLiveActivityArgs) (*sdk.CallToolResult, any, error) {
	if strings.TrimSpace(in.Identifier) == "" {
		return toolError(codeValidation, "identifier is required."), nil, nil
	}
	return s.forward(ctx, req, http.MethodPatch, "/activities/"+url.PathEscape(in.Identifier),
		in.IdempotencyKey, "identifier", "idempotency_key")
}

func (s *Server) endLiveActivity(ctx context.Context, req *sdk.CallToolRequest, in endLiveActivityArgs) (*sdk.CallToolResult, any, error) {
	if strings.TrimSpace(in.Identifier) == "" {
		return toolError(codeValidation, "identifier is required."), nil, nil
	}
	return s.forward(ctx, req, http.MethodPost, "/activities/"+url.PathEscape(in.Identifier)+"/end",
		in.IdempotencyKey, "identifier", "idempotency_key")
}

func (s *Server) getLiveActivity(ctx context.Context, req *sdk.CallToolRequest, in getLiveActivityArgs) (*sdk.CallToolResult, any, error) {
	if strings.TrimSpace(in.Identifier) == "" {
		return toolError(codeValidation, "identifier is required."), nil, nil
	}
	return s.read(ctx, req, http.MethodGet, "/activities/"+url.PathEscape(in.Identifier), nil)
}

func (s *Server) listLiveActivities(ctx context.Context, req *sdk.CallToolRequest, in listLiveActivitiesArgs) (*sdk.CallToolResult, any, error) {
	return s.read(ctx, req, http.MethodGet, "/activities", listQuery(in.Status, in.Limit, in.Cursor))
}

func (s *Server) listDevices(ctx context.Context, req *sdk.CallToolRequest, _ noArgs) (*sdk.CallToolResult, any, error) {
	return s.read(ctx, req, http.MethodGet, "/devices", nil)
}

func (s *Server) listServices(ctx context.Context, req *sdk.CallToolRequest, _ noArgs) (*sdk.CallToolResult, any, error) {
	return s.read(ctx, req, http.MethodGet, "/services", nil)
}

func (s *Server) listWebhookEvents(ctx context.Context, req *sdk.CallToolRequest, in listWebhookEventsArgs) (*sdk.CallToolResult, any, error) {
	return s.read(ctx, req, http.MethodGet, "/events", listQuery("", in.Limit, in.Cursor))
}

// listQuery carries the paging arguments the model set; the endpoint's own
// defaults apply to the rest.
func listQuery(status string, limit int, cursor string) url.Values {
	q := url.Values{}
	if status != "" {
		q.Set("status", status)
	}
	if limit != 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	return q
}

// interactionID reads the id out of a POST /interactions response.
func interactionID(body []byte) string {
	var res struct {
		Interaction struct {
			ID string `json:"id"`
		} `json:"interaction"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return ""
	}
	return res.Interaction.ID
}

// withInteraction replaces the interaction in a create response with the one
// a later read returned, keeping the create response's other fields.
func withInteraction(created, fresh []byte) ([]byte, bool) {
	var (
		createdFields map[string]json.RawMessage
		freshFields   map[string]json.RawMessage
	)
	if err := json.Unmarshal(created, &createdFields); err != nil || createdFields == nil {
		return nil, false
	}
	if err := json.Unmarshal(fresh, &freshFields); err != nil {
		return nil, false
	}
	interaction, ok := freshFields["interaction"]
	if !ok {
		return nil, false
	}
	createdFields["interaction"] = interaction
	merged, err := json.Marshal(createdFields)
	if err != nil {
		return nil, false
	}
	return merged, true
}

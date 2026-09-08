package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/mcp"
)

// mcpCall posts one JSON-RPC message to the MCP endpoint with the given
// credential ("" for none).
func (f *fixture) mcpCall(credential, method string, params any) *httptest.ResponseRecorder {
	f.t.Helper()

	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		f.t.Fatalf("encode the request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, mcp.Path, strings.NewReader(string(body))).WithContext(f.ctx)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

// rpcResult is the part of a tools/call response the tests read.
type rpcResult struct {
	Result struct {
		IsError           bool            `json:"isError"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		Content           []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (f *fixture) toolCall(credential, tool string, args map[string]any) rpcResult {
	f.t.Helper()

	rec := f.mcpCall(credential, "tools/call", map[string]any{"name": tool, "arguments": args})
	if rec.Code != http.StatusOK {
		f.t.Fatalf("tools/call %s: status = %d, want 200: %s", tool, rec.Code, rec.Body)
	}
	var out rpcResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		f.t.Fatalf("tools/call %s: decode: %v\n%s", tool, err, rec.Body)
	}
	if out.Error != nil {
		f.t.Fatalf("tools/call %s: protocol error %d %s", tool, out.Error.Code, out.Error.Message)
	}
	return out
}

// TestMCPToolCallsRunThroughTheAPI proves a tool is the endpoint it wraps: the
// push goes out, the record is written, an idempotency key replays, and a
// token without the scope is refused by the endpoint's own middleware.
func TestMCPToolCallsRunThroughTheAPI(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	device := f.registerDevice(strings.Repeat("c3", 32))

	args := map[string]any{"title": "Deploy bot", "body": "Build 4821 succeeded", "idempotency_key": "mcp-1"}
	out := f.toolCall(f.token, "send_notification", args)
	if out.Result.IsError {
		t.Fatalf("send_notification is an error result: %s", out.Result.Content)
	}
	var sent notificationResponse
	if err := json.Unmarshal(out.Result.StructuredContent, &sent); err != nil {
		t.Fatalf("structuredContent is not the endpoint's response: %v", err)
	}
	if sent.Notification.AcceptedCount != 1 || sent.Replayed {
		t.Errorf("response = %+v, want one accepted delivery", sent)
	}
	if len(out.Result.Content) != 1 || out.Result.Content[0].Type != "text" {
		t.Errorf("content = %+v, want one text block", out.Result.Content)
	}
	alert := f.sender.lastAlert(t)
	if alert.Target.DeviceID != device.ID || alert.Body != "Build 4821 succeeded" {
		t.Errorf("alert = %+v, want the body delivered to the registered device", alert)
	}

	out = f.toolCall(f.token, "send_notification", args)
	var replayed notificationResponse
	if err := json.Unmarshal(out.Result.StructuredContent, &replayed); err != nil {
		t.Fatalf("decode the replay: %v", err)
	}
	if !replayed.Replayed || replayed.Notification.ID != sent.Notification.ID {
		t.Errorf("second call = %+v, want a replay of %s", replayed, sent.Notification.ID)
	}

	_, reader, err := auth.New(f.store, nil).CreateAPIToken(f.ctx, f.userID, auth.CreateAPITokenParams{
		Name: "reader", Scopes: []string{db.ScopeDevicesRead},
	})
	if err != nil {
		t.Fatalf("mint a read-only token: %v", err)
	}
	out = f.toolCall(reader, "send_notification", map[string]any{"body": "not allowed"})
	if !out.Result.IsError {
		t.Fatalf("a token without notifications:send sent a notification: %s", out.Result.StructuredContent)
	}
	var refusal ErrorResponse
	if err := json.Unmarshal([]byte(out.Result.Content[0].Text), &refusal); err != nil {
		t.Fatalf("the tool error is not the API's envelope: %v\n%s", err, out.Result.Content[0].Text)
	}
	if refusal.Error.Code != CodeInsufficientScope {
		t.Errorf("code = %q, want %q", refusal.Error.Code, CodeInsufficientScope)
	}

	out = f.toolCall(reader, "list_devices", map[string]any{})
	var devices deviceListResponse
	if err := json.Unmarshal(out.Result.StructuredContent, &devices); err != nil || out.Result.IsError {
		t.Fatalf("list_devices: error = %v, isError = %v", err, out.Result.IsError)
	}
	if len(devices.Devices) != 1 || devices.Devices[0].ID != device.ID {
		t.Errorf("devices = %+v, want the one registered", devices.Devices)
	}
}

// TestMCPAdmitsOnlyAPITokens pins the gate: no credential, a session, a junk
// token and a revoked token are all 401 with the challenge that starts OAuth.
func TestMCPAdmitsOnlyAPITokens(t *testing.T) {
	f := newFixture(t, fixtureOptions{})

	var tokens tokenListResponse
	f.expect(http.MethodGet, "/tokens", f.session, "", http.StatusOK, &tokens)
	if len(tokens.Tokens) != 1 {
		t.Fatalf("tokens = %+v, want the fixture's one", tokens.Tokens)
	}
	f.expect(http.MethodDelete, "/tokens/"+tokens.Tokens[0].ID, f.session, "", http.StatusNoContent, nil)

	wantMetadata := `resource_metadata="https://hark.example.com` + mcp.ProtectedResourceMetadataPath + `"`
	for name, credential := range map[string]string{
		"anonymous": "",
		"session":   f.session,
		"junk":      "hark_notatoken",
		"revoked":   f.token,
	} {
		rec := f.mcpCall(credential, "tools/list", map[string]any{})
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401: %s", name, rec.Code, rec.Body)
			continue
		}
		challenge := rec.Header().Get("WWW-Authenticate")
		if !strings.HasPrefix(challenge, "Bearer ") || !strings.Contains(challenge, wantMetadata) ||
			!strings.Contains(challenge, `scope="`) {
			t.Errorf("%s: WWW-Authenticate = %q, want the resource metadata and a scope challenge", name, challenge)
		}
		if got := decodeError(t, rec); got.Error.Code != CodeUnauthorized {
			t.Errorf("%s: code = %q, want %q", name, got.Error.Code, CodeUnauthorized)
		}
	}
}

// TestMCPMetadataIsPublic checks the two discovery documents a client reads
// before it holds any credential.
func TestMCPMetadataIsPublic(t *testing.T) {
	f := newFixture(t, fixtureOptions{})

	for _, path := range []string{mcp.ProtectedResourceMetadataPath, mcp.ProtectedResourceMetadataPath + mcp.Path} {
		var doc struct {
			Resource             string   `json:"resource"`
			AuthorizationServers []string `json:"authorization_servers"`
			ScopesSupported      []string `json:"scopes_supported"`
		}
		rec := f.expect(http.MethodGet, path, "", "", http.StatusOK, &doc)
		if doc.Resource != "https://hark.example.com"+mcp.Path {
			t.Errorf("GET %s: resource = %q", path, doc.Resource)
		}
		if len(doc.AuthorizationServers) != 1 || doc.AuthorizationServers[0] != "https://hark.example.com" {
			t.Errorf("GET %s: authorization_servers = %v", path, doc.AuthorizationServers)
		}
		if len(doc.ScopesSupported) != len(db.Scopes) {
			t.Errorf("GET %s: scopes_supported = %v", path, doc.ScopesSupported)
		}
		if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Errorf("GET %s: no CORS header for browser clients", path)
		}
	}

	var as struct {
		Issuer                string   `json:"issuer"`
		AuthorizationEndpoint string   `json:"authorization_endpoint"`
		TokenEndpoint         string   `json:"token_endpoint"`
		RegistrationEndpoint  string   `json:"registration_endpoint"`
		CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
	}
	f.expect(http.MethodGet, AuthorizationServerMetadataPath, "", "", http.StatusOK, &as)
	if as.Issuer != "https://hark.example.com" {
		t.Errorf("issuer = %q", as.Issuer)
	}
	if as.AuthorizationEndpoint != "https://hark.example.com"+OAuthAuthorizePath ||
		as.TokenEndpoint != "https://hark.example.com"+OAuthTokenPath ||
		as.RegistrationEndpoint != "https://hark.example.com"+OAuthRegisterPath {
		t.Errorf("endpoints = %+v", as)
	}
	if len(as.CodeChallengeMethods) != 1 || as.CodeChallengeMethods[0] != "S256" {
		t.Errorf("code_challenge_methods_supported = %v, want S256 only", as.CodeChallengeMethods)
	}
}

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// apiCall is one request a tool makes against the API.
type apiCall struct {
	method string
	path   string
	query  url.Values
	// body is sent as application/json when non-nil.
	body           []byte
	idempotencyKey string
}

// apiResponse is what the API answered.
type apiResponse struct {
	status int
	body   []byte
}

func (r *apiResponse) ok() bool { return r.status >= 200 && r.status < 300 }

// recorder is the ResponseWriter a loopback call is served into.
type recorder struct {
	status      int
	header      http.Header
	body        bytes.Buffer
	wroteHeader bool
}

func (r *recorder) Header() http.Header { return r.header }

func (r *recorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.status = status
	r.wroteHeader = true
}

func (r *recorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	return r.body.Write(b)
}

// call serves one request against the API with the caller's own credential.
//
// The Authorization header is copied from the MCP request rather than
// re-derived, so the API's middleware judges the token exactly as it would
// over HTTP: scopes, revocation and attribution are all its call.
func (s *Server) call(ctx context.Context, incoming http.Header, c apiCall) (*apiResponse, error) {
	target := s.origin + c.path
	if len(c.query) > 0 {
		target += "?" + c.query.Encode()
	}
	var body io.Reader
	if c.body != nil {
		body = bytes.NewReader(c.body)
	}
	req, err := http.NewRequestWithContext(ctx, c.method, target, body)
	if err != nil {
		return nil, err
	}
	// A handler is served a request the way net/http would deliver it, and
	// that includes a Body that is never nil.
	if c.body == nil {
		req.Body = http.NoBody
	}
	req.Header.Set("Accept", "application/json")
	if c.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, name := range []string{"Authorization", requestIDHeader} {
		if v := incoming.Get(name); v != "" {
			req.Header.Set(name, v)
		}
	}
	if c.idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", c.idempotencyKey)
	}

	rec := &recorder{header: make(http.Header)}
	s.opts.API.ServeHTTP(rec, req)
	return &apiResponse{status: rec.status, body: rec.body.Bytes()}, nil
}

// result renders an API response as the tool's result: the body as one text
// block and, when the call succeeded, as structured content too. A refusal is
// a tool error carrying the envelope, so the model reads what went wrong and
// can correct itself; it is never a protocol error.
func result(resp *apiResponse) *sdk.CallToolResult {
	res := &sdk.CallToolResult{
		Content: []sdk.Content{&sdk.TextContent{Text: string(resp.body)}},
	}
	if !resp.ok() {
		res.IsError = true
		return res
	}
	if json.Valid(resp.body) {
		res.StructuredContent = json.RawMessage(resp.body)
	}
	return res
}

// document renders a value the adapter built itself, the way [result] renders
// an endpoint's response.
func document(v any) (*sdk.CallToolResult, any, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, nil, err
	}
	return &sdk.CallToolResult{
		Content:           []sdk.Content{&sdk.TextContent{Text: string(body)}},
		StructuredContent: json.RawMessage(body),
	}, nil, nil
}

// toolError is a refusal the adapter makes before reaching the API, in the
// API's own envelope so the model sees one shape of error.
func toolError(code, message string) *sdk.CallToolResult {
	return &sdk.CallToolResult{
		IsError: true,
		Content: []sdk.Content{&sdk.TextContent{Text: string(envelope(code, message))}},
	}
}

// headerOf returns the HTTP header the MCP request arrived with.
func headerOf(req *sdk.CallToolRequest) http.Header {
	if req == nil || req.Extra == nil {
		return nil
	}
	return req.Extra.Header
}

// bodyWithout returns the raw arguments as a JSON object with the adapter's
// own keys removed. Working on the raw message keeps an explicit null — which
// a PATCH distinguishes from an absent field — intact.
func bodyWithout(raw json.RawMessage, keys ...string) ([]byte, error) {
	fields := map[string]json.RawMessage{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, err
		}
		if fields == nil {
			fields = map[string]json.RawMessage{}
		}
	}
	for _, k := range keys {
		delete(fields, k)
	}
	return json.Marshal(fields)
}

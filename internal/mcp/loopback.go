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

// call forwards a request through the API with the incoming bearer token.
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
	// Incoming net/http requests always have a non-nil Body.
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

// result returns the API response as text and successful JSON as structured
// content. HTTP failures become tool errors.
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

// document returns a JSON value as text and structured content.
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

// toolError returns a tool failure using the API error envelope.
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

// bodyWithout removes adapter arguments and preserves explicit null values.
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

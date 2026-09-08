package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
)

const testToken = "hark_c2xLm9JbR1tXyLp0aNfCd7eJhSu4WgO7xY2bWvK"

func testPublicURL() *url.URL { return &url.URL{Scheme: "https", Host: "hark.example.com"} }

// fakeResolver stands in for *auth.Service: one secret is valid, everything
// else is refused the way the service refuses it.
type fakeResolver struct {
	secret string
	err    error
	mu     sync.Mutex
	seen   []string
}

func (f *fakeResolver) AuthenticateAPIToken(_ context.Context, secret string) (*auth.Principal, error) {
	f.mu.Lock()
	f.seen = append(f.seen, secret)
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if secret != f.secret {
		return nil, auth.ErrInvalidCredentials
	}
	return &auth.Principal{
		Kind:     auth.KindAPIToken,
		User:     db.User{ID: "user-1"},
		APIToken: &db.APIToken{ID: "token-1", UserID: "user-1", Scopes: db.Scopes},
	}, nil
}

// apiCallRecord is one request the fake API received.
type apiCallRecord struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   []byte
}

// fakeAPI stands in for the assembled API handler: it records every request
// and answers from a route table keyed "METHOD /path".
type fakeAPI struct {
	mu     sync.Mutex
	calls  []apiCallRecord
	routes map[string]http.HandlerFunc
}

func newFakeAPI() *fakeAPI { return &fakeAPI{routes: map[string]http.HandlerFunc{}} }

func (f *fakeAPI) handle(method, path string, h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[method+" "+path] = h
}

func (f *fakeAPI) answer(method, path string, status int, body string) {
	f.handle(method, path, canned(status, body))
}

func canned(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.calls = append(f.calls, apiCallRecord{
		Method: r.Method,
		Path:   r.URL.Path,
		Query:  r.URL.Query(),
		Header: r.Header.Clone(),
		Body:   body,
	})
	h := f.routes[r.Method+" "+r.URL.Path]
	f.mu.Unlock()
	if h == nil {
		canned(http.StatusNotFound, `{"error":{"code":"not_found","message":"No route matches."}}`)(w, r)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	h(w, r)
}

func (f *fakeAPI) recorded() []apiCallRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// harness is the MCP server on an httptest listener, with its fakes.
type harness struct {
	api      *fakeAPI
	resolver *fakeResolver
	server   *httptest.Server
}

// newHarness mounts the two handlers where the API would. wrap, when given,
// surrounds the MCP handler the way the API's middleware chain does.
func newHarness(t *testing.T, wrap ...func(http.Handler) http.Handler) *harness {
	t.Helper()
	api := newFakeAPI()
	resolver := &fakeResolver{secret: testToken}
	s := New(Options{API: api, Resolver: resolver, PublicURL: testPublicURL(), Version: "test"})

	var handler http.Handler = s.Handler()
	for _, w := range wrap {
		handler = w(handler)
	}
	mux := http.NewServeMux()
	mux.Handle(Path, handler)
	mux.Handle(ProtectedResourceMetadataPath, s.ResourceMetadata())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &harness{api: api, resolver: resolver, server: srv}
}

func (h *harness) endpoint() string { return h.server.URL + Path }

// headerTransport adds fixed headers to every request the SDK client makes.
type headerTransport struct {
	header http.Header
}

func (t headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, vs := range t.header {
		for _, v := range vs {
			r.Header.Set(k, v)
		}
	}
	return http.DefaultTransport.RoundTrip(r)
}

// connect opens an SDK client session carrying the test token.
func (h *harness) connect(t *testing.T, extra http.Header) *sdk.ClientSession {
	t.Helper()
	header := http.Header{"Authorization": {"Bearer " + testToken}}
	for k, vs := range extra {
		header[k] = vs
	}
	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(t.Context(), &sdk.StreamableClientTransport{
		Endpoint:             h.endpoint(),
		HTTPClient:           &http.Client{Transport: headerTransport{header: header}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func callTool(t *testing.T, session *sdk.ClientSession, name string, args any) *sdk.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	res, err := session.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	return res
}

// textOf returns the first text block of a result.
func textOf(t *testing.T, res *sdk.CallToolResult) string {
	t.Helper()
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			return tc.Text
		}
	}
	t.Fatal("the result has no text content")
	return ""
}

// asJSON round-trips a value through JSON into a generic map.
func asJSON(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", b, err)
	}
	return m
}

func parseJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("unmarshal %q: %v", s, err)
	}
	return m
}

func errorCode(t *testing.T, body string) string {
	t.Helper()
	env, _ := parseJSON(t, body)["error"].(map[string]any)
	code, _ := env["code"].(string)
	return code
}

// postMCP sends one raw JSON-RPC message with the transport's required headers.
func (h *harness) postMCP(t *testing.T, authorization, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, h.endpoint(), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

const pingMessage = `{"jsonrpc":"2.0","id":1,"method":"ping"}`

// challengeParams parses `Bearer k="v", k="v"`.
func challengeParams(t *testing.T, header string) map[string]string {
	t.Helper()
	scheme, rest, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		t.Fatalf("WWW-Authenticate = %q, want a Bearer challenge", header)
	}
	params := map[string]string{}
	for _, part := range strings.Split(rest, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			t.Fatalf("WWW-Authenticate parameter %q is not k=v", part)
		}
		params[k] = strings.Trim(v, `"`)
	}
	return params
}

func TestRequestsWithoutAnAPITokenAreChallenged(t *testing.T) {
	h := newHarness(t)
	for name, authorization := range map[string]string{
		"missing":           "",
		"malformed":         "Bearer",
		"wrong scheme":      "Basic " + testToken,
		"session token":     "Bearer harksess_c2xLm9JbR1tXyLp0aNfCd7eJhSu4WgO7xY2bWvK",
		"unknown API token": "Bearer hark_unknownunknownunknownunknownunknownunknown",
	} {
		t.Run(name, func(t *testing.T) {
			resp := h.postMCP(t, authorization, pingMessage)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", resp.StatusCode)
			}
			params := challengeParams(t, resp.Header.Get("WWW-Authenticate"))
			if got, want := params["resource_metadata"], ResourceMetadataURL(testPublicURL()); got != want {
				t.Errorf("resource_metadata = %q, want %q", got, want)
			}
			if got, want := params["scope"], strings.Join(db.Scopes, " "); got != want {
				t.Errorf("scope = %q, want %q", got, want)
			}
			body, _ := io.ReadAll(resp.Body)
			if code := errorCode(t, string(body)); code != codeUnauthorized {
				t.Errorf("error.code = %q, want %q", code, codeUnauthorized)
			}
		})
	}

	// A session token never reaches the resolver: it is refused on shape.
	for _, seen := range h.resolver.seen {
		if strings.HasPrefix(seen, auth.SessionTokenPrefix) {
			t.Errorf("a session token %q was passed to the resolver", seen)
		}
	}
}

func TestAResolverOutageIs503(t *testing.T) {
	h := newHarness(t)
	h.resolver.err = errors.New("dial tcp: connection refused")
	resp := h.postMCP(t, "Bearer "+testToken, pingMessage)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if code := errorCode(t, string(body)); code != codeUnavailable {
		t.Errorf("error.code = %q, want %q", code, codeUnavailable)
	}
}

func TestAValidTokenIsCheckedOnEveryRequest(t *testing.T) {
	h := newHarness(t)
	resp := h.postMCP(t, "Bearer "+testToken, pingMessage)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := h.resolver.seen; !slices.Equal(got, []string{testToken}) {
		t.Errorf("resolver saw %q, want the one token", got)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if id := resp.Header.Get("Mcp-Session-Id"); id != "" {
		t.Errorf("a stateless server issued Mcp-Session-Id %q", id)
	}
}

func TestOnlyPostIsServed(t *testing.T) {
	h := newHarness(t)
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		req, err := http.NewRequestWithContext(t.Context(), method, h.endpoint(), nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Accept", "application/json, text/event-stream")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want 405", method, resp.StatusCode)
		}
		if allow := resp.Header.Get("Allow"); allow != http.MethodPost {
			t.Errorf("%s: Allow = %q, want POST", method, allow)
		}
	}
}

func TestTheResourceMetadataDocument(t *testing.T) {
	h := newHarness(t)
	resp, err := http.Get(h.server.URL + ProtectedResourceMetadataPath)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	for name, want := range map[string]string{
		"Content-Type":                "application/json",
		"Cache-Control":               "public, max-age=300",
		"Access-Control-Allow-Origin": "*",
	} {
		if got := resp.Header.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	var doc struct {
		Resource               string   `json:"resource"`
		AuthorizationServers   []string `json:"authorization_servers"`
		ScopesSupported        []string `json:"scopes_supported"`
		BearerMethodsSupported []string `json:"bearer_methods_supported"`
		ResourceName           string   `json:"resource_name"`
		ResourceDocumentation  string   `json:"resource_documentation"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc.Resource != Resource(testPublicURL()) {
		t.Errorf("resource = %q, want %q", doc.Resource, Resource(testPublicURL()))
	}
	if !slices.Equal(doc.AuthorizationServers, []string{"https://hark.example.com"}) {
		t.Errorf("authorization_servers = %q", doc.AuthorizationServers)
	}
	if !slices.Equal(doc.ScopesSupported, db.Scopes) {
		t.Errorf("scopes_supported = %q, want every scope", doc.ScopesSupported)
	}
	if !slices.Equal(doc.BearerMethodsSupported, []string{"header"}) {
		t.Errorf("bearer_methods_supported = %q", doc.BearerMethodsSupported)
	}
	if doc.ResourceName != "Hark" {
		t.Errorf("resource_name = %q", doc.ResourceName)
	}
	if doc.ResourceDocumentation != "https://hark.example.com/docs" {
		t.Errorf("resource_documentation = %q", doc.ResourceDocumentation)
	}

	post, err := http.Post(h.server.URL+ProtectedResourceMetadataPath, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = post.Body.Close()
	if post.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", post.StatusCode)
	}
}

func TestInitializeDescribesTheServer(t *testing.T) {
	h := newHarness(t)
	session := h.connect(t, nil)
	init := session.InitializeResult()
	if init.ServerInfo.Name != "hark" {
		t.Errorf("serverInfo.name = %q, want hark", init.ServerInfo.Name)
	}
	if init.ServerInfo.Version != "test" {
		t.Errorf("serverInfo.version = %q, want the build's", init.ServerInfo.Version)
	}
	if init.Instructions == "" {
		t.Error("instructions are empty")
	}
}

func TestTheCorrelationIdReachesTheAPI(t *testing.T) {
	// The API's RequestID middleware puts the id on the response before the
	// handler runs; this stands in for it.
	assign := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set(requestIDHeader, "rid-outer")
			next.ServeHTTP(w, r)
		})
	}
	h := newHarness(t, assign)
	h.api.answer(http.MethodGet, "/devices", http.StatusOK, `{"devices":[]}`)
	session := h.connect(t, http.Header{requestIDHeader: {"rid-client"}})
	callTool(t, session, "list_devices", map[string]any{})

	calls := h.api.recorded()
	if len(calls) != 1 {
		t.Fatalf("API calls = %d, want 1", len(calls))
	}
	if got := calls[0].Header.Get(requestIDHeader); got != "rid-outer" {
		t.Errorf("%s = %q, want the id the middleware assigned", requestIDHeader, got)
	}
}

package apns

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAPNs is a local stand-in for Apple: a real HTTP/2 server that records
// what it was sent and answers however the test needs.
//
// It is a full server rather than a stubbed RoundTripper on purpose. The
// details this package has to get right — HTTP/2 negotiation, the header names,
// the body arriving as one frame, a reason parsed out of an error response —
// only exist on the wire.
type fakeAPNs struct {
	server *httptest.Server

	mu       sync.Mutex
	requests []recordedRequest

	// respond answers one request. The default takes everything.
	respond func(w http.ResponseWriter, r *http.Request)
}

type recordedRequest struct {
	Method  string
	Path    string
	Proto   string
	Headers http.Header
	Body    []byte
}

func newFakeAPNs(t *testing.T) *fakeAPNs {
	t.Helper()

	fake := &fakeAPNs{respond: func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("apns-id", "8B7A2F0C-3D5E-4A1B-9C6D-2E4F6A8B0C1D")
		w.WriteHeader(http.StatusOK)
	}}

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fake.mu.Lock()
		fake.requests = append(fake.requests, recordedRequest{
			Method:  r.Method,
			Path:    r.URL.Path,
			Proto:   r.Proto,
			Headers: r.Header.Clone(),
			Body:    body,
		})
		respond := fake.respond
		fake.mu.Unlock()
		respond(w, r)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	fake.server = server
	return fake
}

// answer installs a canned response.
func (f *fakeAPNs) answer(status int, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.respond = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("apns-id", "8B7A2F0C-3D5E-4A1B-9C6D-2E4F6A8B0C1D")
		w.WriteHeader(status)
		if reason != "" {
			_ = json.NewEncoder(w).Encode(map[string]string{"reason": reason})
		}
	}
}

// answerFunc installs an arbitrary handler.
func (f *fakeAPNs) answerFunc(h func(w http.ResponseWriter, r *http.Request)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.respond = h
}

func (f *fakeAPNs) recorded() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedRequest(nil), f.requests...)
}

func (f *fakeAPNs) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fakeAPNs) last(t *testing.T) recordedRequest {
	t.Helper()

	recorded := f.recorded()
	if len(recorded) == 0 {
		t.Fatal("no request reached APNs")
	}
	return recorded[len(recorded)-1]
}

// newTestClient points a client at the fake, with the credentials a test key
// provides.
func newTestClient(t *testing.T, fake *fakeAPNs, adjust func(*Config)) *Client {
	t.Helper()

	_, pemKey := testKey(t)
	cfg := Config{
		KeyID:       "ABCDE12345",
		TeamID:      "TEAM123456",
		PrivateKey:  pemKey,
		BundleID:    "dev.abdeen.hark",
		Environment: EnvironmentSandbox,
		Host:        fake.server.URL,
		HTTPClient:  fake.server.Client(),
	}
	if adjust != nil {
		adjust(&cfg)
	}

	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func alertRequest() Request {
	expiration := expirationDoNotRetain
	return Request{
		Token:      "0a1b2c3d4e5f",
		PushType:   pushTypeAlert,
		Topic:      "dev.abdeen.hark",
		Priority:   priorityImmediate,
		Expiration: &expiration,
		Payload:    []byte(`{"aps":{}}`),
		DeviceID:   "0198f3a1-2b4c-7d8e-9f01-23456789abcd",
	}
}

// TestClientRequestShape pins everything Apple reads off the wire.
func TestClientRequestShape(t *testing.T) {
	fake := newFakeAPNs(t)
	client := newTestClient(t, fake, nil)

	response := client.Push(context.Background(), alertRequest())
	if !response.Accepted() {
		t.Fatalf("Push = %+v, want accepted", response)
	}

	got := fake.last(t)
	if got.Proto != "HTTP/2.0" {
		t.Errorf("protocol = %s, want HTTP/2.0 — APNs has no HTTP/1.1 endpoint", got.Proto)
	}
	if got.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", got.Method)
	}
	if want := "/3/device/0a1b2c3d4e5f"; got.Path != want {
		t.Errorf("path = %s, want %s", got.Path, want)
	}
	if string(got.Body) != `{"aps":{}}` {
		t.Errorf("body = %s", got.Body)
	}

	headers := map[string]string{
		"apns-push-type":  "alert",
		"apns-topic":      "dev.abdeen.hark",
		"apns-priority":   "10",
		"apns-expiration": "0",
	}
	for name, want := range headers {
		if value := got.Headers.Get(name); value != want {
			t.Errorf("%s = %q, want %q", name, value, want)
		}
	}

	// The scheme keyword is lowercase, one space, then the provider token.
	authorization := got.Headers.Get("authorization")
	jwt, found := strings.CutPrefix(authorization, "bearer ")
	if !found {
		t.Fatalf("authorization = %q, want a lowercase bearer scheme", authorization)
	}
	var header map[string]any
	decodeSegment(t, strings.Split(jwt, ".")[0], &header)
	if header["kid"] != "ABCDE12345" {
		t.Errorf("the request carried a token for kid %v", header["kid"])
	}

	// Headers Hark deliberately never sends. An apns-id of our own would
	// replace the one Apple mints and returns, and a collapse id would let one
	// notification silently replace another.
	for _, name := range []string{"apns-id", "apns-collapse-id", "content-type"} {
		if value := got.Headers.Get(name); value != "" {
			t.Errorf("%s = %q, want it unset", name, value)
		}
	}
}

// TestDefaultTransportNegotiatesHTTP2 exercises the transport a deployment
// actually uses, rather than the one httptest hands out.
//
// It is worth its own test because the failure mode is invisible here: a
// transport that quietly falls back to HTTP/1.1 passes every other test in this
// file and then talks to an endpoint Apple does not run.
func TestDefaultTransportNegotiatesHTTP2(t *testing.T) {
	fake := newFakeAPNs(t)

	transport := newTransport()
	trusted, ok := fake.server.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatalf("the test server's transport is %T, want *http.Transport", fake.server.Client().Transport)
	}
	// The only thing borrowed from the test server is its certificate. The
	// protocol configuration is the production one.
	transport.TLSClientConfig = trusted.TLSClientConfig.Clone()

	client := newTestClient(t, fake, func(cfg *Config) {
		cfg.HTTPClient = &http.Client{Transport: transport}
	})

	response := client.Push(context.Background(), alertRequest())
	if !response.Accepted() {
		t.Fatalf("Push = %+v, want accepted", response)
	}
	if got := fake.last(t).Proto; got != "HTTP/2.0" {
		t.Errorf("protocol = %s, want HTTP/2.0", got)
	}
}

func TestClientLiveActivityHeaders(t *testing.T) {
	fake := newFakeAPNs(t)
	client := newTestClient(t, fake, nil)

	client.Push(context.Background(), Request{
		Token:    "0a1b",
		PushType: pushTypeLiveActivity,
		Topic:    client.activityTopic(),
		Priority: priorityImmediate,
		Payload:  []byte(`{"aps":{}}`),
	})

	got := fake.last(t)
	if want := "liveactivity"; got.Headers.Get("apns-push-type") != want {
		t.Errorf("apns-push-type = %q, want %q", got.Headers.Get("apns-push-type"), want)
	}
	if want := "dev.abdeen.hark.push-type.liveactivity"; got.Headers.Get("apns-topic") != want {
		t.Errorf("apns-topic = %q, want %q", got.Headers.Get("apns-topic"), want)
	}
	// No expiration header: ActivityKit pushes take Apple's own default rather
	// than the discard-now rule alerts use.
	if value, present := got.Headers["Apns-Expiration"]; present {
		t.Errorf("apns-expiration = %v, want it unset on a Live Activity", value)
	}
}

func TestClientHostSelection(t *testing.T) {
	_, pemKey := testKey(t)
	base := Config{KeyID: "ABCDE12345", TeamID: "TEAM123456", PrivateKey: pemKey, BundleID: "dev.abdeen.hark"}

	tests := map[string]string{
		EnvironmentSandbox:    HostSandbox,
		EnvironmentProduction: HostProduction,
		"":                    HostSandbox,
	}
	for environment, want := range tests {
		t.Run("environment="+environment, func(t *testing.T) {
			cfg := base
			cfg.Environment = environment

			client, err := NewClient(cfg)
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			if client.host != want {
				t.Errorf("host = %s, want %s", client.host, want)
			}
		})
	}

	cfg := base
	cfg.Environment = "staging"
	if _, err := NewClient(cfg); err == nil {
		t.Error("NewClient accepted an unknown environment")
	}
}

func TestClientTopics(t *testing.T) {
	fake := newFakeAPNs(t)
	client := newTestClient(t, fake, nil)

	if got := client.alertTopic(); got != "dev.abdeen.hark" {
		t.Errorf("alertTopic = %q, want the bare bundle id", got)
	}
	if want := "dev.abdeen.hark.push-type.liveactivity"; client.activityTopic() != want {
		t.Errorf("activityTopic = %q, want %q", client.activityTopic(), want)
	}
}

// TestClientErrorReasons covers what Apple's answers become.
func TestClientErrorReasons(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		reason   string
		accepted bool
		dead     bool
	}{
		{name: "accepted", status: 200, accepted: true},
		{name: "bad token", status: 400, reason: ReasonBadDeviceToken, dead: true},
		{name: "wrong topic", status: 400, reason: ReasonDeviceTokenNotForTopic},
		{name: "unregistered", status: 410, reason: ReasonUnregistered, dead: true},
		{name: "expired token", status: 410, reason: ReasonExpiredToken, dead: true},
		{name: "rate limited", status: 429, reason: ReasonTooManyRequests},
		{name: "apple is down", status: 503, reason: "ServiceUnavailable"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeAPNs(t)
			fake.answer(tt.status, tt.reason)
			client := newTestClient(t, fake, nil)

			response := client.Push(context.Background(), alertRequest())
			if response.Status != tt.status {
				t.Errorf("status = %d, want %d", response.Status, tt.status)
			}
			if response.Reason != tt.reason {
				t.Errorf("reason = %q, want %q", response.Reason, tt.reason)
			}
			if response.Accepted() != tt.accepted {
				t.Errorf("accepted = %v, want %v", response.Accepted(), tt.accepted)
			}
			if tokenIsDead(response) != tt.dead {
				t.Errorf("tokenIsDead = %v, want %v", tokenIsDead(response), tt.dead)
			}
			if response.APNsID == "" {
				t.Error("the apns-id response header was not recorded")
			}
			// Nothing here is retried. A push APNs refused is lost on purpose:
			// the client re-registers and reconciles, and a duplicate is worse
			// than a gap.
			if fake.count() != 1 {
				t.Errorf("APNs saw %d requests, want exactly 1", fake.count())
			}
		})
	}
}

// TestClientStatusGoneWithoutReason is the 410 an old provider sometimes sends
// with an empty body. The status alone is enough to retire the token.
func TestClientStatusGoneWithoutReason(t *testing.T) {
	fake := newFakeAPNs(t)
	fake.answer(http.StatusGone, "")
	client := newTestClient(t, fake, nil)

	response := client.Push(context.Background(), alertRequest())
	if response.Reason != "" {
		t.Errorf("reason = %q, want empty", response.Reason)
	}
	if !tokenIsDead(response) {
		t.Error("a 410 with no reason did not retire the token")
	}
}

func TestClientUnparseableBody(t *testing.T) {
	fake := newFakeAPNs(t)
	fake.answerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "<html>a proxy is in the way</html>")
	})
	client := newTestClient(t, fake, nil)

	response := client.Push(context.Background(), alertRequest())
	if response.Status != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", response.Status)
	}
	if response.Reason != ReasonInvalidResponse {
		t.Errorf("reason = %q, want %q", response.Reason, ReasonInvalidResponse)
	}
}

func TestClientTimeout(t *testing.T) {
	fake := newFakeAPNs(t)
	fake.answerFunc(func(_ http.ResponseWriter, r *http.Request) {
		// Held until the client gives up, which is what a stalled provider
		// looks like from here.
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	client := newTestClient(t, fake, func(cfg *Config) { cfg.Timeout = 50 * time.Millisecond })

	response := client.Push(context.Background(), alertRequest())
	if response.Status != 0 {
		t.Errorf("status = %d, want 0 — nothing was received", response.Status)
	}
	if response.Reason != ReasonTimeout {
		t.Errorf("reason = %q, want %q", response.Reason, ReasonTimeout)
	}
	if tokenIsDead(response) {
		t.Error("a timeout retired a device token")
	}
	if fake.count() != 1 {
		t.Errorf("APNs saw %d requests, want exactly 1 — timeouts are not retried", fake.count())
	}
}

func TestClientCanceledContext(t *testing.T) {
	fake := newFakeAPNs(t)
	fake.answerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	client := newTestClient(t, fake, nil)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	response := client.Push(ctx, alertRequest())
	if response.Reason != ReasonCanceled {
		t.Errorf("reason = %q, want %q", response.Reason, ReasonCanceled)
	}
}

// hostileTransport fails every request with an error that spells out the full
// request URL, which is net/http's worst case: http.Client.Do wraps transport
// errors in *url.Error, whose text quotes the URL, and the APNs URL carries the
// device token.
type hostileTransport struct{}

func (hostileTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("connection reset by peer talking to %s", req.URL.String())
}

// TestClientTransportErrorLogOmitsToken is the regression for the leak: a
// transport failure whose error deliberately quotes the tokenized URL must
// still produce a log with the stable reason and device id, and nothing else.
func TestClientTransportErrorLogOmitsToken(t *testing.T) {
	const sentinelToken = "feedfacecafef00d00112233445566778899aabbccddeeff0011223344556677"

	var logs bytes.Buffer
	fake := newFakeAPNs(t)
	client := newTestClient(t, fake, func(cfg *Config) {
		cfg.HTTPClient = &http.Client{Transport: hostileTransport{}}
		cfg.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	})

	req := alertRequest()
	req.Token = sentinelToken
	response := client.Push(context.Background(), req)

	if response.Status != 0 {
		t.Errorf("status = %d, want 0 — nothing was received", response.Status)
	}
	if response.Reason != ReasonTransportError {
		t.Fatalf("reason = %q, want %q", response.Reason, ReasonTransportError)
	}

	logged := logs.String()
	if !strings.Contains(logged, ReasonTransportError) {
		t.Errorf("the log does not carry the stable reason:\n%s", logged)
	}
	if !strings.Contains(logged, req.DeviceID) {
		t.Errorf("the log does not carry the device id:\n%s", logged)
	}
	for _, secret := range []string{
		sentinelToken,
		devicePath + sentinelToken,
		client.host + devicePath + sentinelToken,
	} {
		if strings.Contains(logged, secret) {
			t.Errorf("the log quotes %q:\n%s", secret, logged)
		}
	}
}

// TestClientRequestBuildTokenNeverLogged covers the branch before the wire: a
// host that cannot parse makes http.NewRequestWithContext fail, and that parse
// error quotes the whole URL, token included. The log must not.
func TestClientRequestBuildTokenNeverLogged(t *testing.T) {
	const sentinelToken = "feedfacecafef00d00112233445566778899aabbccddeeff0011223344556677"

	var logs bytes.Buffer
	_, pemKey := testKey(t)
	client, err := NewClient(Config{
		KeyID:       "ABCDE12345",
		TeamID:      "TEAM123456",
		PrivateKey:  pemKey,
		BundleID:    "dev.abdeen.hark",
		Environment: EnvironmentSandbox,
		// The control character makes url.Parse refuse the URL inside
		// http.NewRequestWithContext, before any connection is attempted.
		Host:   "https://api.sandbox.push.apple.com\n",
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	req := alertRequest()
	req.Token = sentinelToken
	response := client.Push(context.Background(), req)

	if response.Reason != ReasonTransportError {
		t.Fatalf("reason = %q, want %q", response.Reason, ReasonTransportError)
	}

	logged := logs.String()
	if !strings.Contains(logged, "building the APNs request failed") {
		t.Fatalf("the request-construction branch did not log:\n%s", logged)
	}
	if !strings.Contains(logged, req.DeviceID) {
		t.Errorf("the log does not carry the device id:\n%s", logged)
	}
	if strings.Contains(logged, sentinelToken) {
		t.Errorf("the log quotes the device token:\n%s", logged)
	}
}

// TestClientRefreshesExpiredProviderToken is the one retry in the whole push
// path. Apple rejected the credential rather than the notification, so nothing
// was delivered and sending again cannot duplicate anything.
func TestClientRefreshesExpiredProviderToken(t *testing.T) {
	fake := newFakeAPNs(t)
	fake.answerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fake.count() == 1 {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"reason":"ExpiredProviderToken"}`)
			return
		}
		w.Header().Set("apns-id", "8B7A2F0C-3D5E-4A1B-9C6D-2E4F6A8B0C1D")
		w.WriteHeader(http.StatusOK)
	})

	// The clock stands still, so a second token differs only because the cache
	// was dropped — which is exactly what is being tested.
	now := time.Now()
	client := newTestClient(t, fake, func(cfg *Config) { cfg.Now = func() time.Time { return now } })

	response := client.Push(context.Background(), alertRequest())
	if !response.Accepted() {
		t.Fatalf("Push = %+v, want the retry to be accepted", response)
	}
	if fake.count() != 2 {
		t.Fatalf("APNs saw %d requests, want exactly 2", fake.count())
	}

	recorded := fake.recorded()
	first := recorded[0].Headers.Get("authorization")
	second := recorded[1].Headers.Get("authorization")
	if first == second {
		t.Error("the retry reused the token Apple had just refused")
	}
}

// TestClientDoesNotRetryInvalidProviderToken separates the two 403s: an expired
// token is worth another attempt, a wrong key is not.
func TestClientDoesNotRetryInvalidProviderToken(t *testing.T) {
	fake := newFakeAPNs(t)
	fake.answer(http.StatusForbidden, ReasonInvalidProviderToken)
	client := newTestClient(t, fake, nil)

	response := client.Push(context.Background(), alertRequest())
	if response.Status != http.StatusForbidden {
		t.Errorf("status = %d, want 403", response.Status)
	}
	if fake.count() != 1 {
		t.Errorf("APNs saw %d requests, want exactly 1", fake.count())
	}
}

// TestClientRetriesExpiredProviderTokenOnce guards the bound: a provider that
// answers 403 forever must not be hammered.
func TestClientRetriesExpiredProviderTokenOnce(t *testing.T) {
	fake := newFakeAPNs(t)
	fake.answer(http.StatusForbidden, ReasonExpiredProviderToken)
	client := newTestClient(t, fake, nil)

	client.Push(context.Background(), alertRequest())
	if fake.count() != 2 {
		t.Errorf("APNs saw %d requests, want exactly 2", fake.count())
	}
}

func TestNewClientRequiresCredentials(t *testing.T) {
	_, pemKey := testKey(t)
	complete := Config{
		KeyID: "ABCDE12345", TeamID: "TEAM123456", PrivateKey: pemKey, BundleID: "dev.abdeen.hark",
	}

	if _, err := NewClient(complete); err != nil {
		t.Fatalf("NewClient refused a complete configuration: %v", err)
	}

	spoil := map[string]func(*Config){
		"no key id":      func(c *Config) { c.KeyID = "" },
		"no team id":     func(c *Config) { c.TeamID = "" },
		"no private key": func(c *Config) { c.PrivateKey = nil },
		"no bundle id":   func(c *Config) { c.BundleID = "" },
		"unusable key":   func(c *Config) { c.PrivateKey = []byte("not a key") },
	}
	for name, spoil := range spoil {
		t.Run(name, func(t *testing.T) {
			cfg := complete
			spoil(&cfg)

			if _, err := NewClient(cfg); err == nil {
				t.Error("NewClient accepted an incomplete configuration")
			}
		})
	}
}

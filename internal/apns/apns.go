// Package apns delivers Hark's pushes to the Apple Push Notification service.
//
// The package is split the way the problem is:
//
//   - [Client] is the transport. It knows Apple's HTTP/2 contract — hosts,
//     paths, headers, the provider JWT, and how to read a reason out of a
//     response — and nothing about Hark.
//   - The payload builders know Hark's own vocabulary: what an alert carries,
//     what a Live Activity's content state is, and which of those keys Apple
//     defines versus which are ours.
//   - [Sender] joins the two and implements [push.Sender], which is the only
//     part the rest of the server sees.
//
// Two rules run through all of it. The first is that a send never returns an
// error: every outcome is a status, a reason and an acceptance flag, because a
// fan-out where one phone is unreachable still has to settle the others, and
// every failure has to be recorded against the row it belongs to. The second is
// that there are no retries. A push that APNs did not take is lost on purpose —
// alerts carry `apns-expiration: 0` so Apple will not store them either, and
// the client's own reconciliation is what closes the gap. The single exception
// is a provider token Apple says has expired, which is a rejection that
// happened before the notification was considered at all; that one is minted
// again and sent once more.
package apns

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// APNs hosts. There is exactly one per process: the environment is a property
// of the credentials and the build, not of a request.
const (
	HostProduction = "https://api.push.apple.com"
	HostSandbox    = "https://api.sandbox.push.apple.com"
)

// Environments a device token can belong to.
const (
	EnvironmentSandbox    = "sandbox"
	EnvironmentProduction = "production"
)

const (
	// MaxPayloadBytes is Apple's ceiling on an encoded payload. It is checked
	// before a request is made, so an oversized push is a recorded failure
	// rather than a round trip that ends in 413.
	MaxPayloadBytes = 4096

	// DefaultTimeout bounds one attempt from request to complete response.
	DefaultTimeout = 10 * time.Second

	// liveActivityTopicSuffix is what Apple appends to the bundle id for
	// ActivityKit pushes. Sending a Live Activity to the bare topic is rejected
	// with DeviceTokenNotForTopic.
	liveActivityTopicSuffix = ".push-type.liveactivity"

	// devicePath is the request path, completed with the lowercase hex token.
	devicePath = "/3/device/"

	// maxResponseBytes caps what is read back. Apple's error bodies are a few
	// dozen bytes; anything larger is a proxy in the way, not APNs.
	maxResponseBytes = 8 << 10
)

// Apple's push types. There is no background push anywhere in Hark: the server
// never sends a silent notification.
const (
	pushTypeAlert         = "alert"
	pushTypeLiveActivity  = "liveactivity"
	priorityImmediate     = 10
	expirationDoNotRetain = 0
)

// Reason strings Apple returns that the server acts on. They are Apple's
// spelling, and the synthetic reasons below share the namespace deliberately: a
// delivery's last reason is one field, and a reader should not have to know
// which side produced it.
const (
	ReasonUnregistered           = "Unregistered"
	ReasonBadDeviceToken         = "BadDeviceToken"
	ReasonExpiredToken           = "ExpiredToken"
	ReasonDeviceTokenNotForTopic = "DeviceTokenNotForTopic"
	ReasonExpiredProviderToken   = "ExpiredProviderToken"
	ReasonInvalidProviderToken   = "InvalidProviderToken"
	ReasonPayloadTooLarge        = "PayloadTooLarge"
	ReasonTooManyRequests        = "TooManyRequests"
)

// Reasons this package synthesizes when no APNs verdict was received.
const (
	// ReasonTimeout means the attempt exceeded the per-request timeout.
	ReasonTimeout = "Timeout"
	// ReasonCanceled means the caller's context ended first. The push may or
	// may not have been sent; nothing is retried either way.
	ReasonCanceled = "Canceled"
	// ReasonTransportError covers every other connection-level failure. The
	// underlying error is logged rather than reported, because it embeds the
	// request URL and the request URL embeds a device token.
	ReasonTransportError = "TransportError"
	// ReasonInvalidResponse means APNs answered with a body that is not JSON.
	ReasonInvalidResponse = "InvalidApnsResponse"
	// ReasonProviderTokenUnavailable means the provider JWT could not be
	// signed, so no request was made.
	ReasonProviderTokenUnavailable = "ProviderTokenUnavailable"
)

// Config describes one APNs connection.
type Config struct {
	// KeyID is the 10-character identifier of the .p8 auth key. It becomes the
	// JWT header's kid.
	KeyID string
	// TeamID is the Apple Developer team identifier. It becomes the JWT's iss.
	TeamID string
	// PrivateKey is the ES256 signing key as PKCS#8 PEM.
	PrivateKey []byte
	// BundleID is the app's identifier, and the base of every apns-topic.
	BundleID string
	// Environment is "sandbox" or "production". It picks the host, and Live
	// Activity tokens minted for the other one are refused before a connection
	// is opened.
	Environment string

	// Host overrides the host Environment implies. Tests point it at a local
	// HTTP/2 server; a deployment leaves it empty.
	Host string
	// HTTPClient overrides the transport. It must speak HTTP/2. Tests pass the
	// client an httptest server hands out; a deployment leaves it nil and gets
	// a pooled HTTP/2-only transport.
	HTTPClient *http.Client
	// Timeout bounds one attempt. Zero means DefaultTimeout.
	Timeout time.Duration
	// TokenTTL is how long one provider JWT is reused. Zero means
	// DefaultTokenTTL.
	TokenTTL time.Duration
	// Now is the clock, for tests. Zero means time.Now.
	Now func() time.Time
	// Logger receives transport failures and provider misconfiguration. Zero
	// discards them.
	Logger *slog.Logger
}

// Client is an APNs provider connection.
//
// It is safe for concurrent use: the HTTP transport pools connections and the
// provider token is minted under a mutex, so a fan-out of alerts shares one
// TCP connection and one JWT.
type Client struct {
	host        string
	bundleID    string
	environment string
	timeout     time.Duration
	http        *http.Client
	tokens      *tokenSource
	log         *slog.Logger
}

// NewClient validates the credentials and prepares the transport. It does not
// reach the network: a failure here is a configuration error, reported at boot
// rather than at the first push.
func NewClient(cfg Config) (*Client, error) {
	switch {
	case cfg.KeyID == "":
		return nil, errors.New("apns: KeyID is required")
	case cfg.TeamID == "":
		return nil, errors.New("apns: TeamID is required")
	case len(cfg.PrivateKey) == 0:
		return nil, errors.New("apns: PrivateKey is required")
	case cfg.BundleID == "":
		return nil, errors.New("apns: BundleID is required")
	}

	environment := cfg.Environment
	if environment == "" {
		environment = EnvironmentSandbox
	}
	if environment != EnvironmentSandbox && environment != EnvironmentProduction {
		return nil, fmt.Errorf("apns: Environment must be %q or %q, got %q",
			EnvironmentSandbox, EnvironmentProduction, cfg.Environment)
	}

	tokens, err := newTokenSource(cfg.KeyID, cfg.TeamID, cfg.PrivateKey, cfg.TokenTTL, cfg.Now)
	if err != nil {
		return nil, err
	}

	host := cfg.Host
	if host == "" {
		host = HostSandbox
		if environment == EnvironmentProduction {
			host = HostProduction
		}
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Transport: newTransport()}
	}

	return &Client{
		host:        strings.TrimRight(host, "/"),
		bundleID:    cfg.BundleID,
		environment: environment,
		timeout:     timeout,
		http:        httpClient,
		tokens:      tokens,
		log:         log,
	}, nil
}

// Environment reports which APNs environment this client talks to.
func (c *Client) Environment() string { return c.environment }

// newTransport builds an HTTP/2-only transport.
//
// HTTP/2 is not an optimisation for APNs, it is the protocol: Apple has no
// HTTP/1.1 endpoint. Refusing to negotiate anything else means a proxy that
// downgrades the connection fails loudly instead of silently sending requests
// Apple will not answer.
func newTransport() *http.Transport {
	var protocols http.Protocols
	protocols.SetHTTP2(true)

	return &http.Transport{
		Protocols:           &protocols,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: 10 * time.Second,
		// One host, a handful of devices, and a connection that is cheap to
		// keep: idle connections are held long enough that a burst of alerts
		// reuses one rather than paying for a TLS handshake per phone.
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     5 * time.Minute,
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	}
}

// Request is one push, addressed and encoded.
type Request struct {
	// Token is the destination: an APNs device token for an alert, or an
	// ActivityKit push token for a Live Activity. Lowercase hex.
	Token string
	// PushType fills apns-push-type.
	PushType string
	// Topic fills apns-topic.
	Topic string
	// Priority fills apns-priority.
	Priority int
	// Expiration fills apns-expiration when set. Alerts send 0, which tells
	// APNs to deliver now or discard; Live Activities send nothing and take
	// Apple's default.
	Expiration *int
	// Payload is the encoded JSON body.
	Payload []byte
	// DeviceID identifies the phone in log lines. It never reaches Apple.
	DeviceID string
}

// Response is one normalized APNs answer. It is what every attempt resolves to,
// successful or not.
type Response struct {
	// Status is the HTTP status, or 0 when no response was received at all.
	Status int
	// APNsID is Apple's identifier for the message, from the response header.
	// It is what a support request is opened with.
	APNsID string
	// Reason is Apple's reason string, one of the synthetic reasons above, or
	// empty on success.
	Reason string
}

// Accepted reports whether APNs took the message. Nothing else counts: a 2xx
// that is not 200 is not a documented APNs answer.
func (r Response) Accepted() bool { return r.Status == http.StatusOK }

// Push sends one request and reports what came back.
//
// It never returns an error. A connection that could not be made, a body that
// could not be parsed and a token Apple rejected are all the same kind of
// event to a caller: something to record against the delivery and move on from.
func (c *Client) Push(ctx context.Context, req Request) Response {
	resp, retry := c.attempt(ctx, req)
	if !retry {
		return resp
	}

	// Apple rejected the credential rather than the notification, so nothing
	// was delivered and re-sending cannot duplicate anything. This is the only
	// retry in the push path, and it happens at most once.
	c.log.WarnContext(ctx, "APNs rejected the provider token; minting a new one and retrying once",
		"reason", resp.Reason, "device_id", req.DeviceID)
	resp, _ = c.attempt(ctx, req)
	return resp
}

// attempt makes one request and reports whether a fresh provider token is worth
// one more.
func (c *Client) attempt(ctx context.Context, req Request) (Response, bool) {
	providerToken, err := c.tokens.get()
	if err != nil {
		c.log.ErrorContext(ctx, "signing the APNs provider token failed", "error", err)
		return Response{Reason: ReasonProviderTokenUnavailable}, false
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.host+devicePath+req.Token, bytes.NewReader(req.Payload))
	if err != nil {
		c.log.ErrorContext(ctx, "building the APNs request failed", "error", err)
		return Response{Reason: ReasonTransportError}, false
	}

	// Apple defines these. Three headers are deliberately absent: no
	// content-type, which APNs does not want; no apns-collapse-id, because
	// nothing Hark sends should silently replace something else already on a
	// Lock Screen; and no request apns-id, because Apple mints one and returns
	// it, and with no retries there is nothing to deduplicate against.
	httpReq.Header.Set("authorization", "bearer "+providerToken)
	httpReq.Header.Set("apns-push-type", req.PushType)
	httpReq.Header.Set("apns-topic", req.Topic)
	httpReq.Header.Set("apns-priority", strconv.Itoa(req.Priority))
	if req.Expiration != nil {
		httpReq.Header.Set("apns-expiration", strconv.Itoa(*req.Expiration))
	}

	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		reason := transportReason(err)
		// The error text carries the request URL, and the request URL carries a
		// device token. It goes to the log, never to a caller.
		c.log.WarnContext(ctx, "the APNs request failed",
			"reason", reason, "device_id", req.DeviceID, "error", err)
		return Response{Reason: reason}, false
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(httpResp.Body, maxResponseBytes))
		_ = httpResp.Body.Close()
	}()

	resp := Response{Status: httpResp.StatusCode, APNsID: httpResp.Header.Get("apns-id")}
	body, err := io.ReadAll(io.LimitReader(httpResp.Body, maxResponseBytes))
	if err != nil {
		c.log.WarnContext(ctx, "reading the APNs response failed", "device_id", req.DeviceID, "error", err)
		return resp, false
	}
	if len(bytes.TrimSpace(body)) > 0 {
		var decoded struct {
			Reason string `json:"reason"`
		}
		if json.Unmarshal(body, &decoded) != nil {
			resp.Reason = ReasonInvalidResponse
		} else {
			resp.Reason = decoded.Reason
		}
	}

	if resp.Status == http.StatusForbidden &&
		(resp.Reason == ReasonExpiredProviderToken || resp.Reason == ReasonInvalidProviderToken) {
		// Whatever is cached is what Apple just refused. Drop it either way; an
		// expired one is worth one more attempt, an invalid one is a key or a
		// team id that a retry cannot fix.
		c.tokens.invalidate(providerToken)
		return resp, resp.Reason == ReasonExpiredProviderToken
	}
	if resp.Reason == ReasonDeviceTokenNotForTopic {
		// The token is fine and the topic is wrong, which is this server's
		// mistake and not the phone's. Say so loudly: nothing else in the
		// pipeline will, because no token is pruned for it.
		c.log.ErrorContext(ctx, "APNs refused the topic; check the configured bundle id",
			"topic", req.Topic, "push_type", req.PushType, "device_id", req.DeviceID)
	}
	return resp, false
}

// transportReason names a connection-level failure without quoting it.
//
// The distinction that matters is whose deadline ran out: this package's
// ten seconds is a slow provider, and a cancellation is the caller hanging up.
func transportReason(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return ReasonTimeout
	case errors.Is(err, context.Canceled):
		return ReasonCanceled
	default:
		return ReasonTransportError
	}
}

// alertTopic is the bare bundle id.
func (c *Client) alertTopic() string { return c.bundleID }

// activityTopic is the bundle id ActivityKit pushes are addressed to.
func (c *Client) activityTopic() string { return c.bundleID + liveActivityTopicSuffix }

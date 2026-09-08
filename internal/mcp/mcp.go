// Package mcp exposes the HTTP API as a Model Context Protocol server.
//
// Every tool is an adapter over one documented endpoint: its arguments are
// that endpoint's request fields, its result is that endpoint's response, and
// the call is made against the assembled API handler with the caller's own
// bearer token — so scopes, rate limits, idempotency and attribution are the
// endpoint's, and nothing is reachable here that the same token could not
// reach over HTTP. See docs/api.md § MCP.
package mcp

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
)

// Path is where the MCP endpoint is mounted.
const Path = "/mcp"

// ProtectedResourceMetadataPath is where the endpoint's RFC 9728 metadata is
// published. The same document is served at the path-suffixed form,
// ProtectedResourceMetadataPath + Path, which clients try first.
const ProtectedResourceMetadataPath = "/.well-known/oauth-protected-resource"

// Resource is the identifier a token is issued for (RFC 8707): the deployment's
// public origin plus [Path], with no trailing slash.
func Resource(publicURL *url.URL) string {
	return origin(publicURL) + Path
}

// ResourceMetadataURL is the absolute URL of the protected resource metadata,
// as the 401 challenge names it.
func ResourceMetadataURL(publicURL *url.URL) string {
	return origin(publicURL) + ProtectedResourceMetadataPath
}

func origin(u *url.URL) string {
	if u == nil {
		return ""
	}
	o := *u
	o.Path = strings.TrimRight(o.Path, "/")
	o.RawQuery, o.Fragment = "", ""
	return o.String()
}

// TokenResolver resolves a bearer API token. *auth.Service satisfies it.
type TokenResolver interface {
	AuthenticateAPIToken(ctx context.Context, secret string) (*auth.Principal, error)
}

// Options are the server's dependencies.
type Options struct {
	// API is the fully assembled JSON API handler, credential middleware
	// included. Every tool call is an HTTP request made against it. Required.
	API http.Handler
	// Resolver checks the bearer token on every request. Required.
	Resolver TokenResolver
	// PublicURL is the origin clients reach the deployment on. It names the
	// resource a token is issued for and roots every URL a tool hands back.
	PublicURL *url.URL
	// Version identifies the running build to clients.
	Version string
	// Logger receives handler errors and the transport's own log. Nil discards.
	Logger *slog.Logger
}

// Server is the MCP endpoint together with its protected-resource metadata.
type Server struct {
	opts   Options
	origin string
	// challenge is the WWW-Authenticate value every 401 carries.
	challenge string
	// metadata is the RFC 9728 document, encoded once.
	metadata  []byte
	transport http.Handler
}

// maxRequestBodyBytes caps one JSON-RPC message. The API applies its own cap
// to the loopback request a tool makes, so this one only has to leave room
// for the envelope around a body that size.
const maxRequestBodyBytes = 128 << 10

// New builds the server. It panics when a required dependency is missing: that
// is a wiring mistake, and failing at construction is clearer than failing per
// request.
func New(opts Options) *Server {
	if opts.API == nil {
		panic("mcp: Options.API is required")
	}
	if opts.Resolver == nil {
		panic("mcp: Options.Resolver is required")
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}

	s := &Server{
		opts:   opts,
		origin: origin(opts.PublicURL),
		challenge: `Bearer resource_metadata="` + ResourceMetadataURL(opts.PublicURL) +
			`", scope="` + strings.Join(db.Scopes, " ") + `"`,
		metadata: resourceMetadataDocument(opts.PublicURL),
	}

	// The transport logs a session opening and closing on every stateless
	// request, which the access log already records once; only its warnings
	// and errors are worth a line of their own.
	quiet := slog.New(leveled{Handler: opts.Logger.Handler(), min: slog.LevelWarn})

	server := sdk.NewServer(
		&sdk.Implementation{Name: "hark", Title: "Hark", Version: opts.Version},
		&sdk.ServerOptions{Instructions: instructions, Logger: quiet},
	)
	s.addTools(server)

	s.transport = sdk.NewStreamableHTTPHandler(
		func(*http.Request) *sdk.Server { return server },
		&sdk.StreamableHTTPOptions{
			Stateless:           true,
			JSONResponse:        true,
			Logger:              quiet,
			MaxRequestBodyBytes: maxRequestBodyBytes,
			// The SDK's rebinding guard refuses a request that arrived on a
			// loopback address with a Host that is not loopback — which is
			// every request a reverse proxy on the same machine forwards.
			// The guard exists for servers a browser could reach with no
			// credential. This endpoint admits nothing without a bearer
			// token (gate.go), which a page on another origin cannot attach,
			// so the guard would only ever refuse legitimate deployments.
			DisableLocalhostProtection: true,
		},
	)
	return s
}

// leveled passes records at or above min to the handler it wraps.
type leveled struct {
	slog.Handler
	min slog.Level
}

func (h leveled) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= h.min && h.Handler.Enabled(ctx, level)
}

func (h leveled) WithAttrs(attrs []slog.Attr) slog.Handler {
	return leveled{Handler: h.Handler.WithAttrs(attrs), min: h.min}
}

func (h leveled) WithGroup(name string) slog.Handler {
	return leveled{Handler: h.Handler.WithGroup(name), min: h.min}
}

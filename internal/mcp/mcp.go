// Package mcp exposes the HTTP API as a Model Context Protocol server.
// Tool calls use the caller's bearer token and the API's middleware.
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
	if u == nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
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
	// PublicURL is the public origin for OAuth metadata and result URLs.
	PublicURL *url.URL
	// Version identifies the running build to clients.
	Version string
	// Logger receives handler and transport errors. Nil discards logs.
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

// maxRequestBodyBytes caps the JSON-RPC envelope. The API separately caps
// each forwarded request body.
const maxRequestBodyBytes = 128 << 10

// New builds the server and panics if a required dependency is missing.
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

	// The HTTP access log already records stateless requests.
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
			// Local reverse proxies may forward a public Host. serve checks
			// Origin against PublicURL before authenticating the request.
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

package mcp

import (
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/abdeen-labs/hark/internal/db"
)

// docsPath and dashboardPrefix are httpapi.DocsPath and
// httpapi.DashboardPrefix, which cannot be imported from here.
const (
	docsPath        = "/docs"
	dashboardPrefix = "/dashboard"
)

// resourceMetadata is the RFC 9728 document. See docs/api.md
// § GET /.well-known/oauth-protected-resource.
type resourceMetadata struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	ScopesSupported        []string `json:"scopes_supported"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
	ResourceName           string   `json:"resource_name"`
	ResourceDocumentation  string   `json:"resource_documentation"`
}

func resourceMetadataDocument(publicURL *url.URL) []byte {
	body, err := json.Marshal(resourceMetadata{
		Resource:               Resource(publicURL),
		AuthorizationServers:   []string{origin(publicURL)},
		ScopesSupported:        db.Scopes,
		BearerMethodsSupported: []string{"header"},
		ResourceName:           "Hark",
		ResourceDocumentation:  origin(publicURL) + docsPath,
	})
	if err != nil {
		panic("mcp: encoding the resource metadata: " + err.Error())
	}
	return body
}

// ResourceMetadata serves the RFC 9728 document (public, cacheable, CORS *).
func (s *Server) ResourceMetadata() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeEnvelope(w, http.StatusMethodNotAllowed, codeMethodNotAllowed,
				r.Method+" is not supported here; allowed methods are GET, HEAD.")
			return
		}
		h := w.Header()
		h.Set("Content-Type", "application/json")
		h.Set("Cache-Control", "public, max-age=300")
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(s.metadata)
		}
	})
}

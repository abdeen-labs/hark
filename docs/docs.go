// Package docs embeds the documents that the server publishes at runtime.
// Keeping the embed directives beside the source files ensures the binary uses
// the checked-in documentation directly.
package docs

import _ "embed"

// APIContract contains docs/api.md, which the server renders at /docs.
//
//go:embed api.md
var APIContract string

// OpenAPIContract is the OpenAPI 3.1 definition of the HTTP API,
// served verbatim at /openapi.json.
//
//go:embed openapi.json
var OpenAPIContract string

// LLMsContract is the documentation index served at /llms.txt. Its links are
// root-relative so they work for every deployment.
//
//go:embed llms.txt
var LLMsContract string

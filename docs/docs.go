// Package docs carries the documents the server publishes at runtime.
//
// It exists so that [APIContract] is compiled into the binary from the same
// file people read on disk. A copy under internal/ would be a second source of
// truth, and the one that drifts is always the one nobody edits; an embed
// directive cannot reach up out of its own directory, so the directive lives
// here, beside the document.
//
// There is no logic here, and there should not be. Rendering belongs to
// whatever serves the page.
package docs

import _ "embed"

// APIContract is docs/api.md: the living contract every client is built from,
// served as HTML at /docs.
//
//go:embed api.md
var APIContract string

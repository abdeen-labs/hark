package dashboard

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"net/http"
	"strings"
)

//go:embed assets/app.css assets/app.js assets/docs.css assets/docs.js assets/htmx.min.js
var assetFS embed.FS

// asset is one embedded static file, served from a URL that contains a digest
// of its contents.
//
// Hashing the name is what lets the response be cached forever: a changed file
// is a changed URL, so there is never a stale copy to revalidate and never a
// cache-busting query string to remember to bump.
type asset struct {
	body        []byte
	contentType string
	etag        string
}

// assetLinks are the URLs the layout links to. Docs is the extra sheet the
// contract page loads on top of the shared one, so the dashboard does not carry
// styles for elements it never renders. HTMX is a vendored copy of htmx 2.0.8
// (from the npm registry, unmodified), whose hx-boost is what turns a nav
// click into a fetch and a swap instead of a document teardown.
type assetLinks struct {
	CSS    string
	JS     string
	Docs   string
	DocsJS string
	HTMX   string
}

var (
	// files maps a hashed filename to its contents. It is built once at
	// startup; nothing writes to it afterwards.
	files  = map[string]asset{}
	assets = assetLinks{
		CSS:    mustLoadAsset("app.css", "text/css; charset=utf-8"),
		JS:     mustLoadAsset("app.js", "text/javascript; charset=utf-8"),
		Docs:   mustLoadAsset("docs.css", "text/css; charset=utf-8"),
		DocsJS: mustLoadAsset("docs.js", "text/javascript; charset=utf-8"),
		HTMX:   mustLoadAsset("htmx.min.js", "text/javascript; charset=utf-8"),
	}
)

// mustLoadAsset reads an embedded file, registers it under its hashed name and
// returns the URL to link to. It panics on a missing file, which can only mean
// the embed pattern and the directory have drifted apart.
func mustLoadAsset(name, contentType string) string {
	body, err := assetFS.ReadFile("assets/" + name)
	if err != nil {
		panic("dashboard: embedded asset " + name + ": " + err.Error())
	}

	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])[:12]
	stem, ext, _ := strings.Cut(name, ".")
	hashed := stem + "-" + digest + "." + ext

	files[hashed] = asset{body: body, contentType: contentType, etag: `"` + digest + `"`}
	return pathAssets + "/" + hashed
}

// showAsset serves one embedded file.
func (d *Dashboard) showAsset(w http.ResponseWriter, r *http.Request) {
	file, ok := files[r.PathValue("file")]
	if !ok {
		d.notFound(w, r)
		return
	}

	h := w.Header()
	h.Set("Content-Type", file.contentType)
	h.Set("ETag", file.etag)
	h.Set("Cache-Control", "public, max-age=31536000, immutable")

	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, file.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(file.body)
}

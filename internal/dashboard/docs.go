package dashboard

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"html/template"
	"net/http"
	"strings"

	"github.com/abdeen-labs/hark/docs"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	gmtext "github.com/yuin/goldmark/text"
)

// The published API contract.
//
// docs/api.md is compiled into the binary and rendered to HTML once, so the
// page a client author reads is the same document that ships with the build
// serving it — there is no step where the two can disagree, and no file on
// disk for a deployment to forget to copy.
//
// It is the one page here with no credential anywhere near it: no session, no
// CSRF token, and — see [httpapi.New] — not even the middleware that would
// resolve one. A contract nobody can read before they authenticate is a
// contract nobody can implement a client against.

// markdown is the renderer the contract is built with: CommonMark, plus GFM
// tables because the document is mostly tables, plus the heading ids its own
// cross-links are written against.
//
// Raw HTML is deliberately not enabled. The source is ours today, but a
// renderer that passes through whatever it is handed is one careless paste away
// from putting a script tag on a public page.
var markdown = goldmark.New(
	goldmark.WithExtensions(extension.Table),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
)

// tocLevels are the heading levels the outline shows: the document's sections
// and their endpoints. Going deeper turns a table of contents into a second
// copy of the page.
const (
	tocMinLevel = 2
	tocMaxLevel = 3
)

// contract is the rendered document and the outline drawn beside it, built
// once at package initialisation.
var contract = renderContract()

// renderedContract is what the markdown became.
type renderedContract struct {
	// Body is the goldmark output. It is [template.HTML] because it *is*
	// markup, and safely so: the renderer escapes raw HTML rather than
	// forwarding it, so nothing in the source can produce a tag the renderer
	// did not choose to emit.
	Body     template.HTML
	Contents []docsHeading
}

// docsHeading is one entry in the table of contents.
type docsHeading struct {
	// Level is 2 or 3, which the stylesheet turns into an indent.
	Level int
	Text  string
	ID    string
}

// renderContract converts the embedded markdown once.
//
// It panics on failure, as the asset loader and the template parser do: a
// document that cannot be rendered is a broken build, not a runtime condition,
// and finding out at the first request instead of at startup helps nobody.
func renderContract() renderedContract {
	source := []byte(docs.APIContract)
	root := markdown.Parser().Parse(gmtext.NewReader(source))

	var out renderedContract
	for node := root.FirstChild(); node != nil; node = node.NextSibling() {
		heading, ok := node.(*ast.Heading)
		if !ok || heading.Level < tocMinLevel || heading.Level > tocMaxLevel {
			continue
		}
		id, ok := heading.AttributeString("id")
		raw, isBytes := id.([]byte)
		if !ok || !isBytes {
			continue
		}
		out.Contents = append(out.Contents, docsHeading{
			Level: heading.Level,
			Text:  headingText(heading, source),
			ID:    string(raw),
		})
	}

	var body bytes.Buffer
	if err := markdown.Renderer().Render(&body, source, root); err != nil {
		panic("dashboard: render the API contract: " + err.Error())
	}
	out.Body = template.HTML(body.String()) //nolint:gosec // escaped by goldmark; see renderedContract.Body
	return out
}

// headingText flattens a heading to the plain string the outline shows, so
// "`GET /v1/events`" reads as "GET /v1/events" rather than carrying its
// backticks into a link.
func headingText(node ast.Node, source []byte) string {
	var b strings.Builder
	_ = ast.Walk(node, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch t := n.(type) {
		case *ast.Text:
			b.Write(t.Segment.Value(source))
		case *ast.String:
			b.Write(t.Value)
		}
		return ast.WalkContinue, nil
	})
	return b.String()
}

// docsPage is the whole page, assembled once per [Dashboard] so the footer can
// name the build.
type docsPage struct {
	Title    string
	Version  string
	Paths    paths
	Assets   assetLinks
	Body     template.HTML
	Contents []docsHeading
}

// page is a document rendered ahead of time, with the tag a conditional GET is
// answered against.
type page struct {
	body []byte
	etag string
}

// buildDocs renders the contract page into bytes. It is called from [New], and
// panics for the same reason [New] does: a template that cannot execute is a
// wiring mistake, and the first request is the wrong place to learn about it.
func (d *Dashboard) buildDocs() page {
	var buf bytes.Buffer
	err := tmplDocs.ExecuteTemplate(&buf, "docs", docsPage{
		Title:    "API",
		Version:  d.opts.Version,
		Paths:    d.paths,
		Assets:   assets,
		Body:     contract.Body,
		Contents: contract.Contents,
	})
	if err != nil {
		panic("dashboard: render the contract page: " + err.Error())
	}

	sum := sha256.Sum256(buf.Bytes())
	return page{body: buf.Bytes(), etag: `"` + hex.EncodeToString(sum[:])[:16] + `"`}
}

// showDocs serves the cached page.
func (d *Dashboard) showDocs(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("ETag", d.docs.etag)
	// Public, and cacheable for a few minutes rather than forever: the page is
	// a build artifact at a fixed URL, so a deployment that has just shipped a
	// contract change must not keep answering with the previous one. The ETag
	// is what makes the revalidation after that cheap.
	h.Set("Cache-Control", "public, max-age=300")

	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, d.docs.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(d.docs.body)
}

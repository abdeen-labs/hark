package dashboard

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"html/template"
	"net/http"
	"strings"

	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	gmtext "github.com/yuin/goldmark/text"

	"github.com/abdeen-labs/hark/docs"
)

// markdown renders the embedded API reference with tables, heading IDs, and
// syntax highlighting. The highlighter uses CSS classes so the page does not
// require inline styles. Raw HTML remains disabled.
var markdown = goldmark.New(
	goldmark.WithExtensions(
		extension.Table,
		highlighting.NewHighlighting(
			highlighting.WithFormatOptions(chromahtml.WithClasses(true)),
		),
	),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
)

// tocLevels are the heading levels included in the page outline.
const (
	tocMinLevel = 2
	tocMaxLevel = 3
)

// contract contains the rendered document and page outline.
var contract = renderContract()

// renderedContract contains the HTML document and its outline entries.
type renderedContract struct {
	// Body is safe Goldmark output. The renderer escapes raw HTML from the
	// source document.
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

// renderContract converts the embedded Markdown and builds its outline. A
// rendering failure prevents startup.
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

// headingText returns a heading without Markdown formatting.
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

// docsPage contains the values used to render the documentation page.
type docsPage struct {
	Title    string
	Version  string
	Paths    paths
	Assets   assetLinks
	Body     template.HTML
	Contents []docsHeading
}

// page is a rendered document and its ETag.
type page struct {
	body []byte
	etag string
}

// buildPublicDocs prepares each public documentation format at startup.
func (d *Dashboard) buildPublicDocs() {
	d.docs = d.buildDocs()
	d.docsMarkdown = newPage([]byte(docs.APIContract))
	d.openAPI = newPage([]byte(docs.OpenAPIContract))
	d.llms = newPage([]byte(docs.LLMsContract))
}

func newPage(body []byte) page {
	sum := sha256.Sum256(body)
	return page{body: body, etag: `"` + hex.EncodeToString(sum[:])[:16] + `"`}
}

// buildDocs renders the HTML documentation page. A template failure prevents
// startup.
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

	return newPage(buf.Bytes())
}

// showDocs serves the cached page.
func (d *Dashboard) showDocs(w http.ResponseWriter, r *http.Request) {
	d.servePublicDoc(w, r, d.docs, "text/html; charset=utf-8")
}

func (d *Dashboard) showDocsMarkdown(w http.ResponseWriter, r *http.Request) {
	d.servePublicDoc(w, r, d.docsMarkdown, "text/markdown; charset=utf-8")
}

func (d *Dashboard) showOpenAPI(w http.ResponseWriter, r *http.Request) {
	d.servePublicDoc(w, r, d.openAPI, "application/vnd.oai.openapi+json;version=3.1")
}

func (d *Dashboard) showLLMs(w http.ResponseWriter, r *http.Request) {
	d.servePublicDoc(w, r, d.llms, "text/plain; charset=utf-8")
}

func (d *Dashboard) servePublicDoc(w http.ResponseWriter, r *http.Request, p page, contentType string) {
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("ETag", p.etag)
	// Documentation is public and briefly cached. The ETag supports conditional
	// requests after the cache expires.
	h.Set("Cache-Control", "public, max-age=300")

	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, p.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(p.body)
}

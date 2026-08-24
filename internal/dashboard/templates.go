package dashboard

import (
	"embed"
	"html/template"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/abdeen-labs/hark/internal/db"
)

//go:embed templates/*.html
var templateFS embed.FS

// Each page has a separate template set containing the shared layout and its
// "content" block.
var (
	tmplLogin     = mustParse("login.html")
	tmplOverview  = mustParse("overview.html")
	tmplHistory   = mustParse("history.html")
	tmplServices  = mustParse("services.html")
	tmplService   = mustParse("service.html")
	tmplDevices   = mustParse("devices.html")
	tmplTokens    = mustParse("tokens.html")
	tmplTest      = mustParse("test.html")
	tmplAuthorize = mustParse("authorize.html")
	tmplError     = mustParse("error.html")

	// The public documentation page uses its own layout and table of contents.
	tmplDocs = template.Must(template.New("docs.html").Funcs(funcs).
			ParseFS(templateFS, "templates/docs.html"))
)

// mustParse builds one page's template set: the shared layout, the feed item
// markup the overview and the history both draw, and the page itself.
func mustParse(page string) *template.Template {
	return template.Must(template.New(page).Funcs(funcs).
		ParseFS(templateFS, "templates/layout.html", "templates/feed.html", "templates/"+page))
}

// funcs are the handful of helpers the templates need. Everything else is a
// plain field: the pages read the store's own types, so a view model exists
// only where the page genuinely computes something.
var funcs = template.FuncMap{
	"when":             formatWhen,
	"iso":              formatISO,
	"ago":              formatAgo,
	"text":             text,
	"has":              slices.Contains[[]string, string],
	"title":            titleCase,
	"scopeDescription": db.ScopeDescription,
}

// em is the dash shown wherever a value is absent. One character, one meaning:
// "there is nothing here", never "zero" and never "unknown".
const em = "—"

// formatWhen renders an absolute instant, accepting a time or a nullable one.
func formatWhen(v any) string {
	t, ok := asTime(v)
	if !ok {
		return em
	}
	return t.UTC().Format("2006-01-02 15:04 MST")
}

// formatISO renders the machine-readable half of a <time> element, in the same
// RFC 3339 form the API uses. It is empty when there is no instant, which is a
// valid absent datetime attribute.
func formatISO(v any) string {
	t, ok := asTime(v)
	if !ok {
		return ""
	}
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// formatAgo renders an instant as a coarse distance from now.
//
// It reads the wall clock rather than the server's injectable one. This is the
// only place that does, and it is the right call: the number exists so a person
// can see at a glance that something happened minutes and not days ago, and it
// is rendered into a page that is stale the moment it is sent.
func formatAgo(v any) string {
	t, ok := asTime(v)
	if !ok {
		return em
	}
	d := time.Since(t)
	future := d < 0
	if future {
		d = -d
	}

	var out string
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		out = strconv.Itoa(int(d.Minutes())) + "m"
	case d < 24*time.Hour:
		out = strconv.Itoa(int(d.Hours())) + "h"
	default:
		out = strconv.Itoa(int(d.Hours())/24) + "d"
	}
	if future {
		return "in " + out
	}
	return out + " ago"
}

func asTime(v any) (time.Time, bool) {
	switch t := v.(type) {
	case time.Time:
		return t, !t.IsZero()
	case *time.Time:
		if t == nil {
			return time.Time{}, false
		}
		return *t, !t.IsZero()
	default:
		return time.Time{}, false
	}
}

// text renders a nullable string, so a template never has to branch on nil just
// to print something.
func text(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// titleCase turns a stored enum member into a label: "time_sensitive" reads as
// "Time sensitive".
func titleCase(s string) string {
	s = strings.ReplaceAll(s, "_", " ")
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

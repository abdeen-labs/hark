package httpapi

import (
	"net/http"
	"slices"
	"strings"
)

// router is a thin wrapper over http.ServeMux.
//
// ServeMux already does method+pattern routing; the wrapper exists only so that
// unmatched paths and unsupported methods answer with the JSON error envelope
// instead of net/http's plain-text defaults. It records the methods registered
// per pattern and, on [router.handler], installs a path-only fallback for each
// one that replies 405 with an accurate Allow header.
type router struct {
	mux     *http.ServeMux
	methods map[string][]string
}

func newRouter() *router {
	return &router{mux: http.NewServeMux(), methods: make(map[string][]string)}
}

// handle registers a handler for one method and pattern, e.g.
// ("GET", "/services/{id}").
func (rt *router) handle(method, pattern string, h http.Handler) {
	rt.mux.Handle(method+" "+pattern, h)
	if !slices.Contains(rt.methods[pattern], method) {
		rt.methods[pattern] = append(rt.methods[pattern], method)
	}
}

func (rt *router) handleFunc(method, pattern string, h http.HandlerFunc) {
	rt.handle(method, pattern, h)
}

// mount registers a handler for every method on a pattern, without the 405
// fallback [router.handle] installs.
//
// It exists for the one sub-handler that does not answer in the JSON envelope —
// the dashboard, which renders HTML and therefore has to produce its own "not
// found" and "method not allowed" responses.
func (rt *router) mount(pattern string, h http.Handler) {
	rt.mux.Handle(pattern, h)
}

// handler seals the router. It must be called exactly once, after every route
// is registered.
func (rt *router) handler() http.Handler {
	for pattern, methods := range rt.methods {
		allow := methods
		if slices.Contains(allow, http.MethodGet) && !slices.Contains(allow, http.MethodHead) {
			// ServeMux serves HEAD from a GET registration.
			allow = append(slices.Clone(allow), http.MethodHead)
		}
		slices.Sort(allow)
		rt.mux.Handle(pattern, methodNotAllowed(strings.Join(allow, ", ")))
	}
	// "/" is the catch-all for unrouted paths. Registering a route on it would
	// turn every unknown path into a 405, so the guard here only avoids the
	// duplicate-pattern panic; don't register "/" as a route.
	if _, taken := rt.methods["/"]; !taken {
		rt.mux.HandleFunc("/", notFound)
	}
	return rt.mux
}

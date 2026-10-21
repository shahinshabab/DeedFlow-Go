package web

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
)

// route is a named URL with Django-style converters, e.g.
// "deeds/<uuid:public_id>/wizard/".
type route struct {
	name    string
	pattern string // the Django-style path, without the leading slash
	params  []string
}

// Router registers handlers under names and reverses names back to URLs, the
// way Django's urlconf does; templates use it through {% url %}.
type Router struct {
	mux     *http.ServeMux
	routes  map[string]route
	ordered map[string][]orderedRoute
	mu      sync.RWMutex
}

var paramRe = regexp.MustCompile(`<(?:(\w+):)?(\w+)>`)

// NewRouter makes an empty router.
func NewRouter() *Router {
	return &Router{mux: http.NewServeMux(), routes: map[string]route{}}
}

// Handle registers h for the Django-style path under name. The name is what
// {% url %} and Ctx.ViewName use.
func (rt *Router) Handle(name, path string, h http.Handler) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	r := route{name: name, pattern: path}
	for _, m := range paramRe.FindAllStringSubmatch(path, -1) {
		r.params = append(r.params, m[2])
	}
	rt.routes[name] = r
	muxPath := "/" + paramRe.ReplaceAllString(path, "{$2}")
	if strings.HasSuffix(muxPath, "/") {
		muxPath += "{$}"
	}
	rt.mux.Handle(muxPath, h)
}

// ordered holds the routes of a prefix that can overlap each other (Django
// tries its patterns in order; Go's mux refuses patterns where neither is
// more specific, like "accounts/payments/<int:pk>/" and
// "accounts/<str:token>/receipts/").
type orderedRoute struct {
	re       *regexp.Regexp
	h        http.Handler
	n        []string
	catchAll bool
}

var converterRegex = map[string]string{"int": `[0-9]+`, "uuid": `[0-9a-fA-F-]{36}`, "str": `[^/]+`, "slug": `[-a-zA-Z0-9_]+`, "path": `.+`}

// HandleOrdered registers a named route that is matched in registration order
// once its prefix is mounted with MountOrdered.
func (rt *Router) HandleOrdered(name, path string, h http.Handler) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	r := route{name: name, pattern: path}
	var re strings.Builder
	re.WriteString("^/")
	last := 0
	var names []string
	for _, loc := range paramRe.FindAllStringSubmatchIndex(path, -1) {
		re.WriteString(regexp.QuoteMeta(path[last:loc[0]]))
		conv := "str"
		if loc[2] >= 0 {
			conv = path[loc[2]:loc[3]]
		}
		re.WriteString("(" + converterRegex[conv] + ")")
		names = append(names, path[loc[4]:loc[5]])
		last = loc[1]
	}
	re.WriteString(regexp.QuoteMeta(path[last:]) + "$")
	r.params = names
	rt.routes[name] = r
	if rt.ordered == nil {
		rt.ordered = map[string][]orderedRoute{}
	}
	prefix := path
	if i := strings.Index(prefix, "/"); i >= 0 {
		prefix = prefix[:i+1]
	}
	rt.ordered[prefix] = append(rt.ordered[prefix], orderedRoute{re: regexp.MustCompile(re.String()), h: h, n: names,
		catchAll: strings.HasSuffix(name, "catch_all")})
}

// MountOrdered serves the routes registered with HandleOrdered under prefix
// ("accounts/").
func (rt *Router) MountOrdered(prefix string) {
	rt.mu.RLock()
	routes := rt.ordered[prefix]
	rt.mu.RUnlock()
	rt.mux.Handle("/"+prefix, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, o := range routes {
			if m := o.re.FindStringSubmatch(r.URL.Path); m != nil {
				for i, name := range o.n {
					r.SetPathValue(name, m[i+1])
				}
				o.h.ServeHTTP(w, r)
				return
			}
		}
		http.NotFound(w, r)
	}))
}

// Reverse builds the URL for name from positional args (in the order of the
// path's parameters).
func (rt *Router) Reverse(name string, args ...any) (string, error) {
	rt.mu.RLock()
	r, ok := rt.routes[name]
	rt.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("no route named %q", name)
	}
	i := 0
	var missing error
	out := paramRe.ReplaceAllStringFunc(r.pattern, func(string) string {
		if i >= len(args) {
			missing = fmt.Errorf("route %q needs %d arguments, got %d", name, len(r.params), len(args))
			return ""
		}
		v := url.PathEscape(fmt.Sprint(args[i]))
		i++
		return v
	})
	if missing != nil {
		return "", missing
	}
	return "/" + out, nil
}

// ReverseKw builds the URL for name using named arguments.
func (rt *Router) ReverseKw(name string, kwargs map[string]any) (string, error) {
	rt.mu.RLock()
	r, ok := rt.routes[name]
	rt.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("no route named %q", name)
	}
	args := make([]any, len(r.params))
	for i, p := range r.params {
		v, ok := kwargs[p]
		if !ok {
			return "", fmt.Errorf("route %q needs argument %q", name, p)
		}
		args[i] = v
	}
	return rt.Reverse(name, args...)
}

// Resolves reports whether an ordered route (other than a catch-all) matches the path.
func (rt *Router) Resolves(path string) bool {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	for _, routes := range rt.ordered {
		for _, o := range routes {
			if o.catchAll {
				continue
			}
			if o.re.MatchString(path) {
				return true
			}
		}
	}
	return false
}

// ServeHTTP dispatches to the registered handlers.
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) { rt.mux.ServeHTTP(w, r) }

// Mux exposes the underlying mux for non-named routes (static files).
func (rt *Router) Mux() *http.ServeMux { return rt.mux }

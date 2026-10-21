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
	mux    *http.ServeMux
	routes map[string]route
	mu     sync.RWMutex
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

// ServeHTTP dispatches to the registered handlers.
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) { rt.mux.ServeHTTP(w, r) }

// Mux exposes the underlying mux for non-named routes (static files).
func (rt *Router) Mux() *http.ServeMux { return rt.mux }

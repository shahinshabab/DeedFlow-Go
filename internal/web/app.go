// Package web is the HTTP application: routing, sessions, CSRF, templates and
// the page handlers.
package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/config"
	"github.com/shahinshabab/deedflow-go/internal/events"
	"github.com/shahinshabab/deedflow-go/internal/jobs"
	"github.com/shahinshabab/deedflow-go/internal/models"
	"github.com/shahinshabab/deedflow-go/internal/store"
	"github.com/shahinshabab/deedflow-go/web"
)

// App is the web application.
type App struct {
	Cfg    *config.Config
	Store  *store.Store
	Router *Router
	Hub    *events.Hub
	Jobs   *jobs.Queue
	Log    *slog.Logger

	tpl        *templates
	staticHash sync.Map // path -> short content hash
	handler    http.Handler
}

// New builds the application and its routes.
func New(cfg *config.Config, st *store.Store, hub *events.Hub, queue *jobs.Queue, log *slog.Logger) (*App, error) {
	a := &App{Cfg: cfg, Store: st, Router: NewRouter(), Hub: hub, Jobs: queue, Log: log}
	t, err := newTemplates(a)
	if err != nil {
		return nil, err
	}
	a.tpl = t
	a.routes()
	a.handler = a.middleware(a.Router)
	return a, nil
}

// ServeHTTP implements http.Handler.
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) { a.handler.ServeHTTP(w, r) }

// Handler is a page handler.
type Handler func(c *Ctx)

// Ctx is everything a handler needs about the current request.
type Ctx struct {
	W        http.ResponseWriter
	R        *http.Request
	App      *App
	Session  *Session
	User     *models.User
	ViewName string

	csrfSecret string
	csrfToken  string

	memberLoaded bool
	membership   *models.Membership
	org          *models.Organization
	orgLoaded    bool
	wroteCookie  bool
}

type ctxKey struct{}

func (c *Ctx) Context() context.Context { return c.R.Context() }

// Membership is the user's organization membership (nil if none).
func (c *Ctx) Membership() *models.Membership {
	if !c.memberLoaded {
		c.memberLoaded = true
		if c.User != nil {
			m, err := c.App.Store.MembershipOf(c.Context(), c.User.ID)
			if err != nil {
				c.App.Log.Error("membership lookup", "err", err)
			}
			c.membership = m
		}
	}
	return c.membership
}

// Org is the user's organization (nil if none).
func (c *Ctx) Org() *models.Organization {
	if !c.orgLoaded {
		c.orgLoaded = true
		if m := c.Membership(); m != nil {
			o, err := c.App.Store.OrganizationByID(c.Context(), m.OrganizationID)
			if err != nil {
				c.App.Log.Error("organization lookup", "err", err)
			}
			c.org = o
		}
	}
	return c.org
}

// IsOrgAdmin reports whether the user administers their organization.
func (c *Ctx) IsOrgAdmin() bool {
	m := c.Membership()
	return m != nil && m.IsAdmin()
}

// PathValue is the named path parameter.
func (c *Ctx) PathValue(name string) string { return c.R.PathValue(name) }

// Query is a URL query parameter.
func (c *Ctx) Query(name string) string { return c.R.URL.Query().Get(name) }

// PostForm parses and returns the submitted form (multipart included).
func (c *Ctx) PostForm() url.Values {
	if c.R.PostForm == nil {
		c.parseForm()
	}
	return c.R.PostForm
}

func (c *Ctx) parseForm() {
	ct := c.R.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		_ = c.R.ParseMultipartForm(32 << 20)
	} else {
		_ = c.R.ParseForm()
	}
	if c.R.PostForm == nil {
		c.R.PostForm = url.Values{}
	}
}

// Redirect sends a 302.
func (c *Ctx) Redirect(location string) { http.Redirect(c.W, c.R, location, http.StatusFound) }

// RedirectTo redirects to a named route.
func (c *Ctx) RedirectTo(name string, args ...any) {
	u, err := c.App.Router.Reverse(name, args...)
	if err != nil {
		c.ServerError(err)
		return
	}
	c.Redirect(u)
}

// URL reverses a route name.
func (c *Ctx) URL(name string, args ...any) string {
	u, err := c.App.Router.Reverse(name, args...)
	if err != nil {
		c.App.Log.Error("reverse", "route", name, "err", err)
	}
	return u
}

// Message queues a flash message ("success", "error", "info", "warning").
func (c *Ctx) Message(level, text string) { c.Session.addMessage(level, text) }

// NotFound answers 404.
func (c *Ctx) NotFound() { http.Error(c.W, "Not Found", http.StatusNotFound) }

// Forbidden answers 403.
func (c *Ctx) Forbidden(msg string) {
	if msg == "" {
		msg = "Forbidden"
	}
	http.Error(c.W, "403 Forbidden: "+msg, http.StatusForbidden)
}

// BadRequest answers 400.
func (c *Ctx) BadRequest(msg string) { http.Error(c.W, msg, http.StatusBadRequest) }

// ServerError logs and answers 500.
func (c *Ctx) ServerError(err error) {
	c.App.Log.Error("request failed", "path", c.R.URL.Path, "err", err)
	http.Error(c.W, "Server Error (500)", http.StatusInternalServerError)
}

// Notice returns the request's current message list without consuming it.
func (c *Ctx) isSecure() bool {
	return c.R.TLS != nil || c.R.Header.Get("X-Forwarded-Proto") == "https"
}

func (c *Ctx) setCookie(name, value string, maxAge time.Duration, httpOnly bool) {
	http.SetCookie(c.W, &http.Cookie{
		Name: name, Value: value, Path: "/", MaxAge: int(maxAge.Seconds()),
		HttpOnly: httpOnly, SameSite: http.SameSiteLaxMode, Secure: !c.App.Cfg.Debug,
	})
}

// Login attaches the user to a fresh session (rotating the key).
func (c *Ctx) Login(u *models.User) {
	if c.Session.row.Key != "" {
		_ = c.App.Store.DeleteSession(c.Context(), c.Session.row.Key)
	}
	data := map[string]any{}
	if msgs, ok := c.Session.Get("messages"); ok {
		data["messages"] = msgs
	}
	c.Session = newSession()
	c.Session.row.Data = data
	id := u.ID
	c.Session.row.UserID = &id
	c.Session.Set("auth_hash", authHash(c.App.Cfg.SecretKey, u))
	c.User = u
	c.memberLoaded, c.orgLoaded = false, false
	_ = c.App.Store.TouchLogin(c.Context(), u.ID)
}

// Logout ends the session.
func (c *Ctx) Logout() {
	_ = c.App.Store.DeleteSession(c.Context(), c.Session.row.Key)
	c.Session = newSession()
	c.User = nil
	c.memberLoaded, c.orgLoaded = false, false
}

// UpdateSessionAuthHash keeps the current session valid after the user's
// password changed (update_session_auth_hash).
func (c *Ctx) UpdateSessionAuthHash(u *models.User) {
	c.Session.Set("auth_hash", authHash(c.App.Cfg.SecretKey, u))
}

// --- middleware --------------------------------------------------------------

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// Flush and Hijack keep websockets and streaming working through the wrapper.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (a *App) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w := &statusWriter{ResponseWriter: rw}
		start := time.Now()
		defer func() {
			if rec := recover(); rec != nil {
				a.Log.Error("panic", "path", r.URL.Path, "panic", fmt.Sprint(rec))
				if w.status == 0 {
					http.Error(w, "Server Error (500)", http.StatusInternalServerError)
				}
			}
			if a.Cfg.Debug || w.status >= 500 {
				a.Log.Info("request", "method", r.Method, "path", r.URL.Path, "status", w.status, "ms", time.Since(start).Milliseconds())
			}
		}()
		if !a.Cfg.HostAllowed(r.Host) {
			http.Error(w, "Bad Request (400): invalid Host header", http.StatusBadRequest)
			return
		}
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// wrap turns a Handler into an http.Handler with sessions, CSRF and the
// route name attached.
func (a *App) wrap(name string, fn Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := &Ctx{W: w, R: r, App: a, ViewName: name}
		if !c.begin() {
			return
		}
		fn(c)
		c.end()
	})
}

// begin loads the session and user and enforces CSRF. It returns false when
// it already answered.
func (c *Ctx) begin() bool {
	a := c.App
	if ck, err := c.R.Cookie(sessionCookie); err == nil && ck.Value != "" {
		row, err := a.Store.LoadSession(c.Context(), ck.Value)
		if err != nil {
			c.ServerError(err)
			return false
		}
		if row != nil {
			c.Session = &Session{row: row}
		}
	}
	if c.Session == nil {
		c.Session = newSession()
	}
	if c.Session.row.UserID != nil {
		u, err := a.Store.UserByID(c.Context(), *c.Session.row.UserID)
		if err == nil && u.IsActive && c.Session.GetString("auth_hash") == authHash(a.Cfg.SecretKey, u) {
			c.User = u
		}
	}

	// CSRF secret cookie.
	if ck, err := c.R.Cookie(csrfCookie); err == nil && validCSRFSecret(ck.Value) {
		c.csrfSecret = ck.Value
	} else {
		c.csrfSecret = newCSRFSecret()
		c.setCookie(csrfCookie, c.csrfSecret, csrfAge, false)
	}
	c.csrfToken = maskCSRF(c.csrfSecret)

	if !isSafeMethod(c.R.Method) && !c.csrfOK() {
		a.Log.Warn("csrf failure", "path", c.R.URL.Path)
		http.Error(c.W, "403 Forbidden: CSRF verification failed. Request aborted.", http.StatusForbidden)
		return false
	}
	return true
}

func (c *Ctx) csrfOK() bool {
	if origin := c.R.Header.Get("Origin"); origin != "" && origin != "null" {
		u, err := url.Parse(origin)
		if err != nil || !c.sameHost(u.Host) {
			return false
		}
	} else if c.isSecure() {
		ref := c.R.Header.Get("Referer")
		if ref == "" {
			return false
		}
		u, err := url.Parse(ref)
		if err != nil || !c.sameHost(u.Host) {
			return false
		}
	}
	token := c.R.Header.Get("X-CSRFToken")
	if token == "" {
		token = c.PostForm().Get("csrfmiddlewaretoken")
	}
	return csrfEqual(c.csrfSecret, token)
}

func (c *Ctx) sameHost(h string) bool {
	host := c.R.Host
	if xf := c.R.Header.Get("X-Forwarded-Host"); xf != "" {
		host = xf
	}
	strip := func(s string) string {
		if hh, _, err := net.SplitHostPort(s); err == nil {
			return strings.ToLower(hh)
		}
		return strings.ToLower(s)
	}
	return strings.EqualFold(h, host) || strip(h) == strip(host) && c.App.Cfg.HostAllowed(h)
}

// end persists the session and sets its cookie.
func (c *Ctx) end() {
	// A handler that already wrote its response can't set cookies any more;
	// sessions are saved by the helpers that need them (Render, Redirect).
}

// SaveSession writes the session and its cookie. Handlers call it through
// Render/Redirect helpers before writing the response.
func (c *Ctx) SaveSession() {
	if c.wroteCookie {
		return
	}
	if c.Session.dirty || c.Session.row.UserID != nil || c.User != nil {
		if c.User != nil {
			id := c.User.ID
			c.Session.row.UserID = &id
		}
		if c.Session.dirty {
			if err := c.App.Store.SaveSession(c.Context(), c.Session.row); err != nil {
				c.App.Log.Error("saving session", "err", err)
			}
			c.Session.dirty = false
		}
		c.setCookie(sessionCookie, c.Session.row.Key, sessionAge, true)
		c.wroteCookie = true
	}
}

// --- authentication wrappers ----------------------------------------------------

// LoginRequired redirects anonymous visitors to the login page.
func (a *App) LoginRequired(fn Handler) Handler {
	return func(c *Ctx) {
		if c.User == nil {
			next := c.R.URL.RequestURI()
			c.SaveSession()
			c.Redirect("/login/?next=" + url.QueryEscape(next))
			return
		}
		fn(c)
	}
}

// OrgAdminRequired is LoginRequired plus: only the organization's admin gets in.
func (a *App) OrgAdminRequired(fn Handler) Handler {
	return a.LoginRequired(func(c *Ctx) {
		if !c.IsOrgAdmin() {
			c.Forbidden("Only your organization's admin can open this page.")
			return
		}
		fn(c)
	})
}

// StaffRequired is LoginRequired plus: only Django-admin staff users.
func (a *App) StaffRequired(fn Handler) Handler {
	return a.LoginRequired(func(c *Ctx) {
		if !c.User.IsStaff {
			c.Redirect("/login/?next=" + url.QueryEscape(c.R.URL.RequestURI()))
			return
		}
		fn(c)
	})
}

// Route registers a handler for a named route.
func (a *App) Route(name, path string, fn Handler) {
	a.Router.Handle(name, path, a.wrap(name, fn))
}

// --- static & media ------------------------------------------------------------

// StaticURL is the URL of a static file with a content hash for cache busting.
func (a *App) StaticURL(p string) string {
	p = strings.TrimPrefix(p, "/")
	if v, ok := a.staticHash.Load(p); ok {
		return "/static/" + p + "?v=" + v.(string)
	}
	data, err := fs.ReadFile(web.FS, path.Join("static", p))
	if err != nil {
		return "/static/" + p
	}
	sum := sha256.Sum256(data)
	h := hex.EncodeToString(sum[:])[:10]
	a.staticHash.Store(p, h)
	return "/static/" + p + "?v=" + h
}

func (a *App) staticHandler() http.Handler {
	sub, _ := fs.Sub(web.FS, "static")
	fileServer := http.FileServer(http.FS(sub))
	return http.StripPrefix("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("v") != "" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}
		fileServer.ServeHTTP(w, r)
	}))
}

// MediaPath resolves a stored media name to a file path, refusing anything
// outside MEDIA_ROOT.
func (a *App) MediaPath(name string) (string, error) {
	clean := filepath.Clean("/" + name)
	full := filepath.Join(a.Cfg.MediaRoot, clean)
	if !strings.HasPrefix(full, filepath.Clean(a.Cfg.MediaRoot)+string(filepath.Separator)) {
		return "", errors.New("path outside media root")
	}
	return full, nil
}

// SaveMedia writes data to MEDIA_ROOT/name (creating directories), picking a
// fresh name if one exists, and returns the stored name.
func (a *App) SaveMedia(name string, data []byte) (string, error) {
	dir, base := path.Split(name)
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	candidate := name
	for i := 0; ; i++ {
		full, err := a.MediaPath(candidate)
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return "", err
		}
		f, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			_, werr := f.Write(data)
			cerr := f.Close()
			if werr != nil {
				return "", werr
			}
			return candidate, cerr
		}
		if !os.IsExist(err) {
			return "", err
		}
		suffix := fmt.Sprintf("_%s", randomSuffix(7))
		candidate = dir + stem + suffix + ext
		if i > 20 {
			return "", errors.New("could not find a free file name")
		}
	}
}

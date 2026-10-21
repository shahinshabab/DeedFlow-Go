package web

import (
	"context"
	_ "embed"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/flosch/pongo2/v6"

	"github.com/shahinshabab/deedflow-go/internal/auth"
)

// The admin site (Django's django.contrib.admin, as the DeedFlow project uses
// it): the models registered in the Django project's admin modules, with their
// changelists, forms and inlines. Pages are rendered by Django's own admin
// templates (web/templates/admin), fed by the code in the admin_*.go files.

//go:embed admin_jsi18n.js
var adminJSI18N []byte

var adminSiteSpec = loadAdminSite()

// adminPerms are a user's permissions in the admin.
type adminPerms struct {
	super bool
	set   map[string]bool // "app_label.codename"
}

func (p *adminPerms) has(m *modelSpec, action string) bool {
	return p.super || p.set[m.AppLabel+"."+action+"_"+m.ModelName]
}

func (p *adminPerms) hasModule(m *modelSpec) bool {
	return p.has(m, "add") || p.has(m, "change") || p.has(m, "delete") || p.has(m, "view")
}

func (p *adminPerms) hasAny(app string) bool {
	if p.super {
		return true
	}
	for k := range p.set {
		if strings.HasPrefix(k, app+".") {
			return true
		}
	}
	return false
}

func (a *App) loadAdminPerms(ctx context.Context, userID int64, super bool) (*adminPerms, error) {
	p := &adminPerms{super: super, set: map[string]bool{}}
	if super {
		return p, nil
	}
	rows, err := a.Store.Querier().Query(ctx, `SELECT ct.app_label, p.codename FROM auth_permission p
		JOIN django_content_type ct ON ct.id = p.content_type_id
		WHERE p.id IN (SELECT permission_id FROM auth_user_user_permissions WHERE user_id = $1
			UNION SELECT gp.permission_id FROM auth_group_permissions gp
			JOIN auth_user_groups ug ON ug.group_id = gp.group_id WHERE ug.user_id = $1)`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var app, code string
		if err := rows.Scan(&app, &code); err != nil {
			return nil, err
		}
		p.set[app+"."+code] = true
	}
	return p, rows.Err()
}

// adminReq is one admin request.
type adminReq struct {
	c     *Ctx
	a     *App
	perms *adminPerms
}

// adminView wraps an admin page: only active staff get in, anyone else is
// sent to the admin login page.
func (a *App) adminView(fn func(r *adminReq)) Handler {
	return func(c *Ctx) {
		if c.User == nil || !c.User.IsActive || !c.User.IsStaff {
			c.SaveSession()
			c.Redirect("/admin/login/?next=" + url.QueryEscape(c.R.URL.RequestURI()))
			return
		}
		perms, err := a.loadAdminPerms(c.Context(), c.User.ID, c.User.IsSuperuser)
		if err != nil {
			c.ServerError(err)
			return
		}
		fn(&adminReq{c: c, a: a, perms: perms})
	}
}

func isUsablePassword(p string) bool { return p != "" && !strings.HasPrefix(p, "!") }

func (r *adminReq) userMap() map[string]any {
	u := r.c.User
	short := u.FirstName
	if short == "" {
		short = u.Username
	}
	return map[string]any{
		"is_authenticated": true, "is_anonymous": false, "is_active": u.IsActive, "is_staff": u.IsStaff,
		"is_superuser": u.IsSuperuser, "id": u.ID, "username": u.Username,
		"get_short_name": u.FirstName, "get_username": u.Username, "has_usable_password": isUsablePassword(u.Password),
	}
}

// utcOffset is the office clock's offset from UTC in seconds (data-admin-utc-offset).
func (a *App) utcOffset() int {
	_, off := time.Now().In(a.Cfg.TimeZone).Zone()
	return off
}

// adminModelEntry is a model in the app lists.
func (r *adminReq) modelEntry(m *modelSpec) map[string]any {
	e := map[string]any{
		"name": capfirst(m.VerboseNamePlural), "object_name": m.ModelName,
		"perms": map[string]any{"add": r.perms.has(m, "add"), "change": r.perms.has(m, "change"),
			"delete": r.perms.has(m, "delete"), "view": r.perms.has(m, "view")},
		"admin_url": "", "add_url": "", "view_only": false,
	}
	if r.perms.has(m, "change") || r.perms.has(m, "view") {
		e["admin_url"] = r.a.adminURL(m, "changelist")
		e["view_only"] = !r.perms.has(m, "change")
	}
	if r.perms.has(m, "add") {
		e["add_url"] = r.a.adminURL(m, "add")
	}
	return e
}

func (a *App) adminURL(m *modelSpec, action string, args ...any) string {
	u, err := a.Router.Reverse(fmt.Sprintf("admin:%s_%s_%s", m.AppLabel, m.ModelName, action), args...)
	if err != nil {
		a.Log.Error("admin reverse", "err", err)
	}
	return u
}

// appList is AdminSite.get_app_list: the apps whose models the user may
// reach, sorted by name, each with its models sorted by name.
func (r *adminReq) appList(only string) []any {
	type app struct {
		label, name string
		models      []map[string]any
	}
	var apps []*app
	byLabel := map[string]*app{}
	for _, m := range adminSiteSpec.order {
		if only != "" && m.AppLabel != only {
			continue
		}
		if !r.perms.hasModule(m) {
			continue
		}
		ap := byLabel[m.AppLabel]
		if ap == nil {
			ap = &app{label: m.AppLabel, name: adminSiteSpec.spec.Apps[m.AppLabel]}
			byLabel[m.AppLabel] = ap
			apps = append(apps, ap)
		}
		ap.models = append(ap.models, r.modelEntry(m))
	}
	sort.SliceStable(apps, func(i, j int) bool { return strings.ToLower(apps[i].name) < strings.ToLower(apps[j].name) })
	out := make([]any, len(apps))
	for i, ap := range apps {
		sort.SliceStable(ap.models, func(x, y int) bool { return ap.models[x]["name"].(string) < ap.models[y]["name"].(string) })
		ml := make([]any, len(ap.models))
		for k, m := range ap.models {
			ml[k] = m
		}
		appURL, _ := r.a.Router.Reverse("admin:app_list", ap.label)
		out[i] = map[string]any{"name": ap.name, "app_label": ap.label, "app_url": appURL, "has_module_perms": true, "models": ml}
	}
	return out
}

// base is AdminSite.each_context plus the page's title.
func (r *adminReq) base(title string) pongo2.Context {
	c := r.c
	index, _ := r.a.Router.Reverse("admin:index")
	return pongo2.Context{
		"title": title, "subtitle": nil, "site_title": nil, "site_header": nil, "site_url": "/",
		"has_permission": true, "is_popup": false, "is_nav_sidebar_enabled": true,
		"available_apps": r.appList(""), "user": r.userMap(), "request": map[string]any{"path": c.R.URL.Path},
		"messages": messageMaps(c.Session.popMessages()), "utc_offset": r.a.utcOffset(), "csrf_token": c.csrfToken,
		"index_url": index,
	}
}

func (r *adminReq) render(name string, ctx pongo2.Context) {
	r.c.renderContext(http.StatusOK, name, ctx)
}

// --- login and logout ---------------------------------------------------------------------

// boundField is a form field as the admin's templates read it: it prints as
// its widget.
type boundField map[string]any

func (b boundField) String() string { return b["_html"].(string) }

func newBoundField(name, label string, required bool, widgetHTML string, errs []string, helpText string) boundField {
	id := "id_" + name
	labelTag := `<label for="` + id + `"` + map[bool]string{true: ` class="required"`, false: ""}[required] + `>` + esc(label) + `:</label>`
	return boundField{
		"_html": widgetHTML, "name": name, "label": label, "id_for_label": id, "help_text": helpText,
		"errors": safe(errorList(errs)), "label_tag": safe(labelTag), "html_name": name,
		"has_errors": len(errs) > 0,
	}
}

type loginForm struct {
	username, password string
	errs               map[string][]string
	nonField           []string
	bound              bool
}

func (f *loginForm) fields() map[string]any {
	uAttrs := []attr{{"type", "text"}, {"name", "username"}}
	if f.bound && f.username != "" {
		uAttrs = append(uAttrs, attr{"value", f.username})
	}
	uAttrs = append(uAttrs, attr{"autofocus", true}, attr{"autocapitalize", "none"}, attr{"autocomplete", "username"},
		attr{"maxlength", "150"}, attr{"required", true})
	if len(f.errs["username"]) > 0 {
		uAttrs = append(uAttrs, attr{"aria-invalid", "true"})
	}
	uAttrs = append(uAttrs, attr{"id", "id_username"})
	pAttrs := []attr{{"type", "password"}, {"name", "password"}, {"autocomplete", "current-password"}, {"required", true}}
	if len(f.errs["password"]) > 0 {
		pAttrs = append(pAttrs, attr{"aria-invalid", "true"})
	}
	pAttrs = append(pAttrs, attr{"id", "id_password"})
	errCount := 0
	for _, e := range f.errs {
		if len(e) > 0 {
			errCount++
		}
	}
	return map[string]any{
		"username":         newBoundField("username", "Username", true, "<input"+attrsHTML(uAttrs)+">", f.errs["username"], ""),
		"password":         newBoundField("password", "Password", true, "<input"+attrsHTML(pAttrs)+">", f.errs["password"], ""),
		"errors":           f.errMap(),
		"non_field_errors": f.nonField,
		"media":            "",
	}
}

// errMap is form.errors: something with .items|length and truthiness.
func (f *loginForm) errMap() map[string]any {
	m := map[string]any{}
	for k, v := range f.errs {
		if len(v) > 0 {
			m[k] = v
		}
	}
	if len(f.nonField) > 0 {
		m["__all__"] = f.nonField
	}
	return m
}

func (a *App) adminLogin(c *Ctx) {
	if c.R.Method == http.MethodGet && c.User != nil && c.User.IsActive && c.User.IsStaff {
		next := c.R.URL.Query().Get("next")
		dest := c.safeNext(next)
		if dest == "" {
			dest = "/admin/"
		}
		c.SaveSession()
		c.Redirect("/admin/")
		return
	}
	r := &adminReq{c: c, a: a}
	form := &loginForm{errs: map[string][]string{}}
	next := ""
	postNext := ""
	if c.R.Method == http.MethodPost {
		pf := c.PostForm()
		postNext = pf.Get("next")
		form.bound = true
		form.username = strings.TrimSpace(pf.Get("username"))
		form.password = pf.Get("password")
		if form.username == "" {
			form.errs["username"] = []string{"This field is required."}
		}
		if form.password == "" {
			form.errs["password"] = []string{"This field is required."}
		}
		if len(form.errs) == 0 {
			user, err := a.Store.UserByUsername(c.Context(), form.username)
			ok := err == nil && user.IsActive && auth.CheckPassword(form.password, user.Password)
			if err != nil {
				auth.CheckPassword(form.password, auth.MakePassword("x"))
			}
			if !ok || !user.IsStaff {
				form.nonField = []string{"Please enter the correct username and password for a staff account. Note that both fields may be case-sensitive."}
			} else {
				if auth.NeedsRehash(user.Password) {
					user.Password = auth.MakePassword(form.password)
					_ = a.Store.SaveUser(c.Context(), user, "password")
				}
				c.Login(user)
				dest := c.safeNext(postNext)
				if dest == "" {
					dest = "/admin/"
				}
				c.SaveSession()
				c.Redirect(dest)
				return
			}
		}
	}
	_, hasGet := c.R.URL.Query()["next"]
	_, hasPost := c.R.PostForm["next"]
	if c.R.Method == http.MethodPost {
		next = c.safeNext(postNext)
	} else {
		next = c.safeNext(c.R.URL.Query().Get("next"))
	}
	if !hasGet && !hasPost {
		next = "/admin/"
	}
	user := map[string]any{"is_authenticated": c.User != nil, "is_anonymous": c.User == nil}
	username := ""
	if c.User != nil {
		username = c.User.Username
	}
	ctx := pongo2.Context{
		"title": "Log in", "subtitle": nil, "site_title": nil, "site_header": nil, "site_url": "/",
		"has_permission": false, "is_popup": false, "is_nav_sidebar_enabled": true,
		"available_apps": []any{}, "user": user, "request": map[string]any{"path": c.R.URL.Path},
		"messages": messageMaps(c.Session.popMessages()), "utc_offset": a.utcOffset(), "csrf_token": c.csrfToken,
		"app_path": c.R.URL.RequestURI(), "username": username, "next": next, "form": form.fields(),
	}
	status := http.StatusOK
	r.c.renderContext(status, "admin/login.html", ctx)
}

func (a *App) adminLogout(c *Ctx) {
	if c.R.Method != http.MethodPost {
		c.W.Header().Set("Allow", "POST")
		http.Error(c.W, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	c.Logout()
	ctx := pongo2.Context{
		"title": "Logged out", "subtitle": nil, "site_title": nil, "site_header": nil, "site_url": "/",
		"has_permission": false, "is_popup": false, "is_nav_sidebar_enabled": true,
		"available_apps": []any{}, "user": map[string]any{"is_authenticated": false, "is_anonymous": true},
		"request": map[string]any{"path": c.R.URL.Path}, "messages": messageMaps(nil),
		"utc_offset": a.utcOffset(), "csrf_token": c.csrfToken,
	}
	c.renderContext(http.StatusOK, "registration/logged_out.html", ctx)
}

// --- index and app index -----------------------------------------------------------------------

func (a *App) adminIndex(r *adminReq) {
	ctx := r.base("Site administration")
	ctx["app_list"] = r.appList("")
	ctx["admin_log"] = r.recentActions(10)
	r.render("admin/index.html", ctx)
}

func (a *App) adminAppIndex(r *adminReq) {
	label := r.c.PathValue("app_label")
	known := false
	for _, m := range adminSiteSpec.order {
		if m.AppLabel == label {
			known = true
		}
	}
	list := r.appList(label)
	if !known || len(list) == 0 {
		a.adminNotFound(r.c)
		return
	}
	ctx := r.base(adminSiteSpec.spec.Apps[label] + " administration")
	ctx["subtitle"] = nil
	ctx["app_list"] = list
	ctx["app_label"] = label
	r.render("admin/app_index.html", ctx)
}

// adminNotFound is Django's 404 page.
func (a *App) adminNotFound(c *Ctx) {
	http.Error(c.W, "Not Found", http.StatusNotFound)
}

func (a *App) adminJSI18N(r *adminReq) {
	r.c.W.Header().Set("Content-Type", `text/javascript; charset="utf-8"`)
	_, _ = r.c.W.Write(adminJSI18N)
}

func (a *App) adminPasswordChange(r *adminReq)                      { a.adminNotFound(r.c) }
func (a *App) adminPasswordChangeDone(r *adminReq)                  { a.adminNotFound(r.c) }
func (a *App) adminAutocomplete(r *adminReq)                        { a.adminNotFound(r.c) }
func (a *App) adminViewOnSite(r *adminReq)                          { a.adminNotFound(r.c) }
func (a *App) adminUserPassword(r *adminReq, m *modelSpec)          { a.adminNotFound(r.c) }
func (a *App) adminChangelist(r *adminReq, m *modelSpec)            { a.adminNotFound(r.c) }
func (a *App) adminChangeForm(r *adminReq, m *modelSpec, pk string) { a.adminNotFound(r.c) }
func (a *App) adminHistory(r *adminReq, m *modelSpec)               { a.adminNotFound(r.c) }
func (a *App) adminDelete(r *adminReq, m *modelSpec)                { a.adminNotFound(r.c) }

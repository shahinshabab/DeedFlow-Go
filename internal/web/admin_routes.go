package web

import (
	"net/http"
	"strings"
)

// registerAdmin mounts the admin under /admin/ with Django's URL names
// (admin:index, admin:deeds_deed_change ...) in Django's order.
func (a *App) registerAdmin() {
	reg := func(name, path string, h Handler) {
		a.Router.HandleOrdered("admin:"+name, path, a.wrap("admin:"+name, h))
	}
	view := func(fn func(r *adminReq)) Handler { return a.adminView(fn) }

	reg("index", "admin/", view(a.adminIndex))
	reg("login", "admin/login/", a.adminLogin)
	reg("logout", "admin/logout/", a.adminLogout)
	reg("password_change", "admin/password_change/", view(a.adminPasswordChange))
	reg("password_change_done", "admin/password_change/done/", view(a.adminPasswordChangeDone))
	reg("autocomplete", "admin/autocomplete/", view(a.adminAutocomplete))
	reg("jsi18n", "admin/jsi18n/", view(a.adminJSI18N))
	reg("view_on_site", "admin/r/<int:content_type_id>/<path:object_id>/", view(a.adminViewOnSite))

	for _, m := range adminSiteSpec.order {
		m := m
		base := "admin/" + m.AppLabel + "/" + m.ModelName + "/"
		prefix := m.AppLabel + "_" + m.ModelName + "_"
		if m.Label == "auth.user" {
			reg(prefix+"password_change", base+"<id>/password/", view(func(r *adminReq) { a.adminUserPassword(r, m) }))
		}
		reg(prefix+"changelist", base, view(func(r *adminReq) { a.adminChangelist(r, m) }))
		reg(prefix+"add", base+"add/", view(func(r *adminReq) { a.adminChangeForm(r, m, "") }))
		reg(prefix+"history", base+"<path:object_id>/history/", view(func(r *adminReq) { a.adminHistory(r, m) }))
		reg(prefix+"delete", base+"<path:object_id>/delete/", view(func(r *adminReq) { a.adminDelete(r, m) }))
		reg(prefix+"change", base+"<path:object_id>/change/", view(func(r *adminReq) {
			a.adminChangeForm(r, m, adminUnquote(r.c.PathValue("object_id")))
		}))
		reg(prefix+"redirect", base+"<path:object_id>/", view(func(r *adminReq) {
			r.c.Redirect(a.adminURL(m, "change", r.c.PathValue("object_id")))
		}))
	}
	reg("app_list", "admin/<str:app_label>/", view(a.adminAppIndex))
	reg("catch_all", "admin/<path:url>", a.adminCatchAll)
	a.Router.MountOrdered("admin/")
}

// adminCatchAll is AdminSite.catch_all_view.
func (a *App) adminCatchAll(c *Ctx) {
	if c.User == nil || !c.User.IsActive || !c.User.IsStaff {
		c.SaveSession()
		c.Redirect(loginRedirect("/admin/login/", c.R.URL.RequestURI()))
		return
	}
	if !strings.HasSuffix(c.R.URL.Path, "/") {
		if a.Router.Resolves(c.R.URL.Path + "/") {
			http.Redirect(c.W, c.R, c.R.URL.Path+"/", http.StatusMovedPermanently)
			return
		}
	}
	a.adminNotFound(c)
}

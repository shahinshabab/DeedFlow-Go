package web

import "net/http"

// routes registers every page.
func (a *App) routes() {
	a.Router.Mux().Handle("GET /static/", a.staticHandler())
	a.registerCore()
	a.registerAccounts()
	a.registerDeeds()
	a.registerStubs()
	// Admin entry point for the sidebar link (the admin area itself lives in admin.go).
	a.Route("admin:index", "admin/", a.StaffRequired(func(c *Ctx) { c.Render("admin/index.html", nil) }))
	_ = http.StatusOK
}

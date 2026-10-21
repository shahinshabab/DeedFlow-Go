package web

import (
	"net/url"
	"strings"

	"github.com/shahinshabab/deedflow-go/internal/auth"
)

func (a *App) registerCore() {
	a.Route("core:dashboard", "", a.LoginRequired(func(c *Ctx) {
		c.Render("core/dashboard.html", nil)
	}))
	a.Route("login", "login/", a.loginView)
	a.Route("logout", "logout/", func(c *Ctx) {
		if c.R.Method != "POST" {
			c.W.Header().Set("Allow", "POST")
			http_error(c, 405, "Method Not Allowed")
			return
		}
		c.Logout()
		c.SaveSession()
		c.Redirect("/login/")
	})
}

func http_error(c *Ctx, code int, msg string) { http_Error(c.W, msg, code) }

// safeNext returns next if it is a same-site path, else "".
func (c *Ctx) safeNext(next string) string {
	if next == "" {
		return ""
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") ||
		strings.Contains(next, "\\") {
		return ""
	}
	return next
}

func (a *App) loginView(c *Ctx) {
	next := c.R.URL.Query().Get("next")
	if c.R.Method == "POST" {
		form := c.PostForm()
		next = form.Get("next")
		username, pw := form.Get("username"), form.Get("password")
		user, err := a.Store.UserByUsername(c.Context(), username)
		if err == nil && user.IsActive && auth.CheckPassword(pw, user.Password) {
			if auth.NeedsRehash(user.Password) {
				user.Password = auth.MakePassword(pw)
				_ = a.Store.SaveUser(c.Context(), user, "password")
			}
			c.Login(user)
			dest := c.safeNext(next)
			if dest == "" {
				dest = "/"
			}
			c.SaveSession()
			c.Redirect(dest)
			return
		}
		// Burn the same time whether or not the user exists.
		if err != nil {
			auth.CheckPassword(pw, auth.MakePassword("x"))
		}
		c.Render("registration/login.html", Data{"login_error": true, "next": next})
		return
	}
	c.Render("registration/login.html", Data{"next": next})
}

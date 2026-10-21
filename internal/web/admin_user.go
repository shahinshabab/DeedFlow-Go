package web

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/flosch/pongo2/v6"

	"github.com/shahinshabab/deedflow-go/internal/auth"
	"github.com/shahinshabab/deedflow-go/internal/portalcrypt"
)

// PortalCredentialAdminForm: new_password is a write-only field; filled in,
// it is encrypted into portal_password.
func (a *App) credentialFormSetup(v *cfView) {
	f := v.form
	f.postClean = func(f *aForm) {
		if pw, _ := f.cleaned["new_password"].(string); pw != "" && f.vals != nil {
			enc, err := portalcrypt.Encrypt(a.Cfg.FieldEncryptionKey, pw)
			if err == nil {
				f.vals["portal_password"] = enc
				if f.fields["portal_password"] == nil {
					// the column is not a form field: make sure it is written
					f.fields["portal_password"] = &aField{name: "portal_password", mf: f.m.field("portal_password"), kind: "char", spec: &formFieldSpec{}, w: &widgetSpec{}, wi: &widgetSpec{}}
				}
			}
		}
	}
}

// withAttr copies a field's widget so an attribute can be added.
func (fl *aField) withAttr(k string, val any) {
	w := *fl.w
	attrs := map[string]any{}
	for kk, vv := range w.Attrs {
		attrs[kk] = vv
	}
	attrs[k] = val
	w.Attrs = attrs
	fl.w, fl.wi = &w, &w
}

// userFormSetup makes the user forms behave like AdminUserCreationForm and UserChangeForm.
func (a *App) userFormSetup(v *cfView) {
	f := v.form
	if !v.add {
		if fl := f.fields["password"]; fl != nil && v.obj != nil && !hasUsablePassword(valueString(v.obj.vals["password"])) {
			fl.help = "Enable password-based authentication for this user by setting a password."
		}
		return
	}
	if fl := f.fields["username"]; fl != nil {
		fl.withAttr("autofocus", true)
	}
	for _, n := range []string{"password1", "password2"} {
		if fl := f.fields[n]; fl != nil {
			fl.required = false
		}
	}
	f.cleanFld = map[string]func(f *aForm, v any) (any, string){
		// reject usernames that differ only in case
		"username": func(f *aForm, v any) (any, string) {
			s, _ := v.(string)
			var one int
			err := f.r.q().QueryRow(f.ctx, `SELECT 1 FROM auth_user WHERE UPPER(username) = UPPER($1) LIMIT 1`, s).Scan(&one)
			if err == nil {
				return nil, "A user with that username already exists."
			}
			return v, ""
		},
	}
	f.formClean = func(f *aForm) { validatePasswords(f, "password1", "password2", "usable_password", true) }
	f.postClean = func(f *aForm) {
		usable, _ := f.cleaned["set_usable_password"].(bool)
		if usable {
			validatePasswordForUser(f, "password2", f.userAttrs())
		}
		if f.hasErrors() {
			return
		}
		if usable {
			pw, _ := f.cleaned["password1"].(string)
			f.vals["password"] = auth.MakePassword(pw)
		} else {
			f.vals["password"] = auth.UnusablePassword()
		}
	}
}

func (f *aForm) userAttrs() *auth.UserAttrs {
	get := func(n string) string { return valueString(f.vals[n]) }
	return &auth.UserAttrs{Username: get("username"), FirstName: get("first_name"), LastName: get("last_name"), Email: get("email")}
}

// validatePasswords is SetUnusablePasswordMixin.validate_passwords.
func validatePasswords(f *aForm, p1n, p2n, usableName string, hasUsable bool) {
	usable := true
	if fl := f.fields[usableName]; fl != nil {
		if s, _ := f.cleaned[usableName].(string); s == "false" {
			usable = false
		}
		delete(f.cleaned, usableName)
	}
	f.cleaned["set_usable_password"] = usable
	if !usable {
		return
	}
	p1, _ := f.cleaned[p1n].(string)
	p2, _ := f.cleaned[p2n].(string)
	if p1 == "" && len(f.errs[p1n]) == 0 {
		f.addError(p1n, msgRequired)
	}
	if p2 == "" && len(f.errs[p2n]) == 0 {
		f.addError(p2n, msgRequired)
	}
	if p1 != "" && p2 != "" && p1 != p2 {
		f.addError(p2n, "The two password fields didn’t match.")
	}
}

// validatePasswordForUser runs AUTH_PASSWORD_VALIDATORS on the new password.
func validatePasswordForUser(f *aForm, field string, user *auth.UserAttrs) {
	pw, _ := f.cleaned[field].(string)
	if pw == "" {
		return
	}
	for _, msg := range auth.ValidatePassword(pw, user) {
		f.addError(field, msg)
	}
}

func (a *App) userAddContext(v *cfView, ctx pongo2.Context) {
	ctx["auto_populated_fields"] = []any{}
	ctx["username_help_text"] = v.form.fields["username"].help
}

func (a *App) userChangeContext(v *cfView, ctx pongo2.Context) {}

// --- the password change page of a user ---------------------------------------------------------

// adminUserPassword is UserAdmin.user_change_password.
func (a *App) adminUserPassword(r *adminReq, m *modelSpec) {
	c := r.c
	id := adminUnquote(c.PathValue("id"))
	obj, err := a.loadObj(c.Context(), m, id)
	if err != nil {
		c.ServerError(err)
		return
	}
	if !r.perms.has(m, "change") {
		a.adminForbiddenPage(c)
		return
	}
	if obj == nil {
		http.Error(c.W, fmt.Sprintf("User object with primary key %s does not exist.", "'"+id+"'"), http.StatusNotFound)
		return
	}
	usableNow := hasUsablePassword(valueString(obj.vals["password"]))
	specs := map[string]*formFieldSpec{}
	for _, n := range []string{"password1", "password2", "usable_password"} {
		if sp := m.AddFormFields[n]; sp != nil {
			cp := *sp
			specs[n] = &cp
		}
	}
	order := []string{"password1", "password2"}
	if usableNow {
		order = []string{"usable_password", "password1", "password2"}
		u := specs["usable_password"]
		u.HelpText = u.HelpText + `<ul id="id_unusable_warning" class="messagelist"><li class="warning">If disabled, the current password for this user will be lost.</li></ul>`
	}
	form := a.newForm(r, m, specs, order, "")
	form.modelOnly = true
	form.inst = obj
	for _, n := range order {
		form.fields[n].spec = specs[n]
	}
	form.fields["password1"].withAttr("autofocus", true)
	if usableNow {
		form.fields["password1"].required = false
		form.fields["password2"].required = false
		form.initial["usable_password"] = "true"
	} else {
		form.fields["password1"].required = true
		form.fields["password2"].required = true
	}
	form.formClean = func(f *aForm) {
		validatePasswords(f, "password1", "password2", "usable_password", true)
		if usable, _ := f.cleaned["set_usable_password"].(bool); usable {
			validatePasswordForUser(f, "password2", &auth.UserAttrs{
				Username: valueString(obj.vals["username"]), FirstName: valueString(obj.vals["first_name"]),
				LastName: valueString(obj.vals["last_name"]), Email: valueString(obj.vals["email"])})
		}
	}
	if c.R.Method == http.MethodPost {
		pf := c.PostForm()
		form.bind(pf, nil)
		if form.isValid() {
			usable, _ := form.cleaned["set_usable_password"].(bool)
			if !usable && !hasKeyValues(pf, "unset-password") {
				c.Session.addMessage("error", "Conflicting form data submitted. Please try again.")
				c.SaveSession()
				c.Redirect(c.R.URL.RequestURI())
				return
			}
			pw := auth.UnusablePassword()
			if usable {
				pw, _ = form.cleaned["password1"].(string)
				pw = auth.MakePassword(pw)
			}
			changed := "[]"
			if p1, p2 := pf.Get("password1"), pf.Get("password2"); p1 != "" && p2 != "" {
				changed = `[{"changed": {"fields": ["password"]}}]`
			}
			err := a.withTx(r, func() error {
				if _, err := r.q().Exec(c.Context(), `UPDATE auth_user SET password = $1 WHERE id = $2`, pw, obj.vals["id"]); err != nil {
					return err
				}
				re := a.newRepr(c.Context())
				return a.logAction(c.Context(), c.User.ID, m, obj.pkString(), re.str(obj), logChange, changed)
			})
			if err != nil {
				c.ServerError(err)
				return
			}
			if usable {
				c.Session.addMessage("success", "Password changed successfully.")
			} else {
				c.Session.addMessage("success", "Password-based authentication was disabled.")
			}
			if c.User.ID == obj.vals["id"].(int64) {
				if u, err := a.Store.UserByID(c.Context(), c.User.ID); err == nil {
					c.UpdateSessionAuthHash(u)
				}
			}
			c.SaveSession()
			c.Redirect(a.adminURL(m, "change", obj.pkString()))
			return
		}
	}
	title := "Change password: " + esc(valueString(obj.vals["username"]))
	if !usableNow {
		title = "Set password: " + esc(valueString(obj.vals["username"]))
	}
	ctx := r.base(title)
	re := a.newRepr(c.Context())
	ctx["opts"] = a.adminOpts(m)
	ctx["original_repr"] = re.str(obj)
	ctx["original_pk"] = obj.pkString()
	ctx["original_username"] = valueString(obj.vals["username"])
	ctx["form_url"] = ""
	ctx["is_popup"] = false
	ctx["is_popup_var"] = popupVar
	fm := map[string]any{"user": map[string]any{"has_usable_password": usableNow}}
	for _, n := range order {
		fm[n] = form.boundMap(form.fields[n])
	}
	errs := map[string]any{}
	if form.bound {
		for _, n := range form.errOrder {
			errs[n] = form.errs[n]
		}
	}
	items := []any{}
	for _, n := range form.errOrder {
		items = append(items, []any{n, form.errs[n]})
	}
	if len(errs) > 0 {
		fm["errors"] = map[string]any{"items": items}
	} else {
		fm["errors"] = nil
	}
	ctx["form"] = fm
	c.renderContext(http.StatusOK, "admin/auth/user/change_password.html", ctx)
}

var _ = strings.Join

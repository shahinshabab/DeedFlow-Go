package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/shahinshabab/deedflow-go/internal/auth"
	"github.com/shahinshabab/deedflow-go/internal/config"
	"github.com/shahinshabab/deedflow-go/internal/forms"
	"github.com/shahinshabab/deedflow-go/internal/models"
	"github.com/shahinshabab/deedflow-go/internal/portalcrypt"
	"github.com/shahinshabab/deedflow-go/internal/store"
)

func http_Error(w http.ResponseWriter, msg string, code int) { http.Error(w, msg, code) }

func (a *App) registerAccounts() {
	a.Route("accounts:profile", "account/profile/", a.LoginRequired(a.profile))
	a.Route("accounts:organization", "account/organization/", a.OrgAdminRequired(a.organization))
	a.Route("accounts:staff_toggle_active", "account/organization/staff/<int:user_id>/active/", a.OrgAdminRequired(a.staffToggleActive))
	a.Route("accounts:staff_password", "account/organization/staff/<int:user_id>/password/", a.OrgAdminRequired(a.staffPassword))
}

func (c *Ctx) attrs() *auth.UserAttrs {
	u := c.User
	return &auth.UserAttrs{Username: u.Username, FirstName: u.FirstName, LastName: u.LastName, Email: u.Email}
}

func (a *App) profile(c *Ctx) {
	form := setPasswordForm("", c.attrs(), true)
	if c.R.Method == "POST" && c.PostForm().Has("change_password") {
		form = setPasswordForm("", c.attrs(), true)
		form.Bind(c.PostForm())
		// validate the fields first: clean_old_password runs on a field that passed
		form.IsValid()
		if old := form.Str("old_password"); old != "" && !auth.CheckPassword(old, c.User.Password) {
			form.AddError("old_password", "Your old password was entered incorrectly. Please enter it again.")
		}
		if !form.HasErrors() {
			c.User.Password = auth.MakePassword(form.Str("new_password2"))
			if err := a.Store.SaveUser(c.Context(), c.User, "password"); err != nil {
				c.ServerError(err)
				return
			}
			// Changing the password rotates the session auth hash, which would
			// otherwise sign the user out of their own session straight away.
			c.UpdateSessionAuthHash(c.User)
			c.Message("success", "Your password has been updated.")
			c.SaveSession()
			c.RedirectTo("accounts:profile")
			return
		}
	}
	c.Render("accounts/profile.html", Data{"password_form": form.Template()})
}

func (a *App) memberOrg(c *Ctx) *models.Organization { return c.Org() }

func (a *App) organization(c *Ctx) {
	org := c.Org()
	if org == nil {
		c.Forbidden("You're not in an organization.")
		return
	}
	ctx := c.Context()
	cred, err := a.Store.PortalCredentialFor(ctx, org.ID)
	if err != nil {
		c.ServerError(err)
		return
	}
	isNew := cred == nil
	if isNew {
		cred = &models.PortalCredential{OrganizationID: org.ID, NewDeedMenuIndex: 1, MyDeedsMenuIndex: 2}
	}
	nameForm := organizationNameForm(org.Name, nil)
	portalForm := portalLoginForm()
	fillPortalForm(portalForm, cred, a.Cfg)
	staffForm := staffCreateForm()

	if c.R.Method == "POST" {
		post := c.PostForm()
		switch post.Get("action") {
		case "rename":
			nameForm = organizationNameForm(org.Name, post)
			if nameForm.IsValid() {
				org.Name = nameForm.Str("name")
				if err := a.Store.SaveOrganization(ctx, org); err != nil {
					c.ServerError(err)
					return
				}
				c.Message("success", "Organization name saved.")
				c.SaveSession()
				c.RedirectTo("accounts:organization")
				return
			}
		case "portal":
			portalForm = portalLoginForm()
			fillPortalForm(portalForm, cred, a.Cfg)
			portalForm.Bind(post)
			if portalForm.IsValid() {
				cred.PortalUsername = portalForm.Str("portal_username")
				cred.NewDeedMenuIndex = int16(*portalForm.IntVal("new_deed_menu_index"))
				cred.MyDeedsMenuIndex = int16(*portalForm.IntVal("my_deeds_menu_index"))
				cred.CollectReceipts = portalForm.BoolVal("collect_receipts")
				cred.AlertEmail = portalForm.Str("alert_email")
				if pw := portalForm.Str("password"); pw != "" {
					enc, err := encryptPortalPassword(a.Cfg, pw)
					if err != nil {
						c.ServerError(err)
						return
					}
					cred.PortalPassword = enc
				}
				if err := a.Store.SavePortalCredential(ctx, cred); err != nil {
					c.ServerError(err)
					return
				}
				c.Message("success", "Portal login saved. Every member's submissions use it from now on.")
				c.SaveSession()
				c.RedirectTo("accounts:organization")
				return
			}
		case "add_staff":
			staffForm = staffCreateForm()
			staffForm.Bind(post)
			validateStaffForm(c, staffForm)
			if staffForm.IsValid() && !staffForm.HasErrors() {
				fullName := staffForm.Str("full_name")
				username := strings.TrimSpace(staffForm.Str("username"))
				first, last, _ := strings.Cut(strings.TrimSpace(fullName), " ")
				u := &models.User{Username: username, Password: auth.MakePassword(staffForm.Str("password")),
					FirstName: first, LastName: last, IsActive: true}
				err := a.Store.CreateStaff(ctx, u, org.ID)
				if err != nil {
					c.ServerError(err)
					return
				}
				c.Message("success", "Added "+fullName+". They can sign in as \""+u.Username+"\".")
				c.SaveSession()
				c.RedirectTo("accounts:organization")
				return
			}
		}
	}

	members, err := a.Store.Members(ctx, org.ID)
	if err != nil {
		c.ServerError(err)
		return
	}
	var rows []map[string]any
	for _, m := range members {
		rows = append(rows, map[string]any{
			"is_admin": m.Membership.IsAdmin(),
			"user":     ObjectMap(&m.User, map[string]any{"get_full_name": m.User.FullName()}),
		})
	}
	c.Render("accounts/organization.html", Data{
		"org":              ObjectMap(org, nil),
		"name_form":        nameForm.Template(),
		"portal_form":      portalForm.Template(),
		"staff_form":       staffForm.Template(),
		"members":          rows,
		"has_portal_login": !isNew && cred.PortalUsername != "" && cred.PortalPassword != "",
	})
}

func fillPortalForm(f *forms.Form, cred *models.PortalCredential, cfg *config.Config) {
	f.Initial["portal_username"] = cred.PortalUsername
	if pw, err := portalcrypt.Decrypt(cfg.FieldEncryptionKey, cred.PortalPassword); err == nil {
		f.Initial["password"] = pw
	}
	f.Initial["new_deed_menu_index"] = strconv.Itoa(int(cred.NewDeedMenuIndex))
	f.Initial["my_deeds_menu_index"] = strconv.Itoa(int(cred.MyDeedsMenuIndex))
	if cred.CollectReceipts {
		f.Initial["collect_receipts"] = "on"
	}
	f.Initial["alert_email"] = cred.AlertEmail
}

func encryptPortalPassword(cfg *config.Config, pw string) (string, error) {
	return portalcrypt.Encrypt(cfg.FieldEncryptionKey, pw)
}

// validateStaffForm adds the username and password rules of StaffCreateForm.
func validateStaffForm(c *Ctx, f *forms.Form) {
	if !f.IsValid() {
		return
	}
	username := strings.TrimSpace(f.Str("username"))
	if !validUsername(username) {
		f.AddError("username", badUsername)
		return
	}
	if taken, err := c.App.Store.UsernameTaken(c.Context(), username); err == nil && taken {
		f.AddError("username", "That username is already taken.")
		return
	}
	user := &auth.UserAttrs{Username: username, FirstName: f.Str("full_name")}
	for _, msg := range auth.ValidatePassword(f.Str("password"), user) {
		f.AddError("password", msg)
	}
}

func (a *App) staffToggleActive(c *Ctx) {
	if c.R.Method != "POST" {
		http_error(c, 405, "Method Not Allowed")
		return
	}
	member, ok := a.staffMember(c)
	if !ok {
		return
	}
	member.User.IsActive = !member.User.IsActive
	if err := a.Store.SaveUser(c.Context(), &member.User, "is_active"); err != nil {
		c.ServerError(err)
		return
	}
	name := member.User.DisplayName()
	if member.User.IsActive {
		c.Message("success", name+" can sign in again.")
	} else {
		c.Message("success", name+" can no longer sign in.")
	}
	c.SaveSession()
	c.RedirectTo("accounts:organization")
}

func (a *App) staffMember(c *Ctx) (*store.MemberRow, bool) {
	id, err := strconv.ParseInt(c.PathValue("user_id"), 10, 64)
	if err != nil {
		c.NotFound()
		return nil, false
	}
	org := c.Org()
	if org == nil {
		c.NotFound()
		return nil, false
	}
	m, err := a.Store.StaffMember(c.Context(), org.ID, id)
	if err != nil {
		c.ServerError(err)
		return nil, false
	}
	if m == nil {
		c.NotFound()
		return nil, false
	}
	return m, true
}

func (a *App) staffPassword(c *Ctx) {
	member, ok := a.staffMember(c)
	if !ok {
		return
	}
	attrs := &auth.UserAttrs{Username: member.User.Username, FirstName: member.User.FirstName, LastName: member.User.LastName, Email: member.User.Email}
	form := setPasswordForm("", attrs, false)
	if c.R.Method == "POST" {
		form.Bind(c.PostForm())
		if form.IsValid() {
			member.User.Password = auth.MakePassword(form.Str("new_password2"))
			if err := a.Store.SaveUser(c.Context(), &member.User, "password"); err != nil {
				c.ServerError(err)
				return
			}
			c.Message("success", "New password set for "+member.User.DisplayName()+".")
			c.SaveSession()
			c.RedirectTo("accounts:organization")
			return
		}
	}
	c.Render("accounts/staff_password.html", Data{
		"member": map[string]any{"user": ObjectMap(&member.User, map[string]any{"get_full_name": member.User.FullName()})},
		"form":   form.Template(),
	})
}

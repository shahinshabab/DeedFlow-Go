package web

import (
	"github.com/flosch/pongo2/v6"

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

func (a *App) userFormSetup(v *cfView)                          {}
func (a *App) userAddContext(v *cfView, ctx pongo2.Context)    {}
func (a *App) userChangeContext(v *cfView, ctx pongo2.Context) {}

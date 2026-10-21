package web

import (
	"regexp"
	"strings"

	"github.com/shahinshabab/deedflow-go/internal/auth"
	"github.com/shahinshabab/deedflow-go/internal/forms"
)

const pwHelpHTML = "<ul><li>Your password can’t be too similar to your other personal information.</li>" +
	"<li>Your password must contain at least 8 characters.</li>" +
	"<li>Your password can’t be a commonly used password.</li>" +
	"<li>Your password can’t be entirely numeric.</li></ul>"

// bootstrap gives every field Bootstrap's class, like BootstrapFormMixin.
func bootstrap(f *forms.Form) *forms.Form {
	for _, fl := range f.Fields {
		if fl.Attrs == nil {
			fl.Attrs = map[string]string{}
		}
		if _, ok := fl.Attrs["class"]; ok {
			continue
		}
		switch fl.Widget {
		case forms.Checkbox:
			fl.Attrs["class"] = "form-check-input"
		case forms.Select:
			fl.Attrs["class"] = "form-select"
		default:
			fl.Attrs["class"] = "form-control"
		}
	}
	return f
}

func password(name, label string, autocomplete string) *forms.Field {
	return &forms.Field{Name: name, Label: label, Kind: forms.Char, Widget: forms.PasswordInput, Required: true,
		Attrs: map[string]string{"autocomplete": autocomplete}}
}

// newPasswordFields adds the two new-password fields with Django's messages.
func setPasswordForm(prefix string, user *auth.UserAttrs, withOld bool) *forms.Form {
	var fields []*forms.Field
	if withOld {
		old := password("old_password", "Old password", "current-password")
		old.Attrs["autofocus"] = ""
		fields = append(fields, old)
	}
	p1 := password("new_password1", "New password", "new-password")
	p1.HelpText = pwHelpHTML
	p2 := password("new_password2", "New password confirmation", "new-password")
	fields = append(fields, p1, p2)
	f := forms.New(prefix, fields...)
	f.Clean = func(f *forms.Form) {
		a, b := f.Str("new_password1"), f.Str("new_password2")
		if a != "" && b != "" && a != b {
			f.AddError("new_password2", "The two password fields didn’t match.")
		}
		if b != "" && a == b {
			for _, msg := range auth.ValidatePassword(b, user) {
				f.AddError("new_password2", msg)
			}
		}
	}
	return bootstrap(f)
}

func organizationNameForm(name string, bound map[string][]string) *forms.Form {
	f := forms.New("org", forms.CharField("name", "Organization name", true, 255))
	f.Initial["name"] = name
	if bound != nil {
		f.Bind(bound)
	}
	return bootstrap(f)
}

func intField(name, label string, required bool, def string) *forms.Field {
	var min, max int64 = 0, 32767
	fl := forms.IntField(name, label, required, min, max)
	fl.Initial = def
	return fl
}

func portalLoginForm() *forms.Form {
	pw := &forms.Field{Name: "password", Label: "Portal password", Kind: forms.Char, Widget: forms.PasswordInput, RenderValue: true,
		HelpText: "Leave blank to keep the current password.", Attrs: map[string]string{"autocomplete": "new-password"}}
	newIdx := intField("new_deed_menu_index", "\"New Token\" menu position", true, "1")
	newIdx.HelpText = "Only change if submissions can't find the portal's menu item."
	myIdx := intField("my_deeds_menu_index", "\"My Tokens\" menu position", true, "2")
	myIdx.HelpText = "Only change if edits can't find the portal's menu item."
	collect := forms.BoolField("collect_receipts", "Collect payment receipts nightly")
	collect.HelpText = "Reads the stamp duty and registration fee receipts of registered tokens at 9 PM " +
		"(retries at 10 and 11 PM) into Accounts. Read-only: nothing is changed on the portal."
	alert := &forms.Field{Name: "alert_email", Label: "Email for receipt alerts", Kind: forms.Email, Widget: forms.TextInput, MaxLength: 254,
		HelpText: "Told if receipts are still pending after the 11 PM try. Blank: your own email."}
	f := forms.New("portal",
		forms.CharField("portal_username", "Portal username", false, 255), pw, newIdx, myIdx, collect, alert)
	return bootstrap(f)
}

var usernameRe = regexp.MustCompile(`^[\w.@+-]+$`)

func staffCreateForm() *forms.Form {
	pw := &forms.Field{Name: "password", Label: "Password", Kind: forms.Char, Widget: forms.PasswordInput, Required: true,
		Attrs: map[string]string{"autocomplete": "new-password"}}
	user := forms.CharField("username", "Username", true, 150)
	user.HelpText = "What they type to sign in to DeedFlow."
	return bootstrap(forms.New("staff", forms.CharField("full_name", "Full name", true, 150), user, pw))
}

// validUsername applies Django's UnicodeUsernameValidator.
func validUsername(u string) bool { return usernameRe.MatchString(strings.TrimSpace(u)) }

const badUsername = "Enter a valid username. This value may contain only unicode letters, numbers, and @/./+/-/_ characters."

package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/flosch/pongo2/v6"
)

// The template data of the add/change pages: what Django's helpers.AdminForm,
// Fieldset, Fieldline, AdminField and InlineAdminFormSet objects give the
// admin templates.

func (v *cfView) readonlyContents(f *aForm, name string, obj *adminObj) string {
	a := v.a
	mf := f.m.field(name)
	if mf == nil {
		return esc("-")
	}
	var val any
	if obj != nil {
		val = obj.vals[mf.Name]
	} else {
		val = a.modelDefault(mf)
		if mf.AutoNow || mf.AutoNowAdd {
			val = nil
		}
	}
	var out string
	switch {
	case mf.isM2M():
		out = "-"
	case mf.isFK():
		if val == nil {
			out = esc("-")
		} else {
			rel := adminSiteSpec.models[mf.Related.Label]
			s := esc(f.re.related(f.m, mf.Name, val))
			if rel != nil {
				out = `<a href="` + esc(a.adminURL(rel, "change", adminQuote(valueString(val)))) + `">` + s + `</a>`
			} else {
				out = s
			}
		}
	default:
		out = a.displayForField(val, mf, "-")
	}
	return strings.ReplaceAll(out, "\n", "<br>")
}

func (v *cfView) fieldsetMaps(f *aForm, fsets []adminFieldset, readonly map[string]bool, obj *adminObj, skip string, prefix string, inlineIdx int) []any {
	var out []any
	for _, fs := range fsets {
		var lines []any
		for _, row := range fs.Rows {
			if skip != "" {
				var kept []string
				for _, n := range row {
					if n != skip {
						kept = append(kept, n)
					}
				}
				if len(kept) == 0 {
					continue
				}
				row = kept
			}
			lines = append(lines, v.lineMap(f, row, readonly, obj))
		}
		collapsible := false
		classes := strings.Join(fs.Classes, " ")
		if strings.Contains(classes, "collapse") {
			collapsible = true
			for _, row := range fs.Rows {
				for _, n := range row {
					if len(f.errs[n]) > 0 {
						collapsible = false
					}
				}
			}
		}
		m := map[string]any{"classes": classes, "description": fs.Description, "is_collapsible": collapsible, "lines": lines}
		if fs.Title != nil {
			m["name"] = *fs.Title
		} else {
			m["name"] = nil
		}
		out = append(out, m)
	}
	return out
}

func (v *cfView) lineMap(f *aForm, names []string, readonly map[string]bool, obj *adminObj) map[string]any {
	var items []any
	allHidden := true
	var errs []string
	for i, n := range names {
		if readonly[n] || f.fields[n] == nil {
			label := n
			if mf := f.m.field(n); mf != nil {
				label = mf.VerboseName
			}
			if fm := v.formMF(f, n); fm != nil {
				label = fm.VerboseName
			}
			lt := "<label"
			if i != 0 {
				lt += ` class="inline"`
			}
			lt += ">" + esc(capfirst(label)) + ":</label>"
			help := ""
			if mf := f.m.field(n); mf != nil {
				help = mf.HelpText
			}
			items = append(items, map[string]any{
				"is_readonly": true, "is_checkbox": false,
				"field":     map[string]any{"name": n, "label": label, "help_text": safeOrEmpty(help), "field": n, "is_hidden": false},
				"label_tag": safe(lt), "contents": safe(v.readonlyContents(f, n, obj)), "errors": "",
			})
			allHidden = false
			continue
		}
		fl := f.fields[n]
		bf := f.boundMap(fl)
		if !fl.hidden {
			allHidden = false
		}
		if e := f.errorsHTML(n); e != "" {
			errs = append(errs, e)
		}
		items = append(items, map[string]any{
			"is_readonly": false, "is_checkbox": fl.w.Class == "CheckboxInput", "field": bf,
			"label_tag": safe(f.labelTag(fl, i == 0)), "errors": safeOrEmpty(f.errorsHTML(n)),
		})
	}
	var lineErrors any = ""
	if len(errs) > 0 {
		lineErrors = safe(strings.Join(errs, "\n"))
	}
	return map[string]any{
		"names": names, "items": items, "errors": lineErrors, "has_visible_field": !allHidden,
	}
}

func safeOrEmpty(s string) any {
	if s == "" {
		return ""
	}
	return safe(s)
}

func (v *cfView) formMF(f *aForm, n string) *modelFieldSpec { return nil }

// adminFormMap is the AdminForm.
func (v *cfView) adminFormMap() map[string]any {
	f := v.form
	return map[string]any{
		"fieldsets": v.fieldsetMaps(f, v.fsets, v.readonly, v.obj, "", "", 0),
		"form":      map[string]any{"non_field_errors": safe(f.nonFieldErrorsHTML())},
	}
}

func (v *cfView) inlineMaps() []any {
	var out []any
	for _, fs := range v.inlines {
		out = append(out, v.inlineMap(fs))
	}
	return out
}

func (v *cfView) inlineMap(fs *inlineFS) map[string]any {
	template := "admin/edit_inline/tabular.html"
	if fs.spec.Template != "" {
		template = fs.spec.Template
	}
	readonlyEditing := map[string]bool{}
	for n := range fs.readonly {
		readonlyEditing[n] = true
	}
	if !fs.canChange {
		for _, n := range fs.order {
			readonlyEditing[n] = true
		}
	}
	// the visible fields as one fieldset
	var rows [][]string
	for _, n := range fs.order {
		rows = append(rows, []string{n})
	}
	fsets := []adminFieldset{{Rows: rows}}
	var forms []any
	mk := func(f *aForm, original *adminObj, i int) map[string]any {
		fieldsets := v.fieldsetMaps(f, fsets, readonlyEditing, original, fs.fkName, fs.prefix, i)
		if original == nil {
			fieldsets = v.fieldsetMaps(f, fsets, fs.readonly, nil, fs.fkName, fs.prefix, i)
		}
		cells := 1
		for _, n := range fs.order {
			_ = n
			cells++
		}
		if fs.canDelete {
			cells++
		}
		m := map[string]any{
			"fieldsets": fieldsets, "has_original": original != nil, "show_url": false, "absolute_url": "",
			"needs_explicit_pk_field": true, "cell_count": cells,
			"form": map[string]any{"non_field_errors": safeOrEmpty(f.nonFieldErrorsHTML())},
			"model_admin": map[string]any{"show_change_link": false, "has_registered_model": adminSiteSpec.models[fs.m.Label] != nil},
		}
		if original != nil {
			m["original_repr"] = f.re.str(original)
			m["original_pk"] = original.pkString()
		}
		pkVal := ""
		if original != nil {
			pkVal = original.pkString()
		} else if f.bound && f.data != nil {
			pkVal = f.data.Get(f.htmlName(fs.m.PK))
		}
		pkHTML := inputTag("hidden", f.htmlName(fs.m.PK), strPtrNonEmpty(pkVal), []attr{{"id", f.autoID(fs.m.PK)}})
		m["pk_field"] = map[string]any{"field": boundField{"_html": pkHTML}}
		fkVal := ""
		if fs.parent != nil {
			fkVal = valueString(fs.parent.vals[fs.parent.m.PK])
		} else if f.bound && f.inlineFKVal != nil {
			fkVal = valueString(f.inlineFKVal)
		}
		fkHTML := inputTag("hidden", f.htmlName(fs.fkName), strPtrNonEmpty(fkVal), []attr{{"id", f.autoID(fs.fkName)}})
		m["fk_field"] = map[string]any{"field": boundField{"_html": fkHTML}}
		delOn := f.bound && f.data != nil && f.data.Get(f.htmlName("DELETE")) != ""
		delAttrs := []attr{{"id", f.autoID("DELETE")}}
		if delOn {
			delAttrs = append(delAttrs, attr{"checked", true})
		}
		m["deletion_field"] = map[string]any{
			"field":     boundField{"_html": inputTag("checkbox", f.htmlName("DELETE"), nil, delAttrs)},
			"label_tag": safe(`<label class="vCheckboxLabel inline" for="` + esc(f.autoID("DELETE")) + `">Delete</label>`),
		}
		return m
	}
	for i, f := range fs.forms {
		var orig *adminObj
		if i < fs.nInitial {
			orig = f.inst
			if orig != nil && f.vals != nil {
				orig = &adminObj{m: orig.m, vals: f.vals}
			}
		}
		forms = append(forms, mk(f, orig, i))
	}
	if fs.canAdd {
		forms = append(forms, mk(fs.empty, nil, len(fs.forms)))
	}
	// the tabular columns
	var cols []any
	for _, n := range fs.order {
		if fs.readonly[n] || !fs.canChange {
			label := n
			if mf := fs.m.field(n); mf != nil {
				label = mf.VerboseName
			}
			help := ""
			if mf := fs.m.field(n); mf != nil {
				help = mf.HelpText
			}
			cols = append(cols, map[string]any{"name": n, "label": label, "widget": map[string]any{"is_hidden": false}, "required": false, "help_text": help})
			continue
		}
		fl := fs.empty.fields[n]
		if fl == nil {
			continue
		}
		cols = append(cols, map[string]any{"name": n, "label": fl.label, "widget": map[string]any{"is_hidden": fl.hidden}, "required": fl.required, "help_text": fl.help})
	}
	nonForm := ""
	if len(fs.nonForm) > 0 {
		nonForm = strings.Replace(errorList(fs.nonForm), `class="errorlist"`, `class="errorlist nonform"`, 1)
	}
	return map[string]any{
		"formset": map[string]any{
			"prefix": fs.prefix, "management_form": safe(fs.managementForm()), "non_form_errors": safe(nonForm),
			"max_num": fs.maxNum, "can_delete": fs.canDelete,
		},
		"opts": map[string]any{"verbose_name": fs.spec.VerboseName, "verbose_name_plural": fs.spec.VerboseNamePl, "template": template},
		"forms": forms, "fields": cols, "classes": "", "is_collapsible": false,
		"has_add_permission": fs.canAdd, "has_change_permission": fs.canChange, "has_delete_permission": fs.hasDeletePerm,
		"has_view_permission": fs.canView, "inline_formset_data": fs.formsetData(),
	}
}

func strPtrNonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (v *cfView) mediaFor() any {
	key := v.m.Label + "|add"
	if !v.add {
		key = v.m.Label + "|change"
	}
	s := adminMediaTable[key]
	if s == "" {
		s = adminMediaTable[v.m.Label+"|add"]
	}
	return safe(s)
}

// renderChangeForm is render_change_form plus the context changeform_view builds.
func (a *App) renderChangeForm(v *cfView, isPost, formValidated bool, pf url.Values) {
	r, c, m := v.r, v.r.c, v.m
	add := v.add
	var title string
	switch {
	case add:
		title = "Add " + m.VerboseName
	case r.perms.has(m, "change"):
		title = "Change " + m.VerboseName
	default:
		title = "View " + m.VerboseName
	}
	ctx := r.base(title)
	shown := v.obj
	if v.obj != nil && v.form.vals != nil && isPost {
		shown = &adminObj{m: m, vals: v.form.vals}
		v.form.re.overrides[m.Label+":"+v.obj.pkString()] = shown
	}
	preserved := v.preservedFilters()
	errCount := 0
	if isPost {
		errCount = v.form.errorLists()
		for _, in := range v.inlines {
			errCount += in.errorCount()
		}
	}
	var errList []any
	for i := 0; i < errCount; i++ {
		errList = append(errList, i)
	}
	ctx["adminform"] = v.adminFormMap()
	ctx["inline_admin_formsets"] = v.inlineMaps()
	ctx["errors"] = errList
	ctx["opts"] = a.adminOpts(m)
	ctx["app_label"] = m.AppLabel
	ctx["media"] = v.mediaFor()
	ctx["is_popup"] = false
	ctx["to_field"] = nil
	ctx["is_popup_var"] = popupVar
	ctx["to_field_var"] = toFieldVr
	ctx["preserved_filters"] = preserved
	if !add {
		ctx["subtitle"] = v.form.re.str(shown)
		ctx["original_repr"] = ctx["subtitle"]
		ctx["original_pk"] = v.obj.pkString()
		ctx["object_id"] = v.obj.pkString()
	}
	ctx["add"] = add
	ctx["change"] = !add
	if isPost && !formValidated && hasKeyValues(pf, "_saveasnew") {
		ctx["show_save"] = false
		ctx["show_save_and_continue"] = false
		ctx["add"] = false
		ctx["change"] = true
		add = false
	}
	hasEditableInline := false
	hasFile := false
	for _, fs := range v.inlines {
		if fs.canAdd || fs.canChange || fs.hasDeletePerm {
			hasEditableInline = true
		}
		for _, f := range fs.forms {
			for _, fl := range f.fields {
				if fl.kind == "file" {
					hasFile = true
				}
			}
		}
		if fs.empty != nil {
			for _, fl := range fs.empty.fields {
				if fl.kind == "file" {
					hasFile = true
				}
			}
		}
	}
	for _, fl := range v.form.fields {
		if fl.kind == "file" {
			hasFile = true
		}
	}
	hasView, hasAdd, hasChange, hasDelete := r.perms.has(m, "view"), r.perms.has(m, "add"), r.perms.has(m, "change"), r.perms.has(m, "delete")
	ctx["has_view_permission"] = hasView
	ctx["has_add_permission"] = hasAdd
	ctx["has_change_permission"] = hasChange
	ctx["has_delete_permission"] = hasDelete
	ctx["has_editable_inline_admin_formsets"] = hasEditableInline
	ctx["has_file_field"] = hasFile
	ctx["has_absolute_url"] = false
	ctx["absolute_url"] = nil
	formURL := addPreservedFilters("", m, preserved, a)
	ctx["form_url"] = formURL
	ctx["save_as"] = false
	ctx["save_on_top"] = false
	ctx["prepopulated_fields_json"] = "[]"
	// submit_row
	showSave := true
	if v, ok := ctx["show_save"].(bool); ok {
		showSave = v
	}
	showCont := true
	if v, ok := ctx["show_save_and_continue"].(bool); ok {
		showCont = v
	}
	canSave := (hasChange && !add) || (hasAdd && add) || hasEditableInline
	canSaveAdd := hasAdd && (add || true) && canSave
	canCont := canSave && hasView && showCont
	ctx["can_change"] = hasChange || hasEditableInline
	ctx["show_delete_link"] = hasDelete && !add
	ctx["show_save_as_new"] = false
	ctx["show_save_and_add_another"] = canSaveAdd
	ctx["show_save_and_continue"] = canCont
	ctx["show_save"] = showSave && canSave
	ctx["show_close"] = !(showSave && canSave)
	ctx["changelist_url"] = addPreservedFilters(a.adminURL(m, "changelist"), m, preserved, a)
	if !add {
		ctx["delete_url"] = addPreservedFilters(a.adminURL(m, "delete", adminQuote(v.obj.pkString())), m, preserved, a)
		ctx["history_url"] = addPreservedFilters(a.adminURL(m, "history", adminQuote(v.obj.pkString())), m, preserved, a)
	}
	status := http.StatusOK
	tpl := "admin/change_form.html"
	if m.Label == "auth.user" && add {
		tpl = "admin/auth/user/add_form.html"
		a.userAddContext(v, ctx)
	}
	if m.Label == "auth.user" && !add {
		a.userChangeContext(v, ctx)
	}
	c.renderContext(status, tpl, ctx)
}

var _ = fmt.Sprint
var _ pongo2.Context

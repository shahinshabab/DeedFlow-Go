package web

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/auth"
)

// The HTML of the admin's widgets, as Django's widget templates print them.

// widgetAttrs turns a widget's attrs map into an ordered attribute list.
func widgetAttrs(m map[string]any) []attr {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	rank := func(k string) int {
		switch k {
		case "class":
			return 0
		case "maxlength", "min", "max", "step", "size", "cols", "rows":
			return 1
		}
		return 2
	}
	sort.Slice(keys, func(i, j int) bool {
		if rank(keys[i]) != rank(keys[j]) {
			return rank(keys[i]) < rank(keys[j])
		}
		return keys[i] < keys[j]
	})
	var out []attr
	for _, k := range keys {
		v := m[k]
		switch x := v.(type) {
		case float64:
			out = append(out, attr{k, strconv.FormatFloat(x, 'f', -1, 64)})
		default:
			out = append(out, attr{k, v})
		}
	}
	return out
}

// currentValue is BoundField.value(): the submitted data when bound, else the initial value.
type fieldValue struct {
	text  string
	has   bool // the value attribute is written
	list  []string
	parts [2]string
	hasP  [2]bool
	file  string
	on    bool
}

func (f *aForm) fieldValue(fl *aField) fieldValue {
	var v fieldValue
	if f.bound && !fl.disabled {
		rv := f.valueFromData(fl)
		switch fl.kind {
		case "modelmulti":
			v.list = rv.list
		case "splitdt":
			for i := 0; i < 2; i++ {
				if rv.parts[i] != nil {
					v.parts[i], v.hasP[i] = *rv.parts[i], true
				}
			}
		case "bool":
			v.on = rv.s != nil && *rv.s != "" && strings.ToLower(*rv.s) != "false" && *rv.s != "0"
			if rv.s != nil && strings.ToLower(*rv.s) == "true" {
				v.on = true
			}
		case "file":
			v.file = valueString(f.initial[fl.name])
		default:
			if rv.s != nil {
				v.text, v.has = *rv.s, *rv.s != ""
			}
		}
		return v
	}
	init := f.initial[fl.name]
	switch fl.kind {
	case "modelmulti":
		v.list = f.m2mInit[fl.name]
	case "splitdt":
		if t, ok := init.(time.Time); ok {
			t = t.In(f.a.Cfg.TimeZone)
			v.parts[0], v.hasP[0] = t.Format("2006-01-02"), true
			v.parts[1], v.hasP[1] = t.Format("15:04:05"), true
		}
	case "bool":
		b, _ := init.(bool)
		v.on = b
	case "date":
		if t, ok := init.(time.Time); ok {
			v.text, v.has = t.Format("2006-01-02"), true
		}
	case "json":
		if init == nil {
			v.text, v.has = "null", true
		} else {
			v.text, v.has = pyJSONText(valueString(init)), true
		}
	case "file":
		v.file = valueString(init)
	case "int", "model", "choice", "char", "username", "email", "decimal":
		if init != nil {
			v.text = valueString(init)
			v.has = v.text != ""
		}
	default:
		if init != nil {
			v.text = valueString(init)
			v.has = v.text != ""
		}
	}
	return v
}

// extraAttrs are the attributes BoundField.build_widget_attrs adds.
func (f *aForm) extraAttrs(fl *aField, useRequired bool) []attr {
	var out []attr
	if useRequired && fl.required && !f.noRequired {
		out = append(out, attr{"required", true})
	}
	if fl.disabled {
		out = append(out, attr{"disabled", true})
	}
	if !fl.hidden && f.bound && len(f.errs[fl.name]) > 0 {
		out = append(out, attr{"aria-invalid", "true"})
	}
	if fl.help != "" {
		out = append(out, attr{"aria-describedby", f.autoID(fl.name) + "_helptext"})
	}
	return out
}

func (f *aForm) renderWidget(fl *aField) string {
	w := fl.w
	if w.Class == "RelatedFieldWidgetWrapper" {
		return f.renderRelated(fl)
	}
	return f.renderInner(fl, fl.w)
}

func inputTag(typ, name string, value *string, attrs []attr) string {
	s := `<input type="` + typ + `" name="` + esc(name) + `"`
	if value != nil {
		s += ` value="` + esc(*value) + `"`
	}
	return s + attrsHTML(attrs) + ">"
}

func (f *aForm) renderInner(fl *aField, w *widgetSpec) string {
	name := f.htmlName(fl.name)
	id := f.autoID(fl.name)
	val := f.fieldValue(fl)
	base := widgetAttrs(w.Attrs)
	switch w.Class {
	case "AdminTextInputWidget", "AdminEmailInputWidget", "AdminIntegerFieldWidget", "NumberInput", "TextInput", "HiddenInput", "EmailInput":
		attrs := append(base, f.extraAttrs(fl, !fl.hidden)...)
		attrs = append(attrs, attr{"id", id})
		var v *string
		if val.has {
			v = &val.text
		}
		typ := w.InputType
		if typ == "" {
			typ = "text"
		}
		return inputTag(typ, name, v, attrs)
	case "PasswordInput":
		attrs := append(base, f.extraAttrs(fl, true)...)
		attrs = append(attrs, attr{"id", id})
		return inputTag("password", name, nil, attrs)
	case "AdminTextareaWidget", "Textarea":
		attrs := append(base, f.extraAttrs(fl, true)...)
		attrs = append(attrs, attr{"id", id})
		return `<textarea name="` + esc(name) + `"` + attrsHTML(attrs) + ">\n" + esc(val.text) + "</textarea>"
	case "CheckboxInput":
		attrs := append(base, f.extraAttrs(fl, true)...)
		attrs = append(attrs, attr{"id", id})
		if val.on {
			attrs = append(attrs, attr{"checked", true})
		}
		return inputTag("checkbox", name, nil, attrs)
	case "Select":
		return f.renderSelect(fl, w, name, id, base, val)
	case "AutocompleteSelect":
		return f.renderAutocomplete(fl, w, name, id, val)
	case "FilteredSelectMultiple":
		return f.renderSelectMultiple(fl, w, name, id, base, val)
	case "RadioSelect":
		return f.renderRadio(fl, w, name, id, base, val)
	case "AdminDateWidget":
		attrs := append(base, f.extraAttrs(fl, true)...)
		attrs = append(attrs, attr{"id", id})
		var v *string
		if val.has {
			v = &val.text
		}
		return "<p class=\"date\">\n  " + inputTag("text", name, v, attrs) + "\n</p>"
	case "AdminSplitDateTime":
		return f.renderSplitDateTime(fl, name, id, val)
	case "AdminFileWidget":
		return f.renderFile(fl, name, id, val)
	case "ReadOnlyPasswordHashWidget":
		return f.renderPasswordHash(fl)
	}
	return "<!-- unsupported widget " + w.Class + " -->"
}

func (f *aForm) renderSelect(fl *aField, w *widgetSpec, name, id string, base []attr, val fieldValue) string {
	choices := f.choicesOf(fl)
	var opts []choice
	if fl.kind == "model" {
		if fl.spec.Queryset != nil && fl.spec.Queryset.EmptyLabel != nil {
			opts = append(opts, choice{"", *fl.spec.Queryset.EmptyLabel})
		}
	}
	opts = append(opts, choices...)
	useRequired := true
	if len(opts) == 0 || opts[0].value != "" {
		useRequired = false
	}
	attrs := append(base, f.extraAttrs(fl, useRequired)...)
	attrs = append(attrs, attr{"id", id})
	var b strings.Builder
	b.WriteString(`<select name="` + esc(name) + `"` + attrsHTML(attrs) + ">")
	selected := false
	cur := val.text
	for _, o := range opts {
		sel := !selected && o.value == cur
		selected = selected || sel
		b.WriteString("\n  " + optionTag(o, sel) + "\n")
	}
	b.WriteString("\n</select>")
	return b.String()
}

func optionTag(o choice, sel bool) string {
	s := `<option value="` + esc(o.value) + `"`
	if sel {
		s += " selected"
	}
	return s + ">" + esc(o.label) + "</option>"
}

func (f *aForm) renderSelectMultiple(fl *aField, w *widgetSpec, name, id string, base []attr, val fieldValue) string {
	attrs := append(base, attr{"multiple", true})
	attrs = append(attrs, f.extraAttrs(fl, true)...)
	attrs = append(attrs, attr{"id", id})
	cls := "selectfilter"
	if w.IsStacked {
		cls += "stacked"
	}
	// FilteredSelectMultiple sets class and the data attributes last
	var out []attr
	for _, a := range attrs {
		if a.k == "class" {
			continue
		}
		out = append(out, a)
	}
	out = append(out, attr{"class", cls}, attr{"data-field-name", w.VerboseName}, attr{"data-is-stacked", map[bool]string{true: "1", false: "0"}[w.IsStacked]})
	chosen := map[string]bool{}
	for _, v := range val.list {
		chosen[v] = true
	}
	var b strings.Builder
	b.WriteString(`<select name="` + esc(name) + `"` + attrsHTML(out) + ">")
	for _, o := range f.choicesOf(fl) {
		b.WriteString("\n  " + optionTag(o, chosen[o.value]) + "\n")
	}
	b.WriteString("\n</select>")
	return b.String()
}

func (f *aForm) renderAutocomplete(fl *aField, w *widgetSpec, name, id string, val fieldValue) string {
	attrs := []attr{
		{"data-ajax--cache", "true"}, {"data-ajax--delay", "250"}, {"data-ajax--type", "GET"},
		{"data-ajax--url", "/admin/autocomplete/"},
		{"data-app-label", f.m.AppLabel}, {"data-model-name", f.m.ModelName}, {"data-field-name", fl.name},
		{"data-theme", "admin-autocomplete"}, {"data-allow-clear", "false"}, {"data-placeholder", ""},
		{"lang", "en"}, {"class", "admin-autocomplete"},
	}
	attrs = append(attrs, f.extraAttrs(fl, true)...)
	attrs = append(attrs, attr{"id", id})
	var b strings.Builder
	b.WriteString(`<select name="` + esc(name) + `"` + attrsHTML(attrs) + ">")
	if val.text != "" {
		q := fl.spec.Queryset
		rel := adminSiteSpec.model(q.Label)
		label := f.re.byID(rel.Label, val.text)
		b.WriteString("\n  " + optionTag(choice{val.text, label}, true) + "\n")
	} else if !fl.required {
		b.WriteString("\n  " + optionTag(choice{"", ""}, true) + "\n")
	}
	b.WriteString("\n</select>")
	return b.String()
}

func (f *aForm) renderRadio(fl *aField, w *widgetSpec, name, id string, base []attr, val fieldValue) string {
	var cls string
	for _, a := range base {
		if a.k == "class" {
			cls, _ = a.v.(string)
		}
	}
	var b strings.Builder
	b.WriteString(`<div id="` + esc(id) + `"`)
	if cls != "" {
		b.WriteString(` class="` + esc(cls) + `"`)
	}
	b.WriteString(">")
	selected := false
	for i, c := range f.choicesOf(fl) {
		oid := id + "_" + strconv.Itoa(i)
		sel := !selected && c.value == val.text
		selected = selected || sel
		attrs := []attr{{"id", oid}}
		if sel {
			attrs = append(attrs, attr{"checked", true})
		}
		v := c.value
		b.WriteString("<div>\n    <label for=\"" + esc(oid) + "\">" + inputTag("radio", name, &v, attrs) + " " + esc(c.label) + "</label></div>")
	}
	b.WriteString("\n</div>")
	return b.String()
}

func (f *aForm) renderSplitDateTime(fl *aField, name, id string, val fieldValue) string {
	dAttrs := []attr{{"class", "vDateField"}, {"size", "10"}}
	tAttrs := []attr{{"class", "vTimeField"}, {"size", "8"}}
	ex := f.extraAttrs(fl, false)
	dAttrs = append(append(dAttrs, ex...), attr{"id", id + "_0"})
	tAttrs = append(append(tAttrs, ex...), attr{"id", id + "_1"})
	var dv, tv *string
	if val.hasP[0] && val.parts[0] != "" {
		dv = &val.parts[0]
	}
	if val.hasP[1] && val.parts[1] != "" {
		tv = &val.parts[1]
	}
	return "<p class=\"datetime\">\n  Date: " + inputTag("text", name+"_0", dv, dAttrs) + "<br>\n  Time: " + inputTag("text", name+"_1", tv, tAttrs) + "\n</p>"
}

func (f *aForm) renderFile(fl *aField, name, id string, val fieldValue) string {
	attrs := f.extraAttrs(fl, val.file == "")
	attrs = append(attrs, attr{"id", id})
	var b strings.Builder
	if val.file != "" {
		b.WriteString(`<p class="file-upload">Currently: <a href="` + esc(mediaURL(val.file)) + `">` + esc(val.file) + `</a>`)
		if !fl.required {
			cname := name + "-clear"
			cid := cname + "_id"
			b.WriteString("\n<span class=\"clearable-file-input\">\n<input type=\"checkbox\" name=\"" + esc(cname) + "\" id=\"" + esc(cid) + "\">\n<label for=\"" + esc(cid) + "\">Clear</label></span>")
		}
		b.WriteString("<br>\nChange:")
	}
	b.WriteString("\n" + inputTag("file", name, nil, attrs))
	if val.file != "" {
		b.WriteString("</p>")
	}
	return b.String()
}

// renderPasswordHash is ReadOnlyPasswordHashWidget's summary of a stored password.
func (f *aForm) renderPasswordHash(fl *aField) string {
	return passwordSummary(valueString(f.initial[fl.name]), f.autoID(fl.name))
}

func passwordSummary(encoded, id string) string {
	div := `<div id="` + esc(id) + `">`
	if !isUsablePassword(encoded) {
		return div + "<strong>No password set.</strong></div>"
	}
	parts := strings.Split(encoded, "$")
	if len(parts) < 4 || parts[0] != "pbkdf2_sha256" && parts[0] != "pbkdf2_sha1" {
		return div + "<strong>Invalid password format or unknown hashing algorithm.</strong></div>"
	}
	mask := func(s string, keep int) string {
		if len(s) < keep {
			return strings.Repeat("*", len(s))
		}
		return s[:keep] + strings.Repeat("*", len(s)-keep)
	}
	return div + "<strong>algorithm</strong>: <bdi>" + esc(parts[0]) + "</bdi> <strong>iterations</strong>: <bdi>" + esc(parts[1]) +
		"</bdi> <strong>salt</strong>: <bdi>" + esc(mask(parts[2], 6)) + "</bdi> <strong>hash</strong>: <bdi>" + esc(mask(parts[3], 6)) + "</bdi></div>"
}

var _ = auth.MakePassword

// renderRelated wraps the field's inner widget with the add/change/delete/view links.
func (f *aForm) renderRelated(fl *aField) string {
	w := fl.w
	inner := f.renderInner(fl, fl.wi)
	if w.Class != "RelatedFieldWidgetWrapper" {
		return inner
	}
	name := f.htmlName(fl.name)
	var rel *modelSpec
	if fl.spec.Queryset != nil {
		rel = adminSiteSpec.models[fl.spec.Queryset.Label]
	} else if w.RelatedLabel != "" {
		rel = adminSiteSpec.models[w.RelatedLabel]
	}
	relName := w.ModelName
	relVerbose := relName
	if am := adminSiteSpec.any[w.RelatedLabel]; am != nil {
		relVerbose = am.VerboseName
	}
	var canAdd, canChange, canDelete, canView bool
	multiple := fl.kind == "modelmulti"
	cascade := fl.mf != nil && fl.mf.Related != nil && fl.mf.Related.OnDelete == "CASCADE"
	if rel != nil {
		perms := f.r.perms
		canAdd = perms.has(rel, "add")
		canChange = !multiple && perms.has(rel, "change")
		canDelete = !multiple && !cascade && perms.has(rel, "delete")
		canView = !multiple && perms.has(rel, "view")
	}
	var b strings.Builder
	b.WriteString(`<div class="related-widget-wrapper" data-model-ref="` + esc(relName) + `">`)
	b.WriteString("\n    " + inner + "\n    ")
	if !fl.hidden {
		toField := "id"
		if rel != nil {
			toField = rel.PK
		}
		urlParams := "_to_field=" + toField + "&_popup=1"
		if canChange {
			u := f.a.adminURL(rel, "change", "__fk__")
			b.WriteString(`<a class="related-widget-wrapper-link change-related" id="change_id_` + esc(name) + `"` + "\n" +
				`            data-href-template="` + esc(u+"?"+urlParams) + `"` + "\n" +
				`            data-popup="yes"` + "\n" +
				`            title="Change selected ` + esc(relVerbose) + `">` + "\n" +
				`            <img src="` + staticURL + `admin/img/icon-changelink.svg" alt="" width="20" height="20">` + "\n" +
				`        </a>`)
		}
		if canAdd {
			u := f.a.adminURL(rel, "add")
			b.WriteString(`<a class="related-widget-wrapper-link add-related" id="add_id_` + esc(name) + `"` + "\n" +
				`            data-popup="yes"` + "\n" +
				`            href="` + esc(u+"?"+urlParams) + `"` + "\n" +
				`            title="Add another ` + esc(relVerbose) + `">` + "\n" +
				`            <img src="` + staticURL + `admin/img/icon-addlink.svg" alt="" width="20" height="20">` + "\n" +
				`        </a>`)
		}
		if canDelete {
			u := f.a.adminURL(rel, "delete", "__fk__")
			b.WriteString(`<a class="related-widget-wrapper-link delete-related" id="delete_id_` + esc(name) + `"` + "\n" +
				`            data-href-template="` + esc(u+"?"+urlParams) + `"` + "\n" +
				`            data-popup="yes"` + "\n" +
				`            title="Delete selected ` + esc(relVerbose) + `">` + "\n" +
				`            <img src="` + staticURL + `admin/img/icon-deletelink.svg" alt="" width="20" height="20">` + "\n" +
				`        </a>`)
		}
		if canView {
			u := f.a.adminURL(rel, "change", "__fk__")
			b.WriteString(`<a class="related-widget-wrapper-link view-related" id="view_id_` + esc(name) + `"` + "\n" +
				`             data-href-template="` + esc(u+"?_to_field="+toField) + `"` + "\n" +
				`             title="View selected ` + esc(relVerbose) + `">` + "\n" +
				`            <img src="` + staticURL + `admin/img/icon-viewlink.svg" alt="" width="20" height="20">` + "\n" +
				`          </a>`)
		}
	}
	b.WriteString("\n</div>")
	return b.String()
}

// --- bound fields for the templates ------------------------------------------------------

func (f *aForm) errorsHTML(field string) string {
	return errorList(f.errs[field])
}

func (f *aForm) nonFieldErrorsHTML() string {
	if len(f.nonField) == 0 {
		return ""
	}
	return strings.Replace(errorList(f.nonField), `class="errorlist"`, `class="errorlist nonfield"`, 1)
}

func (f *aForm) idForLabel(fl *aField) string {
	switch fl.w.Class {
	case "AdminSplitDateTime", "RadioSelect":
		return ""
	}
	if fl.wi != nil && fl.wi.Class == "RadioSelect" {
		return ""
	}
	return f.autoID(fl.name)
}

// labelTag is AdminField.label_tag.
func (f *aForm) labelTag(fl *aField, isFirst bool) string {
	var classes []string
	isCheckbox := fl.w.Class == "CheckboxInput"
	if isCheckbox {
		classes = append(classes, "vCheckboxLabel")
	}
	if fl.required {
		classes = append(classes, "required")
	}
	if !isFirst {
		classes = append(classes, "inline")
	}
	contents := esc(fl.label)
	if !isCheckbox && contents != "" && !strings.ContainsAny(contents[len(contents)-1:], ":?.!") {
		contents += ":"
	}
	var attrs []attr
	if len(classes) > 0 {
		attrs = append(attrs, attr{"class", strings.Join(classes, " ")})
	}
	if id := f.idForLabel(fl); id != "" {
		attrs = append(attrs, attr{"for", id})
	}
	return "<label" + attrsHTML(attrs) + ">" + contents + "</label>"
}

// boundMap is the BoundField as the templates read it.
func (f *aForm) boundMap(fl *aField) boundField {
	html := f.renderWidget(fl)
	if callableDefault(fl.mf) && !fl.disabled {
		html += f.hiddenInitial(fl)
	}
	bf := boundField{
		"_html": html, "name": fl.name, "label": fl.label, "is_hidden": fl.hidden,
		"id_for_label": f.idForLabel(fl), "help_text": safe(fl.help), "errors": safe(f.errorsHTML(fl.name)),
		"html_name": f.htmlName(fl.name), "has_errors": len(f.errs[fl.name]) > 0, "required": fl.required,
	}
	if fl.help == "" {
		bf["help_text"] = ""
	}
	return bf
}

var _ = fmt.Sprint

// hiddenInitial is the hidden input that carries the initial value of a field whose default is a function.
func (f *aForm) hiddenInitial(fl *aField) string {
	name := "initial-" + f.htmlName(fl.name)
	id := "initial-" + f.autoID(fl.name)
	val := f.fieldValue(fl)
	if fl.kind == "splitdt" {
		var out string
		for i := 0; i < 2; i++ {
			var v *string
			if val.hasP[i] {
				s := val.parts[i]
				v = &s
			}
			out += inputTag("hidden", name+"_"+strconv.Itoa(i), v, []attr{{"id", id + "_" + strconv.Itoa(i)}})
		}
		return out
	}
	var v *string
	if val.has {
		v = &val.text
	}
	return inputTag("hidden", name, v, []attr{{"id", id}})
}

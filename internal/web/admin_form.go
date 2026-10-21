package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/idna"

	"github.com/shahinshabab/deedflow-go/internal/store"
)

// The admin's forms: a Go version of the parts of django.forms the project's
// model admins use. A form is built from the admin spec (fields, widgets,
// labels), bound to POST data, validated like Django does (field cleaning,
// model validation, uniqueness) and saved.

func idnaASCII(s string) (string, error) { return idna.Lookup.ToASCII(s) }

type adminUpload struct {
	Name  string
	Data  []byte
	Saved string // the stored name, once it has been saved
}

// choice is one option of a select.
type choice struct {
	value string
	label string
}

type aField struct {
	name     string
	spec     *formFieldSpec
	kind     string
	label    string
	help     string
	required bool
	disabled bool
	w        *widgetSpec // the outer widget
	wi       *widgetSpec // the widget inside a related-object wrapper (else the same)
	mf       *modelFieldSpec
	hidden   bool
}

var fieldKinds = map[string]string{
	"CharField": "char", "UsernameField": "username", "EmailField": "email", "IntegerField": "int",
	"DecimalField": "decimal", "DateField": "date", "SplitDateTimeField": "splitdt", "BooleanField": "bool",
	"TypedChoiceField": "choice", "ChoiceField": "choice", "ModelChoiceField": "model",
	"ModelMultipleChoiceField": "modelmulti", "FileField": "file", "JSONField": "json",
	"ReadOnlyPasswordHashField": "ropass",
}

func newAField(name string, spec *formFieldSpec) *aField {
	f := &aField{name: name, spec: spec, kind: fieldKinds[spec.FieldClass], required: spec.Required, disabled: spec.Disabled,
		help: spec.HelpText, w: &spec.Widget, mf: spec.ModelField}
	if f.kind == "" {
		panic("admin: unknown form field class " + spec.FieldClass)
	}
	if spec.Label != nil {
		f.label = *spec.Label
	} else {
		f.label = capfirst(strings.ReplaceAll(name, "_", " "))
	}
	f.wi = f.w
	if f.w.Inner != nil {
		f.wi = f.w.Inner
	}
	f.hidden = f.wi.InputType == "hidden"
	return f
}

// aForm is a ModelForm (or a plain form for the user and password forms).
type aForm struct {
	r      *adminReq
	a      *App
	ctx    context.Context
	m      *modelSpec
	prefix string
	order  []string
	fields map[string]*aField

	bound bool
	data  url.Values
	files map[string]*adminUpload

	inst    *adminObj      // the object being changed (nil when adding)
	initial map[string]any // initial values by field name
	m2mInit map[string][]string

	errs      map[string][]string
	errOrder  []string
	nonField  []string
	cleaned   map[string]any
	vals      map[string]any // the instance's values after construct_instance
	cleanedOK map[string]bool

	choiceCache map[string][]choice
	re          *reprEnv

	// inline forms
	noRequired  bool   // formset forms leave the required attribute out
	inlineFK    string // the foreign key field to the parent, excluded from validation
	inlineFKVal any
	emptyOK     bool

	// hooks for forms that are not plain model forms
	formClean func(f *aForm)
	postClean func(f *aForm)
	cleanFld  map[string]func(f *aForm, v any) (any, string)
	modelOnly bool
}

func (a *App) newForm(r *adminReq, m *modelSpec, specs map[string]*formFieldSpec, order []string, prefix string) *aForm {
	f := &aForm{r: r, a: a, ctx: r.c.Context(), m: m, prefix: prefix, order: order, fields: map[string]*aField{},
		errs: map[string][]string{}, cleaned: map[string]any{}, cleanedOK: map[string]bool{}, initial: map[string]any{},
		m2mInit: map[string][]string{}, choiceCache: map[string][]choice{}, re: r.reprEnv()}
	for _, n := range order {
		sp := specs[n]
		if sp == nil {
			continue
		}
		f.fields[n] = newAField(n, sp)
	}
	return f
}

func (f *aForm) htmlName(n string) string {
	if f.prefix == "" {
		return n
	}
	return f.prefix + "-" + n
}

func (f *aForm) autoID(n string) string { return "id_" + f.htmlName(n) }

func (f *aForm) addError(field, msg string) {
	if field != "" {
		delete(f.cleaned, field)
		f.cleanedOK[field] = false
	}
	if field == "" {
		f.nonField = append(f.nonField, msg)
		return
	}
	if _, ok := f.errs[field]; !ok {
		f.errOrder = append(f.errOrder, field)
	}
	f.errs[field] = append(f.errs[field], msg)
}

func (f *aForm) hasErrors() bool { return len(f.errs) > 0 || len(f.nonField) > 0 }

// errorLists is form.errors.values() as the change view counts them.
func (f *aForm) errorLists() int {
	n := len(f.errs)
	if len(f.nonField) > 0 {
		n++
	}
	return n
}

func (f *aForm) bind(data url.Values, files map[string]*adminUpload) {
	f.bound = true
	f.data = data
	f.files = files
}

// --- initial values ----------------------------------------------------------------------

// modelDefault is Field.get_default() for a model field.
func (a *App) modelDefault(mf *modelFieldSpec) any {
	if mf.HasDefault {
		if ds, ok := mf.Default.(string); ok && strings.HasPrefix(ds, "<class '") {
			switch ds {
			case "<class 'dict'>":
				return "{}"
			case "<class 'list'>":
				return "[]"
			}
		}
		if mf.Default == nil {
			switch mf.InternalType {
			case "DateTimeField":
				return time.Now().In(a.Cfg.TimeZone)
			case "UUIDField":
				return store.NewUUID()
			}
			return nil
		}
		switch v := mf.Default.(type) {
		case float64:
			if mf.InternalType == "DecimalField" {
				return strconv.FormatFloat(v, 'f', -1, 64)
			}
			return int64(v)
		}
		return mf.Default
	}
	switch mf.InternalType {
	case "CharField", "TextField", "FileField", "EmailField":
		if mf.Null {
			return nil
		}
		return ""
	}
	return nil
}

// callableDefault reports a model default that is a function (Django then
// shows the form field's initial value in a hidden input as well).
func callableDefault(mf *modelFieldSpec) bool {
	if mf == nil || !mf.HasDefault {
		return false
	}
	if mf.Default == nil {
		return mf.InternalType == "DateTimeField" || mf.InternalType == "UUIDField"
	}
	ds, ok := mf.Default.(string)
	return ok && strings.HasPrefix(ds, "<class '")
}

// setInitialFromInstance is model_to_dict(instance) for the form's fields.
func (f *aForm) setInitialFromInstance(o *adminObj) {
	f.inst = o
	for n, fl := range f.fields {
		if fl.mf == nil {
			continue
		}
		if fl.mf.isM2M() {
			continue
		}
		f.initial[n] = o.vals[n]
	}
}

// setInitialDefaults is model_to_dict(Model()) plus the form fields' own initial values.
func (f *aForm) setInitialDefaults() {
	for n, fl := range f.fields {
		if fl.mf != nil && !fl.mf.isM2M() {
			f.initial[n] = f.a.modelDefault(fl.mf)
			continue
		}
		if fl.mf == nil || fl.mf.isM2M() {
			if fl.spec.Initial != nil {
				f.initial[n] = fl.spec.Initial
			}
		}
	}
}

// --- reading data -----------------------------------------------------------------------

// rawValue is what the widget reads out of the submitted data.
type rawValue struct {
	s       *string
	list    []string
	parts   [2]*string
	file    *adminUpload
	clear   bool
	present bool
}

func (f *aForm) valueFromData(fl *aField) rawValue {
	name := f.htmlName(fl.name)
	var rv rawValue
	switch fl.kind {
	case "modelmulti":
		rv.list = f.data[name]
		rv.present = len(rv.list) > 0
	case "splitdt":
		for i := 0; i < 2; i++ {
			if v, ok := f.data[name+"_"+strconv.Itoa(i)]; ok && len(v) > 0 {
				s := v[len(v)-1]
				rv.parts[i] = &s
				rv.present = true
			}
		}
	case "bool":
		if v, ok := f.data[name]; ok && len(v) > 0 {
			s := v[len(v)-1]
			rv.s = &s
			rv.present = true
		}
	case "file":
		rv.file = f.files[name]
		if v, ok := f.data[name+"-clear"]; ok && len(v) > 0 && v[len(v)-1] != "" {
			rv.clear = true
		}
		rv.present = rv.file != nil
		if _, ok := f.data[name+"-clear"]; ok {
			rv.present = true
		}
	default:
		if v, ok := f.data[name]; ok && len(v) > 0 {
			s := v[len(v)-1]
			rv.s = &s
			rv.present = true
		}
	}
	return rv
}

// --- cleaning ---------------------------------------------------------------------------------

func intPtr(v any) *int64 {
	switch x := v.(type) {
	case float64:
		n := int64(x)
		return &n
	case int64:
		return &x
	case int:
		n := int64(x)
		return &n
	}
	return nil
}

// validatorLimits reads the model field's min/max value validators.
func validatorLimits(mf *modelFieldSpec) (min, max *int64) {
	if mf == nil {
		return
	}
	for _, v := range mf.Validators {
		name, arg, _ := strings.Cut(v, ":")
		n, err := strconv.ParseInt(arg, 10, 64)
		if err != nil {
			continue
		}
		switch name {
		case "MinValueValidator":
			x := n
			min = &x
		case "MaxValueValidator":
			x := n
			max = &x
		}
	}
	return
}

func hasValidator(mf *modelFieldSpec, name string) bool {
	if mf == nil {
		return false
	}
	for _, v := range mf.Validators {
		if strings.HasPrefix(v, name+":") {
			return true
		}
	}
	return false
}

// cleanField is Field.clean for one field: the cleaned value or an error list.
func (f *aForm) cleanField(fl *aField) (any, []string) {
	rv := f.valueFromData(fl)
	if fl.disabled {
		return f.initial[fl.name], nil
	}
	req := func() []string { return []string{msgRequired} }
	sp := fl.spec
	switch fl.kind {
	case "char", "username", "email", "ropass":
		var raw string
		if rv.s != nil {
			raw = *rv.s
		}
		strip := sp.Strip == nil || *sp.Strip
		if fl.kind == "ropass" {
			return f.initial[fl.name], nil
		}
		s, msg := cleanChar(raw, strip, sp.MinLength, sp.MaxLength)
		if fl.kind == "username" {
			s = nfkc(s)
		}
		if msg != "" {
			return nil, []string{msg}
		}
		if s == "" {
			if fl.required {
				return nil, req()
			}
			return sp.EmptyValue, nil
		}
		if fl.kind == "email" && !validEmail(s) {
			return nil, []string{"Enter a valid email address."}
		}
		if fn := f.cleanFld[fl.name]; fn != nil {
			v, msg := fn(f, s)
			if msg != "" {
				return nil, []string{msg}
			}
			return v, nil
		}
		return s, nil
	case "int":
		var raw string
		if rv.s != nil {
			raw = *rv.s
		}
		min, max := intPtr(sp.MinValue), intPtr(sp.MaxValue)
		v, msg := cleanInteger(raw, min, max)
		if msg != "" {
			return nil, []string{msg}
		}
		if v == nil {
			if fl.required {
				return nil, req()
			}
			return nil, nil
		}
		return *v, nil
	case "decimal":
		var raw string
		if rv.s != nil {
			raw = *rv.s
		}
		d, ok, msg := cleanDecimal(raw, sp.MaxDigits, sp.DecimalPlace)
		if msg != "" {
			return nil, []string{msg}
		}
		if !ok {
			if fl.required {
				return nil, req()
			}
			return nil, nil
		}
		return d, nil
	case "date":
		var raw string
		if rv.s != nil {
			raw = pyStrip(*rv.s)
		}
		if raw == "" {
			if fl.required {
				return nil, req()
			}
			return nil, nil
		}
		t, ok := parseDateInput(raw)
		if !ok {
			return nil, []string{"Enter a valid date."}
		}
		return t, nil
	case "splitdt":
		return f.cleanSplitDateTime(fl, rv)
	case "bool":
		v := false
		if rv.s != nil {
			l := strings.ToLower(*rv.s)
			switch l {
			case "true":
				v = true
			case "false":
				v = false
			default:
				v = *rv.s != ""
			}
			if l == "0" {
				// BooleanField.to_python turns "0" into False after the widget made it True
				v = false
			}
		}
		if !v && fl.required {
			return nil, req()
		}
		return v, nil
	case "choice":
		var raw string
		if rv.s != nil {
			raw = *rv.s
		}
		if raw == "" {
			if fl.required {
				return nil, req()
			}
			return sp.EmptyValue, nil
		}
		for _, c := range f.choicesOf(fl) {
			if c.value == raw {
				if fl.mf != nil && fl.mf.isInt() {
					if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
						return n, nil
					}
				}
				return raw, nil
			}
		}
		return nil, []string{fmt.Sprintf("Select a valid choice. %s is not one of the available choices.", raw)}
	case "model":
		var raw string
		if rv.s != nil {
			raw = *rv.s
		}
		if raw == "" {
			if fl.required {
				return nil, req()
			}
			return nil, nil
		}
		if strings.ContainsRune(raw, 0) {
			return nil, []string{"Null characters are not allowed."}
		}
		id, ok := f.lookupChoice(fl, raw)
		if !ok {
			return nil, []string{"Select a valid choice. That choice is not one of the available choices."}
		}
		return id, nil
	case "modelmulti":
		if len(rv.list) == 0 {
			if fl.required {
				return nil, req()
			}
			return []int64{}, nil
		}
		var ids []int64
		seen := map[int64]bool{}
		for _, raw := range rv.list {
			id, ok := f.lookupChoice(fl, raw)
			if !ok {
				if _, err := strconv.ParseInt(raw, 10, 64); err != nil {
					return nil, []string{fmt.Sprintf("“%s” is not a valid value.", raw)}
				}
				return nil, []string{fmt.Sprintf("Select a valid choice. %s is not one of the available choices.", raw)}
			}
			if !seen[id.(int64)] {
				seen[id.(int64)] = true
				ids = append(ids, id.(int64))
			}
		}
		return ids, nil
	case "file":
		return f.cleanFile(fl, rv)
	case "json":
		var raw string
		if rv.s != nil {
			raw = *rv.s
		}
		if raw == "" {
			if fl.required {
				return nil, req()
			}
			return nil, nil
		}
		var v any
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&v); err != nil || dec.More() {
			return nil, []string{"Enter a valid JSON."}
		}
		if dec.InputOffset() != int64(len(strings.TrimRight(raw, " \t\r\n"))) {
			if _, err := dec.Token(); err == nil {
				return nil, []string{"Enter a valid JSON."}
			}
		}
		return jsonValue{v}, nil
	}
	return nil, []string{"unsupported field"}
}

// jsonValue is a parsed JSONField value (so that a JSON null differs from None).
type jsonValue struct{ v any }

func (f *aForm) cleanSplitDateTime(fl *aField, rv rawValue) (any, []string) {
	empty := func(s *string) bool { return s == nil || *s == "" }
	if empty(rv.parts[0]) && empty(rv.parts[1]) {
		if fl.required {
			return nil, []string{msgRequired}
		}
		return nil, nil
	}
	var errs []string
	var d time.Time
	var tm time.Duration
	for i := 0; i < 2; i++ {
		if empty(rv.parts[i]) {
			if fl.required {
				return nil, []string{msgRequired}
			}
			continue
		}
	}
	if !empty(rv.parts[0]) {
		t, ok := parseDateInput(*rv.parts[0])
		if !ok {
			errs = append(errs, "Enter a valid date.")
		}
		d = t
	}
	if !empty(rv.parts[1]) {
		t, ok := parseTimeInput(*rv.parts[1])
		if !ok {
			errs = append(errs, "Enter a valid time.")
		}
		tm = t
	}
	if len(errs) > 0 {
		return nil, errs
	}
	if empty(rv.parts[0]) {
		return nil, []string{"Enter a valid date."}
	}
	if empty(rv.parts[1]) {
		return nil, []string{"Enter a valid time."}
	}
	loc := f.a.Cfg.TimeZone
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc).Add(tm), nil
}

// cleanFile is FileField.clean.
func (f *aForm) cleanFile(fl *aField, rv rawValue) (any, []string) {
	initial := f.initial[fl.name]
	hasInitial := valueString(initial) != ""
	if rv.clear && rv.file != nil && !fl.required {
		return nil, []string{"Please either submit a file or check the clear checkbox, not both."}
	}
	if rv.clear && !fl.required {
		return fileClear{}, nil
	}
	if rv.file == nil {
		if fl.required && !hasInitial {
			return nil, []string{msgRequired}
		}
		return nil, nil // no change
	}
	name := rv.file.Name
	if fl.spec.MaxLength != nil && runeLen(name) > *fl.spec.MaxLength {
		return nil, []string{fmt.Sprintf("Ensure this filename has at most %d characters (it has %d).", *fl.spec.MaxLength, runeLen(name))}
	}
	if name == "" {
		return nil, []string{"No file was submitted. Check the encoding type on the form."}
	}
	if len(rv.file.Data) == 0 {
		return nil, []string{"The submitted file is empty."}
	}
	return rv.file, nil
}

type fileClear struct{}

// --- choices -------------------------------------------------------------------------------

func (f *aForm) choicesOf(fl *aField) []choice {
	if fl.kind == "model" || fl.kind == "modelmulti" {
		return f.modelChoices(fl)
	}
	var out []choice
	src := fl.spec.Choices
	if len(src) == 0 {
		src = fl.wi.Choices
	}
	for _, c := range src {
		if len(c) == 2 {
			out = append(out, choice{c[0], c[1]})
		}
	}
	return out
}

func orderSQL(m *modelSpec, ordering []string, alias string) (string, string) {
	var parts []string
	join := ""
	for _, o := range ordering {
		desc := strings.HasPrefix(o, "-")
		n := strings.TrimPrefix(o, "-")
		var col string
		if strings.Contains(n, "__") {
			// content_type__app_label on permissions
			p := strings.SplitN(n, "__", 2)
			if m.Label == "auth.permission" && p[0] == "content_type" {
				join = " JOIN django_content_type ct ON ct.id = " + alias + ".content_type_id"
				col = "ct.\"" + p[1] + "\""
			}
		} else if n == "pk" {
			col = alias + `."` + m.pkField().Column + `"`
		} else if mf := m.field(n); mf != nil {
			col = alias + `."` + mf.Column + `"`
		}
		if col == "" {
			continue
		}
		if desc {
			col += " DESC"
		}
		parts = append(parts, col)
	}
	if len(parts) == 0 {
		return join, ""
	}
	return join, " ORDER BY " + strings.Join(parts, ", ")
}

// modelChoices are the objects of the field's queryset, as options.
func (f *aForm) modelChoices(fl *aField) []choice {
	if cs, ok := f.choiceCache[fl.name]; ok {
		return cs
	}
	q := fl.spec.Queryset
	rel := adminSiteSpec.model(q.Label)
	join, order := orderSQL(rel, q.Ordering, "t")
	rows, err := f.r.q().Query(f.ctx, "SELECT "+rel.selectList("t")+" FROM "+quoteIdent(rel.Table)+" t"+join+order)
	var out []choice
	if err == nil {
		objs, err2 := f.a.scanObjs(rel, rows)
		if err2 == nil {
			for _, o := range objs {
				out = append(out, choice{o.pkString(), f.re.str(o)})
			}
		}
	}
	f.choiceCache[fl.name] = out
	return out
}

// lookupChoice finds the object a submitted value points at.
func (f *aForm) lookupChoice(fl *aField, raw string) (any, bool) {
	q := fl.spec.Queryset
	rel := adminSiteSpec.model(q.Label)
	arg, ok := coerceValue(rel.pkField(), raw)
	if !ok {
		return nil, false
	}
	var one int
	err := f.r.q().QueryRow(f.ctx, fmt.Sprintf(`SELECT 1 FROM %s WHERE "%s" = $1`, quoteIdent(rel.Table), rel.pkField().Column), arg).Scan(&one)
	if err != nil {
		return nil, false
	}
	return arg, true
}

// --- full clean -------------------------------------------------------------------------------

func (f *aForm) isValid() bool {
	if !f.bound {
		return false
	}
	f.fullClean()
	return !f.hasErrors()
}

func (f *aForm) fullClean() {
	f.cleaned = map[string]any{}
	f.cleanedOK = map[string]bool{}
	for _, n := range f.order {
		fl := f.fields[n]
		if fl == nil {
			continue
		}
		v, errs := f.cleanField(fl)
		if len(errs) > 0 {
			for _, e := range errs {
				f.addError(n, e)
			}
			continue
		}
		f.cleaned[n] = v
		f.cleanedOK[n] = true
	}
	if f.emptyOK && !f.hasChanged() {
		// an unchanged extra form is dropped without being validated
		f.errs, f.errOrder, f.nonField = map[string][]string{}, nil, nil
		return
	}
	if f.formClean != nil {
		f.formClean(f)
	}
	f.postCleanModel()
	if f.postClean != nil {
		f.postClean(f)
	}
}

// --- changes ----------------------------------------------------------------------------------

// changedFields is form.changed_data.
func (f *aForm) changedFields() []string {
	var out []string
	for _, n := range f.order {
		fl := f.fields[n]
		if fl == nil || fl.disabled {
			continue
		}
		if f.fieldChanged(fl) {
			out = append(out, n)
		}
	}
	return out
}

func (f *aForm) hasChanged() bool { return len(f.changedFields()) > 0 }

func (f *aForm) fieldChanged(fl *aField) bool {
	rv := f.valueFromData(fl)
	init := f.initial[fl.name]
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	switch fl.kind {
	case "char", "username", "email":
		return valueString(init) != str(rv.s)
	case "int":
		v, msg := cleanInteger(str(rv.s), nil, nil)
		if msg != "" {
			return true
		}
		return valueString(init) != valueString(ptrVal(v))
	case "decimal":
		d, ok, msg := cleanDecimal(str(rv.s), nil, nil)
		if msg != "" {
			return true
		}
		if !ok {
			return init != nil && valueString(init) != ""
		}
		return init == nil || !decimalEqual(valueString(init), d)
	case "date":
		raw := pyStrip(str(rv.s))
		var it string
		if t, ok := init.(time.Time); ok {
			it = t.Format("2006-01-02")
		}
		if raw == "" {
			return it != ""
		}
		t, ok := parseDateInput(raw)
		if !ok {
			return true
		}
		return t.Format("2006-01-02") != it
	case "splitdt":
		it, _ := init.(time.Time)
		var ds, ts string
		if !it.IsZero() {
			it = it.In(f.a.Cfg.TimeZone)
			ds, ts = it.Format("2006-01-02"), it.Format("15:04:05")
		}
		nd, nt := str(rv.parts[0]), str(rv.parts[1])
		if nd == "" && nt == "" {
			return !it.IsZero()
		}
		d, ok1 := parseDateInput(nd)
		tm, ok2 := parseTimeInput(nt)
		if !ok1 || !ok2 {
			return true
		}
		return d.Format("2006-01-02") != ds || time.Time{}.Add(tm).Format("15:04:05") != ts
	case "bool":
		b, _ := init.(bool)
		return b != (rv.s != nil && *rv.s != "" && strings.ToLower(*rv.s) != "false" && *rv.s != "0")
	case "choice":
		return valueString(init) != str(rv.s)
	case "model":
		return valueString(init) != str(rv.s)
	case "modelmulti":
		cur := map[string]bool{}
		for _, v := range f.m2mInit[fl.name] {
			cur[v] = true
		}
		if len(rv.list) != len(f.m2mInit[fl.name]) {
			return true
		}
		for _, v := range rv.list {
			if !cur[v] {
				return true
			}
		}
		return false
	case "file":
		return rv.file != nil || rv.clear
	case "json":
		raw := str(rv.s)
		var cur string
		if init != nil {
			cur = jsonCanonical(valueString(init))
		} else {
			cur = "null"
		}
		if raw == "" {
			return cur != "null"
		}
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return true
		}
		b, _ := json.Marshal(v)
		return jsonCanonical(string(b)) != cur
	}
	return false
}

func ptrVal(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// jsonCanonical re-encodes JSON with sorted keys.
func jsonCanonical(s string) string {
	var v any
	if json.Unmarshal([]byte(s), &v) != nil {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// --- model validation ---------------------------------------------------------------------

// postCleanModel is ModelForm._post_clean: the values go into the instance,
// then it is validated (fields, uniqueness, constraints).
func (f *aForm) postCleanModel() {
	if f.modelOnly {
		return
	}
	// construct_instance
	f.vals = map[string]any{}
	if f.inst != nil {
		for k, v := range f.inst.vals {
			f.vals[k] = v
		}
	} else {
		for _, mf := range f.m.ModelFields {
			f.vals[mf.Name] = f.a.modelDefault(mf)
		}
	}
	for _, n := range f.order {
		fl := f.fields[n]
		if fl == nil || fl.mf == nil || fl.mf.isM2M() || !f.cleanedOK[n] || !fl.mf.Editable {
			continue
		}
		v := f.cleaned[n]
		// fields left out of the POST keep their default
		if fl.mf.HasDefault && !f.valueFromData(fl).present && f.emptyValue(fl, v) {
			continue
		}
		switch x := v.(type) {
		case fileClear:
			f.vals[n] = ""
		case *adminUpload:
			f.vals[n] = x
		case nil:
			if fl.kind == "file" {
				continue // no change
			}
			f.vals[n] = nil
		case jsonValue:
			f.vals[n] = x
		default:
			f.vals[n] = x
		}
	}
	exclude := map[string]bool{}
	for _, mf := range f.m.ModelFields {
		fl := f.fields[mf.Name]
		switch {
		case fl == nil:
			exclude[mf.Name] = true
		case !f.cleanedOK[mf.Name]:
			exclude[mf.Name] = true
		case !mf.Blank && !fl.required && f.emptyValue(fl, f.cleaned[mf.Name]):
			exclude[mf.Name] = true
		}
	}
	if f.inlineFK != "" {
		exclude[f.inlineFK] = true
	}
	f.validateFields(exclude)
	f.validateUnique(exclude)
	f.validateConstraints(exclude)
}

// emptyValue reports whether a cleaned value is in the form field's empty_values.
func (f *aForm) emptyValue(fl *aField, v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case []int64:
		return len(x) == 0
	case jsonValue:
		return false
	}
	return false
}

// validateFields runs the model field validators on the values that made it in.
func (f *aForm) validateFields(exclude map[string]bool) {
	for _, mf := range f.m.ModelFields {
		if exclude[mf.Name] || mf.PrimaryKey && mf.InternalType == "AutoField" {
			continue
		}
		v := f.vals[mf.Name]
		if mf.Blank && (v == nil || v == "") {
			continue
		}
		fl := f.fields[mf.Name]
		if fl == nil {
			continue
		}
		s := valueString(v)
		var msgs []string
		min, max := validatorLimits(mf)
		switch mf.InternalType {
		case "CharField", "TextField":
			if mf.MaxLength != nil && runeLen(s) > *mf.MaxLength {
				msgs = append(msgs, fmt.Sprintf("Ensure this value has at most %d %s (it has %d).", *mf.MaxLength, plural(*mf.MaxLength, "character", "characters"), runeLen(s)))
			}
			if hasValidator(mf, "UnicodeUsernameValidator") && !isUsername(s) {
				msgs = append(msgs, "Enter a valid username. This value may contain only letters, numbers, and @/./+/-/_ characters.")
			}
			if hasValidator(mf, "EmailValidator") && s != "" && !validEmail(s) {
				msgs = append(msgs, "Enter a valid email address.")
			}
		case "PositiveSmallIntegerField", "PositiveIntegerField", "IntegerField", "SmallIntegerField", "BigIntegerField":
			if n, ok := v.(int64); ok {
				if min != nil && n < *min {
					msgs = append(msgs, fmt.Sprintf("Ensure this value is greater than or equal to %d.", *min))
				}
				if max != nil && n > *max {
					msgs = append(msgs, fmt.Sprintf("Ensure this value is less than or equal to %d.", *max))
				}
			}
		}
		if len(mf.Choices) > 0 && s != "" {
			found := false
			for _, c := range mf.Choices {
				if len(c) == 2 && c[0] == s {
					found = true
				}
			}
			if !found {
				msgs = append(msgs, fmt.Sprintf("Value '%s' is not a valid choice.", s))
			}
		}
		for _, m := range msgs {
			f.addError(mf.Name, m)
		}
	}
}

func capfirstWords(s string) string { return capfirst(s) }

var uniqueMessages = map[string]string{"auth.user.username": "A user with that username already exists."}

// validateUnique is Model.validate_unique for unique fields and unique_together.
func (f *aForm) validateUnique(exclude map[string]bool) {
	type check struct {
		fields []string
	}
	var checks []check
	for _, mf := range f.m.ModelFields {
		if mf.Unique && !exclude[mf.Name] && !(mf.PrimaryKey) {
			checks = append(checks, check{[]string{mf.Name}})
		}
	}
	for _, ut := range f.m.UniqueTogether {
		checks = append(checks, check{ut})
	}
	for _, c := range checks {
		skip := false
		for _, n := range c.fields {
			if exclude[n] {
				skip = true
			}
		}
		if skip {
			continue
		}
		if f.uniqueViolated(c.fields) {
			f.addUniqueError(c.fields)
		}
	}
}

func (f *aForm) uniqueViolated(fields []string) bool {
	var conds []string
	var args []any
	for _, n := range fields {
		mf := f.m.field(n)
		v := f.vals[n]
		if v == nil {
			return false
		}
		col := `t."` + mf.Column + `"`
		var arg any = v
		cast := ""
		switch x := v.(type) {
		case jsonValue:
			return false
		case *adminUpload:
			return false
		case time.Time, int64, bool:
			arg = x
		case string:
			switch mf.InternalType {
			case "DecimalField":
				cast = "::numeric"
			case "UUIDField":
				cast = "::uuid"
			}
		}
		args = append(args, arg)
		conds = append(conds, fmt.Sprintf("%s = $%d%s", col, len(args), cast))
	}
	if len(conds) != len(fields) {
		return false
	}
	if f.inst != nil {
		args = append(args, f.inst.vals[f.m.PK])
		conds = append(conds, fmt.Sprintf(`t."%s" <> $%d`, f.m.pkField().Column, len(args)))
	}
	var one int
	err := f.r.q().QueryRow(f.ctx, fmt.Sprintf("SELECT 1 FROM %s t WHERE %s LIMIT 1", quoteIdent(f.m.Table), strings.Join(conds, " AND ")), args...).Scan(&one)
	return err == nil
}

func (f *aForm) addUniqueError(fields []string) {
	opts := f.m
	modelName := capfirst(opts.VerboseName)
	if len(fields) == 1 {
		mf := f.m.field(fields[0])
		if msg, ok := uniqueMessages[f.m.Label+"."+mf.Name]; ok {
			f.addError(mf.Name, msg)
			return
		}
		f.addError(mf.Name, fmt.Sprintf("%s with this %s already exists.", modelName, capfirst(mf.VerboseName)))
		return
	}
	var labels []string
	for _, n := range fields {
		labels = append(labels, capfirst(f.m.field(n).VerboseName))
	}
	f.addError("", fmt.Sprintf("%s with this %s already exists.", modelName, textList(labels)))
}

var reCondition = regexp.MustCompile(`^\(AND: \('(\w+)', '([^']*)'\)\)$`)

// validateConstraints checks the model's unique constraints.
func (f *aForm) validateConstraints(exclude map[string]bool) {
	for _, uc := range f.m.UniqueConstraints {
		skip := false
		for _, n := range uc.Fields {
			if exclude[n] {
				skip = true
			}
		}
		if skip {
			continue
		}
		var conds []string
		var args []any
		null := false
		for _, n := range uc.Fields {
			v := f.vals[n]
			if v == nil {
				null = true
				break
			}
			args = append(args, v)
			conds = append(conds, fmt.Sprintf(`t."%s" = $%d`, f.m.field(n).Column, len(args)))
		}
		if null {
			continue
		}
		if uc.Condition != nil {
			cm := reCondition.FindStringSubmatch(*uc.Condition)
			if cm == nil {
				continue
			}
			cf := f.m.field(cm[1])
			// the instance itself has to match the condition
			if valueString(f.vals[cm[1]]) != cm[2] {
				continue
			}
			args = append(args, cm[2])
			conds = append(conds, fmt.Sprintf(`t."%s" = $%d`, cf.Column, len(args)))
		}
		if f.inst != nil {
			args = append(args, f.inst.vals[f.m.PK])
			conds = append(conds, fmt.Sprintf(`t."%s" <> $%d`, f.m.pkField().Column, len(args)))
		}
		var one int
		err := f.r.q().QueryRow(f.ctx, fmt.Sprintf("SELECT 1 FROM %s t WHERE %s LIMIT 1", quoteIdent(f.m.Table), strings.Join(conds, " AND ")), args...).Scan(&one)
		if err != nil {
			continue
		}
		if uc.Condition != nil {
			f.addError("", fmt.Sprintf("Constraint “%s” is violated.", uc.Name))
		} else {
			f.addUniqueError(uc.Fields)
		}
	}
}

var _ = sort.Strings

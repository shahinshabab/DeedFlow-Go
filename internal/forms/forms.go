// Package forms is a small take on Django's forms: declared fields, binding
// to submitted data, Django's validation messages, and rendering of the same
// widgets (so the templates and their JavaScript keep working unchanged).
package forms

import (
	"fmt"
	"html"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shahinshabab/deedflow-go/internal/db"
)

// Kind is the type of a field's value.
type Kind int

const (
	Char Kind = iota
	Int
	Date
	Decimal
	Choice
	Bool
	Email
)

// Widget is how a field is drawn.
type Widget int

const (
	TextInput Widget = iota
	Textarea
	Select
	Checkbox
	NumberInput
	DateInput
	PasswordInput
	HiddenInput
)

// ChoiceOpt is one option of a choice field.
type ChoiceOpt struct{ Value, Label string }

// Field declares one form field.
type Field struct {
	Name      string
	Label     string
	Kind      Kind
	Widget    Widget
	Required  bool
	MaxLength int
	Min, Max  *int64 // for Int
	// DecimalPlaces / MaxDigits describe a Decimal field.
	DecimalPlaces int
	MaxDigits     int
	Choices       []ChoiceOpt
	HelpText      string
	Disabled      bool
	Rows          int
	Attrs         map[string]string
	// RenderValue lets a password input show its value again.
	RenderValue bool
	// MinValueMsg overrides nothing; reserved for model-level validators.
	Initial string
}

// Form is a set of fields, optionally bound to submitted data.
type Form struct {
	Prefix string
	Fields []*Field
	// FullWidth lists fields that span the whole row (ScheduleItemForm).
	FullWidth []string

	Bound          bool
	Data           url.Values
	Initial        map[string]string
	Errors         map[string][]string
	NonFieldErrors []string
	Cleaned        map[string]any
	EmptyPermitted bool
	// Clean runs after the fields validated.
	Clean func(f *Form)
	// BaseClass is the CSS class for text-like widgets ("form-control").
	hiddenExtra []hiddenField
	validated   bool
}

type hiddenField struct{ name, value string }

// New creates an unbound form.
func New(prefix string, fields ...*Field) *Form {
	return &Form{Prefix: prefix, Fields: fields, Initial: map[string]string{}, Errors: map[string][]string{}, Cleaned: map[string]any{}}
}

// Bind attaches submitted data.
func (f *Form) Bind(data url.Values) *Form {
	f.Bound = true
	f.Data = data
	return f
}

// AddPrefix is Django's add_prefix.
func (f *Form) AddPrefix(name string) string {
	if f.Prefix == "" {
		return name
	}
	return f.Prefix + "-" + name
}

// IDFor is the id of a field's input.
func (f *Form) IDFor(name string) string { return "id_" + f.AddPrefix(name) }

// Field returns a declared field by name.
func (f *Form) Field(name string) *Field {
	for _, fl := range f.Fields {
		if fl.Name == name {
			return fl
		}
	}
	return nil
}

// Value is the raw string value a field currently shows: submitted data when
// bound, else its initial value.
func (f *Form) Value(name string) string {
	if f.Bound {
		fl := f.Field(name)
		if fl != nil && fl.Disabled {
			return f.Initial[name]
		}
		if fl != nil && fl.Widget == Checkbox {
			if _, ok := f.Data[f.AddPrefix(name)]; ok {
				return "on"
			}
			return ""
		}
		return f.Data.Get(f.AddPrefix(name))
	}
	if v, ok := f.Initial[name]; ok {
		return v
	}
	if fl := f.Field(name); fl != nil {
		return fl.Initial
	}
	return ""
}

// AddError records a validation error; name "" is a non-field error.
func (f *Form) AddError(name, msg string) {
	if name == "" {
		f.NonFieldErrors = append(f.NonFieldErrors, msg)
		return
	}
	f.Errors[name] = append(f.Errors[name], msg)
	delete(f.Cleaned, name)
}

// HasErrors reports whether validation found anything.
func (f *Form) HasErrors() bool {
	return len(f.Errors) > 0 || len(f.NonFieldErrors) > 0
}

// IsValid validates a bound form (once) and reports whether it is clean.
func (f *Form) IsValid() bool {
	if !f.Bound {
		return false
	}
	f.fullClean()
	return !f.HasErrors()
}

// HasChanged reports whether the submitted data differs from the initial
// values - what lets a formset ignore an untouched extra form.
func (f *Form) HasChanged() bool {
	for _, fl := range f.Fields {
		if fl.Disabled {
			continue
		}
		initial := f.Initial[fl.Name]
		if initial == "" {
			initial = fl.Initial
		}
		submitted := f.Value(fl.Name)
		if fl.Widget == Checkbox {
			if (submitted != "") != (initial != "" && initial != "false" && initial != "False") {
				return true
			}
			continue
		}
		if strings.TrimSpace(submitted) != strings.TrimSpace(initial) {
			return true
		}
	}
	return false
}

func (f *Form) fullClean() {
	if f.validated {
		return
	}
	f.validated = true
	f.Errors = map[string][]string{}
	f.NonFieldErrors = nil
	f.Cleaned = map[string]any{}
	if f.EmptyPermitted && !f.HasChanged() {
		return
	}
	for _, fl := range f.Fields {
		raw := f.Value(fl.Name)
		if fl.Disabled {
			raw = f.Initial[fl.Name]
		}
		v, err := fl.clean(raw)
		if err != "" {
			f.Errors[fl.Name] = append(f.Errors[fl.Name], err)
			continue
		}
		f.Cleaned[fl.Name] = v
	}
	if f.Clean != nil {
		f.Clean(f)
	}
}

// Revalidate forces the next IsValid to run again (after the form changed).
func (f *Form) Revalidate() { f.validated = false }

// Str is the cleaned string value of a field.
func (f *Form) Str(name string) string {
	s, _ := f.Cleaned[name].(string)
	return s
}

// IntVal is the cleaned integer value (nil when blank).
func (f *Form) IntVal(name string) *int64 {
	v, _ := f.Cleaned[name].(*int64)
	return v
}

// DateVal is the cleaned date (nil when blank).
func (f *Form) DateVal(name string) *time.Time {
	v, _ := f.Cleaned[name].(*time.Time)
	return v
}

// DecimalVal is the cleaned decimal (nil when blank).
func (f *Form) DecimalVal(name string) *db.Decimal {
	v, _ := f.Cleaned[name].(*db.Decimal)
	return v
}

// BoolVal is the cleaned boolean.
func (f *Form) BoolVal(name string) bool {
	v, _ := f.Cleaned[name].(bool)
	return v
}

// --- cleaning ---------------------------------------------------------------

const msgRequired = "This field is required."

func (fl *Field) clean(raw string) (any, string) {
	switch fl.Kind {
	case Bool:
		on := raw != "" && raw != "false" && raw != "False" && raw != "0"
		if fl.Required && !on {
			return false, msgRequired
		}
		return on, ""
	case Int:
		s := strings.TrimSpace(raw)
		if s == "" {
			if fl.Required {
				return (*int64)(nil), msgRequired
			}
			return (*int64)(nil), ""
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			// Django also takes "12.0".
			if fv, ferr := strconv.ParseFloat(s, 64); ferr == nil && fv == float64(int64(fv)) {
				n = int64(fv)
			} else {
				return (*int64)(nil), "Enter a whole number."
			}
		}
		if fl.Min != nil && n < *fl.Min {
			return (*int64)(nil), fmt.Sprintf("Ensure this value is greater than or equal to %d.", *fl.Min)
		}
		if fl.Max != nil && n > *fl.Max {
			return (*int64)(nil), fmt.Sprintf("Ensure this value is less than or equal to %d.", *fl.Max)
		}
		return &n, ""
	case Decimal:
		s := strings.TrimSpace(raw)
		if s == "" {
			if fl.Required {
				return (*db.Decimal)(nil), msgRequired
			}
			return (*db.Decimal)(nil), ""
		}
		d, err := db.ParseDecimal(s)
		if err != nil || strings.ContainsAny(strings.ToLower(s), "e") && false {
			return (*db.Decimal)(nil), "Enter a number."
		}
		if msg := checkDecimal(s, fl.MaxDigits, fl.DecimalPlaces); msg != "" {
			return (*db.Decimal)(nil), msg
		}
		return &d, ""
	case Date:
		s := strings.TrimSpace(raw)
		if s == "" {
			if fl.Required {
				return (*time.Time)(nil), msgRequired
			}
			return (*time.Time)(nil), ""
		}
		for _, layout := range []string{"2006-01-02", "01/02/2006", "01/02/06"} {
			if t, err := time.Parse(layout, s); err == nil {
				return &t, ""
			}
		}
		return (*time.Time)(nil), "Enter a valid date."
	case Choice:
		s := strings.TrimSpace(raw)
		if s == "" {
			if fl.Required {
				return "", msgRequired
			}
			return "", ""
		}
		for _, c := range fl.Choices {
			if c.Value == s || c.Value == raw {
				return c.Value, ""
			}
		}
		return "", fmt.Sprintf("Select a valid choice. %s is not one of the available choices.", s)
	case Email:
		s := strings.TrimSpace(raw)
		if s == "" {
			if fl.Required {
				return "", msgRequired
			}
			return "", ""
		}
		if !validEmail(s) {
			return "", "Enter a valid email address."
		}
		if fl.MaxLength > 0 && utf8.RuneCountInString(s) > fl.MaxLength {
			return "", fmt.Sprintf("Ensure this value has at most %d characters (it has %d).", fl.MaxLength, utf8.RuneCountInString(s))
		}
		return s, ""
	default: // Char
		s := raw
		if fl.Widget != PasswordInput {
			s = strings.TrimSpace(raw)
		}
		if s == "" {
			if fl.Required {
				return "", msgRequired
			}
			return "", ""
		}
		if fl.MaxLength > 0 && utf8.RuneCountInString(s) > fl.MaxLength {
			return "", fmt.Sprintf("Ensure this value has at most %d characters (it has %d).", fl.MaxLength, utf8.RuneCountInString(s))
		}
		return s, ""
	}
}

func checkDecimal(s string, maxDigits, places int) string {
	s = strings.TrimPrefix(strings.TrimPrefix(s, "+"), "-")
	whole, frac, _ := strings.Cut(s, ".")
	frac = strings.TrimRight(frac, "0")
	whole = strings.TrimLeft(whole, "0")
	digits := len(whole) + len(frac)
	if maxDigits > 0 && digits > maxDigits {
		return fmt.Sprintf("Ensure that there are no more than %d digits in total.", maxDigits)
	}
	if places >= 0 && len(frac) > places {
		if places == 1 {
			return "Ensure that there are no more than 1 decimal place."
		}
		return fmt.Sprintf("Ensure that there are no more than %d decimal places.", places)
	}
	if maxDigits > 0 && len(whole) > maxDigits-places {
		return fmt.Sprintf("Ensure that there are no more than %d digits before the decimal point.", maxDigits-places)
	}
	return ""
}

func validEmail(s string) bool {
	at := strings.LastIndex(s, "@")
	if at < 1 || at == len(s)-1 || strings.ContainsAny(s, " \t\n,;") {
		return false
	}
	domain := s[at+1:]
	return strings.Contains(domain, ".") && !strings.HasPrefix(domain, ".") && !strings.HasSuffix(domain, ".") &&
		!strings.Contains(domain, "..")
}

// --- rendering ----------------------------------------------------------------

func esc(s string) string { return html.EscapeString(s) }

// Widget HTML, matching Django's output closely.
func (f *Form) widgetHTML(fl *Field) string {
	name := f.AddPrefix(fl.Name)
	id := f.IDFor(fl.Name)
	value := f.Value(fl.Name)
	attrs := map[string]string{}
	for k, v := range fl.Attrs {
		attrs[k] = v
	}
	var b strings.Builder
	class := attrs["class"]
	delete(attrs, "class")
	writeAttrs := func() {
		keys := make([]string, 0, len(attrs))
		for k := range attrs {
			keys = append(keys, k)
		}
		sortStrings(keys)
		for _, k := range keys {
			if attrs[k] == "" && (k == "required" || k == "disabled" || k == "checked" || k == "selected") {
				b.WriteString(" " + k)
				continue
			}
			b.WriteString(fmt.Sprintf(` %s="%s"`, k, esc(attrs[k])))
		}
	}
	if fl.Disabled {
		attrs["disabled"] = ""
	}
	switch fl.Widget {
	case Select:
		if fl.Required && !fl.Disabled {
			attrs["required"] = ""
		}
		attrs["id"] = id
		b.WriteString(fmt.Sprintf(`<select name="%s"`, esc(name)))
		if class != "" {
			b.WriteString(fmt.Sprintf(` class="%s"`, esc(class)))
		}
		writeAttrs()
		b.WriteString(">\n")
		for _, c := range fl.Choices {
			sel := ""
			if c.Value == value || (value == "" && c.Value == "" && false) {
				sel = " selected"
			}
			b.WriteString(fmt.Sprintf(`  <option value="%s"%s>%s</option>`+"\n", esc(c.Value), sel, esc(c.Label)))
		}
		b.WriteString("</select>")
	case Textarea:
		if fl.Required && !fl.Disabled {
			attrs["required"] = ""
		}
		attrs["id"] = id
		rows := fl.Rows
		if rows == 0 {
			rows = 10
		}
		attrs["cols"] = "40"
		attrs["rows"] = strconv.Itoa(rows)
		if fl.MaxLength > 0 {
			attrs["maxlength"] = strconv.Itoa(fl.MaxLength)
		}
		b.WriteString(fmt.Sprintf(`<textarea name="%s"`, esc(name)))
		if class != "" {
			b.WriteString(fmt.Sprintf(` class="%s"`, esc(class)))
		}
		writeAttrs()
		b.WriteString(">\n" + esc(value) + "</textarea>")
	case Checkbox:
		if value != "" && value != "false" && value != "False" {
			attrs["checked"] = ""
		}
		if fl.Required && !fl.Disabled {
			attrs["required"] = ""
		}
		attrs["id"] = id
		b.WriteString(fmt.Sprintf(`<input type="checkbox" name="%s"`, esc(name)))
		if class != "" {
			b.WriteString(fmt.Sprintf(` class="%s"`, esc(class)))
		}
		writeAttrs()
		b.WriteString(">")
	default:
		typ := "text"
		switch fl.Widget {
		case NumberInput:
			typ = "number"
			if fl.Kind == Int {
				if fl.Min != nil {
					attrs["min"] = strconv.FormatInt(*fl.Min, 10)
				}
				if fl.Max != nil {
					attrs["max"] = strconv.FormatInt(*fl.Max, 10)
				}
			}
			if fl.Kind == Decimal {
				attrs["step"] = decimalStep(fl.DecimalPlaces)
			}
		case DateInput:
			typ = "date"
		case PasswordInput:
			typ = "password"
			if !fl.RenderValue {
				value = ""
			}
		case HiddenInput:
			typ = "hidden"
		}
		if fl.Kind == Email {
			typ = map[bool]string{true: typ, false: "email"}[fl.Widget == PasswordInput || fl.Widget == HiddenInput]
		}
		if fl.Required && !fl.Disabled && fl.Widget != HiddenInput {
			attrs["required"] = ""
		}
		if fl.MaxLength > 0 {
			attrs["maxlength"] = strconv.Itoa(fl.MaxLength)
		}
		attrs["id"] = id
		b.WriteString(fmt.Sprintf(`<input type="%s" name="%s"`, typ, esc(name)))
		if value != "" || fl.Widget == HiddenInput {
			b.WriteString(fmt.Sprintf(` value="%s"`, esc(value)))
		}
		if class != "" {
			b.WriteString(fmt.Sprintf(` class="%s"`, esc(class)))
		}
		writeAttrs()
		b.WriteString(">")
	}
	return b.String()
}

func decimalStep(places int) string {
	if places <= 0 {
		return "1"
	}
	return "0." + strings.Repeat("0", places-1) + "1"
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// Bound is a field ready for a template: the Django BoundField attributes the
// templates read, plus the widget HTML.
func (f *Form) BoundField(fl *Field) map[string]any {
	var errs []string
	if f.Bound {
		f.fullClean()
		errs = f.Errors[fl.Name]
	}
	wt := map[Widget]string{TextInput: "text", Textarea: "textarea", Select: "select", Checkbox: "checkbox",
		NumberInput: "number", DateInput: "date", PasswordInput: "password", HiddenInput: "hidden"}[fl.Widget]
	return map[string]any{
		"name":         fl.Name,
		"label":        fl.Label,
		"html":         f.widgetHTML(fl),
		"errors":       errs,
		"help_text":    fl.HelpText,
		"id_for_label": f.IDFor(fl.Name),
		"widget_type":  wt,
		"value":        f.Value(fl.Name),
		"is_hidden":    fl.Widget == HiddenInput,
		"required":     fl.Required,
		"field":        map[string]any{"required": fl.Required},
	}
}

// Template converts the form to the map the _form_fields.html template reads.
func (f *Form) Template() map[string]any {
	if f.Bound {
		f.fullClean()
	}
	var visible, hidden []map[string]any
	byName := map[string]any{}
	for _, fl := range f.Fields {
		bf := f.BoundField(fl)
		byName[fl.Name] = bf
		if fl.Widget == HiddenInput {
			hidden = append(hidden, bf)
		} else {
			visible = append(visible, bf)
		}
	}
	for _, h := range f.hiddenExtra {
		hidden = append(hidden, map[string]any{"html": fmt.Sprintf(`<input type="hidden" name="%s" value="%s" id="id_%s">`, esc(h.name), esc(h.value), esc(h.name))})
	}
	out := map[string]any{
		"visible_fields":    visible,
		"hidden_fields":     hidden,
		"non_field_errors":  f.NonFieldErrors,
		"FULL_WIDTH_FIELDS": f.FullWidth,
		"prefix":            f.Prefix,
		"fields":            byName,
	}
	for k, v := range byName {
		out[k] = v
	}
	return out
}

// AddHidden attaches an extra hidden input (an inline formset's id / fk).
func (f *Form) AddHidden(name, value string) {
	f.hiddenExtra = append(f.hiddenExtra, hiddenField{name: name, value: value})
}

// Helpers to build common fields.

// CharField builds a text input.
func CharField(name, label string, required bool, maxLength int) *Field {
	return &Field{Name: name, Label: label, Kind: Char, Widget: TextInput, Required: required, MaxLength: maxLength}
}

// ChoiceField builds a select.
func ChoiceField(name, label string, required bool, choices []ChoiceOpt) *Field {
	return &Field{Name: name, Label: label, Kind: Choice, Widget: Select, Required: required, Choices: choices}
}

// IntField builds a number input.
func IntField(name, label string, required bool, min, max int64) *Field {
	return &Field{Name: name, Label: label, Kind: Int, Widget: NumberInput, Required: required, Min: &min, Max: &max}
}

// BoolField builds a checkbox.
func BoolField(name, label string) *Field {
	return &Field{Name: name, Label: label, Kind: Bool, Widget: Checkbox}
}

// WithBlank prepends a blank option.
func WithBlank(label string, choices []ChoiceOpt) []ChoiceOpt {
	return append([]ChoiceOpt{{"", label}}, choices...)
}

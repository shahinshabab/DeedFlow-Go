// Package deedforms declares the deed wizard's forms: the Go counterpart of
// deeds/forms.py, with Django's ModelForm field semantics for each model.
package deedforms

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/shahinshabab/deedflow-go/internal/deeds"
	"github.com/shahinshabab/deedflow-go/internal/forms"
)

// label is Django's default label: the column name with spaces, capitalised.
func label(name string) string {
	s := strings.ReplaceAll(name, "_", " ")
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}

func opts(cs []deeds.Choice) []forms.ChoiceOpt {
	out := make([]forms.ChoiceOpt, len(cs))
	for i, c := range cs {
		out[i] = forms.ChoiceOpt{Value: c.Value, Label: c.Label}
	}
	return out
}

func withBlank(blank string, cs []forms.ChoiceOpt) []forms.ChoiceOpt {
	return append([]forms.ChoiceOpt{{Value: "", Label: blank}}, cs...)
}

const dashBlank = "---------"

// charField is a model CharField: required unless blank, with maxlength.
func charField(name string, maxLen int, blank bool) *forms.Field {
	return &forms.Field{Name: name, Label: label(name), Kind: forms.Char, Widget: forms.TextInput, Required: !blank, MaxLength: maxLen}
}

// textField is a model TextField (Textarea, 10 rows unless set).
func textField(name string, blank bool, rows int) *forms.Field {
	return &forms.Field{Name: name, Label: label(name), Kind: forms.Char, Widget: forms.Textarea, Required: !blank, Rows: rows}
}

// textInputField is a TextField drawn as a single-line input.
func textInputField(name string, blank bool) *forms.Field {
	return &forms.Field{Name: name, Label: label(name), Kind: forms.Char, Widget: forms.TextInput, Required: !blank}
}

// choiceChar is a CharField with choices; hasDefault suppresses the blank
// option unless blank=True (Django's include_blank rule).
func choiceChar(name string, blank, hasDefault bool, choices []deeds.Choice, def string) *forms.Field {
	cs := opts(choices)
	if blank || !hasDefault {
		cs = withBlank(dashBlank, cs)
	}
	return &forms.Field{Name: name, Label: label(name), Kind: forms.Choice, Widget: forms.Select, Required: !blank, Choices: cs, Initial: def}
}

func i64(n int64) *int64 { return &n }

// smallUint is PositiveSmallIntegerField (0..32767).
func smallUint(name string, blank bool, initial string) *forms.Field {
	return &forms.Field{Name: name, Label: label(name), Kind: forms.Int, Widget: forms.NumberInput, Required: !blank,
		Min: i64(0), ModelMax: i64(32767), Initial: initial}
}

// uint32f is PositiveIntegerField (0..2147483647).
func uint32f(name string, blank bool) *forms.Field {
	return &forms.Field{Name: name, Label: label(name), Kind: forms.Int, Widget: forms.NumberInput, Required: !blank,
		Min: i64(0), ModelMax: i64(2147483647)}
}

// decimalField is DecimalField(max_digits=10, decimal_places=4, null, blank).
func decimalField(name string) *forms.Field {
	return &forms.Field{Name: name, Label: label(name), Kind: forms.Decimal, Widget: forms.NumberInput, MaxDigits: 10, DecimalPlaces: 4}
}

// dateField is a DateField drawn as <input type="date">.
func dateField(name string, blank bool) *forms.Field {
	return &forms.Field{Name: name, Label: label(name), Kind: forms.Date, Widget: forms.DateInput, Required: !blank}
}

// plainChoice is a declared ChoiceField.
func plainChoice(name, lbl string, required bool, choices []forms.ChoiceOpt) *forms.Field {
	return &forms.Field{Name: name, Label: lbl, Kind: forms.Choice, Widget: forms.Select, Required: required, Choices: choices}
}

// bootstrap adds Bootstrap's classes to every field (BootstrapModelForm).
func bootstrap(f *forms.Form) {
	for _, fl := range f.Fields {
		css := "form-control"
		switch fl.Widget {
		case forms.Checkbox:
			css = "form-check-input"
		case forms.Select:
			css = "form-select"
		}
		if fl.Attrs == nil {
			fl.Attrs = map[string]string{}
		}
		fl.Attrs["class"] = strings.TrimSpace(fl.Attrs["class"] + " " + css)
	}
}

func districtChoices() []forms.ChoiceOpt {
	out := []forms.ChoiceOpt{{Value: "", Label: "---"}}
	for _, d := range deeds.KeralaDistricts {
		out = append(out, forms.ChoiceOpt{Value: d.Name, Label: d.Name})
	}
	return out
}

func allSROChoices() []forms.ChoiceOpt {
	return append([]forms.ChoiceOpt{{Value: "", Label: "---"}}, uniq(func(d deeds.KeralaDistrict) []string { return d.SROs })...)
}

func allTalukChoices() []forms.ChoiceOpt {
	return append([]forms.ChoiceOpt{{Value: "", Label: "---"}}, uniq(func(d deeds.KeralaDistrict) []string { return d.Taluks })...)
}

func uniq(pick func(deeds.KeralaDistrict) []string) []forms.ChoiceOpt {
	seen := map[string]bool{}
	var out []forms.ChoiceOpt
	for _, d := range deeds.KeralaDistricts {
		for _, s := range pick(d) {
			if !seen[s] {
				seen[s] = true
				out = append(out, forms.ChoiceOpt{Value: s, Label: s})
			}
		}
	}
	return out
}

// commas formats n with thousands separators (Python's f"{n:,}").
func commas(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := []byte{}
	digits := []byte(strings.TrimLeft(strings.Repeat("0", 0)+itoa(n), ""))
	for i, c := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			s = append(s, ',')
		}
		s = append(s, c)
	}
	if neg {
		return "-" + string(s)
	}
	return string(s)
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

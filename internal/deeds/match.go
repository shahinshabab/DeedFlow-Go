package deeds

import (
	"regexp"
	"strings"

	"github.com/shahinshabab/deedflow-go/internal/fuzzy"
)

// Option is one <option> of a portal dropdown: its value and its text.
type Option struct{ Value, Text string }

var bracketed = regexp.MustCompile(`\(([^()]*)\)\s*$`)
var slashSpace = regexp.MustCompile(`\s*/\s*`)

// coreText is Normalize() minus a trailing "(English)" gloss and any spacing
// around slashes.
func coreText(text string) string {
	t := strings.TrimSpace(bracketed.ReplaceAllString(Normalize(text), ""))
	return slashSpace.ReplaceAllString(t, "/")
}

func glossOf(text string) string {
	if m := bracketed.FindStringSubmatch(Normalize(text)); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// Alias maps one of our own labels for a dropdown option to its value.
type Alias struct{ Label, Code string }

// BestMatch picks the option that best matches text, or false. It tries, in
// order: the same text once normalized, the same text without its "(English)"
// gloss, text as an option value (or via aliases, our own label for a value),
// the same English gloss, and finally the closest spelling (difflib ratio >=
// threshold).
func BestMatch(text string, options []Option, threshold float64, aliases []Alias) (string, bool) {
	text = strings.TrimSpace(text)
	if text == "" || len(options) == 0 {
		return "", false
	}
	wanted := Normalize(text)
	for _, o := range options {
		if Normalize(o.Text) == wanted {
			return o.Text, true
		}
	}
	core := coreText(text)
	for _, o := range options {
		if core != "" && coreText(o.Text) == core {
			return o.Text, true
		}
	}
	code, haveCode := "", false
	for _, a := range aliases {
		if a.Label == text {
			code, haveCode = a.Code, true
			break
		}
	}
	if !haveCode {
		for _, a := range aliases {
			if coreText(a.Label) == core {
				code, haveCode = a.Code, true
				break
			}
		}
	}
	for _, o := range options {
		if o.Value == text || (haveCode && o.Value == code) {
			return o.Text, true
		}
	}
	gloss := glossOf(text)
	if gloss == "" {
		gloss = wanted
	}
	for _, o := range options {
		if g := glossOf(o.Text); g != "" && g == gloss {
			return o.Text, true
		}
	}
	bestRatio, best := 0.0, ""
	found := false
	for _, o := range options {
		r := fuzzy.SequenceRatio(coreText(o.Text), core)
		if r > bestRatio {
			bestRatio, best, found = r, o.Text, true
		}
	}
	if found && bestRatio >= threshold {
		return best, true
	}
	return "", false
}

// TextOptions makes options whose value equals their text.
func TextOptions(texts []string) []Option {
	out := make([]Option, len(texts))
	for i, t := range texts {
		out[i] = Option{t, t}
	}
	return out
}

// PriorDocAliases are our labels for the portal's PR document type codes, in order.
func PriorDocAliases() []Alias {
	var out []Alias
	for _, c := range PriorDocumentTypeChoices {
		if code, ok := PriorDocTypeValues[c.Value]; ok {
			out = append(out, Alias{c.Value, code})
		}
	}
	return out
}

// Words that give a PR document's type away when the AI (or an older deed)
// names it in its own words instead of one of PriorDocumentTypeChoices.
// Checked in this order.
var priorDocKeywords = []struct {
	Value    string
	Keywords []string
}{
	{"വിൽ", []string{"ഒസ്യത്ത്", "മരണപത്രം", "will", "testament"}},
	{"പട്ടയം", []string{"പട്ടയ", "ട്രിബ്യൂണൽ", "ക്രയ", "patta", "tribunal", "purchase certificate"}},
	{"കോടതി ഉത്തരവ്", []string{"കോടതി", "ഉത്തരവ്", "വിധി", "ഡിക്രി", "court", "decree", "order"}},
	{"രജിസ്റ്റർ/ഫയൽ ചെയ്ത", []string{"രജിസ്റ്റ", "ധാരം", "പത്ര", "ഫയൽ", "register", "filed", "deed"}},
}

// MatchPriorDocumentType maps a PR document type written any which way (our
// label, the portal's option text, the English gloss, or a description like
// "രജിസ്റ്റർ ആധാരം") to one of PriorDocumentTypeChoices' values. It is blank
// when it can't tell, unless registeredHint (the document is cited with an
// SRO, book or volume) makes it a registered document.
func MatchPriorDocumentType(text string, registeredHint bool) string {
	var choices []Option
	for _, c := range PriorDocumentTypeChoices {
		choices = append(choices, Option{c.Value, c.Value})
	}
	for _, c := range PriorDocumentTypeChoices {
		choices = append(choices, Option{c.Value, c.Label})
	}
	if match, ok := BestMatch(text, choices, 0.8, nil); ok {
		for _, o := range choices {
			if o.Text == match {
				return o.Value
			}
		}
	}
	words := Normalize(text)
	for _, k := range priorDocKeywords {
		for _, kw := range k.Keywords {
			if strings.Contains(words, Normalize(kw)) {
				return k.Value
			}
		}
	}
	if registeredHint {
		return "രജിസ്റ്റർ/ഫയൽ ചെയ്ത"
	}
	return ""
}

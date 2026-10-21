// Package deeds holds the deed domain: choice lists, reference data, the
// rules the portal enforces, the payload sent to the portal and the pipeline
// that turns an extracted PDF into a deed.
package deeds

// Choice is a (stored value, display label) pair.
type Choice struct{ Value, Label string }

// Values lists the stored values of choices.
func Values(choices []Choice) []string {
	out := make([]string, len(choices))
	for i, c := range choices {
		out[i] = c.Value
	}
	return out
}

// LabelOf returns the label for a value (or the value itself).
func LabelOf(choices []Choice, value string) string {
	for _, c := range choices {
		if c.Value == value {
			return c.Label
		}
	}
	return value
}

// HasValue reports whether value is one of the choices.
func HasValue(choices []Choice, value string) bool {
	for _, c := range choices {
		if c.Value == value {
			return true
		}
	}
	return false
}

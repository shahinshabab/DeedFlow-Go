package portal

import (
	"encoding/json"
	"fmt"
	"html"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/shahinshabab/deedflow-go/internal/deeds"
	"github.com/shahinshabab/deedflow-go/internal/ordjson"
)

// Option is one <option>: [value, text].
type Option [2]string

// A request whose URL looks like it writes something is never replayed, and a
// dropdown whose onchange looks like it does is never picked.
var unsafeRe = regexp.MustCompile(`(?i)save|insert|update|delete|remove|submit|final|confirm|payment`)

// "-- Select --", "Select District", "-ബുക്ക് നമ്പര്‍-" (a field name between dashes) ...
var placeholderTextRe = regexp.MustCompile(`(?i)^[-\s]*(select|choose|തിരഞ്ഞെടു|തെരഞ്ഞെടു|--)|^\s*-[^-].*-\s*$`)

var (
	optionOpenRe  = regexp.MustCompile(`(?i)<option\b([^>]*)>`)
	optionEndRe   = regexp.MustCompile(`(?i)<option\b|</option>|</select>`)
	valueAttrRe   = regexp.MustCompile(`(?i)\bvalue\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	tagRe         = regexp.MustCompile(`<[^>]+>`)
	loginMarkers  = []string{"loginForm", "captchaImage", "captcha_code"}
	jsonStartChar = "[{"
)

func isPlaceholder(value, text string) bool {
	value, text = strings.TrimSpace(value), strings.TrimSpace(text)
	return value == "" || value == "-1" || text == "" || placeholderTextRe.MatchString(text)
}

func realOptions(options []Option) []Option {
	out := []Option{}
	for _, o := range options {
		if !isPlaceholder(o[0], o[1]) {
			out = append(out, Option{o[0], strings.TrimSpace(o[1])})
		}
	}
	return out
}

func optionSet(options []Option) map[[2]string]bool {
	out := map[[2]string]bool{}
	for _, o := range options {
		out[[2]string{strings.TrimSpace(o[0]), deeds.Normalize(o[1])}] = true
	}
	return out
}

func sameOptionSet(a, b []Option) bool {
	x, y := optionSet(a), optionSet(b)
	if len(x) != len(y) {
		return false
	}
	for k := range x {
		if !y[k] {
			return false
		}
	}
	return true
}

// parseHTMLOptions reads <option> markup; nil when there is none.
func parseHTMLOptions(text string) []Option {
	if text == "" || !strings.Contains(strings.ToLower(text), "<option") {
		return nil
	}
	// Markup sent inside a JavaScript string arrives with escaped quotes.
	text = strings.NewReplacer(`\"`, `"`, `\'`, `'`).Replace(text)
	out := []Option{}
	pos := 0
	for pos < len(text) {
		loc := optionOpenRe.FindStringSubmatchIndex(text[pos:])
		if loc == nil {
			break
		}
		attrs := text[pos+loc[2] : pos+loc[3]]
		bodyStart := pos + loc[1]
		bodyEnd := len(text)
		if end := optionEndRe.FindStringIndex(text[bodyStart:]); end != nil {
			bodyEnd = bodyStart + end[0]
		}
		body := text[bodyStart:bodyEnd]
		label := strings.TrimSpace(html.UnescapeString(tagRe.ReplaceAllString(body, "")))
		value := label
		if m := valueAttrRe.FindStringSubmatch(attrs); m != nil {
			for _, g := range m[1:] {
				if g != "" {
					value = html.UnescapeString(g)
					break
				}
			}
			if m[1] == "" && m[2] == "" && m[3] == "" {
				value = ""
			}
		}
		out = append(out, Option{value, label})
		pos = bodyEnd
	}
	return out
}

// pyStr is Python's str() of a decoded JSON scalar.
func pyStr(v any) string {
	switch t := v.(type) {
	case nil:
		return "None"
	case string:
		return t
	case bool:
		if t {
			return "True"
		}
		return "False"
	case float64:
		if t == math.Trunc(t) && math.Abs(t) < 1e15 {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'g', -1, 64)
	}
	return fmt.Sprint(v)
}

// jsonList is a list in a JSON response that could be a dropdown's options.
type jsonList struct {
	path  []string
	kind  string // "dicts", "lists" or "map"
	items any
}

func isScalar(v any) bool {
	switch v.(type) {
	case string, float64, bool:
		return true
	}
	return false
}

func jsonLists(obj any, path []string, out *[]jsonList) {
	switch t := obj.(type) {
	case []any:
		if len(t) > 0 {
			allDicts, allLists := true, true
			for _, x := range t {
				if _, ok := x.(*ordjson.Map); !ok {
					allDicts = false
				}
				if l, ok := x.([]any); !ok || len(l) < 2 {
					allLists = false
				}
			}
			if allDicts {
				*out = append(*out, jsonList{append([]string(nil), path...), "dicts", t})
			} else if allLists {
				*out = append(*out, jsonList{append([]string(nil), path...), "lists", t})
			}
		}
	case *ordjson.Map:
		if t.Len() > 0 {
			all := true
			for _, k := range t.Keys {
				if !isScalar(t.M[k]) {
					all = false
				}
			}
			if all {
				*out = append(*out, jsonList{append([]string(nil), path...), "map", t})
			}
		}
		for _, k := range t.Keys {
			switch t.M[k].(type) {
			case []any, *ordjson.Map:
				jsonLists(t.M[k], append(append([]string(nil), path...), k), out)
			}
		}
	}
}

func jsonAt(obj any, path []string) (any, bool) {
	for _, k := range path {
		m, ok := obj.(*ordjson.Map)
		if !ok {
			return nil, false
		}
		if obj, ok = m.M[k]; !ok {
			return nil, false
		}
	}
	return obj, true
}

// extraction says how to read a lookup's options out of its response.
type extraction struct {
	Kind     string // html, map, dicts, lists
	Path     []string
	ValueKey any // string (dicts) or int (lists)
	TextKey  any
}

func itemField(item any, key any) (any, bool) {
	switch k := key.(type) {
	case string:
		m, ok := item.(*ordjson.Map)
		if !ok {
			return nil, false
		}
		v, ok := m.M[k]
		return v, ok
	case int:
		l, ok := item.([]any)
		if !ok || k >= len(l) {
			return nil, false
		}
		return l[k], true
	}
	return nil, false
}

// extractOptions reads the options out of a lookup response per the learned
// spec; nil when the response isn't in that shape (e.g. the login page after
// the session expired).
func extractOptions(text string, spec extraction) []Option {
	if spec.Kind == "html" {
		return parseHTMLOptions(text)
	}
	data, err := ordjson.Parse([]byte(text))
	if err != nil {
		return nil
	}
	items, ok := jsonAt(data, spec.Path)
	if !ok {
		return nil
	}
	if spec.Kind == "map" {
		m, ok := items.(*ordjson.Map)
		if !ok {
			return nil
		}
		out := []Option{}
		for _, k := range m.Keys {
			out = append(out, Option{k, pyStr(m.M[k])})
		}
		return out
	}
	list, ok := items.([]any)
	if !ok {
		return nil
	}
	out := []Option{}
	for _, item := range list {
		v, ok1 := itemField(item, spec.ValueKey)
		t, ok2 := itemField(item, spec.TextKey)
		if !ok1 || !ok2 {
			return nil
		}
		out = append(out, Option{pyStr(v), pyStr(t)})
	}
	return out
}

// findExtraction is how to read targetOptions out of a response, if they're in it.
func findExtraction(text string, targetOptions []Option) *extraction {
	targetList := realOptions(targetOptions)
	if len(targetList) == 0 || text == "" {
		return nil
	}
	if parsed := parseHTMLOptions(text); parsed != nil {
		if sameOptionSet(realOptions(parsed), targetList) {
			return &extraction{Kind: "html"}
		}
		return nil
	}
	stripped := strings.TrimSpace(text)
	if stripped == "" || !strings.Contains(jsonStartChar, stripped[:1]) {
		return nil
	}
	data, err := ordjson.Parse([]byte(stripped))
	if err != nil {
		return nil
	}
	var lists []jsonList
	jsonLists(data, nil, &lists)
	for _, jl := range lists {
		if jl.kind == "map" {
			spec := extraction{Kind: "map", Path: jl.path}
			if sameOptionSet(realOptions(extractOptions(stripped, spec)), targetList) {
				return &spec
			}
			continue
		}
		items := jl.items.([]any)
		var keys []any
		if jl.kind == "dicts" {
			for _, k := range items[0].(*ordjson.Map).Keys {
				keys = append(keys, k)
			}
		} else {
			least := len(items[0].([]any))
			for _, x := range items {
				if n := len(x.([]any)); n < least {
					least = n
				}
			}
			for i := 0; i < least; i++ {
				keys = append(keys, i)
			}
		}
		for _, vk := range keys {
			for _, tk := range keys {
				spec := extraction{Kind: jl.kind, Path: jl.path, ValueKey: vk, TextKey: tk}
				if got := extractOptions(stripped, spec); got != nil && sameOptionSet(realOptions(got), targetList) {
					return &spec
				}
			}
		}
	}
	return nil
}

// toJSON is a compact JSON rendering (for signatures).
func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

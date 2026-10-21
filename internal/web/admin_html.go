package web

import (
	"fmt"
	"html"
	"strings"

	"github.com/flosch/pongo2/v6"
)

// Small helpers for writing the admin's HTML the way Django's widget
// templates do.

// attr is one HTML attribute; a bool value is written bare when true and not
// at all when false; nil is skipped.
type attr struct {
	k string
	v any
}

func attrsHTML(list []attr) string {
	var b strings.Builder
	for _, a := range list {
		switch v := a.v.(type) {
		case nil:
		case bool:
			if v {
				b.WriteString(" " + a.k)
			}
		case string:
			b.WriteString(" " + a.k + `="` + html.EscapeString(v) + `"`)
		case int:
			b.WriteString(fmt.Sprintf(` %s="%d"`, a.k, v))
		default:
			b.WriteString(` ` + a.k + `="` + html.EscapeString(fmt.Sprint(v)) + `"`)
		}
	}
	return b.String()
}

func esc(s string) string { return html.EscapeString(s) }

// safe marks HTML as not needing escaping when it is a template value.
func safe(s string) *pongo2.Value { return pongo2.AsSafeValue(s) }

// errorList is Django's ErrorList.as_ul.
func errorList(errs []string) string {
	if len(errs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<ul class="errorlist">`)
	for _, e := range errs {
		b.WriteString("<li>" + esc(e) + "</li>")
	}
	b.WriteString("</ul>")
	return b.String()
}

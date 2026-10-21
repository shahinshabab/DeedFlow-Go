package web

import (
	"strings"
)

// Form media: the scripts and stylesheets the widgets on an admin page need,
// merged the way django.forms.Media merges them (a topological sort of the
// lists each piece of media brings).

type adminMediaSet struct {
	js  [][]string
	css map[string][][]string
	// medium order of appearance
	mediums []string
}

func newMedia() *adminMediaSet { return &adminMediaSet{css: map[string][][]string{}} }

func (m *adminMediaSet) addJS(paths ...string) *adminMediaSet {
	if len(paths) > 0 {
		m.js = append(m.js, paths)
	}
	return m
}

func (m *adminMediaSet) addCSS(medium string, paths ...string) *adminMediaSet {
	if _, ok := m.css[medium]; !ok {
		m.mediums = append(m.mediums, medium)
	}
	m.css[medium] = append(m.css[medium], paths)
	return m
}

// merge is Media.merge: a topological order of the lists, falling back to
// first-seen order when they disagree.
func mergeMediaLists(lists [][]string) []string {
	type info struct {
		npred int
		succ  []string
	}
	nodes := map[string]*info{}
	var order []string
	get := func(n string) *info {
		if i, ok := nodes[n]; ok {
			return i
		}
		i := &info{}
		nodes[n] = i
		order = append(order, n)
		return i
	}
	for _, l := range lists {
		if len(l) == 0 {
			continue
		}
		head := l[0]
		get(head)
		for _, item := range l[1:] {
			if head != item {
				get(item).npred++
				get(head).succ = append(get(head).succ, item)
			}
			head = item
		}
	}
	var ready []string
	for _, n := range order {
		if nodes[n].npred == 0 {
			ready = append(ready, n)
		}
	}
	var out []string
	for len(ready) > 0 {
		group := ready
		ready = nil
		out = append(out, group...)
		for _, n := range group {
			for _, s := range nodes[n].succ {
				nodes[s].npred--
				if nodes[s].npred == 0 {
					ready = append(ready, s)
				}
			}
		}
	}
	if len(out) != len(order) {
		// a cycle: first-seen order
		seen := map[string]bool{}
		out = nil
		for _, l := range lists {
			for _, p := range l {
				if !seen[p] {
					seen[p] = true
					out = append(out, p)
				}
			}
		}
	}
	return out
}

func (m *adminMediaSet) combine(o *adminMediaSet) *adminMediaSet {
	if o == nil {
		return m
	}
	m.js = append(m.js, o.js...)
	for _, medium := range o.mediums {
		if _, ok := m.css[medium]; !ok {
			m.mediums = append(m.mediums, medium)
		}
		m.css[medium] = append(m.css[medium], o.css[medium]...)
	}
	return m
}

// html is str(media): the stylesheets, then the scripts.
func (m *adminMediaSet) html() string {
	var lines []string
	for _, medium := range m.mediums {
		for _, p := range mergeMediaLists(m.css[medium]) {
			lines = append(lines, `<link href="`+staticURL+esc(p)+`" media="`+esc(medium)+`" rel="stylesheet">`)
		}
	}
	for _, p := range mergeMediaLists(m.js) {
		lines = append(lines, `<script src="`+staticURL+esc(p)+`"></script>`)
	}
	return strings.Join(lines, "\n")
}

func (a *App) minExt() string {
	if a.Cfg.Debug {
		return ""
	}
	return ".min"
}

// modelAdminMedia is ModelAdmin.media.
func (a *App) modelAdminMedia() *adminMediaSet {
	x := a.minExt()
	m := newMedia()
	m.addJS("admin/js/vendor/jquery/jquery"+x+".js", "admin/js/jquery.init.js", "admin/js/core.js", "admin/js/admin/RelatedObjectLookups.js",
		"admin/js/actions.js", "admin/js/urlify.js", "admin/js/prepopulate.js", "admin/js/vendor/xregexp/xregexp"+x+".js")
	return m
}

// inlineMedia is InlineModelAdmin.media.
func (a *App) inlineMedia(spec *inlineSpec) *adminMediaSet {
	x := a.minExt()
	m := newMedia()
	m.addJS("admin/js/vendor/jquery/jquery"+x+".js", "admin/js/jquery.init.js", "admin/js/inlines.js")
	return m
}

// widgetMedia is the media of one widget.
func (a *App) widgetMedia(w *widgetSpec) *adminMediaSet {
	m := newMedia()
	switch w.Class {
	case "RelatedFieldWidgetWrapper":
		if w.Inner != nil {
			return a.widgetMedia(w.Inner)
		}
	case "AdminDateWidget", "AdminTimeWidget", "AdminSplitDateTime":
		m.addJS("admin/js/calendar.js", "admin/js/admin/DateTimeShortcuts.js")
	case "FilteredSelectMultiple":
		m.addJS("admin/js/core.js", "admin/js/SelectBox.js", "admin/js/SelectFilter2.js")
	case "AutocompleteSelect":
		x := a.minExt()
		m.addJS("admin/js/vendor/jquery/jquery"+x+".js", "admin/js/vendor/select2/select2.full"+x+".js", "admin/js/vendor/select2/i18n/en.js",
			"admin/js/jquery.init.js", "admin/js/autocomplete.js")
		m.addCSS("screen", "admin/css/vendor/select2/select2"+x+".css", "admin/css/autocomplete.css")
	}
	return m
}

// formMedia is Form.media: the widgets of the form's fields, in order.
func (a *App) formMedia(f *aForm) *adminMediaSet {
	m := newMedia()
	for _, n := range f.order {
		if fl := f.fields[n]; fl != nil {
			m.combine(a.widgetMedia(fl.w))
		}
	}
	return m
}

// pageMedia is the media of an add/change page.
func (v *cfView) pageMedia() *adminMediaSet {
	a := v.a
	m := a.modelAdminMedia()
	m.combine(a.formMedia(v.form))
	for _, in := range v.inlines {
		m.combine(a.inlineMedia(in.spec))
		form := in.empty
		if len(in.forms) > 0 {
			form = in.forms[0]
		}
		m.combine(a.formMedia(form))
	}
	return m
}

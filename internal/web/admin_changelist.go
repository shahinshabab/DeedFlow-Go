package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/flosch/pongo2/v6"
)

// The admin's changelist: a port of django.contrib.admin.views.main.ChangeList
// and the admin_list template tags, for the list_display, list_filter and
// search_fields the project's model admins declare.

const (
	allVar    = "all"
	orderVar  = "o"
	pageVar   = "p"
	searchVar = "q"
	errorFlag = "e"
	facetsVar = "_facets"
	popupVar  = "_popup"
	toFieldVr = "_to_field"
)

var ignoredParams = []string{allVar, orderVar, searchVar, facetsVar, popupVar, toFieldVr}

// pairs is an ordered query string: keys in order of first appearance, each
// with all its values (a QueryDict).
type pairs struct {
	keys []string
	vals map[string][]string
}

func parseQueryOrdered(raw string) *pairs {
	p := &pairs{vals: map[string][]string{}}
	for _, part := range strings.Split(raw, "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		k, err1 := url.QueryUnescape(k)
		v, err2 := url.QueryUnescape(v)
		if err1 != nil || err2 != nil {
			continue
		}
		if _, ok := p.vals[k]; !ok {
			p.keys = append(p.keys, k)
		}
		p.vals[k] = append(p.vals[k], v)
	}
	return p
}

func (p *pairs) last(k string) (string, bool) {
	v, ok := p.vals[k]
	if !ok || len(v) == 0 {
		return "", false
	}
	return v[len(v)-1], true
}

// encode is QueryDict.urlencode().
func (p *pairs) encode() string {
	var parts []string
	for _, k := range p.keys {
		for _, v := range p.vals[k] {
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

type ldCol struct {
	name string
	ld   *listDisplaySpec // nil for the action checkbox
}

// clChoice is one entry of a filter.
type clChoice struct {
	selected bool
	query    string
	display  string
}

// listFilter is one of the changelist's filters (a FieldListFilter).
type listFilter struct {
	kind    string // choices, bool, related, allvalues
	f       *modelFieldSpec
	path    string
	title   string
	kwarg   string // the main lookup
	kwarg2  string // the isnull lookup
	used    map[string][]any
	valList []string // params.get(kwarg), all values
	valNull *string  // last value of the isnull parameter
	related []relChoice
	values  []*string
	null    bool
}

type relChoice struct {
	id    string
	label string
}

func (lf *listFilter) expected() []string { return []string{lf.kwarg, lf.kwarg2} }

func (lf *listFilter) hasOutput() bool {
	if lf.kind == "related" {
		extra := 0
		if lf.f.Null || lf.f.isM2M() {
			extra = 1
		}
		return len(lf.related)+extra > 1
	}
	return true
}

func prepareLookupValue(key, value string) any {
	if strings.HasSuffix(key, "__isnull") {
		l := strings.ToLower(value)
		return !(l == "" || l == "false" || l == "0")
	}
	return value
}

func (lf *listFilter) apply(q *clQuery) error {
	for _, p := range lf.expected() {
		vals := lf.used[p]
		if len(vals) == 0 {
			continue
		}
		if err := q.filterAny(p, vals); err != nil {
			return err
		}
	}
	return nil
}

func inList(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// changeList is one changelist request.
type changeList struct {
	a     *App
	r     *adminReq
	m     *modelSpec
	empty string

	cols       []ldCol
	links      []string
	hasActions bool

	query      string
	pageNum    int
	showAll    bool
	isPopup    bool
	toFieldVal string
	addFacets  bool
	get        *pairs
	params     *pairs // dict(request.GET.items()) without p and e
	fparams    map[string][]string
	remaining  map[string][]string
	filters    []*listFilter
	hasActive  bool

	clearAllQS string
	preserved  string
	baseOrder  []string

	resultCount     int
	fullResultCount int
	canShowAll      bool
	multiPage       bool
	numPages        int
	results         []*adminObj
}

// errRedirectE makes the view redirect to ?e=1 like IncorrectLookupParameters.
var errInvalidPage = badLookup("invalid page")

func (a *App) newChangeList(r *adminReq, m *modelSpec, hasActions bool) (*changeList, error) {
	c := r.c
	cl := &changeList{a: a, r: r, m: m, empty: "-", hasActions: hasActions}
	cl.get = parseQueryOrdered(c.R.URL.RawQuery)
	cl.query, _ = cl.get.last(searchVar)
	cl.pageNum = 1
	if v, ok := cl.get.last(pageVar); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			cl.pageNum = n
		}
	}
	_, cl.showAll = cl.get.vals[allVar]
	_, cl.isPopup = cl.get.vals[popupVar]
	cl.toFieldVal, _ = cl.get.last(toFieldVr)
	_, cl.addFacets = cl.get.vals[facetsVar]

	cl.params = &pairs{vals: map[string][]string{}}
	cl.fparams = map[string][]string{}
	for _, k := range cl.get.keys {
		if k == pageVar || k == errorFlag {
			continue
		}
		cl.params.keys = append(cl.params.keys, k)
		last, _ := cl.get.last(k)
		cl.params.vals[k] = []string{last}
		cl.fparams[k] = cl.get.vals[k]
	}

	// list_display, with the action checkbox in front
	if hasActions {
		cl.cols = append(cl.cols, ldCol{name: "action_checkbox"})
	}
	for _, ld := range m.ListDisplay {
		cl.cols = append(cl.cols, ldCol{name: ld.Name, ld: ld})
	}
	cl.links = m.ListDisplayLinks

	// the preserved filters of the changelist's own links
	if raw := cl.get.encode(); raw != "" {
		cl.preserved = encodeQuery([][2]string{{"_changelist_filters", raw}})
	}

	if err := cl.buildFilters(); err != nil {
		return nil, err
	}
	q, err := cl.buildQuery(nil)
	if err != nil {
		return nil, err
	}
	cl.clearAllQS = cl.queryString(cl.remainingNew(), cl.filtersParamKeys())

	// results
	perPage := m.ListPerPage
	var count int
	if err := a.Store.Querier().QueryRow(c.Context(), "SELECT COUNT(*)"+q.fromWhere(), q.args...).Scan(&count); err != nil {
		return nil, err
	}
	cl.resultCount = count
	if err := a.Store.Querier().QueryRow(c.Context(), `SELECT COUNT(*) FROM `+quoteIdent(m.Table)).Scan(&cl.fullResultCount); err != nil {
		return nil, err
	}
	cl.numPages = (count + perPage - 1) / perPage
	if cl.numPages < 1 {
		cl.numPages = 1
	}
	cl.canShowAll = count <= m.ListMaxShowAll
	cl.multiPage = count > perPage
	var sqlText string
	if (cl.showAll && cl.canShowAll) || !cl.multiPage {
		sqlText = "SELECT " + m.selectList("t") + q.fromWhere() + q.orderLimit(0, 0)
	} else {
		if cl.pageNum < 1 || cl.pageNum > cl.numPages {
			return nil, errInvalidPage
		}
		sqlText = "SELECT " + m.selectList("t") + q.fromWhere() + q.orderLimit(perPage, (cl.pageNum-1)*perPage)
	}
	rows, err := a.Store.Querier().Query(c.Context(), sqlText, q.args...)
	if err != nil {
		return nil, err
	}
	cl.results, err = a.scanObjs(m, rows)
	return cl, err
}

func encodeQuery(p [][2]string) string {
	var parts []string
	for _, kv := range p {
		parts = append(parts, url.QueryEscape(kv[0])+"="+url.QueryEscape(kv[1]))
	}
	return strings.Join(parts, "&")
}

// lookupAllowed is ModelAdmin.lookup_allowed.
func (cl *changeList) lookupAllowed(lookup string) bool {
	m := cl.m
	var relationParts []string
	var prev *modelFieldSpec
	cur := m
	for _, part := range strings.Split(lookup, "__") {
		f := cur.field(part)
		if f == nil {
			break
		}
		isPK := part == cur.PK
		toField := prev != nil && prev.Related != nil && part == prev.Related.ToField
		if prev == nil || (prev.Related != nil && !isPK && !toField) {
			relationParts = append(relationParts, part)
		}
		if f.Related == nil {
			break
		}
		prev = f
		if rel := adminSiteSpec.model(f.Related.Label); rel != nil {
			cur = rel
		} else {
			break
		}
	}
	if len(relationParts) <= 1 {
		return true
	}
	valid := map[string]bool{}
	for _, n := range cl.m.ListFilterNames {
		valid[n] = true
	}
	return valid[strings.Join(relationParts, "__")] || valid[lookup]
}

func (cl *changeList) filtersParamKeys() []string {
	var keys []string
	for _, k := range cl.params.keys {
		if !inList(ignoredParams, k) {
			keys = append(keys, k)
		}
	}
	return keys
}

// remainingNew is the lookup parameters no filter took.
func (cl *changeList) remainingNew() [][2]string {
	var out [][2]string
	for _, k := range cl.filtersParamKeys() {
		if vals, ok := cl.remaining[k]; ok {
			out = append(out, [2]string{k, vals[len(vals)-1]})
		}
	}
	return out
}

func (cl *changeList) buildFilters() error {
	lookup := map[string][]string{}
	var lkeys []string
	for _, k := range cl.get.keys {
		if k == pageVar || k == errorFlag || inList(ignoredParams, k) {
			continue
		}
		lookup[k] = cl.get.vals[k]
		lkeys = append(lkeys, k)
	}
	for _, k := range lkeys {
		for range lookup[k] {
			if !cl.lookupAllowed(k) {
				return &errDisallowed{"Filtering by " + k + " not allowed"}
			}
		}
	}
	ctx := cl.r.c.Context()
	for _, name := range cl.m.ListFilterNames {
		f := cl.m.field(name)
		if f == nil {
			return fmt.Errorf("list_filter %q is not a field", name)
		}
		before := len(lookup)
		lf := &listFilter{f: f, path: name, title: f.VerboseName, used: map[string][]any{}, null: f.Null}
		switch {
		case f.Related != nil:
			lf.kind = "related"
			lf.kwarg = name + "__" + f.Related.ToField + "__exact"
			lf.kwarg2 = name + "__isnull"
		case f.InternalType == "BooleanField":
			lf.kind = "bool"
			lf.kwarg = name + "__exact"
			lf.kwarg2 = name + "__isnull"
		case len(f.Choices) > 0:
			lf.kind = "choices"
			lf.kwarg = name + "__exact"
			lf.kwarg2 = name + "__isnull"
		default:
			lf.kind = "allvalues"
			lf.kwarg = name
			lf.kwarg2 = name + "__isnull"
		}
		lf.valList = lookup[lf.kwarg]
		if v := lookup[lf.kwarg2]; len(v) > 0 {
			s := v[len(v)-1]
			lf.valNull = &s
		}
		for _, p := range lf.expected() {
			if vals, ok := lookup[p]; ok {
				delete(lookup, p)
				for _, v := range vals {
					lf.used[p] = append(lf.used[p], prepareLookupValue(p, v))
				}
			}
		}
		switch lf.kind {
		case "related":
			rel := adminSiteSpec.model(f.Related.Label)
			if f.Related.Label == "auth.group" || rel != nil {
				order := ""
				if rm := adminSiteSpec.models[f.Related.Label]; rm != nil && len(rm.AdminOrdering) > 0 {
					var parts []string
					for _, o := range rm.AdminOrdering {
						col := `"` + rel.field(strings.TrimPrefix(o, "-")).Column + `"`
						if strings.HasPrefix(o, "-") {
							col += " DESC"
						}
						parts = append(parts, col)
					}
					order = " ORDER BY " + strings.Join(parts, ", ")
				}
				rows, err := cl.a.Store.Querier().Query(ctx, "SELECT "+rel.selectList("t")+" FROM "+quoteIdent(rel.Table)+" t"+order)
				if err != nil {
					return err
				}
				objs, err := cl.a.scanObjs(rel, rows)
				if err != nil {
					return err
				}
				re := cl.a.newRepr(ctx)
				for _, o := range objs {
					lf.related = append(lf.related, relChoice{id: o.pkString(), label: re.str(o)})
				}
			}
		case "allvalues":
			rows, err := cl.a.Store.Querier().Query(ctx, fmt.Sprintf(`SELECT v::text FROM (SELECT DISTINCT t."%s" AS v FROM %s t) s ORDER BY v`,
				f.Column, quoteIdent(cl.m.Table)))
			if err != nil {
				return err
			}
			for rows.Next() {
				var s *string
				if err := rows.Scan(&s); err != nil {
					rows.Close()
					return err
				}
				lf.values = append(lf.values, s)
			}
			rows.Close()
		}
		if lf.hasOutput() {
			cl.filters = append(cl.filters, lf)
			if before > len(lookup) {
				cl.hasActive = true
			}
		}
	}
	// what is left must be real lookups
	cl.remaining = lookup
	for _, k := range lkeys {
		if _, ok := lookup[k]; !ok {
			continue
		}
		q := newQuery(cl.m)
		if _, err := q.resolve(k); err != nil {
			return err
		}
	}
	return nil
}

// buildQuery is ChangeList.get_queryset: the filters, the remaining lookups,
// the ordering and the search; exclude names the filter to leave out.
func (cl *changeList) buildQuery(exclude *listFilter) (*clQuery, error) {
	q := newQuery(cl.m)
	for _, lf := range cl.filters {
		if lf == exclude {
			continue
		}
		if err := lf.apply(q); err != nil {
			return nil, err
		}
	}
	for _, k := range cl.filtersParamKeys() {
		vals, ok := cl.remaining[k]
		if !ok {
			continue
		}
		var anys []any
		for _, v := range vals {
			anys = append(anys, prepareLookupValue(k, v))
		}
		if err := q.filterAny(k, anys); err != nil {
			return nil, err
		}
	}
	q.setOrdering(cl.ordering())
	if err := q.search(cl.m.SearchFields, cl.query); err != nil {
		return nil, err
	}
	return q, nil
}

// defaultOrdering is _get_default_ordering.
func (cl *changeList) defaultOrdering() []string {
	if len(cl.m.AdminOrdering) > 0 {
		return cl.m.AdminOrdering
	}
	return cl.m.Ordering
}

// orderingField is get_ordering_field: the model field a column sorts by.
func (cl *changeList) orderingField(name string) string {
	if f := cl.m.field(name); f != nil {
		return f.Name
	}
	for _, ld := range cl.m.ListDisplay {
		if ld.Name == name && ld.AdminOrderField != nil {
			return *ld.AdminOrderField
		}
	}
	return ""
}

// ordering is ChangeList.get_ordering.
func (cl *changeList) ordering() []string {
	ordering := append([]string(nil), cl.defaultOrdering()...)
	if o, ok := cl.params.last(orderVar); ok {
		ordering = nil
		for _, p := range strings.Split(o, ".") {
			i := strings.LastIndex(p, "-")
			pfx, idxs := "", p
			if i >= 0 {
				pfx, idxs = "-", p[i+1:]
			}
			idx, err := strconv.Atoi(idxs)
			if err != nil || idx < 0 || idx >= len(cl.cols) {
				continue
			}
			col := cl.cols[idx]
			of := cl.orderingField(col.name)
			if of == "" {
				continue
			}
			if pfx == "-" && strings.HasPrefix(of, "-") {
				ordering = append(ordering, strings.TrimPrefix(of, "-"))
			} else {
				ordering = append(ordering, pfx+of)
			}
		}
	}
	return cl.deterministicOrdering(ordering)
}

func (cl *changeList) deterministicOrdering(ordering []string) []string {
	m := cl.m
	total := map[string]bool{"pk": true}
	for _, f := range m.ModelFields {
		if f.Unique && !f.Null {
			total[f.Attname] = true
		}
	}
	seen := map[string]bool{}
	broke := false
	for _, part := range ordering {
		name := strings.TrimLeft(part, "-")
		f := m.field(name)
		if f == nil {
			for _, mf := range m.ModelFields {
				if mf.Attname == name {
					f = mf
				}
			}
		}
		if f == nil {
			continue
		}
		if f.Related != nil && name == f.Name {
			continue
		}
		if total[f.Attname] {
			broke = true
			break
		}
		seen[f.Attname] = true
	}
	if !broke {
		var sets [][]string
		sets = append(sets, m.UniqueTogether...)
		for _, uc := range m.UniqueConstraints {
			if uc.Condition == nil {
				sets = append(sets, uc.Fields)
			}
		}
		for _, names := range sets {
			ok, nullable := true, false
			for _, n := range names {
				f := m.field(n)
				if f == nil || f.Null {
					nullable = true
					break
				}
				if !seen[f.Attname] {
					ok = false
				}
			}
			if nullable {
				continue
			}
			if ok {
				broke = true
				break
			}
		}
	}
	if !broke {
		ordering = append(ordering, "-pk")
	}
	return ordering
}

// orderingFieldColumns is get_ordering_field_columns: column number -> asc/desc,
// in the order they apply.
func (cl *changeList) orderingFieldColumns() ([]int, map[int]string) {
	var idxs []int
	out := map[int]string{}
	if o, ok := cl.params.last(orderVar); !ok {
		for _, field := range cl.defaultOrdering() {
			typ := "asc"
			if strings.HasPrefix(field, "-") {
				field, typ = field[1:], "desc"
			}
			for i, col := range cl.cols {
				if cl.orderingField(col.name) == field {
					if _, dup := out[i]; !dup {
						idxs = append(idxs, i)
					}
					out[i] = typ
					break
				}
			}
		}
	} else {
		for _, p := range strings.Split(o, ".") {
			i := strings.LastIndex(p, "-")
			pfx, idxs0 := "", p
			if i >= 0 {
				pfx, idxs0 = "-", p[i+1:]
			}
			idx, err := strconv.Atoi(idxs0)
			if err != nil {
				continue
			}
			typ := "asc"
			if pfx == "-" {
				typ = "desc"
			}
			if _, dup := out[idx]; !dup {
				idxs = append(idxs, idx)
			}
			out[idx] = typ
		}
	}
	return idxs, out
}

// queryString is ChangeList.get_query_string. A nil value in set removes the key.
func (cl *changeList) queryString(set [][2]string, remove []string) string {
	p := map[string][]string{}
	for k, v := range cl.fparams {
		p[k] = v
	}
	for _, r := range remove {
		for k := range p {
			if strings.HasPrefix(k, r) {
				delete(p, k)
			}
		}
	}
	for _, kv := range set {
		p[kv[0]] = []string{kv[1]}
	}
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		for _, v := range p[k] {
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	return "?" + strings.Join(parts, "&")
}

func (cl *changeList) removeKeys(set [][2]string, remove []string, drop ...string) string {
	// set entries with a nil-like drop are handled by listing them in drop
	saved := map[string][]string{}
	for _, d := range drop {
		saved[d] = cl.fparams[d]
		delete(cl.fparams, d)
	}
	s := cl.queryString(set, remove)
	for k, v := range saved {
		if v != nil {
			cl.fparams[k] = v
		}
	}
	return s
}

// --- filter choices --------------------------------------------------------------------------

func (cl *changeList) facetCount(lf *listFilter, path string, value any) (int, error) {
	q, err := cl.buildQuery(lf)
	if err != nil {
		return 0, err
	}
	c, err := q.cond(path, value)
	if err != nil {
		return 0, err
	}
	q.where = append(q.where, c)
	var n int
	err = cl.a.Store.Querier().QueryRow(cl.r.c.Context(), "SELECT COUNT(*)"+q.fromWhere(), q.args...).Scan(&n)
	return n, err
}

func (cl *changeList) filterChoices(lf *listFilter) ([]clChoice, error) {
	var out []clChoice
	facets := cl.addFacets
	title := func(base string, path string, value any) (string, error) {
		if !facets {
			return base, nil
		}
		n, err := cl.facetCount(lf, path, value)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s (%d)", base, n), nil
	}
	allQS := cl.queryString(nil, []string{lf.kwarg, lf.kwarg2})
	switch lf.kind {
	case "choices":
		out = append(out, clChoice{selected: len(lf.valList) == 0, query: allQS, display: "All"})
		for _, c := range lf.f.Choices {
			t, err := title(c[1], lf.kwarg, c[0])
			if err != nil {
				return nil, err
			}
			out = append(out, clChoice{
				selected: len(lf.valList) > 0 && inList(lf.valList, c[0]),
				query:    cl.queryString([][2]string{{lf.kwarg, c[0]}}, []string{lf.kwarg2}),
				display:  t,
			})
		}
	case "bool":
		isTrue := func(s *string) bool { return s != nil }
		lookupVal := ""
		hasVal := len(lf.valList) > 0
		if hasVal {
			lookupVal = lf.valList[len(lf.valList)-1]
		}
		valNull2 := lf.valNull != nil && *lf.valNull != ""
		_ = isTrue
		for _, c := range []struct {
			lookup *string
			title  string
			value  any
		}{{nil, "All", nil}, {ptr("1"), "Yes", true}, {ptr("0"), "No", false}} {
			t := c.title
			if facets && c.lookup != nil {
				n, err := cl.facetCount(lf, lf.path, c.value)
				if err != nil {
					return nil, err
				}
				t = fmt.Sprintf("%s (%d)", t, n)
			}
			sel := false
			if c.lookup == nil {
				sel = !hasVal && !valNull2
			} else {
				sel = hasVal && lookupVal == *c.lookup && !valNull2
			}
			var set [][2]string
			var q string
			if c.lookup == nil {
				q = cl.removeKeys(nil, []string{lf.kwarg2}, lf.kwarg)
			} else {
				set = [][2]string{{lf.kwarg, *c.lookup}}
				q = cl.queryString(set, []string{lf.kwarg2})
			}
			out = append(out, clChoice{selected: sel, query: q, display: t})
		}
		if lf.f.Null {
			t := "Unknown"
			if facets {
				n, err := cl.facetCount(lf, lf.kwarg2, true)
				if err != nil {
					return nil, err
				}
				t = fmt.Sprintf("%s (%d)", t, n)
			}
			out = append(out, clChoice{
				selected: lf.valNull != nil && *lf.valNull == "True",
				query:    cl.queryString([][2]string{{lf.kwarg2, "True"}}, []string{lf.kwarg}),
				display:  t,
			})
		}
	case "related":
		out = append(out, clChoice{selected: len(lf.valList) == 0 && !(lf.valNull != nil && *lf.valNull != ""), query: allQS, display: "All"})
		for _, rc := range lf.related {
			t := rc.label
			if facets {
				n, err := cl.facetCount(lf, lf.kwarg, rc.id)
				if err != nil {
					return nil, err
				}
				t = fmt.Sprintf("%s (%d)", t, n)
			}
			out = append(out, clChoice{
				selected: len(lf.valList) > 0 && containsStr(lf.valList, rc.id),
				query:    cl.queryString([][2]string{{lf.kwarg, rc.id}}, []string{lf.kwarg2}),
				display:  t,
			})
		}
		if lf.f.Null || lf.f.isM2M() {
			t := cl.empty
			if facets {
				n, err := cl.facetCount(lf, lf.kwarg2, true)
				if err != nil {
					return nil, err
				}
				t = fmt.Sprintf("%s (%d)", t, n)
			}
			out = append(out, clChoice{
				selected: lf.valNull != nil && *lf.valNull != "",
				query:    cl.queryString([][2]string{{lf.kwarg2, "True"}}, []string{lf.kwarg}),
				display:  t,
			})
		}
	case "allvalues":
		out = append(out, clChoice{selected: len(lf.valList) == 0 && lf.valNull == nil, query: allQS, display: "All"})
		includeNone := false
		emptyTitle := cl.empty
		for _, v := range lf.values {
			count := ""
			if facets {
				var value any
				path := lf.kwarg
				if v == nil {
					path, value = lf.kwarg2, true
				} else {
					value = *v
				}
				n, err := cl.facetCount(lf, path, value)
				if err != nil {
					return nil, err
				}
				count = fmt.Sprintf(" (%d)", n)
			}
			if v == nil {
				includeNone = true
				emptyTitle += count
				continue
			}
			out = append(out, clChoice{
				selected: len(lf.valList) > 0 && containsStr(lf.valList, *v),
				query:    cl.queryString([][2]string{{lf.kwarg, *v}}, []string{lf.kwarg2}),
				display:  *v + count,
			})
		}
		if includeNone {
			out = append(out, clChoice{
				selected: lf.valNull != nil && *lf.valNull != "",
				query:    cl.queryString([][2]string{{lf.kwarg2, "True"}}, []string{lf.kwarg}),
				display:  emptyTitle,
			})
		}
	}
	return out, nil
}

func ptr(s string) *string { return &s }

// containsStr is Python's `str in list` for a value that is a list of strings
// (membership, as the filters use it with lookup_val lists).
func containsStr(list []string, s string) bool { return inList(list, s) }

// --- the view -----------------------------------------------------------------------------------

func (a *App) adminChangelist(r *adminReq, m *modelSpec) {
	c := r.c
	if !r.perms.has(m, "view") && !r.perms.has(m, "change") {
		a.adminForbidden(c)
		return
	}
	actions := a.adminActions(r, m)
	cl, err := a.newChangeList(r, m, len(actions) > 0)
	if err != nil {
		var dis *errDisallowed
		switch {
		case asErr(err, &dis):
			http.Error(c.W, "Bad Request (400)", http.StatusBadRequest)
		case isLookupErr(err):
			if _, ok := c.R.URL.Query()[errorFlag]; ok {
				c.renderContext(http.StatusOK, "admin/invalid_setup.html", pongo2.Context{"title": "Database error", "utc_offset": a.utcOffset()})
				return
			}
			c.Redirect(c.R.URL.Path + "?" + errorFlag + "=1")
		default:
			c.ServerError(err)
		}
		return
	}

	actionFailed := false
	if c.R.Method == http.MethodPost {
		pf := c.PostForm()
		selected := pf["_selected_action"]
		_, hasIndex := pf["index"]
		_, hasSave := pf["_save"]
		if len(actions) > 0 && hasIndex && !hasSave {
			if len(selected) > 0 {
				handled, err := a.runAdminAction(r, cl, actions)
				if err != nil {
					c.ServerError(err)
					return
				}
				if handled {
					return
				}
				actionFailed = true
			} else {
				c.Session.addMessage("warning", "Items must be selected in order to perform actions on them. No items have been changed.")
				actionFailed = true
			}
		}
		_, hasChk := pf["_selected_action"]
		if len(actions) > 0 && hasChk && !hasIndex && !hasSave {
			if len(selected) > 0 {
				handled, err := a.runAdminAction(r, cl, actions)
				if err != nil {
					c.ServerError(err)
					return
				}
				if handled {
					return
				}
				actionFailed = true
			}
		}
	}
	if actionFailed {
		c.SaveSession()
		c.Redirect(c.R.URL.RequestURI())
		return
	}
	a.renderChangelist(r, cl, actions)
}

func (a *App) adminForbidden(c *Ctx) {
	http.Error(c.W, "Forbidden", http.StatusForbidden)
}

func (a *App) renderChangelist(r *adminReq, cl *changeList, actions []adminAction) {
	m := cl.m
	title := fmt.Sprintf("Select %s to %s", m.VerboseName, map[bool]string{true: "change", false: "view"}[r.perms.has(m, "change")])
	if cl.isPopup {
		title = fmt.Sprintf("Select %s", m.VerboseName)
	}
	ctx := r.base(title)
	opts := a.adminOpts(m)
	clMap, err := cl.templateMap(opts)
	if err != nil {
		r.c.ServerError(err)
		return
	}
	n := len(cl.results)
	ctx["cl"] = clMap
	ctx["opts"] = opts
	ctx["module_name"] = m.VerboseNamePlural
	ctx["selection_note"] = fmt.Sprintf("0 of %d selected", n)
	if cl.resultCount == 1 {
		ctx["selection_note_all"] = "1 selected"
	} else {
		ctx["selection_note_all"] = fmt.Sprintf("All %d selected", cl.resultCount)
	}
	ctx["has_add_permission"] = r.perms.has(m, "add")
	ctx["actions_on_top"] = true
	ctx["actions_on_bottom"] = false
	ctx["actions_selection_counter"] = true
	ctx["preserved_filters"] = cl.preserved
	ctx["media"] = adminMediaCtx(a.modelAdminMedia())
	ctx["is_popup"] = cl.isPopup
	ctx["to_field"] = nil
	ctx["search_var"] = searchVar
	ctx["is_popup_var"] = popupVar
	ctx["is_facets_var"] = facetsVar
	ctx["show_result_count"] = cl.resultCount != cl.fullResultCount
	if len(actions) > 0 {
		ctx["action_form"] = actionForm(actions, m, a)
	} else {
		ctx["action_form"] = nil
	}
	if r.perms.has(m, "add") {
		ctx["add_url"] = addPreservedFiltersP(a.adminURL(m, "add"), m, cl.preserved, a, cl.isPopup, cl.toFieldVal)
	}
	// result_list
	headers, numSorted := cl.resultHeaders()
	ctx["result_headers"] = headers
	ctx["num_sorted_fields"] = numSorted
	ctx["result_hidden_fields"] = []any{}
	rows, err := cl.resultRows()
	if err != nil {
		r.c.ServerError(err)
		return
	}
	ctx["results"] = rows
	// pagination
	pagReq := (!cl.showAll || !cl.canShowAll) && cl.multiPage
	ctx["pagination_required"] = pagReq
	var pageRange []any
	if pagReq {
		pageRange = cl.pageRange()
	}
	ctx["page_range"] = pageRange
	if cl.canShowAll && !cl.showAll && cl.multiPage {
		ctx["show_all_url"] = cl.queryString([][2]string{{allVar, ""}}, nil)
	} else {
		ctx["show_all_url"] = ""
	}
	r.render("admin/change_list.html", ctx)
}

// adminOpts is the model's _meta as the templates read it.
func (a *App) adminOpts(m *modelSpec) map[string]any {
	return map[string]any{
		"app_label": m.AppLabel, "model_name": m.ModelName, "object_name": objectName(m),
		"verbose_name": m.VerboseName, "verbose_name_plural": m.VerboseNamePlural,
		"app_config": map[string]any{"verbose_name": adminSiteSpec.spec.Apps[m.AppLabel]},
	}
}

func objectName(m *modelSpec) string {
	parts := strings.Split(m.Label, ".")
	n := parts[len(parts)-1]
	// the class name is the model name with capitals; the spec only has the
	// lower-case name, which is what Django's object_name is for single words
	switch m.Label {
	case "accounts.portalcredential":
		return "PortalCredential"
	case "deeds.deed":
		return "Deed"
	case "ledger.registeredtoken":
		return "RegisteredToken"
	case "ledger.receiptsyncrun":
		return "ReceiptSyncRun"
	}
	return capfirst(n)
}

// templateMap is the changelist as the templates read it (`cl`).
func (cl *changeList) templateMap(opts map[string]any) (map[string]any, error) {
	var items []any
	for _, k := range cl.params.keys {
		items = append(items, []string{k, cl.params.vals[k][0]})
	}
	var specs []any
	for _, lf := range cl.filters {
		choices, err := cl.filterChoices(lf)
		if err != nil {
			return nil, err
		}
		var cs []any
		for _, c := range choices {
			cs = append(cs, map[string]any{"selected": c.selected, "query_string": c.query, "display": c.display})
		}
		specs = append(specs, map[string]any{"title": lf.title, "choices": cs})
	}
	resultList := make([]any, len(cl.results))
	for i := range cl.results {
		resultList[i] = i
	}
	var searchFields any
	if len(cl.m.SearchFields) > 0 {
		searchFields = cl.m.SearchFields
	}
	return map[string]any{
		"opts": opts, "has_filters": len(cl.filters) > 0, "has_active_filters": cl.hasActive,
		"is_facets_optional": true, "add_facets": cl.addFacets,
		"remove_facet_link":    cl.queryString(nil, []string{facetsVar}),
		"add_facet_link":       cl.queryString([][2]string{{facetsVar, "True"}}, nil),
		"clear_all_filters_qs": cl.clearAllQS,
		"filter_specs":         specs, "query": cl.query, "search_fields": searchFields, "search_help_text": nil,
		"result_count": cl.resultCount, "full_result_count": cl.fullResultCount, "show_full_result_count": true,
		"show_admin_actions": cl.fullResultCount > 0, "is_popup": cl.isPopup, "result_list": resultList,
		"params": map[string]any{"items": items}, "formset": nil, "date_hierarchy": nil,
	}, nil
}

// pageRange is Paginator.get_elided_page_range, rendered as paginator_number does.
func (cl *changeList) pageRange() []any {
	const onEachSide, onEnds = 3, 2
	n, cur := cl.numPages, cl.pageNum
	var seq []int // 0 is the ellipsis
	if n <= (onEachSide+onEnds)*2 {
		for i := 1; i <= n; i++ {
			seq = append(seq, i)
		}
	} else {
		if cur > (1+onEachSide+onEnds)+1 {
			for i := 1; i <= onEnds; i++ {
				seq = append(seq, i)
			}
			seq = append(seq, 0)
			for i := cur - onEachSide; i <= cur; i++ {
				seq = append(seq, i)
			}
		} else {
			for i := 1; i <= cur; i++ {
				seq = append(seq, i)
			}
		}
		if cur < (n-onEachSide-onEnds)-1 {
			for i := cur + 1; i <= cur+onEachSide; i++ {
				seq = append(seq, i)
			}
			seq = append(seq, 0)
			for i := n - onEnds + 1; i <= n; i++ {
				seq = append(seq, i)
			}
		} else {
			for i := cur + 1; i <= n; i++ {
				seq = append(seq, i)
			}
		}
	}
	var out []any
	for _, i := range seq {
		switch {
		case i == 0:
			out = append(out, safe("… "))
		case i == cur:
			out = append(out, safe(fmt.Sprintf(`<span class="this-page">%d</span> `, i)))
		default:
			class := ""
			if i == n {
				class = ` class="end"`
			}
			out = append(out, safe(fmt.Sprintf(`<a href="%s"%s>%d</a> `, esc(cl.queryString([][2]string{{pageVar, strconv.Itoa(i)}}, nil)), class, i)))
		}
	}
	return out
}

// --- headers and rows ----------------------------------------------------------------------

func (cl *changeList) columnLabel(col ldCol) string {
	if col.ld == nil {
		return ""
	}
	if col.ld.Field != nil {
		return col.ld.Field.VerboseName
	}
	switch col.name {
	case "__str__":
		return cl.m.VerboseName
	case "admin":
		return "Admin"
	}
	return col.ld.ShortDescription
}

func (cl *changeList) resultHeaders() ([]any, int) {
	idxs, ordering := cl.orderingFieldColumns()
	var headers []any
	numSorted := 0
	for i, col := range cl.cols {
		if col.name == "action_checkbox" {
			headers = append(headers, map[string]any{
				"html_text":    safe(`<input type="checkbox" id="action-toggle" aria-label="Select all objects on this page for an action">`),
				"class_attrib": safe(` class="action-checkbox-column"`), "sortable": false,
			})
			continue
		}
		text := cl.columnLabel(col)
		sortable := true
		if col.ld.Field == nil {
			ao := col.ld.AdminOrderField
			if (ao == nil || *ao == "") && !strings.Contains(col.name, "__") {
				sortable = false
			}
		}
		if !sortable {
			headers = append(headers, map[string]any{
				"text": text, "class_attrib": safe(fmt.Sprintf(` class="column-%s"`, esc(col.name))), "sortable": false,
			})
			continue
		}
		classes := []string{"sortable", "column-" + col.name}
		orderType, newOrder, priority := "", "asc", 0
		_, isSorted := ordering[i]
		if isSorted {
			orderType = ordering[i]
			for pos, ix := range idxs {
				if ix == i {
					priority = pos + 1
				}
			}
			classes = append(classes, "sorted "+orderType+"ending")
			if orderType == "asc" {
				newOrder = "desc"
			} else {
				newOrder = "asc"
			}
		}
		mk := func(t string, n int) string {
			if t == "desc" {
				return "-" + strconv.Itoa(n)
			}
			return strconv.Itoa(n)
		}
		var primary, remove, toggle []string
		for _, j := range idxs {
			ot := ordering[j]
			if j == i {
				p := mk(newOrder, j)
				primary = append([]string{p}, primary...)
				toggle = append(toggle, p)
			} else {
				p := mk(ot, j)
				primary = append(primary, p)
				toggle = append(toggle, p)
				remove = append(remove, p)
			}
		}
		if !isSorted {
			primary = append([]string{mk(newOrder, i)}, primary...)
		}
		if isSorted {
			numSorted++
		}
		headers = append(headers, map[string]any{
			"text": text, "sortable": true, "sorted": isSorted, "ascending": orderType == "asc", "sort_priority": priority,
			"url_primary":  cl.queryString([][2]string{{orderVar, strings.Join(primary, ".")}}, nil),
			"url_remove":   cl.queryString([][2]string{{orderVar, strings.Join(remove, ".")}}, nil),
			"url_toggle":   cl.queryString([][2]string{{orderVar, strings.Join(toggle, ".")}}, nil),
			"class_attrib": safe(` class="` + esc(strings.Join(classes, " ")) + `"`),
		})
	}
	return headers, numSorted
}

func (cl *changeList) linkInCol(first bool, name string) bool {
	if first && len(cl.links) == 0 {
		return true
	}
	return inList(cl.links, name)
}

func (cl *changeList) resultRows() ([]any, error) {
	re := cl.a.newRepr(cl.r.c.Context())
	var rows []any
	for _, o := range cl.results {
		first := true
		var cells []any
		for _, col := range cl.cols {
			classes := []string{"field-" + col.name}
			var repr string
			switch {
			case col.name == "action_checkbox":
				classes = []string{"action-checkbox"}
				repr = fmt.Sprintf(`<input type="checkbox" name="_selected_action" value="%s" class="action-select" aria-label="Select this object for an action - %s">`,
					esc(o.pkString()), esc(re.str(o)))
			case col.ld.Field != nil:
				f := col.ld.Field
				v := o.vals[f.Name]
				if f.isFK() {
					if v == nil {
						repr = esc(cl.empty)
					} else {
						repr = esc(re.related(cl.m, f.Name, v))
					}
				} else {
					repr = cl.a.displayForField(v, f, cl.empty)
				}
				if f.InternalType == "DateField" || f.InternalType == "DateTimeField" || f.InternalType == "ForeignKey" {
					classes = append(classes, "nowrap")
				}
			default:
				val, boolean, err := cl.computedValue(o, col.name, re)
				if err != nil {
					return nil, err
				}
				switch {
				case boolean:
					repr = booleanIcon(val)
				case val == nil || val == "":
					repr = esc(cl.empty)
				default:
					repr = esc(valueString(val))
				}
			}
			cls := ` class="` + strings.Join(classes, " ") + `"`
			if cl.linkInCol(first, col.name) {
				tag := "td"
				if first {
					tag = "th"
				}
				first = false
				href := addPreservedFilters(cl.a.adminURL(cl.m, "change", adminQuote(o.pkString())), cl.m, cl.preserved, cl.a)
				opener := ""
				if cl.isPopup {
					opener = fmt.Sprintf(` data-popup-opener="%s"`, esc(o.pkString()))
				}
				cells = append(cells, safe(fmt.Sprintf(`<%s%s><a href="%s"%s>%s</a></%s>`, tag, cls, esc(href), opener, repr, tag)))
			} else {
				cells = append(cells, safe(fmt.Sprintf(`<td%s>%s</td>`, cls, repr)))
			}
		}
		rows = append(rows, cells)
	}
	return rows, nil
}

// computedValue is the value of a list_display entry that is not a model
// field: __str__, a model property or an admin method.
func (cl *changeList) computedValue(o *adminObj, name string, re *reprEnv) (any, bool, error) {
	ctx := cl.r.c.Context()
	switch cl.m.Label + "." + name {
	case cl.m.Label + ".__str__":
		return re.str(o), false, nil
	case "accounts.portalcredential.has_password":
		return valueString(o.vals["portal_password"]) != "", true, nil
	case "accounts.organization.admin":
		var name *string
		err := cl.a.Store.Querier().QueryRow(ctx, `SELECT u.username FROM accounts_membership m JOIN auth_user u ON u.id = m.user_id
			WHERE m.organization_id = $1 AND m.role = 'admin' ORDER BY m.id LIMIT 1`, o.vals["id"]).Scan(&name)
		if err != nil {
			return nil, false, nil
		}
		return *name, false, nil
	case "accounts.organization.member_count":
		var n int64
		err := cl.a.Store.Querier().QueryRow(ctx, `SELECT COUNT(*) FROM accounts_membership WHERE organization_id = $1`, o.vals["id"]).Scan(&n)
		return n, false, err
	}
	return nil, false, fmt.Errorf("admin: unknown list_display entry %s.%s", cl.m.Label, name)
}

// adminMediaCtx is a media object as the templates read it ({{ media.js }}, {{ media.css }}).
func adminMediaCtx(m *adminMediaSet) map[string]any {
	js := newMedia()
	js.js = m.js
	css := newMedia()
	css.css, css.mediums = m.css, m.mediums
	return map[string]any{"js": safe(js.html()), "css": safe(css.html())}
}

// addPreservedFilters is the add_preserved_filters template tag.
func addPreservedFilters(u string, m *modelSpec, preserved string, a *App) string {
	return addPreservedFiltersP(u, m, preserved, a, false, "")
}

// addPreservedFiltersP also carries the popup and to_field flags along.
func addPreservedFiltersP(u string, m *modelSpec, preserved string, a *App, popup bool, toField string) string {
	pu, err := url.Parse(u)
	if err != nil {
		return u
	}
	merged := map[string]string{}
	var order []string
	set := func(k, v string) {
		if _, ok := merged[k]; !ok {
			order = append(order, k)
		}
		merged[k] = v
	}
	if preserved != "" {
		pf := parseQueryOrdered(preserved)
		kv := map[string]string{}
		var keys []string
		for _, k := range pf.keys {
			v, _ := pf.last(k)
			kv[k] = v
			keys = append(keys, k)
		}
		if a.Router.Resolves(unquoteURLPath(pu.Path)) {
			if name, ok := a.Router.NameOf(unquoteURLPath(pu.Path)); ok && name == "admin:"+m.AppLabel+"_"+m.ModelName+"_changelist" {
				if inner, ok := kv["_changelist_filters"]; ok {
					ip := parseQueryOrdered(inner)
					kv, keys = map[string]string{}, nil
					for _, k := range ip.keys {
						v, _ := ip.last(k)
						kv[k] = v
						keys = append(keys, k)
					}
				}
			}
		}
		for _, k := range keys {
			set(k, kv[k])
		}
	}
	if popup {
		set(popupVar, "1")
	}
	if toField != "" {
		set(toFieldVr, toField)
	}
	own := parseQueryOrdered(pu.RawQuery)
	for _, k := range own.keys {
		v, _ := own.last(k)
		set(k, v)
	}
	var parts []string
	for _, k := range order {
		parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(merged[k]))
	}
	pu.RawQuery = strings.Join(parts, "&")
	return pu.String()
}

func unquoteURLPath(p string) string {
	if s, err := url.PathUnescape(p); err == nil {
		return s
	}
	return p
}

func asErr[T error](err error, target *T) bool {
	if e, ok := err.(T); ok {
		*target = e
		return true
	}
	return false
}

func isLookupErr(err error) bool {
	_, ok := err.(*errLookup)
	return ok
}

var _ = context.Background

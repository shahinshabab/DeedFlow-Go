package web

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// A small query builder for the admin's changelists: field lookups the way
// Django spells them in query strings (status__exact, owner__username__icontains,
// groups__id__exact ...), search, ordering.

// errLookup is IncorrectLookupParameters / FieldError / ValueError: the
// parameters cannot be turned into a query.
type errLookup struct{ msg string }

func (e *errLookup) Error() string { return e.msg }

func badLookup(format string, args ...any) error { return &errLookup{fmt.Sprintf(format, args...)} }

// errDisallowed is DisallowedModelAdminLookup: a 400 response.
type errDisallowed struct{ msg string }

func (e *errDisallowed) Error() string { return e.msg }

type clQuery struct {
	m     *modelSpec
	joins []string
	where []string
	args  []any
	order []string
	nj    int
}

func newQuery(m *modelSpec) *clQuery { return &clQuery{m: m} }

func (q *clQuery) clone() *clQuery {
	c := *q
	c.joins = append([]string(nil), q.joins...)
	c.where = append([]string(nil), q.where...)
	c.args = append([]any(nil), q.args...)
	c.order = append([]string(nil), q.order...)
	return &c
}

func (q *clQuery) arg(v any) string {
	q.args = append(q.args, v)
	return "$" + strconv.Itoa(len(q.args))
}

var lookupNames = map[string]bool{
	"exact": true, "iexact": true, "contains": true, "icontains": true, "startswith": true, "istartswith": true,
	"endswith": true, "iendswith": true, "gt": true, "gte": true, "lt": true, "lte": true, "in": true, "isnull": true,
}

// target is a resolved lookup path.
type target struct {
	expr   string // SQL expression of the column
	field  *modelFieldSpec
	lookup string
	m2m    *m2mTarget
}

type m2mTarget struct {
	through, leftCol, rightCol string
}

// resolve walks a path such as "owner__username__icontains".
func (q *clQuery) resolve(path string) (*target, error) {
	parts := strings.Split(path, "__")
	cur := q.m
	alias := "t"
	for i := 0; i < len(parts); i++ {
		name := parts[i]
		f := cur.field(name)
		if f == nil {
			for _, cf := range cur.ModelFields {
				if cf.Attname == name {
					f = cf
				}
			}
		}
		if f == nil {
			return nil, badLookup("Cannot resolve keyword %q into field", name)
		}
		rest := parts[i+1:]
		switch {
		case f.isM2M():
			rt := f.Related
			through := cur.Table + "_" + f.Name
			t := &target{field: f, m2m: &m2mTarget{through: through, leftCol: cur.ModelName + "_id", rightCol: rt.ModelName + "_id"}}
			t.expr = alias + `."` + cur.pkField().Column + `"`
			// groups__id__exact: the related primary key, then the lookup
			if len(rest) > 0 && rest[0] == rt.ToField {
				rest = rest[1:]
			}
			t.lookup, rest = lookupOf(rest)
			if len(rest) > 0 {
				return nil, badLookup("unsupported lookup %q", path)
			}
			return t, nil
		case f.isFK():
			rel := adminSiteSpec.model(f.Related.Label)
			if len(rest) > 0 && !lookupNames[rest[0]] && rel != nil && rel.field(rest[0]) != nil {
				if rest[0] == rel.PK {
					// the foreign key's column is the related primary key
					rest = rest[1:]
					t := &target{field: f, expr: alias + `."` + f.Column + `"`}
					t.lookup, rest = lookupOf(rest)
					if len(rest) > 0 {
						return nil, badLookup("unsupported lookup %q", path)
					}
					return t, nil
				}
				q.nj++
				ja := "j" + strconv.Itoa(q.nj)
				q.joins = append(q.joins, fmt.Sprintf(`LEFT JOIN %s %s ON %s."%s" = %s."%s"`,
					quoteIdent(rel.Table), ja, ja, rel.pkField().Column, alias, f.Column))
				cur, alias = rel, ja
				continue
			}
			t := &target{field: f, expr: alias + `."` + f.Column + `"`}
			var left []string
			t.lookup, left = lookupOf(rest)
			if len(left) > 0 {
				return nil, badLookup("unsupported lookup %q", path)
			}
			return t, nil
		default:
			t := &target{field: f, expr: alias + `."` + f.Column + `"`}
			var left []string
			t.lookup, left = lookupOf(rest)
			if len(left) > 0 {
				return nil, badLookup("unsupported lookup %q", path)
			}
			return t, nil
		}
	}
	return nil, badLookup("Cannot resolve %q", path)
}

func lookupOf(rest []string) (string, []string) {
	if len(rest) == 0 {
		return "exact", nil
	}
	if lookupNames[rest[0]] {
		return rest[0], rest[1:]
	}
	return "", rest
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// cond builds the SQL condition of one lookup with one value (text from the
// query string, or an already typed value for isnull).
func (q *clQuery) cond(path string, value any) (string, error) {
	t, err := q.resolve(path)
	if err != nil {
		return "", err
	}
	if t.lookup == "" {
		return "", badLookup("Unsupported lookup in %q", path)
	}
	if t.lookup == "isnull" {
		isnull, _ := value.(bool)
		if t.m2m != nil {
			not := ""
			if isnull {
				not = "NOT "
			}
			return fmt.Sprintf(`%sEXISTS (SELECT 1 FROM %s x WHERE x."%s" = %s)`, not, quoteIdent(t.m2m.through), t.m2m.leftCol, t.expr), nil
		}
		if isnull {
			return t.expr + " IS NULL", nil
		}
		return t.expr + " IS NOT NULL", nil
	}
	text := valueString(value)
	f := t.field
	if t.m2m != nil {
		rt := f.Related
		target := &modelFieldSpec{InternalType: "IntegerField"}
		_ = rt
		arg, ok := coerceValue(target, text)
		if !ok {
			return "", badLookup("Field 'id' expected a number but got %q.", text)
		}
		if t.lookup != "exact" {
			return "", badLookup("unsupported lookup %q on a many-to-many field", t.lookup)
		}
		return fmt.Sprintf(`EXISTS (SELECT 1 FROM %s x WHERE x."%s" = %s AND x."%s" = %s)`,
			quoteIdent(t.m2m.through), t.m2m.leftCol, t.expr, t.m2m.rightCol, q.arg(arg)), nil
	}
	switch t.lookup {
	case "contains", "icontains", "startswith", "istartswith", "endswith", "iendswith":
		esc := likeEscaper.Replace(text)
		switch t.lookup {
		case "contains", "icontains":
			esc = "%" + esc + "%"
		case "startswith", "istartswith":
			esc += "%"
		default:
			esc = "%" + esc
		}
		if strings.HasPrefix(t.lookup, "i") {
			return fmt.Sprintf("UPPER(%s::text) LIKE UPPER(%s)", t.expr, q.arg(esc)), nil
		}
		return fmt.Sprintf("%s::text LIKE %s", t.expr, q.arg(esc)), nil
	case "iexact":
		return fmt.Sprintf("UPPER(%s::text) = UPPER(%s)", t.expr, q.arg(text)), nil
	case "in":
		return "", badLookup("in is only supported with several values")
	}
	arg, ok := coerceValue(f, text)
	if !ok {
		return "", badLookup("Field %q expected a valid value but got %q.", f.Name, text)
	}
	cast := ""
	switch f.InternalType {
	case "UUIDField":
		cast = "::uuid"
	case "DecimalField":
		cast = "::numeric"
	}
	op := map[string]string{"exact": "=", "gt": ">", "gte": ">=", "lt": "<", "lte": "<="}[t.lookup]
	return fmt.Sprintf("%s %s %s%s", t.expr, op, q.arg(arg), cast), nil
}

// filterAny adds "path = v1 OR path = v2 ..." (Django ORs repeated values).
func (q *clQuery) filterAny(path string, values []any) error {
	var parts []string
	for _, v := range values {
		c, err := q.cond(path, v)
		if err != nil {
			return err
		}
		parts = append(parts, c)
	}
	if len(parts) == 0 {
		return nil
	}
	q.where = append(q.where, "("+strings.Join(parts, " OR ")+")")
	return nil
}

var smartSplitRe = regexp.MustCompile(`(?:[^\s'"]*(?:(?:"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*')[^\s'"]*)+)|\S+`)

// smartSplit is django.utils.text.smart_split.
func smartSplit(s string) []string { return smartSplitRe.FindAllString(s, -1) }

func unescapeStringLiteral(s string) string {
	q := s[:1]
	return strings.ReplaceAll(strings.ReplaceAll(s[1:len(s)-1], `\`+q, q), `\\`, `\`)
}

// search is ModelAdmin.get_search_results.
func (q *clQuery) search(fields []string, term string) error {
	if len(fields) == 0 || term == "" {
		return nil
	}
	for _, bit := range smartSplit(term) {
		if (strings.HasPrefix(bit, `"`) || strings.HasPrefix(bit, `'`)) && len(bit) > 1 && bit[0] == bit[len(bit)-1] {
			bit = unescapeStringLiteral(bit)
		}
		var ors []string
		for _, sf := range fields {
			lookup, path := "icontains", sf
			switch {
			case strings.HasPrefix(sf, "^"):
				lookup, path = "istartswith", sf[1:]
			case strings.HasPrefix(sf, "="):
				lookup, path = "iexact", sf[1:]
			case strings.HasPrefix(sf, "@"):
				path = sf[1:]
			case strings.HasPrefix(sf, "$"):
				path = sf[1:]
			}
			c, err := q.cond(path+"__"+lookup, bit)
			if err != nil {
				return err
			}
			ors = append(ors, c)
		}
		q.where = append(q.where, "("+strings.Join(ors, " OR ")+")")
	}
	return nil
}

// orderBy resolves ordering names ("-created_at", "pk") to ORDER BY items.
func (q *clQuery) setOrdering(names []string) {
	q.order = nil
	for _, n := range names {
		desc := strings.HasPrefix(n, "-")
		n = strings.TrimPrefix(n, "-")
		col := ""
		switch {
		case n == "pk":
			col = `t."` + q.m.pkField().Column + `"`
		case q.m.field(n) != nil && !q.m.field(n).isM2M():
			col = `t."` + q.m.field(n).Column + `"`
		default:
			for _, f := range q.m.ModelFields {
				if f.Attname == n {
					col = `t."` + f.Column + `"`
				}
			}
		}
		if col == "" {
			continue
		}
		if desc {
			col += " DESC"
		}
		q.order = append(q.order, col)
	}
}

func (q *clQuery) fromWhere() string {
	s := " FROM " + quoteIdent(q.m.Table) + " t"
	if len(q.joins) > 0 {
		s += " " + strings.Join(q.joins, " ")
	}
	if len(q.where) > 0 {
		s += " WHERE " + strings.Join(q.where, " AND ")
	}
	return s
}

func (q *clQuery) orderLimit(limit, offset int) string {
	s := ""
	if len(q.order) > 0 {
		s += " ORDER BY " + strings.Join(q.order, ", ")
	}
	if limit > 0 {
		s += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)
	}
	return s
}

// parseDateTime reads the datetime formats Django's DateTimeField accepts.
func parseDateTime(s string, loc *time.Location) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"2006-01-02 15:04:05.999999", "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02T15:04:05.999999", "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

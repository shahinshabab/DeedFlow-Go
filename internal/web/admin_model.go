package web

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Generic access to the rows of the models the admin shows: loading them by
// primary key or query, their string form (Django's __str__) and the way the
// admin displays a field's value.

// adminObj is one row, keyed by field name (a foreign key's value is the id
// of the object it points at).
type adminObj struct {
	m    *modelSpec
	vals map[string]any
}

func (o *adminObj) pk() any { return o.vals[o.m.PK] }

func (o *adminObj) pkString() string { return valueString(o.pk()) }

func valueString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case int64:
		return strconv.FormatInt(x, 10)
	case bool:
		if x {
			return "True"
		}
		return "False"
	default:
		return fmt.Sprint(v)
	}
}

// model finds a model of the admin site by label, including the models that
// only appear as inlines.
func (s *adminSite) model(label string) *modelSpec {
	if m := s.models[label]; m != nil {
		return m
	}
	if m := s.inlineModels[label]; m != nil {
		return m
	}
	if g := s.graph[label]; g != nil {
		return g.spec
	}
	return nil
}

// inlineModel is the model of an inline, as far as the admin needs to know it.
func inlineModel(in *inlineSpec) *modelSpec {
	m := &modelSpec{
		Label: in.Model, AppLabel: in.AppLabel, ModelName: in.ModelName, VerboseName: in.VerboseName,
		VerboseNamePlural: in.VerboseNamePl, Table: in.Table, PK: "id", ModelFields: in.ModelFields,
		Ordering: in.Ordering, byName: map[string]*modelFieldSpec{},
	}
	for _, f := range in.ModelFields {
		m.byName[f.Name] = f
		if f.PrimaryKey {
			m.PK = f.Name
		}
	}
	return m
}

func (m *modelSpec) pkField() *modelFieldSpec { return m.byName[m.PK] }

func (f *modelFieldSpec) isFK() bool {
	return f.Related != nil && !f.Related.ManyToMany && (f.InternalType == "ForeignKey" || f.InternalType == "OneToOneField")
}

func (f *modelFieldSpec) isM2M() bool { return f.Related != nil && f.Related.ManyToMany }

func (f *modelFieldSpec) isInt() bool {
	switch f.InternalType {
	case "AutoField", "BigAutoField", "IntegerField", "BigIntegerField", "SmallIntegerField",
		"PositiveIntegerField", "PositiveSmallIntegerField", "PositiveBigIntegerField":
		return true
	}
	return false
}

// concreteFields are the fields stored in the model's own table.
func (m *modelSpec) concreteFields() []*modelFieldSpec { return m.ModelFields }

func selectExpr(alias string, f *modelFieldSpec) string {
	c := alias + `."` + f.Column + `"`
	switch f.InternalType {
	case "DecimalField", "UUIDField", "JSONField":
		return c + "::text"
	}
	return c
}

func (m *modelSpec) selectList(alias string) string {
	cols := make([]string, len(m.ModelFields))
	for i, f := range m.ModelFields {
		cols[i] = selectExpr(alias, f)
	}
	return strings.Join(cols, ", ")
}

// scanObjs reads rows selected with selectList.
func (a *App) scanObjs(m *modelSpec, rows pgx.Rows) ([]*adminObj, error) {
	defer rows.Close()
	var out []*adminObj
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		o := &adminObj{m: m, vals: map[string]any{}}
		for i, f := range m.ModelFields {
			o.vals[f.Name] = a.normalizeValue(f, vals[i])
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (a *App) normalizeValue(f *modelFieldSpec, v any) any {
	switch x := v.(type) {
	case int16:
		return int64(x)
	case int32:
		return int64(x)
	case int:
		return int64(x)
	case float32:
		return float64(x)
	case time.Time:
		if f.InternalType == "DateTimeField" {
			return x.In(a.Cfg.TimeZone)
		}
		return time.Date(x.Year(), x.Month(), x.Day(), 0, 0, 0, 0, time.UTC)
	}
	return v
}

// loadObj finds one object by primary key; the pk is text as it comes from a URL.
func (a *App) loadObj(ctx context.Context, m *modelSpec, pk string) (*adminObj, error) {
	arg, ok := coerceValue(m.pkField(), pk)
	if !ok {
		return nil, nil
	}
	rows, err := a.Store.Querier().Query(ctx, fmt.Sprintf(`SELECT %s FROM %s t WHERE t."%s" = $1`,
		m.selectList("t"), quoteIdent(m.Table), m.pkField().Column), arg)
	if err != nil {
		return nil, err
	}
	objs, err := a.scanObjs(m, rows)
	if err != nil || len(objs) == 0 {
		return nil, err
	}
	return objs[0], nil
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// coerceValue converts the text of a query parameter to the value the field's
// column compares with; ok is false when it is not a valid value (Django's
// ValueError or ValidationError).
func coerceValue(f *modelFieldSpec, s string) (any, bool) {
	typ := f.InternalType
	if f.isFK() {
		if t := f.Related; t != nil {
			typ = "IntegerField"
			if strings.HasPrefix(t.ToField, "public") {
				typ = "UUIDField"
			}
		}
	}
	switch typ {
	case "AutoField", "BigAutoField", "IntegerField", "BigIntegerField", "SmallIntegerField",
		"PositiveIntegerField", "PositiveSmallIntegerField", "PositiveBigIntegerField":
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return nil, false
		}
		return n, true
	case "BooleanField":
		switch s {
		case "t", "True", "1":
			return true, true
		case "f", "False", "0":
			return false, true
		}
		return nil, false
	case "DecimalField":
		if _, ok := new(big.Rat).SetString(strings.TrimSpace(s)); !ok {
			return nil, false
		}
		return strings.TrimSpace(s), true
	case "UUIDField":
		if !isUUID(s) {
			return nil, false
		}
		return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "-", "")), true
	case "DateField":
		t, err := time.Parse("2006-01-02", strings.TrimSpace(s))
		if err != nil {
			return nil, false
		}
		return t, true
	}
	if strings.ContainsRune(s, 0) {
		return nil, false
	}
	return s, true
}

func isUUID(s string) bool {
	s = strings.ToLower(strings.TrimSpace(strings.Trim(s, "{}")))
	s = strings.TrimPrefix(s, "urn:uuid:")
	s = strings.ReplaceAll(s, "-", "")
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// --- string forms ----------------------------------------------------------------------------

// reprEnv computes str(obj) for objects of the admin's models, caching the
// related objects it has to load.
type reprEnv struct {
	a     *App
	ctx   context.Context
	cache map[string]string
}

func (a *App) newRepr(ctx context.Context) *reprEnv {
	return &reprEnv{a: a, ctx: ctx, cache: map[string]string{}}
}

func choiceLabel(f *modelFieldSpec, v any) (string, bool) {
	for _, c := range f.Choices {
		if len(c) == 2 && c[0] == valueString(v) {
			return c[1], true
		}
	}
	return "", false
}

func (e *reprEnv) choice(m *modelSpec, o *adminObj, field string) string {
	f := m.byName[field]
	if l, ok := choiceLabel(f, o.vals[field]); ok {
		return l
	}
	return valueString(o.vals[field])
}

func (e *reprEnv) str(o *adminObj) string {
	v := o.vals
	s := func(k string) string { return valueString(v[k]) }
	m := o.m
	switch m.Label {
	case "auth.group":
		return s("name")
	case "auth.user":
		return s("username")
	case "accounts.organization":
		return s("name")
	case "accounts.membership":
		return fmt.Sprintf("%s (%s, %s)", e.related(m, "user", v["user"]), e.choice(m, o, "role"), e.related(m, "organization", v["organization"]))
	case "accounts.portalcredential":
		return "Portal credential for " + e.related(m, "organization", v["organization"])
	case "deeds.deed":
		if s("title") != "" {
			return s("title")
		}
		if s("token_number") != "" {
			return s("token_number")
		}
		return "Deed #" + s("id")
	case "deeds.registrationdetail":
		return "Registration for " + e.related(m, "deed", v["deed"])
	case "deeds.presenter":
		return "Presenter: " + s("name")
	case "deeds.priordocument":
		n := s("number")
		if n == "" {
			n = "?"
		}
		return fmt.Sprintf("%s #%s", s("document_type"), n)
	case "deeds.executant", "deeds.claimant":
		return s("name")
	case "deeds.scheduleitem":
		n := s("number")
		if n == "" {
			n = s("id")
		}
		return "Schedule item " + n
	case "deeds.witness":
		return fmt.Sprintf("%s: %s", e.choice(m, o, "witness_type"), s("name"))
	case "deeds.deedscreenshot":
		return fmt.Sprintf("%s (%s)", s("label"), e.related(m, "deed", v["deed"]))
	case "ledger.registeredtoken":
		return s("token")
	case "ledger.paymentreceipt":
		return fmt.Sprintf("%s %s", e.choice(m, o, "kind"), s("transaction_no"))
	case "ledger.receiptsyncrun":
		t, _ := v["started_at"].(time.Time)
		return fmt.Sprintf("%s %s (%s)", e.related(m, "organization", v["organization"]), t.UTC().Format("2006-01-02 15:04"), s("status"))
	case "ledger.expense":
		t, _ := v["date"].(time.Time)
		return fmt.Sprintf("%s %s Rs %s", e.choice(m, o, "category"), t.Format("02-01-2006"), s("amount"))
	case "ledger.expensebill":
		if s("original_name") != "" {
			return s("original_name")
		}
		return s("file")
	case "filereader.filingsheet":
		return s("name")
	case "auth.permission":
		ct, _ := v["content_type"].(int64)
		return e.contentTypeStr(ct) + " | " + s("name")
	case "admin.logentry":
		switch v["action_flag"] {
		case int64(logAddition):
			return fmt.Sprintf("Added “%s”.", s("object_repr"))
		case int64(logChange):
			return fmt.Sprintf("Changed “%s” — %s", s("object_repr"), logChangeMessage(s("change_message")))
		case int64(logDeletion):
			return fmt.Sprintf("Deleted “%s.”", s("object_repr"))
		}
		return "LogEntry Object"
	}
	if g := adminSiteSpec.graph[m.Label]; g != nil {
		return fmt.Sprintf("%s object (%s)", g.ObjectName, s(m.PK))
	}
	return s(m.PK)
}

// related is str() of the object a foreign key points at ("None" when unset).
func (e *reprEnv) related(m *modelSpec, field string, id any) string {
	if id == nil {
		return "None"
	}
	f := m.byName[field]
	if f == nil || f.Related == nil {
		return valueString(id)
	}
	return e.byID(f.Related.Label, valueString(id))
}

// byID is str() of the object of a model with the given primary key.
func (e *reprEnv) byID(label, id string) string {
	key := label + ":" + id
	if s, ok := e.cache[key]; ok {
		return s
	}
	s := id
	if label == "auth.permission" {
		s = e.permissionStr(id)
	} else if m := adminSiteSpec.model(label); m != nil {
		if o, err := e.a.loadObj(e.ctx, m, id); err == nil && o != nil {
			s = e.str(o)
		}
	}
	e.cache[key] = s
	return s
}

// permissionStr is Permission.__str__: "<app> | <model> | <name>".
func (e *reprEnv) permissionStr(id string) string {
	var ct int64
	var name string
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return id
	}
	if err := e.a.Store.Querier().QueryRow(e.ctx, `SELECT content_type_id, name FROM auth_permission WHERE id = $1`, n).Scan(&ct, &name); err != nil {
		return id
	}
	return e.contentTypeStr(ct) + " | " + name
}

// contentTypeStr is ContentType.__str__ (app_labeled_name).
func (e *reprEnv) contentTypeStr(id int64) string {
	label := e.a.contentTypeLabel(e.ctx, id)
	if am := adminSiteSpec.any[label]; am != nil {
		return am.AppVerboseName + " | " + am.VerboseName
	}
	return label
}

// --- display of values ------------------------------------------------------------------------

const staticURL = "/static/"

func booleanIcon(v any) string {
	kind, alt := "unknown", "None"
	if b, ok := v.(bool); ok {
		if b {
			kind, alt = "yes", "True"
		} else {
			kind, alt = "no", "False"
		}
	}
	return `<img src="` + staticURL + `admin/img/icon-` + kind + `.svg" alt="` + alt + `">`
}

// displayForField is admin.utils.display_for_field, returning escaped HTML.
func (a *App) displayForField(v any, f *modelFieldSpec, empty string) string {
	if len(f.Choices) > 0 {
		if l, ok := choiceLabel(f, v); ok {
			return esc(l)
		}
		return esc(empty)
	}
	if f.InternalType == "BooleanField" {
		return booleanIcon(v)
	}
	if v == nil || v == "" {
		return esc(empty)
	}
	switch f.InternalType {
	case "DateTimeField":
		if t, ok := v.(time.Time); ok {
			return esc(djangoDate(t, "N j, Y, P"))
		}
	case "DateField":
		if t, ok := v.(time.Time); ok {
			return esc(djangoDate(t, "N j, Y"))
		}
	case "DecimalField":
		return esc(numberFormat(valueString(v), f.DecimalPlace))
	case "FileField":
		s := valueString(v)
		return `<a href="` + esc(mediaURL(s)) + `">` + esc(s) + `</a>`
	case "JSONField":
		switch s := valueString(v); s {
		case "null", "{}", "[]", `""`:
			return esc(empty)
		default:
			return esc(pyJSONText(s))
		}
	}
	return esc(valueString(v))
}

func mediaURL(name string) string { return "/media/" + name }

// pyJSONText is json.dumps(value, ensure_ascii=False) of a jsonb text value:
// Postgres already prints it with Python's separators.
func pyJSONText(s string) string {
	if !utf8.ValidString(s) {
		return s
	}
	var v any
	if json.Unmarshal([]byte(s), &v) != nil {
		return s
	}
	return s
}

// numberFormat is formats.number_format(value, decimal_pos) without grouping.
func numberFormat(s string, places int) string {
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	whole, frac, _ := strings.Cut(s, ".")
	if len(frac) > places {
		frac = frac[:places]
	}
	frac += strings.Repeat("0", places-len(frac))
	out := whole
	if places > 0 {
		out += "." + frac
	}
	if neg && strings.Trim(out, "0.") != "" {
		out = "-" + out
	}
	return out
}

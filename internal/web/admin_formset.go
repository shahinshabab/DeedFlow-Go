package web

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/shahinshabab/deedflow-go/internal/pyjson"
)

// Inline formsets: the objects related to the one being edited, shown as a
// table or a stack of forms below its own fields.

type inlineFS struct {
	r      *adminReq
	a      *App
	spec   *inlineSpec
	m      *modelSpec
	prefix string
	parent *adminObj // nil when the parent is being added

	bound    bool
	data     url.Values
	files    map[string]*adminUpload
	existing []*adminObj
	forms    []*aForm
	empty    *aForm
	nInitial int
	total    int

	fkName string
	fkCol  string
	minNum int
	maxNum int

	canAdd, canChange, canDelete, canView bool
	hasDeletePerm                         bool

	nonForm   []string
	order     []string // the inline's visible fields (without the foreign key)
	readonly  map[string]bool
	deleteIDs map[int]bool
	errLists  int
}

const mgmtTotal, mgmtInitial, mgmtMin, mgmtMax = "TOTAL_FORMS", "INITIAL_FORMS", "MIN_NUM_FORMS", "MAX_NUM_FORMS"

func (a *App) newInlineFS(r *adminReq, spec *inlineSpec, parent *adminObj, parentModel *modelSpec) (*inlineFS, error) {
	m := adminSiteSpec.model(spec.Model)
	fs := &inlineFS{r: r, a: a, spec: spec, m: m, prefix: spec.Prefix, parent: parent, fkName: spec.FKName, fkCol: spec.FKColumn,
		readonly: map[string]bool{}, deleteIDs: map[int]bool{}}
	for _, n := range spec.ReadonlyFields {
		fs.readonly[n] = true
	}
	fs.maxNum = 1000
	if spec.OneToOne {
		fs.maxNum = 1
	} else if spec.MaxNum != nil {
		fs.maxNum = *spec.MaxNum
	}
	fs.minNum = spec.MinNum
	rel := adminSiteSpec.models[m.Label]
	p := r.perms
	if rel != nil {
		fs.canAdd, fs.canChange, fs.canDelete, fs.canView = p.has(rel, "add"), p.has(rel, "change"), p.has(rel, "delete"), p.has(rel, "view")
	} else {
		// inline models are checked with their own permissions
		fs.canAdd, fs.canChange, fs.canDelete, fs.canView = p.has(m, "add"), p.has(m, "change"), p.has(m, "delete"), p.has(m, "view")
	}
	// a user who may change the parent but sees the inline model read-only
	if !fs.canView && fs.canChange {
		fs.canView = true
	}
	fs.hasDeletePerm = fs.canDelete
	fs.canDelete = fs.canDelete && spec.CanDelete
	listed := map[string]bool{}
	for _, n := range spec.Fields {
		listed[n] = true
		if n == fs.fkName {
			continue
		}
		fs.order = append(fs.order, n)
	}
	for _, n := range spec.ReadonlyFields {
		if !listed[n] {
			fs.order = append(fs.order, n)
		}
	}
	if parent != nil {
		if err := fs.loadExisting(); err != nil {
			return nil, err
		}
	}
	return fs, nil
}

func (fs *inlineFS) loadExisting() error {
	m := fs.m
	order := ""
	if len(fs.spec.Ordering) > 0 {
		_, order = orderSQL(m, fs.spec.Ordering, "t")
	} else if len(m.Ordering) > 0 {
		_, order = orderSQL(m, m.Ordering, "t")
	} else {
		order = ` ORDER BY t."` + m.pkField().Column + `"`
	}
	fkCol := fs.fkCol
	rows, err := fs.a.Store.Querier().Query(fs.r.c.Context(), "SELECT "+m.selectList("t")+" FROM "+quoteIdent(m.Table)+" t WHERE t.\""+fkCol+"\" = $1"+order, fs.parent.vals[fs.parent.m.PK])
	if err != nil {
		return err
	}
	fs.existing, err = fs.a.scanObjs(m, rows)
	return err
}

func (fs *inlineFS) newForm(prefix string) *aForm {
	order := append([]string{}, fs.order...)
	specs := fs.spec.FormFields
	// readonly fields are not part of the form
	var formOrder []string
	for _, n := range order {
		if fs.readonly[n] || !fs.canChange && fs.canView {
			continue
		}
		formOrder = append(formOrder, n)
	}
	f := fs.a.newForm(fs.r, fs.m, specs, formOrder, prefix)
	// the foreign key, the primary key and the delete flag
	if sp := specs[fs.fkName]; sp != nil {
		f.fields[fs.fkName] = newAField(fs.fkName, sp)
		f.fields[fs.fkName].hidden = true
	}
	f.inlineFK = fs.fkName
	f.noRequired = true
	return f
}

func (fs *inlineFS) formPrefix(i int) string { return fs.prefix + "-" + strconv.Itoa(i) }

// build prepares the forms: from the database when unbound, from the POST
// data when bound.
func (fs *inlineFS) build(bound bool, data url.Values, files map[string]*adminUpload) {
	fs.bound, fs.data, fs.files = bound, data, files
	initial := len(fs.existing)
	total := initial
	if bound {
		initial = fs.formsetInt(mgmtInitial, 0)
		total = fs.formsetInt(mgmtTotal, 0)
		if len(fs.mgmtErrors()) > 0 {
			initial, total = 0, 0
		}
		if total > 1000 {
			total = 1000
		}
		if initial > total {
			initial = total
		}
	} else {
		total = max(initial, fs.minNum)
		if initial > fs.maxNum {
			total = initial
		} else if total > fs.maxNum {
			total = fs.maxNum
		}
	}
	fs.nInitial, fs.total = initial, total
	fs.forms = nil
	byPK := map[string]*adminObj{}
	for _, o := range fs.existing {
		byPK[o.pkString()] = o
	}
	for i := 0; i < total; i++ {
		f := fs.newForm(fs.formPrefix(i))
		f.emptyOK = i >= initial && i >= fs.minNum
		if i < initial {
			var obj *adminObj
			if bound {
				obj = byPK[fs.data.Get(fs.formPrefix(i)+"-"+fs.m.PK)]
			} else if i < len(fs.existing) {
				obj = fs.existing[i]
			}
			if obj != nil {
				f.setInitialFromInstance(obj)
			}
		} else {
			f.setInitialDefaults()
		}
		f.inlineFKVal = nil
		if fs.parent != nil {
			f.inlineFKVal = fs.parent.vals[fs.parent.m.PK]
		}
		if bound {
			f.bind(fs.data, fs.files)
		}
		fs.forms = append(fs.forms, f)
	}
	fs.empty = fs.newForm(fs.prefix + "-__prefix__")
	fs.empty.setInitialDefaults()
	fs.empty.emptyOK = true
	if fs.parent != nil {
		fs.empty.inlineFKVal = fs.parent.vals[fs.parent.m.PK]
	}
}

func (fs *inlineFS) formsetInt(key string, def int) int {
	v := fs.data.Get(fs.prefix + "-" + key)
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return def
	}
	return n
}

func (fs *inlineFS) deleted(i int) bool {
	if !fs.canDelete || !fs.bound {
		return false
	}
	v := fs.data.Get(fs.formPrefix(i) + "-DELETE")
	return v != "" && strings.ToLower(v) != "false" && v != "0" && i < fs.nInitial
}

func (fs *inlineFS) deletedAny(i int) bool {
	if !fs.bound {
		return false
	}
	v := fs.data.Get(fs.formPrefix(i) + "-DELETE")
	return v != "" && strings.ToLower(v) != "false" && v != "0"
}

// validate runs every form (like formset.full_clean) and reports validity.
func (fs *inlineFS) validate() bool {
	valid := true
	fs.nonForm = nil
	if fs.bound {
		if errs := fs.mgmtErrors(); len(errs) > 0 {
			var names []string
			for _, e := range errs {
				n := strings.TrimSuffix(strings.TrimPrefix(strings.SplitN(e, ")", 2)[0], "(Hidden field "), "")
				names = append(names, fs.prefix+"-"+n)
			}
			fs.nonForm = append(fs.nonForm, "ManagementForm data is missing or has been tampered with. Missing fields: "+strings.Join(names, ", ")+". You may need to file a bug report if the issue persists.")
		}
	}
	for i, f := range fs.forms {
		if i < fs.nInitial {
			// a posted id the object list does not have
			if f.inst == nil && !fs.bound {
				continue
			}
		}
		f.cleaned = map[string]any{}
		f.isValid()
		if fs.canDelete && fs.deletedAny(i) {
			continue
		}
		if f.hasErrors() {
			valid = false
		}
	}
	if valid {
		count := 0
		for i, f := range fs.forms {
			if fs.canDelete && fs.deletedAny(i) {
				continue
			}
			if i < fs.nInitial || f.hasChanged() {
				count++
			}
		}
		if fs.minNum > 0 && count < fs.minNum {
			fs.nonForm = append(fs.nonForm, fmt.Sprintf("Please submit at least %d %s.", fs.minNum, plural(fs.minNum, "form", "forms")))
			valid = false
		}
		if count > fs.maxNum {
			fs.nonForm = append(fs.nonForm, fmt.Sprintf("Please submit at most %d %s.", fs.maxNum, plural(fs.maxNum, "form", "forms")))
			valid = false
		}
	}
	return valid
}

// --- saving ---------------------------------------------------------------------------------

type inlineChange struct {
	added   []*adminObj
	changed []changedObj
	deleted []*adminObj
}

type changedObj struct {
	obj    *adminObj
	fields []string // labels
}

// save is formset.save(): existing objects first, then the new ones.
func (fs *inlineFS) save(tx *App, parentPK any) (*inlineChange, error) {
	ch := &inlineChange{}
	for i, f := range fs.forms {
		if i >= fs.nInitial || f.inst == nil {
			continue
		}
		if fs.canDelete && fs.deletedAny(i) {
			if err := fs.a.deleteObjects(fs.r, []*adminObj{f.inst}); err != nil {
				return nil, err
			}
			ch.deleted = append(ch.deleted, f.inst)
			continue
		}
		if !f.hasChanged() {
			continue
		}
		labels := changedLabels(f)
		obj, err := f.saveInstance(fs.fkName, parentPK)
		if err != nil {
			return nil, err
		}
		ch.changed = append(ch.changed, changedObj{obj, labels})
	}
	for i, f := range fs.forms {
		if i < fs.nInitial && f.inst != nil {
			continue
		}
		if !f.hasChanged() {
			continue
		}
		if fs.canDelete && fs.deletedAny(i) {
			continue
		}
		obj, err := f.saveInstance(fs.fkName, parentPK)
		if err != nil {
			return nil, err
		}
		ch.added = append(ch.added, obj)
	}
	return ch, nil
}

func changedLabels(f *aForm) []string {
	var out []string
	for _, n := range f.changedFields() {
		if fl := f.fields[n]; fl != nil {
			out = append(out, fl.label)
		}
	}
	return out
}

// --- template data -----------------------------------------------------------------------

func (fs *inlineFS) verboseName() string       { return fs.spec.VerboseName }
func (fs *inlineFS) verboseNamePlural() string { return fs.spec.VerboseNamePl }

// mgmtErrors are the ManagementForm's own validation errors.
func (fs *inlineFS) mgmtErrors() []string {
	if !fs.bound {
		return nil
	}
	var errs []string
	for _, name := range []string{mgmtTotal, mgmtInitial, mgmtMin, mgmtMax} {
		v, present := fs.data[fs.prefix+"-"+name]
		required := name == mgmtTotal || name == mgmtInitial
		raw := ""
		if present && len(v) > 0 {
			raw = v[len(v)-1]
		}
		if strings.TrimSpace(raw) == "" {
			if required {
				errs = append(errs, "(Hidden field "+name+") "+msgRequired)
			}
			continue
		}
		if _, ok := pyIntBig(raw); !ok {
			errs = append(errs, "(Hidden field "+name+") Enter a whole number.")
		}
	}
	return errs
}

// managementForm is the formset's four hidden inputs.
func (fs *inlineFS) managementForm() string {
	total, initial := fs.total, fs.nInitial
	if !fs.bound && fs.parent == nil {
		total, initial = 0, 0
	}
	vals := [][2]string{{mgmtTotal, strconv.Itoa(total)}, {mgmtInitial, strconv.Itoa(initial)},
		{mgmtMin, strconv.Itoa(fs.minNum)}, {mgmtMax, strconv.Itoa(fs.maxNum)}}
	errs := fs.mgmtErrors()
	var b strings.Builder
	for _, kv := range vals {
		name := fs.prefix + "-" + kv[0]
		if fs.bound {
			// a bound management form shows what was submitted
			raw, present := fs.data[name]
			if !present || len(raw) == 0 {
				b.WriteString(`<input type="hidden" name="` + esc(name) + `" id="id_` + esc(name) + `">`)
				continue
			}
			b.WriteString(`<input type="hidden" name="` + esc(name) + `" value="` + esc(raw[len(raw)-1]) + `" id="id_` + esc(name) + `">`)
			continue
		}
		b.WriteString(`<input type="hidden" name="` + esc(name) + `" value="` + esc(kv[1]) + `" id="id_` + esc(name) + `">`)
	}
	if len(errs) > 0 {
		return strings.Replace(errorList(errs), `class="errorlist"`, `class="errorlist nonfield"`, 1) + "\n<div>" + b.String() + "</div>"
	}
	return b.String()
}

func (fs *inlineFS) formsetData() string {
	s, _ := pyjson.Dumps(map[string]any{})
	_ = s
	type opts struct {
		Prefix     string `json:"prefix"`
		AddText    string `json:"addText"`
		DeleteText string `json:"deleteText"`
	}
	// the key order Django's json.dumps writes
	v := fmt.Sprintf(`{"name": "#%s", "options": {"prefix": "%s", "addText": "Add another %s", "deleteText": "Remove"}}`,
		fs.prefix, fs.prefix, jsonEscape(capfirst(fs.spec.VerboseName)))
	return v
}

func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	return strings.Trim(string(b), `"`)
}

func (fs *inlineFS) hasErrors() bool {
	if len(fs.nonForm) > 0 {
		return true
	}
	for i, f := range fs.forms {
		if fs.canDelete && fs.deletedAny(i) {
			continue
		}
		if f.hasErrors() {
			return true
		}
	}
	return false
}

// errorLists counts the error lists AdminErrorList collects from the formset.
func (fs *inlineFS) errorCount() int {
	n := 0
	if len(fs.nonForm) > 0 {
		n++
	}
	for i, f := range fs.forms {
		if fs.canDelete && fs.deletedAny(i) {
			continue
		}
		n += f.errorLists()
	}
	return n
}

var _ = sort.Strings

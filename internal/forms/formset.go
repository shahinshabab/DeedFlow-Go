package forms

import (
	"context"
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// RowStore is what a formset needs to save its rows.
type RowStore interface {
	InsertRow(ctx context.Context, table string, row any) error
	UpdateRow(ctx context.Context, table string, row any, cols ...string) error
	DeleteRows(ctx context.Context, table string, deedID int64, ids ...int64) error
	SetPosition(ctx context.Context, table string, id int64, position int) error
}

// RowForm is one form of a formset.
type RowForm struct {
	*Form
	Row   any // pointer to the model row (a zero row for a new entry)
	Index int
	PK    int64
	// idErr is an error on the hidden id field (tampered post data).
	idErr string
}

// FormSet is an inline formset (Django's inlineformset_factory with the
// ordering/page-order extension): the rows of a deed's repeatable section.
type FormSet struct {
	Prefix      string
	Table       string
	ParentID    int64
	ParentField string // "deed"
	Bound       bool
	Data        url.Values

	Existing []any // pointers to the saved rows, in order
	NewRow   func() any
	// Build declares the form fields of a row; prefix is the form's.
	Build func(prefix string, row any) *Form
	// Finish adjusts a form once it is bound and has its initial values
	// (dependent choices that read what is currently chosen).
	Finish func(f *Form, row any)

	Extra, MinNum, MaxNum int
	ValidateMin           bool
	CanDelete             bool
	// Clean is the formset-level clean, run after the forms validated.
	Clean func(fs *FormSet)

	Forms         []*RowForm
	initialCount  int
	nonFormErrors []string
	cleanErrors   []string
	validated     bool
}

const maxFormsDefault = 1000

// Init builds the forms (call after setting the fields above).
func (fs *FormSet) Init() *FormSet {
	if fs.MaxNum == 0 {
		fs.MaxNum = maxFormsDefault
	}
	var total int
	if fs.Bound {
		t, errT := fs.mgmtInt("TOTAL_FORMS")
		i, errI := fs.mgmtInt("INITIAL_FORMS")
		if errT != nil || errI != nil {
			var missing []string
			if errT != nil {
				missing = append(missing, fs.Prefix+"-TOTAL_FORMS")
			}
			if errI != nil {
				missing = append(missing, fs.Prefix+"-INITIAL_FORMS")
			}
			fs.nonFormErrors = append(fs.nonFormErrors, "ManagementForm data is missing or has been tampered with. Missing fields: "+
				strings.Join(missing, ", ")+". You may need to file a bug report if the issue persists.")
			t, i = 0, 0
		}
		total, fs.initialCount = min(t, fs.MaxNum), i
	} else {
		fs.initialCount = len(fs.Existing)
		total = max(fs.initialCount, fs.MinNum) + fs.Extra
		if fs.initialCount > fs.MaxNum {
			total = fs.initialCount
		} else if total > fs.MaxNum {
			total = fs.MaxNum
		}
	}
	for i := 0; i < total; i++ {
		fs.Forms = append(fs.Forms, fs.construct(i))
	}
	return fs
}

func (fs *FormSet) mgmtInt(name string) (int, error) {
	raw := fs.Data.Get(fs.Prefix + "-" + name)
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 0 {
		return 0, fmt.Errorf("bad %s", name)
	}
	return n, nil
}

func (fs *FormSet) construct(i int) *RowForm {
	prefix := fmt.Sprintf("%s-%d", fs.Prefix, i)
	var row any
	var idErr string
	switch {
	case i < fs.initialCount && !fs.Bound:
		row = fs.Existing[i]
	case i < fs.initialCount:
		raw, ok := fs.Data[prefix+"-id"]
		if !ok || len(raw) == 0 || strings.TrimSpace(raw[0]) == "" {
			idErr = "This field is required."
			break
		}
		pk, err := strconv.ParseInt(strings.TrimSpace(raw[0]), 10, 64)
		if err != nil {
			idErr = "Select a valid choice. That choice is not one of the available choices."
			break
		}
		for _, e := range fs.Existing {
			if PKOf(e) == pk {
				row = e
			}
		}
		if row == nil {
			idErr = "Select a valid choice. That choice is not one of the available choices."
		}
	}
	if row == nil {
		row = fs.NewRow()
	}
	f := fs.Build(prefix, row)
	f.NoRequiredAttr = true
	f.Prefix = prefix
	if fs.Bound {
		f.Bind(fs.Data)
	}
	pk := PKOf(row)
	if pk != 0 {
		f.InitialFromRow(row)
	}
	f.EmptyPermitted = i >= fs.initialCount && i >= fs.MinNum
	if fs.Finish != nil {
		fs.Finish(f, row)
	}
	// Django's hidden extras: the row's id and the foreign key, then DELETE.
	idField := &Field{Name: "id", Label: "Id", Kind: Char, Widget: HiddenInput, Required: i < fs.initialCount}
	f.Fields = append(f.Fields, idField)
	if pk != 0 {
		f.Initial["id"] = strconv.FormatInt(pk, 10)
	}
	if fs.CanDelete {
		f.Fields = append(f.Fields, &Field{Name: "DELETE", Label: "Delete", Kind: Bool, Widget: Checkbox})
	}
	if fs.ParentField != "" {
		f.Fields = append(f.Fields, &Field{Name: fs.ParentField, Label: label(fs.ParentField), Kind: Char, Widget: HiddenInput, IgnoreChange: true})
		f.Initial[fs.ParentField] = strconv.FormatInt(fs.ParentID, 10)
	}
	return &RowForm{Form: f, Row: row, Index: i, PK: pk, idErr: idErr}
}

// InitialForms are the forms of saved rows.
func (fs *FormSet) InitialForms() []*RowForm {
	if fs.initialCount > len(fs.Forms) {
		return fs.Forms
	}
	return fs.Forms[:fs.initialCount]
}

func (fs *FormSet) shouldDelete(rf *RowForm) bool {
	if !fs.CanDelete {
		return false
	}
	rf.fullCleanOnce(fs)
	v, _ := rf.Cleaned["DELETE"].(bool)
	return v
}

func (rf *RowForm) fullCleanOnce(fs *FormSet) {
	if !rf.Bound {
		return
	}
	rf.IsValid()
	if rf.idErr != "" && len(rf.Errors["id"]) == 0 {
		rf.Errors["id"] = []string{rf.idErr}
	}
}

// DeletedForms are the forms the user ticked "Remove" on (none while the
// formset is invalid, as in Django).
func (fs *FormSet) DeletedForms() []*RowForm {
	if !fs.IsValid() {
		return nil
	}
	return fs.deletedRaw()
}

func (fs *FormSet) deletedRaw() []*RowForm {
	var out []*RowForm
	if !fs.Bound || !fs.CanDelete {
		return out
	}
	for i, rf := range fs.Forms {
		if i >= fs.initialCount && !rf.HasChanged() {
			continue
		}
		if fs.shouldDelete(rf) {
			out = append(out, rf)
		}
	}
	return out
}

func (fs *FormSet) isDeleted(rf *RowForm) bool {
	for _, d := range fs.deletedRaw() {
		if d == rf {
			return true
		}
	}
	return false
}

// HasFormErrors reports whether any non-deleted form has errors (`any(self.errors)`).
func (fs *FormSet) HasFormErrors() bool {
	for _, rf := range fs.Forms {
		rf.fullCleanOnce(fs)
		if fs.CanDelete && fs.shouldDelete(rf) {
			continue
		}
		if rf.HasErrors() {
			return true
		}
	}
	return false
}

// AddNonFormError records a formset-level validation error (from Clean).
func (fs *FormSet) AddNonFormError(msg string) { fs.cleanErrors = append(fs.cleanErrors, msg) }

func (fs *FormSet) fullClean() {
	if fs.validated || !fs.Bound {
		return
	}
	fs.validated = true
	empty := 0
	for i, rf := range fs.Forms {
		if i >= fs.initialCount && !rf.HasChanged() {
			empty++
		}
		rf.fullCleanOnce(fs)
	}
	// Like Django, an error from the min check or clean() replaces what
	// was collected so far (a missing management form's message).
	if fs.ValidateMin && len(fs.Forms)-len(fs.deletedRaw())-empty < fs.MinNum {
		word := "forms"
		if fs.MinNum == 1 {
			word = "form"
		}
		fs.nonFormErrors = []string{fmt.Sprintf("Please submit at least %d %s.", fs.MinNum, word)}
	} else if fs.Clean != nil {
		fs.cleanErrors = nil
		fs.Clean(fs)
		if len(fs.cleanErrors) > 0 {
			fs.nonFormErrors = fs.cleanErrors
		}
	}
}

// IsValid validates every form that isn't being removed, and the set.
func (fs *FormSet) IsValid() bool {
	if !fs.Bound {
		return false
	}
	fs.fullClean()
	for _, rf := range fs.Forms {
		if fs.CanDelete && fs.shouldDelete(rf) {
			continue
		}
		if !rf.IsValid() || rf.idErr != "" {
			return false
		}
	}
	return len(fs.nonFormErrors) == 0
}

// NonFormErrors are the formset-level errors.
func (fs *FormSet) NonFormErrors() []string {
	fs.fullClean()
	return fs.nonFormErrors
}

// PageOrder is the form indices in the order the page posted them ("<prefix>-ROWS").
func (fs *FormSet) PageOrder() []int {
	if !fs.Bound {
		return nil
	}
	var out []int
	for _, part := range strings.Split(fs.Data.Get(fs.Prefix+"-ROWS"), ",") {
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil
		}
		out = append(out, n)
	}
	return out
}

// FormsInPageOrder is the forms as the page arranged them.
func (fs *FormSet) FormsInPageOrder() []*RowForm {
	order := fs.PageOrder()
	if len(order) == 0 {
		return fs.Forms
	}
	rank := map[int]int{}
	for n, idx := range order {
		if _, seen := rank[idx]; !seen {
			rank[idx] = n
		}
	}
	idx := make([]int, len(fs.Forms))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		ra, ok := rank[idx[a]]
		if !ok {
			ra = len(rank)
		}
		rb, ok := rank[idx[b]]
		if !ok {
			rb = len(rank)
		}
		if ra != rb {
			return ra < rb
		}
		return idx[a] < idx[b]
	})
	out := make([]*RowForm, len(idx))
	for n, i := range idx {
		out[n] = fs.Forms[i]
	}
	return out
}

// Save writes the rows: changed saved rows are updated, removed ones deleted,
// new changed ones inserted, then every kept row is numbered in page order.
func (fs *FormSet) Save(ctx context.Context, st RowStore) error {
	deleted := map[*RowForm]bool{}
	for _, rf := range fs.DeletedForms() {
		deleted[rf] = true
	}
	for _, rf := range fs.InitialForms() {
		if rf.PK == 0 {
			continue
		}
		switch {
		case deleted[rf]:
			if err := st.DeleteRows(ctx, fs.Table, fs.ParentID, rf.PK); err != nil {
				return err
			}
		case rf.HasChanged():
			if err := rf.ApplyToRow(rf.Row); err != nil {
				return err
			}
			if err := st.UpdateRow(ctx, fs.Table, rf.Row); err != nil {
				return err
			}
		}
	}
	for i := fs.initialCount; i < len(fs.Forms); i++ {
		rf := fs.Forms[i]
		if !rf.HasChanged() || deleted[rf] {
			continue
		}
		if err := rf.ApplyToRow(rf.Row); err != nil {
			return err
		}
		setColumn(rf.Row, "deed_id", fs.ParentID)
		if err := st.InsertRow(ctx, fs.Table, rf.Row); err != nil {
			return err
		}
		rf.PK = PKOf(rf.Row)
	}
	// Unchanged rows aren't saved above, so their new places go in here.
	position := 0
	for _, rf := range fs.FormsInPageOrder() {
		if deleted[rf] || PKOf(rf.Row) == 0 {
			continue
		}
		if ColumnInt(rf.Row, "position") != int64(position) {
			SetColumnInt(rf.Row, "position", int64(position))
			if err := st.SetPosition(ctx, fs.Table, PKOf(rf.Row), position); err != nil {
				return err
			}
		}
		position++
	}
	return nil
}

// ManagementHTML renders the four hidden management inputs: the posted
// values when bound (a missing one has no value), else the formset's own.
func (fs *FormSet) ManagementHTML() string {
	var b strings.Builder
	for _, f := range []struct {
		name string
		val  int
	}{{"TOTAL_FORMS", len(fs.Forms)}, {"INITIAL_FORMS", fs.initialCount}, {"MIN_NUM_FORMS", fs.MinNum}, {"MAX_NUM_FORMS", fs.MaxNum}} {
		key := fs.Prefix + "-" + f.name
		value := strconv.Itoa(f.val)
		if fs.Bound {
			value = fs.Data.Get(key)
		}
		fmt.Fprintf(&b, `<input type="hidden" name="%s"`, esc(key))
		if value != "" {
			fmt.Fprintf(&b, ` value="%s"`, esc(value))
		}
		fmt.Fprintf(&b, ` id="id_%s">`, esc(key))
	}
	return b.String()
}

// EmptyForm is the blank form with a __prefix__ index, the page clones for "Add".
func (fs *FormSet) EmptyForm() *RowForm {
	save := fs.initialCount
	fs.initialCount = 0
	rf := fs.construct(0)
	fs.initialCount = save
	prefix := fs.Prefix + "-__prefix__"
	rf.Prefix = prefix
	rf.Form.Prefix = prefix
	rf.Bound = false
	rf.EmptyPermitted = true
	return rf
}

// Template converts one row form for the templates.
func (rf *RowForm) Template() map[string]any {
	m := rf.Form.Template()
	var pk any
	if rf.PK != 0 {
		pk = rf.PK
	}
	m["instance"] = map[string]any{"pk": pk}
	if rf.idErr != "" {
		m["non_field_errors"] = append(append([]string{}, rf.NonFieldErrors...), "(Hidden field id) "+rf.idErr)
	}
	return m
}

// Template converts the formset for the templates.
func (fs *FormSet) Template() map[string]any {
	var inOrder []map[string]any
	for _, rf := range fs.FormsInPageOrder() {
		inOrder = append(inOrder, rf.Template())
	}
	forms := make([]map[string]any, len(fs.Forms))
	for i, rf := range fs.Forms {
		forms[i] = rf.Template()
	}
	return map[string]any{
		"management_form":     fs.ManagementHTML(),
		"non_form_errors":     fs.NonFormErrors(),
		"prefix":              fs.Prefix,
		"forms":               forms,
		"forms_in_page_order": inOrder,
		"empty_form":          fs.EmptyForm().Template(),
		"is_formset":          true,
	}
}

// SavedData is the post data that reproduces the saved rows (the wizard's
// "rows as saved, ids included, rather than as posted"), with the blank extra
// entries left out.
func (fs *FormSet) SavedData() url.Values {
	data := url.Values{}
	p := fs.Prefix
	n := len(fs.InitialForms())
	data.Set(p+"-TOTAL_FORMS", strconv.Itoa(n))
	data.Set(p+"-INITIAL_FORMS", strconv.Itoa(n))
	data.Set(p+"-MIN_NUM_FORMS", strconv.Itoa(fs.MinNum))
	data.Set(p+"-MAX_NUM_FORMS", strconv.Itoa(fs.MaxNum))
	for _, rf := range fs.InitialForms() {
		for _, fl := range rf.Fields {
			if fl.Name == "DELETE" {
				continue
			}
			v := rf.Value(fl.Name)
			if fl.Widget == Checkbox {
				if v != "" {
					data.Set(rf.AddPrefix(fl.Name), "on")
				}
				continue
			}
			if v != "" {
				data.Set(rf.AddPrefix(fl.Name), v)
			}
		}
	}
	return data
}

var _ = reflect.TypeOf

func label(name string) string {
	if name == "" {
		return ""
	}
	return strings.ToUpper(name[:1]) + strings.ReplaceAll(name[1:], "_", " ")
}

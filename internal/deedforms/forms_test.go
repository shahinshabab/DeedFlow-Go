package deedforms

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	nethtml "golang.org/x/net/html"

	"github.com/shahinshabab/deedflow-go/internal/db"
	"github.com/shahinshabab/deedflow-go/internal/deeds"
	"github.com/shahinshabab/deedflow-go/internal/forms"
	"github.com/shahinshabab/deedflow-go/internal/models"
	"github.com/shahinshabab/deedflow-go/internal/store"
	"github.com/shahinshabab/deedflow-go/internal/testdb"
)

type elem struct {
	Tag     string         `json:"tag"`
	Attrs   map[string]any `json:"attrs"`
	Options []option       `json:"options,omitempty"`
	Text    *string        `json:"text,omitempty"`
}

type option struct {
	Value    *string `json:"value"`
	Selected bool    `json:"selected"`
	Text     string  `json:"text"`
}

type fieldN struct {
	Name     string   `json:"name"`
	Label    string   `json:"label"`
	Required bool     `json:"required"`
	Help     string   `json:"help_text"`
	Errors   []string `json:"errors"`
	Hidden   bool     `json:"hidden"`
	HTML     elem     `json:"html"`
}

type formN struct {
	Fields []fieldN `json:"fields"`
	NFE    []string `json:"non_field_errors"`
	Valid  *bool    `json:"valid"`
}

type setN struct {
	Management    [][]string `json:"management"`
	NonFormErrors []string   `json:"non_form_errors"`
	Forms         []formN    `json:"forms"`
	Order         []string   `json:"order"`
	Empty         formN      `json:"empty"`
	Valid         *bool      `json:"valid"`
	Deleted       []string   `json:"deleted"`
	Changed       []bool     `json:"changed"`
}

type caseT struct {
	Name           string
	Section        string
	Rows           []map[string]any
	Post           map[string]string
	Dropdowns      bool
	Token          string
	MinValue       *int64 `json:"min_value"`
	DocValue       *int64 `json:"doc_value"`
	WizardDefaults bool   `json:"wizard_defaults"`
	Locked         bool
	Expected       json.RawMessage
}

func parseElem(s string) elem {
	e := elem{Attrs: map[string]any{}}
	doc, err := nethtml.Parse(strings.NewReader("<body>" + s + "</body>"))
	if err != nil {
		return e
	}
	var walk func(n *nethtml.Node)
	var cur *option
	txt := ""
	found := false
	walk = func(n *nethtml.Node) {
		if n.Type == nethtml.ElementNode {
			switch n.Data {
			case "input", "select", "textarea":
				if !found {
					found = true
					e.Tag = n.Data
					for _, a := range n.Attr {
						if a.Val == "" || a.Val == a.Key {
							e.Attrs[a.Key] = true
						} else {
							e.Attrs[a.Key] = a.Val
						}
					}
				}
			case "option":
				o := option{}
				for _, a := range n.Attr {
					if a.Key == "value" {
						v := a.Val
						o.Value = &v
					}
					if a.Key == "selected" {
						o.Selected = true
					}
				}
				cur = &o
			}
		}
		if n.Type == nethtml.TextNode {
			if cur != nil {
				cur.Text += n.Data
			} else if e.Tag == "textarea" {
				txt += n.Data
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == nethtml.ElementNode && n.Data == "option" && cur != nil {
			cur.Text = strings.TrimSpace(cur.Text)
			e.Options = append(e.Options, *cur)
			cur = nil
		}
	}
	walk(doc)
	if e.Tag == "textarea" {
		t := strings.TrimPrefix(txt, "\n")
		e.Text = &t
	}
	return e
}

func normForm(f *forms.Form) formN {
	m := f.Template()
	n := formN{}
	var all []map[string]any
	// Django iterates the fields in declared order, hidden ones included.
	byName, _ := m["fields"].(map[string]any)
	for _, fl := range f.Fields {
		all = append(all, byName[fl.Name].(map[string]any))
	}
	for _, bf := range all {
		errs, _ := bf["errors"].([]string)
		if errs == nil {
			errs = []string{}
		}
		n.Fields = append(n.Fields, fieldN{
			Name: bf["name"].(string), Label: bf["label"].(string),
			Required: bf["required"].(bool), Help: bf["help_text"].(string), Errors: errs,
			Hidden: bf["is_hidden"].(bool), HTML: parseElem(bf["html"].(string)),
		})
	}
	nfe := f.NonFieldErrors
	if f.Bound {
		f.IsValid()
		nfe = f.NonFieldErrors
	}
	if nfe == nil {
		nfe = []string{}
	}
	n.NFE = nfe
	if f.Bound {
		v := f.IsValid()
		n.Valid = &v
	}
	return n
}

func normSet(fs *forms.FormSet) setN {
	s := setN{}
	re := regexp.MustCompile(`name="([^"]+)" value="([^"]*)"`)
	for _, m := range re.FindAllStringSubmatch(fs.ManagementHTML(), -1) {
		s.Management = append(s.Management, []string{m[1], m[2]})
	}
	s.NonFormErrors = fs.NonFormErrors()
	if s.NonFormErrors == nil {
		s.NonFormErrors = []string{}
	}
	for _, rf := range fs.Forms {
		s.Forms = append(s.Forms, normForm(rf.Form))
	}
	for _, rf := range fs.FormsInPageOrder() {
		s.Order = append(s.Order, rf.Prefix)
	}
	s.Empty = normForm(fs.EmptyForm().Form)
	if fs.Bound {
		v := fs.IsValid()
		s.Valid = &v
		s.Deleted = []string{}
		for _, rf := range fs.DeletedForms() {
			s.Deleted = append(s.Deleted, rf.Prefix)
		}
		for _, rf := range fs.Forms {
			s.Changed = append(s.Changed, rf.HasChanged())
		}
	}
	return s
}

// --- helpers to insert rows from the oracle's field dicts -----------------------

func assign(row any, fields map[string]any) error {
	rv := reflect.ValueOf(row).Elem()
	t := rv.Type()
	for k, v := range fields {
		found := false
		for i := 0; i < t.NumField(); i++ {
			if t.Field(i).Tag.Get("db") != k {
				continue
			}
			found = true
			f := rv.Field(i)
			if v == nil {
				continue
			}
			if err := setAny(f, v); err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
		}
		if !found {
			return fmt.Errorf("no column %s on %T", k, row)
		}
	}
	return nil
}

func setAny(f reflect.Value, v any) error {
	for f.Kind() == reflect.Ptr {
		if f.IsNil() {
			f.Set(reflect.New(f.Type().Elem()))
		}
		f = f.Elem()
	}
	switch f.Type() {
	case reflect.TypeOf(db.Decimal{}):
		d, err := db.ParseDecimal(fmt.Sprint(v))
		if err != nil {
			return err
		}
		f.Set(reflect.ValueOf(d))
		return nil
	case reflect.TypeOf(time.Time{}):
		t, err := time.Parse("2006-01-02", fmt.Sprint(v))
		if err != nil {
			return err
		}
		f.Set(reflect.ValueOf(t))
		return nil
	}
	switch f.Kind() {
	case reflect.String:
		f.SetString(fmt.Sprint(v))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		switch x := v.(type) {
		case float64:
			f.SetInt(int64(x))
		case string:
			n, _ := strconv.ParseInt(x, 10, 64)
			f.SetInt(n)
		}
	case reflect.Bool:
		f.SetBool(v.(bool))
	default:
		return fmt.Errorf("unsupported %s", f.Type())
	}
	return nil
}

func testStore(t *testing.T) (*store.Store, *db.DB) {
	d := testdb.Open(t)
	return store.New(d), d
}

func stripNulls(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if e == nil {
				delete(x, k)
			} else {
				x[k] = stripNulls(e)
			}
		}
	case []any:
		for i := range x {
			x[i] = stripNulls(x[i])
		}
	}
	if m, ok := v.(map[string]any); ok {
		for k, e := range m {
			if l, ok := e.([]any); ok && len(l) == 0 {
				delete(m, k)
			}
		}
	}
	return v
}

func diffJSON(t *testing.T, name string, got any, want json.RawMessage) {
	t.Helper()
	gj, _ := json.Marshal(got)
	var g, w any
	_ = json.Unmarshal(gj, &g)
	_ = json.Unmarshal(want, &w)
	g, w = stripNulls(g), stripNulls(w)
	if reflect.DeepEqual(g, w) {
		return
	}
	var lines []string
	diffValue("", g, w, &lines)
	if len(lines) > 12 {
		lines = append(lines[:12], fmt.Sprintf("... %d more", len(lines)-12))
	}
	t.Errorf("%s differs:\n  %s", name, strings.Join(lines, "\n  "))
}

func diffValue(path string, g, w any, out *[]string) {
	switch gv := g.(type) {
	case map[string]any:
		wv, ok := w.(map[string]any)
		if !ok {
			*out = append(*out, fmt.Sprintf("%s: got object, want %v", path, w))
			return
		}
		keys := map[string]bool{}
		for k := range gv {
			keys[k] = true
		}
		for k := range wv {
			keys[k] = true
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k := range sorted {
			diffValue(path+"."+k, gv[k], wv[k], out)
		}
	case []any:
		wv, ok := w.([]any)
		if !ok {
			*out = append(*out, fmt.Sprintf("%s: got list %v, want %v", path, g, w))
			return
		}
		if len(gv) != len(wv) {
			*out = append(*out, fmt.Sprintf("%s: got %d items %v, want %d %v", path, len(gv), g, len(wv), w))
			return
		}
		for i := range gv {
			diffValue(fmt.Sprintf("%s[%d]", path, i), gv[i], wv[i], out)
		}
	default:
		if !reflect.DeepEqual(g, w) {
			*out = append(*out, fmt.Sprintf("%s: got %v, want %v", path, g, w))
		}
	}
}

func TestAgainstDjango(t *testing.T) {
	raw, err := os.ReadFile("testdata/forms_cases.json")
	if err != nil {
		t.Skip("no golden data")
	}
	var data struct {
		Today     string
		Dropdowns []struct {
			Page, Field string
			Parents     [][]string
			Options     [][]string
		} `json:"-"`
		RawDropdowns [][]json.RawMessage `json:"dropdowns"`
		Cases        []caseT
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	st, d := testStore(t)
	ctx := context.Background()
	today, _ := time.Parse("2006-01-02", data.Today)

	user := &models.User{Username: "gofm-" + store.NewUUID()[:8], IsActive: true}
	if err := st.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Pool.Exec(ctx, `DELETE FROM auth_user WHERE id = $1`, user.ID) })

	setDropdowns := func(on bool) {
		_, _ = d.Pool.Exec(ctx, `DELETE FROM deeds_portaldropdown`)
		if !on {
			return
		}
		var rows []models.PortalDropdown
		for _, r := range data.RawDropdowns {
			var page, field string
			var parents [][]string
			var options [][]string
			_ = json.Unmarshal(r[0], &page)
			_ = json.Unmarshal(r[1], &field)
			_ = json.Unmarshal(r[2], &parents)
			_ = json.Unmarshal(r[3], &options)
			pj, _ := json.Marshal(parents)
			oj, _ := json.Marshal(options)
			fields := make([]string, len(parents))
			texts := make([]string, len(parents))
			for i, p := range parents {
				fields[i], texts[i] = p[0], p[2]
			}
			rows = append(rows, models.PortalDropdown{Page: page, Field: field, Label: field, Parents: pj,
				ParentKey: deeds.MakeParentKey(fields, texts), Options: oj, ScrapedAt: time.Now()})
		}
		pages := map[string][]models.PortalDropdown{}
		for _, r := range rows {
			pages[r.Page] = append(pages[r.Page], r)
		}
		for page, rs := range pages {
			if err := st.ReplacePortalDropdowns(ctx, page, rs); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Cleanup(func() { setDropdowns(false) })

	for _, c := range data.Cases {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			setDropdowns(c.Dropdowns)
			deed := &models.Deed{OwnerID: user.ID, Status: models.StatusDraft, TokenNumber: c.Token}
			if err := st.CreateDeed(ctx, deed); err != nil {
				t.Fatal(err)
			}
			defer d.Pool.Exec(ctx, `DELETE FROM deeds_deed WHERE id = $1`, deed.ID)
			b := &Builder{Ctx: ctx, DD: &deeds.Dropdowns{Src: st}, Today: func() time.Time { return today }}

			ids := map[string]string{"{DEED}": strconv.FormatInt(deed.ID, 10)}
			subst := func(s string) string {
				for k, v := range ids {
					s = strings.ReplaceAll(s, k, v)
				}
				return s
			}
			var post url.Values
			expected := string(c.Expected)

			if c.Section == "registration" || c.Section == "presenter" {
				var form *forms.Form
				if c.Post != nil {
					post = url.Values{}
					for k, v := range c.Post {
						post.Set(k, subst(v))
					}
				}
				if c.Section == "registration" {
					var row *models.RegistrationDetail
					if len(c.Rows) > 0 {
						row = &models.RegistrationDetail{DeedID: deed.ID}
						if err := assign(row, c.Rows[0]); err != nil {
							t.Fatal(err)
						}
						if err := st.SaveRegistration(ctx, row); err != nil {
							t.Fatal(err)
						}
					}
					form = b.Registration("registration", row, RegistrationOptions{Data: post, Locked: c.Locked})
				} else {
					var row *models.Presenter
					if len(c.Rows) > 0 {
						row = &models.Presenter{DeedID: deed.ID}
						if err := assign(row, c.Rows[0]); err != nil {
							t.Fatal(err)
						}
						if err := st.SavePresenter(ctx, row); err != nil {
							t.Fatal(err)
						}
					}
					var min int64
					if c.MinValue != nil {
						min = *c.MinValue
					}
					form = b.Presenter("presenter", row, post, min)
					if post == nil && c.WizardDefaults {
						b.PresenterDefaults(form, row)
					}
				}
				if c.Locked && c.Post != nil {
					t.Skip("locked + posted differs on purpose (Django's disabled-field bug)")
				}
				diffJSON(t, c.Name, normForm(form), json.RawMessage(expected))
				return
			}

			// repeatable section
			var existing []any
			rowsOf := func() []any {
				data, err := st.LoadDeedData(ctx, deed)
				if err != nil {
					t.Fatal(err)
				}
				switch c.Section {
				case "documents":
					return Rows(data.PriorDocuments)
				case "executants":
					return Rows(data.Executants)
				case "claimants":
					return Rows(data.Claimants)
				case "schedule":
					return Rows(data.Schedule)
				default:
					return Rows(data.Witnesses)
				}
			}
			for i, r := range c.Rows {
				var row any
				switch c.Section {
				case "documents":
					row = &models.PriorDocument{DeedID: deed.ID}
				case "executants", "claimants":
					row = &models.Party{DeedID: deed.ID}
				case "schedule":
					row = &models.ScheduleItem{DeedID: deed.ID}
				default:
					row = &models.Witness{DeedID: deed.ID}
				}
				if err := assign(row, r); err != nil {
					t.Fatal(err)
				}
				if err := st.InsertRow(ctx, tableFor(c.Section), row); err != nil {
					t.Fatal(err)
				}
				ids[fmt.Sprintf("{ROW%d}", i)] = strconv.FormatInt(forms.PKOf(row), 10)
			}
			existing = rowsOf()
			if c.Post != nil {
				post = url.Values{}
				for k, v := range c.Post {
					post.Set(k, subst(v))
				}
			}
			var opt FormSetOptions
			opt.DocumentValue = c.DocValue
			fs := b.SectionFormSet(c.Section, deed.ID, existing, post, opt)
			got := normSet(fs)
			// Row ids differ run to run: swap them for tokens in the hidden id / deed inputs.
			inv := map[string]string{}
			for k, v := range ids {
				inv[v] = k
			}
			tokenize := func(fn *formN) {
				for i := range fn.Fields {
					if fn.Fields[i].Name == "id" || fn.Fields[i].Name == "deed" {
						if v, ok := fn.Fields[i].HTML.Attrs["value"].(string); ok {
							if tok, ok := inv[v]; ok {
								fn.Fields[i].HTML.Attrs["value"] = tok
							}
						}
					}
				}
			}
			for i := range got.Forms {
				tokenize(&got.Forms[i])
			}
			tokenize(&got.Empty)
			gj, _ := json.Marshal(got)
			gs := string(gj)
			diffJSON(t, c.Name, json.RawMessage(gs), json.RawMessage(expected))
		})
	}
}

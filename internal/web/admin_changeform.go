package web

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/flosch/pongo2/v6"

	"github.com/shahinshabab/deedflow-go/internal/store"
)

// The add and change pages of a model: one form, the inline formsets below
// it, saving, the log entry and the redirect (ModelAdmin.changeform_view).

//go:embed admin_media.json
var adminMediaJSON []byte

var adminMediaTable = func() map[string]string {
	m := map[string]string{}
	_ = json.Unmarshal(adminMediaJSON, &m)
	return m
}()

// cfView is one add/change request.
type cfView struct {
	r        *adminReq
	a        *App
	m        *modelSpec
	obj      *adminObj
	add      bool
	form     *aForm
	inlines  []*inlineFS
	readonly map[string]bool
	fsets    []adminFieldset
	hooks    cfHooks
}

// cfHooks lets the user admin plug its forms in.
type cfHooks struct {
	newForm func(v *cfView) *aForm
	after   func(v *cfView)
}

func (a *App) adminForbiddenPage(c *Ctx) {
	c.W.Header().Set("Content-Type", "text/html; charset=utf-8")
	c.W.WriteHeader(http.StatusForbidden)
	_, _ = c.W.Write([]byte("<!doctype html>\n<html lang=\"en\">\n<head>\n  <title>403 Forbidden</title>\n</head>\n<body>\n  <h1>403 Forbidden</h1><p></p>\n</body>\n</html>\n"))
}

func (a *App) adminBadRequest(c *Ctx) {
	c.W.Header().Set("Content-Type", "text/html; charset=utf-8")
	c.W.WriteHeader(http.StatusBadRequest)
	_, _ = c.W.Write([]byte("<!doctype html>\n<html lang=\"en\">\n<head>\n  <title>400 Bad Request</title>\n</head>\n<body>\n  <h1>400 Bad Request</h1><p></p>\n</body>\n</html>\n"))
}

// adminChangeForm is changeform_view.
func (a *App) adminChangeForm(r *adminReq, m *modelSpec, objectID string) {
	c := r.c
	isPost := c.R.Method == http.MethodPost
	var pf url.Values
	if isPost {
		pf = c.PostForm()
	}
	toField := c.R.URL.Query().Get("_to_field")
	if isPost && pf.Get("_to_field") != "" {
		toField = pf.Get("_to_field")
	}
	if toField != "" && toField != m.PK && toField != "pk" {
		a.adminBadRequest(c)
		return
	}
	if isPost && hasKeyValues(pf, "_saveasnew") {
		objectID = ""
	}
	v := &cfView{r: r, a: a, m: m, add: objectID == "", readonly: map[string]bool{}}
	if v.add {
		if !r.perms.has(m, "add") {
			a.adminForbiddenPage(c)
			return
		}
	} else {
		obj, err := a.loadObj(c.Context(), m, objectID)
		if err != nil {
			c.ServerError(err)
			return
		}
		if isPost {
			if !r.perms.has(m, "change") {
				a.adminForbiddenPage(c)
				return
			}
		} else if !r.perms.has(m, "view") && !r.perms.has(m, "change") {
			a.adminForbiddenPage(c)
			return
		}
		if obj == nil {
			a.objectMissing(r, m, objectID)
			return
		}
		v.obj = obj
	}
	if v.add {
		v.fsets = m.fieldsetsAdd
	} else {
		v.fsets = m.fieldsetsChange
	}
	canChange := v.add || r.perms.has(m, "change")
	if !v.add && !canChange {
		for _, fs := range v.fsets {
			for _, row := range fs.Rows {
				for _, n := range row {
					v.readonly[n] = true
				}
			}
		}
	} else {
		for _, n := range m.ReadonlyFields {
			v.readonly[n] = true
		}
	}
	// the form
	specs := m.ChangeFormFields
	if v.add {
		specs = m.AddFormFields
	}
	var order []string
	for _, fs := range v.fsets {
		for _, row := range fs.Rows {
			for _, n := range row {
				if v.readonly[n] || specs[n] == nil {
					continue
				}
				order = append(order, n)
			}
		}
	}
	form := a.newForm(r, m, specs, order, "")
	v.form = form
	if m.Label == "auth.user" {
		a.userFormSetup(v)
	}
	if m.Label == "accounts.portalcredential" {
		a.credentialFormSetup(v)
	}
	if v.obj != nil {
		form.setInitialFromInstance(v.obj)
		if err := a.loadM2MInitial(r, form, v.obj); err != nil {
			c.ServerError(err)
			return
		}
	} else {
		form.setInitialDefaults()
		a.addInitialFromQuery(form, c.R.URL.Query())
	}
	// inline formsets
	if err := a.makeInlines(v); err != nil {
		c.ServerError(err)
		return
	}
	valid := false
	formValidated := false
	if isPost {
		var files map[string]*adminUpload
		files = readUploads(c)
		form.bind(pf, files)
		for _, in := range v.inlines {
			in.build(true, pf, files)
		}
		formValidated = form.isValid()
		allOK := true
		for _, in := range v.inlines {
			if !in.validate() {
				allOK = false
			}
		}
		if allOK && formValidated {
			valid = true
		}
	} else {
		for _, in := range v.inlines {
			in.build(false, nil, nil)
		}
	}
	if isPost && valid {
		if a.saveChangeForm(v, pf) {
			return
		}
	}
	a.renderChangeForm(v, isPost, formValidated, pf)
}

func hasKeyValues(v url.Values, k string) bool { _, ok := v[k]; return ok }

func (a *App) objectMissing(r *adminReq, m *modelSpec, objectID string) {
	c := r.c
	c.Session.addMessage("warning", fmt.Sprintf("%s with ID “%s” doesn’t exist. Perhaps it was deleted?", m.VerboseName, adminUnquote(objectID)))
	idx, _ := a.Router.Reverse("admin:index")
	c.SaveSession()
	c.Redirect(idx)
}

// addInitialFromQuery is get_changeform_initial_data: ?field=value in the URL.
func (a *App) addInitialFromQuery(f *aForm, q url.Values) {
	for k, vals := range q {
		fl := f.fields[k]
		if fl == nil || len(vals) == 0 {
			continue
		}
		last := vals[len(vals)-1]
		switch {
		case fl.mf != nil && fl.mf.isM2M():
			f.m2mInit[k] = strings.Split(last, ",")
		case fl.kind == "bool":
			f.initial[k] = last != "" && last != "0" && strings.ToLower(last) != "false"
		case fl.kind == "model", fl.kind == "int", fl.kind == "choice", fl.kind == "char":
			f.initial[k] = last
		}
	}
}

func (a *App) loadM2MInitial(r *adminReq, f *aForm, obj *adminObj) error {
	for n, fl := range f.fields {
		if fl.mf == nil || !fl.mf.isM2M() {
			continue
		}
		through := f.m.Table + "_" + fl.mf.Name
		left := f.m.ModelName + "_id"
		right := fl.mf.Related.ModelName + "_id"
		rows, err := r.q().Query(r.c.Context(), fmt.Sprintf(`SELECT "%s" FROM %s WHERE "%s" = $1 ORDER BY id`, right, quoteIdent(through), left), obj.vals[obj.m.PK])
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, fmt.Sprint(id))
		}
		rows.Close()
		f.m2mInit[n] = ids
	}
	return nil
}

func (a *App) makeInlines(v *cfView) error {
	for _, spec := range v.m.Inlines {
		fs, err := a.newInlineFS(v.r, spec, v.obj, v.m)
		if err != nil {
			return err
		}
		if !(fs.canAdd || fs.canChange || fs.hasDeletePerm || fs.canView) {
			continue
		}
		v.inlines = append(v.inlines, fs)
	}
	return nil
}

// readUploads collects the files of a multipart POST by field name.
func readUploads(c *Ctx) map[string]*adminUpload {
	out := map[string]*adminUpload{}
	if c.R.MultipartForm == nil {
		return out
	}
	for name, hs := range c.R.MultipartForm.File {
		if len(hs) == 0 {
			continue
		}
		h := hs[len(hs)-1]
		up := readUpload(h)
		if up.name == "" && len(up.data) == 0 {
			continue
		}
		out[name] = &adminUpload{Name: up.name, Data: up.data}
	}
	return out
}

// --- saving ------------------------------------------------------------------------------------

// saveChangeForm saves a valid form and its formsets, logs the change and
// answers with the redirect. It reports whether a response was written.
func (a *App) saveChangeForm(v *cfView, pf url.Values) bool {
	r, c, m := v.r, v.r.c, v.m
	var saved *adminObj
	var changes []*inlineChange
	var msg string
	err := a.withTx(r, func() error {
		obj, err := v.form.saveInstance("", nil)
		if err != nil {
			return err
		}
		saved = obj
		if v.hooks.after != nil {
			v.hooks.after(v)
		}
		for _, in := range v.inlines {
			ch, err := in.save(a, obj.vals[obj.m.PK])
			if err != nil {
				return err
			}
			changes = append(changes, ch)
		}
		msg = a.changeMessage(v, changes)
		re := a.newRepr(c.Context())
		re.q = r.q
		flag := logChange
		if v.add {
			flag = logAddition
		}
		return a.logAction(c.Context(), c.User.ID, m, obj.pkString(), re.str(obj), flag, msg)
	})
	if err != nil {
		c.ServerError(err)
		return true
	}
	a.changeResponse(v, saved, pf)
	return true
}

// changeMessage is construct_change_message as the JSON text Django stores.
func (a *App) changeMessage(v *cfView, changes []*inlineChange) string {
	var parts []string
	if v.add {
		parts = append(parts, `{"added": {}}`)
	} else if fields := changedLabels(v.form); len(fields) > 0 {
		var q []string
		for _, f := range fields {
			q = append(q, pyString(f))
		}
		parts = append(parts, `{"changed": {"fields": [`+strings.Join(q, ", ")+`]}}`)
	}
	re := a.newRepr(v.r.c.Context())
	re.q = v.r.q
	for i, ch := range changes {
		in := v.inlines[i]
		name := pyString(in.spec.VerboseName)
		for _, o := range ch.added {
			parts = append(parts, `{"added": {"name": `+name+`, "object": `+pyString(re.str(o))+`}}`)
		}
		for _, co := range ch.changed {
			var q []string
			for _, f := range co.fields {
				q = append(q, pyString(f))
			}
			parts = append(parts, `{"changed": {"name": `+name+`, "object": `+pyString(re.str(co.obj))+`, "fields": [`+strings.Join(q, ", ")+`]}}`)
		}
		for _, o := range ch.deleted {
			parts = append(parts, `{"deleted": {"name": `+name+`, "object": `+pyString(re.str(o))+`}}`)
		}
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// preservedQSL is the extra query string of the request (not the preserved filters).
func (v *cfView) preservedFilters() string {
	c := v.r.c
	raw := c.R.URL.Query().Get("_changelist_filters")
	if raw == "" {
		return ""
	}
	return encodeQuery([][2]string{{"_changelist_filters", raw}})
}

func (a *App) changeResponse(v *cfView, obj *adminObj, pf url.Values) {
	r, c, m := v.r, v.r.c, v.m
	preserved := v.preservedFilters()
	opts := m
	re := a.newRepr(c.Context())
	objStr := re.str(obj)
	_, hasPopup := pf["_popup"]
	if hasPopup {
		a.popupResponse(v, obj, objStr)
		return
	}
	redirect := func(u string) {
		c.SaveSession()
		c.Redirect(u)
	}
	objURL := a.adminURL(m, "change", adminQuote(obj.pkString()))
	if v.add {
		canChange := r.perms.has(m, "change")
		var linked string
		if canChange {
			linked = `<a href="` + esc(objURL) + `">` + esc(objStr) + `</a>`
		} else {
			linked = esc(objStr)
		}
		name := m.VerboseName
		switch {
		case hasKeyValues(pf, "_continue") || (m.Label == "auth.user" && !hasKeyValues(pf, "_addanother")):
			text := fmt.Sprintf("The %s “%s” was added successfully.", esc(name), linked)
			if canChange {
				text += " You may edit it again below."
			}
			c.Session.addSafeMessage("success", text)
			redirect(addPreservedFilters(objURL, opts, preserved, a))
		case hasKeyValues(pf, "_addanother"):
			c.Session.addSafeMessage("success", fmt.Sprintf("The %s “%s” was added successfully. You may add another %s below.", esc(name), linked, esc(name)))
			redirect(addPreservedFilters(c.R.URL.Path, opts, preserved, a))
		default:
			c.Session.addSafeMessage("success", fmt.Sprintf("The %s “%s” was added successfully.", esc(name), linked))
			redirect(a.postSaveURL(r, m, preserved))
		}
		return
	}
	linked := `<a href="` + esc(c.R.URL.Path) + `">` + esc(objStr) + `</a>`
	name := esc(m.VerboseName)
	switch {
	case hasKeyValues(pf, "_continue"):
		c.Session.addSafeMessage("success", fmt.Sprintf("The %s “%s” was changed successfully. You may edit it again below.", name, linked))
		redirect(addPreservedFilters(c.R.URL.Path, opts, preserved, a))
	case hasKeyValues(pf, "_addanother"):
		c.Session.addSafeMessage("success", fmt.Sprintf("The %s “%s” was changed successfully. You may add another %s below.", name, linked, name))
		redirect(addPreservedFilters(a.adminURL(m, "add"), opts, preserved, a))
	default:
		c.Session.addSafeMessage("success", fmt.Sprintf("The %s “%s” was changed successfully.", name, linked))
		redirect(a.postSaveURL(r, m, preserved))
	}
}

func (a *App) postSaveURL(r *adminReq, m *modelSpec, preserved string) string {
	if r.perms.has(m, "view") || r.perms.has(m, "change") {
		return addPreservedFilters(a.adminURL(m, "changelist"), m, preserved, a)
	}
	idx, _ := a.Router.Reverse("admin:index")
	return idx
}

// popupResponse is the page a related-object popup answers with.
func (a *App) popupResponse(v *cfView, obj *adminObj, objStr string) {
	c := v.r.c
	data := map[string]string{"value": obj.pkString(), "obj": objStr}
	var b strings.Builder
	if v.add {
		b.WriteString(fmt.Sprintf(`{"value": %s, "obj": %s}`, pyString(data["value"]), pyString(data["obj"])))
	} else {
		b.WriteString(fmt.Sprintf(`{"action": "change", "value": %s, "obj": %s, "new_value": %s}`,
			pyString(data["value"]), pyString(data["obj"]), pyString(data["value"])))
	}
	c.renderContext(http.StatusOK, "admin/popup_response.html", pongo2.Context{"popup_response_data": b.String()})
}

var _ = store.NewUUID

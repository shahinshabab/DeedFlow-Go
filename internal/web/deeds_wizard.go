package web

import (
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"

	"github.com/shahinshabab/deedflow-go/internal/deedforms"
	"github.com/shahinshabab/deedflow-go/internal/deeds"
	"github.com/shahinshabab/deedflow-go/internal/forms"
	"github.com/shahinshabab/deedflow-go/internal/models"
	"github.com/shahinshabab/deedflow-go/internal/store"
)

// stepLabels are the wizard's steps in order.
var stepLabels = [][2]string{
	{"registration", "Registration"},
	{"presenter", "Presenter"},
	{"documents", "Prior Documents"},
	{"executants", "Executants"},
	{"claimants", "Claimants"},
	{"schedule", "Schedule"},
	{"witnesses", "Witnesses"},
}

func stepIndex(step string) int {
	for i, s := range stepLabels {
		if s[0] == step {
			return i
		}
	}
	return -1
}

// wizardStorage is formtools' SessionStorage: the current step and each
// finished step's posted data, kept in the user's session per deed.
type wizardStorage struct {
	c       *Ctx
	key     string
	Current string
	Data    map[string]url.Values
}

func newWizardStorage(c *Ctx, publicID string) *wizardStorage {
	s := &wizardStorage{c: c, key: "deed_wizard_" + publicID, Data: map[string]url.Values{}}
	if raw, ok := c.Session.Get(s.key); ok {
		if m, ok := raw.(map[string]any); ok {
			s.Current, _ = m["step"].(string)
			if sd, ok := m["step_data"].(map[string]any); ok {
				for step, v := range sd {
					vals := url.Values{}
					if vm, ok := v.(map[string]any); ok {
						for k, list := range vm {
							if l, ok := list.([]any); ok {
								for _, e := range l {
									if str, ok := e.(string); ok {
										vals.Add(k, str)
									}
								}
							}
						}
					}
					s.Data[step] = vals
				}
			}
		}
	}
	return s
}

func (s *wizardStorage) save() {
	sd := map[string]any{}
	for step, vals := range s.Data {
		m := map[string]any{}
		for k, list := range vals {
			l := make([]any, len(list))
			for i, e := range list {
				l[i] = e
			}
			m[k] = l
		}
		sd[step] = m
	}
	s.c.Session.Set(s.key, map[string]any{"step": s.Current, "step_data": sd})
}

func (s *wizardStorage) reset() {
	s.Current = ""
	s.Data = map[string]url.Values{}
	s.c.Session.Delete(s.key)
}

func (s *wizardStorage) stepData(step string) url.Values {
	if v, ok := s.Data[step]; ok {
		return v
	}
	return nil
}

// wizardForm is the form or formset of one step.
type wizardForm struct {
	step string
	form *forms.Form
	set  *forms.FormSet
}

func (f *wizardForm) valid() bool {
	if f.set != nil {
		return f.set.IsValid()
	}
	return f.form.IsValid()
}

func (f *wizardForm) template() map[string]any {
	if f.set != nil {
		return f.set.Template()
	}
	return f.form.Template()
}

type deedWizard struct {
	c       *Ctx
	a       *App
	deed    *models.Deed
	storage *wizardStorage
	editing bool
	b       *deedforms.Builder
	data    *deeds.DeedData
}

func (a *App) wizard(c *Ctx) {
	deed := c.deedOr404()
	if deed == nil {
		return
	}
	if deed.Status == models.StatusSubmitting {
		c.Message("info", "This deed is being submitted to the portal right now - edit it once that finishes.")
		c.SaveSession()
		c.RedirectTo("deeds:detail", deed.PublicID)
		return
	}
	if len(deed.SurveyMapping) > 0 {
		c.SaveSession()
		c.RedirectTo("deeds:survey_mapping", deed.PublicID)
		return
	}
	w := &deedWizard{c: c, a: a, deed: deed, storage: newWizardStorage(c, deed.PublicID), b: a.builder(c.Context())}
	w.editing = deed.Status != models.StatusDraft
	// Record what's on the portal *before* any edit, so edits made now show
	// up as changes (for deeds submitted before tracking existed).
	if err := w.reload(); err != nil {
		c.ServerError(err)
		return
	}
	a.ensureBaseline(c, deed, w.data)
	switch c.R.Method {
	case http.MethodGet:
		w.get()
	case http.MethodPost:
		w.post()
	default:
		c.W.Header().Set("Allow", "GET, POST")
		http.Error(c.W, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func (w *deedWizard) reload() error {
	d, err := w.a.Store.LoadDeedData(w.c.Context(), w.deed)
	w.data = d
	return err
}

func (w *deedWizard) current() string {
	if w.storage.Current != "" && stepIndex(w.storage.Current) >= 0 {
		return w.storage.Current
	}
	return stepLabels[0][0]
}

func (w *deedWizard) get() {
	// ?step=<name> opens that page directly (the View page's "Edit this
	// page"); formtools' own GET always restarts at the first step.
	w.storage.reset()
	step := w.c.Query("step")
	if w.editing && stepIndex(step) >= 0 {
		w.storage.Current = step
	} else {
		w.storage.Current = stepLabels[0][0]
	}
	w.render(w.getForm(w.storage.Current, nil))
}

func (w *deedWizard) post() {
	c := w.c
	post := c.PostForm()
	prefix := "deed_wizard_" + w.deed.PublicID
	if w.editing && post.Has("save_and_close") {
		form := w.getForm(w.current(), post)
		if !form.valid() {
			w.render(form)
			return
		}
		if err := w.processStep(form); err != nil {
			c.ServerError(err)
			return
		}
		w.finish()
		return
	}
	if goto_ := post.Get("wizard_goto_step"); goto_ != "" && stepIndex(goto_) >= 0 {
		w.storage.Current = goto_
		w.render(w.getForm(goto_, w.storage.stepData(goto_)))
		return
	}
	cur := post.Get(prefix + "-current_step")
	if stepIndex(cur) < 0 {
		c.BadRequest("ManagementForm data is missing or has been tampered.")
		return
	}
	if cur != w.current() && w.storage.Current != "" {
		w.storage.Current = cur
	}
	step := w.current()
	form := w.getForm(step, post)
	if !form.valid() {
		w.render(form)
		return
	}
	if err := w.processStep(form); err != nil {
		c.ServerError(err)
		return
	}
	last := stepLabels[len(stepLabels)-1][0]
	if step == last {
		w.renderDone(post)
		return
	}
	next := stepLabels[stepIndex(step)+1][0]
	w.storage.Current = next
	w.render(w.getForm(next, w.storage.stepData(next)))
}

// getForm builds a step's form: bound to data, or unbound over the saved rows.
func (w *deedWizard) getForm(step string, data url.Values) *wizardForm {
	ctx := w.c.Context()
	if err := w.reload(); err != nil {
		panic(err)
	}
	wf := &wizardForm{step: step}
	switch step {
	case "registration":
		wf.form = w.b.Registration("registration", w.data.Registration, deedforms.RegistrationOptions{Data: data, Locked: w.deed.TokenNumber != ""})
	case "presenter":
		var min int64
		if m, ok := deeds.MinimumDocumentValue(w.data.Schedule); ok {
			min = m
		}
		wf.form = w.b.Presenter("presenter", w.data.Presenter, data, min)
		if data == nil {
			w.b.PresenterDefaults(wf.form, w.data.Presenter)
		}
	default:
		var rows []any
		switch step {
		case "documents":
			rows = deedforms.Rows(w.data.PriorDocuments)
		case "executants":
			rows = deedforms.Rows(w.data.Executants)
		case "claimants":
			rows = deedforms.Rows(w.data.Claimants)
		case "schedule":
			rows = deedforms.Rows(w.data.Schedule)
		case "witnesses":
			rows = deedforms.Rows(w.data.Witnesses)
		}
		var opt deedforms.FormSetOptions
		if step == "schedule" && w.data.Presenter != nil && w.data.Presenter.DocumentValue != nil {
			v := int64(*w.data.Presenter.DocumentValue)
			opt.DocumentValue = &v
		}
		wf.set = w.b.SectionFormSet(step, w.deed.ID, rows, data, opt)
	}
	_ = ctx
	return wf
}

// processStep saves a valid step to the database as soon as it validates (a
// refresh or a dropped connection can't lose the pages already completed) and
// remembers it in the session.
func (w *deedWizard) processStep(wf *wizardForm) error {
	ctx := w.c.Context()
	st := w.a.Store
	switch wf.step {
	case "registration":
		row := w.data.Registration
		if row == nil {
			row = &models.RegistrationDetail{}
		}
		if err := wf.form.ApplyToRow(row); err != nil {
			return err
		}
		row.DeedID = w.deed.ID
		if err := st.SaveRegistration(ctx, row); err != nil {
			return err
		}
		w.storage.Data[wf.step] = wf.form.Data
	case "presenter":
		row := w.data.Presenter
		if row == nil {
			row = &models.Presenter{}
		}
		if err := wf.form.ApplyToRow(row); err != nil {
			return err
		}
		row.DeedID = w.deed.ID
		if err := st.SavePresenter(ctx, row); err != nil {
			return err
		}
		w.storage.Data[wf.step] = wf.form.Data
	default:
		if err := wf.set.Save(ctx, st); err != nil {
			return err
		}
		// Keep the rows as saved, ids included, rather than as posted: an
		// entry added (or cloned) on this page has no id in the post, so
		// going Back and Next again, or finishing, would create it a second time.
		if err := w.reload(); err != nil {
			return err
		}
		fresh := w.getForm(wf.step, nil)
		w.storage.Data[wf.step] = fresh.set.SavedData()
	}
	return nil
}

// renderDone is formtools' render_done: revalidate every step from storage,
// then save everything and finish.
func (w *deedWizard) renderDone(lastPost url.Values) {
	if w.editing {
		// Every page visited was saved as it was submitted; pages not opened
		// this time are unchanged - nothing to revalidate.
		w.finish()
		return
	}
	for _, s := range stepLabels {
		wf := w.getForm(s[0], w.storage.stepData(s[0]))
		if wf.set == nil && wf.form != nil && wf.form.Bound == false {
			w.storage.Current = s[0]
			w.render(wf)
			return
		}
		if !wf.valid() {
			w.storage.Current = s[0]
			w.render(wf)
			return
		}
	}
	// done(): save everything once more (a no-op for rows already saved).
	for _, s := range stepLabels {
		wf := w.getForm(s[0], w.storage.stepData(s[0]))
		if err := w.processStep(wf); err != nil {
			w.c.ServerError(err)
			return
		}
	}
	w.finish()
}

func (w *deedWizard) finish() {
	c := w.c
	ctx := c.Context()
	st := w.a.Store
	deed, err := st.DeedByID(ctx, w.deed.ID)
	if err != nil {
		c.ServerError(err)
		return
	}
	data, err := st.LoadDeedData(ctx, deed)
	if err != nil {
		c.ServerError(err)
		return
	}
	if deed.Title == "" && data.Presenter != nil {
		deed.Title = "Deed for " + data.Presenter.Name
	}
	if deed.TokenNumber != "" {
		pending := deeds.PendingSections(data)
		if len(pending) > 0 {
			deed.Status = models.StatusReadyForReview
			var labels []string
			for _, p := range pending {
				labels = append(labels, deeds.SectionLabel(p))
			}
			deed.ResultMessage = "Edited: " + strings.Join(labels, ", ") +
				fmt.Sprintf(". Send the edits to update token %s - only what changed will be sent.", deed.TokenNumber)
		} else {
			deed.Status = models.StatusCompleted
			deed.ResultMessage = fmt.Sprintf("No changes - token %s on the portal is up to date.", deed.TokenNumber)
		}
	} else {
		deed.Status = models.StatusReadyForReview
	}
	if err := st.SaveDeedQuiet(ctx, deed, "title", "status", "result_message"); err != nil {
		c.ServerError(err)
		return
	}
	w.storage.reset()
	c.SaveSession()
	c.RedirectTo("deeds:detail", deed.PublicID)
}

func (w *deedWizard) render(wf *wizardForm) {
	c := w.c
	step := wf.step
	w.storage.Current = step
	w.storage.save()
	idx := stepIndex(step)
	steps := make([]map[string]string, len(stepLabels))
	for i, s := range stepLabels {
		steps[i] = map[string]string{"name": s[0], "label": s[1]}
	}
	colWidth := "col-md-4"
	if step == "registration" {
		colWidth = "col-md-6"
	}
	pendingSteps := map[string]bool{}
	if w.editing && w.deed.TokenNumber != "" {
		if err := w.reload(); err == nil {
			pending := map[string]bool{}
			for _, s := range deeds.PendingSections(w.data) {
				pending[s] = true
			}
			for _, sec := range deeds.PortalSections {
				if pending[sec.Key] {
					pendingSteps[sec.Step] = true
				}
			}
		}
	}
	var prev, next any
	if idx > 0 {
		prev = stepLabels[idx-1][0]
	}
	if idx < len(stepLabels)-1 {
		next = stepLabels[idx+1][0]
	}
	prefix := "deed_wizard_" + w.deed.PublicID
	mgmt := fmt.Sprintf(`<input type="hidden" name="%s-current_step" value="%s" id="id_%s-current_step">`, prefix, step, prefix)
	c.Render("deeds/wizard/step.html", Data{
		"deed":                deedObj(w.deed, nil),
		"step_labels":         steps,
		"current_step_index":  idx,
		"col_width":           colWidth,
		"is_editing":          w.editing,
		"registration_locked": step == "registration" && w.deed.TokenNumber != "",
		"pending_steps":       pendingSteps,
		"progress_width":      int(math.Round(float64(idx+1) / float64(len(stepLabels)) * 100)),
		"wizard": map[string]any{
			"steps": map[string]any{
				"current": step, "first": stepLabels[0][0], "last": stepLabels[len(stepLabels)-1][0],
				"prev": prev, "next": next, "count": len(stepLabels),
			},
			"management_form": mgmt,
			"form":            wf.template(),
		},
	})
}

var _ = store.NewUUID

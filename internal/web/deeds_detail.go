package web

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/shahinshabab/deedflow-go/internal/db"
	"github.com/shahinshabab/deedflow-go/internal/deedforms"
	"github.com/shahinshabab/deedflow-go/internal/deeds"
	"github.com/shahinshabab/deedflow-go/internal/forms"
	"github.com/shahinshabab/deedflow-go/internal/jobs"
	"github.com/shahinshabab/deedflow-go/internal/models"
	"github.com/shahinshabab/deedflow-go/internal/pipeline"
	"github.com/shahinshabab/deedflow-go/internal/store"
)

var timeZero time.Time

func (a *App) builder(ctx context.Context) *deedforms.Builder {
	return &deedforms.Builder{Ctx: ctx, DD: &deeds.Dropdowns{Src: a.Store}, Today: func() time.Time { return deeds.Today(a.Cfg.TimeZone) }}
}

// ensureBaseline records what is on the portal for a deed that has a token but
// no recorded fingerprints (ensure_portal_baseline), saving it quietly.
func (a *App) ensureBaseline(c *Ctx, d *models.Deed, data *deeds.DeedData) {
	snap, changed := deeds.EnsurePortalBaseline(data)
	if !changed {
		return
	}
	d.PortalSnapshot = snap
	if err := a.Store.SaveDeedQuiet(c.Context(), d, "portal_snapshot"); err != nil {
		a.Log.Error("saving the portal baseline", "err", err)
	}
}

func (a *App) hasPortalCredential(c *Ctx, d *models.Deed) bool {
	if d.OrganizationID == nil {
		return false
	}
	cred, err := a.Store.PortalCredentialFor(c.Context(), *d.OrganizationID)
	return err == nil && cred != nil && cred.PortalUsername != "" && cred.PortalPassword != ""
}

func presenterObj(p *models.Presenter) any {
	if p == nil {
		return nil
	}
	return ObjectMap(p, nil)
}

func (a *App) deedDetail(c *Ctx) {
	d := c.deedOr404()
	if d == nil {
		return
	}
	ctx := c.Context()
	pipeline.ReapStaleSubmission(ctx, a.Store, d)
	data, err := a.Store.LoadDeedData(ctx, d)
	if err != nil {
		c.ServerError(err)
		return
	}
	a.ensureBaseline(c, d, data)
	pending := map[string]bool{}
	var pendingRows, pendingWhole []string
	if d.TokenNumber != "" {
		for _, s := range deeds.PendingSections(data) {
			pending[s] = true
		}
		if len(pending) > 0 {
			pendingRows, pendingWhole = deeds.PendingChanges(data)
		}
	}

	b := a.builder(ctx)
	readonly := func(f *forms.Form) map[string]any {
		for _, fl := range f.Fields {
			fl.Disabled = true
		}
		return f.Template()
	}
	sectionForms := map[string][]map[string]any{}
	if data.Registration != nil {
		sectionForms["registration"] = []map[string]any{readonly(b.Registration("registration", data.Registration, deedforms.RegistrationOptions{}))}
	}
	if data.Presenter != nil {
		sectionForms["presenter"] = []map[string]any{readonly(b.Presenter("presenter", data.Presenter, nil, 0))}
	}
	add := func(key, section string, rows []any) {
		for i, row := range rows {
			sectionForms[key] = append(sectionForms[key], readonly(b.SectionForm(section, fmt.Sprintf("%s-%d", section, i), row, nil)))
		}
	}
	add("prior_documents", "documents", deedforms.Rows(data.PriorDocuments))
	add("executants", "executants", deedforms.Rows(data.Executants))
	add("claimants", "claimants", deedforms.Rows(data.Claimants))
	add("schedule", "schedule", deedforms.Rows(data.Schedule))
	add("witnesses", "witnesses", deedforms.Rows(data.Witnesses))

	var tabs []map[string]any
	var pendingLabels []string
	for _, sec := range deeds.PortalSections {
		colWidth := "col-md-4"
		if sec.Key == "registration" {
			colWidth = "col-md-6"
		}
		fl := sectionForms[sec.Key]
		if fl == nil {
			fl = []map[string]any{}
		}
		tabs = append(tabs, map[string]any{
			"key": sec.Key, "label": sec.Label, "step": sec.Step, "forms": fl,
			"repeatable": sec.Key != "registration" && sec.Key != "presenter",
			"col_width":  colWidth,
			"pending":    pending[sec.Key],
			// Registration is fixed once the portal has issued a token.
			"editable": !(sec.Key == "registration" && d.TokenNumber != "") && d.Status != models.StatusSubmitting,
		})
		if pending[sec.Key] {
			pendingLabels = append(pendingLabels, sec.Label)
		}
	}
	var shots []map[string]any
	for i := len(data.Screenshots) - 1; i >= 0; i-- {
		s := data.Screenshots[i]
		shots = append(shots, map[string]any{"label": s.Label, "url": "/media/" + s.Image, "created_at": s.CreatedAt})
	}
	fillingToken := false
	if d.TokenNumber != "" {
		fillingToken = true
		for k := range d.PortalSnapshot.Sections {
			if k != "registration" {
				fillingToken = false
			}
		}
	}
	canSubmit := d.Status == models.StatusReadyForReview || d.Status == models.StatusFailed ||
		(d.Status == models.StatusCompleted && len(pending) > 0)
	c.Render("deeds/detail.html", Data{
		"deed":                  deedObj(d, map[string]any{"presenter": presenterObj(data.Presenter)}),
		"tabs":                  tabs,
		"screenshots":           shots,
		"pending_labels":        pendingLabels,
		"pending_rows":          pendingRows,
		"pending_whole":         pendingWhole,
		"can_submit":            canSubmit,
		"has_portal_credential": a.hasPortalCredential(c, d),
		"filling_token":         fillingToken,
	})
}

func (a *App) deedSubmit(c *Ctx) {
	d := c.deedOr404()
	if d == nil {
		return
	}
	if c.R.Method != http.MethodPost {
		c.SaveSession()
		c.RedirectTo("deeds:detail", d.PublicID)
		return
	}
	ctx := c.Context()
	if !a.hasPortalCredential(c, d) {
		c.Message("error", "No portal login is set for your organization yet. Ask your organization's admin to add it.")
		c.SaveSession()
		c.RedirectTo("deeds:detail", d.PublicID)
		return
	}
	// Claimed atomically: two quick clicks (or two tabs) must not start two
	// parallel runs against the same deed - that's how duplicate portal
	// entries get made. Only one UPDATE can win the status change.
	pipeline.ReapStaleSubmission(ctx, a.Store, d)
	allowed := []string{models.StatusReadyForReview, models.StatusFailed}
	if d.Status == models.StatusCompleted && d.TokenNumber != "" {
		data, err := a.Store.LoadDeedData(ctx, d)
		if err != nil {
			c.ServerError(err)
			return
		}
		if len(deeds.PendingSections(data)) > 0 {
			allowed = append(allowed, models.StatusCompleted)
		}
	}
	claimed, err := a.Store.ClaimForSubmission(ctx, d.ID, allowed)
	if err != nil {
		c.ServerError(err)
		return
	}
	if !claimed {
		c.Message("error", "This deed is already being submitted, or isn't in a state that can be submitted.")
		c.SaveSession()
		c.RedirectTo("deeds:detail", d.PublicID)
		return
	}
	if _, err := a.Jobs.Enqueue(ctx, jobs.KindSubmitDeed, map[string]any{"deed_id": d.ID}, timeZero); err != nil {
		c.ServerError(err)
		return
	}
	c.SaveSession()
	c.RedirectTo("deeds:submitting", d.PublicID)
}

func (a *App) deedSubmitting(c *Ctx) {
	d := c.deedOr404()
	if d == nil {
		return
	}
	// Deliberately not gated tighter than this: right after the job is
	// enqueued the worker may not have picked it up yet, so the status can
	// still read ready_for_review for a moment.
	pipeline.ReapStaleSubmission(c.Context(), a.Store, d)
	if d.Status == models.StatusCompleted || d.Status == models.StatusDraft {
		c.SaveSession()
		c.RedirectTo("deeds:detail", d.PublicID)
		return
	}
	c.Render("deeds/submitting.html", Data{"deed": deedObj(d, nil)})
}

func (a *App) deedStatus(c *Ctx) {
	d := c.deedOr404()
	if d == nil {
		return
	}
	pipeline.ReapStaleSubmission(c.Context(), a.Store, d)
	pipeline.ReapStaleProcessing(c.Context(), a.Store, d)
	c.jsonOK(map[string]any{
		"status":       d.Status,
		"progress":     d.Progress,
		"message":      d.ResultMessage,
		"token_number": d.TokenNumber,
		"detail_url":   "/deeds/" + d.PublicID + "/",
	})
}

// --- media ---------------------------------------------------------------------------

// mediaFile serves an uploaded file (a deed's source PDF or a portal
// screenshot) only to members of the organization the deed belongs to: files
// hold personal data. Anything else under media is for superusers only.
func (a *App) mediaFile(c *Ctx) {
	name := c.PathValue("path")
	v, err := c.viewer()
	if err != nil {
		c.ServerError(err)
		return
	}
	allowed := c.User.IsSuperuser
	if !allowed {
		if ok, err := a.Store.SourcePDFVisible(c.Context(), v, name); err == nil && ok {
			allowed = true
		}
	}
	if !allowed {
		if ok, err := a.Store.ScreenshotVisible(c.Context(), v, name); err == nil && ok {
			allowed = true
		}
	}
	if !allowed {
		c.NotFound()
		return
	}
	full, err := a.MediaPath(name)
	if err != nil {
		c.NotFound()
		return
	}
	f, err := os.Open(full)
	if err != nil {
		c.NotFound()
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		c.NotFound()
		return
	}
	c.W.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(c.W, c.R, path.Base(name), st.ModTime(), f)
}

// --- live status socket ---------------------------------------------------------------

// deedStatusSocket streams a deed's progress events (see pipeline) to whoever
// has its processing / submitting page open. The connecting user's
// organization must own the deed.
func (a *App) deedStatusSocket(w http.ResponseWriter, r *http.Request) {
	c := &Ctx{W: w, R: r, App: a, ViewName: "ws"}
	if !c.loadUser() || c.User == nil {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	publicID := strings.ToLower(r.PathValue("public_id"))
	if !uuidRe.MatchString(publicID) {
		http.NotFound(w, r)
		return
	}
	v, err := c.viewer()
	if err != nil {
		http.Error(w, "Server Error", http.StatusInternalServerError)
		return
	}
	if ok, err := a.Store.DeedVisible(r.Context(), v, publicID); err != nil || !ok {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: a.originPatterns(r)})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	sub := a.Hub.Subscribe(pipeline.GroupFor(publicID))
	defer sub.Close()
	ctx := conn.CloseRead(r.Context())
	for {
		select {
		case msg := <-sub.C:
			wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Write(wctx, websocket.MessageText, msg)
			cancel()
			if err != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// originPatterns allows the page's own host (the AllowedHostsOriginValidator).
func (a *App) originPatterns(r *http.Request) []string {
	host := r.Host
	if xf := r.Header.Get("X-Forwarded-Host"); xf != "" {
		host = xf
	}
	return []string{host}
}

var _ = db.ErrNotFound
var _ = store.NewUUID

package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/shahinshabab/deedflow-go/internal/db"
	"github.com/shahinshabab/deedflow-go/internal/deeds"
	"github.com/shahinshabab/deedflow-go/internal/jobs"
	"github.com/shahinshabab/deedflow-go/internal/models"
	"github.com/shahinshabab/deedflow-go/internal/pipeline"
	"github.com/shahinshabab/deedflow-go/internal/plumber"
	"github.com/shahinshabab/deedflow-go/internal/store"
)

func (a *App) registerDeeds() {
	a.Route("deeds:my_deeds", "deeds/", a.LoginRequired(a.myDeeds))
	a.Route("deeds:create", "deeds/new/", a.LoginRequired(a.deedCreate))
	a.Route("deeds:upload", "deeds/upload/", a.LoginRequired(a.deedUpload))
	a.Route("deeds:test_dashboard", "deeds/test/", a.OrgAdminRequired(a.testDashboard))
	a.Route("deeds:pdf_test", "deeds/test/pdf/", a.OrgAdminRequired(a.extractPreview))
	a.Route("deeds:ai_test", "deeds/test/ai/", a.OrgAdminRequired(a.aiTest))
	a.Route("deeds:selenium_test", "deeds/test/selenium/", a.OrgAdminRequired(a.seleniumTestCreate))
	a.Route("deeds:ajax_sros", "deeds/ajax/sros/", a.LoginRequired(a.ajaxSROs))
	a.Route("deeds:ajax_taluks", "deeds/ajax/taluks/", a.LoginRequired(a.ajaxTaluks))
	a.Route("deeds:ajax_deed_types", "deeds/ajax/deed-types/", a.LoginRequired(a.ajaxDeedTypes))
	a.Route("deeds:ajax_deed_subtypes", "deeds/ajax/deed-subtypes/", a.LoginRequired(a.ajaxDeedSubtypes))
	a.Route("deeds:ajax_portal_options", "deeds/ajax/portal-options/", a.LoginRequired(a.ajaxPortalOptions))
	a.Route("deeds:wizard", "deeds/<uuid:public_id>/wizard/", a.LoginRequired(a.wizard))
	a.Route("deeds:processing", "deeds/<uuid:public_id>/processing/", a.LoginRequired(a.deedProcessing))
	a.Route("deeds:survey_mapping", "deeds/<uuid:public_id>/survey-mapping/", a.LoginRequired(a.deedSurveyMapping))
	a.Route("deeds:reprocess", "deeds/<uuid:public_id>/reprocess/", a.LoginRequired(a.deedReprocess))
	a.Route("deeds:submit", "deeds/<uuid:public_id>/submit/", a.LoginRequired(a.deedSubmit))
	a.Route("deeds:submitting", "deeds/<uuid:public_id>/submitting/", a.LoginRequired(a.deedSubmitting))
	a.Route("deeds:status", "deeds/<uuid:public_id>/status/", a.LoginRequired(a.deedStatus))
	a.Route("deeds:detail", "deeds/<uuid:public_id>/", a.LoginRequired(a.deedDetail))
	a.Router.Mux().Handle("GET /media/{path...}", a.wrap("media", a.LoginRequired(a.mediaFile)))
	a.Router.Mux().Handle("GET /ws/deeds/{public_id}/status/", http.HandlerFunc(a.deedStatusSocket))
}

// --- My Deeds -----------------------------------------------------------------

func (a *App) myDeeds(c *Ctx) {
	v, err := c.viewer()
	if err != nil {
		c.ServerError(err)
		return
	}
	q := strings.TrimSpace(c.Query("q"))
	status := c.Query("status")
	dateFrom, dateTo := safeDate(c.Query("from")), safeDate(c.Query("to"))
	validStatus := false
	for _, s := range models.StatusChoices {
		if s[0] == status {
			validStatus = true
		}
	}
	if !validStatus {
		status = ""
	}
	filter := store.DeedListFilter{Q: q, Status: status, From: dateFrom, To: dateTo}
	// Count first, then fetch the clamped page.
	filter.Limit, filter.Offset = 1, 0
	_, total, err := a.Store.ListDeeds(c.Context(), v, filter)
	if err != nil {
		c.ServerError(err)
		return
	}
	pager := NewPager(total, 20, c.Query("page"))
	filter.Limit, filter.Offset = pager.PerPage, pager.Offset()
	items, _, err := a.Store.ListDeeds(c.Context(), v, filter)
	if err != nil {
		c.ServerError(err)
		return
	}
	rows := make([]any, 0, len(items))
	for _, it := range items {
		var pres any
		if it.PresenterName != "" || it.PresenterDate != nil {
			pres = map[string]any{"name": it.PresenterName, "presenter_date": it.PresenterDate}
		}
		rows = append(rows, deedObj(&it.Deed, map[string]any{
			"owner":     map[string]any{"get_full_name": it.OwnerName, "username": it.OwnerName},
			"presenter": pres,
		}))
	}
	choices := make([]map[string]string, len(models.StatusChoices))
	for i, s := range models.StatusChoices {
		choices[i] = map[string]string{"value": s[0], "label": s[1]}
	}
	c.Render("deeds/my_deeds.html", Data{
		"page":           pager.Map(rows),
		"q":              q,
		"status":         status,
		"date_from":      c.Query("from"),
		"date_to":        c.Query("to"),
		"status_choices": choices,
		"query_string":   queryWithout(c.R.URL.RawQuery, "page"),
		"filtered":       q != "" || status != "" || dateFrom != nil || dateTo != nil,
	})
}

func (a *App) deedCreate(c *Ctx) {
	d := &models.Deed{OwnerID: c.User.ID}
	if err := a.Store.CreateDeed(c.Context(), d); err != nil {
		c.ServerError(err)
		return
	}
	c.SaveSession()
	c.RedirectTo("deeds:wizard", d.PublicID)
}

// --- upload ---------------------------------------------------------------------

// maxPDFUploadMB only stops a wrong or runaway file; real deed PDFs are well under 1 MB.
const maxPDFUploadMB = 25

// pdfUploadProblem says why data can't be processed as a deed PDF ("" if it can).
func pdfUploadProblem(data []byte, log func(string, ...any)) string {
	if len(data) == 0 {
		return "That file is empty."
	}
	if len(data) > maxPDFUploadMB*1024*1024 {
		return fmt.Sprintf("That file is %.0f MB; the limit is %d MB.", float64(len(data))/1024/1024, maxPDFUploadMB)
	}
	head := data
	if len(head) > 1024 {
		head = head[:1024]
	}
	if !bytes.Contains(head, []byte("%PDF-")) {
		return "That file isn't a PDF. Upload the deed as a PDF."
	}
	doc, err := plumber.Open(data)
	if err != nil {
		log("Rejected unreadable PDF upload", "err", err)
		return "That PDF can't be opened. It may be damaged or password-protected."
	}
	if doc.NumPages() == 0 {
		return "That PDF has no pages."
	}
	return ""
}

var tokenNumberRe = regexp.MustCompile(`^T\d{6,}$`)

// existingTokenProblem says why token can't be continued as a new deed's existing portal token.
func (c *Ctx) existingTokenProblem(token string) string {
	if !tokenNumberRe.MatchString(token) {
		return "Enter the token number as it shows on the portal, like T10380594."
	}
	v, err := c.viewer()
	if err != nil {
		return ""
	}
	taken, err := c.App.Store.DeedByTokenNumber(c.Context(), v, token)
	if err == nil && taken != nil {
		return fmt.Sprintf("Token %s is already on %s in My Deeds. Open that deed to change it, "+
			"so two deeds never write to the same token.", token, taken.String())
	}
	return ""
}

// uploadedFile reads a multipart file field fully.
func uploadedFile(r *http.Request, field string) (name string, data []byte, ok bool, err error) {
	if r.MultipartForm == nil {
		return "", nil, false, nil
	}
	files := r.MultipartForm.File[field]
	if len(files) == 0 {
		return "", nil, false, nil
	}
	return readHeader(files[0])
}

func readHeader(h *multipart.FileHeader) (string, []byte, bool, error) {
	f, err := h.Open()
	if err != nil {
		return "", nil, false, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 64<<20))
	return path.Base(strings.ReplaceAll(h.Filename, "\\", "/")), data, true, err
}

func (a *App) deedUpload(c *Ctx) {
	mode := c.R.FormValue("mode")
	if mode == "" {
		mode = c.Query("mode")
	}
	if mode != "existing" {
		mode = "fresh"
	}
	token := strings.ReplaceAll(strings.ToUpper(strings.TrimSpace(c.R.FormValue("token_number"))), " ", "")
	data := Data{"mode": mode, "token_number": token}
	if c.R.Method == http.MethodPost {
		c.PostForm() // parses the multipart body
		name, content, ok, err := uploadedFile(c.R, "pdf_file")
		if err != nil {
			c.ServerError(err)
			return
		}
		if ok {
			problem := pdfUploadProblem(content, a.Log.Info)
			if problem == "" && mode == "existing" {
				problem = c.existingTokenProblem(token)
			}
			if problem != "" {
				c.Message("error", problem)
				c.RenderStatus(http.StatusBadRequest, "deeds/upload.html", data)
				return
			}
			stored, err := a.SaveMedia("deed_pdfs/"+validFilename(name), content)
			if err != nil {
				c.ServerError(err)
				return
			}
			title := name
			if r := []rune(title); len(r) > 255 {
				title = string(r[:255])
			}
			d := &models.Deed{OwnerID: c.User.ID, SourcePDF: &stored, Title: title}
			if mode == "existing" {
				d.TokenNumber = token
			}
			if err := a.Store.CreateDeed(c.Context(), d); err != nil {
				c.ServerError(err)
				return
			}
			if _, err := a.Jobs.Enqueue(c.Context(), jobs.KindProcessPDF, map[string]any{"deed_id": d.ID}, timeZero); err != nil {
				c.ServerError(err)
				return
			}
			c.SaveSession()
			c.RedirectTo("deeds:processing", d.PublicID)
			return
		}
	}
	c.Render("deeds/upload.html", data)
}

// --- processing / mapping --------------------------------------------------------

func (a *App) deedProcessing(c *Ctx) {
	d := c.deedOr404()
	if d == nil {
		return
	}
	pipeline.ReapStaleProcessing(c.Context(), a.Store, d)
	if d.Status == models.StatusReadyForReview {
		c.SaveSession()
		c.RedirectTo("deeds:wizard", d.PublicID)
		return
	}
	if d.Status == models.StatusFailed && !pipeline.ProcessingFailed(d) {
		c.SaveSession()
		c.RedirectTo("deeds:detail", d.PublicID)
		return
	}
	failed := d.Status == models.StatusFailed
	c.Render("deeds/processing.html", Data{
		"deed":      deedObj(d, nil),
		"failed":    failed,
		"can_retry": pipeline.ProcessingFailed(d) && d.SourcePDF != nil && *d.SourcePDF != "",
	})
}

func (a *App) deedSurveyMapping(c *Ctx) {
	d := c.deedOr404()
	if d == nil {
		return
	}
	if len(d.SurveyMapping) == 0 {
		c.SaveSession()
		c.RedirectTo("deeds:wizard", d.PublicID)
		return
	}
	if c.R.Method == http.MethodPost {
		post := c.PostForm()
		var assignments [][]int
		for g, m := range d.SurveyMapping {
			var assignment []int
			for j := range m.Subs {
				owner, err := strconv.Atoi(strings.TrimSpace(post.Get(fmt.Sprintf("map-%d-%d", g, j))))
				if err != nil {
					owner = -1
				}
				if owner < 0 || owner >= len(m.OldSurveys) {
					c.Message("error", "Choose an old survey number for every sub-division.")
					c.SaveSession()
					c.RedirectTo("deeds:survey_mapping", d.PublicID)
					return
				}
				assignment = append(assignment, owner)
			}
			assignments = append(assignments, assignment)
		}
		if err := pipeline.ApplySurveyMapping(c.Context(), a.Store, d, assignments); err != nil {
			c.ServerError(err)
			return
		}
		c.SaveSession()
		c.RedirectTo("deeds:wizard", d.PublicID)
		return
	}
	var groups []map[string]any
	for g, m := range d.SurveyMapping {
		var olds, subs []map[string]any
		for i, o := range m.OldSurveys {
			olds = append(olds, map[string]any{"i": i, "old": o})
		}
		for j, sub := range m.Subs {
			owner := 0
			if j < len(m.Assignment) {
				owner = m.Assignment[j]
			}
			subs = append(subs, map[string]any{"index": j, "sub": sub, "owner": owner})
		}
		groups = append(groups, map[string]any{"index": g, "number": m.Number, "old_surveys": olds, "subs": subs})
	}
	c.Render("deeds/survey_mapping.html", Data{"deed": deedObj(d, nil), "groups": groups})
}

func (a *App) deedReprocess(c *Ctx) {
	d := c.deedOr404()
	if d == nil {
		return
	}
	if c.R.Method != http.MethodPost {
		c.W.Header().Set("Allow", "POST")
		http.Error(c.W, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if pipeline.ProcessingFailed(d) && d.SourcePDF != nil && *d.SourcePDF != "" {
		if err := a.Store.ResetForReprocess(c.Context(), d.ID); err != nil {
			c.ServerError(err)
			return
		}
		if _, err := a.Jobs.Enqueue(c.Context(), jobs.KindProcessPDF, map[string]any{"deed_id": d.ID}, timeZero); err != nil {
			c.ServerError(err)
			return
		}
	}
	c.SaveSession()
	c.RedirectTo("deeds:processing", d.PublicID)
}

// --- ajax cascades -----------------------------------------------------------------

func (c *Ctx) jsonOK(v any) {
	c.W.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(c.W).Encode(v)
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (a *App) ajaxSROs(c *Ctx) {
	c.jsonOK(map[string]any{"sros": orEmpty(deeds.SROsForDistrict(c.Query("district")))})
}

func (a *App) ajaxTaluks(c *Ctx) {
	c.jsonOK(map[string]any{"taluks": orEmpty(deeds.TaluksForSRO(c.Query("district"), c.Query("sro")))})
}

func (a *App) ajaxDeedTypes(c *Ctx) {
	c.jsonOK(map[string]any{"types": orEmpty(deeds.DeedTypesForBook(c.Query("book")))})
}

func (a *App) ajaxDeedSubtypes(c *Ctx) {
	subs := []map[string]string{}
	for _, s := range deeds.DeedSubtypesForType(c.Query("book"), c.Query("type")) {
		subs = append(subs, map[string]string{"name": s.Name, "value": s.Value})
	}
	c.jsonOK(map[string]any{"subtypes": subs})
}

func (a *App) ajaxPortalOptions(c *Ctx) {
	known := map[string]string{}
	for k, vs := range c.R.URL.Query() {
		if strings.HasPrefix(k, "p.") && len(vs) > 0 {
			known[k[2:]] = vs[len(vs)-1]
		}
	}
	dd := &deeds.Dropdowns{Src: a.Store}
	texts, _ := dd.OptionTexts(c.Context(), c.Query("page"), c.Query("field"), known)
	c.jsonOK(map[string]any{"options": orEmpty(texts)})
}

var _ = errors.New
var _ = db.ErrNotFound

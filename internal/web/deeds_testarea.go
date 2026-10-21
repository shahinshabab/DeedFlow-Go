package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/shahinshabab/deedflow-go/internal/ai"
	"github.com/shahinshabab/deedflow-go/internal/db"
	"github.com/shahinshabab/deedflow-go/internal/deeds"
	"github.com/shahinshabab/deedflow-go/internal/extract"
	"github.com/shahinshabab/deedflow-go/internal/models"
	"github.com/shahinshabab/deedflow-go/internal/pipeline"
	"github.com/shahinshabab/deedflow-go/internal/store"
	"github.com/shahinshabab/deedflow-go/internal/textconv"
)

// testDashboard is the landing page of the Test area.
func (a *App) testDashboard(c *Ctx) {
	v, err := c.viewer()
	if err != nil {
		c.ServerError(err)
		return
	}
	recent, err := a.Store.RecentTestDeeds(c.Context(), v, 10)
	if err != nil {
		c.ServerError(err)
		return
	}
	var rows []any
	for i := range recent {
		rows = append(rows, deedObj(&recent[i], nil))
	}
	c.Render("deeds/test_dashboard.html", Data{"recent_tests": rows})
}

// entryMap is a schedule entry as the PDF test page reads it.
func entryMap(e extract.Entry) map[string]any {
	dec := func(d *db.Decimal) string {
		if d == nil {
			return ""
		}
		return d.String()
	}
	return map[string]any{
		"schedule_number": e.Number, "district": e.District, "sro": e.SRO, "taluk": e.Taluk, "village": e.Village,
		"desam": e.Desam, "local_body_type": e.LocalBodyType, "local_body_name": e.LocalBodyName,
		"resurvey_block": e.ResurveyBlock, "resurvey_number": e.ResurveyNumber, "resurvey_sub": e.ResurveySub,
		"old_survey": e.OldSurvey, "old_sub": e.OldSub,
		"total_hectare": dec(e.TotalHectare), "total_are": dec(e.TotalAre), "total_sqm": dec(e.TotalSqm),
		"nature_of_right": e.NatureOfRight, "tp_number": e.TPNumber, "buyer_tp_number": e.BuyerTPNumber,
		"details": e.Details, "boundary_east": e.BoundaryEast, "boundary_north": e.BoundaryNorth,
		"boundary_west": e.BoundaryWest, "boundary_south": e.BoundarySouth,
	}
}

// extractPreview shows exactly what the extraction pipeline pulls out of an
// uploaded deed PDF, before any AI structuring.
func (a *App) extractPreview(c *Ctx) {
	var result map[string]any
	if c.R.Method == http.MethodPost {
		c.PostForm()
		name, content, ok, err := uploadedFile(c.R, "pdf_file")
		if err != nil {
			c.ServerError(err)
			return
		}
		if ok {
			pages := extract.ExtractPagesByStructure(content, true)
			te := extract.NewTableExtractor()
			land := extract.ExtractLandType(pages.CombinedText)
			var tablePages []map[string]any
			total := 0
			for _, tp := range pages.TablePages {
				entries := te.ExtractSchedule(tp.Tables)
				total += len(entries)
				var ems []map[string]any
				for _, e := range entries {
					ems = append(ems, entryMap(e))
				}
				tablePages = append(tablePages, map[string]any{
					"page_number": tp.PageNumber, "image_base64": tp.ImageBase64, "entries": ems})
			}
			result = map[string]any{
				"file_name": name, "page_count": len(pages.TextPages) + len(pages.TablePages),
				"text_page_count": len(pages.TextPages), "combined_text": pages.CombinedText, "land_type": land,
				"table_pages": tablePages, "total_entries": total,
			}
		}
	}
	c.Render("deeds/extract_preview.html", Data{"result": result})
}

// aiTest runs extraction and AI structuring on an uploaded PDF and shows the
// raw JSON; nothing is saved.
func (a *App) aiTest(c *Ctx) {
	var result map[string]any
	if c.R.Method == http.MethodPost {
		c.PostForm()
		name, content, ok, err := uploadedFile(c.R, "pdf_file")
		if err != nil {
			c.ServerError(err)
			return
		}
		if ok {
			raw, _, land := extract.ExtractTextAndTables(content)
			converted := textconv.Default().ConvertASCIIToUnicode(raw)
			extracted, err := ai.New(a.Cfg).StructureDeedText(c.Context(), converted)
			if err != nil {
				c.Message("error", fmt.Sprintf("AI structuring failed: %v", err))
			} else {
				var buf bytes.Buffer
				enc := json.NewEncoder(&buf)
				enc.SetEscapeHTML(false)
				enc.SetIndent("", "  ")
				_ = enc.Encode(extracted)
				result = map[string]any{"file_name": name, "land_type": land, "json": string(bytes.TrimRight(buf.Bytes(), "\n"))}
			}
		}
	}
	c.Render("deeds/ai_test.html", Data{"result": result})
}

// seleniumTestCreate starts a Selenium-submission test: a normal deed flagged
// is_test (so it never shows up in the real list), pre-filled across every step.
func (a *App) seleniumTestCreate(c *Ctx) {
	d := &models.Deed{OwnerID: c.User.ID, IsTest: true, Title: "Selenium test"}
	if err := a.Store.CreateDeed(c.Context(), d); err != nil {
		c.ServerError(err)
		return
	}
	if err := pipeline.PopulateDummyDeed(c.Context(), a.Store, d, deeds.Today(a.Cfg.TimeZone)); err != nil {
		c.ServerError(err)
		return
	}
	c.SaveSession()
	c.RedirectTo("deeds:wizard", d.PublicID)
}

var _ = store.NewUUID

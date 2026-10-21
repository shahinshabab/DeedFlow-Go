package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/ai"
	"github.com/shahinshabab/deedflow-go/internal/config"
	"github.com/shahinshabab/deedflow-go/internal/deeds"
	"github.com/shahinshabab/deedflow-go/internal/events"
	"github.com/shahinshabab/deedflow-go/internal/extract"
	"github.com/shahinshabab/deedflow-go/internal/models"
	"github.com/shahinshabab/deedflow-go/internal/store"
)

// ProcessingFailedPrefix starts every PDF-processing failure message, which is
// how the processing page tells a processing failure (safe to run again) from
// a later submission failure (re-running would overwrite the user's edits).
const ProcessingFailedPrefix = "Processing failed"

// ProcessingTimeLimit is how long a processing run may take.
const ProcessingTimeLimit = 20 * time.Minute

// StaleProcessing is how long a processing run may go without a sign of life
// before its deed is marked failed.
const StaleProcessing = ProcessingTimeLimit + 5*time.Minute

// ProcessingStatuses are the statuses of a deed being processed.
var ProcessingStatuses = []string{models.StatusExtracting, models.StatusStructuring}

// Env bundles what the background flows need.
type Env struct {
	Cfg   *config.Config
	Store *store.Store
	Hub   *events.Hub
	AI    *ai.Client
	Loc   *time.Location
}

// GroupFor is the event group of a deed's live status.
func GroupFor(publicID string) string { return "deed_status_" + publicID }

// Broadcast sends a status event to the browsers watching the deed.
func (e *Env) Broadcast(ctx context.Context, deed *models.Deed, event string, extra map[string]any) {
	payload := map[string]any{"type": "deed.update", "event": event}
	for k, v := range extra {
		payload[k] = v
	}
	if err := e.Hub.Publish(ctx, GroupFor(deed.PublicID), payload); err != nil {
		slog.Warn("could not publish a deed event", "deed", deed.PublicID, "err", err)
	}
}

// SetProgress records a run's status and progress, bumps its heartbeat, and
// tells the browser.
func (e *Env) SetProgress(ctx context.Context, deed *models.Deed, status string, progress int, message string, cols ...string) error {
	deed.Status = status
	deed.Progress = int16(progress)
	if len(cols) == 0 {
		cols = []string{"status", "progress"}
	}
	if err := e.Store.SaveDeed(ctx, deed, cols...); err != nil {
		return err
	}
	e.Broadcast(ctx, deed, "progress", map[string]any{"status": deed.Status, "progress": progress, "message": message})
	return nil
}

// ProcessingFailed reports whether the deed's last PDF processing failed.
func ProcessingFailed(d *models.Deed) bool {
	return d.Status == models.StatusFailed && strings.HasPrefix(d.ResultMessage, ProcessingFailedPrefix)
}

// ReapStaleProcessing marks a deed whose PDF processing was cut off as failed,
// so its page offers "Try again" instead of spinning forever.
func ReapStaleProcessing(ctx context.Context, st *store.Store, d *models.Deed) bool {
	if d.Status != models.StatusExtracting && d.Status != models.StatusStructuring {
		return false
	}
	if time.Since(d.UpdatedAt) < StaleProcessing {
		return false
	}
	changed, err := st.MarkStaleProcessing(ctx, d.ID, ProcessingFailedPrefix+
		": it was interrupted before it finished (the server restarted or it ran too long).")
	if err != nil || !changed {
		return false
	}
	slog.Warn("Marked stale PDF processing of deed as failed", "deed", d.PublicID)
	if fresh, err := st.DeedByID(ctx, d.ID); err == nil && fresh != nil {
		*d = *fresh
	}
	return true
}

func (e *Env) readSource(deed *models.Deed) ([]byte, error) {
	if deed.SourcePDF == nil || *deed.SourcePDF == "" {
		return nil, errors.New("this deed has no source PDF")
	}
	clean := filepath.Clean(filepath.FromSlash(*deed.SourcePDF))
	full := filepath.Join(e.Cfg.MediaRoot, clean)
	if !strings.HasPrefix(full, filepath.Clean(e.Cfg.MediaRoot)+string(filepath.Separator)) {
		return nil, errors.New("invalid source PDF path")
	}
	return os.ReadFile(full)
}

// ProcessDeedPDF runs the full "create a deed from a PDF" pipeline: extract
// text/tables, convert the Malayalam font, parse the property schedule,
// structure the deed text with AI and fill in the deed's related rows.
func (e *Env) ProcessDeedPDF(ctx context.Context, deedID int64) (err error) {
	ctx, cancel := context.WithTimeout(ctx, ProcessingTimeLimit)
	defer cancel()
	deed, err := e.Store.DeedByID(ctx, deedID)
	if err != nil {
		return err
	}
	if deed == nil {
		return fmt.Errorf("deed %d does not exist", deedID)
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
		if err == nil {
			return
		}
		slog.Error("PDF processing failed", "deed", deed.PublicID, "err", err)
		// The run's own context may be what ended; record the failure anyway.
		fctx, fcancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer fcancel()
		deed.Status = models.StatusFailed
		deed.ResultMessage = fmt.Sprintf("%s: %v", ProcessingFailedPrefix, err)
		_ = e.Store.SaveDeed(fctx, deed, "status", "result_message")
		e.Broadcast(fctx, deed, "failed", map[string]any{"status": deed.Status, "error": err.Error()})
	}()

	deed.ResultMessage = ""
	if err = e.SetProgress(ctx, deed, models.StatusExtracting, 5, "Reading the PDF…", "status", "progress", "result_message"); err != nil {
		return err
	}
	pdfBytes, err := e.readSource(deed)
	if err != nil {
		return err
	}

	pages := extract.ExtractPagesByStructure(pdfBytes, false)
	text := pages.CombinedText
	if strings.TrimSpace(text) == "" && len(pages.TablePages) == 0 {
		// Otherwise the AI step would be handed nothing and "fill in" a deed from thin air.
		return errors.New("No text could be read from this PDF. It may be a scanned image or damaged - " +
			"upload the PDF exported from the typing software instead.")
	}
	// No land type in the deed: the portal's first one, as the office asked.
	landType := extract.ExtractLandType(text)
	if landType == "" {
		landType = extract.LandTypes[0]
	}
	deed.ExtractedText = text
	if err = e.Store.SaveDeedQuiet(ctx, deed, "extracted_text"); err != nil {
		return err
	}
	if err = e.SetProgress(ctx, deed, models.StatusExtracting, 30, "Extracted the deed text."); err != nil {
		return err
	}

	te := extract.NewTableExtractor()
	var entries []extract.Entry
	for _, tp := range pages.TablePages {
		entries = append(entries, te.ExtractSchedule(tp.Tables)...)
	}
	if err = e.SetProgress(ctx, deed, models.StatusExtracting, 45, "Parsed the property schedule."); err != nil {
		return err
	}

	if err = e.SetProgress(ctx, deed, models.StatusStructuring, 55, "Structuring the deed with AI…"); err != nil {
		return err
	}
	extracted, err := e.AI.StructureDeedText(ctx, text)
	if err != nil {
		return err
	}
	raw, err := marshalExtracted(extracted)
	if err != nil {
		return err
	}
	deed.AIRawResponse = raw
	if err = e.Store.SaveDeedQuiet(ctx, deed, "ai_raw_response"); err != nil {
		return err
	}
	if err = e.SetProgress(ctx, deed, models.StatusStructuring, 85, "Filling in the review form…"); err != nil {
		return err
	}

	dd := &deeds.Dropdowns{Src: e.Store}
	if err = ApplyExtraction(ctx, e.Store, dd, deed, extracted, entries, landType, deeds.Today(e.Loc)); err != nil {
		return err
	}
	fresh, err := e.Store.DeedByID(ctx, deedID)
	if err != nil {
		return err
	}
	*deed = *fresh
	e.Broadcast(ctx, deed, "complete", map[string]any{
		"status": deed.Status, "progress": 100, "redirect_url": fmt.Sprintf("/deeds/%s/wizard/", deed.PublicID),
	})
	return nil
}

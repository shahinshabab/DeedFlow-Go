package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/config"
	"github.com/shahinshabab/deedflow-go/internal/deeds"
	"github.com/shahinshabab/deedflow-go/internal/jobs"
	"github.com/shahinshabab/deedflow-go/internal/media"
	"github.com/shahinshabab/deedflow-go/internal/models"
	"github.com/shahinshabab/deedflow-go/internal/portal"
	"github.com/shahinshabab/deedflow-go/internal/portalcrypt"
)

// Portal is the part of the portal automation a submission uses; it is an
// interface so the orchestration, progress reporting and screenshot storage
// can be verified with a stand-in driver, independent of whether the real
// portal's markup still matches the selectors.
type Portal interface {
	SubmitDeed(p *deeds.Payload, progress portal.Progress, existingToken string, onToken func(string), sections []string) (*portal.SubmitResult, error)
	SubmitRowEdits(p *deeds.Payload, plan deeds.RowPlan, token string, progress portal.Progress) (*portal.RowEditResult, error)
	Close()
}

// PortalFactory starts a portal browser session for an organization's login.
type PortalFactory func(ctx context.Context, cred *models.PortalCredential, deadline time.Time) (Portal, error)

func (e *Env) newPortal(ctx context.Context, cred *models.PortalCredential, deadline time.Time) (Portal, error) {
	if e.NewPortal != nil {
		return e.NewPortal(ctx, cred, deadline)
	}
	a, err := OpenPortal(ctx, e.Cfg, cred, deadline)
	if err != nil {
		return nil, err
	}
	return a, nil
}

// OpenPortal starts a real portal browser session for an organization's
// login: the Selenium grid and portal addresses come from the configuration,
// the password is decrypted with FIELD_ENCRYPTION_KEY.
func OpenPortal(ctx context.Context, cfg *config.Config, cred *models.PortalCredential, deadline time.Time) (*portal.Automation, error) {
	password, _ := portalcrypt.Decrypt(cfg.FieldEncryptionKey, cred.PortalPassword)
	return portal.New(ctx, portal.Config{
		SeleniumURL: cfg.SeleniumRemoteURL, PortalURL: cfg.PortalURL, Tesseract: cfg.TesseractCmd, Headless: true,
	}, portal.Credentials{
		Username: cred.PortalUsername, Password: password,
		NewDeedMenuIndex: int(cred.NewDeedMenuIndex), MyDeedsMenuIndex: int(cred.MyDeedsMenuIndex),
	}, deadline)
}

// Passes over one submission: the first, then up to four more on the same
// token for whatever wasn't confirmed (a dropped session, a portal error
// page, a crashed browser) - while enough of the deadline is left for one.
const (
	MaxSubmissionPasses       = 5
	MinTimeForAnotherPass     = 8 * time.Minute
	submissionLockWaitMessage = "Waiting for another deed's submission to finish…"
)

// Failure texts that mean the portal refused the deed's data (see the
// non-retryable fill errors in the portal package) - not worth repeating.
var dataRefusalMarkers = []string{"rejected", "refused", "not one of the portal's options", "among the portal's options",
	"is blank", "invalid", "required", "won't let it be changed", "no portal transaction-type code"}
var passingErrorMarkers = []string{"try again", "error while", "server error", "logged us out", "browser"}

func anyContains(text string, markers []string) bool {
	for _, m := range markers {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}

// dataRefusals are the sections whose failures were all the portal refusing the data.
func dataRefusals(result *portal.SubmitResult) map[string]bool {
	refused := map[string]bool{}
	if result == nil {
		return refused
	}
	for section, msgs := range result.SectionErrors {
		if len(msgs) == 0 {
			continue
		}
		all := true
		for _, msg := range msgs {
			low := strings.ToLower(msg)
			if !(anyContains(low, dataRefusalMarkers) && !anyContains(low, passingErrorMarkers)) {
				all = false
			}
		}
		if all {
			refused[section] = true
		}
	}
	return refused
}

// SubmitDeedToPortal is the job: it drives the Registration Department portal
// to submit a deed - login, captcha, then every form section - broadcasting
// live progress and saving a screenshot of each section's form right before
// it's submitted (fully filled in), whether that submission then succeeds or
// fails - one per repeated item too. Never the login page.
//
// A job that finds the portal browser busy asks to be run again shortly
// (jobs.RetryLater); the deed's own lock keeps a redelivered job from writing
// to the same token twice.
func (e *Env) SubmitDeedToPortal(ctx context.Context, deedID int64) error {
	ctx, cancel := context.WithTimeout(ctx, SubmissionTimeLimit)
	defer cancel()
	deedLock, ok, err := e.Store.DB.TryLock(ctx, DeedLockKey(deedID))
	if err != nil {
		return err
	}
	if !ok {
		// Another run of this same deed is still going (e.g. a redelivered
		// job) - two runs on one token would duplicate its entries.
		slog.Warn("Deed is already being submitted by another run - skipping this one.", "deed", deedID)
		return nil
	}
	defer deedLock.Release()
	deed, err := e.Store.DeedByID(ctx, deedID)
	if err != nil {
		return err
	}
	if deed == nil {
		return fmt.Errorf("deed %d does not exist", deedID)
	}
	if deed.Status != models.StatusSubmitting {
		// Only the submit view puts a deed in this state; anything else is a
		// stale/redelivered job for a run that was already settled.
		slog.Warn("Deed is not submitting - ignoring a stale job.", "deed", deed.PublicID, "status", deed.Status)
		return nil
	}
	browserLock, ok, err := e.Store.DB.TryLock(ctx, BrowserLockKey())
	if err != nil {
		return err
	}
	if !ok {
		// The grid's one Chrome is busy with another deed. Asking it for a
		// second session used to queue inside the grid and then fail with
		// "Could not start a browser session"; come back later.
		_ = e.Store.TouchDeed(ctx, deed.ID) // still alive, just queued
		e.Broadcast(ctx, deed, "progress", map[string]any{"status": deed.Status, "progress": 0, "message": submissionLockWaitMessage})
		return jobs.RetryLater{After: BrowserWaitRetry}
	}
	defer browserLock.Release()
	return e.submitDeed(ctx, deedID)
}

// failDeed records a failed submission and tells the browser.
func (e *Env) failDeed(ctx context.Context, deed *models.Deed, message string) {
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	deed.Status = models.StatusFailed
	deed.ResultMessage = message
	if err := e.Store.SaveDeedQuiet(fctx, deed, "status", "result_message"); err != nil {
		slog.Error("could not record a failed submission", "deed", deed.PublicID, "err", err)
	}
	e.Broadcast(fctx, deed, "failed", map[string]any{"status": deed.Status, "error": message})
}

func (e *Env) completeDeed(ctx context.Context, deed *models.Deed, cols ...string) {
	if err := e.Store.SaveDeedQuiet(ctx, deed, cols...); err != nil {
		slog.Error("could not record a finished submission", "deed", deed.PublicID, "err", err)
	}
	e.Broadcast(ctx, deed, "complete", map[string]any{
		"status": deed.Status, "progress": 100,
		"redirect_url": "/deeds/" + deed.PublicID + "/", "token_number": deed.TokenNumber,
	})
}

// EnsureBaseline records the portal baseline of a deed that already has a token.
func (e *Env) EnsureBaseline(ctx context.Context, deed *models.Deed) error {
	data, err := e.Store.LoadDeedData(ctx, deed)
	if err != nil {
		return err
	}
	if snap, changed := deeds.EnsurePortalBaseline(data); changed {
		deed.PortalSnapshot = snap
		return e.Store.SaveDeedQuiet(ctx, deed, "portal_snapshot")
	}
	return nil
}

func (e *Env) buildPayload(ctx context.Context, deed *models.Deed) (*deeds.Payload, *deeds.DeedData, error) {
	data, err := e.Store.LoadDeedData(ctx, deed)
	if err != nil {
		return nil, nil, err
	}
	p, err := deeds.BuildPayload(data)
	return p, data, err
}

func (e *Env) submitDeed(ctx context.Context, deedID int64) error {
	deed, err := e.Store.DeedByID(ctx, deedID)
	if err != nil || deed == nil {
		return fmt.Errorf("deed %d: %v", deedID, err)
	}
	var cred *models.PortalCredential
	if deed.OrganizationID != nil {
		if cred, err = e.Store.PortalCredentialFor(ctx, *deed.OrganizationID); err != nil {
			return err
		}
	}
	if cred == nil {
		e.failDeed(ctx, deed, "No portal login is set for this organization. Ask your organization's admin to add it.")
		return nil
	}
	if err := e.runSubmission(ctx, deed, cred); err != nil {
		if portal.IsPortalError(err) {
			slog.Error("Portal submission failed", "deed", deed.PublicID, "err", err)
			msg := err.Error()
			// A token the portal won't let us use wasn't created by this run.
			if !portal.IsTokenUnavailable(err) {
				msg = WithResumeHint(msg, deed)
			}
			e.failDeed(ctx, deed, msg)
			return nil
		}
		slog.Error("Unexpected error during portal submission", "deed", deed.PublicID, "err", err)
		text := portal.Short(err)
		if ctx.Err() != nil {
			text = "the submission ran out of time"
		}
		e.failDeed(ctx, deed, WithResumeHint("Unexpected error: "+text, deed))
		return err
	}
	return nil
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func (e *Env) runSubmission(ctx context.Context, deed *models.Deed, cred *models.PortalCredential) error {
	deed.Progress = 0
	if err := e.Store.SaveDeed(ctx, deed, "progress"); err != nil {
		return err
	}
	payload, data, err := e.buildPayload(ctx, deed)
	if err != nil {
		return err
	}
	fingerprints := deeds.SectionFingerprints(payload)
	existingToken := deed.TokenNumber
	var sections []string // nil = everything
	var rowPlan deeds.RowPlan
	var whole []string
	if existingToken != "" {
		// Update-token flow: only the categories edited since they were last
		// confirmed on the portal are sent (each cleared there and re-entered).
		if err := e.EnsureBaseline(ctx, deed); err != nil {
			return err
		}
		sections = []string{}
		for _, sec := range deeds.PortalSections {
			if deed.PortalSnapshot.Sections[sec.Key] != fingerprints[sec.Key] {
				sections = append(sections, sec.Key)
			}
		}
	}
	if sections == nil || contains(sections, "presenter") {
		// Whenever the Presentation page is sent, a presenting date that has
		// passed since the deed was made becomes today (the portal refuses it
		// otherwise), and blank stamp date / filing sheets get defaults.
		if data.Presenter != nil {
			if changed := deeds.ApplyPresenterDefaults(data.Presenter, deeds.Today(e.Loc)); len(changed) > 0 {
				if err := e.Store.SavePresenter(ctx, data.Presenter); err != nil {
					return err
				}
				if payload, data, err = e.buildPayload(ctx, deed); err != nil {
					return err
				}
				fingerprints = deeds.SectionFingerprints(payload)
			}
		}
	}
	if existingToken != "" {
		if len(sections) == 0 {
			deed.Status = models.StatusCompleted
			deed.Progress = 100
			deed.ResultMessage = fmt.Sprintf("Nothing was edited since the last submission - token %s is up to date.", existingToken)
			e.completeDeed(ctx, deed, "status", "progress", "result_message")
			return nil
		}
		rowPlan, whole = deeds.PlanRowEdits(deed.PortalSnapshot, payload, sections)
		var what []string
		for _, list := range rowPlan.Lists {
			for _, ch := range rowPlan.Changes[list] {
				what = append(what, deeds.DescribeRowChange(list, ch, payload))
			}
		}
		for _, s := range whole {
			label := deeds.SectionLabel(s)
			if deeds.ListsOf(s) != nil {
				label += " (all entries)"
			}
			what = append(what, label)
		}
		e.Broadcast(ctx, deed, "progress", map[string]any{"status": deed.Status, "progress": 0,
			"message": fmt.Sprintf("Updating token %s: %s", existingToken, strings.Join(what, ", "))})
	} else {
		e.Broadcast(ctx, deed, "progress", map[string]any{"status": deed.Status, "progress": 0, "message": "Starting the portal submission…"})
	}

	if problems := deeds.PreflightProblems(payload, sections, deeds.Today(e.Loc)); len(problems) > 0 {
		e.failDeed(ctx, deed, "Not sent to the portal - please fix first: "+strings.Join(problems, " "))
		return nil
	}

	progress := func(stage string, percent int, ok bool, message string, shots [][]byte) {
		deed.Progress = int16(percent)
		// updated_at doubles as the run's heartbeat (ReapStaleSubmission).
		if err := e.Store.SaveDeed(ctx, deed, "progress"); err != nil {
			slog.Warn("could not record progress", "deed", deed.PublicID, "err", err)
		}
		// shots is a list - one per scroll position down that section's page
		// (see portal.Automation.captureScrollingScreenshots) - so a long form
		// is fully visible across the set of saved rows, not just cropped to
		// its top portion.
		var screenshotURL any
		for i, png := range shots {
			label := stage
			if len(shots) != 1 {
				label = fmt.Sprintf("%s_p%d", stage, i+1)
			}
			name, err := media.Save(e.Cfg.MediaRoot, "deed_screenshots/"+label+".png", png)
			if err == nil {
				_, err = e.Store.AddScreenshot(ctx, deed.ID, label, name)
			}
			if err != nil {
				slog.Warn("could not store a screenshot", "deed", deed.PublicID, "label", label, "err", err)
				continue
			}
			if i == 0 {
				screenshotURL = e.Cfg.MediaURL + name
			}
		}
		e.Broadcast(ctx, deed, "progress", map[string]any{
			"status": deed.Status, "progress": percent, "message": message, "stage": stage, "ok": ok,
			"screenshot_url": screenshotURL,
		})
	}

	onToken := func(token string) {
		// Saved the moment the portal assigns it, not at the end: if this run
		// dies later, the next submit must resume this token rather than
		// create a second one.
		if token != "" && token != deed.TokenNumber {
			deed.TokenNumber = token
			if err := e.Store.SaveDeedQuiet(ctx, deed, "token_number"); err != nil {
				slog.Error("could not record the token number", "deed", deed.PublicID, "token", token, "err", err)
			}
			e.Broadcast(ctx, deed, "progress", map[string]any{"status": deed.Status, "progress": deed.Progress,
				"message": "Portal token number: " + token, "token_number": token})
		}
	}

	deadline := time.Now().Add(SubmissionDeadline)
	requested := sections
	rowSections := map[string]bool{}
	if existingToken != "" && len(rowPlan.Lists) > 0 {
		// Edit pipeline: only the changed rows, each through its own edit
		// action on the portal.
		for _, s := range sections {
			if !contains(whole, s) {
				rowSections[s] = true
			}
		}
		done, failed, err := e.sendRowEdits(ctx, deed, payload, rowPlan, cred, progress, deadline)
		if err != nil {
			return err
		}
		if len(whole) == 0 {
			if len(failed) > 0 {
				deed.Status = models.StatusFailed
				deed.ResultMessage = fmt.Sprintf("Edit failed on the portal: %s. Token number: %s", strings.Join(failed, "; "), deed.TokenNumber)
			} else {
				deed.Status = models.StatusCompleted
				deed.ResultMessage = fmt.Sprintf("Edit finished on the portal (%s). Token number: %s", strings.Join(done, ", "), deed.TokenNumber)
			}
			deed.Progress = 100
			e.completeDeed(ctx, deed, "status", "progress", "result_message")
			return nil
		}
		sections = whole
	}

	anotherPass := func(passes int) bool {
		return deed.TokenNumber != "" && passes < MaxSubmissionPasses && time.Until(deadline) > MinTimeForAnotherPass
	}
	passes := 0
	var result *portal.SubmitResult
	for {
		passes++
		auto, err := e.newPortal(ctx, cred, deadline)
		if err != nil {
			return err
		}
		result, err = auto.SubmitDeed(payload, progress, deed.TokenNumber, onToken, sections)
		auto.Close()
		if err != nil {
			if !(portal.IsPortalError(err) || portal.IsSessionLost(err)) {
				return err
			}
			if portal.IsTokenUnavailable(err) || !anotherPass(passes) {
				if portal.IsSessionLost(err) {
					return portal.NewError("Lost the portal session: %s", err)
				}
				return err
			}
			result = nil
			slog.Warn("Pass stopped - resuming on the same token", "pass", passes, "deed", deed.PublicID, "err", err, "token", deed.TokenNumber)
		}
		if result != nil {
			if result.TokenNumber != "" {
				deed.TokenNumber = result.TokenNumber
			}
			// Record exactly what the portal now holds, section by section - a
			// section that failed keeps its old fingerprint and stays pending
			// for the next pass / resubmission.
			snapshot := deed.PortalSnapshot.Clone()
			sectionsOK := map[string]bool{}
			for k, v := range result.SectionsOK {
				sectionsOK[k] = v
			}
			if ok, has := sectionsOK["document_link"]; has && !ok {
				// Links aren't a section of their own; the schedule is what re-sends them.
				sectionsOK["schedule"] = false
			}
			var confirmed []string
			for s, ok := range sectionsOK {
				if _, known := fingerprints[s]; ok && known {
					confirmed = append(confirmed, s)
				}
			}
			sort.Strings(confirmed)
			for _, s := range confirmed {
				snapshot.Sections[s] = fingerprints[s]
			}
			// Row by row too, so a later edit of one row can be sent alone.
			snapshot = deeds.RecordRows(snapshot, payload, confirmed)
			deed.PortalSnapshot = snapshot
			if err := e.Store.SaveDeedQuiet(ctx, deed, "token_number", "portal_snapshot"); err != nil {
				return err
			}
			if result.OverallOK || !anotherPass(passes) {
				break
			}
		}
		// Another pass on the same token, for the sections not confirmed yet -
		// minus any the portal refused on their data, which would only be
		// refused again.
		if err := e.EnsureBaseline(ctx, deed); err != nil {
			return err
		}
		refused := dataRefusals(result)
		var retry []string
		for _, sec := range deeds.PortalSections {
			if deed.PortalSnapshot.Sections[sec.Key] != fingerprints[sec.Key] && !refused[sec.Key] && !rowSections[sec.Key] {
				retry = append(retry, sec.Key)
			}
		}
		if len(retry) == 0 {
			if result == nil {
				return portal.NewError("Stopped before every section could be confirmed.")
			}
			break
		}
		sections = retry
		slog.Warn("Pass left sections unconfirmed - going over them again", "deed", deed.PublicID, "pass", passes,
			"sections", strings.Join(retry, ", "), "token", deed.TokenNumber)
		labels := make([]string, len(sections))
		for i, s := range sections {
			labels[i] = deeds.SectionLabel(s)
		}
		e.Broadcast(ctx, deed, "progress", map[string]any{"status": deed.Status, "progress": deed.Progress,
			"message": fmt.Sprintf("Some sections weren't confirmed - going over them again on token %s: %s",
				deed.TokenNumber, strings.Join(labels, ", "))})
	}

	// Done only when every section is confirmed on the portal - not just the
	// ones the last pass happened to cover.
	allConfirmed := true
	for _, sec := range deeds.PortalSections {
		if deed.PortalSnapshot.Sections[sec.Key] != fingerprints[sec.Key] {
			allConfirmed = false
		}
	}
	if result.OverallOK && allConfirmed {
		deed.Status = models.StatusCompleted
	} else {
		deed.Status = models.StatusFailed
	}
	deed.Progress = 100
	if deed.Status == models.StatusCompleted {
		if len(requested) > 0 {
			labels := make([]string, len(requested))
			for i, s := range requested {
				labels[i] = deeds.SectionLabel(s)
			}
			deed.ResultMessage = fmt.Sprintf("Updated on the portal (%s). Token number: %s", strings.Join(labels, ", "), deed.TokenNumber)
		} else {
			deed.ResultMessage = "Submitted successfully. Token number: " + deed.TokenNumber
		}
	} else {
		deed.ResultMessage = "Submitted with some sections not confirmed - see the detail page. Token number: " + deed.TokenNumber
	}
	e.completeDeed(ctx, deed, "token_number", "portal_snapshot", "status", "progress", "result_message")
	return nil
}

func rowsEqual(a, b []models.RowFP) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// sendRowEdits runs the edit pipeline and records what landed: each confirmed
// row in the deed's row record, and a section as a whole once all its rows
// match. A token that isn't in My Token (or can't be edited any more) comes
// back as a portal error with nothing changed.
func (e *Env) sendRowEdits(ctx context.Context, deed *models.Deed, payload *deeds.Payload, plan deeds.RowPlan,
	cred *models.PortalCredential, progress portal.Progress, deadline time.Time) (done, failed []string, err error) {
	auto, err := e.newPortal(ctx, cred, deadline)
	if err != nil {
		return nil, nil, err
	}
	result, err := auto.SubmitRowEdits(payload, plan, deed.TokenNumber, progress)
	auto.Close()
	if err != nil {
		if portal.IsSessionLost(err) {
			return nil, nil, portal.NewError("Lost the portal session: %s", err)
		}
		return nil, nil, err
	}

	current := deeds.RowFingerprints(payload)
	snapshot := deed.PortalSnapshot.Clone()
	rows := map[string][]models.RowFP{}
	for name, v := range snapshot.Rows {
		rows[name] = append([]models.RowFP(nil), v...)
	}
	for _, name := range plan.Lists {
		changes := append([]deeds.RowChange(nil), plan.Changes[name]...)
		sort.SliceStable(changes, func(x, y int) bool { return changes[x].Index < changes[y].Index })
		for _, change := range changes {
			i := change.Index
			label := deeds.DescribeRowChange(name, change, payload)
			if result.RowsOK[name][i] {
				done = append(done, label)
				switch {
				case i < len(rows[name]):
					rows[name][i] = current[name][i]
				case i == len(rows[name]):
					rows[name] = append(rows[name], current[name][i])
				}
				continue
			}
			message := result.SectionResults[fmt.Sprintf("%s_%d", name, i+1)].Message
			if message == "" {
				message = "not attempted"
			}
			prefix := label
			if k := strings.Index(prefix, " ("); k >= 0 {
				prefix = prefix[:k]
			}
			if strings.HasPrefix(message, prefix) {
				failed = append(failed, message)
			} else {
				failed = append(failed, label+": "+message)
			}
		}
	}
	snapshot.Rows = rows
	fingerprints := deeds.SectionFingerprints(payload)
	for _, rl := range deeds.RowLists {
		match := true
		for _, name := range rl.Lists {
			held, ok := rows[name]
			if !ok || !rowsEqual(held, current[name]) {
				match = false
			}
		}
		if match {
			snapshot.Sections[rl.Section] = fingerprints[rl.Section]
		}
	}
	deed.PortalSnapshot = snapshot
	if err := e.Store.SaveDeedQuiet(ctx, deed, "portal_snapshot"); err != nil {
		return nil, nil, err
	}
	return done, failed, nil
}

var _ = errors.New

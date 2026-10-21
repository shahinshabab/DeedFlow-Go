package portal

import (
	"fmt"
	"strings"

	"github.com/shahinshabab/deedflow-go/internal/deeds"
	"github.com/shahinshabab/deedflow-go/internal/webdriver"
)

// SubmitResult is what SubmitDeed reports: {token_number, section_results,
// sections_ok, overall_ok}. SectionResults holds each stage's outcome;
// SectionErrors is the "<section>_errors" lists of failure messages.
type SubmitResult struct {
	TokenNumber    string
	SectionResults map[string]StageResult
	SectionErrors  map[string][]string
	SectionsOK     map[string]bool
	OverallOK      bool
}

// runStep runs one step, recovering from a lost session that escaped the
// step's own handling - a single section's crash never aborts the sections
// after it. A one-off browser error (e.g. Chrome's renderer hanging for 90s
// while a page reloads after a dialog) gets one more try on a fresh page;
// steps are safe to repeat, since every insert first checks what the table
// already holds.
func (a *Automation) runStep(fn func() StepResult, token string) StepResult {
	if token != "" {
		a.token = token
	}
	browserErrors := 0
	for i := 0; i < 3; i++ {
		a.checkDeadline()
		var res StepResult
		err := try(func() { res = fn() })
		if err == nil {
			return res
		}
		var sl *SessionLostError
		switch {
		case asErr(err, &sl):
			a.log.Warn("Session lost", "err", sl.Msg)
			a.recoverSession(token, sl.Msg)
		case isPortalErr(err):
			panic(err)
		case isWD(err):
			// A hung renderer would hang the session check too.
			rendererHung := strings.Contains(strings.ToLower(err.Error()), "renderer")
			if !rendererHung && a.sessionLost() {
				a.recoverSession(token, short(err))
				continue
			}
			browserErrors++
			if browserErrors > 1 {
				a.log.Error("Unexpected browser error in a step", "err", err)
				return StepResult{Message: "Unexpected browser error: " + short(err), Screenshots: a.safeScreenshots(), Retryable: true}
			}
			a.log.Warn("Browser error in a step, retrying it on a fresh page", "err", short(err))
			if rendererHung {
				a.restartDriver()
			}
			a.recoverSession(token, short(err))
		default:
			panic(err)
		}
	}
	return fn()
}

// SubmitDeed walks a deed through the portal form.
//
// progress is called after each stage. existingToken updates that token
// instead of creating a new one; onToken is called as soon as a token number
// is known. sections (only with existingToken) limits the run to those
// categories - each is cleared on the portal and re-entered - plus whatever
// ExpandSections says must be redone with them. Without it, everything is
// submitted.
func (a *Automation) SubmitDeed(p *deeds.Payload, progress Progress, existingToken string, onToken func(string), sections []string) (res *SubmitResult, err error) {
	err = try(func() { res = a.submitDeed(p, progress, existingToken, onToken, sections) })
	return res, err
}

func (a *Automation) submitDeed(p *deeds.Payload, progress Progress, existingToken string, onToken func(string), sections []string) *SubmitResult {
	wanted := map[string]bool{}
	if sections == nil || existingToken == "" {
		for _, s := range []string{"registration", "presenter", "prior_documents", "executants", "claimants",
			"schedule", "witnesses", "document_link"} {
			wanted[s] = true
		}
	} else {
		wanted = ExpandSections(sections)
	}
	cumulative := 0.0
	res := &SubmitResult{SectionResults: map[string]StageResult{}, SectionErrors: map[string][]string{}}
	token := ""

	report := func(stage string, ok bool, message string, shots [][]byte, weight float64) {
		if weight < 0 {
			w, found := stageWeights[stage]
			if !found {
				w = 5
			}
			weight = w
		}
		cumulative += weight
		a.log.Info("Portal stage", "ok", ok, "stage", stage, "message", message)
		pct := cumulative
		if pct > 99 {
			pct = 99
		}
		progress(stage, int(pct+0.5), ok, message, shots)
		res.SectionResults[stage] = StageResult{ok, message}
	}
	fail := func(stage, message string) { res.SectionErrors[stage] = append(res.SectionErrors[stage], message) }

	// clear deletes every existing record on the tab. false = it couldn't, and
	// the caller then enters nothing there (never old + new mixed). required
	// false: a failed clear is reported but isn't an error of the section by itself.
	clear := func(stage string, t tab, what string, required bool) bool {
		run := func() StepResult {
			var r StepResult
			err := try(func() { r = a.clearTab(t, what) })
			if err != nil {
				var fe *fillError
				if asErr(err, &fe) {
					return StepResult{Message: fe.msg, Screenshots: a.safeScreenshots(), Retryable: fe.retryable}
				}
				panic(err)
			}
			return r
		}
		result := a.runStep(run, token)
		if !result.OK {
			if required {
				fail(stage, result.Message)
			}
			report(stage+"_clear", false, result.Message, result.Screenshots, 0)
			return false
		}
		if len(result.Screenshots) > 0 { // only when something was actually deleted
			report(stage+"_clear", true, result.Message, result.Screenshots, 0)
		}
		return true
	}

	// section is the update-token flow for one tab: delete every existing
	// record, then enter this deed's records fresh, one save per record.
	section := func(stage string, t tab, what string, count int, labelOf func(i int) string, fill func(i int),
		save webdriver.By, firstNumber, total int) {
		if total == 0 {
			total = count
		}
		weightOf := stageWeights[stage]
		if weightOf == 0 {
			weightOf = 5
		}
		weight := weightOf / float64(max(total, 1))
		stageName := func(i int) string {
			if total == 1 {
				return stage
			}
			return fmt.Sprintf("%s_%d", stage, firstNumber+i-1)
		}
		cleared := clear(stage, t, what, true)
		for i := 1; i <= count; i++ {
			if !cleared {
				message := labelOf(i-1) + ": not entered - the existing records couldn't be deleted first."
				fail(stage, message)
				report(stageName(i), false, message, nil, weight)
				continue
			}
			result := a.runStep(func() StepResult {
				return a.insertRow(t, labelOf(i-1), func() { fill(i - 1) }, save, i, token)
			}, token)
			if !result.OK {
				fail(stage, result.Message)
			}
			report(stageName(i), result.OK, result.Message, result.Screenshots, weight)
		}
	}

	a.EnsureLoggedIn(10)
	report("login", true, "Logged in.", nil, -1)

	// Create-token flow: registration + presenter, only ever for a deed that
	// has no token yet (nothing reaches the portal before the presenter save
	// assigns the token). Everything after that is the update-token flow
	// below, the same for a new or an existing token.
	if existingToken != "" {
		// The token must be this login's own and still editable before
		// anything on it is touched (a token typed in by hand for a "Continue
		// with existing token" deed may be neither).
		a.runStep(func() StepResult { a.FindToken(existingToken); return StepResult{OK: true} }, "")
		a.runStep(func() StepResult { a.OpenToken(existingToken); return StepResult{OK: true} }, "")
		token = existingToken
		if wanted["registration"] {
			// District/SRO/book/type are fixed once the token exists - the
			// portal has no way to change them on this token.
			fail("registration", "Registration details can't be changed on an existing token.")
			report("registration", false, "Registration details can't be changed once the token exists "+
				"- only a new deed/token can use different ones.", nil, -1)
		}
		if wanted["presenter"] {
			result := a.runStep(func() StepResult { return a.updatePresenter(p.Presenter, token) }, token)
			if !result.OK {
				fail("presenter", result.Message)
			}
			report("presenter", result.OK, result.Message, result.Screenshots, -1)
		}
	} else {
		token = a.createToken(p, func(stage string, ok bool, msg string, shots [][]byte) { report(stage, ok, msg, shots, -1) })
	}
	if onToken != nil {
		onToken(token)
	}

	// Document links point at PR documents and properties, so they're cleared
	// before those are deleted. They aren't re-made: a wrong link is easy to
	// miss, so the office links documents, executants and claimants by hand
	// (shahin, 2026-10-07).
	if wanted["document_link"] {
		// Clearing the prior documents and properties below removes their
		// links too - so links that won't delete here aren't fatal.
		clear("document_link", tabDocLink, "Document links", false)
	}

	if wanted["prior_documents"] {
		section("prior_documents", tabPriorDoc, "PR documents", len(p.PriorDocuments),
			func(i int) string {
				d := p.PriorDocuments[i]
				return fmt.Sprintf("Prior document %s/%s", d.Number, d.Year)
			},
			func(i int) { a.fillPriorDocumentForm(p.PriorDocuments[i]) },
			webdriver.ByXPath("//input[@name='Save' and @type='button']"), 1, 0)
	}
	// Cleared *after* the PR documents are saved: saving a PR document that
	// matches a real past registration makes the portal auto-add that
	// document's claimants as bare-bones executants - clearing here removes
	// those too, leaving exactly this deed's executants.
	if wanted["executants"] {
		section("executants", tabExecutant, "Executants", len(p.Executants),
			func(i int) string { return "Executant " + p.Executants[i].Name },
			func(i int) { e := p.Executants[i]; a.fillPartyForm(e.PartyPayload, e.Type, "ddlExecutantType") },
			webdriver.ByID("saveForm2"), 1, 0)
	}
	if wanted["claimants"] {
		section("claimants", tabClaimant, "Claimants", len(p.Claimants),
			func(i int) string { return "Claimant " + p.Claimants[i].Name },
			func(i int) { a.fillPartyForm(p.Claimants[i], "", "") },
			webdriver.ByID("saveForm"), 1, 0)
	}
	if wanted["schedule"] {
		section("schedule", tabProperty, "Properties", len(p.Schedule),
			func(i int) string { return "Property " + p.Schedule[i].Number },
			func(i int) { a.fillScheduleForm(p.Schedule[i], false) },
			webdriver.ByName("Save"), 1, 0)
	}

	if wanted["witnesses"] {
		attesting, identifying := p.AttestingWitnesses, p.IdentifyingWitnesses
		total := len(attesting) + len(identifying)
		section("witnesses", tabAttestWitness, "Attesting witnesses", len(attesting),
			func(i int) string { return "Attesting witness " + attesting[i].Name },
			func(i int) { a.fillWitnessForm(attesting[i], true) },
			webdriver.ByID("saveForm2"), 1, total)
		section("witnesses", tabIdentWitness, "Identifying witnesses", len(identifying),
			func(i int) string { return "Identifying witness " + identifying[i].Name },
			func(i int) { a.fillWitnessForm(identifying[i], false) },
			webdriver.ByID("saveForm2"), len(attesting)+1, total)
	}

	res.SectionsOK = map[string]bool{}
	for s := range wanted {
		_, failed := res.SectionErrors[s]
		res.SectionsOK[s] = !failed
	}
	res.OverallOK = len(res.SectionErrors) == 0
	progress("complete", 100, res.OverallOK, "Submission complete.", a.safeScreenshots())
	res.TokenNumber = token
	return res
}

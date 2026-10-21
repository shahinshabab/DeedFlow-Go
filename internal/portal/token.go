package portal

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/shahinshabab/deedflow-go/internal/deeds"
	"github.com/shahinshabab/deedflow-go/internal/webdriver"
)

// isPortalErr is `except PortalAutomationError` (which includes
// TokenUnavailableError, its subclass).
func isPortalErr(err error) bool {
	var pe *Error
	var tu *TokenUnavailableError
	return asErr(err, &pe) || asErr(err, &tu)
}

// --- token creation (registration + presenter) -----------------------------------

func (a *Automation) fillRegistration(d deeds.RegistrationPayload) StepResult {
	var shots [][]byte
	var dialogs []string
	err := try(func() {
		a.present(webdriver.ByID("ddDistrict"), 20)
		for _, f := range []kv{
			{"ddDistrict", d.District}, {"ddSro", d.SRO}, {"ddTaluk", d.Taluk},
			{"selddbookno", d.BookLabel}, {"selddMainTtype", d.RegistrationType},
			{"selddTtype", d.RegistrationSubType},
		} {
			if strings.TrimSpace(f.val) == "" {
				panic(fillErrorf(false, "Registration value for '%s' is blank.", f.id))
			}
			a.cascade(f.id, f.val)
		}
		shots = a.captureScrollingScreenshots(6)

		a.jsClick(a.clickable(webdriver.ByID("saveForm"), defaultWait))
		// The "PEARLPUBLIC" recap modal - its OK ("Proceed") is what actually
		// moves on to the document-details stage.
		if a.presentOrNil(webdriver.ByID("popup_ok"), 10) != nil {
			a.jsClick(a.find(webdriver.ByID("popup_ok")))
		}
		dialogs = a.handleDialogs(2, 6)
	})
	if err != nil {
		var fe *fillError
		var sl *SessionLostError
		switch {
		case asErr(err, &fe):
			return StepResult{Message: fe.msg, Screenshots: a.safeScreenshots(), Retryable: fe.retryable}
		case asErr(err, &sl):
			panic(err)
		case isWD(err):
			return StepResult{Message: "Error on the registration page: " + short(err), Screenshots: a.safeScreenshots(), Retryable: true}
		}
		panic(err)
	}
	// Success = the document-details stage actually opened.
	if a.presentOrNil(webdriver.ByID("li_appln_det"), 20) != nil {
		return StepResult{OK: true, Message: "Registration saved.", Screenshots: shots, Retryable: true}
	}
	if rejected := rejections(dialogs); len(rejected) > 0 {
		return StepResult{Message: "Portal rejected the registration: " + rejected[0], Screenshots: shots, Retryable: false}
	}
	return StepResult{Message: "Registration did not reach the document-details stage.", Screenshots: shots, Retryable: true}
}

// fillPresenter fills and saves the Presentation page. On a new token this is
// the save that makes the portal assign the token number; with update=true
// (token already open) it re-saves the presenter via the page's Update button
// instead.
func (a *Automation) fillPresenter(d deeds.PresenterPayload, update bool) StepResult {
	var shots [][]byte
	var dialogs []string
	err := try(func() {
		if !update {
			a.jsClick(a.present(webdriver.ByID("li_appln_det"), defaultWait))
		}
		link := a.present(webdriver.ByXPath("//a[contains(text(), 'Presentation')]"), defaultWait)
		a.jsClick(link)
		// Page-load notices (maintenance warnings etc.) are cleared here,
		// before filling, so they're never mistaken for a rejection of one of
		// the presenter's fields below.
		if notices := a.handleDialogs(1.5, 6); len(notices) > 0 {
			a.log.Info("Portal notice(s) on the presenter page", "notices", notices)
		}
		a.visible(webdriver.ByID("presentCode"), defaultWait)
		a.handleDialogs(0.3, 6)

		if d.StampMode == "Physical Paper" {
			a.jsClick(a.find(webdriver.ByID("estamp_rqd_s")))
		} else {
			a.jsClick(a.find(webdriver.ByID("estamp_rqd_e")))
		}
		// Gender and ID type before docamt/amtAssessed - the portal blocks
		// those with "Please Select ID Type" otherwise.
		if d.Gender != "" {
			a.selectExact("selSex", d.Gender, false)
		}
		if d.IDType != "" {
			a.selectExact("ddIdCard", d.IDType, false)
		}
		fields := []kv{
			{"presentCode", d.Type}, {"presentDate", d.Date}, {"presentName", d.Name}, {"presentAge", d.Age},
			{"rName", d.Relative}, {"profession", d.Job}, {"houseName", d.HouseName}, {"presenterdesam", d.Place},
			{"postOffice", d.Post}, {"pincode", d.Pin}, {"phone", d.Mobile}, {"idNo", d.IDNumber},
			{"docamt", d.DocumentValue}, {"stampDate", d.StampDate}, {"txtfilingsheet", d.DocumentSheet},
			{"executionDate", d.Date}, {"amtAssessed", d.DocumentFees},
		}
		if d.StampMode != "Physical Paper" {
			// E-Stamp hides the document value / amount assessed rows (the
			// e-stamp certificate carries them) - waiting for them to become
			// typable timed out and failed every E-Stamp deed. Where the rows
			// do show and are typable, they still get filled; a value the
			// portal holds and won't let us change must match the deed, or the
			// property save is refused later with "Total amount ... should be
			// equal to Document amount".
			var rest []kv
			for _, f := range fields {
				if f.id != "docamt" && f.id != "amtAssessed" {
					rest = append(rest, f)
					continue
				}
				found := a.findAll(webdriver.ByID(f.id))
				if len(found) == 0 {
					continue
				}
				box := found[0]
				if a.displayed(box) && a.enabled(box) && a.attr(box, "readonly") == "" {
					a.fillText(f.id, f.val)
				} else if f.id == "docamt" {
					held := digitsOnly(a.attr(box, "value"))
					if held != "" && digitsOnly(f.val) != "" && held != digitsOnly(f.val) {
						panic(fillErrorf(false, "The portal holds a document value of %s for this e-stamp token and won't "+
							"let it be changed, but this deed's document value is %s.", held, digitsOnly(f.val)))
					}
				}
			}
			fields = rest
		}
		for _, f := range fields {
			a.fillText(f.id, f.val)
			a.sleep(0.3)
			// Some of these fields validate on change with an alert (e.g. a
			// date format) that would otherwise block the rest.
			if early := rejections(a.handleDialogs(0.2, 6)); len(early) > 0 {
				panic(fillErrorf(false, "Portal rejected presenter field '%s': %s", f.id, early[0]))
			}
		}
		a.selectCascade("ddRtype", d.Relation, 0.5, false, 20)

		if copies := d.DocumentCopies; 0 < copies && copies <= 20 {
			a.clickRadio("cduplicate", "1")
			a.exec("document.getElementById('txtDupno').value = arguments[0];", strconv.Itoa(copies))
		}

		shots = a.captureScrollingScreenshots(6)
		save := webdriver.ByID("saveForm")
		if update {
			save = webdriver.ByXPath("//input[@type='button' and (@id='update' or @value='Update')]")
		}
		a.jsClick(a.present(save, defaultWait))
		dialogs = a.awaitSaveOutcome(90, 3)
	})
	if err != nil {
		var fe *fillError
		var sl *SessionLostError
		switch {
		case asErr(err, &fe):
			return StepResult{Message: fe.msg, Screenshots: a.safeScreenshots(), Retryable: fe.retryable}
		case asErr(err, &sl):
			panic(err)
		case isWD(err):
			return StepResult{Message: "Error on the presenter page: " + short(err), Screenshots: a.safeScreenshots(), Retryable: true}
		}
		panic(err)
	}
	if rejected := rejections(dialogs); len(rejected) > 0 {
		return StepResult{Message: "Portal rejected the presenter save: " + rejected[0], Screenshots: shots, Retryable: transient(rejected)}
	}
	if update {
		confirmed := false
		for _, dlg := range dialogs {
			low := strings.ToLower(dlg)
			if strings.Contains(low, "success") || strings.Contains(low, "updated") {
				confirmed = true
			}
		}
		if !confirmed {
			shown := "none"
			if len(dialogs) > 0 {
				shown = pyList(dialogs)
			}
			return StepResult{Message: fmt.Sprintf("Presenter update not confirmed (dialogs: %s).", shown), Screenshots: shots, Retryable: true}
		}
		return okResult("Presenter updated.", shots)
	}
	return okResult("Presenter saved.", shots)
}

// updatePresenter re-saves the presenter on an existing token. An update
// overwrites the one presenter record, so unlike adding a row it's safe to retry.
func (a *Automation) updatePresenter(d deeds.PresenterPayload, token string) StepResult {
	result := StepResult{Message: "not attempted", Retryable: true}
	for attempt := 1; attempt <= maxItemAttempts; attempt++ {
		var sl *SessionLostError
		var r StepResult
		err := try(func() { r = a.fillPresenter(d, true) })
		if err != nil {
			if asErr(err, &sl) {
				a.recoverSession(token, "session lost while updating the presenter")
				continue
			}
			panic(err)
		}
		result = r
		if result.OK || !result.Retryable {
			return result
		}
		a.log.Warn("Presenter update attempt failed", "attempt", attempt, "of", maxItemAttempts, "message", result.Message)
		a.OpenToken(token)
	}
	return result
}

// readToken is the real token number, from the current page's docNo field or,
// failing that, the PR document tab's (which always shows it).
func (a *Automation) readToken() string {
	fromPage := func(timeout float64) string {
		found := ""
		err := try(func() {
			a.until(timeout, func() bool {
				v, _ := a.exec("var e = document.getElementById('docNo'); return e ? e.value : null;").(string)
				v = strings.TrimSpace(v)
				if v != "" && tokenRe.MatchString(v) {
					found = v
					return true
				}
				return false
			})
		})
		if err != nil {
			if isWD(err) {
				return ""
			}
			panic(err)
		}
		return found
	}
	if token := fromPage(8); token != "" {
		return token
	}
	err := try(func() { a.openTab(tabPriorDoc) })
	if err != nil {
		var fe *fillError
		var sl *SessionLostError
		if asErr(err, &fe) || asErr(err, &sl) || isWD(err) {
			return ""
		}
		panic(err)
	}
	return fromPage(8)
}

// findNewToken is a token in My Token that wasn't there before this run
// started - i.e. one this run created even though its own save outcome was
// unclear. It returns "" only when My Token was read and genuinely shows no
// new token; anything it can't establish for certain raises instead, since
// retrying token creation on a guess is exactly how a duplicate token gets made.
func (a *Automation) findNewToken(known map[string]bool, presenterName string) string {
	const manual = "please check My Token on the portal before submitting again."
	if known == nil {
		panic(errorf("Couldn't tell whether the portal created a token (My Token couldn't be read before starting) - %s", manual))
	}
	var current []TokenRow
	have := false
	for attempt := 1; attempt <= 5; attempt++ {
		err := try(func() { current = a.listMyTokens() })
		if err == nil {
			have = true
			break
		}
		if isPortalErr(err) {
			panic(err)
		}
		if errors_Is(err, errCanceled) {
			panic(err)
		}
		a.log.Warn("Could not read My Token", "attempt", attempt, "of", 5, "err", short(err))
		a.sleep(3)
		rerr := try(func() {
			if a.sessionLost() {
				// A dropped session is the usual reason - log back in (no
				// token to reopen) and read it again.
				a.recoverSession("", "session lost while reading My Token")
			}
		})
		if rerr != nil && !isWD(rerr) {
			panic(rerr)
		}
	}
	if !have {
		panic(errorf("Couldn't read My Token to check whether a token was already created - %s", manual))
	}
	var fresh []TokenRow
	for _, row := range current {
		if !known[row.Token] {
			fresh = append(fresh, row)
		}
	}
	name := cleanText(presenterName)
	var named []TokenRow
	for _, row := range fresh {
		if name == "" || cleanText(row.Name) == name {
			named = append(named, row)
		}
	}
	candidates := named
	if len(candidates) == 0 {
		candidates = fresh
	}
	if len(candidates) == 1 {
		return candidates[0].Token
	}
	if len(candidates) > 0 {
		ids := make([]string, len(candidates))
		for i, c := range candidates {
			ids[i] = c.Token
		}
		panic(errorf("Several new tokens appeared during this run (%s) - %s", strings.Join(ids, ", "), manual))
	}
	return ""
}

type reportFn func(stage string, ok bool, message string, shots [][]byte)

// createToken is registration + presenter, retried only in ways that can't
// leave a second token behind: before any retry the My Token list is checked
// for a token this run already created.
func (a *Automation) createToken(p *deeds.Payload, report reportFn) string {
	var known map[string]bool
	if err := try(func() {
		known = map[string]bool{}
		for _, row := range a.listMyTokens() {
			known[row.Token] = true
		}
	}); err != nil {
		if errors_Is(err, errCanceled) {
			panic(err)
		}
		known = nil
		a.log.Warn("Could not snapshot My Token before creating a token", "err", err)
	}
	presenterName := p.Presenter.Name

	last := "not attempted"
	registrationReported := false
	for attempt := 1; attempt <= maxTokenAttempts; attempt++ {
		a.checkDeadline()
		presenterClicked := false
		token := ""
		err := try(func() {
			a.navigateMenu(a.cred.NewDeedMenuIndex)
			reg := a.fillRegistration(p.Registration)
			if !reg.OK {
				// Nothing is committed before the presenter save, so a failed
				// registration page is always safe to redo.
				last = reg.Message
				if a.sessionLost() && attempt < maxTokenAttempts {
					// Logged out / browser gone mid-page: not the data's fault.
					a.recoverSession("", reg.Message)
					return
				}
				if !reg.Retryable || attempt == maxTokenAttempts {
					report("registration", false, reg.Message, reg.Screenshots)
					panic(errorf("Registration step failed: %s", reg.Message))
				}
				a.log.Warn("Registration attempt failed", "attempt", attempt, "message", reg.Message)
				a.sleep(3 * float64(attempt)) // give a struggling portal a moment
				return
			}
			if !registrationReported {
				report("registration", true, reg.Message, reg.Screenshots)
				registrationReported = true
			}

			presenterClicked = true
			pres := a.fillPresenter(p.Presenter, false)
			if !pres.OK && a.sessionLost() {
				panic(&SessionLostError{pres.Message})
			}
			// Whatever the dialogs said (a notice can pop up on the page the
			// save lands on), a token the portal now shows is the truth.
			token = a.readToken()
			if token == "" {
				token = a.findNewToken(known, presenterName)
			}
			if token == "" && !pres.OK && !pres.Retryable {
				// An explicit validation rejection - nothing was saved.
				report("presenter", false, pres.Message, pres.Screenshots)
				panic(errorf("Presenter step failed: %s", pres.Message))
			}
			if token != "" {
				msg := pres.Message
				if !pres.OK {
					msg = fmt.Sprintf("Presenter save outcome was unclear, but token %s exists - continuing with it.", token)
				}
				report("presenter", true, msg, pres.Screenshots)
				return
			}
			last = pres.Message
			if attempt == maxTokenAttempts {
				report("presenter", false, pres.Message, pres.Screenshots)
				panic(errorf("Presenter step failed: %s", pres.Message))
			}
			a.log.Warn("Presenter attempt failed (no token was created)", "attempt", attempt, "message", pres.Message)
		})
		if err == nil {
			if token != "" {
				return token
			}
			continue
		}
		var sl *SessionLostError
		if !(asErr(err, &sl) || isWD(err)) {
			panic(err)
		}
		last = short(err)
		a.recoverSession("", last)
		if presenterClicked {
			if t := a.findNewToken(known, presenterName); t != "" {
				report("presenter", true, fmt.Sprintf("Session dropped after saving - recovered token %s.", t), nil)
				return t
			}
		}
	}
	panic(errorf("Could not create a token: %s", last))
}

// --- single-row edits on an existing token ----------------------------------------

// FindToken checks My Token: the token must be listed and still have its View
// link (a token sent on to the SRO has only "View Details"/print, and its rows
// can't be changed). It raises an Error otherwise - nothing is touched on the portal.
func (a *Automation) FindToken(token string) {
	a.gotoMyToken()
	found, _ := a.exec(`
            var tok = arguments[0];
            var tr = Array.from(document.querySelectorAll('tr')).filter(function (tr) {
              var td = tr.querySelector('td'); return td && td.innerText.trim() === tok; })[0];
            if (!tr) return null;
            var view = Array.from(tr.querySelectorAll('a')).filter(function (a) {
              return /showDetails\s*\(/.test(a.getAttribute('href') || ''); })[0];
            return {editable: !!view, text: tr.innerText};
            `, token).(map[string]any)
	if found == nil {
		panic(&TokenUnavailableError{fmt.Sprintf("Token %s isn't in My Token on the portal - nothing was changed.", token)})
	}
	if editable, _ := found["editable"].(bool); !editable {
		text, _ := found["text"].(string)
		panic(&TokenUnavailableError{fmt.Sprintf("Token %s can no longer be edited on the portal (%s) - nothing was changed.",
			token, strings.Join(strings.Fields(text), " "))})
	}
}

// sameValue compares a field read back after an update with what was typed:
// the portal stores numbers its own way ("52" comes back as "52.00").
func sameValue(held, typed string) bool {
	x, y := cleanText(held), cleanText(typed)
	if x == y {
		return true
	}
	fx, errx := strconv.ParseFloat(x, 64)
	fy, erry := strconv.ParseFloat(y, 64)
	return errx == nil && erry == nil && fx == fy
}

// pickRow is the portal row to edit: the one at the same position, provided it
// shows the old name/number (or already the new one, if an earlier attempt's
// update landed). Failing that, the only row that shows the old one. Otherwise
// nothing is edited - never a row that might be someone else's.
func (a *Automation) pickRow(rows []savedRow, index int, oldKey, newKey, label string) int {
	shows := func(row savedRow, key string) bool {
		return key != "" && strings.Contains(cleanText(row.Text), cleanText(key))
	}
	if index < len(rows) && (oldKey == "" || shows(rows[index], oldKey) || shows(rows[index], newKey)) {
		return index
	}
	var matching []int
	for i, row := range rows {
		if shows(row, oldKey) {
			matching = append(matching, i)
		}
	}
	if len(matching) == 1 {
		a.log.Warn("Row on the portal isn't the expected one; using the one row that is.", "label", label, "row", index+1, "expected", oldKey)
		return matching[0]
	}
	panic(fillErrorf(false, "%s: couldn't tell which of the portal's %d row(s) is '%s' (expected it at row %d) - not edited.",
		label, len(rows), oldKey, index+1))
}

// openRowEdit runs the row's own edit action and waits for its form (with its
// Update button) - a page load for most tabs, an in-page swap for PR documents.
func (a *Automation) openRowEdit(row savedRow, label string) {
	if row.Edit == "" {
		panic(fillErrorf(false, "%s: the portal row has no edit action.", label))
	}
	a.execIgnoreAlert(strings.TrimPrefix(row.Edit, "javascript:"))
	dialogs := a.handleDialogs(1.5, 6)
	for _, d := range dialogs {
		if strings.Contains(strings.ToLower(d), "not possible") {
			panic(fillErrorf(false, "%s: the portal won't allow editing - %s", label, d))
		}
	}
	if a.presentOrNil(updateButton, 30) == nil {
		panic(fillErrorf(true, "%s: the row's edit form didn't open.", label))
	}
	a.waitPageSettled(10)
	a.handleDialogs(0.5, 6)
}

// editRow opens row `index` (0-based) of the tab through its own edit action,
// fills the whole form, presses Update, and confirms by opening the row again
// and reading every typed field back. An update overwrites that one row, so a
// try that wasn't confirmed is safe to repeat.
func (a *Automation) editRow(t tab, label string, fill func(), index int, oldKey, newKey, token string) StepResult {
	lastMessage := "not attempted"
	var lastShots [][]byte
	for attempt := 1; attempt <= maxItemAttempts; attempt++ {
		var result *StepResult
		err := try(func() {
			a.openTab(t)
			rows := a.savedRowsWithEdit()
			position := a.pickRow(rows, index, oldKey, newKey, label)
			a.openRowEdit(rows[position], label)
			a.typed, a.typedKeys = map[string]string{}, nil
			fill()
			typed := map[string]string{}
			keys := append([]string(nil), a.typedKeys...)
			for k, v := range a.typed {
				typed[k] = v
			}
			shots := a.captureScrollingScreenshots(6)
			a.jsClick(a.find(updateButton))
			dialogs := a.awaitSaveOutcome(90, 3)
			rejected := rejections(dialogs)
			if len(rejected) > 0 && !transient(rejected) && !(attempt == 1 && allRequired(rejected)) {
				result = &StepResult{Message: fmt.Sprintf("%s: portal rejected the update - %s", label, strings.Join(rejected, " / ")),
					Screenshots: append(append([][]byte{}, shots...), a.safeScreenshots()...), Retryable: false}
				return
			}

			// Confirm: same number of rows, and the row's form now holds what was typed.
			a.openTab(t)
			after := a.savedRowsWithEdit()
			if len(after) != len(rows) {
				result = &StepResult{Message: fmt.Sprintf("%s: the tab had %d row(s) before the update and %d after - please check the token on the portal.",
					label, len(rows), len(after)), Screenshots: append(append([][]byte{}, shots...), a.safeScreenshots()...), Retryable: false}
				return
			}
			a.openRowEdit(after[position], label)
			heldRaw, _ := a.exec("var out = {}; arguments[0].forEach(function (id) {"+
				" var e = document.getElementById(id); out[id] = e ? e.value : null; }); return out;", keys).(map[string]any)
			var differ []string
			for _, f := range keys {
				held, ok := heldRaw[f].(string)
				if ok && !sameValue(held, typed[f]) {
					differ = append(differ, f)
				}
			}
			if len(differ) == 0 {
				result = &StepResult{OK: true, Message: label + " updated.", Screenshots: shots, Retryable: true}
				return
			}
			if len(differ) > 5 {
				differ = differ[:5]
			}
			shown := "none"
			if len(dialogs) > 0 {
				shown = pyList(dialogs)
			}
			lastMessage = fmt.Sprintf("%s: update not confirmed - the portal still shows other values in %s (dialogs: %s)",
				label, strings.Join(differ, ", "), shown)
			lastShots = shots
		})
		if err != nil {
			out := a.classifyAttemptError(err, label, token, true)
			if out.final != nil {
				return *out.final
			}
			lastMessage = out.message
			if out.next {
				continue
			}
			lastShots = out.shots
		}
		if result != nil {
			return *result
		}
		a.log.Warn("Attempt did not succeed", "message", lastMessage, "attempt", attempt, "of", maxItemAttempts)
		a.sleep(2)
	}
	return StepResult{Message: lastMessage, Screenshots: lastShots, Retryable: true}
}

// Progress receives each stage's outcome: (stage, percent, ok, message, screenshots).
type Progress func(stage string, percent int, ok bool, message string, shots [][]byte)

// StageResult is one entry of section_results.
type StageResult struct {
	OK      bool
	Message string
}

// rowTab says how one payload list is entered: its tab, label, form filler
// and the Save button of a new row. Prior documents are never edited row by
// row (see deeds.PlanRowEdits).
type rowTab struct {
	tab   tab
	label string
	fill  func(p *deeds.Payload, i int, editing bool)
	save  webdriver.By
}

func (a *Automation) rowTabs() map[string]rowTab {
	return map[string]rowTab{
		"executants": {tabExecutant, "Executant", func(p *deeds.Payload, i int, _ bool) {
			e := p.Executants[i]
			a.fillPartyForm(e.PartyPayload, e.Type, "ddlExecutantType")
		}, webdriver.ByID("saveForm2")},
		"claimants": {tabClaimant, "Claimant", func(p *deeds.Payload, i int, _ bool) {
			a.fillPartyForm(p.Claimants[i], "", "")
		}, webdriver.ByID("saveForm")},
		"schedule": {tabProperty, "Property", func(p *deeds.Payload, i int, editing bool) {
			a.fillScheduleForm(p.Schedule[i], editing)
		}, webdriver.ByName("Save")},
		"attesting_witnesses": {tabAttestWitness, "Attesting witness", func(p *deeds.Payload, i int, _ bool) {
			a.fillWitnessForm(p.AttestingWitnesses[i], true)
		}, webdriver.ByID("saveForm2")},
		"identifying_witnesses": {tabIdentWitness, "Identifying witness", func(p *deeds.Payload, i int, _ bool) {
			a.fillWitnessForm(p.IdentifyingWitnesses[i], false)
		}, webdriver.ByID("saveForm2")},
	}
}

// RowEditResult is what the edit pipeline reports.
type RowEditResult struct {
	TokenNumber    string
	RowsOK         map[string]map[int]bool
	SectionResults map[string]StageResult
	OverallOK      bool
}

// SubmitRowEdits is the edit pipeline: only the changed rows of a token the
// portal already holds. plan is deeds.PlanRowEdits()'s result.
//
// Login -> My Token: the token must be listed and editable, else stop (an
// Error, nothing changed) -> Enter Document Details -> per changed row: its
// tab, its row's edit action, fill, Update, read back. Added rows are saved
// as new rows after the existing ones.
func (a *Automation) SubmitRowEdits(p *deeds.Payload, plan deeds.RowPlan, token string, progress Progress) (res *RowEditResult, err error) {
	err = try(func() { res = a.submitRowEdits(p, plan, token, progress) })
	return res, err
}

func (a *Automation) submitRowEdits(p *deeds.Payload, plan deeds.RowPlan, token string, progress Progress) *RowEditResult {
	tabs := a.rowTabs()
	total := 0
	for _, changes := range plan.Changes {
		total += len(changes)
	}
	if total == 0 {
		total = 1
	}
	done := 0
	sectionResults := map[string]StageResult{}
	rowsOK := map[string]map[int]bool{}

	report := func(stage string, ok bool, message string, shots [][]byte, percent int) {
		a.log.Info("Portal stage", "ok", ok, "stage", stage, "message", message)
		progress(stage, percent, ok, message, shots)
		sectionResults[stage] = StageResult{ok, message}
	}

	a.EnsureLoggedIn(10)
	report("login", true, "Logged in.", nil, 5)
	a.runStep(func() StepResult { a.FindToken(token); return StepResult{OK: true} }, "")
	report("find_token", true, fmt.Sprintf("Token %s found in My Token.", token), nil, 10)
	a.runStep(func() StepResult { a.OpenToken(token); return StepResult{OK: true} }, "")
	a.token = token

	for _, listName := range plan.Lists {
		rt := tabs[listName]
		changes := append([]deeds.RowChange(nil), plan.Changes[listName]...)
		sort.SliceStable(changes, func(x, y int) bool {
			kx, ky := changes[x].Kind != "edit", changes[y].Kind != "edit"
			if kx != ky {
				return !kx
			}
			return changes[x].Index < changes[y].Index
		})
		for _, change := range changes {
			i := change.Index
			newKey := p.RowKey(listName, i)
			label := fmt.Sprintf("%s %d", rt.label, i+1)
			if newKey != "" {
				label = fmt.Sprintf("%s %d (%s)", rt.label, i+1, newKey)
			}
			var result StepResult
			if change.Kind == "edit" {
				result = a.runStep(func() StepResult {
					return a.editRow(rt.tab, label, func() { rt.fill(p, i, true) }, i, change.OldKey, newKey, token)
				}, token)
			} else {
				result = a.runStep(func() StepResult {
					return a.insertRow(rt.tab, label, func() { rt.fill(p, i, false) }, rt.save, i+1, token)
				}, token)
			}
			if rowsOK[listName] == nil {
				rowsOK[listName] = map[int]bool{}
			}
			rowsOK[listName][i] = result.OK
			done++
			report(fmt.Sprintf("%s_%d", listName, i+1), result.OK, result.Message, result.Screenshots, 10+89*done/total)
		}
	}

	overall := true
	for _, byIndex := range rowsOK {
		for _, ok := range byIndex {
			overall = overall && ok
		}
	}
	msg := "Some edits failed."
	if overall {
		msg = "Edits finished."
	}
	progress("complete", 100, overall, msg, a.safeScreenshots())
	return &RowEditResult{TokenNumber: token, RowsOK: rowsOK, SectionResults: sectionResults, OverallOK: overall}
}

// ExpandSections adds the sections the portal forces to be redone alongside an
// edited one: saving PR documents makes the portal auto-add executants (so
// executants must be cleared and re-entered after), and document links point
// at PR documents and properties.
func ExpandSections(sections []string) map[string]bool {
	expanded := map[string]bool{}
	for _, s := range sections {
		expanded[s] = true
	}
	if expanded["prior_documents"] {
		expanded["executants"] = true
	}
	if expanded["prior_documents"] || expanded["schedule"] {
		expanded["document_link"] = true
	}
	return expanded
}

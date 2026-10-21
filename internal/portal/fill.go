package portal

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shahinshabab/deedflow-go/internal/deeds"
	"github.com/shahinshabab/deedflow-go/internal/fuzzy"
	"github.com/shahinshabab/deedflow-go/internal/webdriver"
)

// kv is one text field to fill; the order matters (the portal's own handlers
// react to each field in turn), so these are slices, not maps.
type kv struct{ id, val string }

func (a *Automation) jsClick(el *webdriver.Element) {
	// A native .click() can land on an overlay/popup that happens to be on
	// top instead of the button; a JS click always reaches the target.
	if _, err := a.d.ExecuteScript(a.ctx, "arguments[0].click();", el); err != nil && !isUnexpAlert(err) {
		raise(err)
	}
	// (An UnexpectedAlertPresent here means the click itself opened a native
	// alert/confirm - a delete confirmation, a client-side validation message.
	// It's still open: the caller's handleDialogs() reads and answers it.)
}

// execIgnoreAlert runs a script whose side effect may open a native dialog.
func (a *Automation) execIgnoreAlert(script string, args ...any) {
	if _, err := a.d.ExecuteScript(a.ctx, script, args...); err != nil && !isUnexpAlert(err) {
		raise(err)
	}
}

// presentOrNil is a presence wait whose timeout is "not there".
func (a *Automation) presentOrNil(by webdriver.By, timeout float64) *webdriver.Element {
	el, err := a.d.Present(a.ctx, secs(timeout), by)
	if err == nil {
		return el
	}
	if isTimeout(err) {
		return nil
	}
	raise(err)
	return nil
}

// --- shared field-filling helpers -------------------------------------------

// waitForDropdownOptions waits for a cascading dropdown's real <option>s to
// arrive, with one fresh retry - the same cascade was seen live to
// intermittently stay empty on one run and fill on the next with identical data.
func (a *Automation) waitForDropdownOptions(dropdownID string, timeout float64, attempts int) bool {
	for attempt := 1; attempt <= attempts; attempt++ {
		started := time.Now()
		err := try(func() {
			a.until(timeout, func() bool {
				sel := a.find(webdriver.ByID(dropdownID))
				return len(mustv(sel.FindElements(a.ctx, webdriver.ByTag("option")))) > 1
			})
		})
		if err == nil {
			return true
		}
		if errors_Is(err, errCanceled) {
			panic(err)
		}
		if isUnexpAlert(err) {
			a.handleDialogs(0.3, 6)
			continue
		}
		if attempt < attempts {
			a.log.Warn("Dropdown had no options", "dropdown", dropdownID, "after_s", int(time.Since(started).Seconds()+0.5),
				"attempt", attempt, "of", attempts, "err", exceptionNameOf(err))
			a.refireLastSelection(dropdownID)
			a.sleep(1)
		}
	}
	return false
}

// refireLastSelection: a cascade's options come from an AJAX call fired by
// the dropdown above it - which on these forms is the one selected just
// before. If that call failed (a dropped request, a server error), firing
// that dropdown's change again asks for the options again.
func (a *Automation) refireLastSelection(waitingFor string) {
	parent := a.lastSelected
	if parent == "" || parent == waitingFor {
		return
	}
	err := try(func() {
		a.exec("var e = document.getElementById(arguments[0]);"+
			"if (e) e.dispatchEvent(new Event('change', {bubbles: true}));", parent)
	})
	if err != nil && !isWD(err) {
		panic(err)
	}
}

// selectBestMatch picks the dropdown option closest to targetText. threshold
// < 0 means the default (0.75 for a longer text, 0.5 for a short one).
func (a *Automation) selectBestMatch(dropdownID, targetText string, threshold float64) bool {
	if threshold < 0 {
		if utf8.RuneCountInString(strings.TrimSpace(targetText)) > 5 {
			threshold = 0.75
		} else {
			threshold = 0.5
		}
	}
	scan := func() (*webdriver.Element, float64) {
		sel, err := a.d.FindElement(a.ctx, webdriver.ByID(dropdownID))
		if err != nil {
			if isNoSuch(err) {
				return nil, 0
			}
			raise(err)
		}
		targetClean := cleanText(targetText)
		bestRatio := 0.0
		var best *webdriver.Element
		for _, opt := range mustv(webdriver.Select{El: sel}.Options(a.ctx)) {
			optClean := cleanText(a.text(opt))
			if optClean == targetClean {
				return opt, 1.0
			}
			ratio := fuzzy.SequenceRatio(optClean, targetClean)
			if ratio > bestRatio {
				bestRatio, best = ratio, opt
			}
		}
		return best, bestRatio
	}
	// Some cascades stream their options in over more than one AJAX response
	// - one re-scan catches a list that was still growing.
	bestRatio := 0.0
	for attempt := 1; attempt <= 2; attempt++ {
		var best *webdriver.Element
		best, bestRatio = scan()
		if best != nil && bestRatio >= threshold {
			err := best.Click(a.ctx)
			switch {
			case err == nil:
				return true
			case isStale(err):
				continue
			case isUnexpAlert(err):
				// The option was picked; its onchange raised a dialog.
				a.handleDialogs(0.3, 6)
				return true
			default:
				raise(err)
			}
		}
		if attempt == 1 {
			a.sleep(0.7)
		}
	}
	a.log.Warn("No dropdown match", "target", targetText, "dropdown", dropdownID, "best_ratio", fmt.Sprintf("%.2f", bestRatio))
	return false
}

// retryStale runs a fill helper again when the portal rebuilt that part of the
// form under it (an onchange that re-renders the section - seen live on the PR
// document tab right after choosing the document type).
func (a *Automation) retryStale(fieldID string, fn func()) {
	for attempt := 1; attempt <= 3; attempt++ {
		err := try(fn)
		if err == nil {
			return
		}
		if !isStale(err) {
			panic(err)
		}
		if attempt == 3 {
			panic(fillErrorf(true, "The portal kept rebuilding the form while filling '%s'.", fieldID))
		}
		a.waitPageSettled(10)
	}
}

func (a *Automation) selectExact(dropdownID, value string, byValue bool) {
	a.retryStale(dropdownID, func() { a.selectExactOnce(dropdownID, value, byValue) })
}

// selectExactOnce is an exact select on a static option list; a value that
// isn't an option is a data problem, so it's reported as non-retryable.
func (a *Automation) selectExactOnce(dropdownID, value string, byValue bool) {
	if !a.waitForDropdownOptions(dropdownID, 20, 3) {
		panic(fillErrorf(true, "'%s' dropdown never loaded.", dropdownID))
	}
	sel := webdriver.Select{El: a.find(webdriver.ByID(dropdownID))}
	var err error
	if byValue {
		err = sel.SelectByValue(a.ctx, value)
	} else {
		err = sel.SelectByVisibleText(a.ctx, value)
	}
	if err != nil {
		if isNoSuch(err) {
			panic(fillErrorf(false, "'%s' is not one of the portal's options for '%s'.", value, dropdownID))
		}
		raise(err)
	}
	a.lastSelected = dropdownID
}

// selectCascade is a fuzzy-matched select on a cascading/dynamic dropdown.
func (a *Automation) selectCascade(dropdownID, value string, threshold float64, optional bool, timeout float64) {
	a.retryStale(dropdownID, func() { a.selectCascadeOnce(dropdownID, value, threshold, optional, timeout) })
}

// cascade is selectCascade with the usual defaults.
func (a *Automation) cascade(dropdownID, value string) {
	a.selectCascade(dropdownID, value, 0.75, false, 20)
}

// selectCascadeOnce skips blank values (sparse data legitimately leaves
// optional dropdowns empty).
func (a *Automation) selectCascadeOnce(dropdownID, value string, threshold float64, optional bool, timeout float64) {
	if strings.TrimSpace(value) == "" {
		return
	}
	if !a.waitForDropdownOptions(dropdownID, timeout, 3) {
		if optional {
			return
		}
		panic(fillErrorf(true, "'%s' dropdown never loaded.", dropdownID))
	}
	if !a.selectBestMatch(dropdownID, value, threshold) {
		if optional {
			return
		}
		panic(fillErrorf(false, "Could not find '%s' among the portal's options for '%s'.", value, dropdownID))
	}
	a.lastSelected = dropdownID
	// The portal's onchange AJAX for the next level fires off this selection -
	// give it a moment rather than racing it.
	a.sleep(0.5)
}

func (a *Automation) fillText(fieldID, value string) { a.fillTextR(fieldID, value, false) }

func (a *Automation) fillTextR(fieldID, value string, retrying bool) {
	skipCheck := false
	err := try(func() {
		element := a.find(webdriver.ByID(fieldID))
		// A few ids (e.g. presentCode) are <select>s on the portal - .clear()
		// on one raises "invalid element state".
		if tag := mustv(element.TagName(a.ctx)); tag == "select" {
			if value != "" {
				must(webdriver.Select{El: element}.SelectByVisibleText(a.ctx, value))
			}
			skipCheck = true
			return
		}
		// clear() on a locked field raises InvalidElementStateException - on
		// the live portal that failed the whole presenter page of an E-Stamp deed.
		if a.attr(element, "disabled") != "" {
			a.log.Info("Skipping field: the portal has disabled it.", "field", fieldID)
			skipCheck = true
			return
		}
		if a.attr(element, "readonly") != "" {
			// Typically a date-picker input: set it the way the picker does.
			if value != "" && a.attr(element, "value") != value {
				a.exec("arguments[0].value = arguments[1];"+
					"arguments[0].dispatchEvent(new Event('change', {bubbles: true}));", element, value)
			}
			skipCheck = true
			return
		}
		// Some fields start out present but not yet interactable until the
		// portal's own JS enables them - wait for real clickability.
		element = a.clickable(webdriver.ByID(fieldID), defaultWait)
		must(element.Clear(a.ctx))
		if value != "" {
			must(element.SendKeys(a.ctx, value))
		}
	})
	switch {
	case err == nil:
	case isNotInter(err):
		// A couple of fields pass Selenium's clickable check yet refuse native
		// keystrokes (a portal CSS quirk) - set the value via JS and fire the
		// events typing would, so onchange validation runs.
		element := a.find(webdriver.ByID(fieldID))
		a.exec("arguments[0].value = arguments[1];"+
			"arguments[0].dispatchEvent(new Event('input', {bubbles: true}));"+
			"arguments[0].dispatchEvent(new Event('change', {bubbles: true}));", element, value)
	case isStale(err):
		// The portal re-rendered that bit of DOM mid-fill; let it finish, then
		// re-locate once.
		if retrying {
			panic(fillErrorf(true, "The portal kept rebuilding the form while filling '%s'.", fieldID))
		}
		a.waitPageSettled(10)
		a.fillTextR(fieldID, value, true)
		return
	case isNoSuch(err):
		panic(fillErrorf(true, "Field '%s' is not on the page.", fieldID))
	case isUnexpAlert(err):
		// A dialog raised by the previous field's onchange (or a portal
		// notice) is in the way. Clear it and type this field again.
		dialogs := a.handleDialogs(0.3, 6)
		if retrying {
			panic(fillErrorf(true, "Portal dialog kept interrupting field '%s': %s", fieldID, pyList(dialogs)))
		}
		a.fillTextR(fieldID, value, true)
		return
	default:
		panic(err)
	}
	if !skipCheck {
		a.checkFieldValue(fieldID, value)
	}
}

// checkFieldValue reads a field back after typing: the portal's keypress
// filters (digits-only fields, maxlength) silently drop characters, and the
// log is the only place that would otherwise show it.
func (a *Automation) checkFieldValue(fieldID, intended string) {
	var actual any
	err := try(func() {
		actual = a.exec("var e = document.getElementById(arguments[0]); return e ? e.value : null;", fieldID)
	})
	if err != nil {
		if isWD(err) {
			return
		}
		panic(err)
	}
	str, ok := actual.(string)
	if !ok {
		return
	}
	if _, seen := a.typed[fieldID]; !seen {
		a.typedKeys = append(a.typedKeys, fieldID)
	}
	a.typed[fieldID] = str
	if cleanText(str) != cleanText(intended) {
		a.log.Warn("Portal altered a field while typing", "field", fieldID, "intended", intended, "now", str)
	}
}

func (a *Automation) clickRadio(name, value string) {
	radio := a.find(webdriver.ByXPath(fmt.Sprintf("//input[@name='%s' and @value='%s']", name, value)))
	if !a.selected(radio) {
		a.jsClick(radio)
	}
}

type savedRow struct{ Text, Delete, Edit string }

func (a *Automation) readRows(script string) []savedRow {
	var raw any
	err := try(func() { raw = a.exec(script) })
	if err != nil {
		if isWD(err) {
			return nil
		}
		panic(err)
	}
	list, _ := raw.([]any)
	var out []savedRow
	for _, item := range list {
		m, _ := item.(map[string]any)
		row := savedRow{}
		row.Text, _ = m["text"].(string)
		row.Delete, _ = m["delete"].(string)
		row.Edit, _ = m["edit"].(string)
		out = append(out, row)
	}
	return out
}

func (a *Automation) savedRows() []savedRow { return a.readRows(savedRowsJS) }

func (a *Automation) savedRowsWithEdit() []savedRow { return a.readRows(rowsWithEditJS) }

// --- per-tab form fillers (fill only - never click Save) ---------------------

func (a *Automation) fillPriorDocumentForm(d deeds.PriorDocPayload) {
	// By value: the option texts carry stray whitespace and older chillu
	// spellings, the values are stable codes.
	if code := deeds.PriorDocTypeValues[d.Type]; code != "" {
		a.selectExact("documentType", code, true)
	} else {
		a.selectExact("documentType", d.Type, false)
	}
	// Choosing the type rebuilds the rest of this form on the live portal;
	// anything located before that is stale.
	a.waitPageSettled(10)
	var fields []kv
	if d.Type == "രജിസ്റ്റർ/ഫയൽ ചെയ്ത" || d.Type == "വിൽ" {
		a.selectCascade("district", d.District, 0.75, d.District == "", 20)
		a.selectCascade("sro", d.SRO, 0.75, d.SRO == "", 20)
		fields = []kv{{"pyear", d.Year}, {"pbookno", d.Book}, {"pattayamNo", d.Number}, {"volume", d.Volume},
			{"pageFrom", d.PageFrom}, {"pageTo", d.PageTo}}
	} else {
		fields = []kv{{"pyear", d.Year}, {"pattayamNo", d.Number}, {"remarks", d.Remarks}}
	}
	for _, f := range fields {
		a.fillText(f.id, f.val)
	}
}

func (a *Automation) fillPartyForm(d deeds.PartyPayload, partyType, typeDropdownID string) {
	if d.Gender != "" {
		a.selectExact("selSex", d.Gender, false)
	}
	// rtype's options depend on gender (loaded after selSex) and the location
	// chain is geographic - both fuzzy-matched. id_type and the capacity
	// dropdown are static lists - exact.
	a.selectCascade("rtype", d.Relation, 0.55, false, 20)
	// Native place: Kerala, or "other" - which blanks and disables the
	// district/taluk/village dropdowns (portal's changeKeralite()).
	if d.OutsideKerala {
		a.clickRadio("radNative", "0")
	} else {
		a.clickRadio("radNative", "1")
	}
	a.waitPageSettled(10)
	post, place, house := d.Post, d.Place, d.HouseName
	if d.OutsideKerala {
		// The post office field is "State/Country" here; the deed's
		// out-of-state district is the closest thing it names. A written-out
		// address trips the portal's special-character check (_isSpclChar),
		// so its punctuation becomes spaces.
		first := d.Post
		if first == "" {
			first = d.District
		}
		post, place, house = deeds.PortalPlainText(first), deeds.PortalPlainText(place), deeds.PortalPlainText(house)
	} else {
		a.cascade("district", d.District)
		a.cascade("thaluk", d.Taluk)
		a.cascade("village", d.Village)
	}
	// A minor claimant (a gift to a child, through a guardian) goes in with the
	// portal's Minor box ticked, which also drops the ID fields; without it the
	// portal refuses any age under 18.
	minor := false
	if typeDropdownID == "" && isASCIIDigits(d.Age) {
		if n, err := strconv.Atoi(d.Age); err == nil && n < 18 {
			minor = true
		}
	}
	if minor {
		if box := a.findAll(webdriver.ByID("chkminor")); len(box) > 0 && !a.selected(box[0]) {
			a.jsClick(box[0])
			a.waitPageSettled(10)
		}
	}
	if d.IDType != "" && !minor {
		a.selectExact("id_type", d.IDType, false)
	}
	if typeDropdownID != "" && partyType != "" {
		// A will's executant page (Book 3) has no executant-type choices.
		if a.waitForDropdownOptions(typeDropdownID, 5, 1) {
			a.selectExact(typeDropdownID, partyType, false)
		} else {
			a.log.Info("No choices on this page - leaving it.", "dropdown", typeDropdownID)
		}
	}
	fields := []kv{
		{"name", d.Name}, {"age", d.Age}, {"rname", d.Relative}, {"profession", d.Job},
		{"houseName", house}, {"postOffice", post}, {"place", place},
		{"pinCode", d.Pin}, {"phone", d.Mobile},
	}
	if !minor {
		// Ticking Minor hides the ID block; waiting on its field would time out.
		fields = append(fields, kv{"id_no", d.IDNumber})
	}
	for _, f := range fields {
		a.fillText(f.id, f.val)
	}
	checkbox := a.present(webdriver.ByID("radio_is_present"), defaultWait)
	if !a.selected(checkbox) {
		a.jsClick(checkbox)
	}
	a.refillCleared(fields)
}

func isASCIIDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// refillCleared re-types any field the page emptied after we filled it. Seen
// live: a claimant's ID number was typed, then cleared by a late handler (the
// ID type's), and the save was refused with "Id No Required".
func (a *Automation) refillCleared(fields []kv) {
	a.waitPageSettled(10)
	for _, f := range fields {
		if strings.TrimSpace(f.val) == "" {
			continue
		}
		var current any
		err := try(func() {
			current = a.exec("var e = document.getElementById(arguments[0]); return e ? e.value : null;", f.id)
		})
		if err != nil {
			if isWD(err) {
				continue
			}
			panic(err)
		}
		if str, ok := current.(string); ok && strings.TrimSpace(str) == "" {
			a.log.Warn("Field was cleared by the page after being filled - filling it again.", "field", f.id)
			a.fillText(f.id, f.val)
		}
	}
}

func (a *Automation) fillScheduleForm(d deeds.SchedulePayload, editing bool) {
	if d.TransactionTypeCode == "" {
		panic(fillErrorf(false, "No portal transaction-type code is known for this deed type."))
	}
	if strings.TrimSpace(d.FairValue) == "" {
		panic(fillErrorf(false, "The portal requires a fair value for every property, and this one is blank."))
	}
	pattika := a.find(webdriver.ByID("pattikaNo"))
	// An edit form already holds the schedule number; setting it again runs
	// the portal's fillSerialno(), which renumbers the row.
	if !(editing && strings.TrimSpace(a.attr(pattika, "value")) == strings.TrimSpace(d.Number)) {
		a.exec("arguments[0].value = arguments[1];"+
			"arguments[0].dispatchEvent(new Event('change', {bubbles: true}));", pattika, d.Number)
	}
	// Must be answered before district: the district onchange raises a blocking
	// "Select Transfer of Revenue Registry" alert otherwise.
	if d.Pokuvarav == "Yes" {
		a.clickRadio("chkTrr", "1")
	} else {
		a.clickRadio("chkTrr", "0")
	}
	// The four-level location chain here is slower than elsewhere (seen live
	// taking >10s), hence the longer waits.
	a.selectCascade("district", d.District, 0.75, false, 20)
	a.selectCascade("sro", d.SRO, 0.75, false, 20)
	a.selectCascade("thaluk", d.Taluk, 0.75, false, 20)
	a.selectCascade("village", d.Village, 0.75, false, 20)
	// desam is legitimately empty for plenty of villages; reBlockNo only has
	// options in the digitally-surveyed workflow - both optional.
	a.selectCascade("desam", d.Desam, 0.75, true, 20)
	if d.LBody != "" {
		a.selectExact("lbodytype", d.LBody, false)
		a.sleep(0.5)
	}
	a.selectCascade("lbodyname", d.LBodyName, 0.75, false, 20)
	a.selectCascade("reBlockNo", d.ResurveyBlock, 0.75, true, 20)
	if d.LandType != "" {
		a.selectExact("landType", d.LandType, false)
	}
	a.selectExact("ddlTransType", d.TransactionTypeCode, true)

	fields := []kv{
		{"reSurveyNo", d.ResurveyNumber}, {"reSurveysubNo", d.ResurveySub},
		{"oldSurveyNo", d.OldSurvey}, {"oldSurveysubNo", d.OldSub}, {"tpNo", d.TPNumber},
		{"hecter", d.TotalHector}, {"are", d.TotalAre}, {"sqmtr", d.TotalMeter},
		{"trns_hecter", d.TransferHector}, {"trns_are", d.TransferAre},
		{"trns_sqmtr", d.TransferMeter}, {"cost", d.Price}, {"property_det", d.Details},
		{"east", d.East}, {"north", d.North}, {"west", d.West}, {"south", d.South},
	}
	for _, f := range fields {
		a.fillText(f.id, f.val)
	}
	if d.UndividedShare == "Yes" {
		a.clickRadio("undivideShare", "Y")
		a.fillText("firstShare", d.Share)
		a.fillText("secndShare", d.ShareOutOf)
	} else {
		a.clickRadio("undivideShare", "N")
	}
	a.refillCleared(fields)

	// insertData() refuses to save without a building Y/N answer.
	if checked, _ := a.exec("return !!document.querySelector(\"input[name='building']:checked\");").(bool); !checked {
		for _, r := range a.findAll(webdriver.ByName("building")) {
			switch strings.ToLower(a.attr(r, "value")) {
			case "n", "0", "f":
				a.jsClick(r)
			default:
				continue
			}
			break
		}
	}

	// Fair value goes last: blurring price/area/ward fields runs the portal's
	// changeFairvalue(), which unticks this radio. Ticking "enter fair value"
	// (value 2) then AJAX-loads the fairvalueamt field itself into the page -
	// it doesn't exist until that lands.
	a.waitPageSettled(10)
	a.clickRadio("fairvaluesel", "2")
	if dialogs := a.handleDialogs(1.0, 6); len(dialogs) > 0 {
		panic(fillErrorf(false, "Portal refused the fair-value step: %s", dialogs[0]))
	}
	if _, err := a.d.Visible(a.ctx, secs(20), webdriver.ByID("fairvalueamt")); err != nil {
		if isTimeout(err) {
			panic(fillErrorf(true, "The fair-value amount field never loaded after choosing 'enter fair value'."))
		}
		raise(err)
	}
	a.fillText("fairvalueamt", d.FairValue)
}

func (a *Automation) fillWitnessForm(d deeds.WitnessPayload, attesting bool) {
	if d.Name == "" {
		panic(fillErrorf(false, "Witness name is blank - the portal requires one."))
	}
	for _, f := range []kv{{"wname", d.Name}, {"rname", d.RName}, {"houseName", d.HouseName},
		{"desam", d.Desam}, {"place", d.Place}, {"profession", d.Job}} {
		a.fillText(f.id, f.val)
	}
	if d.Gender != "" {
		a.selectExact("selSex", d.Gender, false)
	}
	a.selectCascade("ddRtype", d.Relation, 0.55, false, 20)
	wtype := d.WType
	if wtype == "" {
		wtype = "WB"
	}
	// The Identifying Witness page only offers WB/WE.
	if !attesting && wtype != "WB" && wtype != "WE" {
		wtype = "WB"
	}
	a.selectExact("wtype", wtype, true)
}

// --- clear-then-insert for repeatable tabs ---------------------------------------

var quotedArg = regexp.MustCompile(`'([^']*)'`)

// deleteFirstRow deletes the first saved row via its own Delete link: that
// opens the tab's deletion form ("... Deletion Form", reason pre-filled "Not
// Applicable"), whose Delete button (#saveForm) confirms with a native
// confirm() and removes the row. Returns the dialogs seen.
func (a *Automation) deleteFirstRow(label string) []string {
	rows := a.savedRows()
	var args []string
	for _, m := range quotedArg.FindAllStringSubmatch(rows[0].Delete, -1) {
		args = append(args, m[1])
	}
	oldHTML := a.find(webdriver.ByTag("html"))
	a.execIgnoreAlert("showDetailsDel.apply(null, arguments[0]);", args)
	// "Delete Not Possible!!!" (token already verified/submitted) comes back as
	// a jAlert instead of the deletion form. Judged by whether the form opened,
	// not by the dialogs alone: the form's own page can greet us with an
	// unrelated notice too.
	dialogs := a.handleDialogs(2, 6)
	if !a.untilQuiet(20, func() bool { return oldHTML.IsStale(a.ctx) }) {
		if refused := rejections(dialogs); len(refused) > 0 {
			panic(fillErrorf(false, "%s: portal refused to delete an existing row - %s", label, refused[0]))
		}
		panic(fillErrorf(true, "%s: the row's delete page didn't open.", label))
	}
	a.waitPageSettled(10)
	dialogs = append(dialogs, a.handleDialogs(0.5, 6)...)
	deleteButton := webdriver.ByXPath("//input[@type='button' and (@value='Delete' or @id='saveForm')]")
	if len(a.findAll(webdriver.ByID("deleteReason"))) == 0 && len(a.findAll(deleteButton)) == 0 {
		refused := rejections(dialogs)
		if len(refused) > 0 && strings.Contains(strings.ToLower(strings.Join(refused, " ")), "not possible") {
			panic(fillErrorf(false, "%s: portal refused to delete an existing row - %s", label, refused[0]))
		}
	}
	// The Document Link deletion page refuses ("Please Select Property and
	// Update the Details.") unless the link's property boxes are ticked -
	// confirmed live from its deleteData() source.
	for _, box := range a.findAll(webdriver.ByName("chk_plId[]")) {
		if !a.selected(box) {
			a.jsClick(box)
		}
	}
	if reason := a.findAll(webdriver.ByID("deleteReason")); len(reason) > 0 && strings.TrimSpace(a.attr(reason[0], "value")) == "" {
		a.fillText("deleteReason", "Not Applicable")
	}
	if buttons := a.findAll(deleteButton); len(buttons) > 0 {
		a.jsClick(buttons[0])
		dialogs = append(dialogs, a.awaitSaveOutcome(90, 3)...)
	}
	// (No button = a tab whose Delete link removes the row directly.)
	return dialogs
}

// clearTab deletes every saved row on a tab, one at a time, re-reading the
// table after each delete, until it's empty. Bounded: a row that won't go away
// stops the clear (and the caller then doesn't insert, so old and new records
// are never mixed). Failures are raised as a fillError.
func (a *Automation) clearTab(t tab, label string) StepResult {
	a.openTab(t)
	start := len(a.savedRows())
	if start == 0 {
		return StepResult{OK: true, Message: label + ": no existing records.", Retryable: true}
	}
	shots := a.captureScrollingScreenshots(6)
	stuck := 0
	remaining := start
	for i := 0; i < start*2+3; i++ {
		dialogs := a.deleteFirstRow(label)
		a.openTab(t)
		now := len(a.savedRows())
		if now == 0 {
			return StepResult{OK: true, Retryable: true, Screenshots: shots,
				Message: fmt.Sprintf("%s: deleted %d existing record(s) before entering fresh ones.", label, start)}
		}
		if now < remaining {
			remaining, stuck = now, 0
			continue
		}
		stuck++
		if stuck >= 2 {
			shown := "none"
			if len(dialogs) > 0 {
				shown = pyList(dialogs)
			}
			panic(fillErrorf(true, "%s: an existing record wouldn't delete (%d left; dialogs: %s).", label, remaining, shown))
		}
	}
	panic(fillErrorf(true, "%s: gave up clearing existing records (%d left).", label, remaining))
}

// handleStepError is the common except-ladder of insertRow/editRow: what to
// record for the attempt, and whether the loop should go straight to the next
// attempt (a recovered session) or stop with a final result.
type attemptOutcome struct {
	final    *StepResult
	next     bool // continue without the log line and the pause
	message  string
	shots    [][]byte
	hasShots bool
}

func (a *Automation) classifyAttemptError(err error, label, token string, prefixed bool) attemptOutcome {
	var sl *SessionLostError
	var fe *fillError
	switch {
	case asErr(err, &sl):
		msg := label + ": " + sl.Msg
		a.recoverSession(token, msg)
		return attemptOutcome{next: true, message: msg}
	case asErr(err, &fe):
		msg := label + ": " + fe.msg
		if prefixed && strings.HasPrefix(fe.msg, label) {
			msg = fe.msg
		}
		shots := a.safeScreenshots()
		if !fe.retryable {
			return attemptOutcome{final: &StepResult{Message: msg, Screenshots: shots}, message: msg}
		}
		return attemptOutcome{message: msg, shots: shots, hasShots: true}
	case isUnexpAlert(err):
		dialogs := a.handleDialogs(0.5, 6)
		return attemptOutcome{message: fmt.Sprintf("%s: interrupted by portal dialog %s", label, pyList(dialogs)),
			shots: a.safeScreenshots(), hasShots: true}
	case isWD(err):
		msg := label + ": browser error - " + short(err)
		if a.sessionLost() {
			a.recoverSession(token, msg)
			return attemptOutcome{next: true, message: msg}
		}
		return attemptOutcome{message: msg, shots: a.safeScreenshots(), hasShots: true}
	}
	panic(err)
}

// insertRow: fill -> screenshot -> Save -> confirm a new row appeared. Runs on
// a tab that was just cleared, so this entry is the `index`-th row: if that
// many rows already exist, an earlier attempt's save did land (e.g. confirmed
// only after a dropped session) and it isn't saved again. Only a save the
// table shows did *not* land is retried.
func (a *Automation) insertRow(t tab, label string, fill func(), save webdriver.By, index int, token string) StepResult {
	lastMessage := "not attempted"
	var lastShots [][]byte
	for attempt := 1; attempt <= maxItemAttempts; attempt++ {
		var result *StepResult
		err := try(func() {
			a.openTab(t)
			rows := a.savedRows()
			if len(rows) > index {
				r := StepResult{Message: fmt.Sprintf("%s: the table holds more rows than entered so far (%d) - an earlier save landed twice; this section will be cleared and entered again.", label, len(rows)),
					Screenshots: a.safeScreenshots(), Retryable: true}
				result = &r
				return
			}
			if len(rows) == index {
				shots := lastShots
				if len(shots) == 0 {
					shots = a.safeScreenshots()
				}
				r := StepResult{OK: true, Message: label + " saved.", Screenshots: shots, Retryable: true}
				result = &r
				return
			}
			fill()
			shots := a.captureScrollingScreenshots(6)
			before := len(rows)

			a.jsClick(a.present(save, defaultWait))
			dialogs := a.awaitSaveOutcome(90, 3)

			a.openTab(t)
			after := len(a.savedRows())
			if after > before+1 {
				// A slow earlier save landed as well - a duplicate on the
				// portal. Not "saved": failing the section makes the next pass
				// clear the tab and enter it cleanly.
				r := StepResult{Message: fmt.Sprintf("%s: %d rows appeared after one save (an earlier save landed late) - this section will be cleared and entered again.", label, after-before),
					Screenshots: shots, Retryable: true}
				result = &r
				return
			}
			if after > before {
				r := StepResult{OK: true, Message: label + " saved.", Screenshots: shots, Retryable: true}
				result = &r
				return
			}
			rejected := rejections(dialogs)
			// "X Required" for a field we did fill is a race with the page (it
			// cleared it), so that's given one more go.
			requiredRace := attempt == 1 && len(rejected) > 0 && allRequired(rejected)
			if len(rejected) > 0 && !transient(rejected) && !requiredRace {
				r := StepResult{Message: fmt.Sprintf("%s: portal rejected the save - %s", label, strings.Join(rejected, " / ")),
					Screenshots: append(append([][]byte{}, shots...), a.safeScreenshots()...), Retryable: false}
				result = &r
				return
			}
			if len(rejected) > 0 {
				lastMessage = fmt.Sprintf("%s: portal had a passing error - %s", label, strings.Join(rejected, " / "))
			} else {
				shown := "none"
				if len(dialogs) > 0 {
					shown = pyList(dialogs)
				}
				lastMessage = fmt.Sprintf("%s: save not confirmed (no new row appeared; dialogs: %s)", label, shown)
			}
			lastShots = shots
		})
		if err != nil {
			out := a.classifyAttemptError(err, label, token, false)
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

func allRequired(msgs []string) bool {
	for _, m := range msgs {
		if !strings.Contains(strings.ToLower(m), "required") {
			return false
		}
	}
	return true
}

// pyList renders a list of strings the way Python's repr of a list does.
func pyList(items []string) string {
	parts := make([]string, len(items))
	for i, it := range items {
		parts[i] = pyRepr(it)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func pyRepr(s string) string {
	quote := "'"
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		quote = `"`
	}
	var b strings.Builder
	b.WriteString(quote)
	for _, r := range s {
		switch {
		case string(r) == quote || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteString(quote)
	return b.String()
}

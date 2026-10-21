package portal

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/shahinshabab/deedflow-go/internal/webdriver"
)

// DefaultPortalURL is the Registration Department portal's entry page.
const DefaultPortalURL = "https://pearl.registration.kerala.gov.in/index.php?visit=1"

// Config says where the browser and the portal are.
type Config struct {
	SeleniumURL string // the Selenium Grid / chromedriver address
	PortalURL   string
	Tesseract   string // the tesseract command
	// Headless is the default; false is for interactive/exploratory work only
	// (watching live via noVNC), never for real submissions.
	Headless bool
	// ChromeBinary and DriverArgs are for local runs and tests.
	ChromeBinary string
	DriverArgs   []string
	// Solver reads the login captcha; nil means Tesseract.
	Solver Solver
}

// Solver reads the four digits of a captcha image. The second result is the
// OCR log, for the failure message.
type Solver interface {
	SolveCaptcha(ctx context.Context, image []byte, length int) (string, []string)
}

// Credentials are the organization's portal login.
type Credentials struct {
	Username, Password string
	NewDeedMenuIndex   int
	MyDeedsMenuIndex   int
}

// Stage weights: the percentage of the overall submission each represents.
// They don't need to be exact - they just need to move forward monotonically
// so the progress bar means something.
var stageWeights = map[string]float64{
	"login": 8, "registration": 10, "presenter": 14, "prior_documents": 10, "executants": 16,
	"claimants": 16, "schedule": 16, "document_link": 4, "witnesses": 10,
}

const (
	maxItemAttempts  = 3
	maxTokenAttempts = 6
	maxRecoveries    = 10
)

var tokenRe = regexp.MustCompile(`^T\d{6,}$`)

// digitsOnly is a money amount as bare digits ("2,50,000.00" -> "250000").
func digitsOnly(value string) string {
	text := value
	if i := strings.Index(text, "."); i >= 0 {
		text = text[:i]
	}
	var b strings.Builder
	for _, r := range text {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return strings.TrimLeft(b.String(), "0")
}

// StepResult is what one step of a submission reports.
type StepResult struct {
	OK          bool
	Message     string
	Screenshots [][]byte
	Retryable   bool
}

func okResult(msg string, shots [][]byte) StepResult {
	return StepResult{OK: true, Message: msg, Screenshots: shots, Retryable: true}
}

func failResult(msg string, shots [][]byte, retryable bool) StepResult {
	return StepResult{OK: false, Message: msg, Screenshots: shots, Retryable: retryable}
}

type tab struct{ liID, readyID string }

var (
	tabPriorDoc      = tab{"li_prevDoc", "documentType"}
	tabExecutant     = tab{"li_executant", "ddlExecutantType"}
	tabClaimant      = tab{"li_claimant", "saveForm"}
	tabProperty      = tab{"li_property", "pattikaNo"}
	tabDocLink       = tab{"li_doc_prop_link", "doclink"}
	tabAttestWitness = tab{"li_witness", "wname"}
	tabIdentWitness  = tab{"li_endorse_witness", "wname"}
)

// updateButton is the Update button of a row's edit form: id "saveForm"
// (executant), id "update" (claimant), name "Update" without an id (property)
// - all value "Update" (read live).
var updateButton = webdriver.ByXPath("//input[@type='button' and (@value='Update' or @name='Update' or @name='update')]")

// Saved rows with their edit action: the row's other javascript: link next to
// its Delete link ("Edit" / "മാറ്റം വരുത്തുക" -> showDetails(...) or fillDisplayEdit(...)).
const rowsWithEditJS = `
var seen = [];
return Array.from(document.querySelectorAll("a[href*='showDetailsDel']")).map(function (a) {
  var tr = a.closest('tr');
  if (!tr || seen.indexOf(tr) >= 0) return null;
  seen.push(tr);
  var edit = Array.from(tr.querySelectorAll('a')).map(function (x) { return x.getAttribute('href') || ''; })
    .filter(function (h) { return h.indexOf('javascript:') === 0 && h.indexOf('showDetailsDel') < 0; })[0] || null;
  return {text: tr.innerText, edit: edit};
}).filter(function (r) { return r; });
`

const popupTextJS = `
var c = document.getElementById('popup_container');
if (!c) return null;
var r = c.getBoundingClientRect(), s = window.getComputedStyle(c);
if (r.width === 0 || s.display === 'none' || s.visibility === 'hidden') return null;
var m = document.getElementById('popup_message');
return ((m ? m.innerText : c.innerText) || '').trim();
`

const popupOKJS = `
var b = document.getElementById('popup_ok')
     || document.querySelector('#popup_panel input, #popup_panel button');
if (b) { b.click(); return true; }
return false;
`

// Saved entries on a repeatable tab, found by their Delete links
// ("javascript:showDetailsDel(...)") - every tab's saved-entries table has one
// per row, whatever the table's id/layout, so this works the same on PR
// document, executant, claimant, property, witness and document-link.
const savedRowsJS = `
var seen = [];
return Array.from(document.querySelectorAll("a[href*='showDetailsDel']"))
  .map(function (a) {
    var tr = a.closest('tr');
    if (!tr || seen.indexOf(tr) >= 0) return null;
    seen.push(tr);
    return {text: tr.innerText, delete: a.getAttribute('href')};
  })
  .filter(function (r) { return r; });
`

// Automation is one portal browser session.
type Automation struct {
	cfg  Config
	cred Credentials
	ctx  context.Context
	log  *slog.Logger

	d          *webdriver.Driver
	recoveries int
	token      string
	// lastSelected is the id of the dropdown filled last (a cascade's options
	// come from the AJAX call its change event fires).
	lastSelected   string
	lastLoginError string
	// typed: field id -> the value it held right after this run typed it (for
	// reading a row edit back - see editRow).
	typed     map[string]string
	typedKeys []string
	// deadline: after it the run stops itself cleanly (a *Error) instead of
	// being killed mid-save, which would leave the deed stuck. Zero = none.
	deadline time.Time
	solver   Solver
}

// New starts a browser session for the portal login.
func New(ctx context.Context, cfg Config, cred Credentials, deadline time.Time) (*Automation, error) {
	if cred.Username == "" || cred.Password == "" {
		return nil, errorf("Portal credentials aren't configured for this account. An administrator needs to set them up in Django admin.")
	}
	if cfg.PortalURL == "" {
		cfg.PortalURL = DefaultPortalURL
	}
	tess := cfg.Tesseract
	if tess == "" {
		tess = "tesseract"
	}
	solver := cfg.Solver
	if solver == nil {
		solver = Tesseract{Cmd: tess}
	}
	a := &Automation{cfg: cfg, cred: cred, ctx: ctx, log: slog.Default(), typed: map[string]string{},
		deadline: deadline, solver: solver}
	if err := try(func() { a.startDriver(3) }); err != nil {
		return nil, err
	}
	return a, nil
}

// --- small helpers ---------------------------------------------------------

func (a *Automation) sleep(seconds float64) {
	select {
	case <-time.After(time.Duration(seconds * float64(time.Second))):
	case <-a.ctx.Done():
		panic(errCanceled)
	}
}

func (a *Automation) find(by webdriver.By) *webdriver.Element {
	return mustv(a.d.FindElement(a.ctx, by))
}

func (a *Automation) findAll(by webdriver.By) []*webdriver.Element {
	return mustv(a.d.FindElements(a.ctx, by))
}

func (a *Automation) exec(script string, args ...any) any {
	return mustv(a.d.ExecuteScript(a.ctx, script, args...))
}

func (a *Automation) execAsync(script string, args ...any) any {
	return mustv(a.d.ExecuteAsyncScript(a.ctx, script, args...))
}

func (a *Automation) text(e *webdriver.Element) string { return mustv(e.Text(a.ctx)) }

func (a *Automation) attr(e *webdriver.Element, name string) string {
	return mustv(e.Attribute(a.ctx, name))
}

func (a *Automation) displayed(e *webdriver.Element) bool { return mustv(e.IsDisplayed(a.ctx)) }
func (a *Automation) enabled(e *webdriver.Element) bool   { return mustv(e.IsEnabled(a.ctx)) }
func (a *Automation) selected(e *webdriver.Element) bool  { return mustv(e.IsSelected(a.ctx)) }

func (a *Automation) selectOf(e *webdriver.Element) webdriver.Select { return webdriver.Select{El: e} }

// until is WebDriverWait(driver, timeout).until(fn): fn may raise; a missing
// element keeps waiting, anything else ends the wait.
func (a *Automation) until(timeout float64, fn func() bool) {
	err := webdriver.Until(a.ctx, time.Duration(timeout*float64(time.Second)), func(context.Context) (bool, error) {
		var ok bool
		err := try(func() { ok = fn() })
		return ok, err
	})
	must(err)
}

// untilQuiet is until() with the timeout swallowed (Python's try/except TimeoutException: pass).
func (a *Automation) untilQuiet(timeout float64, fn func() bool) bool {
	err := try(func() { a.until(timeout, fn) })
	if err == nil {
		return true
	}
	if isTimeout(err) {
		return false
	}
	panic(err)
}

func (a *Automation) present(by webdriver.By, timeout float64) *webdriver.Element {
	return mustv(a.d.Present(a.ctx, time.Duration(timeout*float64(time.Second)), by))
}

func (a *Automation) visible(by webdriver.By, timeout float64) *webdriver.Element {
	return mustv(a.d.Visible(a.ctx, time.Duration(timeout*float64(time.Second)), by))
}

func (a *Automation) clickable(by webdriver.By, timeout float64) *webdriver.Element {
	return mustv(a.d.Clickable(a.ctx, time.Duration(timeout*float64(time.Second)), by))
}

// defaultWait is the 15s self.wait.
const defaultWait = 15.0

// --- driver lifecycle ---------------------------------------------------------

func (a *Automation) startDriver(attempts int) {
	args := []string{"--no-sandbox", "--disable-dev-shm-usage", "--window-size=1920,1080"}
	if a.cfg.Headless {
		args = append([]string{"--headless=new"}, args...)
	}
	args = append(args, a.cfg.DriverArgs...)
	var last error
	for attempt := 1; attempt <= attempts; attempt++ {
		// Leave native alert()/confirm() dialogs open for handleDialogs()
		// instead of letting the driver auto-dismiss them: the default
		// ("dismiss and notify") cancels the deletion form's "Are you sure?"
		// confirm - so no row was ever deleted - and throws away the text of a
		// validation alert, the one place the portal says what it didn't like.
		d, err := webdriver.NewRemote(a.ctx, a.cfg.SeleniumURL, webdriver.Options{
			Args: args, Binary: a.cfg.ChromeBinary, UnhandledPrompt: "ignore", PageLoadTimeout: 90 * time.Second})
		if err == nil {
			a.d = d
			return
		}
		// A grid still holding a previous (crashed) session can refuse a new
		// one for a few seconds.
		last = err
		a.log.Warn("Could not start a browser session", "attempt", attempt, "of", attempts, "err", err)
		a.sleep(5 * float64(attempt))
	}
	panic(errorf("Could not start a browser session on the Selenium grid: %s", short(last)))
}

func (a *Automation) restartDriver() {
	a.Close()
	a.startDriver(3)
}

// Close ends the browser session.
func (a *Automation) Close() {
	if a.d == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := a.d.Quit(ctx); err != nil {
		// Usually just an already-dead session (the very thing being
		// recovered from) - one line, not the grid's Java stack trace.
		first := strings.SplitN(err.Error(), "\n", 2)[0]
		if len(first) > 200 {
			first = first[:200]
		}
		a.log.Info("Browser session was already gone when closing it", "err", first)
	}
	a.d = nil
}

func (a *Automation) sessionAlive() bool {
	_, err := a.d.CurrentURL(a.ctx)
	return err == nil || isUnexpAlert(err)
}

// loggedInQuick is a no-wait check that the portal still shows us as logged
// in (the "User : NAME" header / logout link is on every logged-in page).
func (a *Automation) loggedInQuick() bool {
	var res bool
	err := try(func() {
		if len(a.findAll(webdriver.ByID("lnkLogout"))) > 0 ||
			len(a.findAll(webdriver.ByXPath("//span[contains(., 'User')]/font/b"))) > 0 {
			res = true
			return
		}
		// The token wizard's pages have neither of those (confirmed live):
		// they show "User : NAME" as plain text next to the date.
		v := a.exec(`return !!document.body && /User\s*:\s*\S/.test(document.body.innerText);`)
		res, _ = v.(bool)
	})
	switch {
	case err == nil:
		return res
	case isUnexpAlert(err):
		return true
	case isWD(err):
		return false
	}
	panic(err)
}

// looksLoggedOut is positive evidence of a logout, for mid-run checks. Not
// simply "no logout link": not every portal page carries the logged-in header,
// and treating its absence as a logout made the live PR document page log in
// again and again until the run's recovery budget ran out.
func (a *Automation) looksLoggedOut() bool {
	var res bool
	err := try(func() {
		// A page still loading (a slow save's reload) has no furniture yet;
		// judging it then was a false logout on the live portal twice.
		_ = try(func() { must(a.d.DocumentComplete(a.ctx, 30*time.Second)) })
		if len(a.findAll(webdriver.ByID("loginForm"))) > 0 || len(a.findAll(webdriver.ByID("captchaImage"))) > 0 {
			res = true
			return
		}
		if a.loggedInQuick() {
			res = false
			return
		}
		// Neither a login form nor the header: a logout only if the page has
		// none of the logged-in app's furniture either.
		res = !(len(a.findAll(webdriver.ByID("menu"))) > 0 ||
			len(a.findAll(webdriver.ByCSS("li[id^='li_']"))) > 0 ||
			len(a.findAll(webdriver.ByID("docNo"))) > 0)
	})
	switch {
	case err == nil:
		return res
	case isUnexpAlert(err):
		return false
	case isWD(err):
		return true
	}
	panic(err)
}

func (a *Automation) sessionLost() bool { return !a.sessionAlive() || a.looksLoggedOut() }

func (a *Automation) raiseIfSessionLost() {
	if !a.sessionAlive() {
		panic(&SessionLostError{"The browser session ended."})
	}
	a.handleDialogs(0.3, 6)
	if a.looksLoggedOut() {
		panic(&SessionLostError{"The portal logged us out."})
	}
}

func (a *Automation) checkDeadline() {
	if !a.deadline.IsZero() && time.Now().After(a.deadline) {
		panic(errorf("Stopped: the portal was too slow to finish this submission in time."))
	}
}

// recover gets back to a logged-in session with token open (if given).
// Bounded by maxRecoveries per run so a portal that's genuinely down fails the
// run instead of looping forever.
func (a *Automation) recoverSession(token string, reason string) {
	a.checkDeadline()
	a.recoveries++
	if a.recoveries > maxRecoveries {
		panic(errorf("Lost the portal session too many times - stopping this run."))
	}
	if reason == "" {
		reason = "unknown"
	}
	a.log.Warn("Recovering the portal session", "recovery", a.recoveries, "of", maxRecoveries, "after", reason)
	var last error
	for attempt := 1; attempt <= 3; attempt++ {
		err := try(func() {
			if attempt > 1 || !a.sessionAlive() {
				a.restartDriver()
			}
			a.handleDialogs(0.5, 6)
			a.EnsureLoggedIn(10)
			if token != "" {
				a.OpenToken(token)
			}
		})
		if err == nil {
			return
		}
		var pe *Error
		var sl *SessionLostError
		if asErr(err, &pe) || asErr(err, &sl) || isWD(err) {
			last = err
		} else {
			panic(err)
		}
		a.sleep(3 * float64(attempt))
	}
	panic(errorf("Lost the portal session and could not recover it: %s", short(last)))
}

// --- screenshots ----------------------------------------------------------------

// ScreenshotPNG is the page as a PNG.
func (a *Automation) ScreenshotPNG() []byte { return mustv(a.d.Screenshot(a.ctx)) }

// waitPageSettled waits for in-flight jQuery AJAX to finish (this portal is
// jQuery based, so jQuery.active == 0 means nothing's pending through it).
// Some older handlers use raw XMLHttpRequest, which jQuery can't see - the
// short trailing pause covers those.
func (a *Automation) waitPageSettled(timeout float64) {
	_ = try(func() {
		a.until(timeout, func() bool {
			v, _ := a.exec("return (typeof jQuery === 'undefined') || jQuery.active === 0;").(bool)
			return v
		})
	})
	a.sleep(0.5)
}

// captureScrollingScreenshots shoots the whole form top to bottom in
// viewport-sized steps (a single shot only covers one screen), after the page
// settles.
func (a *Automation) captureScrollingScreenshots(maxShots int) [][]byte {
	a.waitPageSettled(10)
	a.exec("window.scrollTo(0, 0);")
	a.sleep(0.2)
	shots := [][]byte{a.ScreenshotPNG()}
	for i := 0; i < maxShots-1; i++ {
		reached, _ := a.exec(
			"var atBottom = (window.innerHeight + window.scrollY) >= (document.body.scrollHeight - 2);" +
				"if (!atBottom) { window.scrollBy(0, window.innerHeight * 0.9); }" +
				"return atBottom;").(bool)
		if reached {
			break
		}
		a.sleep(0.2)
		shots = append(shots, a.ScreenshotPNG())
	}
	return shots
}

// safeScreenshots is the scrolling capture, but never raises - for failure
// paths, where an open dialog or a dead session could break it.
func (a *Automation) safeScreenshots() [][]byte {
	var shots [][]byte
	err := try(func() {
		a.handleDialogs(0.2, 6)
		shots = a.captureScrollingScreenshots(6)
	})
	if err == nil {
		return shots
	}
	if errors_Is(err, errCanceled) {
		panic(err)
	}
	var one []byte
	if err := try(func() { one = a.ScreenshotPNG() }); err != nil {
		first := strings.SplitN(err.Error(), "\n", 2)[0]
		if len(first) > 200 {
			first = first[:200]
		}
		a.log.Warn("Could not capture a screenshot", "err", first)
		return nil
	}
	return [][]byte{one}
}

// --- dialogs ------------------------------------------------------------------------

// nextDialog waits up to timeout seconds for either a native alert/confirm or
// a jQuery popup, accepts/OKs it and returns its text; ok=false if nothing
// appeared.
func (a *Automation) nextDialog(timeout float64) (string, bool) {
	end := time.Now().Add(time.Duration(timeout * float64(time.Second)))
	for {
		if text, err := a.d.AlertText(a.ctx); err == nil {
			must(a.d.AlertAccept(a.ctx))
			a.sleep(0.4)
			return strings.TrimSpace(text), true
		} else if !is(err, webdriver.ErrNoAlert) {
			// Not "no alert": the session itself is in trouble.
			panic(err)
		}
		v, err := a.d.ExecuteScript(a.ctx, popupTextJS)
		if err != nil {
			if isUnexpAlert(err) {
				continue
			}
			panic(err)
		}
		if v != nil {
			if text, ok := v.(string); ok {
				if _, err := a.d.ExecuteScript(a.ctx, popupOKJS); err != nil && !isUnexpAlert(err) {
					panic(err)
				}
				a.sleep(0.5)
				return text, true
			}
		}
		if !time.Now().Before(end) {
			return "", false
		}
		a.sleep(0.25)
	}
}

// handleDialogs clears every dialog currently showing or arriving within wait
// seconds (and shortly after each other, for chained ones like "Saved
// Successfully" followed by "Add another?"). Returns their texts in order.
func (a *Automation) handleDialogs(wait float64, maxDialogs int) []string {
	var seen []string
	for i := 0; i < maxDialogs; i++ {
		w := wait
		if i > 0 {
			w = 1.5
		}
		text, ok := a.nextDialog(w)
		if !ok {
			break
		}
		a.log.Info("Portal dialog", "text", text)
		seen = append(seen, text)
	}
	return seen
}

// transient: a refusal that's the portal's own hiccup ("Error while saving.
// Please try again.", a server error) rather than a verdict on the data - worth
// trying again.
func transient(rejections []string) bool {
	markers := []string{"try again", "error while", "server error", "temporarily", "timed out", "time out"}
	for _, r := range rejections {
		low := strings.ToLower(r)
		found := false
		for _, m := range markers {
			if strings.Contains(low, m) {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// rejections: dialog texts that read as a validation error rather than a
// success message or an "add another?" question.
func rejections(dialogs []string) []string {
	benign := []string{"success", "saved", "stored", "do you want", "add another", "add more"}
	var out []string
	for _, d := range dialogs {
		if d == "" {
			continue
		}
		low := strings.ToLower(d)
		isBenign := false
		for _, b := range benign {
			if strings.Contains(low, b) {
				isBenign = true
			}
		}
		if !isBenign {
			out = append(out, d)
		}
	}
	return out
}

// awaitSaveOutcome, after a Save click, collects dialogs until the save has
// visibly finished - the page reloaded, or no AJAX has been in flight for
// quietNeeded seconds. Some saves (property) run a chain of several async
// requests; leaving the page mid-chain could let the save land *after* we'd
// judged it failed, and a retry then duplicate it.
func (a *Automation) awaitSaveOutcome(timeout, quietNeeded float64) []string {
	var dialogs []string
	quiet := 0.0
	end := time.Now().Add(time.Duration(timeout * float64(time.Second)))
	// The click may already have raised a native dialog (a confirm, a
	// validation alert); while one is open nothing else can be read.
	var oldHTML *webdriver.Element
	for oldHTML == nil && time.Now().Before(end) {
		el, err := a.d.FindElement(a.ctx, webdriver.ByTag("html"))
		if err == nil {
			oldHTML = el
			break
		}
		if !isUnexpAlert(err) {
			panic(err)
		}
		if text, ok := a.nextDialog(0.5); ok {
			a.log.Info("Portal dialog", "text", text)
			dialogs = append(dialogs, text)
		}
	}
	for time.Now().Before(end) {
		if text, ok := a.nextDialog(0.5); ok {
			a.log.Info("Portal dialog", "text", text)
			dialogs = append(dialogs, text)
			quiet = 0
			continue
		}
		var finished bool
		var active float64
		err := try(func() {
			if oldHTML == nil || oldHTML.IsStale(a.ctx) {
				must(a.d.DocumentComplete(a.ctx, 20*time.Second))
				a.waitPageSettled(10)
				dialogs = append(dialogs, a.handleDialogs(1.5, 6)...)
				finished = true
				return
			}
			v := a.exec("return (typeof jQuery === 'undefined') ? 0 : jQuery.active;")
			active, _ = v.(float64)
		})
		if err != nil {
			if isUnexpAlert(err) {
				continue
			}
			panic(err)
		}
		if finished {
			return dialogs
		}
		if active == 0 {
			quiet += 0.5
		} else {
			quiet = 0
		}
		if quiet >= quietNeeded {
			return dialogs
		}
	}
	a.log.Warn("Save outcome still unsettled", "timeout", timeout)
	return dialogs
}

// --- login / captcha ------------------------------------------------------------------

func (a *Automation) fetchCaptchaImageBytes() []byte {
	captcha := a.present(webdriver.ByID("captchaImage"), defaultWait)
	// Drawn before it finished loading, the image comes out blank and OCR
	// reads nothing - a wasted attempt that looks like a hard captcha.
	a.until(15, func() bool {
		v, _ := a.exec("return arguments[0].complete && arguments[0].naturalWidth > 0;", captcha).(bool)
		return v
	})
	b64 := a.execAsync(`
            var ele = arguments[0], callback = arguments[1];
            ele.addEventListener('load', function fn(){
                ele.removeEventListener('load', fn, false);
                var cnv = document.createElement('canvas');
                cnv.width = this.width; cnv.height = this.height;
                cnv.getContext('2d').drawImage(this, 0, 0);
                callback(cnv.toDataURL('image/jpeg').substring(22));
            }, false);
            ele.dispatchEvent(new Event('load'));
            `, captcha)
	s, _ := b64.(string)
	// Python's b64decode drops characters outside the alphabet - and the
	// script's substring(22) leaves the data URL's comma in front.
	var clean strings.Builder
	for _, r := range s {
		if r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '+' || r == '/' {
			clean.WriteRune(r)
		}
	}
	data, err := base64.RawStdEncoding.DecodeString(clean.String())
	if err != nil {
		panic(errorf("The captcha image could not be decoded: %v", err))
	}
	return data
}

func (a *Automation) isLoggedIn(timeout float64) bool {
	return a.untilQuiet(timeout, func() bool { return a.loggedInQuick() })
}

func (a *Automation) isMalayalamVersion() bool {
	// A notice open on the page would make every lookup below fail.
	a.handleDialogs(0.5, 6)
	var res bool
	err := try(func() {
		label := a.present(webdriver.ByCSS("#menu11 .menulink"), 5)
		res = strings.Contains(strings.TrimSpace(a.text(label)), "പ്രധാന പേജ്")
	})
	if err != nil {
		if errors_Is(err, errCanceled) {
			panic(err)
		}
		return false
	}
	return res
}

func (a *Automation) switchMalayalam() {
	err := try(func() {
		img := a.visible(webdriver.ByXPath("//*[@id='content']/div/span/div[1]/div[7]/img"), 5)
		a.handleDialogs(0.3, 6)
		a.jsClick(img)
	})
	if err != nil {
		if errors_Is(err, errCanceled) {
			panic(err)
		}
		a.log.Warn("Error switching language to Malayalam", "err", err)
	}
}

// attemptLoginOnce is one login attempt against whatever captcha is currently
// on the page. It returns (success, error message, captcha OCR log).
func (a *Automation) attemptLoginOnce() (bool, string, []string) {
	err := try(func() {
		// Notices/popups on the landing page would swallow the click.
		a.handleDialogs(0.5, 6)
		login := a.present(webdriver.ByXPath(`//*[@id="content"]/div/span/div[1]/div[2]/img`), defaultWait)
		a.jsClick(login)
		a.present(webdriver.ByID("loginForm"), defaultWait)
		user := a.clickable(webdriver.ByID("user_name"), defaultWait)
		pass := a.find(webdriver.ByID("passwd"))
		must(user.Clear(a.ctx))
		must(user.SendKeys(a.ctx, a.cred.Username))
		must(pass.Clear(a.ctx))
		must(pass.SendKeys(a.ctx, a.cred.Password))
	})
	if err != nil {
		return false, "Could not open the login form: " + short(err), nil
	}
	image, err := tryv(a.fetchCaptchaImageBytes)
	if err != nil {
		return false, "Could not read the captcha image: " + short(err), nil
	}
	answer, ocrLog := a.solver.SolveCaptcha(a.ctx, image, 4)
	if answer == "" {
		return false, "Captcha OCR could not produce a 4-digit answer.", ocrLog
	}
	err = try(func() {
		input := a.find(webdriver.ByID("imgtext"))
		must(input.Clear(a.ctx))
		must(input.SendKeys(a.ctx, answer))
		a.jsClick(a.find(webdriver.ByXPath("//input[@name='Login']")))
	})
	if err != nil {
		return false, "Error submitting the login form: " + short(err), ocrLog
	}
	dialogs := a.handleDialogs(5, 6)
	// A dialog here isn't necessarily a rejection: the portal also greets a
	// successful login with notices. Whether we're in is what counts.
	timeout := 8.0
	if len(dialogs) > 0 {
		timeout = 4
	}
	if a.isLoggedIn(timeout) {
		if len(dialogs) > 0 {
			a.log.Info("Logged in; portal notice(s) after login", "notices", dialogs)
		}
		return true, "", ocrLog
	}
	if len(dialogs) > 0 {
		return false, "Portal rejected login: " + dialogs[0], ocrLog
	}
	return false, "Login did not complete (no error shown).", ocrLog
}

// EnsureLoggedIn logs in, retrying with a genuinely fresh captcha each time
// via a full get() (a refresh() after the login POST would hit Chrome's
// "confirm form resubmission" page instead).
func (a *Automation) EnsureLoggedIn(maxAttempts int) {
	if a.sessionAlive() && a.loggedInQuick() {
		return
	}
	errMsg := ""
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		a.checkDeadline()
		err := try(func() {
			if a.loginAndSwitchLanguage(attempt, maxAttempts) {
				errMsg = ""
				attempt = -attempt // success marker
				return
			}
			errMsg = a.lastLoginError
		})
		if attempt < 0 && err == nil {
			return
		}
		if err != nil {
			if !isWD(err) {
				panic(err)
			}
			errMsg = "Browser error while logging in: " + short(err)
			a.log.Warn("Login attempt failed", "attempt", attempt, "of", maxAttempts, "err", errMsg)
			if !a.sessionAlive() {
				// The browser itself died - a fresh one, then log in again.
				if rerr := try(a.restartDriver0); rerr != nil {
					var pe *Error
					if asErr(rerr, &pe) {
						errMsg = pe.Msg
					} else {
						panic(rerr)
					}
				}
			}
		}
		a.sleep(2)
	}
	panic(errorf("Could not log in after %d attempts: %s", maxAttempts, errMsg))
}

func (a *Automation) restartDriver0() { a.restartDriver() }

// loginAndSwitchLanguage is one pass of: load the portal, log in (unless still
// logged in), switch to Malayalam. false (reason in lastLoginError) means try
// again - including when the session was dropped between logging in and
// switching the language, which used to fail the whole run.
func (a *Automation) loginAndSwitchLanguage(attempt, maxAttempts int) bool {
	a.handleDialogs(0.3, 6)
	if err := a.d.Get(a.ctx, a.cfg.PortalURL); err != nil {
		switch {
		case isUnexpAlert(err):
			a.handleDialogs(0.5, 6)
		case isWD(err):
			a.lastLoginError = "Could not load the portal: " + short(err)
			a.log.Warn("Login attempt failed", "attempt", attempt, "of", maxAttempts, "err", a.lastLoginError)
			a.sleep(3 * float64(attempt))
			return false
		default:
			panic(err)
		}
	}
	// Still logged in from before (the portal sends a live session straight to
	// its home page, with no login form to fill).
	a.handleDialogs(0.5, 6)
	if a.loggedInQuick() {
		a.log.Info("Already logged in to the portal.")
	} else {
		ok, errMsg, ocrLog := a.attemptLoginOnce()
		if !ok {
			a.lastLoginError = errMsg
			a.log.Warn("Login attempt failed", "attempt", attempt, "of", maxAttempts, "err", errMsg, "captcha", ocrLog)
			return false
		}
		a.log.Info("Logged in to the portal", "attempt", attempt)
	}
	for i := 0; i < 3; i++ {
		if a.isMalayalamVersion() {
			return true
		}
		if a.looksLoggedOut() {
			a.lastLoginError = "The portal logged us out while switching to Malayalam."
			a.log.Warn("Login attempt failed", "attempt", attempt, "of", maxAttempts, "err", a.lastLoginError)
			return false
		}
		a.switchMalayalam()
		a.sleep(2)
	}
	a.lastLoginError = "Logged in, but could not switch the portal to Malayalam."
	a.log.Warn("Login attempt failed", "attempt", attempt, "of", maxAttempts, "err", a.lastLoginError)
	return false
}

// --- navigation ----------------------------------------------------------------------------

func (a *Automation) navigateMenu(index int) {
	a.handleDialogs(0.3, 6)
	parent := a.present(webdriver.ByXPath("//*[@id='menu']/li[5]/a"), defaultWait)
	a.exec("arguments[0].dispatchEvent(new MouseEvent('mouseover', {bubbles: true, cancelable: true}));", parent)
	a.sleep(1)
	item := a.present(webdriver.ByXPath(fmt.Sprintf("//*[@id='menu']/li[5]/ul/li[%d]/a", index)), defaultWait)
	a.exec("arguments[0].click();", item)
}

func (a *Automation) gotoMyToken() {
	// Checked by URL, not just by showDetails() existing: the wizard's own
	// executant/claimant pages define a different showDetails().
	a.navigateMenu(a.cred.MyDeedsMenuIndex)
	a.until(30, func() bool {
		u := mustv(a.d.CurrentURL(a.ctx))
		if !strings.Contains(u, "tokensearch.php") {
			return false
		}
		v, _ := a.exec("return document.readyState === 'complete' && typeof showDetails === 'function';").(bool)
		return v
	})
}

// TokenRow is a row of the My Token list.
type TokenRow struct{ Token, Name, Date string }

// listMyTokens is the first page (newest first) of the "My Token" list.
func (a *Automation) listMyTokens() []TokenRow {
	a.gotoMyToken()
	rows, _ := a.exec(`
            return Array.from(document.querySelectorAll('tr')).map(function (tr) {
              return Array.from(tr.querySelectorAll('td')).map(function (td) { return td.innerText.trim(); });
            });
            `).([]any)
	var out []TokenRow
	for _, r := range rows {
		cells, _ := r.([]any)
		if len(cells) == 0 {
			continue
		}
		first, _ := cells[0].(string)
		if !tokenRe.MatchString(first) {
			continue
		}
		row := TokenRow{Token: first}
		if len(cells) > 1 {
			row.Name, _ = cells[1].(string)
		}
		if len(cells) > 2 {
			row.Date, _ = cells[2].(string)
		}
		out = append(out, row)
	}
	return out
}

// OpenToken re-enters an existing token's 13-tab wizard via My Token - the
// only safe way back into a token (New Token would create another).
func (a *Automation) OpenToken(token string) {
	var last error
	for attempt := 1; attempt <= 3; attempt++ {
		err := try(func() {
			a.handleDialogs(0.3, 6)
			a.gotoMyToken()
			a.exec("showDetails(arguments[0]);", token)
			link := a.present(webdriver.ByXPath("//a[contains(., 'Enter Document Details')]"), 20)
			a.jsClick(link)
			a.present(webdriver.ByXPath(fmt.Sprintf("//*[@id='%s']/a", tabPriorDoc.liID)), 20)
			a.waitPageSettled(10)
		})
		if err == nil {
			return
		}
		if !isWD(err) {
			panic(err)
		}
		last = err
		a.log.Warn("Could not open token", "token", token, "attempt", attempt, "of", 3, "err", err)
		if !a.sessionAlive() {
			panic(&SessionLostError{"Browser session ended while opening the token."})
		}
		a.sleep(2)
	}
	panic(errorf("Could not open token %s via My Token: %s", token, short(last)))
}

// openTab opens one of the wizard's tabs and waits for its form to be ready -
// always a fresh load, so a half-filled or stale form from an earlier attempt
// never carries over.
func (a *Automation) openTab(t tab) {
	var last error
	for attempt := 1; attempt <= 3; attempt++ {
		var lastErr error
		err := try(func() {
			a.handleDialogs(0.3, 6)
			if attempt > 1 && a.token != "" && len(a.findAll(webdriver.ByID(t.liID))) == 0 {
				// Stuck on a page without the wizard's tab bar (a server error
				// page, a half-loaded reload): go back in through My Token.
				if err := try(func() { a.OpenToken(a.token) }); err != nil {
					var sl *SessionLostError
					var pe *Error
					if asErr(err, &sl) {
						panic(err)
					}
					if asErr(err, &pe) {
						lastErr = err
						return
					}
					panic(err)
				}
			}
			oldHTML := a.find(webdriver.ByTag("html"))
			link := a.present(webdriver.ByXPath(fmt.Sprintf("//*[@id='%s']/a", t.liID)), 15)
			a.jsClick(link)
			a.untilQuiet(15, func() bool { return oldHTML.IsStale(a.ctx) })
			a.handleDialogs(0.5, 6)
			a.present(webdriver.ByID(t.readyID), 20)
			a.waitPageSettled(10)
		})
		if err == nil && lastErr == nil {
			return
		}
		if lastErr != nil {
			last = lastErr
			continue
		}
		switch {
		case isUnexpAlert(err):
			last = err
		case isWD(err):
			last = err
			a.raiseIfSessionLost()
			a.sleep(1.5)
		default:
			panic(err)
		}
	}
	panic(fillErrorf(true, "Could not open the '%s' tab: %s", t.liID, short(last)))
}

// cleanText normalises text for comparison: NFC, with format characters dropped.
func cleanText(text string) string {
	n := norm.NFC.String(text)
	var b strings.Builder
	for _, r := range n {
		if !unicode.Is(unicode.Cf, r) {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

func secs(seconds float64) time.Duration { return time.Duration(seconds * float64(time.Second)) }

func exceptionNameOf(err error) string {
	var wd *webdriver.Error
	if asErr(err, &wd) {
		return exceptionName(wd)
	}
	return fmt.Sprintf("%T", err)
}

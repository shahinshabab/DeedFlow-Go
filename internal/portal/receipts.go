package portal

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/db"
	"github.com/shahinshabab/deedflow-go/internal/webdriver"
)

// The receipt scraper reads payment receipts off the Registration Department
// portal - strictly read-only. It only ever opens lists and views, following
// what a person does by hand:
//
//  1. My Token -> "view all" -> every row's status; the "Registered" rows are
//     the tokens to collect. Then log out.
//  2. For each token (logging in again whenever the portal has dropped us):
//     home page (Malayalam) -> e-Payment -> type the token -> View Details ->
//     "Online - Payment Details" -> "1. Stamp Duty" -> Receipt, then
//     "2. Registration Fee" -> Receipt.
//
// Safety: this is a real office's portal account. The only things ever clicked
// are the header's Home / e-Payment / Logout icons and the controls named
// above, and click() presses one only if its handler is one of the
// display-only allowedHandlers - so never, notably, the "e-Stamp" button next
// to each Receipt, which texts an OTP to the party's phone. Nothing here types
// into any field except the token search box.

// Registered is the status of a token whose receipts are collected.
const Registered = "Registered"

// The only handlers this module ever triggers on a page (beyond the header's
// Home / e-Payment navigation icons). All of them only display data.
var allowedHandlers = []string{"sorter.showall", "getDocumentAndPayDetails", "getEstampPaymentDetails",
	"getPaymentDetails", "viewPaymentReceipt"}

// Belt and braces: never press anything whose handler mentions these.
var unsafeHandler = regexp.MustCompile(`(?i)save|insert|update|delete|remove|confirm|proceed|otp|topay|gotopayment|signout|logout`)

const myTokenRowsJS = `
return Array.from(document.querySelectorAll('tr')).map(function (tr) {
  return Array.from(tr.querySelectorAll(':scope > td')).map(function (td) { return td.innerText.trim(); });
}).filter(function (cells) { return cells.length >= 5; });
`

// label/value rows of a small two-column table (receipt, presenter details).
const kvRowsJS = `
var root = document.getElementById(arguments[0]);
if (!root) return null;
var out = [];
root.querySelectorAll('tr').forEach(function (tr) {
  var tds = Array.from(tr.querySelectorAll(':scope > td')).map(function (td) {
    return td.innerText.replace(/\s+/g, ' ').trim(); });
  for (var i = 0; i + 1 < tds.length; i += 2) if (tds[i]) out.push([tds[i], tds[i + 1]]);
  if (tds.length === 1 && tds[0]) out.push([tds[0], null]);
});
return out;
`

const paymentRowsJS = `
var root = document.getElementById('content_fee');
if (!root) return null;
var heading = (root.querySelector('.heading') || {}).innerText || '';
var rows = [];
root.querySelectorAll('table tr').forEach(function (tr) {
  var tds = tr.querySelectorAll(':scope > td');
  var btn = tr.querySelector("input[type=button][value='Receipt']");
  if (tds.length >= 5 && btn) rows.push({
    transaction_no: tds[0].innerText.trim(), payment_type: tds[1].innerText.trim(), amount: tds[2].innerText.trim(),
    payment_mode: tds[3].innerText.trim(), status: tds[4].innerText.trim(), onclick: btn.getAttribute('onclick') || ''});
});
return {heading: heading.trim(), rows: rows, text: root.innerText.slice(0, 2000)};
`

var subtabs = map[string][2]string{"SD": {"li_stamp_det", "stamp duty"}, "RF": {"li_pay_det", "registration fee"}}

var nonAmount = regexp.MustCompile(`[^\d.]`)

// ParseAmount reads "Rs 2,50,000/-" style amounts; nil when there are none.
func ParseAmount(text string) *db.Decimal {
	digits := strings.Trim(nonAmount.ReplaceAllString(strings.ReplaceAll(text, "/-", ""), ""), ".")
	if digits == "" {
		return nil
	}
	d, err := db.ParseDecimal(digits)
	if err != nil {
		return nil
	}
	return &d
}

var defaultDateTimeFormats = []string{"02/01/2006 15:04:05", "02-01-2006 15:04:05", "02/01/2006 15:04", "02-01-2006"}

// ParseDateTime reads the portal's day-first date-times in loc; nil if unreadable.
func ParseDateTime(text string, loc *time.Location, formats ...string) *time.Time {
	text = strings.TrimSpace(text)
	if len(formats) == 0 {
		formats = defaultDateTimeFormats
	}
	for _, f := range formats {
		if t, err := time.ParseInLocation(f, text, loc); err == nil {
			return &t
		}
	}
	return nil
}

// ParseDate reads a day-first date as a date at midnight UTC; nil if unreadable.
func ParseDate(text string) *time.Time {
	if t := ParseDateTime(text, time.UTC, "02-01-2006", "02/01/2006"); t != nil {
		return t
	}
	return nil
}

// RegisteredRow is a "Registered" row of My Token.
type RegisteredRow struct {
	Token, Name string
	Date        *time.Time
	Status      string
}

// ParseMyTokenRows keeps the rows whose status is exactly "Registered".
func ParseMyTokenRows(rows [][]string) []RegisteredRow {
	var out []RegisteredRow
	for _, cells := range rows {
		if len(cells) < 5 || !tokenRe.MatchString(cells[0]) {
			continue
		}
		status := strings.Join(strings.Fields(cells[4]), " ")
		if status != Registered {
			continue
		}
		out = append(out, RegisteredRow{cells[0], cells[1], ParseDate(cells[2]), status})
	}
	return out
}

// Pair is a [label, value] row; a nil value marks a title row.
type Pair struct {
	Label string
	Value *string
}

// Receipt is one payment receipt.
type Receipt struct {
	TransactionNo, ApplicantName, SRO, GRN string
	Amount                                 *db.Decimal
	PaymentMode, BankReference, BankCIN    string
	PaidAt                                 *time.Time
	Status, Title                          string
	Fields                                 [][2]string
}

var receiptLabels = map[string]string{
	"transaction number": "transaction_no", "applicant's name": "applicant_name", "sub registry office": "sro",
	"grn": "grn", "receipt amount": "amount", "bank reference no": "bank_reference", "bank cin": "bank_cin",
	"receipt date and time": "paid_at", "status": "status",
}

func labelKey(label string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(label), ":"))
}

// ParseReceipt turns the receipt's rows into field values (+ Title, and every
// pair under Fields).
func ParseReceipt(pairs []Pair, loc *time.Location) Receipt {
	r := Receipt{Fields: [][2]string{}}
	var amount, paid string
	for _, p := range pairs {
		if p.Value == nil {
			if r.Title == "" {
				r.Title = p.Label
			}
			continue
		}
		r.Fields = append(r.Fields, [2]string{p.Label, *p.Value})
		value := strings.TrimSpace(*p.Value)
		switch receiptLabels[labelKey(p.Label)] {
		case "transaction_no":
			r.TransactionNo = value
		case "applicant_name":
			r.ApplicantName = value
		case "sro":
			r.SRO = value
		case "grn":
			r.GRN = value
		case "amount":
			amount = value
		case "bank_reference":
			r.BankReference = value
		case "bank_cin":
			r.BankCIN = value
		case "paid_at":
			paid = value
		case "status":
			r.Status = value
		}
	}
	r.Amount = ParseAmount(amount)
	r.PaidAt = ParseDateTime(paid, loc)
	return r
}

// Presenter is the e-Payment page's presenter details.
type Presenter struct {
	Name, Address, Place, City, SRO string
	PresentedAt                     *time.Time
	DocumentAmount                  *db.Decimal
}

var presenterLabels = map[string]string{"name": "name", "address": "address", "place": "place", "city": "city",
	"presentation date & time": "presented_at", "document amount": "document_amount", "sro": "sro"}

// ParsePresenter reads the presenter rows.
func ParsePresenter(pairs []Pair, loc *time.Location) Presenter {
	var p Presenter
	var presented, amount string
	for _, pair := range pairs {
		if pair.Value == nil {
			continue
		}
		value := strings.TrimSpace(*pair.Value)
		switch presenterLabels[labelKey(pair.Label)] {
		case "name":
			p.Name = value
		case "address":
			p.Address = value
		case "place":
			p.Place = value
		case "city":
			p.City = value
		case "sro":
			p.SRO = value
		case "presented_at":
			presented = value
		case "document_amount":
			amount = value
		}
	}
	p.PresentedAt = ParseDateTime(presented, loc)
	p.DocumentAmount = ParseAmount(amount)
	return p
}

// TokenReceipts is everything read for one token.
type TokenReceipts struct {
	Presenter Presenter
	Stamp     []Receipt
	Fee       []Receipt
}

// ReceiptScraper reads receipts through an Automation session.
type ReceiptScraper struct {
	bot      *Automation
	deadline time.Time
	loc      *time.Location
}

// NewReceiptScraper wraps a browser session; loc is the portal's time zone.
func NewReceiptScraper(bot *Automation, deadline time.Time, loc *time.Location) *ReceiptScraper {
	return &ReceiptScraper{bot: bot, deadline: deadline, loc: loc}
}

// OutOfTime is the message of the deadline stop.
const OutOfTime = "Stopped: out of time for tonight's run."

func (r *ReceiptScraper) checkDeadline() {
	if !r.deadline.IsZero() && time.Now().After(r.deadline) {
		panic(errorf(OutOfTime))
	}
}

// click presses element only if its handler is handler (one of the
// display-only allowedHandlers) and nothing else.
func (r *ReceiptScraper) click(el *webdriver.Element, handler string) {
	b := r.bot
	var parts []string
	for _, attr := range []string{"onclick", "href"} {
		if v := b.attr(el, attr); v != "" {
			parts = append(parts, v)
		}
	}
	action := strings.TrimSpace(strings.Join(parts, " "))
	action = strings.TrimSpace(strings.TrimPrefix(action, "javascript:"))
	allowed := false
	for _, h := range allowedHandlers {
		if h == handler {
			allowed = true
		}
	}
	if !allowed || !strings.HasPrefix(action, handler+"(") || unsafeHandler.MatchString(action) {
		shown := action
		if len(shown) > 120 {
			shown = shown[:120]
		}
		panic(errorf("Refusing to click a control that isn't a plain view (%s).", pyRepr(shown)))
	}
	b.jsClick(el)
}

// wait polls fn until it reports done; a timeout is a lost session if the
// portal has logged us out, else a plain "timed out" error.
func (r *ReceiptScraper) wait(timeout float64, what string, fn func() bool) {
	b := r.bot
	err := try(func() { b.until(timeout, fn) })
	if err == nil {
		return
	}
	if !isTimeout(err) {
		panic(err)
	}
	b.handleDialogs(0.5, 6)
	if b.sessionLost() {
		panic(&SessionLostError{fmt.Sprintf("Lost the portal session waiting for %s.", what)})
	}
	panic(errorf("Timed out waiting for %s.", what))
}

func (r *ReceiptScraper) icon(srcPart string) *webdriver.Element {
	found := r.bot.findAll(webdriver.ByXPath(fmt.Sprintf("//img[contains(@src, '%s')]", srcPart)))
	if len(found) == 0 {
		panic(&SessionLostError{fmt.Sprintf("The portal header (%s) isn't on the page.", srcPart)})
	}
	return found[0]
}

// navigate clicks a header icon and waits for the next page to replace this
// one (judging the old page "ready" was a race).
func (r *ReceiptScraper) navigate(srcPart, what string) {
	b := r.bot
	old := b.find(webdriver.ByTag("html"))
	b.jsClick(r.icon(srcPart))
	r.wait(30, what, func() bool { return old.IsStale(b.ctx) })
	r.wait(30, what, func() bool {
		v, _ := b.exec("return document.readyState").(string)
		return v == "complete"
	})
	b.handleDialogs(0.5, 6)
}

func (r *ReceiptScraper) settle() {
	if err := try(func() { r.bot.waitPageSettled(10) }); err != nil && !isWD(err) {
		panic(err)
	}
}

// markContent tags what #content_fee holds now, so a wait can tell the
// reloaded content from the old (both can carry the same heading).
func (r *ReceiptScraper) markContent() {
	r.bot.exec("var c = document.getElementById('content_fee');" +
		" if (c) Array.from(c.children).forEach(function (e) { e.setAttribute('data-df-old', '1'); });")
}

// ensureLoggedIn: logged in (with the portal in Malayalam), logging in again
// if the portal dropped the session.
func (r *ReceiptScraper) ensureLoggedIn() {
	b := r.bot
	r.checkDeadline()
	if b.d == nil || !b.sessionAlive() {
		b.restartDriver()
	}
	b.handleDialogs(0.3, 6)
	if !b.loggedInQuick() {
		b.EnsureLoggedIn(10)
	}
}

func (r *ReceiptScraper) logout() {
	b := r.bot
	err := try(func() {
		if b.sessionAlive() && b.loggedInQuick() {
			// Logout is the one header icon click() won't press.
			b.jsClick(r.icon("images/logout_img.png"))
			b.sleep(2)
			b.handleDialogs(1, 6)
		}
	})
	if err != nil {
		if !(isWD(err) || IsSessionLost(err)) {
			panic(err)
		}
		first := strings.SplitN(err.Error(), "\n", 2)[0]
		if len(first) > 200 {
			first = first[:200]
		}
		b.log.Info("Could not log out cleanly", "err", first)
	}
}

// Logout signs out of the portal (a failure to is only logged).
func (r *ReceiptScraper) Logout() error { return try(r.logout) }

// RegisteredTokens is step 1: the tokens My Token shows as Registered.
func (r *ReceiptScraper) RegisteredTokens() (out []RegisteredRow, err error) {
	err = try(func() {
		b := r.bot
		r.ensureLoggedIn()
		b.gotoMyToken()
		if showAll := b.findAll(webdriver.ByXPath("//a[contains(@href, 'sorter.showall')]")); len(showAll) > 0 {
			r.click(showAll[0], "sorter.showall")
			b.sleep(2)
		}
		raw, _ := b.exec(myTokenRowsJS).([]any)
		var rows [][]string
		any1 := false
		for _, rr := range raw {
			cells, _ := rr.([]any)
			row := make([]string, len(cells))
			for i, c := range cells {
				row[i], _ = c.(string)
			}
			rows = append(rows, row)
			if len(row) > 0 && tokenRe.MatchString(row[0]) {
				any1 = true
			}
		}
		if !any1 {
			panic(errorf("My Token showed no tokens."))
		}
		out = ParseMyTokenRows(rows)
	})
	return out, err
}

func (r *ReceiptScraper) openEpayment() {
	b := r.bot
	// Home page first (and Malayalam, as the office uses it), then the
	// e-Payment icon - by clicking it: its handler carries the page's one-time
	// tok, so building the URL ourselves gets "Retry After Some Time".
	r.navigate("images/home.png", "the home page")
	if !b.isMalayalamVersion() {
		b.switchMalayalam()
		b.sleep(2)
	}
	r.navigate("images/payment.png", "the e-Payment page")
	r.wait(30, "the e-Payment page", func() bool {
		u := mustv(b.d.CurrentURL(b.ctx))
		return strings.Contains(u, "direct_payment.php") && len(b.findAll(webdriver.ByID("tokenNo"))) > 0
	})
}

func (r *ReceiptScraper) pairs(id string) []Pair {
	raw, _ := r.bot.exec(kvRowsJS, id).([]any)
	var out []Pair
	for _, item := range raw {
		pair, _ := item.([]any)
		if len(pair) != 2 {
			continue
		}
		label, _ := pair[0].(string)
		p := Pair{Label: label}
		if v, ok := pair[1].(string); ok {
			p.Value = &v
		}
		out = append(out, p)
	}
	return out
}

func (r *ReceiptScraper) viewDetails(token string) Presenter {
	b := r.bot
	box := b.find(webdriver.ByID("tokenNo"))
	must(box.Clear(b.ctx))
	must(box.SendKeys(b.ctx, strings.TrimPrefix(token, "T")))
	r.click(b.find(webdriver.ByID("submitbtn")), "getDocumentAndPayDetails")
	result := ""
	r.wait(40, "the token's details", func() bool {
		if len(b.findAll(webdriver.ByID("li_pay"))) > 0 {
			result = "ok"
			return true
		}
		msg, _ := b.exec("var e = document.getElementById('div_doc_error');" +
			" return e && e.offsetParent !== null ? e.innerText.trim() : '';").(string)
		if msg != "" {
			result = "error:" + msg
			return true
		}
		return false
	})
	if result != "ok" {
		panic(&ReceiptsUnavailable{strings.SplitN(result, ":", 2)[1]})
	}
	return ParsePresenter(r.pairs("Presenter"), r.loc)
}

func (r *ReceiptScraper) openPaymentTab() {
	b := r.bot
	r.click(b.find(webdriver.ByID("li_pay")), "getEstampPaymentDetails")
	// It loads in steps (several requests, the stamp duty view last).
	r.wait(40, "the payment details", func() bool {
		if len(b.findAll(webdriver.ByID("li_stamp_det"))) > 0 {
			return true
		}
		v, _ := b.exec("var c = document.getElementById('content_fee'); return c && /\\S/.test(c.innerText) &&" +
			" !document.getElementById('li_stamp_det') ? 'message' : false;").(string)
		return v != ""
	})
	if len(b.findAll(webdriver.ByID("li_stamp_det"))) == 0 {
		text, _ := b.exec("return document.getElementById('content_fee').innerText.trim();").(string)
		if len([]rune(text)) > 200 {
			text = string([]rune(text)[:200])
		}
		panic(&ReceiptsUnavailable{"No payment details: " + text})
	}
	r.settle()
}

type paymentRow struct{ transactionNo, amount, paymentMode, onclick string }

type paymentInfo struct {
	heading string
	rows    []paymentRow
}

func (r *ReceiptScraper) readPaymentInfo() *paymentInfo {
	raw, _ := r.bot.exec(paymentRowsJS).(map[string]any)
	if raw == nil {
		return nil
	}
	info := &paymentInfo{}
	info.heading, _ = raw["heading"].(string)
	rows, _ := raw["rows"].([]any)
	for _, item := range rows {
		m, _ := item.(map[string]any)
		var row paymentRow
		row.transactionNo, _ = m["transaction_no"].(string)
		row.amount, _ = m["amount"].(string)
		row.paymentMode, _ = m["payment_mode"].(string)
		row.onclick, _ = m["onclick"].(string)
		info.rows = append(info.rows, row)
	}
	return info
}

func (r *ReceiptScraper) subtabRows(kind string) *paymentInfo {
	b := r.bot
	liID, headingWord := subtabs[kind][0], subtabs[kind][1]
	li := b.find(webdriver.ByID(liID))
	if !strings.Contains(strings.ReplaceAll(b.attr(li, "onclick"), `"`, "'"), fmt.Sprintf("getPaymentDetails('%s')", kind)) {
		panic(errorf("The %s sub tab isn't what was expected.", headingWord))
	}
	info := r.readPaymentInfo()
	active := false
	for _, c := range strings.Fields(b.attr(li, "class")) {
		if c == "active" {
			active = true
		}
	}
	if active && info != nil && strings.Contains(strings.ToLower(info.heading), headingWord) {
		// The payment tab opens on stamp duty; pressing it again only reloads
		// the same table under our feet.
		return info
	}
	r.markContent()
	r.click(li, "getPaymentDetails")
	var loaded *paymentInfo
	r.wait(40, fmt.Sprintf("the %s payment details", headingWord), func() bool {
		if len(b.findAll(webdriver.ByCSS("#content_fee [data-df-old]"))) > 0 {
			return false
		}
		cur := r.readPaymentInfo()
		if cur != nil && strings.Contains(strings.ToLower(cur.heading), headingWord) && len(b.findAll(webdriver.ByID(liID))) > 0 {
			loaded = cur
			return true
		}
		return false
	})
	r.settle()
	return loaded
}

func (r *ReceiptScraper) receipt(row paymentRow) Receipt {
	b := r.bot
	for attempt := 0; attempt < 3; attempt++ {
		var match *webdriver.Element
		for _, btn := range b.findAll(webdriver.ByXPath("//*[@id='content_fee']//input[@type='button' and @value='Receipt']")) {
			if b.attr(btn, "onclick") == row.onclick {
				match = btn
				break
			}
		}
		if match == nil {
			panic(errorf("No Receipt button for transaction %s.", row.transactionNo))
		}
		err := try(func() { r.click(match, "viewPaymentReceipt") })
		if err == nil {
			break
		}
		if !isStale(err) || attempt == 2 {
			panic(err)
		}
		r.settle()
	}
	var got []Pair
	r.wait(40, fmt.Sprintf("receipt %s", row.transactionNo), func() bool {
		pairs := r.pairs("payReceipt_div")
		for _, p := range pairs {
			if p.Value != nil && strings.TrimSpace(*p.Value) == row.transactionNo {
				got = pairs
				return true
			}
		}
		return false
	})
	data := ParseReceipt(got, r.loc)
	data.PaymentMode = row.paymentMode
	if data.TransactionNo == "" {
		data.TransactionNo = row.transactionNo
	}
	if data.Amount == nil {
		data.Amount = ParseAmount(row.amount)
	}
	return data
}

// TokenReceipts is step 2: one token's presenter details and receipts.
func (r *ReceiptScraper) TokenReceipts(token string) (out *TokenReceipts, err error) {
	err = try(func() {
		r.ensureLoggedIn()
		r.openEpayment()
		res := &TokenReceipts{Presenter: r.viewDetails(token)}
		r.openPaymentTab()
		for _, kind := range []string{"SD", "RF"} {
			info := r.subtabRows(kind)
			var list []Receipt
			for _, row := range info.rows {
				list = append(list, r.receipt(row))
			}
			if kind == "SD" {
				res.Stamp = list
			} else {
				res.Fee = list
			}
		}
		out = res
	})
	return out, err
}

// IsReceiptsUnavailable reports a token whose payment details can't be shown.
func IsReceiptsUnavailable(err error) bool {
	var ru *ReceiptsUnavailable
	return asErr(err, &ru)
}

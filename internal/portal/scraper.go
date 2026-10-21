package portal

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/deeds"
	"github.com/shahinshabab/deedflow-go/internal/webdriver"
)

// The dropdown scraper reads every dropdown on the registration portal -
// including every level of every cascading dropdown (district -> SRO -> taluk
// -> village ...) - so DeedFlow's wizard can offer exactly the options the
// portal will accept. The result is a JSON file that Store.ImportScrape stores
// in the PortalDropdown table.
//
// It is read-only by construction: it only opens pages, picks dropdown values
// and repeats the lookups those picks make. It never clicks a button, so
// nothing is ever saved on the portal (the New Token page is left without
// saving, and token tabs are reached through My Token on an existing token).
//
// How a page is read, without knowing its markup in advance:
//
//  1. Discover the cascade. Every <select> on the page is tried with a few of
//     its own options; any other dropdown whose options change in response is
//     its child. While doing that, the page's AJAX calls are recorded
//     (XMLHttpRequest is wrapped in-page).
//  2. Learn each lookup. For a child dropdown, the recorded request whose
//     response holds exactly the options that appeared is its lookup; the
//     request's parameters are matched to the parent dropdowns' values. The
//     lookup is then replayed for the other sampled values and must reproduce
//     what the page itself showed - only then is it trusted.
//  3. Walk every combination. Each cascade is walked parent by parent. With
//     trusted lookups that's done by calling them directly from inside the
//     logged-in page (hundreds of times faster than picking values one by one
//     and waiting for the page); a cascade with any lookup that couldn't be
//     learned is walked by picking values on the page instead.

const hookJS = `
if (!window.__df) {
  window.__df = {pending: 0, log: []};
  var P = XMLHttpRequest.prototype, open = P.open, send = P.send, header = P.setRequestHeader;
  P.open = function (method, url) {
    this.__dfReq = {method: String(method || 'GET').toUpperCase(), url: String(url), headers: {}};
    return open.apply(this, arguments);
  };
  P.setRequestHeader = function (k, v) {
    if (this.__dfReq) this.__dfReq.headers[k] = v;
    return header.apply(this, arguments);
  };
  P.send = function (body) {
    var x = this, r = x.__dfReq;
    if (r) {
      try { r.url = new URL(r.url, location.href).href; } catch (e) {}
      if (typeof body === 'string') { r.body = body; }
      else if (body == null) { r.body = ''; }
      else if (typeof FormData !== 'undefined' && body instanceof FormData) {
        r.form = Array.from(body.entries()).filter(function (e) { return typeof e[1] === 'string'; });
      } else { r.body = null; }
      window.__df.pending++;
      var done = false;
      x.addEventListener('loadend', function () {
        if (done) return;
        done = true;
        window.__df.pending--;
        r.status = x.status;
        try { r.response = x.responseText; } catch (e) { r.response = null; }
        window.__df.log.push(r);
      });
    }
    return send.apply(this, arguments);
  };
}
return true;
`

const stateJS = `
return {
  hooked: !!window.__df,
  pending: window.__df ? window.__df.pending : 0,
  jq: (typeof jQuery === 'undefined') ? 0 : jQuery.active,
  ready: document.readyState
};
`

const takeLogJS = "var l = window.__df ? window.__df.log : []; if (window.__df) window.__df.log = []; return l;"

const snapshotJS = `
var out = [], seen = {};
document.querySelectorAll('select').forEach(function (s) {
  var key = s.id || s.name;
  if (!key || s.multiple || seen[key]) return;
  seen[key] = 1;
  var label = '';
  if (s.id) {
    var l = document.querySelector('label[for="' + s.id.replace(/"/g, '') + '"]');
    if (l) label = l.innerText;
  }
  if (!label) {
    var td = s.closest('td'), prev = td && td.previousElementSibling;
    if (prev) label = prev.innerText;
  }
  if (!label) {
    var p = s.previousElementSibling;
    while (p && !label) { label = (p.innerText || '').trim(); p = p.previousElementSibling; }
  }
  var r = s.getBoundingClientRect(), st = window.getComputedStyle(s);
  out.push({
    key: key,
    label: (label || '').replace(/\s+/g, ' ').trim().slice(0, 200),
    visible: r.width > 0 && st.display !== 'none' && st.visibility !== 'hidden',
    value: s.value,
    onchange: (s.getAttribute('onchange') || '').slice(0, 300),
    options: Array.from(s.options).map(function (o) { return [o.value, o.text]; })
  });
});
return {url: location.href, selects: out};
`

const selectJS = `
var s = document.getElementById(arguments[0]) || document.getElementsByName(arguments[0])[0];
if (!s) return false;
s.value = arguments[1];
s.dispatchEvent(new Event('change', {bubbles: true}));
return true;
`

// fetchJS runs a batch of lookups from inside the page (same session cookies,
// same origin), a few at a time so the portal isn't hammered.
const fetchJS = `
var reqs = arguments[0], done = arguments[arguments.length - 1], out = new Array(reqs.length), next = 0;
function one() {
  if (next >= reqs.length) return Promise.resolve();
  var i = next++, r = reqs[i];
  var init = {method: r.method, headers: r.headers || {}, credentials: 'same-origin'};
  if (r.method !== 'GET' && r.method !== 'HEAD') {
    if (r.form) { var fd = new FormData(); r.form.forEach(function (e) { fd.append(e[0], e[1]); }); init.body = fd; }
    else { init.body = r.body || ''; }
  }
  return fetch(r.url, init).then(function (resp) {
    return resp.text().then(function (t) { out[i] = {status: resp.status, text: t, url: resp.url}; });
  }).catch(function (e) { out[i] = {status: 0, text: '', error: String(e)}; }).then(one);
}
var workers = [];
for (var k = 0; k < Math.min(arguments[1], reqs.length); k++) workers.push(one());
Promise.all(workers).then(function () { done(out); });
`

// errScrapeSessionLost: a lookup answered with the login page or an error.
type errScrapeSessionLost struct{ msg string }

func (e *errScrapeSessionLost) Error() string { return e.msg }

// PageSpec is a portal page the scraper knows how to open.
type PageSpec struct {
	Key, Label string
	Open       func(*Scraper)
	NeedsToken bool
}

func openRegistration(s *Scraper) {
	b := s.bot
	b.navigateMenu(b.cred.NewDeedMenuIndex)
	b.present(webdriver.ByID("ddDistrict"), 30)
}

func openPresenter(s *Scraper) {
	b := s.bot
	s.ensureTokenOpen()
	link := b.present(webdriver.ByXPath("//a[contains(text(), 'Presentation')]"), defaultWait)
	b.jsClick(link)
	b.handleDialogs(1.5, 6)
	b.present(webdriver.ByID("presentCode"), defaultWait)
}

func tabOpener(t tab, prepare func(*Scraper)) func(*Scraper) {
	return func(s *Scraper) {
		s.ensureTokenOpen()
		s.bot.openTab(t)
		if prepare != nil {
			prepare(s)
		}
	}
}

func prepareProperty(s *Scraper) {
	// Must be answered before the district: its onchange otherwise stops with
	// a "Select Transfer of Revenue Registry" alert.
	if err := try(func() { s.bot.clickRadio("chkTrr", "0") }); err != nil && !isWD(err) {
		panic(err)
	}
}

// KnownPages are the pages with dedicated entries, in portal order.
var KnownPages = []PageSpec{
	{"registration", "New Token (registration)", openRegistration, false},
	{"presenter", "Presentation", openPresenter, true},
	{"prior_document", "PR Document", tabOpener(tabPriorDoc, nil), true},
	{"executant", "Executant", tabOpener(tabExecutant, nil), true},
	{"claimant", "Claimant", tabOpener(tabClaimant, nil), true},
	{"property", "Property", tabOpener(tabProperty, prepareProperty), true},
	{"document_link", "Document - Property Link", tabOpener(tabDocLink, nil), true},
	{"attesting_witness", "Attesting Witness", tabOpener(tabAttestWitness, nil), true},
	{"identifying_witness", "Identifying Witness", tabOpener(tabIdentWitness, nil), true},
}

var knownTabIDs = map[string]bool{
	"li_prevDoc": true, "li_executant": true, "li_claimant": true, "li_property": true, "li_doc_prop_link": true,
	"li_witness": true, "li_endorse_witness": true, "li_appln_det": true, "li_presentation": true,
}

// Scraper reads a portal's dropdowns through an Automation session.
type Scraper struct {
	bot         *Automation
	token       string
	maxOptions  int
	concurrency int
	say         func(string)
	tokenOpen   bool
	page        *PageSpec // currently loaded
}

// NewScraper wraps a logged-in-capable automation session. token is the
// existing token to read the token pages on ("" for the New Token page only).
func NewScraper(bot *Automation, token string, maxOptions int, say func(string)) *Scraper {
	if say == nil {
		say = func(msg string) { bot.log.Info(msg) }
	}
	return &Scraper{bot: bot, token: token, maxOptions: maxOptions, concurrency: 3, say: say}
}

// --- browser plumbing ----------------------------------------------------------

func (s *Scraper) js(script string, args ...any) any {
	for i := 0; i < 5; i++ {
		v, err := s.bot.d.ExecuteScript(s.bot.ctx, script, args...)
		if err == nil {
			return v
		}
		if !isUnexpAlert(err) {
			raise(err)
		}
		s.bot.handleDialogs(0.3, 6)
	}
	return s.bot.exec(script, args...)
}

func (s *Scraper) ensureTokenOpen() {
	if s.token == "" {
		panic(errorf("This page is only reachable inside a token - pass --token."))
	}
	if !s.tokenOpen || len(s.bot.findAll(webdriver.ByCSS("li[id^='li_']"))) == 0 {
		s.bot.OpenToken(s.token)
		s.bot.token = s.token
		s.tokenOpen = true
	}
}

func (s *Scraper) recoverSession() {
	s.tokenOpen = false
	s.bot.recoverSession("", "scraper")
}

func (s *Scraper) openPage(page *PageSpec) {
	var last error
	for attempt := 1; attempt <= 3; attempt++ {
		err := try(func() {
			s.bot.handleDialogs(0.3, 6)
			page.Open(s)
			s.bot.waitPageSettled(10)
			s.bot.handleDialogs(0.5, 6)
			s.js(hookJS)
			s.js(takeLogJS)
			s.page = page
		})
		if err == nil {
			return
		}
		var fe *fillError
		if !(IsSessionLost(err) || isPortalErr(err) || asErr(err, &fe) || isWD(err)) {
			panic(err)
		}
		last = err
		s.say(fmt.Sprintf("  could not open %s (attempt %d/3): %s", page.Label, attempt, short(err)))
		s.recoverSession()
	}
	panic(errorf("Could not open %s: %s", page.Label, short(last)))
}

// settle waits until the page's lookups have all answered. It returns false if
// the page navigated away/reloaded instead.
func (s *Scraper) settle(timeout float64) bool {
	end := time.Now().Add(secs(timeout))
	quiet := 0
	for time.Now().Before(end) {
		v, err := s.bot.d.ExecuteScript(s.bot.ctx, stateJS)
		if err != nil {
			if isUnexpAlert(err) {
				s.bot.handleDialogs(0.3, 6)
				quiet = 0
				continue
			}
			raise(err)
		}
		state, _ := v.(map[string]any)
		if hooked, _ := state["hooked"].(bool); !hooked {
			s.bot.waitPageSettled(10)
			s.js(hookJS)
			return false
		}
		pending, _ := state["pending"].(float64)
		jq, _ := state["jq"].(float64)
		ready, _ := state["ready"].(string)
		if pending <= 0 && jq == 0 && ready == "complete" {
			quiet++
			if quiet >= 2 {
				break
			}
		} else {
			quiet = 0
		}
		s.bot.sleep(0.25)
	}
	s.bot.handleDialogs(0.2, 6)
	return true
}

// selectInfo is one <select> of the page.
type selectInfo struct {
	Key, Label string
	Visible    bool
	Value      string
	OnChange   string
	Options    []Option
}

// snapshot is the page's selects in document order.
type snapshot struct {
	list []*selectInfo
	by   map[string]*selectInfo
}

func (sn *snapshot) get(key string) (*selectInfo, bool) { v, ok := sn.by[key]; return v, ok }

func (s *Scraper) snapshot() *snapshot {
	data, _ := s.js(snapshotJS).(map[string]any)
	sn := &snapshot{by: map[string]*selectInfo{}}
	selects, _ := data["selects"].([]any)
	for _, raw := range selects {
		m, _ := raw.(map[string]any)
		info := &selectInfo{}
		info.Key, _ = m["key"].(string)
		info.Label, _ = m["label"].(string)
		info.Visible, _ = m["visible"].(bool)
		info.Value, _ = m["value"].(string)
		info.OnChange, _ = m["onchange"].(string)
		opts, _ := m["options"].([]any)
		for _, o := range opts {
			pair, _ := o.([]any)
			if len(pair) >= 2 {
				v, _ := pair[0].(string)
				t, _ := pair[1].(string)
				info.Options = append(info.Options, Option{v, t})
			}
		}
		sn.list = append(sn.list, info)
		sn.by[info.Key] = info
	}
	return sn
}

// selectedText is the text of the select's chosen option.
func (si *selectInfo) selectedText() string {
	for _, o := range si.Options {
		if o[0] == si.Value {
			return o[1]
		}
	}
	return ""
}

// pick chooses value in dropdown field the way a person would (its change
// handlers run) and waits for the page to settle. It returns false if that
// reloaded the page.
func (s *Scraper) pick(field, value string) bool {
	if _, err := s.bot.d.ExecuteScript(s.bot.ctx, selectJS, field, value); err != nil {
		if !isUnexpAlert(err) {
			raise(err)
		}
		// The change handler raised a dialog; it ran - not run it again.
		s.bot.handleDialogs(0.3, 6)
	}
	return s.settle(40)
}

// request is one recorded AJAX call.
type request struct {
	Method, URL string
	Headers     map[string]string
	Body        *string // nil: unknown (a binary body)
	Form        [][2]string
	Status      int
	Response    string
}

func (s *Scraper) takeLog() []request {
	raw, _ := s.js(takeLogJS).([]any)
	var out []request
	for _, r := range raw {
		m, _ := r.(map[string]any)
		req := request{Headers: map[string]string{}}
		req.Method, _ = m["method"].(string)
		req.URL, _ = m["url"].(string)
		if h, ok := m["headers"].(map[string]any); ok {
			for k, v := range h {
				req.Headers[k], _ = v.(string)
			}
		}
		if b, ok := m["body"].(string); ok {
			req.Body = &b
		}
		if form, ok := m["form"].([]any); ok {
			for _, e := range form {
				pair, _ := e.([]any)
				if len(pair) == 2 {
					k, _ := pair[0].(string)
					v, _ := pair[1].(string)
					req.Form = append(req.Form, [2]string{k, v})
				}
			}
		}
		if st, ok := m["status"].(float64); ok {
			req.Status = int(st)
		}
		req.Response, _ = m["response"].(string)
		out = append(out, req)
	}
	return out
}

func (s *Scraper) loggedOut() bool { return s.bot.sessionLost() }

// --- lookups ---------------------------------------------------------------------------

type param struct{ loc, name, value string }

func queryPairs(rawQuery string) [][2]string {
	var out [][2]string
	for _, part := range strings.Split(rawQuery, "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		uk, err1 := url.QueryUnescape(k)
		uv, err2 := url.QueryUnescape(v)
		if err1 != nil {
			uk = k
		}
		if err2 != nil {
			uv = v
		}
		out = append(out, [2]string{uk, uv})
	}
	return out
}

func urlQuery(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return parsed.RawQuery
}

func urlPath(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return parsed.Path
}

// params is [(location, name, value)] for a recorded request.
func requestParams(req request) []param {
	var out []param
	for _, kv := range queryPairs(urlQuery(req.URL)) {
		out = append(out, param{"query", kv[0], kv[1]})
	}
	if len(req.Form) > 0 {
		for _, kv := range req.Form {
			out = append(out, param{"form", kv[0], kv[1]})
		}
	} else if req.Body != nil && *req.Body != "" {
		body := *req.Body
		if strings.HasPrefix(strings.TrimSpace(body), "{") {
			var obj map[string]any
			if json.Unmarshal([]byte(body), &obj) == nil {
				keys := make([]string, 0, len(obj))
				for k := range obj {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					switch obj[k].(type) {
					case map[string]any, []any:
					default:
						out = append(out, param{"json", k, pyStr(obj[k])})
					}
				}
			}
		} else {
			for _, kv := range queryPairs(body) {
				out = append(out, param{"body", kv[0], kv[1]})
			}
		}
	}
	return out
}

type paramMap struct{ Loc, Name, Field, By string }

type lookupTemplate struct {
	Method, URL string
	Body        *string
	Form        [][2]string
	Headers     map[string]string
	Extract     extraction
	Params      []paramMap
}

func (s *Scraper) buildTemplate(req request, child string, sn *snapshot, allowed map[string]bool) *lookupTemplate {
	if req.Body == nil && len(req.Form) == 0 {
		return nil
	}
	if unsafeRe.MatchString(urlPath(req.URL)) || req.Status >= 400 {
		return nil
	}
	childInfo, ok := sn.get(child)
	if !ok {
		return nil
	}
	spec := findExtraction(req.Response, childInfo.Options)
	if spec == nil {
		return nil
	}
	var mapping []paramMap
	for _, p := range requestParams(req) {
		if p.value == "" {
			continue
		}
		type match struct{ key, by string }
		var matches []match
		for _, sel := range sn.list {
			if !allowed[sel.Key] || sel.Value == "" {
				continue
			}
			text := sel.selectedText()
			switch {
			case sel.Value == p.value:
				matches = append(matches, match{sel.Key, "value"})
			case text != "" && deeds.Normalize(text) == deeds.Normalize(p.value):
				matches = append(matches, match{sel.Key, "text"})
			}
		}
		if len(matches) == 0 {
			continue
		}
		if len(matches) > 1 {
			// Two dropdowns showing the same value; replaying for other values
			// would be guesswork - verification decides.
			sort.SliceStable(matches, func(i, j int) bool { return matches[i].by == "value" && matches[j].by != "value" })
		}
		mapping = append(mapping, paramMap{p.loc, p.name, matches[0].key, matches[0].by})
	}
	if len(mapping) == 0 {
		return nil
	}
	return &lookupTemplate{Method: req.Method, URL: req.URL, Body: req.Body, Form: req.Form, Headers: req.Headers,
		Extract: *spec, Params: mapping}
}

// assignment is {field: (value, text)}.
type assignment map[string][2]string

type renderedRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Form    [][2]string       `json:"form,omitempty"`
	Body    *string           `json:"body,omitempty"`
}

// render is the request for template with each mapped parameter set from
// assign; unmapped ones keep what the page sent.
func render(t *lookupTemplate, assign assignment) renderedRequest {
	subs := map[[2]string]string{}
	for _, m := range t.Params {
		if vt, ok := assign[m.Field]; ok {
			if m.By == "value" {
				subs[[2]string{m.Loc, m.Name}] = vt[0]
			} else {
				subs[[2]string{m.Loc, m.Name}] = vt[1]
			}
		}
	}
	apply := func(loc string, pairs [][2]string) [][2]string {
		out := make([][2]string, len(pairs))
		for i, kv := range pairs {
			v := kv[1]
			if sub, ok := subs[[2]string{loc, kv[0]}]; ok {
				v = sub
			}
			out[i] = [2]string{kv[0], v}
		}
		return out
	}
	encode := func(pairs [][2]string) string {
		parts := make([]string, len(pairs))
		for i, kv := range pairs {
			parts[i] = url.QueryEscape(kv[0]) + "=" + url.QueryEscape(kv[1])
		}
		return strings.Join(parts, "&")
	}
	base := t.URL
	if i := strings.Index(base, "?"); i >= 0 {
		base = base[:i]
	}
	fragment := ""
	if i := strings.Index(t.URL, "#"); i >= 0 {
		fragment = t.URL[i:]
		base = strings.SplitN(base, "#", 2)[0]
	}
	query := encode(apply("query", queryPairs(urlQuery(t.URL))))
	full := base
	if query != "" {
		full += "?" + query
	}
	full += fragment
	req := renderedRequest{Method: t.Method, URL: full, Headers: t.Headers}
	switch {
	case len(t.Form) > 0:
		req.Form = apply("form", t.Form)
	case t.Body != nil && *t.Body != "":
		body := *t.Body
		if strings.HasPrefix(strings.TrimSpace(body), "{") {
			var obj map[string]any
			if json.Unmarshal([]byte(body), &obj) == nil {
				for k := range obj {
					if sub, ok := subs[[2]string{"json", k}]; ok {
						obj[k] = sub
					}
				}
				enc, _ := json.Marshal(obj)
				text := string(enc)
				req.Body = &text
			}
		} else {
			text := encode(apply("body", queryPairs(body)))
			req.Body = &text
		}
	default:
		empty := ""
		req.Body = &empty
	}
	return req
}

type fetched struct {
	Status int
	Text   string
}

func (s *Scraper) fetchMany(reqs []renderedRequest) []fetched {
	if len(reqs) == 0 {
		return nil
	}
	must(s.bot.d.SetScriptTimeout(s.bot.ctx, 300*time.Second))
	args := make([]any, len(reqs))
	for i, r := range reqs {
		raw, _ := json.Marshal(r)
		var generic map[string]any
		_ = json.Unmarshal(raw, &generic)
		args[i] = generic
	}
	var out any
	for attempt := 0; ; attempt++ {
		v, err := s.bot.d.ExecuteAsyncScript(s.bot.ctx, fetchJS, args, s.concurrency)
		if err == nil {
			out = v
			break
		}
		if !isUnexpAlert(err) || attempt >= 3 {
			raise(err)
		}
		s.bot.handleDialogs(0.3, 6)
	}
	results, _ := out.([]any)
	res := make([]fetched, len(results))
	for i, r := range results {
		m, _ := r.(map[string]any)
		st, _ := m["status"].(float64)
		res[i].Status = int(st)
		res[i].Text, _ = m["text"].(string)
	}
	return res
}

// runLookups is the options for each assignment via the learned lookup. It
// raises errScrapeSessionLost if the portal answered with its login page.
func (s *Scraper) runLookups(t *lookupTemplate, assigns []assignment) [][]Option {
	reqs := make([]renderedRequest, len(assigns))
	for i, a := range assigns {
		reqs[i] = render(t, a)
	}
	var out [][]Option
	for _, res := range s.fetchMany(reqs) {
		opts := extractOptions(res.Text, t.Extract)
		if opts == nil {
			lost := res.Status == 0 || res.Status == 401 || res.Status == 403 || res.Status >= 500
			for _, m := range loginMarkers {
				if strings.Contains(res.Text, m) {
					lost = true
				}
			}
			if lost {
				panic(&errScrapeSessionLost{fmt.Sprintf("lookup answered %d", res.Status)})
			}
		}
		out = append(out, realOptions(opts))
	}
	return out
}

// --- one page ----------------------------------------------------------------------------

func samples(options []Option, current string) []Option {
	real := realOptions(options)
	var opts []Option
	for _, o := range real {
		if o[0] != current {
			opts = append(opts, o)
		}
	}
	if len(opts) == 0 {
		opts = real
	}
	if len(opts) <= 3 {
		return opts
	}
	return []Option{opts[0], opts[len(opts)/2], opts[len(opts)-1]}
}

// depMap is a defaultdict(list) that remembers key insertion order.
type depMap struct {
	order []string
	m     map[string][]string
}

func newDepMap() *depMap { return &depMap{m: map[string][]string{}} }

// get reads without inserting (dict.get).
func (d *depMap) get(k string) []string { return d.m[k] }

// at reads like parents[k]: it inserts an empty list for a missing key.
func (d *depMap) at(k string) []string {
	if _, ok := d.m[k]; !ok {
		d.m[k] = nil
		d.order = append(d.order, k)
	}
	return d.m[k]
}

func (d *depMap) add(k, v string) {
	d.at(k)
	d.m[k] = append(d.m[k], v)
}

func (d *depMap) has(k string) bool { _, ok := d.m[k]; return ok }

func containsStr(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

type observation struct {
	assign  assignment
	options []Option
}

type evidenceItem struct {
	req request
	sn  *snapshot
}

type discovery struct {
	order       []string
	labels      map[string]string
	parents     *depMap
	templates   map[string]*lookupTemplate
	navigates   map[string]bool
	rootOptions map[string][]Option
}

// discover finds which dropdowns cascade from which, and learns the lookup
// behind each child dropdown.
//
// A dropdown is another's child when picking the other changes its options,
// or when the lookup that picking fired (carrying the picked value) answered
// with exactly its options. Repeated until a pass finds nothing new: a
// dropdown fed by two others (local body name, from local-body type and
// village) only reacts to the first once the second has a value.
func (s *Scraper) discover(page *PageSpec) *discovery {
	initial := s.snapshot()
	var order []string
	labels := map[string]string{}
	rootOptions := map[string][]Option{}
	for _, sel := range initial.list {
		order = append(order, sel.Key)
		labels[sel.Key] = sel.Label
		rootOptions[sel.Key] = realOptions(sel.Options)
	}
	parents := newDepMap()
	observations := map[string][]observation{}
	evidence := map[string][]evidenceItem{}
	navigates := map[string]bool{}

	var ancestorsOf func(f string, seen map[string]bool) map[string]bool
	ancestorsOf = func(f string, seen map[string]bool) map[string]bool {
		if seen == nil {
			seen = map[string]bool{}
		}
		for _, p := range parents.get(f) {
			if !seen[p] {
				seen[p] = true
				ancestorsOf(p, seen)
			}
		}
		return seen
	}

	for passNo := 1; passNo <= 3; passNo++ {
		tested := map[string]bool{}
		newEdges := false
		for {
			snap := s.snapshot()
			for _, sel := range snap.list {
				if !containsStr(order, sel.Key) {
					order = append(order, sel.Key)
					if _, ok := labels[sel.Key]; !ok {
						labels[sel.Key] = sel.Label
					}
					if _, ok := rootOptions[sel.Key]; !ok {
						rootOptions[sel.Key] = realOptions(sel.Options)
					}
				}
			}
			field := ""
			for _, k := range order {
				sel, ok := snap.get(k)
				if ok && !tested[k] && !navigates[k] && len(realOptions(sel.Options)) > 0 && !unsafeRe.MatchString(sel.OnChange) {
					field = k
					break
				}
			}
			if field == "" {
				break
			}
			tested[field] = true
			fieldInfo, _ := snap.get(field)
			samp := samples(fieldInfo.Options, fieldInfo.Value)
			if passNo > 1 && len(samp) > 0 {
				samp = samp[len(samp)-1:]
			}
			var best, last *string
			bestRichness := -1
			for _, vt := range samp {
				value, text := vt[0], vt[1]
				before := s.snapshot()
				s.takeLog()
				if !s.pick(field, value) {
					s.say(fmt.Sprintf("  '%s' reloads the page when changed - its options are kept, not walked", field))
					navigates[field] = true
					s.openPage(page)
					best, last = nil, nil
					break
				}
				v := value
				last = &v
				after := s.snapshot()
				log := s.takeLog()
				assign := assignment{}
				for _, sel := range after.list {
					if sel.Value != "" {
						assign[sel.Key] = [2]string{sel.Value, sel.selectedText()}
					}
				}
				picked := map[string]bool{value: true, deeds.Normalize(text): true}
				var fired []request
				for _, r := range log {
					for _, p := range requestParams(r) {
						if p.value != "" && (p.value == value || picked[deeds.Normalize(p.value)]) {
							fired = append(fired, r)
							break
						}
					}
				}
				richness := 0
				for _, child := range after.list {
					if child.Key == field {
						continue
					}
					now := realOptions(child.Options)
					if len(now) == 0 {
						continue
					}
					beforeChild, had := before.get(child.Key)
					changed := !had || !sameOptionSet(now, realOptions(beforeChild.Options))
					var answered []request
					for _, r := range fired {
						if findExtraction(r.Response, child.Options) != nil {
							answered = append(answered, r)
						}
					}
					if !changed && len(answered) == 0 && !containsStr(parents.get(child.Key), field) {
						continue
					}
					if !containsStr(parents.at(child.Key), field) && field != child.Key && !ancestorsOf(field, nil)[child.Key] {
						parents.add(child.Key, field)
						newEdges = true
					}
					richness += len(now)
					observations[child.Key] = append(observations[child.Key], observation{assign, now})
					use := answered
					if len(use) == 0 {
						use = log
					}
					for _, r := range use {
						evidence[child.Key] = append(evidence[child.Key], evidenceItem{r, after})
					}
				}
				if richness > bestRichness {
					b := value
					best, bestRichness = &b, richness
				}
			}
			if best != nil && (last == nil || *best != *last) {
				// Leave the choice that filled the most dropdowns below it, so
				// they can be tried in turn.
				s.pick(field, *best)
			}
		}
		if !newEdges {
			break
		}
	}

	// Keep only direct parents: drop one that's itself an ancestor of another
	// parent of the same child (picking a district also resets the taluk list,
	// but the taluk list comes from the SRO).
	direct := newDepMap()
	for _, child := range parents.order {
		ps := parents.m[child]
		keep := []string{}
		for _, p := range ps {
			drop := false
			for _, q := range ps {
				if q != p && ancestorsOf(q, nil)[p] {
					drop = true
				}
			}
			if !drop {
				keep = append(keep, p)
			}
		}
		direct.at(child)
		direct.m[child] = keep
	}
	parents = direct

	templates := map[string]*lookupTemplate{}
	for _, child := range parents.order {
		allowed := ancestorsOf(child, nil)
		tried := map[string]bool{}
		for _, ev := range evidence[child] {
			t := s.buildTemplate(ev.req, child, ev.sn, allowed)
			if t == nil {
				continue
			}
			sig := toJSON([]any{strings.SplitN(t.URL, "?", 2)[0], t.Params})
			if tried[sig] {
				continue
			}
			tried[sig] = true
			if s.verify(t, observations[child]) {
				templates[child] = t
				break
			}
		}
	}
	return &discovery{order: order, labels: labels, parents: parents, templates: templates, navigates: navigates, rootOptions: rootOptions}
}

func (s *Scraper) verify(t *lookupTemplate, obs []observation) bool {
	if len(obs) == 0 {
		return false
	}
	sample := append([]observation(nil), obs[:min(2, len(obs))]...)
	if len(obs) > 2 {
		rest := obs[2:]
		if len(rest) > 3 {
			rest = rest[len(rest)-3:]
		}
		sample = append(sample, rest...)
	}
	assigns := make([]assignment, len(sample))
	for i, o := range sample {
		assigns[i] = o.assign
	}
	var got [][]Option
	err := try(func() { got = s.runLookups(t, assigns) })
	if err != nil {
		var lost *errScrapeSessionLost
		if asErr(err, &lost) || isWD(err) {
			return false
		}
		panic(err)
	}
	for i, o := range sample {
		if i >= len(got) || !sameOptionSet(got[i], o.options) {
			return false
		}
	}
	return true
}

package portal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/shahinshabab/deedflow-go/internal/webdriver"
)

// The probes are read-only looks at the live portal, for diagnosing selector
// and staleness problems: they log in, open a token via My Token and print
// what the automation relies on. They never click Save, Update or Delete.

var allTabs = []tab{tabPriorDoc, tabExecutant, tabClaimant, tabProperty, tabDocLink, tabAttestWitness, tabIdentWitness}

func tabByLiID(li string) (tab, bool) {
	for _, t := range allTabs {
		if t.liID == li {
			return t, true
		}
	}
	return tab{}, false
}

func pretty(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(v); err != nil {
		return fmt.Sprint(v)
	}
	return strings.TrimRight(buf.String(), "\n")
}

func (a *Automation) saveShot(path string, out func(string)) {
	png, err := a.d.Screenshot(a.ctx)
	if err != nil {
		out(fmt.Sprintf("(could not take %s: %v)", path, err))
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
		err = os.WriteFile(path, png, 0o644)
	}
	if err != nil {
		out(fmt.Sprintf("(could not save %s: %v)", path, err))
	}
}

const probeInfoJS = `
var q = function (s) { return document.querySelectorAll(s).length; };
var dt = document.getElementById(arguments[0]);
return {
  url: location.href,
  iframes: q('iframe') + q('frame'),
  lnkLogout: q('#lnkLogout'),
  userHeader: document.evaluate("//span[contains(., 'User')]/font/b", document, null,
                                XPathResult.ORDERED_NODE_SNAPSHOT_TYPE, null).snapshotLength,
  logoutLinks: Array.from(document.querySelectorAll('a')).filter(function (a) {
    return /logout|log out|sign out/i.test(a.innerText + ' ' + a.href); }).map(function (a) {
    return a.outerHTML.slice(0, 200); }),
  tabs: Array.from(document.querySelectorAll("li[id^='li_']")).map(function (l) { return l.id; }),
  docNo: (document.getElementById('docNo') || {}).value,
  jquery: typeof jQuery !== 'undefined',
  readyEl: !!dt,
  readyOnchange: dt ? (dt.getAttribute('onchange') || '') : '',
  bodyTop: document.body.innerText.slice(0, 600),
};
`

// ProbePortalPage is a read-only look at how one wizard tab of an existing
// portal token is built.
func (a *Automation) ProbePortalPage(token, tabLiID, shotsDir string, out func(string)) (err error) {
	return try(func() {
		target, ok := tabByLiID(tabLiID)
		if !ok {
			panic(errorf("unknown tab %q", tabLiID))
		}
		a.EnsureLoggedIn(10)
		out(fmt.Sprintf("home: %s", pretty(a.exec(probeInfoJS, "x"))))
		a.OpenToken(token)
		a.openTab(target)
		out(fmt.Sprintf("\n%s after open: %s", tabLiID, pretty(a.exec(probeInfoJS, target.readyID))))
		out(fmt.Sprintf("logged_in_quick() says: %v", a.loggedInQuick()))
		out(fmt.Sprintf("saved rows: %s", pretty(a.savedRows())))
		out("grid links: " + pretty(a.exec("return Array.from(document.querySelectorAll('a')).filter(function (a) {"+
			" return /edit|delete|showDetails/i.test(a.innerText + ' ' + (a.getAttribute('href') || '') + ' ' +"+
			" (a.getAttribute('onclick') || '')); }).map(function (a) {"+
			" return [a.innerText.trim(), a.getAttribute('href'), a.getAttribute('onclick')]; });")))
		for _, fn := range []string{"showDetailsDel", "showDetails", "deleteData", "deleteLink", "insertData", "updateData"} {
			if src, _ := a.exec(fmt.Sprintf("return typeof %s === 'function' ? %s.toString() : null;", fn, fn)).(string); src != "" {
				out(fmt.Sprintf("\nfunction %s:\n%s", fn, truncate(src, 3000)))
			}
		}
		grid, _ := a.exec("var g = document.getElementById('divgrid'); return g ? g.outerHTML.slice(0, 6000) : null;").(string)
		out("\ngrid html: " + grid)
		form, _ := a.exec("var e = document.getElementById(arguments[0]); var f = e && (e.closest('form') || e.parentNode);"+
			"return f ? f.outerHTML : null;", target.readyID).(string)
		out(fmt.Sprintf("\nform html (%d chars):\n%s", len([]rune(form)), truncate(form, 12000)))
		a.saveShot(filepath.Join(shotsDir, "probe_before.png"), out)
		if tabLiID != "li_prevDoc" {
			return
		}
		// The type change is what a real fill does first; see whether it
		// rebuilds the rest of the form (-> stale elements).
		ids := []string{"district", "sro", "pyear", "pbookno", "pattayamNo", "volume", "pageFrom", "pageTo", "remarks"}
		before := map[string][]*webdriver.Element{}
		for _, id := range ids {
			before[id] = a.findAll(webdriver.ByID(id))
		}
		must(webdriver.Select{El: a.find(webdriver.ByID("documentType"))}.SelectByVisibleText(a.ctx, "രജിസ്റ്റർ/ഫയൽ ചെയ്ത"))
		a.sleep(4)
		dialogs := a.handleDialogs(1, 6)
		for _, id := range ids {
			state := "absent before"
			if els := before[id]; len(els) > 0 {
				if _, err := els[0].TagName(a.ctx); err != nil {
					state = fmt.Sprintf("REPLACED (%s)", exceptionNameOf(err))
				} else {
					state = "same element"
				}
			}
			now := a.findAll(webdriver.ByID(id))
			shown := "None"
			if len(now) > 0 {
				shown = fmt.Sprint(a.displayed(now[0]))
			}
			out(fmt.Sprintf("  %s: %s; now present=%v displayed=%s", id, state, len(now) > 0, shown))
		}
		out(fmt.Sprintf("dialogs after type change: %s", pyList(dialogs)))
		out(fmt.Sprintf("after type change: %s", pretty(a.exec(probeInfoJS, target.readyID))))
		a.saveShot(filepath.Join(shotsDir, "probe_after.png"), out)
	})
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

const fairValuePattern = `fair|cost|docamt|trns_|hecter|sqmtr|market|minimum|less than`

const probeFuncsJS = `
var out = {};
var re = new RegExp(arguments[0], 'i');
for (var k in window) {
  try {
    if (typeof window[k] === 'function') {
      var s = window[k].toString();
      if (s.indexOf('[native code]') < 0 && re.test(s)) { out[k] = s; }
    }
  } catch (e) {}
}
return out;
`

const probeScriptsJS = `
return Array.from(document.scripts).map(function (s) {
  return {src: s.src || '', text: s.src ? '' : s.text};
});
`

// ProbeFairValue prints the portal's own JavaScript behind the Property tab's
// fair-value / price / area checks, so DeedFlow's document-value rule can
// match the portal's exactly.
func (a *Automation) ProbeFairValue(token string, out func(string)) error {
	return try(func() {
		re := regexp.MustCompile("(?i)" + fairValuePattern)
		a.EnsureLoggedIn(10)
		a.OpenToken(token)
		a.openTab(tabProperty)
		out("url: " + mustv(a.d.CurrentURL(a.ctx)))
		funcs, _ := a.exec(probeFuncsJS, fairValuePattern).(map[string]any)
		names := make([]string, 0, len(funcs))
		for k := range funcs {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, name := range names {
			src, _ := funcs[name].(string)
			out(fmt.Sprintf("\n===== function %s =====\n%s", name, src))
		}
		scripts, _ := a.exec(probeScriptsJS).([]any)
		for _, raw := range scripts {
			sc, _ := raw.(map[string]any)
			src, _ := sc["src"].(string)
			text, _ := sc["text"].(string)
			if src != "" {
				out(fmt.Sprintf("\n===== external script %s =====", src))
				var fetched any
				fetchErr := try(func() {
					fetched = a.exec("var x = new XMLHttpRequest(); x.open('GET', arguments[0], false); x.send();"+
						"return x.responseText;", src)
				})
				if fetchErr != nil {
					if !isWD(fetchErr) {
						panic(fetchErr)
					}
					out(fmt.Sprintf("(could not fetch: %v)", fetchErr))
					continue
				}
				text, _ = fetched.(string)
			}
			if re.MatchString(text) {
				label := src
				if label == "" {
					label = "inline"
				}
				out(fmt.Sprintf("\n===== script (%s) =====\n%s", label, text))
			}
		}
		for _, fid := range []string{"fairvaluesel", "cost", "trns_hecter", "trns_are", "trns_sqmtr", "fairvalueamt"} {
			html, _ := a.exec("var e = document.getElementById(arguments[0]) ||"+
				" document.querySelector('[name=\"' + arguments[0] + '\"]');"+
				"return e ? (e.parentNode || e).outerHTML.slice(0, 2000) : null;", fid).(string)
			if html == "" {
				html = "None"
			}
			out(fmt.Sprintf("\n===== %s =====\n%s", fid, html))
		}
	})
}

const probeLinksJS = `
var root = arguments[0] || document;
return Array.from(root.querySelectorAll('a, img[onclick], input[type=button], input[type=submit], button, span[onclick], td[onclick]'))
  .map(function (a) {
    return {tag: a.tagName, text: (a.innerText || a.value || a.alt || a.title || '').trim().slice(0, 60),
            href: a.getAttribute('href'), onclick: a.getAttribute('onclick'), id: a.id, name: a.getAttribute('name'),
            src: a.getAttribute('src'), visible: !!(a.offsetWidth || a.offsetHeight)};
  });
`

const probeRowsTableJS = `
var a = document.querySelector("a[href*='showDetailsDel']");
if (!a) return null;
var t = a.closest('table');
return t ? t.outerHTML.slice(0, 8000) : null;
`

const probeRowActionsJS = `
var seen = [];
return Array.from(document.querySelectorAll("a[href*='showDetailsDel']")).map(function (a) {
  var tr = a.closest('tr');
  if (!tr || seen.indexOf(tr) >= 0) return null;
  seen.push(tr);
  return {text: tr.innerText.slice(0, 300),
          cells: Array.from(tr.querySelectorAll('td')).map(function (td) { return td.innerText.trim().slice(0, 80); }),
          actions: Array.from(tr.querySelectorAll('a, img[onclick], input, span[onclick]')).map(function (x) {
            return {tag: x.tagName, text: (x.innerText || x.value || x.alt || x.title || '').trim(),
                    href: x.getAttribute('href'), onclick: x.getAttribute('onclick'),
                    html: x.outerHTML.slice(0, 300)};
          })};
}).filter(function (r) { return r; });
`

const probeFormJS = `
return Array.from(document.querySelectorAll('input, select, textarea')).map(function (e) {
  var o = {tag: e.tagName, type: e.type, id: e.id, name: e.getAttribute('name'),
           visible: !!(e.offsetWidth || e.offsetHeight), disabled: e.disabled, readonly: e.readOnly};
  if (e.tagName === 'SELECT') {
    var s = e.options[e.selectedIndex];
    o.value = e.value; o.text = s ? s.text.trim() : null; o.n_options = e.options.length;
  } else if (e.type === 'checkbox' || e.type === 'radio') {
    o.value = e.value; o.checked = e.checked;
  } else {
    o.value = (e.value || '').slice(0, 80);
  }
  o.onclick = e.getAttribute('onclick'); o.onchange = e.getAttribute('onchange');
  return o;
});
`

const probeFnSrcJS = "return typeof window[arguments[0]] === 'function' ? window[arguments[0]].toString().slice(0, 5000) : null;"

var fnNameRe = regexp.MustCompile(`([A-Za-z_]\w*)\s*\(`)

func fnNames(texts ...string) map[string]bool {
	names := map[string]bool{}
	for _, text := range texts {
		for _, m := range fnNameRe.FindAllStringSubmatch(text, -1) {
			switch m[1] {
			case "javascript", "return", "if", "function", "confirm", "alert":
			default:
				names[m[1]] = true
			}
		}
	}
	return names
}

// ProbeRowActions is a read-only look at how a token's saved rows can be
// edited on the portal: it reads the My Token page and the token's details
// panel, then on each repeatable tab dumps the saved-rows table, the row
// action links, and what the first row's non-delete action opens.
func (a *Automation) ProbeRowActions(token string, tabIDs []string, shotsDir string, out func(string)) error {
	section := func(title string, value any) {
		text, ok := value.(string)
		if !ok {
			text = pretty(value)
		}
		out(fmt.Sprintf("\n===== %s =====\n%s", title, text))
	}
	sources := func(names map[string]bool) {
		keys := make([]string, 0, len(names))
		for k := range names {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, name := range keys {
			if src, _ := a.exec(probeFnSrcJS, name).(string); src != "" {
				section("function "+name, src)
			}
		}
	}
	linkTexts := func(links any) []string {
		var texts []string
		list, _ := links.([]any)
		for _, l := range list {
			m, _ := l.(map[string]any)
			texts = append(texts, fmt.Sprint(m["onclick"])+fmt.Sprint(m["href"]))
		}
		return texts
	}
	return try(func() {
		a.EnsureLoggedIn(10)
		a.gotoMyToken()
		section("my token url", mustv(a.d.CurrentURL(a.ctx)))
		links := a.exec(probeLinksJS, nil)
		section("my token links/inputs", links)
		section("my token form fields", a.exec(probeFormJS))
		section("my token body (first 3000)", a.exec("return document.body.innerText.slice(0, 3000);"))
		section("my token html (first 10000)", a.exec("return document.body.innerHTML.slice(0, 10000);"))
		section("tokens on first page", a.listMyTokens())
		names := fnNames("showDetails", "searchToken", "search", "getDetails", "tokenSearch")
		for k := range fnNames(linkTexts(links)...) {
			names[k] = true
		}
		names["showDetails"], names["searchToken"], names["search"], names["getDetails"], names["tokenSearch"] = true, true, true, true, true
		sources(names)
		a.saveShot(filepath.Join(shotsDir, "probe_mytoken.png"), out)

		a.exec("showDetails(arguments[0]);", token)
		a.sleep(5)
		a.handleDialogs(1, 6)
		section("after showDetails: url", mustv(a.d.CurrentURL(a.ctx)))
		section("after showDetails: body (first 4000)", a.exec("return document.body.innerText.slice(0, 4000);"))
		var visible []any
		all, _ := a.exec(probeLinksJS, nil).([]any)
		for _, l := range all {
			if m, _ := l.(map[string]any); m["visible"] == true {
				visible = append(visible, l)
			}
		}
		section("after showDetails: links", visible)
		a.saveShot(filepath.Join(shotsDir, "probe_details.png"), out)
		// And a token that doesn't exist, to see how "not found" looks.
		a.gotoMyToken()
		a.exec("showDetails(arguments[0]);", "T00000001")
		a.sleep(5)
		section("bogus token dialogs", pyList(a.handleDialogs(2, 6)))
		section("bogus token body (first 2000)", a.exec("return document.body.innerText.slice(0, 2000);"))
		a.saveShot(filepath.Join(shotsDir, "probe_bogus.png"), out)

		a.OpenToken(token)
		for _, li := range tabIDs {
			t, ok := tabByLiID(li)
			if !ok {
				section(li+": could not open", "unknown tab")
				continue
			}
			if err := try(func() { a.openTab(t) }); err != nil {
				if !isRaisedBroad(err) {
					panic(err)
				}
				section(li+": could not open", err.Error())
				continue
			}
			rows, _ := a.exec(probeRowActionsJS).([]any)
			section(fmt.Sprintf("%s: saved rows (%d)", li, len(rows)), rows)
			section(li+": rows table html", a.exec(probeRowsTableJS))
			var buttons []any
			btnAll, _ := a.exec(probeLinksJS, nil).([]any)
			for _, l := range btnAll {
				if m, _ := l.(map[string]any); m["tag"] == "INPUT" || m["tag"] == "BUTTON" {
					buttons = append(buttons, l)
				}
			}
			section(li+": page buttons", buttons)
			rowNames := map[string]bool{}
			for _, r := range rows {
				m, _ := r.(map[string]any)
				acts, _ := m["actions"].([]any)
				for _, act := range acts {
					am, _ := act.(map[string]any)
					for k := range fnNames(fmt.Sprint(am["href"]), fmt.Sprint(am["onclick"])) {
						rowNames[k] = true
					}
				}
			}
			sources(rowNames)
			a.saveShot(filepath.Join(shotsDir, fmt.Sprintf("probe_%s_table.png", li)), out)
			if len(rows) == 0 {
				continue
			}
			// Open the first row's non-delete action (read-only: it only loads a form).
			first, _ := rows[0].(map[string]any)
			acts, _ := first["actions"].([]any)
			var edit map[string]any
			for _, act := range acts {
				am, _ := act.(map[string]any)
				href, _ := am["href"].(string)
				onclick, _ := am["onclick"].(string)
				if !strings.Contains(href+onclick, "showDetailsDel") && (strings.HasPrefix(href, "javascript") || onclick != "") {
					edit = am
					break
				}
			}
			section(li+": chosen edit action", edit)
			if edit == nil {
				continue
			}
			code := strings.Replace(fmt.Sprint(edit["href"]), "javascript:", "", 1)
			if edit["href"] == nil || code == "" {
				code, _ = edit["onclick"].(string)
			}
			old := a.find(webdriver.ByTag("html"))
			if err := try(func() { a.execIgnoreAlert(code) }); err != nil {
				if !isRaisedBroad(err) {
					panic(err)
				}
				section(li+": running the action raised", err.Error())
			}
			reloaded := a.untilQuiet(15, func() bool { return old.IsStale(a.ctx) })
			a.sleep(3)
			section(fmt.Sprintf("%s: after edit action: reloaded=%v url", li, reloaded), mustv(a.d.CurrentURL(a.ctx)))
			section(li+": dialogs", pyList(a.handleDialogs(2, 6)))
			section(li+": edit form fields", a.exec(probeFormJS))
			var editButtons []any
			eb, _ := a.exec(probeLinksJS, nil).([]any)
			var onclicks []string
			for _, l := range eb {
				if m, _ := l.(map[string]any); m["tag"] == "INPUT" || m["tag"] == "BUTTON" {
					editButtons = append(editButtons, l)
					onclicks = append(onclicks, fmt.Sprint(m["onclick"]))
				}
			}
			section(li+": edit page buttons", editButtons)
			names := fnNames(onclicks...)
			names["updateData"], names["editData"] = true, true
			sources(names)
			section(li+": edit page body (first 2500)", a.exec("return document.body.innerText.slice(0, 2500);"))
			a.saveShot(filepath.Join(shotsDir, fmt.Sprintf("probe_%s_edit.png", li)), out)
			// Back to a clean wizard for the next tab.
			a.OpenToken(token)
		}
	})
}

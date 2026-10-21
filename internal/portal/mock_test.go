package portal

import (
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
)

// mockPortal is a tiny stand-in for the Registration Department portal, with
// just the pages and widgets the automation touches (selectors taken from
// selenium_automation.py): a captcha login, the My Token list, a token's
// wizard tabs and the witness tab's save / edit / delete forms.
type mockPortal struct {
	mu       sync.Mutex
	loggedIn bool
	rows     map[string]map[string]string // id -> fields
	nextID   int
	saves    int
	srv      *httptest.Server
}

func newMockPortal() *mockPortal {
	m := &mockPortal{rows: map[string]map[string]string{}, nextID: 1}
	mux := http.NewServeMux()
	mux.HandleFunc("/index.php", m.index)
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.loggedIn = true
		m.mu.Unlock()
		http.Redirect(w, r, "/index.php", http.StatusFound)
	})
	mux.HandleFunc("/tokensearch.php", m.tokenSearch)
	mux.HandleFunc("/tokendetail", m.tokenDetail)
	mux.HandleFunc("/wizard", m.wizard)
	mux.HandleFunc("/save", m.save)
	mux.HandleFunc("/delete", m.deletePage)
	mux.HandleFunc("/dodelete", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		delete(m.rows, r.URL.Query().Get("id"))
		m.mu.Unlock()
		_, _ = w.Write([]byte("ok"))
	})
	m.srv = httptest.NewServer(mux)
	return m
}

func (m *mockPortal) logout() {
	m.mu.Lock()
	m.loggedIn = false
	m.mu.Unlock()
}

func (m *mockPortal) rowNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, r := range m.rows {
		out = append(out, r["wname"])
	}
	sort.Strings(out)
	return out
}

const header = `<!doctype html><html><head><meta charset="utf-8"><title>portal</title></head><body>
<span>User : <font><b>demo</b></font></span> <a id="lnkLogout" href="/logout">Logout</a>
<div id="menu11"><span class="menulink">പ്രധാന പേജ്</span></div>
<ul id="menu"><li>a</li><li>b</li><li>c</li><li>d</li><li><a href="#">Tokens</a>
<ul><li><a href="/index.php?new=1">New token</a></li><li><a href="/tokensearch.php">My token</a></li></ul></li></ul>`

func (m *mockPortal) loggedInNow() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loggedIn
}

func (m *mockPortal) index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if m.loggedInNow() {
		fmt.Fprint(w, header+`<h1>Home</h1></body></html>`)
		return
	}
	fmt.Fprint(w, `<!doctype html><html><body><div id="content"><div><span>
<div><div>x</div><div><img id="loginicon" width="20" height="20" onclick="document.getElementById('loginForm').style.display='block'"></div></div></span></div></div>
<form id="loginForm" style="display:none" onsubmit="return false">
<input id="user_name"><input id="passwd" type="password">
<img id="captchaImage" src="data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==">
<input id="imgtext">
<input type="button" name="Login" value="Login" onclick="if (document.getElementById('imgtext').value === '1234' && document.getElementById('user_name').value === 'demo' && document.getElementById('passwd').value === 'secret') { location = '/login'; } else { alert('Invalid captcha or password'); }">
</form></body></html>`)
}

func (m *mockPortal) tokenSearch(w http.ResponseWriter, r *http.Request) {
	if !m.loggedInNow() {
		http.Redirect(w, r, "/index.php", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, header+`<script>function showDetails(t) { location = '/tokendetail?t=' + t; }</script>
<table><tr><th>Token</th><th>Name</th></tr>
<tr><td>T123456</td><td>Test Presenter</td><td>01/01/2026</td><td><a href="javascript:showDetails('T123456')">View</a></td></tr>
<tr><td>T123457</td><td>Sent On</td><td>01/01/2026</td><td>View Details</td></tr>
</table></body></html>`)
}

func (m *mockPortal) tokenDetail(w http.ResponseWriter, r *http.Request) {
	if !m.loggedInNow() {
		http.Redirect(w, r, "/index.php", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, header+`<a href="/wizard?tab=prevDoc">Enter Document Details</a></body></html>`)
}

const tabBar = `<ul>
<li id="li_prevDoc"><a href="/wizard?tab=prevDoc">Prior</a></li>
<li id="li_executant"><a href="/wizard?tab=executant">Executant</a></li>
<li id="li_claimant"><a href="/wizard?tab=claimant">Claimant</a></li>
<li id="li_property"><a href="/wizard?tab=property">Property</a></li>
<li id="li_doc_prop_link"><a href="/wizard?tab=link">Link</a></li>
<li id="li_witness"><a href="/wizard?tab=witness">Witness</a></li>
<li id="li_endorse_witness"><a href="/wizard?tab=endorse">Identifying</a></li>
</ul>`

var witnessFields = []string{"wname", "rname", "houseName", "desam", "place", "profession"}

func (m *mockPortal) wizard(w http.ResponseWriter, r *http.Request) {
	if !m.loggedInNow() {
		http.Redirect(w, r, "/index.php", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tab := r.URL.Query().Get("tab")
	if tab != "witness" {
		fmt.Fprint(w, header+tabBar+`<input id="documentType"></body></html>`)
		return
	}
	edit := r.URL.Query().Get("edit")
	m.mu.Lock()
	defer m.mu.Unlock()
	values := map[string]string{}
	if edit != "" {
		values = m.rows[edit]
	}
	var b strings.Builder
	b.WriteString(header + tabBar)
	b.WriteString(`<form onsubmit="return false">`)
	for _, f := range witnessFields {
		fmt.Fprintf(&b, `<input id="%s" name="%s" value="%s">`, f, f, html.EscapeString(values[f]))
	}
	b.WriteString(`<select id="selSex"><option value="">--</option><option>Male</option><option>Female</option></select>
<select id="ddRtype"><option value="">--</option><option>Father of</option><option>Husband of</option><option>Mother of</option></select>
<select id="wtype"><option value="">--</option><option value="WB">Both</option><option value="WE">Execution</option></select>`)
	query := "?id=" + edit
	button := `<input type="button" id="saveForm2" value="Save" onclick="doSave()">`
	if edit != "" {
		button = `<input type="button" name="Update" value="Update" onclick="doSave()">`
	}
	b.WriteString(button + `</form>
<script>
function doSave() {
  var p = new URLSearchParams();
  ['wname','rname','houseName','desam','place','profession'].forEach(function (f) { p.set(f, document.getElementById(f).value); });
  fetch('/save` + query + `&' + p.toString()).then(function (r) { return r.text(); }).then(function (t) {
    alert(t);
    location = '/wizard?tab=witness';
  });
}
function showDetailsDel(id) { location = '/delete?id=' + id; }
function editRow(id) { location = '/wizard?tab=witness&edit=' + id; }
</script><table>`)
	ids := make([]string, 0, len(m.rows))
	for id := range m.rows {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		fmt.Fprintf(&b, `<tr><td>%s</td><td><a href="javascript:editRow('%s')">Edit</a> <a href="javascript:showDetailsDel('%s','witness')">Delete</a></td></tr>`,
			html.EscapeString(m.rows[id]["wname"]), id, id)
	}
	b.WriteString(`</table></body></html>`)
	_, _ = w.Write([]byte(b.String()))
}

func (m *mockPortal) save(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.TrimSpace(q.Get("wname")) == "" || q.Get("wname") == "REJECT" {
		_, _ = w.Write([]byte("Name Invalid"))
		return
	}
	id := q.Get("id")
	if id == "" {
		id = fmt.Sprintf("%d", m.nextID)
		m.nextID++
	}
	row := map[string]string{}
	for _, f := range witnessFields {
		row[f] = q.Get(f)
	}
	m.rows[id] = row
	m.saves++
	_, _ = w.Write([]byte("Saved Successfully"))
}

func (m *mockPortal) deletePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	id := r.URL.Query().Get("id")
	fmt.Fprintf(w, header+tabBar+`<h3>Witness Deletion Form</h3><input id="deleteReason" value="">
<input type="button" id="saveForm" value="Delete" onclick="if (confirm('Are you sure?')) { fetch('/dodelete?id=%s').then(function () { alert('Deleted Successfully'); location = '/wizard?tab=witness'; }); }">
</body></html>`, id)
}

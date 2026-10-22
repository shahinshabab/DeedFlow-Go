package web

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/auth"
	"github.com/shahinshabab/deedflow-go/internal/config"
	"github.com/shahinshabab/deedflow-go/internal/db"
	"github.com/shahinshabab/deedflow-go/internal/events"
	"github.com/shahinshabab/deedflow-go/internal/fernet"
	"github.com/shahinshabab/deedflow-go/internal/jobs"
	"github.com/shahinshabab/deedflow-go/internal/models"
	"github.com/shahinshabab/deedflow-go/internal/store"
	"golang.org/x/net/html"
)

// The page tests below are the Go counterparts of the Django project's
// tests.py files: they drive the real application over HTTP against a
// database (TEST_DATABASE_URL, default the local docker-compose one) and clean
// up everything they create. They skip when no database is reachable.

const testPassword = "pw-for-tests-123"

type site struct {
	t     *testing.T
	app   *App
	db    *db.DB
	st    *store.Store
	srv   *httptest.Server
	cfg   *config.Config
	users []int64
	orgs  []int64
	tag   string
}

func testDatabaseURL() string {
	if url := os.Getenv("TEST_DATABASE_URL"); url != "" {
		return url
	}
	return "postgres://deedflow:deedflow@127.0.0.1:5433/deedflow"
}

// newSite runs the app against the shared test database; every test cleans up
// the rows it creates.
func newSite(t *testing.T) *site { return newSiteAt(t, testDatabaseURL()) }

// newIsolatedSite runs the app against a database created for this test alone
// (and dropped afterwards), for tests that replace shared tables such as the
// scraped portal options.
func newIsolatedSite(t *testing.T) *site {
	t.Helper()
	base := testDatabaseURL()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	admin, err := db.Connect(ctx, base)
	if err != nil {
		t.Skip("no test database: ", err)
	}
	name := "deedflow_test_" + strings.ReplaceAll(store.NewUUID()[:12], "-", "")
	if _, err := admin.Pool.Exec(ctx, `CREATE DATABASE `+name); err != nil {
		admin.Close()
		t.Skip("cannot create a scratch database: ", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Pool.Exec(context.Background(), `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`)
		admin.Close()
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return newSiteAt(t, u.String())
}

func newSiteAt(t *testing.T, url string) *site {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	d, err := db.Connect(ctx, url)
	if err != nil {
		t.Skip("no test database: ", err)
	}
	if err := d.Migrate(context.Background()); err != nil {
		d.Close()
		t.Fatal(err)
	}
	key, err := fernet.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{SecretKey: "test", Debug: true, FieldEncryptionKey: key, TimeZone: time.UTC, MediaRoot: t.TempDir(), MediaURL: "/media/",
		PortalURL: "https://portal.invalid", ProcessingWorkers: 1, DefaultFromEmail: "deedflow@example.com", SiteURL: "http://testserver"}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := store.New(d)
	app, err := New(cfg, st, events.NewHub(d, log), jobs.New(d, log), log)
	if err != nil {
		d.Close()
		t.Fatal(err)
	}
	s := &site{t: t, app: app, db: d, st: st, cfg: cfg, srv: httptest.NewServer(app), tag: store.NewUUID()[:8]}
	t.Cleanup(s.cleanup)
	return s
}

// cleanup removes the users, organizations and everything hanging off them.
func (s *site) cleanup() {
	s.srv.Close()
	ctx := context.Background()
	q := func(sql string, args ...any) { _, _ = s.db.Pool.Exec(ctx, sql, args...) }
	// Delete deeds with every table that points at them.
	rows, _ := s.db.Pool.Query(ctx, `SELECT conrelid::regclass::text, a.attname FROM pg_constraint c
		JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = c.conkey[1]
		WHERE c.contype = 'f' AND c.confrelid = 'deeds_deed'::regclass`)
	type ref struct{ table, col string }
	var refs []ref
	for rows != nil && rows.Next() {
		var r ref
		_ = rows.Scan(&r.table, &r.col)
		refs = append(refs, r)
	}
	if rows != nil {
		rows.Close()
	}
	sub := `SELECT id FROM deeds_deed WHERE owner_id = ANY($1) OR organization_id = ANY($2)`
	for _, r := range refs {
		q(`DELETE FROM `+r.table+` WHERE `+r.col+` IN (`+sub+`)`, s.users, s.orgs)
	}
	q(`DELETE FROM deeds_deed WHERE owner_id = ANY($1) OR organization_id = ANY($2)`, s.users, s.orgs)
	q(`DELETE FROM ledger_expensebill WHERE expense_id IN (SELECT id FROM ledger_expense WHERE organization_id = ANY($1))`, s.orgs)
	q(`DELETE FROM ledger_expense WHERE organization_id = ANY($1)`, s.orgs)
	q(`DELETE FROM ledger_paymentreceipt WHERE token_id IN (SELECT id FROM ledger_registeredtoken WHERE organization_id = ANY($1))`, s.orgs)
	q(`DELETE FROM ledger_registeredtoken WHERE organization_id = ANY($1)`, s.orgs)
	q(`DELETE FROM ledger_receiptsyncrun WHERE organization_id = ANY($1)`, s.orgs)
	q(`DELETE FROM filereader_filingsheet WHERE owner_id = ANY($1)`, s.users)
	q(`DELETE FROM accounts_portalcredential WHERE organization_id = ANY($1)`, s.orgs)
	q(`DELETE FROM accounts_membership WHERE organization_id = ANY($1) OR user_id = ANY($2)`, s.orgs, s.users)
	q(`DELETE FROM accounts_organization WHERE id = ANY($1)`, s.orgs)
	q(`DELETE FROM django_admin_log WHERE user_id = ANY($1)`, s.users)
	q(`DELETE FROM auth_user WHERE id = ANY($1)`, s.users)
	s.db.Close()
}

func (s *site) org(name string) *models.Organization {
	s.t.Helper()
	o := &models.Organization{Name: name + " " + s.tag}
	if err := s.st.CreateOrganization(context.Background(), o); err != nil {
		s.t.Fatal(err)
	}
	s.orgs = append(s.orgs, o.ID)
	return o
}

func (s *site) member(username string, o *models.Organization, role string) *models.User {
	s.t.Helper()
	u := &models.User{Username: username + "-" + s.tag, FirstName: strings.Title(username), IsActive: true, Password: auth.MakePassword(testPassword)}
	ctx := context.Background()
	if err := s.st.CreateUser(ctx, u); err != nil {
		s.t.Fatal(err)
	}
	s.users = append(s.users, u.ID)
	if err := s.st.CreateMembership(ctx, &models.Membership{UserID: u.ID, OrganizationID: o.ID, Role: role}); err != nil {
		s.t.Fatal(err)
	}
	return u
}

// user is a user with no organization.
func (s *site) user(username string) *models.User {
	s.t.Helper()
	u := &models.User{Username: username + "-" + s.tag, IsActive: true, Password: auth.MakePassword(testPassword)}
	if err := s.st.CreateUser(context.Background(), u); err != nil {
		s.t.Fatal(err)
	}
	s.users = append(s.users, u.ID)
	return u
}

func (s *site) deed(owner *models.User) *models.Deed {
	s.t.Helper()
	d := &models.Deed{OwnerID: owner.ID}
	if err := s.st.CreateDeed(context.Background(), d); err != nil {
		s.t.Fatal(err)
	}
	return d
}

// client is a browser: cookies are kept, redirects are reported, not followed.
type client struct {
	s   *site
	t   *testing.T
	c   *http.Client
	url *url.URL
}

type response struct {
	Status   int
	Body     string
	Location string
}

func (r response) contains(sub string) bool { return strings.Contains(r.Body, sub) }

func (s *site) client() *client {
	jar, _ := cookiejar.New(nil)
	u, _ := url.Parse(s.srv.URL)
	return &client{s: s, t: s.t, url: u, c: &http.Client{Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (c *client) do(req *http.Request) response {
	c.t.Helper()
	resp, err := c.c.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return response{resp.StatusCode, string(b), resp.Header.Get("Location")}
}

func (c *client) get(path string) response {
	c.t.Helper()
	req, _ := http.NewRequest("GET", c.s.srv.URL+path, nil)
	return c.do(req)
}

func (c *client) cookie(name string) string {
	for _, ck := range c.c.Jar.Cookies(c.url) {
		if ck.Name == name {
			return ck.Value
		}
	}
	return ""
}

func (c *client) post(path string, form map[string]string) response {
	c.t.Helper()
	if c.cookie(csrfCookie) == "" {
		c.get("/login/")
	}
	v := url.Values{"csrfmiddlewaretoken": {maskCSRF(c.cookie(csrfCookie))}}
	for k, val := range form {
		v.Set(k, val)
	}
	req, _ := http.NewRequest("POST", c.s.srv.URL+path, strings.NewReader(v.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.do(req)
}

// filePart is a file part of a multipart form.
type filePart struct {
	field, name, contentType string
	data                     []byte
}

// postForm posts a multipart form (repeated values allowed) with files.
func (c *client) postForm(path string, form map[string][]string, files ...filePart) response {
	c.t.Helper()
	if c.cookie(csrfCookie) == "" {
		c.get("/login/")
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("csrfmiddlewaretoken", maskCSRF(c.cookie(csrfCookie)))
	for k, vals := range form {
		for _, v := range vals {
			_ = w.WriteField(k, v)
		}
	}
	for _, f := range files {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, f.field, f.name))
		h.Set("Content-Type", f.contentType)
		part, _ := w.CreatePart(h)
		_, _ = part.Write(f.data)
	}
	_ = w.Close()
	req, _ := http.NewRequest("POST", c.s.srv.URL+path, &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return c.do(req)
}

// login signs in through the real login form.
func (c *client) login(u *models.User) *client {
	c.t.Helper()
	r := c.post("/login/", map[string]string{"username": u.Username, "password": testPassword})
	if r.Status != 302 {
		c.t.Fatalf("login as %s: %d", u.Username, r.Status)
	}
	return c
}

func (c *client) wantStatus(path string, want int) response {
	c.t.Helper()
	r := c.get(path)
	if r.Status != want {
		c.t.Errorf("GET %s = %d, want %d", path, r.Status, want)
	}
	return r
}

func (s *site) rev(name string, args ...any) string {
	s.t.Helper()
	p, err := s.app.Router.Reverse(name, args...)
	if err != nil {
		s.t.Fatal(err)
	}
	return p
}

func newJSONRequest(url string, body []byte, csrf string) (*http.Request, error) {
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRFToken", csrf)
	return req, nil
}

// formValues reads the values a browser would submit for the inputs, selects
// and textareas of a page whose names start with prefix.
func formValues(t *testing.T, body, prefix string) url.Values {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	out := url.Values{}
	attr := func(n *html.Node, key string) (string, bool) {
		for _, a := range n.Attr {
			if a.Key == key {
				return a.Val, true
			}
		}
		return "", false
	}
	text := func(n *html.Node) string {
		var b strings.Builder
		var walk func(*html.Node)
		walk = func(n *html.Node) {
			if n.Type == html.TextNode {
				b.WriteString(n.Data)
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
		}
		walk(n)
		return b.String()
	}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			name, _ := attr(n, "name")
			if strings.HasPrefix(name, prefix) {
				switch n.Data {
				case "input":
					typ, _ := attr(n, "type")
					val, _ := attr(n, "value")
					_, checked := attr(n, "checked")
					if (typ == "checkbox" || typ == "radio") && !checked {
						break
					}
					if typ != "submit" && typ != "button" && typ != "file" {
						out.Add(name, val)
					}
				case "textarea":
					out.Add(name, strings.TrimPrefix(text(n), "\n"))
				case "select":
					chosen, haveFirst, haveSelected := "", false, false
					var scan func(*html.Node)
					scan = func(m *html.Node) {
						for o := m.FirstChild; o != nil; o = o.NextSibling {
							if o.Type != html.ElementNode {
								continue
							}
							if o.Data == "optgroup" {
								scan(o)
								continue
							}
							if o.Data != "option" {
								continue
							}
							v, ok := attr(o, "value")
							if !ok {
								v = text(o)
							}
							if !haveFirst {
								chosen, haveFirst = v, true
							}
							if _, sel := attr(o, "selected"); sel && !haveSelected {
								chosen, haveSelected = v, true
							}
						}
					}
					scan(n)
					out.Add(name, chosen)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return out
}

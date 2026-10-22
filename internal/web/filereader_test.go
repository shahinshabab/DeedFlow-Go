package web

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/shahinshabab/deedflow-go/internal/models"
)

// Ports of filereader/tests.py: FileReaderViewTests, ConvertViewTests and
// FilingSheetViewTests (the readers and writers have their own tests in
// internal/filereader).

func sample(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../filereader/testdata/files/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func fileForm(c *client, path string, extra map[string][]string, files ...filePart) response {
	return c.postForm(path, extra, files...)
}

func TestFileReaderLoginRequired(t *testing.T) {
	s := newSite(t)
	if r := s.client().get(s.rev("filereader:index")); r.Status != 302 {
		t.Errorf("anonymous = %d", r.Status)
	}
}

func TestFileReaderUploadRendersPages(t *testing.T) {
	s := newSite(t)
	c := s.client().login(s.user("reader"))
	r := fileForm(c, s.rev("filereader:index"), nil, filePart{"file", "deed.idml", "application/octet-stream", sample(t, "sample.idml")})
	if r.Status != 200 || !r.contains(`id="fr-doc"`) || !r.contains("File reader") {
		t.Fatalf("upload = %d", r.Status)
	}
	// the page data is embedded as JSON; the sample has two pages
	if n := strings.Count(r.Body, `"width":`); n < 2 {
		t.Errorf("expected two pages in the embedded document, found %d page boxes", n)
	}
}

func TestFileReaderShowsReaderErrors(t *testing.T) {
	s := newSite(t)
	c := s.client().login(s.user("reader"))
	r := fileForm(c, s.rev("filereader:index"), nil, filePart{"file", "x.pmd", "application/octet-stream", []byte("x")})
	if r.Status != 200 || r.contains(`id="fr-doc"`) || !r.contains("PageMaker") {
		t.Errorf("a broken file should explain itself, got %d", r.Status)
	}
}

func TestConvertOnlyPageMakerFiles(t *testing.T) {
	s := newSite(t)
	c := s.client().login(s.user("conv"))
	r := fileForm(c, s.rev("filereader:index"), map[string][]string{"action": {"idml"}},
		filePart{"file", "a.idml", "application/octet-stream", sample(t, "sample.idml")})
	if r.Status != 200 || !r.contains("Only PageMaker files") {
		t.Errorf("idml → idml = %d", r.Status)
	}
}

func TestConvertDownloadsScriptForCS3(t *testing.T) {
	s := newSite(t)
	c := s.client().login(s.user("conv"))
	r := fileForm(c, s.rev("filereader:index"), map[string][]string{"action": {"jsx"}},
		filePart{"file", "deed.pmd", "application/octet-stream", sample(t, "sample.pmd")})
	if !r.contains("#target indesign") {
		t.Errorf("not a script: %d %.80q", r.Status, r.Body)
	}
}

func TestConvertManyFilesMakeOneScript(t *testing.T) {
	s := newSite(t)
	c := s.client().login(s.user("conv"))
	pmd := sample(t, "sample.pmd")
	r := fileForm(c, s.rev("filereader:index"), map[string][]string{"action": {"jsx"}},
		filePart{"file", "one.pmd", "application/octet-stream", pmd}, filePart{"file", "two.pmd", "application/octet-stream", pmd},
		filePart{"file", "note.idml", "application/octet-stream", sample(t, "sample.idml")})
	script := r.Body
	_, rest, _ := strings.Cut(script, "var DOCS = ")
	docsJSON, _, _ := strings.Cut(rest, ";\n")
	var docs []struct{ Title string }
	if err := json.Unmarshal([]byte(docsJSON), &docs); err != nil || len(docs) != 2 || docs[0].Title != "one" || docs[1].Title != "two" {
		t.Errorf("docs = %v (%v)", docs, err)
	}
	if !strings.Contains(script, `var SKIPPED = ["note.idml (not a PageMaker file)"];`) {
		t.Errorf("skipped list missing")
	}
}

func TestConvertManyFilesMakeAZipOfIdml(t *testing.T) {
	s := newSite(t)
	c := s.client().login(s.user("conv"))
	pmd := sample(t, "sample.pmd")
	r := fileForm(c, s.rev("filereader:index"), map[string][]string{"action": {"idml"}},
		filePart{"file", "deed.pmd", "application/octet-stream", pmd}, filePart{"file", "deed.PMD", "application/octet-stream", pmd})
	z, err := zip.NewReader(bytes.NewReader([]byte(r.Body)), int64(len(r.Body)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range z.File {
		names = append(names, f.Name)
	}
	if strings.Join(sortedStrings(names), "|") != "deed (2).idml|deed.idml" {
		t.Errorf("zip = %v", names)
	}
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func TestConvertManyFilesNoneConvertible(t *testing.T) {
	s := newSite(t)
	c := s.client().login(s.user("conv"))
	r := fileForm(c, s.rev("filereader:index"), map[string][]string{"action": {"jsx"}},
		filePart{"file", "a.idml", "application/octet-stream", sample(t, "sample.idml")},
		filePart{"file", "b.pdf", "application/pdf", []byte("%PDF")})
	if !r.contains("None of these files could be converted") {
		t.Errorf("response = %d", r.Status)
	}
}

func TestConvertDownloadsIdml(t *testing.T) {
	s := newSite(t)
	c := s.client().login(s.user("conv"))
	r := fileForm(c, s.rev("filereader:index"), map[string][]string{"action": {"idml"}},
		filePart{"file", "deed.pmd", "application/octet-stream", sample(t, "sample.pmd")})
	z, err := zip.NewReader(bytes.NewReader([]byte(r.Body)), int64(len(r.Body)))
	if err != nil || r.Status != 200 {
		t.Fatalf("not a zip: %d %v", r.Status, err)
	}
	found := false
	for _, f := range z.File {
		found = found || f.Name == "designmap.xml"
	}
	if !found {
		t.Errorf("no designmap.xml in the package")
	}
}

// --- filing sheets ----------------------------------------------------------------

type filingFixture struct {
	*site
	user *client
}

func (f *filingFixture) upload(t *testing.T, name string) response {
	t.Helper()
	return f.user.postForm(f.rev("filereader:filing_new"), nil, filePart{"file", name, "application/octet-stream", sample(t, "sample.idml")})
}

func (f *filingFixture) sheet(t *testing.T, name string) (publicID string, content string, pages int, settings string) {
	t.Helper()
	err := f.db.Pool.QueryRow(context.Background(), `SELECT public_id::text, content, pages, settings::text FROM filereader_filingsheet WHERE name = $1 AND owner_id = ANY($2)`,
		name, f.users).Scan(&publicID, &content, &pages, &settings)
	if err != nil {
		t.Fatal(err)
	}
	return
}

func newFilingFixture(t *testing.T) *filingFixture {
	s := newSite(t)
	return &filingFixture{s, s.client().login(s.user("filing"))}
}

func TestFilingUploadForm(t *testing.T) {
	f := newFilingFixture(t)
	if r := f.user.get(f.rev("filereader:filing_new")); !r.contains("Make filing sheet") {
		t.Errorf("form = %d", r.Status)
	}
}

func TestFilingUploadSavesASheetAndOpensIt(t *testing.T) {
	f := newFilingFixture(t)
	r := f.upload(t, "a.idml")
	id, content, _, _ := f.sheet(t, "a.idml")
	if r.Status != 302 || r.Location != f.rev("filereader:filing_edit", id) || content != "" {
		t.Fatalf("upload = %d %q (content %q)", r.Status, r.Location, content)
	}
	page := f.user.get(r.Location)
	for _, want := range []string{`id="fs-doc"`, `id="fs-saved"`, "Line gap", `id="fsSave"`, `id="fsDraw"`, `id="fsTableLines"`} {
		if !page.contains(want) {
			t.Errorf("editor lacks %s", want)
		}
	}
}

func TestFilingSheetKeepsTheUploadedFileName(t *testing.T) {
	f := newFilingFixture(t)
	name := "ആധാരം 1234 of 2024 (Ravi).idml"
	f.upload(t, name)
	f.sheet(t, name)
	if !f.user.get(f.rev("filereader:filing")).contains(name) {
		t.Errorf("the list lacks the sheet's name")
	}
}

func TestFilingListSearchAndView(t *testing.T) {
	f := newFilingFixture(t)
	f.upload(t, "first deed.idml")
	f.upload(t, "second.idml")
	page := f.user.get(f.rev("filereader:filing"))
	if !page.contains("first deed.idml") || !page.contains("second.idml") {
		t.Errorf("both sheets should be listed")
	}
	page = f.user.get(f.rev("filereader:filing") + "?q=FIRST")
	if !page.contains("first deed.idml") || page.contains("second.idml") {
		t.Errorf("search is case-insensitive and narrows the list")
	}
	id, _, _, _ := f.sheet(t, "second.idml")
	view := f.user.get(f.rev("filereader:filing_view", id))
	if !view.contains(`contenteditable="false"`) || view.contains(`id="fsSave"`) || view.contains(`id="fsDraw"`) {
		t.Errorf("view page should be read-only")
	}
}

func (c *client) postJSON(path string, v any) response {
	c.t.Helper()
	b, _ := json.Marshal(v)
	if c.cookie(csrfCookie) == "" {
		c.get("/login/")
	}
	req, _ := newJSONRequest(c.s.srv.URL+path, b, maskCSRF(c.cookie(csrfCookie)))
	return c.do(req)
}

func TestFilingSave(t *testing.T) {
	f := newFilingFixture(t)
	f.upload(t, "a.idml")
	id, _, _, _ := f.sheet(t, "a.idml")
	url := f.rev("filereader:filing_save", id)
	r := f.user.postJSON(url, map[string]any{"content": "<p>Corrected</p>", "settings": map[string]any{"size": "13", "extra": 1}, "pages": 4})
	if r.Status != 200 {
		t.Fatalf("save = %d %s", r.Status, r.Body)
	}
	_, content, pages, settings := f.sheet(t, "a.idml")
	var st map[string]any
	_ = json.Unmarshal([]byte(settings), &st)
	if content != "<p>Corrected</p>" || pages != 4 || st["size"] != "13" {
		t.Errorf("saved = %q %d %v", content, pages, st)
	}
	if r := f.user.postJSON(url, map[string]any{"settings": map[string]any{}}); r.Status != 400 {
		t.Errorf("a save without content = %d, want 400", r.Status)
	}
}

func TestFilingOtherPeoplesSheetsAreOutOfReach(t *testing.T) {
	f := newFilingFixture(t)
	f.upload(t, "a.idml")
	id, _, _, _ := f.sheet(t, "a.idml")
	o := f.client().login(f.user2())
	if o.get(f.rev("filereader:filing")).contains("a.idml") {
		t.Errorf("another user's sheet is listed")
	}
	o.wantStatus(f.rev("filereader:filing_edit", id), 404)
	if r := o.postJSON(f.rev("filereader:filing_save", id), map[string]any{"content": "x"}); r.Status != 404 {
		t.Errorf("save by another user = %d", r.Status)
	}
}

func (f *filingFixture) user2() *models.User { return f.site.user("other") }

func TestFilingConvertBothWays(t *testing.T) {
	f := newFilingFixture(t)
	url := f.rev("filereader:filing_convert")
	r := f.user.postJSON(url, map[string]any{"to": "legacy", "texts": []string{"ആധാരം ok"}})
	var out map[string]any
	_ = json.Unmarshal([]byte(r.Body), &out)
	if got, _ := json.Marshal(out); string(got) != `{"texts":[[["B[mcw ",true],["ok",false]]]}` {
		t.Errorf("to legacy = %s", got)
	}
	r = f.user.postJSON(url, map[string]any{"to": "unicode", "texts": []string{"B[mcw", " "}})
	if strings.TrimSpace(r.Body) != `{"texts":["ആധാരം"," "]}` {
		t.Errorf("to unicode = %s", r.Body)
	}
	if r := f.user.postJSON(url, map[string]any{"to": "other", "texts": []string{}}); r.Status != 400 {
		t.Errorf("unknown direction = %d", r.Status)
	}
}

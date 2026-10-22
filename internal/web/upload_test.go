package web

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/models"
)

// Ports of PdfUploadTests (deeds/tests.py): what's turned away at the door,
// who can open the stored file, and that a cut-off run can be retried. They
// run on a scratch database because they count queued jobs.

// minimalPDF is a one-page PDF with one line of text, built by hand.
func minimalPDF(text string) []byte {
	stream := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", text)
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	var offsets []int
	for i, body := range objects {
		offsets = append(offsets, out.Len())
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, o := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return out.Bytes()
}

type uploadFixture struct {
	*site
	org, otherOrg *models.Organization
	user, other   *models.User
	c             *client
}

func newUploadFixture(t *testing.T) *uploadFixture {
	s := newIsolatedSite(t)
	f := &uploadFixture{site: s}
	f.org, f.otherOrg = s.org("Office A"), s.org("Office B")
	f.user = s.member("uploader", f.org, models.RoleStaff)
	f.other = s.member("outsider", f.otherOrg, models.RoleAdmin)
	f.c = s.client().login(f.user)
	return f
}

func (f *uploadFixture) upload(content []byte, form map[string][]string) response {
	return f.c.postForm(f.rev("deeds:upload"), form, filePart{"pdf_file", "deed.pdf", "application/pdf", content})
}

func (f *uploadFixture) jobs() int {
	var n int
	_ = f.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM df_job WHERE kind = 'process_deed_pdf'`).Scan(&n)
	return n
}

func (f *uploadFixture) deeds(owner *models.User) []models.Deed {
	rows, err := f.db.Pool.Query(context.Background(), `SELECT id FROM deeds_deed WHERE owner_id = $1 ORDER BY id`, owner.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	var out []models.Deed
	for _, id := range ids {
		d, err := f.st.DeedByID(context.Background(), id)
		if err != nil {
			f.t.Fatal(err)
		}
		out = append(out, *d)
	}
	return out
}

func TestUploadRealPDFIsStoredAndQueued(t *testing.T) {
	f := newUploadFixture(t)
	r := f.upload(minimalPDF("Deed"), nil)
	deeds := f.deeds(f.user)
	if len(deeds) != 1 {
		t.Fatalf("%d deeds (response %d)", len(deeds), r.Status)
	}
	d := deeds[0]
	if r.Status != 302 || r.Location != f.rev("deeds:processing", d.PublicID) {
		t.Errorf("redirect = %d %q", r.Status, r.Location)
	}
	if f.jobs() != 1 {
		t.Errorf("%d processing jobs queued, want 1", f.jobs())
	}
	if d.OrganizationID == nil || *d.OrganizationID != f.org.ID {
		t.Errorf("organization = %v", d.OrganizationID)
	}
	if got := f.c.get("/media/" + *d.SourcePDF); got.Status != 200 || got.Body != string(minimalPDF("Deed")) {
		t.Errorf("stored pdf = %d (%d bytes)", got.Status, len(got.Body))
	}
}

func TestUploadTurnsAwayWrongEmptyOrDamagedFiles(t *testing.T) {
	f := newUploadFixture(t)
	for _, c := range []struct{ content, want string }{
		{"", "empty"},
		{"PK\x03\x04 a zip file", "a PDF. Upload the deed as a PDF"},
		{"%PDF-1.4\nnot really a pdf", "be opened. It may be damaged"},
		{string(minimalPDF("Deed")) + strings.Repeat(" ", 26*1024*1024), "the limit is"},
	} {
		r := f.upload([]byte(c.content), nil)
		if r.Status != 400 || !r.contains(c.want) {
			t.Errorf("%.20q: %d, want 400 with %q", c.content, r.Status, c.want)
		}
	}
	if f.jobs() != 0 || len(f.deeds(f.user)) != 0 {
		t.Errorf("a rejected file left a deed or a job behind")
	}
}

func TestUploadContinuingAnExistingToken(t *testing.T) {
	f := newUploadFixture(t)
	r := f.upload(minimalPDF("Deed"), map[string][]string{"mode": {"existing"}, "token_number": {" t10380594 "}})
	deeds := f.deeds(f.user)
	if len(deeds) != 1 || r.Status != 302 || r.Location != f.rev("deeds:processing", deeds[0].PublicID) || deeds[0].TokenNumber != "T10380594" || f.jobs() != 1 {
		t.Fatalf("existing token: %d %q deeds=%d jobs=%d", r.Status, r.Location, len(deeds), f.jobs())
	}
	// A fresh upload ignores a token number.
	other := f.site.client().login(f.other)
	other.postForm(f.rev("deeds:upload"), map[string][]string{"mode": {"fresh"}, "token_number": {"T10380594"}},
		filePart{"pdf_file", "deed.pdf", "application/pdf", minimalPDF("Deed")})
	if d := f.deeds(f.other); len(d) != 1 || d[0].TokenNumber != "" {
		t.Errorf("a fresh upload took the token: %+v", d)
	}
}

func TestUploadBadOrTakenTokenNumberIsTurnedAway(t *testing.T) {
	f := newUploadFixture(t)
	existing := func(token string) response {
		return f.upload(minimalPDF("Deed"), map[string][]string{"mode": {"existing"}, "token_number": {token}})
	}
	for _, token := range []string{"", "10380594"} {
		if r := existing(token); r.Status != 400 || !r.contains("like T10380594") {
			t.Errorf("token %q: %d", token, r.Status)
		}
	}
	earlier := f.deed(f.user)
	earlier.TokenNumber, earlier.Title = "T10380594", "Earlier deed"
	if err := f.st.SaveDeed(context.Background(), earlier, "token_number", "title"); err != nil {
		t.Fatal(err)
	}
	if r := existing("T10380594"); r.Status != 400 || !r.contains("already on Earlier deed") {
		t.Errorf("taken token: %d", r.Status)
	}
	if f.jobs() != 0 || len(f.deeds(f.user)) != 1 {
		t.Errorf("a refused upload left a deed or a job behind")
	}
}

func TestStoredFilesOpenForTheirOrganizationOnly(t *testing.T) {
	f := newUploadFixture(t)
	f.upload(minimalPDF("Deed"), nil)
	d := f.deeds(f.user)[0]
	shot, err := f.app.SaveMedia("deed_screenshots/p1.png", []byte("png"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(context.Background(), `INSERT INTO deeds_deedscreenshot (deed_id, label, image, created_at) VALUES ($1, 'p1', $2, now())`, d.ID, shot); err != nil {
		t.Fatal(err)
	}
	pdfURL, shotURL := "/media/"+*d.SourcePDF, "/media/"+shot
	f.c.wantStatus(pdfURL, 200)
	f.c.wantStatus(shotURL, 200)
	o := f.client().login(f.other)
	o.wantStatus(pdfURL, 404)
	o.wantStatus(shotURL, 404)
	// the mux cleans the path before anything is looked up
	if r := o.get("/media/../config/settings.py"); r.Status != 404 && !(r.Status == 301 && o.get(r.Location).Status == 404) {
		t.Errorf("path traversal = %d %q", r.Status, r.Location)
	}
	if r := f.client().get(pdfURL); r.Status != 302 {
		t.Errorf("anonymous = %d, want a redirect to the login", r.Status)
	}
}

func TestCutOffProcessingFailsAndCanBeRetried(t *testing.T) {
	f := newUploadFixture(t)
	f.upload(minimalPDF("Deed"), nil)
	d := f.deeds(f.user)[0]
	ctx := context.Background()
	set := func(status string, age time.Duration) {
		_, err := f.db.Pool.Exec(ctx, `UPDATE deeds_deed SET status = $2, result_message = '', updated_at = now() - make_interval(secs => $3) WHERE id = $1`, d.ID, status, age.Seconds())
		if err != nil {
			t.Fatal(err)
		}
	}
	page := f.rev("deeds:processing", d.PublicID)
	set(models.StatusStructuring, 0)
	if r := f.c.get(page); !r.contains(`id="retry-form"`) || !r.contains(" hidden>") {
		t.Errorf("a run still going offers a retry")
	}
	set(models.StatusStructuring, 2*time.Hour)
	r := f.c.get(page)
	if !r.contains("interrupted") || r.contains(`class="d-flex flex-wrap gap-2 mt-2" hidden>`) {
		t.Errorf("a cut-off run should say so and offer a retry (%d)", r.Status)
	}
	before := f.jobs()
	f.c.post(f.rev("deeds:reprocess", d.PublicID), nil)
	got, _ := f.st.DeedByID(ctx, d.ID)
	if got.Status != models.StatusExtracting || f.jobs() != before+1 {
		t.Errorf("after retry: status %s, jobs %d→%d", got.Status, before, f.jobs())
	}
}

func TestDeedThatFailedAtSubmissionIsNeverReprocessed(t *testing.T) {
	f := newUploadFixture(t)
	f.upload(minimalPDF("Deed"), nil)
	d := f.deeds(f.user)[0]
	ctx := context.Background()
	_, _ = f.db.Pool.Exec(ctx, `UPDATE deeds_deed SET status = 'failed', result_message = 'Not sent to the portal - fix first' WHERE id = $1`, d.ID)
	r := f.c.get(f.rev("deeds:processing", d.PublicID))
	if r.Status != 302 || r.Location != f.rev("deeds:detail", d.PublicID) {
		t.Errorf("processing page = %d %q", r.Status, r.Location)
	}
	before := f.jobs()
	f.c.post(f.rev("deeds:reprocess", d.PublicID), nil)
	got, _ := f.st.DeedByID(ctx, d.ID)
	if got.Status != models.StatusFailed || f.jobs() != before {
		t.Errorf("reprocessed: status %s, jobs %d→%d", got.Status, before, f.jobs())
	}
}

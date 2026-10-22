package pipeline

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/config"
	"github.com/shahinshabab/deedflow-go/internal/db"
	"github.com/shahinshabab/deedflow-go/internal/deeds"
	"github.com/shahinshabab/deedflow-go/internal/events"
	"github.com/shahinshabab/deedflow-go/internal/models"
	"github.com/shahinshabab/deedflow-go/internal/portal"
	"github.com/shahinshabab/deedflow-go/internal/store"
)

// fakePortal stands in for the real portal automation, so the orchestration
// (locks, passes, snapshot bookkeeping, screenshots, events) is verified
// independent of the portal's markup.
type fakePortal struct {
	mu        sync.Mutex
	submits   [][]string // the sections each SubmitDeed pass was asked for
	rowPlans  []deeds.RowPlan
	failOnce  string // a section to report failed on the first pass only
	failAlway string // a section to report failed on every pass
	token     string
}

func (f *fakePortal) SubmitDeed(p *deeds.Payload, progress portal.Progress, existing string, onToken func(string), sections []string) (*portal.SubmitResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.submits = append(f.submits, append([]string(nil), sections...))
	token := existing
	if token == "" {
		token = f.token
		onToken(token)
	}
	progress("login", 8, true, "Logged in.", nil)
	progress("registration", 20, true, "Registration saved.", [][]byte{{0x89, 'P', 'N', 'G'}, {0x89, 'P', 'N', 'G', 1}})
	wanted := sections
	if wanted == nil {
		for _, s := range deeds.PortalSections {
			wanted = append(wanted, s.Key)
		}
		wanted = append(wanted, "document_link")
	}
	res := &portal.SubmitResult{TokenNumber: token, SectionResults: map[string]portal.StageResult{},
		SectionErrors: map[string][]string{}, SectionsOK: map[string]bool{}, OverallOK: true}
	for _, s := range wanted {
		res.SectionsOK[s] = true
		fails := s == f.failAlway || (s == f.failOnce && len(f.submits) == 1)
		if fails {
			res.SectionsOK[s] = false
			res.SectionErrors[s] = []string{"Property 1: browser error - something transient"}
			res.OverallOK = false
		}
	}
	progress("complete", 100, res.OverallOK, "Submission complete.", nil)
	return res, nil
}

func (f *fakePortal) SubmitRowEdits(p *deeds.Payload, plan deeds.RowPlan, token string, progress portal.Progress) (*portal.RowEditResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rowPlans = append(f.rowPlans, plan)
	res := &portal.RowEditResult{TokenNumber: token, RowsOK: map[string]map[int]bool{}, SectionResults: map[string]portal.StageResult{}, OverallOK: true}
	for _, list := range plan.Lists {
		res.RowsOK[list] = map[int]bool{}
		for _, ch := range plan.Changes[list] {
			res.RowsOK[list][ch.Index] = true
		}
	}
	progress("complete", 100, true, "Edits finished.", nil)
	return res, nil
}

func (f *fakePortal) Close() {}

func submitFixture(t *testing.T, fp *fakePortal) (*Env, *models.Deed, *store.Store) {
	d := testDB(t)
	ctx := context.Background()
	st := store.New(d)
	u := &models.User{Username: "submit-test-" + store.NewUUID()[:8], IsActive: true}
	if err := st.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	org := &models.Organization{Name: "Submit test " + u.Username}
	if err := st.CreateOrganization(ctx, org); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateMembership(ctx, &models.Membership{OrganizationID: org.ID, UserID: u.ID, Role: models.RoleAdmin}); err != nil {
		t.Fatal(err)
	}
	if err := st.SavePortalCredential(ctx, &models.PortalCredential{OrganizationID: org.ID, PortalUsername: "x",
		PortalPassword: "y", NewDeedMenuIndex: 1, MyDeedsMenuIndex: 2}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		purgeDeeds(ctx, d, u.ID)
		_, _ = d.Pool.Exec(ctx, `DELETE FROM accounts_portalcredential WHERE organization_id = $1`, org.ID)
		_, _ = d.Pool.Exec(ctx, `DELETE FROM accounts_membership WHERE organization_id = $1`, org.ID)
		_, _ = d.Pool.Exec(ctx, `DELETE FROM accounts_organization WHERE id = $1`, org.ID)
		_, _ = d.Pool.Exec(ctx, `DELETE FROM auth_user WHERE id = $1`, u.ID)
	})
	deed, err := SeedTestDeed(ctx, st, u.ID, 2, deeds.Today(time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	media := t.TempDir()
	hub := events.NewHub(d, nil)
	env := &Env{Cfg: &config.Config{MediaRoot: media, MediaURL: "/media/", TimeZone: time.UTC}, Store: st, Hub: hub,
		Loc: time.UTC, NewPortal: func(context.Context, *models.PortalCredential, time.Time) (Portal, error) { return fp, nil }}
	return env, deed, st
}

func startSubmit(t *testing.T, st *store.Store, deed *models.Deed) {
	deed.Status = models.StatusSubmitting
	if err := st.SaveDeed(context.Background(), deed, "status"); err != nil {
		t.Fatal(err)
	}
}

func TestSubmitDeedFirstSubmissionAndEdit(t *testing.T) {
	if os.Getenv("SKIP_DB") != "" {
		t.Skip()
	}
	fp := &fakePortal{token: "T777001"}
	env, deed, st := submitFixture(t, fp)
	ctx := context.Background()

	startSubmit(t, st, deed)
	if err := env.SubmitDeedToPortal(ctx, deed.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := st.DeedByID(ctx, deed.ID)
	if got.Status != models.StatusCompleted || got.TokenNumber != "T777001" || got.Progress != 100 {
		t.Fatalf("deed = %s %q %d (%s)", got.Status, got.TokenNumber, got.Progress, got.ResultMessage)
	}
	if !strings.HasPrefix(got.ResultMessage, "Submitted successfully. Token number: T777001") {
		t.Fatalf("message = %q", got.ResultMessage)
	}
	for _, sec := range deeds.PortalSections {
		if got.PortalSnapshot.Sections[sec.Key] == "" {
			t.Fatalf("section %s not recorded in the snapshot: %v", sec.Key, got.PortalSnapshot.Sections)
		}
	}
	if len(got.PortalSnapshot.Rows["claimants"]) != 2 {
		t.Fatalf("rows recorded = %v", got.PortalSnapshot.Rows)
	}
	shots, _ := st.RecentScreenshots(ctx, deed.ID, 10)
	if len(shots) != 2 || !strings.HasPrefix(shots[0].Label+shots[1].Label, "registration_p") {
		t.Fatalf("screenshots = %+v", shots)
	}
	if _, err := os.Stat(env.Cfg.MediaRoot + "/" + shots[0].Image); err != nil {
		t.Fatal(err)
	}
	if len(fp.submits) != 1 || fp.submits[0] != nil {
		t.Fatalf("submits = %v", fp.submits)
	}

	// Nothing edited: nothing is sent.
	startSubmit(t, st, got)
	if err := env.SubmitDeedToPortal(ctx, deed.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = st.DeedByID(ctx, deed.ID)
	if got.Status != models.StatusCompleted || !strings.Contains(got.ResultMessage, "Nothing was edited") || len(fp.submits) != 1 {
		t.Fatalf("expected a no-op: %s %q submits=%d", got.Status, got.ResultMessage, len(fp.submits))
	}

	// Edit one claimant: only that row goes through the row-edit pipeline.
	data, _ := st.LoadDeedData(ctx, got)
	claimant := data.Claimants[1]
	claimant.Name = "ഷബ്ന എം"
	if err := st.UpdateRow(ctx, store.RowTable["claimants"], &claimant); err != nil {
		t.Fatal(err)
	}
	startSubmit(t, st, got)
	if err := env.SubmitDeedToPortal(ctx, deed.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = st.DeedByID(ctx, deed.ID)
	if got.Status != models.StatusCompleted || !strings.HasPrefix(got.ResultMessage, "Edit finished on the portal (Claimant 2") {
		t.Fatalf("edit result: %s %q", got.Status, got.ResultMessage)
	}
	if len(fp.rowPlans) != 1 || len(fp.rowPlans[0].Changes["claimants"]) != 1 || fp.rowPlans[0].Changes["claimants"][0].Index != 1 {
		t.Fatalf("row plans = %+v", fp.rowPlans)
	}
	data, _ = st.LoadDeedData(ctx, got)
	payload, _ := deeds.BuildPayload(data)
	if got.PortalSnapshot.Sections["claimants"] != deeds.SectionFingerprints(payload)["claimants"] {
		t.Fatal("the edited section is not recorded as confirmed")
	}
}

func TestSubmitDeedPassesAndFailures(t *testing.T) {
	fp := &fakePortal{token: "T777002", failOnce: "schedule"}
	env, deed, st := submitFixture(t, fp)
	ctx := context.Background()
	startSubmit(t, st, deed)
	if err := env.SubmitDeedToPortal(ctx, deed.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := st.DeedByID(ctx, deed.ID)
	if got.Status != models.StatusCompleted {
		t.Fatalf("a transient failure must be retried on the same token: %s %q", got.Status, got.ResultMessage)
	}
	if len(fp.submits) != 2 || strings.Join(fp.submits[1], ",") != "schedule" {
		t.Fatalf("second pass sections = %v", fp.submits)
	}

	// A section that keeps failing: a failed deed that says so, token kept.
	fp2 := &fakePortal{token: "T777003", failAlway: "schedule"}
	env2, deed2, st2 := submitFixture(t, fp2)
	startSubmit(t, st2, deed2)
	if err := env2.SubmitDeedToPortal(ctx, deed2.ID); err != nil {
		t.Fatal(err)
	}
	got2, _ := st2.DeedByID(ctx, deed2.ID)
	if got2.Status != models.StatusFailed || got2.TokenNumber != "T777003" ||
		!strings.Contains(got2.ResultMessage, "some sections not confirmed") {
		t.Fatalf("persistent failure: %s %q %q", got2.Status, got2.TokenNumber, got2.ResultMessage)
	}
	if len(fp2.submits) != MaxSubmissionPasses {
		t.Fatalf("passes = %d", len(fp2.submits))
	}
}

func TestSubmitDeedGuards(t *testing.T) {
	fp := &fakePortal{token: "T777004"}
	env, deed, st := submitFixture(t, fp)
	ctx := context.Background()

	// A deed that isn't submitting is a stale job: ignored.
	if err := env.SubmitDeedToPortal(ctx, deed.ID); err != nil {
		t.Fatal(err)
	}
	if len(fp.submits) != 0 {
		t.Fatal("a stale job must not touch the portal")
	}

	// The browser is busy: the job asks to run again later.
	startSubmit(t, st, deed)
	lock, ok, err := env.Store.DB.TryLock(ctx, BrowserLockKey())
	if err != nil || !ok {
		t.Skip("browser lock is held elsewhere")
	}
	err = env.SubmitDeedToPortal(ctx, deed.ID)
	lock.Release()
	if _, retry := err.(interface{ Error() string }); !retry || err == nil || !strings.HasPrefix(err.Error(), "retry in") {
		t.Fatalf("expected a retry request, got %v", err)
	}
	if len(fp.submits) != 0 {
		t.Fatal("the portal must not be used while the browser is busy")
	}

	// A failed pre-flight check leaves the portal alone.
	data, _ := st.LoadDeedData(ctx, deed)
	data.Presenter.Name = ""
	if err := st.SavePresenter(ctx, data.Presenter); err != nil {
		t.Fatal(err)
	}
	if err := env.SubmitDeedToPortal(ctx, deed.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := st.DeedByID(ctx, deed.ID)
	if got.Status != models.StatusFailed || !strings.HasPrefix(got.ResultMessage, "Not sent to the portal - please fix first") || len(fp.submits) != 0 {
		t.Fatalf("pre-flight: %s %q", got.Status, got.ResultMessage)
	}
}

// purgeDeeds deletes a user's deeds with every row that points at them.
func purgeDeeds(ctx context.Context, d *db.DB, ownerID int64) {
	rows, err := d.Pool.Query(ctx, `SELECT conrelid::regclass::text, a.attname FROM pg_constraint c
		JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = c.conkey[1]
		WHERE c.contype = 'f' AND c.confrelid = 'deeds_deed'::regclass`)
	if err != nil {
		return
	}
	type ref struct{ table, col string }
	var refs []ref
	for rows.Next() {
		var r ref
		_ = rows.Scan(&r.table, &r.col)
		refs = append(refs, r)
	}
	rows.Close()
	for _, r := range refs {
		_, _ = d.Pool.Exec(ctx, `DELETE FROM `+r.table+` WHERE `+r.col+` IN (SELECT id FROM deeds_deed WHERE owner_id = $1)`, ownerID)
	}
	_, _ = d.Pool.Exec(ctx, `DELETE FROM deeds_deed WHERE owner_id = $1`, ownerID)
}

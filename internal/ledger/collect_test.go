package ledger_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/config"
	"github.com/shahinshabab/deedflow-go/internal/db"
	"github.com/shahinshabab/deedflow-go/internal/ledger"
	"github.com/shahinshabab/deedflow-go/internal/mail"
	"github.com/shahinshabab/deedflow-go/internal/models"
	"github.com/shahinshabab/deedflow-go/internal/portal"
	"github.com/shahinshabab/deedflow-go/internal/store"
	"github.com/shahinshabab/deedflow-go/internal/testdb"
	"github.com/shahinshabab/deedflow-go/internal/web"
)

type fakeSession struct {
	mu         sync.Mutex
	registered []portal.RegisteredRow
	regErr     error
	receipts   map[string]any // *portal.TokenReceipts or error
	calls      []string
}

func (f *fakeSession) RegisteredTokens() ([]portal.RegisteredRow, error) {
	return f.registered, f.regErr
}
func (f *fakeSession) TokenReceipts(token string) (*portal.TokenReceipts, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, token)
	switch v := f.receipts[token].(type) {
	case error:
		return nil, v
	case *portal.TokenReceipts:
		return v, nil
	}
	return nil, errors.New("not scripted")
}
func (f *fakeSession) Logout() error { return nil }
func (f *fakeSession) Close()        {}

type outbox struct {
	mu   sync.Mutex
	sent []*mail.Message
}

func (o *outbox) Send(_ context.Context, m *mail.Message) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sent = append(o.sent, m)
	return nil
}
func (o *outbox) Description() string { return "test" }

func receipt(txn, amount string) portal.Receipt {
	d := db.MustDecimal(amount)
	paid := time.Date(2026, 10, 6, 9, 47, 5, 0, time.UTC)
	return portal.Receipt{TransactionNo: txn, ApplicantName: "TEST PERSON", SRO: "Vilayur", Amount: &d, PaymentMode: "e-Payment",
		PaidAt: &paid, Status: "Payment Successfully done", Fields: [][2]string{{"Transaction Number", txn}}}
}

type fixture struct {
	svc  *ledger.Service
	st   *store.Store
	org  *models.Organization
	cred *models.PortalCredential
	fake *fakeSession
	out  *outbox
}

func newFixture(t *testing.T) *fixture {
	d := testdb.Open(t)
	bg := context.Background()
	st := store.New(d)
	admin := &models.User{Username: "ledger-test-" + store.NewUUID()[:8], Email: "boss@example.com", IsActive: true}
	if err := st.CreateUser(bg, admin); err != nil {
		t.Fatal(err)
	}
	org := &models.Organization{Name: "Office " + admin.Username}
	if err := st.CreateOrganization(bg, org); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateMembership(bg, &models.Membership{OrganizationID: org.ID, UserID: admin.ID, Role: models.RoleAdmin}); err != nil {
		t.Fatal(err)
	}
	cred := &models.PortalCredential{OrganizationID: org.ID, PortalUsername: "u", CollectReceipts: true, NewDeedMenuIndex: 1, MyDeedsMenuIndex: 2}
	if err := st.SavePortalCredential(bg, cred); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.Pool.Exec(bg, `DELETE FROM ledger_receiptsyncrun WHERE organization_id = $1`, org.ID)
		_, _ = d.Pool.Exec(bg, `DELETE FROM ledger_registeredtoken WHERE organization_id = $1`, org.ID)
		_, _ = d.Pool.Exec(bg, `DELETE FROM accounts_portalcredential WHERE organization_id = $1`, org.ID)
		_, _ = d.Pool.Exec(bg, `DELETE FROM accounts_membership WHERE organization_id = $1`, org.ID)
		_, _ = d.Pool.Exec(bg, `DELETE FROM accounts_organization WHERE id = $1`, org.ID)
		_, _ = d.Pool.Exec(bg, `DELETE FROM auth_user WHERE id = $1`, admin.ID)
	})
	cfg := &config.Config{TimeZone: time.UTC, DefaultFromEmail: "deedflow@example.com", SiteURL: "https://deedflow.example"}
	render, err := web.NewRenderer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	d1, d2 := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	fake := &fakeSession{
		registered: []portal.RegisteredRow{
			{Token: "T10000001", Name: "ഒന്ന്", Date: &d1, Status: "Registered"},
			{Token: "T10000002", Name: "രണ്ട്", Date: &d2, Status: "Registered"},
		},
		receipts: map[string]any{
			"T10000001": &portal.TokenReceipts{Presenter: portal.Presenter{Place: "സ്ഥലം", SRO: "Vilayur"},
				Stamp: []portal.Receipt{receipt("620T1000000101", "2000.00")}, Fee: []portal.Receipt{receipt("620T1000000102", "10130.00")}},
			"T10000002": &portal.ReceiptsUnavailable{Msg: "Token No does not exist .. !!!"},
		},
	}
	out := &outbox{}
	svc := &ledger.Service{Cfg: cfg, Store: st, Mail: out, Render: render,
		Open: func(context.Context, *models.PortalCredential, time.Time) (ledger.Session, error) { return fake, nil }}
	return &fixture{svc, st, org, cred, fake, out}
}

func (f *fixture) run(t *testing.T, attempt int) *models.ReceiptSyncRun {
	run, err := f.svc.RunCollection(context.Background(), f.cred, f.org, attempt)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestCollectsReceiptsAndReportsPending(t *testing.T) {
	f := newFixture(t)
	run := f.run(t, 1)
	if run.Status != models.RunPartial || run.RegisteredFound != 2 || run.Collected != 1 || run.Pending != 1 {
		t.Fatalf("run = %+v", run)
	}
	ctx := context.Background()
	done, _ := f.st.RegisteredTokenByNumber(ctx, f.org.ID, "T10000001")
	if done.Status != models.TokenCollected || done.ApplicantName != "TEST PERSON" || done.SRO != "Vilayur" || done.PresenterPlaceML != "സ്ഥലം" {
		t.Fatalf("token = %+v", done)
	}
	if got := models.TotalOf(done.StampReceipts()).String(); got != "2000.00" {
		t.Fatalf("stamp total = %s", got)
	}
	if got := models.TotalOf(done.FeeReceipts()).String(); got != "10130.00" {
		t.Fatalf("fee total = %s", got)
	}
	failed, _ := f.st.RegisteredTokenByNumber(ctx, f.org.ID, "T10000002")
	if failed.Status != models.TokenFailed || !strings.Contains(failed.LastError, "does not exist") {
		t.Fatalf("failed token = %+v", failed)
	}
	if len(f.out.sent) != 0 {
		t.Fatal("not the last try yet: no email")
	}
}

func TestNextRunOnlyRetriesPendingAndNeverDuplicates(t *testing.T) {
	f := newFixture(t)
	f.run(t, 1)
	f.fake.calls = nil
	f.fake.receipts["T10000002"] = &portal.TokenReceipts{Fee: []portal.Receipt{receipt("620T1000000202", "500")}}
	run := f.run(t, 2)
	if len(f.fake.calls) != 1 || f.fake.calls[0] != "T10000002" {
		t.Fatalf("calls = %v", f.fake.calls)
	}
	if run.Status != models.RunDone {
		t.Fatalf("status = %s", run.Status)
	}
	var n int
	_ = f.st.DB.Pool.QueryRow(context.Background(), `SELECT count(*) FROM ledger_paymentreceipt r JOIN ledger_registeredtoken t ON t.id = r.token_id
		WHERE t.organization_id = $1`, f.org.ID).Scan(&n)
	if n != 3 {
		t.Fatalf("receipts = %d", n)
	}
	// Collecting again changes nothing.
	f.run(t, 2)
	_ = f.st.DB.Pool.QueryRow(context.Background(), `SELECT count(*) FROM ledger_paymentreceipt r JOIN ledger_registeredtoken t ON t.id = r.token_id
		WHERE t.organization_id = $1`, f.org.ID).Scan(&n)
	if n != 3 {
		t.Fatalf("receipts after a repeat = %d", n)
	}
}

func TestFinalAttemptEmailsAdminWhenStillPending(t *testing.T) {
	f := newFixture(t)
	run := f.run(t, 3)
	if len(f.out.sent) != 1 {
		t.Fatalf("emails = %d", len(f.out.sent))
	}
	m := f.out.sent[0]
	if len(m.To) != 1 || m.To[0] != "boss@example.com" || !strings.Contains(m.Text, "T10000002") {
		t.Fatalf("mail = %+v", m)
	}
	if want := "DeedFlow: 1 payment receipt pending for " + f.org.Name; m.Subject != want {
		t.Fatalf("subject = %q", m.Subject)
	}
	for _, s := range []string{"T10000002", "does not exist", "Collected tonight"} {
		if !strings.Contains(m.HTML, s) {
			t.Fatalf("html lacks %q:\n%s", s, m.HTML)
		}
	}
	if run.AlertSentTo != "boss@example.com" {
		t.Fatalf("alert_sent_to = %q", run.AlertSentTo)
	}
}

func TestAlertSaysWhenTheRunStopped(t *testing.T) {
	f := newFixture(t)
	f.fake.regErr = errors.New("Login page did not load")
	run := f.run(t, 3)
	if run.Status != models.RunFailed {
		t.Fatalf("status = %s", run.Status)
	}
	m := f.out.sent[0]
	if m.Subject != "DeedFlow: receipt collection stopped for "+f.org.Name || !strings.Contains(m.Text, "Login page did not load") ||
		!strings.Contains(m.HTML, "https://deedflow.example/accounts/") {
		t.Fatalf("mail = %+v", m)
	}
}

func TestAlertEmailOverridesAdmin(t *testing.T) {
	f := newFixture(t)
	f.cred.AlertEmail = "office@example.com"
	f.run(t, 3)
	if len(f.out.sent) != 1 || f.out.sent[0].To[0] != "office@example.com" {
		t.Fatalf("sent = %+v", f.out.sent)
	}
}

func TestNoEmailWhenEverythingCollected(t *testing.T) {
	f := newFixture(t)
	f.fake.registered = f.fake.registered[:1]
	run := f.run(t, 3)
	if run.Status != models.RunDone || len(f.out.sent) != 0 {
		t.Fatalf("status %s, emails %d", run.Status, len(f.out.sent))
	}
}

func TestLaterAttemptsSkipWhenTonightIsDone(t *testing.T) {
	f := newFixture(t)
	f.fake.registered = f.fake.registered[:1]
	f.svc.Cfg.TimeZone = time.FixedZone("test", 0)
	f.run(t, 1)
	// Today's run is "done": whether it counts as tonight's depends on the
	// clock, so only the pending-token half of NeedsAnotherRun is certain.
	again, err := f.svc.NeedsAnotherRun(context.Background(), f.org.ID)
	if err != nil {
		t.Fatal(err)
	}
	pending, _ := f.st.CountNotCollected(context.Background(), f.org.ID)
	if pending != 0 {
		t.Fatalf("pending = %d", pending)
	}
	started := f.svc.TonightStart()
	if time.Now().After(started) && again {
		t.Fatal("after 8:30 PM a finished run means tonight is done")
	}
}

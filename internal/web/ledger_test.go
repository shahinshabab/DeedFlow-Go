package web

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/shahinshabab/deedflow-go/internal/db"
	"github.com/shahinshabab/deedflow-go/internal/models"
)

// Ports of ledger/tests.py: AccountsPageTests and PaymentsTests (parsing and
// the nightly run are covered in internal/portal and internal/ledger).

func (s *site) registeredToken(org *models.Organization, token, sro string, day int) {
	s.t.Helper()
	ctx := context.Background()
	date := time.Date(2026, 10, day, 0, 0, 0, 0, time.UTC)
	rt := &models.RegisteredToken{OrganizationID: org.ID, Token: token, SRO: sro, ApplicantName: "P " + token,
		PresentationDate: &date, Status: models.TokenCollected, FirstSeenAt: time.Now()}
	id, err := db.Insert(ctx, s.db.Pool, "ledger_registeredtoken", rt)
	if err != nil {
		s.t.Fatal(err)
	}
	paid := time.Now()
	for _, r := range []models.PaymentReceipt{
		{Kind: "SD", TransactionNo: token + "01", Amount: dec("100"), Fields: []byte(fmt.Sprintf(`[["Transaction Number","%s01"]]`, token))},
		{Kind: "RF", TransactionNo: token + "02", Amount: dec("50"), Fields: []byte(`[]`)},
	} {
		r.TokenID, r.PaidAt = id, &paid
		if err := s.st.UpsertReceipt(ctx, &r); err != nil {
			s.t.Fatal(err)
		}
	}
}

func dec(s string) *db.Decimal {
	d, err := db.ParseDecimal(s)
	if err != nil {
		panic(err)
	}
	return &d
}

func xlsxRows(t *testing.T, body string, sheet string) [][]string {
	t.Helper()
	f, err := excelize.OpenReader(bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := f.GetRows(sheet, excelize.Options{RawCellValue: true})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestAccountsListShowsOwnOrgNewestFirst(t *testing.T) {
	s := newSite(t)
	org, other := s.org("Office"), s.org("Other office")
	staff := s.member("staff", org, models.RoleStaff)
	s.registeredToken(org, "T10000001", "Vilayur", 5)
	s.registeredToken(org, "T10000002", "Pattambi", 6)
	s.registeredToken(other, "T10000003", "Vilayur", 6)
	c := s.client().login(staff)

	page := c.get(s.rev("ledger:list")).Body
	i1, i2 := strings.Index(page, "T10000001"), strings.Index(page, "T10000002")
	if i1 < 0 || i2 < 0 || i2 > i1 || strings.Contains(page, "T10000003") {
		t.Errorf("list order/visibility wrong: T1=%d T2=%d other=%v", i1, i2, strings.Contains(page, "T10000003"))
	}
	// place filter and search
	page = c.get(s.rev("ledger:list") + "?place=Vilayur").Body
	if !strings.Contains(page, "T10000001") || strings.Contains(page, "T10000002") {
		t.Errorf("place filter: %v", page)
	}
	page = c.get(s.rev("ledger:list") + "?q=T10000002").Body
	if strings.Contains(page, "T10000001") || !strings.Contains(page, "T10000002") {
		t.Errorf("search wrong")
	}
	// another organization's receipts are hidden
	c.wantStatus(s.rev("ledger:receipts", "T10000003"), 404)
	if r := c.get(s.rev("ledger:receipts", "T10000001") + "?partial=1"); !r.contains("T1000000101") {
		t.Errorf("receipts partial lacks the transaction: %d", r.Status)
	}
	// Excel: two sheets, one row per token
	r := c.get(s.rev("ledger:export") + "?from=2026-10-06")
	f, err := excelize.OpenReader(bytes.NewReader([]byte(r.Body)))
	if err != nil {
		t.Fatal(err)
	}
	if names := f.GetSheetList(); len(names) != 2 || names[0] != "Stamp payment" || names[1] != "Fee payment" {
		t.Fatalf("sheets = %v", names)
	}
	f.Close()
	stamp := xlsxRows(t, r.Body, "Stamp payment")
	if len(stamp) != 2 || stamp[1][0] != "T10000002" {
		t.Errorf("stamp sheet = %v", stamp)
	}
	if fee := xlsxRows(t, r.Body, "Fee payment"); len(fee) != 2 || fee[1][11] != "50" {
		t.Errorf("fee sheet = %v", fee)
	}
	// collecting now is for the admin
	if r := c.post(s.rev("ledger:collect"), nil); r.Status != 403 {
		t.Errorf("collect as staff = %d", r.Status)
	}
}

type paymentFixture struct {
	*site
	org, other           *models.Organization
	staff, staff2, admin *models.User
	c                    *client
}

func newPaymentFixture(t *testing.T) *paymentFixture {
	s := newSite(t)
	f := &paymentFixture{site: s}
	f.org, f.other = s.org("Office"), s.org("Other office")
	f.staff = s.member("staff", f.org, models.RoleStaff)
	f.staff2 = s.member("staff2", f.org, models.RoleStaff)
	f.admin = s.member("boss", f.org, models.RoleAdmin)
	f.c = s.client().login(f.staff)
	return f
}

func (f *paymentFixture) add(c *client, extra map[string]string, files ...filePart) response {
	data := map[string]string{"date": "2026-10-01", "category": "rent", "amount": "8000", "payment_mode": "upi", "description": "October rent"}
	for k, v := range extra {
		data[k] = v
	}
	form := map[string][]string{}
	for k, v := range data {
		form[k] = []string{v}
	}
	return c.postForm(f.rev("ledger:payment_add"), form, files...)
}

func (f *paymentFixture) expenses() (n int, ids []int64) {
	rows, _ := f.db.Pool.Query(context.Background(), `SELECT id FROM ledger_expense WHERE organization_id = ANY($1) ORDER BY id`, []int64{f.org.ID, f.other.ID})
	defer rows.Close()
	for rows.Next() {
		var id int64
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	return len(ids), ids
}

func TestPaymentAddWithPhotoAndPdfBills(t *testing.T) {
	f := newPaymentFixture(t)
	r := f.add(f.c, nil,
		filePart{"bills", "IMG_001.jpg", "image/jpeg", []byte("\xff\xd8\xff fake jpeg")},
		filePart{"bills", "bill.pdf", "application/pdf", []byte("%PDF-1.4 fake")})
	if r.Status != 302 || r.Location != f.rev("ledger:payments") {
		t.Fatalf("add = %d %q\n%s", r.Status, r.Location, r.Body)
	}
	_, ids := f.expenses()
	if len(ids) != 1 {
		t.Fatalf("expenses = %v", ids)
	}
	e, err := f.st.ExpenseByID(context.Background(), f.org.ID, ids[0])
	if err != nil || e == nil {
		t.Fatal(err)
	}
	if e.OrganizationID != f.org.ID || e.CreatedByID == nil || *e.CreatedByID != f.staff.ID || e.Amount.String() != "8000.00" {
		t.Errorf("expense = %+v", e)
	}
	var names []string
	var pdf *models.ExpenseBill
	for i := range e.Bills {
		names = append(names, e.Bills[i].OriginalName)
		if e.Bills[i].OriginalName == "bill.pdf" {
			pdf = &e.Bills[i]
		}
	}
	if len(names) != 2 || pdf == nil {
		t.Fatalf("bills = %v", names)
	}
	if strings.Contains(pdf.File, "bill.pdf") {
		t.Errorf("stored under its own name: %s", pdf.File)
	}
	got := f.c.get(f.rev("ledger:payment_bill", pdf.ID))
	if got.Status != 200 || got.Body != "%PDF-1.4 fake" {
		t.Errorf("bill = %d %q", got.Status, got.Body)
	}
	if _, err := os.Stat(filepath.Join(f.cfg.MediaRoot, pdf.File)); err != nil {
		t.Errorf("bill file not on disk: %v", err)
	}
}

func TestPaymentRejectsOtherFileTypesAndBadAmount(t *testing.T) {
	f := newPaymentFixture(t)
	r := f.add(f.c, nil, filePart{"bills", "notes.exe", "application/octet-stream", []byte("MZ")})
	if r.Status != 200 || !r.contains("only photos") {
		t.Errorf("bad file: %d", r.Status)
	}
	if r := f.add(f.c, map[string]string{"amount": "0"}); !r.contains("above zero") {
		t.Errorf("zero amount accepted")
	}
	if n, _ := f.expenses(); n != 0 {
		t.Errorf("%d expenses saved", n)
	}
}

func TestPaymentsOfOtherOrganizationsAreHidden(t *testing.T) {
	f := newPaymentFixture(t)
	f.add(f.c, nil, filePart{"bills", "a.png", "image/png", []byte("png")})
	_, ids := f.expenses()
	e, _ := f.st.ExpenseByID(context.Background(), f.org.ID, ids[0])
	outsider := f.member("out", f.other, models.RoleAdmin)
	o := f.client().login(outsider)
	o.wantStatus(f.rev("ledger:payment_bill", e.Bills[0].ID), 404)
	o.wantStatus(f.rev("ledger:payment_edit", e.ID), 404)
	if page := o.get(f.rev("ledger:payments")); strings.Contains(page.Body, "October rent") {
		t.Errorf("another organization's payment is listed")
	}
}

func TestPaymentsListFiltersAndTotals(t *testing.T) {
	f := newPaymentFixture(t)
	f.add(f.c, nil)
	f.add(f.c, map[string]string{"category": "electricity", "amount": "1200.50", "date": "2026-10-05", "description": "KSEB"})
	f.add(f.c, map[string]string{"category": "salary", "amount": "15000", "date": "2026-09-30", "description": "Anil"})
	page := f.c.get(f.rev("ledger:payments") + "?from=2026-10-01").Body
	if !strings.Contains(page, "9,200.50") {
		t.Errorf("total missing")
	}
	k, o, a := strings.Index(page, "KSEB"), strings.Index(page, "October rent"), strings.Index(page, "Anil")
	if k < 0 || o < k || a >= 0 {
		t.Errorf("rows: KSEB=%d rent=%d Anil=%d", k, o, a)
	}
	page = f.c.get(f.rev("ledger:payments") + "?category=salary").Body
	if !strings.Contains(page, "Anil") || strings.Contains(page, "KSEB") {
		t.Errorf("category filter wrong")
	}
}

func TestPaymentEditRemovesAndAddsBills(t *testing.T) {
	f := newPaymentFixture(t)
	f.add(f.c, nil, filePart{"bills", "old.jpg", "image/jpeg", []byte("old")})
	_, ids := f.expenses()
	e, _ := f.st.ExpenseByID(context.Background(), f.org.ID, ids[0])
	r := f.c.postForm(f.rev("ledger:payment_edit", e.ID), map[string][]string{
		"date": {"2026-10-01"}, "category": {"rent"}, "amount": {"8500"}, "description": {"October rent"},
		"remove_bill": {fmt.Sprint(e.Bills[0].ID)}},
		filePart{"bills", "new.jpg", "image/jpeg", []byte("new")})
	if r.Status != 302 || r.Location != f.rev("ledger:payments") {
		t.Fatalf("edit = %d %q\n%s", r.Status, r.Location, r.Body)
	}
	e, _ = f.st.ExpenseByID(context.Background(), f.org.ID, e.ID)
	if e.Amount.String() != "8500.00" || len(e.Bills) != 1 || e.Bills[0].OriginalName != "new.jpg" {
		t.Errorf("expense = %+v", e)
	}
}

func TestPaymentDeleteOnlyByAdminOrWhoeverAddedIt(t *testing.T) {
	f := newPaymentFixture(t)
	f.add(f.c, nil)
	_, ids := f.expenses()
	f.client().login(f.staff2).post(f.rev("ledger:payment_delete", ids[0]), nil)
	if n, _ := f.expenses(); n != 1 {
		t.Fatalf("another staff member deleted it")
	}
	f.client().login(f.admin).post(f.rev("ledger:payment_delete", ids[0]), nil)
	if n, _ := f.expenses(); n != 0 {
		t.Errorf("the admin could not delete it")
	}
}

func TestPaymentsExcelAndSidebar(t *testing.T) {
	f := newPaymentFixture(t)
	f.add(f.c, nil)
	f.add(f.c, map[string]string{"category": "telephone", "amount": "499", "description": "BSNL"})
	rows := xlsxRows(t, f.c.get(f.rev("ledger:payments_export")).Body, "Payments")
	want := []string{"Date", "Category", "Details", "Paid by", "Amount (Rs)"}
	if len(rows) != 4 || strings.Join(rows[0][:5], "|") != strings.Join(want, "|") || rows[3][4] != "8499" {
		t.Errorf("rows = %v", rows)
	}
	page := f.c.get(f.rev("ledger:payments"))
	if !page.contains(`id="accountsMenu"`) || !page.contains(f.rev("ledger:list")) {
		t.Errorf("sidebar lacks the accounts menu")
	}
}

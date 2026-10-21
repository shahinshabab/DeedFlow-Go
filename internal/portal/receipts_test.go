package portal

import (
	"testing"
	"time"
)

func str(s string) *string { return &s }

// As the portal's receipt table reads (copied from a live receipt, names changed).
var sdReceipt = []Pair{
	{"Stamp Duty Payment", nil},
	{"Transaction Number", str("620T1000000001")},
	{"Applicant's Name", str("TEST PERSON")},
	{"Sub Registry Office", str("Vilayur")},
	{"GRN", str("KL0300000000202627E")},
	{"Receipt Amount", str("Rs 2000.00/-")},
	{"Bank Reference No", str("IK0TEST")},
	{"Bank CIN", str("00044782026100000001")},
	{"Receipt Date and Time", str("01/10/2026 14:41:55")},
	{"Status", str("Payment Successfully done")},
}

func TestParseReceipt(t *testing.T) {
	loc := time.UTC
	r := ParseReceipt(sdReceipt, loc)
	if r.Title != "Stamp Duty Payment" || r.TransactionNo != "620T1000000001" || r.ApplicantName != "TEST PERSON" ||
		r.SRO != "Vilayur" || r.GRN != "KL0300000000202627E" || r.BankCIN != "00044782026100000001" {
		t.Fatalf("receipt = %+v", r)
	}
	if r.Amount == nil || r.Amount.String() != "2000.00" {
		t.Fatalf("amount = %v", r.Amount)
	}
	if want := time.Date(2026, 10, 1, 14, 41, 55, 0, loc); r.PaidAt == nil || !r.PaidAt.Equal(want) {
		t.Fatalf("paid_at = %v", r.PaidAt)
	}
	if len(r.Fields) != 9 {
		t.Fatalf("fields = %d", len(r.Fields))
	}
}

func TestParseMyTokenKeepsOnlyRegistered(t *testing.T) {
	rows := [][]string{
		{"T10000001", "പേര്", "06-10-2026", "e-Payment", "Registered", "Print"},
		{"T10000002", "പേര്", "06-10-2026", "e-Payment", "Registration Pending", "View Details"},
		{"T10000003", "പേര്", "05-10-2026", "e-Payment", "Registration Fee Pending", ""},
		{"header", "", "", "", "Status"},
	}
	got := ParseMyTokenRows(rows)
	if len(got) != 1 || got[0].Token != "T10000001" || got[0].Name != "പേര്" || got[0].Status != "Registered" ||
		got[0].Date == nil || got[0].Date.Format("2006-01-02") != "2026-10-06" {
		t.Fatalf("rows = %+v", got)
	}
}

func TestParsePresenter(t *testing.T) {
	p := ParsePresenter([]Pair{{"Name:", str("പേര്")}, {"Place:", str("സ്ഥലം")},
		{"Presentation Date & Time:", str("06-10-2026 10:23:00")}, {"Document Amount:", str("1000000")}, {"SRO:", str("Vilayur")}}, time.UTC)
	if p.Place != "സ്ഥലം" || p.SRO != "Vilayur" || p.DocumentAmount == nil || p.DocumentAmount.String() != "1000000" {
		t.Fatalf("presenter = %+v", p)
	}
	if want := time.Date(2026, 10, 6, 10, 23, 0, 0, time.UTC); p.PresentedAt == nil || !p.PresentedAt.Equal(want) {
		t.Fatalf("presented_at = %v", p.PresentedAt)
	}
}

func TestParseAmountAndDates(t *testing.T) {
	if a := ParseAmount("Rs 10,130.00/-"); a == nil || a.String() != "10130.00" {
		t.Fatalf("amount = %v", a)
	}
	if ParseAmount("n/a") != nil || ParseAmount("") != nil {
		t.Fatal("no digits is no amount")
	}
	if ParseDate("31/12/2026") == nil || ParseDate("2026-12-31") != nil {
		t.Fatal("dates are day-first")
	}
}

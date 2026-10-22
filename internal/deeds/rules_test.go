package deeds

import (
	"reflect"
	"testing"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/db"
	"github.com/shahinshabab/deedflow-go/internal/models"
)

// Ports of the payload-level tests of deeds/tests.py (RealPortalRunTests,
// UndividedSharePriceTests); TestPayloadAgainstPython covers the rest of the
// payload builder against Django's own output.

var testToday = time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)

func TestPreflightListsEveryMissingMandatoryPresenterField(t *testing.T) {
	p := &Payload{Presenter: PresenterPayload{Name: "ശാരദ", IDNumber: "527329309630"}}
	got := PreflightProblems(p, []string{"presenter"}, testToday)
	want := []string{"Presenter: age, mobile number, document value, amount for stamp duty and fees missing - the portal requires them."}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("problems = %q", got)
	}
}

func TestPreflightFlagsAnAadhaarNumberWithout12Digits(t *testing.T) {
	aadhaar := IDTypeChoices[0].Value
	p := &Payload{
		Presenter:  PresenterPayload{Name: "ആലി", Age: "64", Mobile: "9999999999", IDType: aadhaar, IDNumber: "223627671678", DocumentValue: "1", DocumentFees: "1"},
		Executants: []ExecutantPayload{{PartyPayload: PartyPayload{Name: "ഇസ്ഹാഖ്", Job: "farmer", IDType: aadhaar, IDNumber: "56697100040"}}},
	}
	got := PreflightProblems(p, []string{"presenter", "executants"}, testToday)
	want := []string{"Executant ഇസ്ഹാഖ്: Aadhaar number 56697100040 has 11 digits - the portal requires 12."}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("problems = %q", got)
	}
}

func TestPreflightFlagsARegisteredPriorDocumentWithoutABookNumber(t *testing.T) {
	p := &Payload{PriorDocuments: []PriorDocPayload{
		{Type: "രജിസ്റ്റർ/ഫയൽ ചെയ്ത", Number: "1487", Year: "2007"},
		{Type: "പട്ടയം", Number: "31", Year: "1978"},
		{Type: "പട്ടയം"},
	}}
	got := PreflightProblems(p, []string{"prior_documents"}, testToday)
	want := []string{
		"Prior document 1487/2007: no book number - the portal requires one for a registered document or will.",
		"Prior document ?/?: no number or year - the portal requires both.",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("problems = %q", got)
	}
}

func TestOutsideKeralaPartyAndOneOldSurveyValue(t *testing.T) {
	for _, c := range []struct {
		pin, district string
		want          bool
	}{{"522001", "ഗുണ്ടൂർ", true}, {"679307", "പാലക്കാട്", false}, {"", "ഗുണ്ടൂർ", true}, {"", "", false}} {
		if got := outsideKerala(c.pin, c.district); got != c.want {
			t.Errorf("outsideKerala(%q, %q) = %v", c.pin, c.district, got)
		}
	}
	if firstSurveyValue("100, 101") != "100" || firstSurveyValue("1, 1B") != "1" {
		t.Errorf("first survey value wrong")
	}
}

func TestPortalPlainTextDropsForbiddenCharacters(t *testing.T) {
	for in, want := range map[string]string{
		"B 2763/215, maddicala colony, Guntur, Andhra Pradesh": "B 2763 215 maddicala colony Guntur Andhra Pradesh",
		"LR6 83758/78": "LR6 83758 78",
	} {
		if got := PortalPlainText(in); got != want {
			t.Errorf("PortalPlainText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBoundaryLeftAsDotsGetsTheOfficeFallback(t *testing.T) {
	if boundary("................") != BoundaryFallback {
		t.Errorf("dots should fall back to %q", BoundaryFallback)
	}
	if got := boundary("............റോഡ്"); got != "റോഡ്" {
		t.Errorf("boundary = %q", got)
	}
}

func TestSurveyValuesAreCutToWhatThePortalAccepts(t *testing.T) {
	var got []string
	for _, v := range []string{"15.....", "...", "1/10", "100, 101", "1 1B"} {
		got = append(got, firstSurveyValue(v))
	}
	if want := []string{"15", "", "1/10", "100", "1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestScheduleNumberIsSentAsAPlainNumber(t *testing.T) {
	item := &models.ScheduleItem{Number: "3/1"}
	if got := schedulePayload(item, "2101").Number; got != "3" {
		t.Errorf("number = %q", got)
	}
}

func TestScheduleNumbersGoInSequence(t *testing.T) {
	var items []SchedulePayload
	for _, n := range []string{"3", "1", "2", "2", "3", "4"} {
		items = append(items, SchedulePayload{Number: n})
	}
	sequentialNumbers(items)
	var got []string
	for _, it := range items {
		got = append(got, it.Number)
	}
	if want := []string{"1", "2", "3", "3", "1", "4"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestAnUndividedShareIsLeftForThePortalToValue(t *testing.T) {
	d := func(s string) *db.Decimal { v, _ := db.ParseDecimal(s); return &v }
	fair := int32(16500)
	item := models.ScheduleItem{FairValue: &fair, TransferHectare: d("0"), TransferAre: d("5"), TransferSqm: d("81"),
		TotalHectare: d("0"), TotalAre: d("5"), TotalSqm: d("81"), UndividedShare: "No"}
	if v, ok := ItemMinimumPrice(&item); !ok || v != 95865 {
		t.Errorf("minimum = %d, %v", v, ok)
	}
	item.UndividedShare = "Yes"
	if _, ok := ItemMinimumPrice(&item); ok {
		t.Errorf("an undivided share has no minimum price")
	}
	p := &Payload{Schedule: []SchedulePayload{{Number: "1", Price: "75000", FairValue: "16500", UndividedShare: "Yes",
		TransferHector: "0", TransferAre: "5", TransferMeter: "81", TotalHector: "0", TotalAre: "5", TotalMeter: "81",
		LandType: "Coastal (belt)", ResurveyNumber: "277"}}}
	if problems := PreflightProblems(p, []string{"schedule"}, testToday); len(problems) != 0 {
		t.Errorf("problems = %q", problems)
	}
}

// --- PlaceholderTests ----------------------------------------------------------

func TestPartyGaps(t *testing.T) {
	party := PartyPayload{Relation: "മകള്‍", IDType: IDTypeChoices[2].Value, IDNumber: "ZZT12"}
	p := party
	fillPartyGaps(&p)
	want := PartyPayload{Age: "18", Mobile: "9999999999", HouseName: "വിലാസം ചേർക്കുക", Job: "തൊഴിൽ ചേർക്കുക",
		Relative: "അച്ഛൻ", Relation: "മകള്‍", IDType: IDTypeChoices[2].Value, IDNumber: "TJO9999999"}
	if p != want {
		t.Errorf("gaps = %+v", p)
	}
	wife := PartyPayload{Relation: "ഭാര്യ"}
	fillPartyGaps(&wife)
	if wife.Relative != "ഭർത്താവ്" || wife.IDType != IDTypeChoices[0].Value || wife.IDNumber != "999999999999" {
		t.Errorf("wife = %+v", wife)
	}
	real := party
	real.IDType, real.IDNumber = IDTypeChoices[4].Value, "M1234567"
	fillPartyGaps(&real)
	if real.IDNumber != "M1234567" {
		t.Errorf("a real passport number was replaced: %q", real.IDNumber)
	}
}

func TestPriorDocumentGapsAreNoted(t *testing.T) {
	doc := PriorDocPayload{Type: "രജിസ്റ്റർ/ഫയൽ ചെയ്ത", Remarks: "ആധാരം"}
	fillPriorDocumentGaps(&doc)
	if doc.Number != "9999" || doc.Year != "1900" || doc.Book != "1" || doc.Remarks != "ആധാരം പി ആര് ശെരിയാക്കുക" {
		t.Errorf("doc = %+v", doc)
	}
	whole := PriorDocPayload{Type: "പട്ടയം", Number: "31", Year: "1978"}
	fillPriorDocumentGaps(&whole)
	if whole.Remarks != "" {
		t.Errorf("a complete document got a note: %q", whole.Remarks)
	}
}

// gapPayload is a payload whose parties and presenter are complete, so a test
// can look at the schedule alone.
func gapPayload(schedule ...SchedulePayload) *Payload {
	return &Payload{Presenter: PresenterPayload{Age: "1", Mobile: "1", HouseName: "x", Job: "x", Relative: "x", IDNumber: "999999999999"},
		Schedule: schedule}
}

func TestMoneyGaps(t *testing.T) {
	p := gapPayload(SchedulePayload{ResurveyNumber: "1", TransferHector: "0", TransferAre: "4", TransferMeter: "26",
		TotalHector: "0", TotalAre: "4", TotalMeter: "26"})
	FillGaps(p)
	if p.Schedule[0].FairValue != "1000" || p.Schedule[0].Price != "4260" {
		t.Errorf("fair value/price = %q/%q", p.Schedule[0].FairValue, p.Schedule[0].Price)
	}
	if p.Presenter.DocumentValue != "4260" || p.Presenter.DocumentFees != "4260" {
		t.Errorf("document value/fees = %q/%q", p.Presenter.DocumentValue, p.Presenter.DocumentFees)
	}
}

// --- OldSurveyRowTests (payload side) -------------------------------------------

func scheduleItem(number, resurvey, sqm, price string) SchedulePayload {
	return SchedulePayload{Number: number, ResurveyNumber: resurvey, FairValue: "1000", Price: price, LandType: "x",
		UndividedShare: "No", TransferHector: "0", TransferAre: "0", TransferMeter: sqm, TotalHector: "0", TotalAre: "0", TotalMeter: sqm}
}

func TestExtraRowsGetNoResurveyPlaceholderOrPriceCheck(t *testing.T) {
	schedule := []SchedulePayload{scheduleItem("1", "343", "21", "500"), scheduleItem("1", "", "0", "0"), scheduleItem("2", "", "10", "")}
	if got := ContinuationRows(schedule); !reflect.DeepEqual(got, map[int]bool{1: true}) {
		t.Errorf("continuation rows = %v", got)
	}
	p := gapPayload(schedule...)
	FillGaps(p)
	var got []string
	for _, it := range p.Schedule {
		got = append(got, it.ResurveyNumber)
	}
	if want := []string{"343", "", "9999"}; !reflect.DeepEqual(got, want) {
		t.Errorf("resurvey numbers = %q", got)
	}
	if p.Schedule[1].Price != "0" {
		t.Errorf("an extra row's price = %q", p.Schedule[1].Price)
	}
	if problems := PreflightProblems(p, []string{"schedule"}, testToday); len(problems) != 0 {
		t.Errorf("problems = %q", problems)
	}
}

func TestOldSurveyOnlyGetsNoResurveyPlaceholder(t *testing.T) {
	for _, c := range []struct{ block, want string }{{"", ""}, {"22", "9999"}} {
		it := scheduleItem("1", "", "21", "")
		it.ResurveyBlock, it.OldSurvey = c.block, "66"
		p := gapPayload(it)
		FillGaps(p)
		if got := p.Schedule[0].ResurveyNumber; got != c.want {
			t.Errorf("block %q: resurvey number %q, want %q", c.block, got, c.want)
		}
		if problems := PreflightProblems(p, []string{"schedule"}, testToday); len(problems) != 0 {
			t.Errorf("block %q: problems = %q", c.block, problems)
		}
	}
}

func TestResurveyInWordsIsSentEmpty(t *testing.T) {
	for _, words := range []string{"ഇല്ല", "Nil", "0"} {
		item := &models.ScheduleItem{Number: "1", ResurveyBlock: "ഇല്ല", ResurveyNumber: words, ResurveySub: words, OldSurvey: "66", OldSub: "1"}
		p := schedulePayload(item, "1")
		if got := []string{p.ResurveyBlock, p.ResurveyNumber, p.ResurveySub, p.OldSurvey, p.OldSub}; !reflect.DeepEqual(got, []string{"", "", "", "66", "1"}) {
			t.Errorf("%q: %q", words, got)
		}
	}
	item := &models.ScheduleItem{Number: "1", ResurveyBlock: "22", ResurveyNumber: "343", ResurveySub: "2/1"}
	if got := schedulePayload(item, "1").ResurveyNumber; got != "343" {
		t.Errorf("a real resurvey number = %q", got)
	}
}

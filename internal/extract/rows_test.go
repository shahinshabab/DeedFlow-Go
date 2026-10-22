package extract

import (
	"reflect"
	"strings"
	"testing"
)

// Ports of the table and extraction tests in deeds/tests.py
// (RealDeedExtractionTests, OldSurveyRowTests, PlaceholderTests); the golden
// test in table_test.go compares whole schedules with Django's.

func sp_(s string) *string { return &s }

func TestOldSurveySubdivisionLetterIsNotAParcel(t *testing.T) {
	for in, want := range map[string]string{"1 B": "1B", "1 1 B": "1, 1B"} {
		if got := joinValues(customParse(sp_(in))); got != want {
			t.Errorf("customParse(%q) = %q, want %q", in, got, want)
		}
	}
	rows := make(Table, 21)
	for i := range rows {
		rows[i] = []*string{sp_(string(rune('0' + (i+1)%10))), sp_(""), nil, nil, sp_("")}
	}
	rows[8][4], rows[9][4], rows[10][4], rows[11][4], rows[12][4] = sp_("83"), sp_("1/10"), sp_("100 101"), sp_("1 1 B"), sp_("8.62 BÀ")
	ex := NewTableExtractor()
	expanded := ex.expandTable(ex.parseRawTable(rows))
	if len(expanded) != 1 {
		t.Fatalf("%d tables", len(expanded))
	}
	row := expanded[0]
	got := []string{sval(row[8][4]), sval(row[10][4]), sval(row[11][4]), sval(row[12][4])}
	if want := []string{"83", "100, 101", "1, 1B", "8.62"}; !reflect.DeepEqual(got, want) {
		t.Errorf("cells = %q, want %q", got, want)
	}
}

func TestAreaSpelledOutWhenTheFigureIsBlank(t *testing.T) {
	if v, ok := areaFromWords("............ ആർ നാല് പോയിൻറ് രണ്ട് അഞ്ച് ആർ ഇതുകളിൽ"); !ok || v != 4.25 {
		t.Errorf("area = %v, %v", v, ok)
	}
	if _, ok := areaFromWords("ഇരുപത്തി എട്ട് പോയിൻറ് ആറ് മൂന്ന് ആർ"); ok {
		t.Errorf("a compound number must not be read")
	}
}

func TestDescriptionStopsBeforeTheMeasurements(t *testing.T) {
	got := description("വെളളാടത്ത് പറമ്പിൽ തെക്കു കിഴക്കെമൂല\nമീറ്റർ അളവിൽ കിഴക്ക് 4.79 വടക്ക് 4.35")
	if got != "വെളളാടത്ത് പറമ്പിൽ തെക്കു കിഴക്കെമൂല" {
		t.Errorf("description = %q", got)
	}
}

func TestLandTypeFromPanchayathRoadWording(t *testing.T) {
	text := "പട്ടിക വഹകൾക്ക് Residential Plot with Panchayath road access ൽ 1 ആറിന് " + strings.Repeat("ഫെയർവാല്യു ", 200)
	if got := ExtractLandType(text); got != "Residential plot with Corp.Mun.Panch. road access" {
		t.Errorf("land type = %q", got)
	}
}

// --- OldSurveyRowTests -------------------------------------------------------------

func surveyTable(oldSurvey, oldSub, area string) Table {
	rows := make(Table, 21)
	for i := range rows {
		rows[i] = []*string{sp_(string(rune('0' + (i+1)%10))), sp_(""), nil, nil, sp_("")}
	}
	rows[0][4] = sp_("3")
	for i, v := range []string{"22", "343", "2/1", oldSurvey, oldSub, area} {
		rows[7+i][4] = sp_(v)
	}
	return rows
}

type surveyRow struct {
	Number, Block, Resurvey, Old, Sub, Sqm string
	PriceSet                               bool
}

func surveyRows(entries []Entry) []surveyRow {
	var out []surveyRow
	for _, e := range entries {
		out = append(out, surveyRow{e.Number, e.ResurveyBlock, e.ResurveyNumber, e.OldSurvey, e.OldSub, e.TotalSqm.String(), e.PriceSet})
	}
	return out
}

func TestOneOldSurveyWithTwoSubDivisions(t *testing.T) {
	entries := NewTableExtractor().ExtractSchedule([]Table{surveyTable("66", "1 2", "0.21 BÀ")})
	want := []surveyRow{{"3", "22", "343", "66", "1", "21", false}, {"3", "", "", "66", "2", "0", true}}
	if got := surveyRows(entries); !reflect.DeepEqual(got, want) {
		t.Errorf("rows = %+v", got)
	}
	if entries[0].SurveyMapping != nil {
		t.Errorf("a clear pairing needs no mapping")
	}
}

func TestTwoOldSurveysEachTakeTheirOwnArea(t *testing.T) {
	entries := NewTableExtractor().ExtractSchedule([]Table{surveyTable("66 67", "1 2", "0.21 BÀ 0.10 BÀ")})
	want := []surveyRow{{"3", "22", "343", "66", "1", "21", false}, {"3", "", "", "67", "2", "10", true}}
	if got := surveyRows(entries); !reflect.DeepEqual(got, want) {
		t.Errorf("rows = %+v", got)
	}
	if entries[0].SurveyMapping != nil {
		t.Errorf("a clear pairing needs no mapping")
	}
}

func TestUnclearPairingIsLeftToTheUser(t *testing.T) {
	entries := NewTableExtractor().ExtractSchedule([]Table{surveyTable("66 67", "1 2 4", "0.21 BÀ")})
	var got [][2]string
	for _, e := range entries {
		got = append(got, [2]string{e.OldSurvey, e.OldSub})
	}
	if want := [][2]string{{"66", "1"}, {"67", "2"}, {"67", "4"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("pairs = %v", got)
	}
	want := &SurveyMapping{Count: 3, OldSurveys: []string{"66", "67"}, Subs: []string{"1", "2", "4"}, Assignment: []int{0, 1, 1}, Areas: []float64{0.21}}
	if !reflect.DeepEqual(entries[0].SurveyMapping, want) {
		t.Errorf("mapping = %+v", entries[0].SurveyMapping)
	}
}

func TestNoResurveyLeavesTheResurveyFieldsEmpty(t *testing.T) {
	// ഇല്ല (raw legacy font "CÃ" or Unicode), Nil, a dash, 0 or nothing: land
	// with only an old survey - no block, no resurvey numbers.
	for _, none := range []string{"CÃ", "ഇല്ല", "Nil", "NA", "-", "0", ""} {
		table := surveyTable("66", "1", "0.21 BÀ")
		table[7][4], table[8][4], table[9][4] = sp_(none), sp_(none), sp_(none)
		e := NewTableExtractor().ExtractSchedule([]Table{table})[0]
		got := []string{e.ResurveyBlock, e.ResurveyNumber, e.ResurveySub, e.OldSurvey, e.OldSub, e.TotalSqm.String()}
		if want := []string{"", "", "", "66", "1", "21"}; !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %q", none, got)
		}
	}
}

func TestNoResurveyNumberDropsTheBlockToo(t *testing.T) {
	table := surveyTable("66", "1", "0.21 BÀ")
	table[8][4] = sp_("CÃ")
	e := NewTableExtractor().ExtractSchedule([]Table{table})[0]
	if e.ResurveyBlock != "" || e.ResurveyNumber != "" || e.ResurveySub != "" {
		t.Errorf("resurvey = %q %q %q", e.ResurveyBlock, e.ResurveyNumber, e.ResurveySub)
	}
}

func TestResurveyWithoutASubDivisionKeepsItsNumber(t *testing.T) {
	table := surveyTable("66", "1", "0.21 BÀ")
	table[9][4] = sp_("CÃ")
	e := NewTableExtractor().ExtractSchedule([]Table{table})[0]
	if e.ResurveyBlock != "22" || e.ResurveyNumber != "343" || e.ResurveySub != "" {
		t.Errorf("resurvey = %q %q %q", e.ResurveyBlock, e.ResurveyNumber, e.ResurveySub)
	}
}

func TestDraftDotsStayAGap(t *testing.T) {
	table := surveyTable("66", "1", "0.21 BÀ")
	table[7][4], table[8][4] = sp_("......"), sp_("......")
	e := NewTableExtractor().ExtractSchedule([]Table{table})[0]
	if e.ResurveyBlock != "......" || e.ResurveyNumber != "......" {
		t.Errorf("resurvey = %q %q", e.ResurveyBlock, e.ResurveyNumber)
	}
}

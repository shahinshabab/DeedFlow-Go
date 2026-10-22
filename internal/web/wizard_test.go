package web

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/db"
	"github.com/shahinshabab/deedflow-go/internal/deeds"
	"github.com/shahinshabab/deedflow-go/internal/models"
	"github.com/shahinshabab/deedflow-go/internal/pipeline"
	"github.com/shahinshabab/deedflow-go/internal/store"
)

// Ports of SectionOrderTests and test_mapping_page_rebuilds_the_schedule_rows
// (deeds/tests.py): the wizard's move up / down and Clone post their entries'
// order, and that's the order they're saved, shown and sent in.

type wizardFixture struct {
	*site
	user *models.User
	deed *models.Deed
	c    *client
	url  string
}

func newWizardFixture(t *testing.T) *wizardFixture {
	s := newSite(t)
	org := s.org("Test office")
	user := s.member("tester", org, models.RoleAdmin)
	f := &wizardFixture{site: s, user: user, c: s.client().login(user)}
	// The first seed deed with two executants.
	for i := 0; i < 3; i++ {
		d, err := pipeline.SeedTestDeed(context.Background(), s.st, user.ID, i, deeds.Today(time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		if data, _ := s.st.LoadDeedData(context.Background(), d); f.deed == nil && len(data.Executants) == 2 {
			f.deed = d
		}
	}
	if f.deed == nil {
		t.Fatal("no seeded deed with two executants")
	}
	f.url = s.rev("deeds:wizard", f.deed.PublicID)
	return f
}

func (f *wizardFixture) data() *deeds.DeedData {
	f.t.Helper()
	d, err := f.st.DeedByID(context.Background(), f.deed.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	data, err := f.st.LoadDeedData(context.Background(), d)
	if err != nil {
		f.t.Fatal(err)
	}
	return data
}

func (f *wizardFixture) names() []string {
	var out []string
	for _, e := range f.data().Executants {
		out = append(out, e.Name)
	}
	return out
}

// page is what the wizard shows for a step, as the form a browser would send.
func (f *wizardFixture) page(step string) url.Values {
	f.t.Helper()
	r := f.c.get(f.url + "?step=" + step)
	if r.Status != 200 {
		f.t.Fatalf("GET step %s = %d", step, r.Status)
	}
	return formValues(f.t, r.Body, step+"-")
}

// post sends the page as shown with its entries in `rows` order (form
// indices), plus extra entries; Save & close or Next.
func (f *wizardFixture) post(step string, page url.Values, rows []int, extra []url.Values, closeToo bool) response {
	f.t.Helper()
	form := map[string]string{
		fmt.Sprintf("deed_wizard_%s-current_step", f.deed.PublicID): step,
		step + "-ROWS": strings.Trim(strings.Join(strings.Fields(fmt.Sprint(rows)), ","), "[]"),
	}
	if closeToo {
		form["save_and_close"] = "1"
	}
	// Only the saved entries (the page also offers a blank one) plus the extras.
	total := intFrom(page.Get(step + "-INITIAL_FORMS"))
	for k, v := range page {
		if len(v) == 0 {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(strings.TrimPrefix(k, step+"-"), "%d-", &n); err == nil && n >= total {
			continue
		}
		form[k] = v[len(v)-1]
	}
	for i, row := range extra {
		for k, v := range row {
			form[fmt.Sprintf("%s-%d-%s", step, total+i, k)] = v[0]
		}
	}
	form[step+"-TOTAL_FORMS"] = fmt.Sprint(total + len(extra))
	form[step+"-ROWS"] = strings.Trim(strings.Join(strings.Fields(fmt.Sprint(rows)), ","), "[]")
	return f.c.post(f.url, form)
}

func intFrom(s string) int {
	var n int
	fmt.Sscan(s, &n)
	return n
}

// rowFields are the fields of entry i of a page, without the prefix.
func rowFields(page url.Values, step string, i int) url.Values {
	out := url.Values{}
	prefix := fmt.Sprintf("%s-%d-", step, i)
	for k, v := range page {
		if name, ok := strings.CutPrefix(k, prefix); ok && name != "id" && name != "deed" && name != "DELETE" && v[0] != "" {
			out[name] = v
		}
	}
	return out
}

func TestMovedEntriesAreSavedAndSentInTheirNewOrder(t *testing.T) {
	f := newWizardFixture(t)
	before := f.names()
	r := f.post("executants", f.page("executants"), []int{1, 0}, nil, true)
	if r.Status != 302 {
		t.Fatalf("save = %d\n%.300s", r.Status, r.Body)
	}
	if got := f.names(); got[0] != before[1] || got[1] != before[0] {
		t.Errorf("order after the move = %q, was %q", got, before)
	}
	p, _ := deeds.BuildPayload(f.data())
	if p.Executants[0].Name != before[1] || p.Executants[1].Name != before[0] {
		t.Errorf("payload order = %q %q", p.Executants[0].Name, p.Executants[1].Name)
	}
	page := f.page("executants")
	if page.Get("executants-0-name") != before[1] || page.Get("executants-1-name") != before[0] {
		t.Errorf("the page shows %q, %q", page.Get("executants-0-name"), page.Get("executants-1-name"))
	}
}

func TestCloneIsSavedWhereItWasPlaced(t *testing.T) {
	f := newWizardFixture(t)
	before := f.names()
	page := f.page("executants")
	copy := rowFields(page, "executants", 0)
	copy.Set("name", "പകർപ്പ്")
	if r := f.post("executants", page, []int{0, 2, 1}, []url.Values{copy}, true); r.Status != 302 {
		t.Fatalf("save = %d", r.Status)
	}
	got := f.names()
	if want := []string{before[0], "പകർപ്പ്", before[1]}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("order = %q, want %q", got, want)
	}
}

func TestCloneIsntSavedAgainAfterBackAndNext(t *testing.T) {
	f := newWizardFixture(t)
	before := f.names()
	page := f.page("executants")
	copy := rowFields(page, "executants", 0)
	copy.Set("name", "പകർപ്പ്")
	r := f.post("executants", page, []int{0, 2, 1}, []url.Values{copy}, false)
	if r.Status != 200 || !strings.Contains(r.Body, `value="claimants"`) {
		t.Fatalf("Next should show the claimants step (%d)", r.Status)
	}
	back := f.c.post(f.url, map[string]string{
		fmt.Sprintf("deed_wizard_%s-current_step", f.deed.PublicID): "claimants", "wizard_goto_step": "executants"})
	shown := formValues(t, back.Body, "executants-")
	want := []string{before[0], "പകർപ്പ്", before[1]}
	for i, name := range want {
		if shown.Get(fmt.Sprintf("executants-%d-name", i)) != name {
			t.Errorf("entry %d on the page = %q, want %q", i, shown.Get(fmt.Sprintf("executants-%d-name", i)), name)
		}
	}
	r = f.post("executants", shown, []int{0, 1, 2}, nil, false)
	if r.Status != 200 || !strings.Contains(r.Body, `value="claimants"`) {
		t.Fatalf("Next again = %d", r.Status)
	}
	if got := f.names(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("order = %q, want %q (the clone was saved twice?)", got, want)
	}
}

func TestMovedSchedulesGoInNumberedInTheirNewOrder(t *testing.T) {
	f := newWizardFixture(t)
	data := f.data()
	if len(data.Schedule) != 2 {
		t.Skipf("this seed deed has %d schedule items", len(data.Schedule))
	}
	first, second := data.Schedule[0].ResurveyNumber, data.Schedule[1].ResurveyNumber
	r := f.post("schedule", f.page("schedule"), []int{1, 0}, nil, true)
	if r.Status != 302 {
		t.Fatalf("save = %d", r.Status)
	}
	p, _ := deeds.BuildPayload(f.data())
	got := fmt.Sprint(p.Schedule[0].Number, p.Schedule[0].ResurveyNumber, p.Schedule[1].Number, p.Schedule[1].ResurveyNumber)
	if want := fmt.Sprint("1", second, "2", first); got != want {
		t.Errorf("schedule = %v, want %v", got, want)
	}
}

func TestMappingPageRebuildsTheScheduleRows(t *testing.T) {
	s := newSite(t)
	org := s.org("Office M")
	user := s.member("mapper", org, models.RoleStaff)
	c := s.client().login(user)
	ctx := context.Background()
	deed := &models.Deed{OwnerID: user.ID, Status: models.StatusReadyForReview}
	if err := s.st.CreateDeed(ctx, deed); err != nil {
		t.Fatal(err)
	}
	add := func(item models.ScheduleItem) {
		item.DeedID = deed.ID
		if err := s.st.InsertRow(ctx, store.RowTable["schedule"], &item); err != nil {
			t.Fatal(err)
		}
	}
	d := func(v string) *db.Decimal { x, _ := db.ParseDecimal(v); return &x }
	price := func(n int32) *int32 { return &n }
	add(models.ScheduleItem{Number: "1", ResurveyNumber: "10", OldSurvey: "5", OldSub: "1", TotalSqm: d("50"), Price: price(900)})
	add(models.ScheduleItem{Number: "2", ResurveyBlock: "22", ResurveyNumber: "343", OldSurvey: "66", OldSub: "1", TotalSqm: d("21"), Price: price(500)})
	add(models.ScheduleItem{Number: "2", OldSurvey: "66", OldSub: "2", TotalSqm: d("0"), Price: price(0)})
	add(models.ScheduleItem{Number: "2", OldSurvey: "67", OldSub: "4", TotalSqm: d("0"), Price: price(0)})
	deed.SurveyMapping = []models.SurveyMapping{{Start: 1, Count: 3, Number: "2", OldSurveys: []string{"66", "67"},
		Subs: []string{"1", "2", "4"}, Assignment: []int{0, 1, 1}, Areas: []float64{0.21}}}
	if err := s.st.SaveDeed(ctx, deed, "survey_mapping"); err != nil {
		t.Fatal(err)
	}

	r := c.get(s.rev("deeds:wizard", deed.PublicID))
	if r.Status != 302 || r.Location != s.rev("deeds:survey_mapping", deed.PublicID) {
		t.Errorf("wizard = %d %q, want a redirect to the mapping page", r.Status, r.Location)
	}
	if page := c.get(s.rev("deeds:survey_mapping", deed.PublicID)); !page.contains("Schedule 2") {
		t.Errorf("mapping page lacks the schedule number")
	}
	r = c.post(s.rev("deeds:survey_mapping", deed.PublicID), map[string]string{"map-0-0": "0", "map-0-1": "0", "map-0-2": "1"})
	if r.Status != 302 || r.Location != s.rev("deeds:wizard", deed.PublicID) {
		t.Fatalf("mapping post = %d %q", r.Status, r.Location)
	}
	got, _ := s.st.DeedByID(ctx, deed.ID)
	if len(got.SurveyMapping) != 0 {
		t.Errorf("mapping not cleared: %+v", got.SurveyMapping)
	}
	data, _ := s.st.LoadDeedData(ctx, got)
	var rows []string
	for _, it := range data.Schedule {
		p := int32(-1)
		if it.Price != nil {
			p = *it.Price
		}
		rows = append(rows, fmt.Sprintf("%s/%s/%s/%s/%d", it.Number, it.ResurveyNumber, it.OldSurvey, it.OldSub, p))
	}
	want := "1/10/5/1/900|2/343/66/1/500|2//66/2/0|2//67/4/0"
	if strings.Join(rows, "|") != want {
		t.Errorf("rows = %s, want %s", strings.Join(rows, "|"), want)
	}
}

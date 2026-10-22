package web

import (
	"context"
	"encoding/json"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/deeds"
	"github.com/shahinshabab/deedflow-go/internal/models"
	"github.com/shahinshabab/deedflow-go/internal/pipeline"
)

// Ports of PortalDropdownTests (deeds/tests.py). They replace the scraped
// options, so they run against a database of their own.

const scrapeJSON = `{
 "scraped_at": "2026-10-02T04:05:09+00:00",
 "pages": {"registration": {"fields": {"ddDistrict": {"label": "District"}}, "rows": [
  {"field": "ddDistrict", "parents": [], "options": [["01", "തിരുവനന്തപുരം"], ["03", "പത്തനംതിട്ട"]]},
  {"field": "ddSro", "parents": [["ddDistrict", "01", "തിരുവനന്തപുരം"]], "options": [["0102", "ആറ്റിങ്ങൽ"]]},
  {"field": "ddSro", "parents": [["ddDistrict", "03", "പത്തനംതിട്ട"]], "options": [["0302", "കോന്നി"]]},
  {"field": "ddTaluk", "parents": [["ddDistrict", "01", "തിരുവനന്തപുരം"], ["ddSro", "0102", "ആറ്റിങ്ങൽ"]],
   "options": [["05", "ചിറയിന്‍കീഴ്"], ["06", "വര്‍ക്കല"]]},
  {"field": "ddTaluk", "parents": [["ddDistrict", "03", "പത്തനംതിട്ട"], ["ddSro", "0302", "കോന്നി"]], "options": [["09", "കോന്നി"]]},
  {"field": "selddbookno", "parents": [], "options": [["1", "1 - Immovable Property"], ["3", "3 - Will"]]},
  {"field": "selddMainTtype", "parents": [["selddbookno", "1", "1 - Immovable Property"]], "options": [["0700", "വിലയാധാരം"], ["1200", "ദാനം"]]},
  {"field": "selddTtype", "parents": [["selddbookno", "1", "1 - Immovable Property"], ["selddMainTtype", "1200", "ദാനം"]],
   "options": [["1209", "ദാനം - കുടുംബാംഗത്തിന്‍ പേരിലുള്ളത് - Book-I--1209"], ["1201", "ദാനം--1201"]]},
  {"field": "selddTtype", "parents": [["selddbookno", "1", "1 - Immovable Property"], ["selddMainTtype", "0700", "വിലയാധാരം"]],
   "options": [["0702", "വിലയാധാരം - (തീറാധാരം)--0702"]]}
 ]}}}`

func (s *site) importScrape(js string) {
	s.t.Helper()
	var f deeds.ScrapeFile
	if err := json.Unmarshal([]byte(js), &f); err != nil {
		s.t.Fatal(err)
	}
	if _, err := s.st.ImportScrape(context.Background(), &f, nil); err != nil {
		s.t.Fatal(err)
	}
}

func (s *site) optionTexts(page, field string, known map[string]string) ([]string, bool) {
	return (&deeds.Dropdowns{Src: s.st}).OptionTexts(context.Background(), page, field, known)
}

func TestDropdownLookupFollowsTheCascade(t *testing.T) {
	s := newIsolatedSite(t)
	s.importScrape(scrapeJSON)
	if got, _ := s.optionTexts("registration", "ddSro", map[string]string{"ddDistrict": "പത്തനംതിട്ട"}); !reflect.DeepEqual(got, []string{"കോന്നി"}) {
		t.Errorf("SROs of Pathanamthitta = %v", got)
	}
	if got, ok := s.optionTexts("registration", "ddSro", map[string]string{}); len(got) != 0 || !ok {
		t.Errorf("no parent chosen yet should list nothing, got %v (known=%v)", got, ok)
	}
	if got, ok := s.optionTexts("registration", "nothing_scraped", nil); got != nil || ok {
		t.Errorf("an unscraped field = %v, %v", got, ok)
	}
	// Chillu letters match in either Unicode spelling.
	got, _ := s.optionTexts("registration", "ddTaluk", map[string]string{"ddDistrict": "തിരുവനന്തപുരം", "ddSro": "ആറ്റിങ്ങല്‍"})
	if !reflect.DeepEqual(got, []string{"ചിറയിന്‍കീഴ്", "വര്‍ക്കല"}) {
		t.Errorf("taluks = %v", got)
	}
}

func TestDropdownParentTheWizardHasNoFieldForIsIgnored(t *testing.T) {
	s := newIsolatedSite(t)
	s.importScrape(`{"pages": {"executant": {"rows": [
	 {"field": "district", "parents": [], "options": [["01", "തിരുവനന്തപുരം"]]},
	 {"field": "thaluk", "parents": [["district_per", "02", "കൊല്ലം"], ["district", "01", "തിരുവനന്തപുരം"]], "options": [["04", "നെയ്യാറ്റിന്‍കര"]]}]}}}`)
	got, _ := s.optionTexts("executant", "thaluk", map[string]string{"district": "തിരുവനന്തപുരം"})
	if !reflect.DeepEqual(got, []string{"നെയ്യാറ്റിന്‍കര"}) {
		t.Errorf("taluks = %v", got)
	}
}

func TestDropdownAjaxEndpoint(t *testing.T) {
	s := newIsolatedSite(t)
	s.importScrape(scrapeJSON)
	c := s.client().login(s.user("tester"))
	q := url.Values{"page": {"registration"}, "field": {"selddTtype"},
		"p.selddbookno": {"1 - Immovable Property"}, "p.selddMainTtype": {"വിലയാധാരം"}}
	r := c.get(s.rev("deeds:ajax_portal_options") + "?" + q.Encode())
	var out struct{ Options []string }
	if err := json.Unmarshal([]byte(r.Body), &out); err != nil || !reflect.DeepEqual(out.Options, []string{"വിലയാധാരം - (തീറാധാരം)--0702"}) {
		t.Errorf("options = %s (%v)", r.Body, err)
	}
}

func TestRescrapeReplacesAPage(t *testing.T) {
	s := newIsolatedSite(t)
	s.importScrape(scrapeJSON)
	s.importScrape(`{"pages": {"registration": {"rows": [{"field": "ddDistrict", "parents": [], "options": [["01", "തിരുവനന്തപുരം"]]}]}}}`)
	var n int
	if err := s.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM deeds_portaldropdown WHERE page = 'registration'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("rows after a rescrape = %d (%v)", n, err)
	}
}

// The wizard's choice is what gets submitted; a sub doc type that doesn't
// belong to the chosen type is refused.
func TestWizardDeedTypeChoiceIsWhatGetsSubmitted(t *testing.T) {
	s := newIsolatedSite(t)
	s.importScrape(scrapeJSON)
	org := s.org("Office")
	admin := s.member("tester", org, models.RoleAdmin)
	deed, err := pipeline.SeedTestDeed(context.Background(), s.st, admin.ID, 0, deeds.Today(time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	c := s.client().login(admin)
	post := func(subType string) response {
		return c.post(s.rev("deeds:wizard", deed.PublicID)+"?step=registration", map[string]string{
			"registration-district": "പത്തനംതിട്ട", "registration-sro": "കോന്നി", "registration-taluk": "കോന്നി",
			"registration-book": "1", "registration-deed_type": "വിലയാധാരം", "registration-deed_sub_type": subType,
			"deed_wizard_" + deed.PublicID + "-current_step": "registration"})
	}
	r := post("വിലയാധാരം - (തീറാധാരം)--0702")
	if r.Status != 200 {
		t.Fatalf("a valid choice should move on: %d\n%.300s", r.Status, r.Body)
	}
	got, _ := s.st.DeedByID(context.Background(), deed.ID)
	data, err := s.st.LoadDeedData(context.Background(), got)
	if err != nil {
		t.Fatal(err)
	}
	if data.Registration == nil || data.Registration.DeedSubType != "വിലയാധാരം - (തീറാധാരം)--0702" {
		t.Fatalf("registration = %+v", data.Registration)
	}
	p, err := deeds.BuildPayload(data)
	if err != nil {
		t.Fatal(err)
	}
	if code := p.Schedule[0].TransactionTypeCode; code != "0702" {
		t.Errorf("transaction type code = %q, want 0702", code)
	}
	if p.Registration.RegistrationType != "വിലയാധാരം" || p.Registration.RegistrationSubType != "വിലയാധാരം - (തീറാധാരം)--0702" {
		t.Errorf("payload registration = %+v", p.Registration)
	}
	// Neither a sub doc type of another type nor a blank one is saved.
	for _, bad := range []string{"ദാനം--1201", ""} {
		post(bad)
		got, _ = s.st.DeedByID(context.Background(), deed.ID)
		data, _ = s.st.LoadDeedData(context.Background(), got)
		if data.Registration.DeedSubType != "വിലയാധാരം - (തീറാധാരം)--0702" {
			t.Errorf("sub doc type %q replaced the valid one: %q", bad, data.Registration.DeedSubType)
		}
	}
}

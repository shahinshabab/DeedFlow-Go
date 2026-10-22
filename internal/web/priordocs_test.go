package web

import (
	"context"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/ai"
	"github.com/shahinshabab/deedflow-go/internal/deeds"
	"github.com/shahinshabab/deedflow-go/internal/models"
	"github.com/shahinshabab/deedflow-go/internal/pipeline"
	"github.com/shahinshabab/deedflow-go/internal/store"
)

// Ports of PriorDocumentMatchTests (deeds/tests.py): the PR document
// dropdowns match the AI's (or our) wording against the portal's own option
// text, which uses older chillu spellings and stray spaces.

const priorScrape = `{"pages": {"prior_document": {"rows": [
 {"field": "documentType", "parents": [],
  "options": [["C", " കോടതി ഉത്തരവ് "], ["P", "പട്ടയം"], ["D", "രജിസ്റ്റര്‍ / ഫയല്‍ ചെയ്ത"], ["W", "വില്‍"]]},
 {"field": "district", "parents": [["documentType", "D", "രജിസ്റ്റര്‍ / ഫയല്‍ ചെയ്ത"]], "options": [["09", "പാലക്കാട്"], ["10", "മലപ്പുറം"]]},
 {"field": "district", "parents": [["documentType", "W", "വില്‍"]], "options": [["09", "പാലക്കാട്"]]},
 {"field": "sro", "parents": [["documentType", "D", "രജിസ്റ്റര്‍ / ഫയല്‍ ചെയ്ത"], ["district", "09", "പാലക്കാട്"]],
  "options": [["0912", "വിളയൂര്‍"], ["0913", "പട്ടാമ്പി"]]}]}}}`

func TestPriorDocumentTypeInOtherWordsMapsToAChoice(t *testing.T) {
	const registered = "രജിസ്റ്റർ/ഫയൽ ചെയ്ത"
	for in, want := range map[string]string{
		registered: registered,
		"രജിസ്റ്റര്‍ / ഫയല്‍ ചെയ്ത (Registered/Filed)": registered,
		"Registered/Filed": registered,
		"രജിസ്റ്റർ ഭാഗപത്രം":      registered,
		"വിലയാധാരം":               registered,
		"വില്‍":                   "വിൽ",
		"ഒസ്യത്ത്":                "വിൽ",
		"ലാൻഡ് ട്രിബ്യൂണൽ പട്ടയം": "പട്ടയം",
		"കോടതി വിധി":              "കോടതി ഉത്തരവ്",
		"???":                     "",
	} {
		if got := deeds.MatchPriorDocumentType(in, false); got != want {
			t.Errorf("match(%q) = %q, want %q", in, got, want)
		}
	}
	if got := deeds.MatchPriorDocumentType("???", true); got != registered {
		t.Errorf("a cited SRO/book makes it a registered document, got %q", got)
	}
}

func TestPriorDocumentCascadeFindsThePortalSpellingOfTheType(t *testing.T) {
	s := newIsolatedSite(t)
	s.importScrape(priorScrape)
	for _, c := range []struct {
		field string
		known map[string]string
		want  []string
	}{
		{"district", map[string]string{"documentType": "രജിസ്റ്റർ/ഫയൽ ചെയ്ത"}, []string{"പാലക്കാട്", "മലപ്പുറം"}},
		{"district", map[string]string{"documentType": "വിൽ"}, []string{"പാലക്കാട്"}},
		{"sro", map[string]string{"documentType": "രജിസ്റ്റർ/ഫയൽ ചെയ്ത", "district": "പാലക്കാട്"}, []string{"വിളയൂര്‍", "പട്ടാമ്പി"}},
	} {
		if got, _ := s.optionTexts("prior_document", c.field, c.known); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s %v = %v, want %v", c.field, c.known, got, c.want)
		}
	}
	// ...and the ajax endpoint answers the same for our type label.
	c := s.client().login(s.user("tester"))
	q := url.Values{"page": {"prior_document"}, "field": {"district"}, "p.documentType": {"രജിസ്റ്റർ/ഫയൽ ചെയ്ത"}}
	r := c.get(s.rev("deeds:ajax_portal_options") + "?" + q.Encode())
	if !strings.Contains(r.Body, `"പാലക്കാട്"`) || !strings.Contains(r.Body, `"മലപ്പുറം"`) {
		t.Errorf("ajax = %s", r.Body)
	}
}

func TestAssemblyStoresMatchedOptions(t *testing.T) {
	s := newIsolatedSite(t)
	s.importScrape(priorScrape)
	user := s.user("tester")
	ctx := context.Background()
	deed := &models.Deed{OwnerID: user.ID}
	if err := s.st.CreateDeed(ctx, deed); err != nil {
		t.Fatal(err)
	}
	book, volume, year := 1, 275, 1990
	extracted := &ai.ExtractedDeedData{PreviousDocuments: []ai.ExtractedPriorDocument{{
		DocumentType: "രജിസ്റ്റർ ആധാരം", Number: "870", District: "പാലക്കാട", SRO: "വിളയൂർ", Book: &book, Volume: &volume, Year: &year}}}
	if err := pipeline.ApplyExtraction(ctx, s.st, &deeds.Dropdowns{Src: s.st}, deed, extracted, nil, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	data, err := s.st.LoadDeedData(ctx, deed)
	if err != nil || len(data.PriorDocuments) != 1 {
		t.Fatalf("documents = %v, %v", data.PriorDocuments, err)
	}
	doc := data.PriorDocuments[0]
	if doc.DocumentType != "രജിസ്റ്റർ/ഫയൽ ചെയ്ത" || doc.District != "പാലക്കാട്" || doc.SRO != "വിളയൂര്‍" {
		t.Errorf("document = %q / %q / %q", doc.DocumentType, doc.District, doc.SRO)
	}
}

func TestOlderDeedShowsDropdownsPreselected(t *testing.T) {
	s := newIsolatedSite(t)
	s.importScrape(priorScrape)
	org := s.org("Office")
	user := s.member("tester", org, models.RoleAdmin)
	ctx := context.Background()
	deed := &models.Deed{OwnerID: user.ID, Status: models.StatusReadyForReview}
	if err := s.st.CreateDeed(ctx, deed); err != nil {
		t.Fatal(err)
	}
	book, volume := int16(1), int32(275)
	doc := &models.PriorDocument{DeedID: deed.ID, DocumentType: "രജിസ്റ്റർ ഭാഗപത്രം", District: "പാലക്കാട്", SRO: "വിളയൂർ", Book: &book, Volume: &volume}
	if err := s.st.InsertRow(ctx, store.RowTable["documents"], doc); err != nil {
		t.Fatal(err)
	}
	c := s.client().login(user)
	r := c.get(s.rev("deeds:wizard", deed.PublicID) + "?step=documents")
	if r.Status != 200 {
		t.Fatalf("wizard = %d", r.Status)
	}
	form := formValues(t, r.Body, "documents-")
	if form.Get("documents-0-document_type") != "രജിസ്റ്റർ/ഫയൽ ചെയ്ത" || form.Get("documents-0-sro") != "വിളയൂര്‍" || form.Get("documents-0-district") != "പാലക്കാട്" {
		t.Errorf("preselected: type %q, district %q, sro %q", form.Get("documents-0-document_type"), form.Get("documents-0-district"), form.Get("documents-0-sro"))
	}
	for _, opt := range []string{">വിളയൂര്‍<", ">പട്ടാമ്പി<", ">മലപ്പുറം<"} {
		if !strings.Contains(r.Body, opt) {
			t.Errorf("the page lacks the option %s", opt)
		}
	}
}

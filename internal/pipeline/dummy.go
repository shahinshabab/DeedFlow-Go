package pipeline

import (
	"context"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/db"
	"github.com/shahinshabab/deedflow-go/internal/deeds"
	"github.com/shahinshabab/deedflow-go/internal/models"
	"github.com/shahinshabab/deedflow-go/internal/store"
)

// The real, live-verified location chain of the reference document's
// property: every level exists verbatim in the reference dataset, so the
// portal's own cascading dropdowns accept it.
const (
	dummyDistrict = "പാലക്കാട്"
	dummySRO      = "വിളയൂർ"
	dummyTaluk    = "പട്ടാമ്പി"
	dummyVillage  = "കൊപ്പം"
	dummyDesam    = "ആമയൂര്‍"
)

func i16(n int16) *int16 { return &n }
func i32(n int32) *int32 { return &n }
func dec(s string) *db.Decimal {
	d := db.MustDecimal(s)
	return &d
}

// PopulateDummyDeed fills a deed with data extracted from a real reference
// document (a ജന്മം തീരാധാരം transferring a residential plot in Koppam,
// Pattambi taluk, Palakkad district), for the Test area's "Selenium Test"
// button: a submission test can run end to end without typing through all
// seven wizard steps first. The document left several fields blank (the
// exact day, every monetary amount); those are reasonable placeholders.
func PopulateDummyDeed(ctx context.Context, st *store.Store, deed *models.Deed, today time.Time) error {
	aadhaar := deeds.IDTypeChoices[0].Value
	voter := deeds.IDTypeChoices[2].Value
	son := deeds.RelationChoices[0].Value
	wife := deeds.RelationChoices[2].Value
	return st.InTx(ctx, func(tx *store.Store) error {
		for _, tbl := range []string{"documents", "executants", "claimants", "schedule", "witnesses"} {
			if err := tx.ClearSection(ctx, store.RowTable[tbl], deed.ID); err != nil {
				return err
			}
		}
		// "തീരാധാരം" (not "വിലയാധാരം", book_data.json's own name for the same
		// transaction) is deliberate: the portal payload resolves the portal
		// registration type from DEED_TYPE_PORTAL_MAPPING, keyed by this short
		// colloquial vocabulary.
		if err := tx.SaveRegistration(ctx, &models.RegistrationDetail{
			DeedID: deed.ID, District: dummyDistrict, SRO: dummySRO, Taluk: dummyTaluk, Book: 1,
			DeedType: "തീരാധാരം", DeedSubType: "വിലയാധാരം - (തീറാധാരം)--0702"}); err != nil {
			return err
		}
		date := today
		stampDate := today
		// Physical Paper, not E-Stamp: the portal only shows the Document
		// Value and Amount Assessed fields in Physical Paper mode.
		// document_fees must be >= document_value (a real portal validation).
		if err := tx.SavePresenter(ctx, &models.Presenter{
			DeedID: deed.ID, Name: "സഫിയ", PresenterType: "Executant", HouseName: "കുറുപ്പുതൊടിയിൽ",
			RelativeName: "സുലൈമാൻ", Relation: wife, Gender: "Female", Place: dummyDesam, Post: "ആമയൂർ", Pin: "679303",
			Age: i16(58), Mobile: "9999999999", Job: "സ്വന്തം", IDType: voter, IDNumber: "TJO0571356",
			PresenterDate: &date, DocumentSheetCount: i16(6), StampMode: "Physical Paper", StampDate: &stampDate,
			DocumentCopies: 0, DocumentValue: i32(500000), DocumentFees: i32(600000)}); err != nil {
			return err
		}
		for _, p := range []models.PriorDocument{
			{DocumentType: "രജിസ്റ്റർ/ഫയൽ ചെയ്ത", District: dummyDistrict, SRO: dummySRO, Number: "1036", Year: i16(1980),
				Book: i16(1), Volume: i32(1), PageFrom: i32(1), PageTo: i32(1), Remarks: "Register Bhagapathram (partition deed)"},
			{DocumentType: "രജിസ്റ്റർ/ഫയൽ ചെയ്ത", District: dummyDistrict, SRO: dummySRO, Number: "1327", Year: i16(2014),
				Book: i16(1), Volume: i32(879), PageFrom: i32(353), PageTo: i32(355), Remarks: "Gift deed establishing the executant's title"},
		} {
			p.DeedID = deed.ID
			if err := tx.InsertRow(ctx, store.RowTable["documents"], &p); err != nil {
				return err
			}
		}
		if err := tx.InsertRow(ctx, store.RowTable["executants"], &models.Party{
			DeedID: deed.ID, Capacity: "Executant", Name: "സഫിയ", HouseName: "കുറുപ്പുതൊടിയിൽ", Age: i16(58), Job: "സ്വന്തം",
			RelativeName: "സുലൈമാൻ", Relation: wife, Gender: "Female", Place: dummyDesam, Post: "ആമയൂർ", Pin: "679303",
			District: dummyDistrict, Taluk: dummyTaluk, Village: dummyVillage, IDType: voter, IDNumber: "TJO0571356",
			Mobile: "9999999999"}); err != nil {
			return err
		}
		// The source document's own real Aadhaar number; it passes the Verhoeff checksum.
		if err := tx.InsertRow(ctx, store.RowTable["claimants"], &models.Party{
			DeedID: deed.ID, Capacity: "Claimant", Name: "മുസ്തഫ", HouseName: "പറയരുകുഴിയിൽ", Age: i16(60), Job: "സ്വന്തകാര്യം",
			RelativeName: "കുഞ്ഞറമു", Relation: son, Gender: "Male", Place: dummyDesam, Post: "ആമയൂർ", Pin: "679303",
			District: dummyDistrict, Taluk: dummyTaluk, Village: dummyVillage, IDType: aadhaar, IDNumber: "631223992931",
			Mobile: "9999999998"}); err != nil {
			return err
		}
		if err := tx.InsertRow(ctx, store.RowTable["schedule"], &models.ScheduleItem{
			DeedID: deed.ID, Number: "1", District: dummyDistrict, SRO: dummySRO, Taluk: dummyTaluk, Village: dummyVillage,
			Desam: dummyDesam, LocalBodyType: "പഞ്ചായത്ത്", LocalBodyName: "Koppam",
			ResurveyBlock: "024", ResurveyNumber: "282", ResurveySub: "10/2", OldSurvey: "35", OldSub: "1", TPNumber: "13420",
			LandType:     "Residential plot with Corp.Mun.Panch. road access",
			TotalHectare: dec("0"), TotalAre: dec("1.52"), TotalSqm: dec("0"),
			TransferHectare: dec("0"), TransferAre: dec("1.52"), TransferSqm: dec("0"),
			Price: i32(500000), FairValue: i32(50000), Pokkuvaravu: "Yes", UndividedShare: "No",
			Details:       "തേക്കേ ഏളംകുളം പറമ്പ് കുടിയിരുപ്പിൽ തെക്കുപടിഞ്ഞാറ് ഭാഗം ഓഹരിയിൽ തെക്കു കിഴക്കെ തല",
			BoundaryEast:  "ഹംസ, സെനബ ഇവർ കൈവശം സ്ഥലം",
			BoundaryNorth: "10 അടി വീതി വഴി",
			BoundaryWest:  "സെന്തിൽ കൈവശം സ്ഥലം",
			BoundarySouth: "കുഞ്ഞിമുഹമ്മദ് കൈവശം സ്ഥലം",
			HasBuilding:   "No", HasWell: "No", Paattam: "No"}); err != nil {
			return err
		}
		for _, w := range []models.Witness{
			{WitnessType: "attesting", PortalWitnessType: "WB", Name: "വിനീത്", Gender: "Male", Relation: son,
				RelativeName: "പത്മനാഭൻ", HouseName: "ചോലയിൽ", Job: "ടെക്നീഷ്യൻ", Place: "വിളയൂർ", Desam: "വിളയൂർ"},
			{WitnessType: "identifying", PortalWitnessType: "WB", Name: "മുഹമ്മദ് അലി", Gender: "Male", Relation: son,
				RelativeName: "കുഞ്ഞിമുഹമ്മദ്", HouseName: "പാട്ടാരത്തിൽ", Job: "എഴുത്തു പ്രവൃത്തി", Place: "പുലാശ്ശേരി", Desam: "പുലാശ്ശേരി"},
		} {
			w.DeedID = deed.ID
			if err := tx.InsertRow(ctx, store.RowTable["witnesses"], &w); err != nil {
				return err
			}
		}
		return nil
	})
}

package pipeline

import (
	"context"
	"fmt"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/db"
	"github.com/shahinshabab/deedflow-go/internal/deeds"
	"github.com/shahinshabab/deedflow-go/internal/models"
	"github.com/shahinshabab/deedflow-go/internal/store"
)

// Test deeds (is_test) filled with made-up Malayalam data, for exercising the
// portal submission end to end without typing through the wizard. Every name,
// address, phone and ID number is fictional; only the location chains
// (district -> SRO -> taluk -> village -> desam / resurvey block) and dropdown
// labels are real, because the portal's dropdowns only accept values that
// exist there.

const seedLandType = "Residential plot with Corp.Mun.Panch. road access"

type seedLocation struct {
	district, sro, taluk, village, desam, block, lbody, post, pin string
}

// Real location chains, copied from data/property_data.json (the same data
// the wizard's dropdowns use). Some names end in a legacy chillu (consonant +
// virama + ZWJ) - kept exactly as the portal spells them.
var seedLocations = []seedLocation{
	{"പാലക്കാട്", "വിളയൂർ", "പട്ടാമ്പി", "കൊപ്പം", "ആമയൂര്‍", "024", "Koppam", "കൊപ്പം", "679307"},
	{"തൃശ്ശൂർ", "മതിലകം", "കൊടുങ്ങല്ലൂര്‍", "ആല", "ആല", "008", "Sreenarayanapuram", "ആല", "680668"},
	{"മലപ്പുറം", "അരീക്കോട്", "ഏറനാട്", "അരീക്കോട്", "അരീക്കോട്", "031", "Areekode", "അരീക്കോട്", "673639"},
	{"കോഴിക്കോട്", "അഴിയൂർ", "വടകര", "അഴിയൂര്‍", "ചോമ്പാല", "002", "Azhiyur", "ചോമ്പാല", "673308"},
	{"കണ്ണൂർ", "ആലക്കോട്", "തളിപ്പറമ്പ്", "ആലക്കോട്", "അരങ്ങം", "063", "Alakode", "ആലക്കോട്", "670571"},
}

type seedPerson struct {
	name, gender, relation, relative, house string
	age                                     int16
	job                                     string
}

type seedProperty struct {
	are          string
	price, fair  int32
	pokku        string
	share, outOf string
}

type seedPrior struct {
	docType, number string
	year            int16
}

type seedSpec struct {
	deedType, stamp string
	value           int32
	executants      []seedPerson
	claimants       []seedPerson
	prior           []seedPrior
	properties      []seedProperty
}

// SeedSpecCount is how many test deeds exist to be created.
func SeedSpecCount() int { return len(seedSpecs(deeds.RelationChoices)) }

func seedSpecs(rel []deeds.Choice) []seedSpec {
	son, daughter, wife := rel[0].Value, rel[1].Value, rel[2].Value
	reg := "രജിസ്റ്റർ/ഫയൽ ചെയ്ത"
	return []seedSpec{
		{"തീരാധാരം", "Physical Paper", 750000,
			[]seedPerson{{"രാമചന്ദ്രൻ", "Male", son, "കൃഷ്ണൻ", "തെക്കേപ്പുരയിൽ", 54, "കൃഷി"}},
			[]seedPerson{{"അനിൽകുമാർ", "Male", son, "ഗോപാലൻ", "പുത്തൻവീട്ടിൽ", 41, "അധ്യാപകൻ"}},
			[]seedPrior{{reg, "2104", 2009}},
			[]seedProperty{{"4.05", 750000, 150000, "Yes", "", ""}}},
		{"ദാനാധാരം", "E-Stamp", 400000,
			[]seedPerson{{"ലക്ഷ്മി", "Female", wife, "നാരായണൻ", "കിഴക്കേടത്ത്", 67, "വീട്ടമ്മ"}},
			[]seedPerson{{"ദിവ്യ", "Female", daughter, "നാരായണൻ", "കിഴക്കേടത്ത്", 38, "നഴ്സ്"},
				{"ദീപക്", "Male", son, "നാരായണൻ", "കിഴക്കേടത്ത്", 35, "എഞ്ചിനീയർ"}},
			[]seedPrior{{"പട്ടയം", "LA 118", 1987}},
			[]seedProperty{{"8.10", 400000, 45000, "Yes", "", ""}}},
		{"ഭാഗപത്രം", "Physical Paper", 1200000,
			[]seedPerson{{"അബ്ദുൽ റഹ്മാൻ", "Male", son, "മുഹമ്മദ്", "പള്ളിപ്പറമ്പിൽ", 62, "വ്യാപാരം"},
				{"ഫാത്തിമ", "Female", wife, "അബ്ദുൽ റഹ്മാൻ", "പള്ളിപ്പറമ്പിൽ", 56, "വീട്ടമ്മ"}},
			[]seedPerson{{"ഷമീർ", "Male", son, "അബ്ദുൽ റഹ്മാൻ", "പള്ളിപ്പറമ്പിൽ", 33, "ഡ്രൈവർ"},
				{"ഷബ്ന", "Female", daughter, "അബ്ദുൽ റഹ്മാൻ", "പള്ളിപ്പറമ്പിൽ", 29, "അധ്യാപിക"}},
			[]seedPrior{{reg, "877", 1998}, {reg, "1410", 2011}},
			[]seedProperty{{"6.00", 600000, 90000, "Yes", "", ""}, {"3.25", 600000, 150000, "No", "1", "2"}}},
		{"ഒഴിമുറി", "Physical Paper", 250000,
			[]seedPerson{{"സുരേഷ്", "Male", son, "വാസു", "മണ്ണിൽവീട്", 45, "കൂലിപ്പണി"}},
			[]seedPerson{{"സുനിത", "Female", daughter, "വാസു", "മണ്ണിൽവീട്", 43, "തയ്യൽ"}},
			[]seedPrior{{reg, "3302", 2016}},
			[]seedProperty{{"2.02", 250000, 100000, "No", "1", "3"}}},
		{"ധനനിശ്ചയാധാരം", "Physical Paper", 900000,
			[]seedPerson{{"ജോസഫ്", "Male", son, "തോമസ്", "വടക്കേക്കര", 70, "റിട്ട. ഉദ്യോഗസ്ഥൻ"}},
			[]seedPerson{{"മേരി", "Female", daughter, "ജോസഫ്", "വടക്കേക്കര", 40, "ബാങ്ക് ജീവനക്കാരി"}},
			[]seedPrior{{"കോടതി ഉത്തരവ്", "OS 45", 2005}},
			[]seedProperty{{"10.12", 900000, 80000, "Yes", "", ""}}},
	}
}

type seedWitness struct{ name, gender, relation, relative, house, job string }

// SeedTestDeed creates test deed number i (0-based) for a user.
func SeedTestDeed(ctx context.Context, st *store.Store, userID int64, i int, today time.Time) (*models.Deed, error) {
	specs := seedSpecs(deeds.RelationChoices)
	spec, loc := specs[i], seedLocations[i]
	n := i + 1
	son := deeds.RelationChoices[0].Value
	voter := deeds.IDTypeChoices[2].Value
	witnesses := []seedWitness{
		{"വിനോദ്", "Male", son, "ശങ്കരൻ", "ചിറയിൽ", "എഴുത്തുകാരൻ"},
		{"ബിജു", "Male", son, "ദാമോദരൻ", "പാലക്കൽ", "ആധാരമെഴുത്ത്"},
	}
	regInfo := deeds.ResolveRegistrationType(spec.deedType)

	deed := &models.Deed{OwnerID: userID, IsTest: true, Title: fmt.Sprintf("Test deed %d: %s", n, spec.deedType),
		Status: models.StatusReadyForReview}
	err := st.InTx(ctx, func(tx *store.Store) error {
		if err := tx.CreateDeed(ctx, deed); err != nil {
			return err
		}
		if err := tx.SaveRegistration(ctx, &models.RegistrationDetail{DeedID: deed.ID, District: loc.district, SRO: loc.sro,
			Taluk: loc.taluk, Book: int32(regInfo.Book), DeedType: spec.deedType, DeedSubType: regInfo.SubType}); err != nil {
			return err
		}
		person := 0
		party := func(p seedPerson, capacity string) models.Party {
			person++
			age := p.age
			// Fictional EPIC-style numbers (3 letters + 7 digits) that can't
			// belong to anyone: the "ZZT" series isn't issued.
			return models.Party{DeedID: deed.ID, Name: p.name, Capacity: capacity, Gender: p.gender, Relation: p.relation,
				RelativeName: p.relative, HouseName: p.house, Age: &age, Job: p.job, IDType: voter,
				IDNumber: fmt.Sprintf("ZZT%02d%05d", n, person), Mobile: fmt.Sprintf("90000%02d%03d", n, person),
				Place: loc.desam, Post: loc.post, Pin: loc.pin}
		}
		first := party(spec.executants[0], "")
		date, stampDate := today, today
		sheets := int16(4 + n)
		value, fees := spec.value, spec.value+50000
		age := first.Age
		if err := tx.SavePresenter(ctx, &models.Presenter{
			DeedID: deed.ID, Name: first.Name, PresenterType: "Executant", HouseName: first.HouseName,
			RelativeName: first.RelativeName, Relation: first.Relation, Gender: first.Gender, Place: first.Place,
			Post: first.Post, Pin: first.Pin, Age: age, Mobile: first.Mobile, Job: first.Job, IDType: first.IDType,
			IDNumber: first.IDNumber, PresenterDate: &date, StampDate: &stampDate, StampMode: spec.stamp,
			DocumentSheetCount: &sheets, DocumentCopies: 0, DocumentValue: &value, DocumentFees: &fees}); err != nil {
			return err
		}
		person = 0 // the presenter is the first executant - same ID
		for _, p := range spec.executants {
			row := party(p, "Executant")
			row.District, row.Taluk, row.Village = loc.district, loc.taluk, loc.village
			if err := tx.InsertRow(ctx, store.RowTable["executants"], &row); err != nil {
				return err
			}
		}
		for _, p := range spec.claimants {
			row := party(p, "Claimant")
			row.District, row.Taluk, row.Village = loc.district, loc.taluk, loc.village
			if err := tx.InsertRow(ctx, store.RowTable["claimants"], &row); err != nil {
				return err
			}
		}
		for _, p := range spec.prior {
			year := p.year
			row := models.PriorDocument{DeedID: deed.ID, DocumentType: p.docType, Number: p.number, Year: &year,
				Remarks: "Test record"}
			if p.docType == "രജിസ്റ്റർ/ഫയൽ ചെയ്ത" {
				book, volume, from, to := int16(1), int32(100+n), int32(10), int32(14)
				row.District, row.SRO, row.Book, row.Volume, row.PageFrom, row.PageTo, row.Remarks =
					loc.district, loc.sro, &book, &volume, &from, &to, ""
			}
			if err := tx.InsertRow(ctx, store.RowTable["documents"], &row); err != nil {
				return err
			}
		}
		for k, prop := range spec.properties {
			k++
			price, fair := prop.price, prop.fair
			zero := db.MustDecimal("0")
			are := db.MustDecimal(prop.are)
			share := "No"
			if prop.share != "" {
				share = "Yes"
			}
			row := models.ScheduleItem{
				DeedID: deed.ID, Number: fmt.Sprint(k), District: loc.district, SRO: loc.sro, Taluk: loc.taluk,
				Village: loc.village, Desam: loc.desam, LocalBodyType: "പഞ്ചായത്ത്", LocalBodyName: loc.lbody,
				ResurveyBlock: loc.block, ResurveyNumber: fmt.Sprint(100 + 10*n + k), ResurveySub: fmt.Sprintf("%d/1", k),
				OldSurvey: fmt.Sprint(40 + n), OldSub: fmt.Sprint(k), TPNumber: fmt.Sprint(5000 + 100*n + k),
				LandType: seedLandType, TotalHectare: &zero, TotalAre: &are, TotalSqm: &zero,
				TransferHectare: &zero, TransferAre: &are, TransferSqm: &zero,
				Price: &price, FairValue: &fair, Pokkuvaravu: prop.pokku, UndividedShare: share,
				Share: prop.share, ShareOutOf: prop.outOf,
				Details:      fmt.Sprintf("%s ദേശത്ത് റീസർവേ %d ൽ പെട്ട പുരയിടം", loc.desam, 100+10*n+k),
				BoundaryEast: "പഞ്ചായത്ത് റോഡ്", BoundaryNorth: "ബാലൻ കൈവശം സ്ഥലം",
				BoundaryWest: "തോട്", BoundarySouth: "ജാനകി കൈവശം സ്ഥലം",
				HasBuilding: "No", HasWell: "No", Paattam: "No"}
			if err := tx.InsertRow(ctx, store.RowTable["schedule"], &row); err != nil {
				return err
			}
		}
		for j, kind := range []string{"attesting", "identifying"} {
			w := witnesses[j]
			row := models.Witness{DeedID: deed.ID, WitnessType: kind, PortalWitnessType: "WB", Name: w.name, Gender: w.gender,
				Relation: w.relation, RelativeName: w.relative, HouseName: w.house, Job: w.job, Place: loc.desam, Desam: loc.desam}
			if err := tx.InsertRow(ctx, store.RowTable["witnesses"], &row); err != nil {
				return err
			}
		}
		return nil
	})
	return deed, err
}

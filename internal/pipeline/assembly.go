// Package pipeline runs the background "PDF to reviewed deed" flow: reading
// the PDF, parsing the schedule, structuring the text with AI and filling
// the deed's related rows (deeds/tasks.py process_deed_pdf and
// deeds/utils/deed_assembly.py).
package pipeline

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/ai"
	"github.com/shahinshabab/deedflow-go/internal/deeds"
	"github.com/shahinshabab/deedflow-go/internal/extract"
	"github.com/shahinshabab/deedflow-go/internal/models"
	"github.com/shahinshabab/deedflow-go/internal/store"
)

func int16p(v *int, what string) (*int16, error) {
	if v == nil {
		return nil, nil
	}
	if *v < 0 || *v > 32767 {
		return nil, fmt.Errorf("%s %d is out of range", what, *v)
	}
	n := int16(*v)
	return &n, nil
}

func int32p(v *int, what string) (*int32, error) {
	if v == nil {
		return nil, nil
	}
	if *v < -2147483648 || *v > 2147483647 {
		return nil, fmt.Errorf("%s %d is out of range", what, *v)
	}
	n := int32(*v)
	return &n, nil
}

func partyFromExtracted(p ai.ExtractedParty, capacity string, deedID int64) (models.Party, error) {
	age, err := int16p(p.Age, "age")
	if err != nil {
		return models.Party{}, err
	}
	return models.Party{
		DeedID: deedID, Name: p.Name, Capacity: capacity, HouseName: p.HouseName, Age: age, Job: p.Job,
		RelativeName: p.RelativeName, Relation: p.Relation, Gender: p.Gender, Place: p.Place, Post: p.Post,
		Pin: p.Pin, District: p.District, Taluk: p.Taluk, Village: p.Village, IDType: p.IDType, IDNumber: p.IDNumber,
	}, nil
}

// registrationLocation is the district/SRO/taluk for a deed with no property
// schedule (a power of attorney, a will): the SRO its earlier documents were
// registered at and the first executant's taluk, matched to the reference names.
func registrationLocation(e *ai.ExtractedDeedData) (district, sro, taluk string) {
	data := deeds.PropertyData()
	var doc *ai.ExtractedPriorDocument
	for i := range e.PreviousDocuments {
		if d := &e.PreviousDocuments[i]; d.District != "" && d.SRO != "" {
			doc = d
			break
		}
	}
	var giver *ai.ExtractedParty
	if len(e.Giver) > 0 {
		giver = &e.Giver[0]
	}
	hint := ""
	if doc != nil {
		hint = doc.District
	}
	if hint == "" && giver != nil {
		hint = giver.District
	}
	if hint == "" {
		return
	}
	district, _ = deeds.FindBestMatch(hint, data.KeyList())
	sros := data.Obj(district).Obj("sros")
	if doc != nil {
		sro, _ = deeds.FindBestMatch(doc.SRO, sros.KeyList())
	}
	var taluks []string
	if sro != "" {
		taluks = sros.Obj(sro).Obj("thaluks").KeyList()
	}
	switch {
	case giver != nil && giver.Taluk != "" && len(taluks) > 0:
		taluk, _ = deeds.FindBestMatch(giver.Taluk, taluks)
	case len(taluks) > 0:
		taluk = taluks[0]
	}
	return
}

// ApplyExtraction populates the deed's related rows. Calling it again for the
// same deed replaces the repeatable sections instead of duplicating them.
func ApplyExtraction(ctx context.Context, st *store.Store, dd *deeds.Dropdowns, deed *models.Deed,
	e *ai.ExtractedDeedData, entries []extract.Entry, landType string, today time.Time) error {
	return st.InTx(ctx, func(tx *store.Store) error {
		other := e.OtherDetails
		reg := deeds.ResolveRegistrationType(other.DeedType)
		var district, sro, taluk string
		if len(entries) > 0 {
			district, sro, taluk = entries[0].District, entries[0].SRO, entries[0].Taluk
		} else {
			district, sro, taluk = registrationLocation(e)
		}
		rd := &models.RegistrationDetail{DeedID: deed.ID, District: district, SRO: sro, Taluk: taluk,
			Book: int32(reg.Book), DeedType: reg.RegistrationType, DeedSubType: reg.SubType}
		if err := tx.SaveRegistration(ctx, rd); err != nil {
			return err
		}

		if err := tx.ClearSection(ctx, store.RowTable["executants"], deed.ID); err != nil {
			return err
		}
		for _, p := range e.Giver {
			row, err := partyFromExtracted(p, "Executant", deed.ID)
			if err != nil {
				return err
			}
			if err := tx.InsertRow(ctx, store.RowTable["executants"], &row); err != nil {
				return err
			}
		}
		if err := tx.ClearSection(ctx, store.RowTable["claimants"], deed.ID); err != nil {
			return err
		}
		for _, p := range e.Recipient {
			row, err := partyFromExtracted(p, "Claimant", deed.ID)
			if err != nil {
				return err
			}
			if err := tx.InsertRow(ctx, store.RowTable["claimants"], &row); err != nil {
				return err
			}
		}

		// Default the presenter to the first executant, only when none is set yet.
		if len(e.Giver) > 0 {
			data, err := tx.LoadDeedData(ctx, deed)
			if err != nil {
				return err
			}
			if data.Presenter == nil {
				first, err := partyFromExtracted(e.Giver[0], "Executant", deed.ID)
				if err != nil {
					return err
				}
				sheets := int16(deeds.DefaultFilingSheets)
				d := today
				p := &models.Presenter{
					DeedID: deed.ID, Name: first.Name, PresenterType: "Executant", HouseName: first.HouseName,
					RelativeName: first.RelativeName, Relation: first.Relation, Gender: first.Gender, Place: first.Place,
					Post: first.Post, Pin: first.Pin, Age: first.Age, Job: first.Job, IDType: first.IDType,
					IDNumber: first.IDNumber, PresenterDate: &d, StampDate: &d, DocumentSheetCount: &sheets,
				}
				if err := tx.SavePresenter(ctx, p); err != nil {
					return err
				}
			}
		}

		if err := tx.ClearSection(ctx, store.RowTable["documents"], deed.ID); err != nil {
			return err
		}
		for _, doc := range e.PreviousDocuments {
			docType := deeds.MatchPriorDocumentType(doc.DocumentType, doc.SRO != "" || doc.Book != nil || doc.Volume != nil)
			if docType == "" {
				docType = doc.DocumentType
			}
			dist := dd.MatchOption(ctx, "prior_document", "district", doc.District, map[string]string{"documentType": docType})
			srov := dd.MatchOption(ctx, "prior_document", "sro", doc.SRO, map[string]string{"documentType": docType, "district": dist})
			year, err := int16p(doc.Year, "year")
			if err != nil {
				return err
			}
			book, err := int16p(doc.Book, "book")
			if err != nil {
				return err
			}
			vol, err := int32p(doc.Volume, "volume")
			if err != nil {
				return err
			}
			pf, err := int32p(doc.PageFrom, "page_from")
			if err != nil {
				return err
			}
			pt, err := int32p(doc.PageTo, "page_to")
			if err != nil {
				return err
			}
			row := &models.PriorDocument{DeedID: deed.ID, DocumentType: docType, District: dist, SRO: srov, Number: doc.Number,
				Year: year, Book: book, Volume: vol, PageFrom: pf, PageTo: pt, Remarks: doc.Remarks}
			if err := tx.InsertRow(ctx, store.RowTable["documents"], row); err != nil {
				return err
			}
		}

		if err := tx.ClearSection(ctx, store.RowTable["schedule"], deed.ID); err != nil {
			return err
		}
		deed.SurveyMapping = []models.SurveyMapping{}
		docPrice, err := int32p(other.DocumentPrice, "document_price")
		if err != nil {
			return err
		}
		fair, err := int32p(other.FairValue, "fair_value")
		if err != nil {
			return err
		}
		for idx, entry := range entries {
			idx1 := idx + 1
			number := entry.Number
			if number == "" {
				number = strconv.Itoa(idx1)
			}
			if m := entry.SurveyMapping; m != nil {
				deed.SurveyMapping = append(deed.SurveyMapping, models.SurveyMapping{
					Start: idx1 - 1, Count: m.Count, Number: number, OldSurveys: m.OldSurveys, Subs: m.Subs,
					Assignment: m.Assignment, Areas: m.Areas})
			}
			item := entry.ScheduleItem
			item.ID, item.DeedID, item.Number, item.LandType = 0, deed.ID, number, landType
			item.Position = 0
			if !entry.PriceSet {
				item.Price = docPrice
			}
			item.FairValue = fair
			if err := tx.InsertRow(ctx, store.RowTable["schedule"], &item); err != nil {
				return err
			}
		}

		deed.Status = models.StatusReadyForReview
		deed.Progress = 100
		if deed.Title == "" && len(e.Giver) > 0 {
			deed.Title = "Deed for " + e.Giver[0].Name
		}
		return tx.SaveDeed(ctx, deed, "status", "progress", "title", "survey_mapping")
	})
}

// ApplySurveyMapping rebuilds the rows of each schedule in the deed's
// survey_mapping from the user's pairing of its old survey numbers and
// sub-divisions (one list per mapping, an index into old_surveys per
// sub-division), then clears the mapping.
func ApplySurveyMapping(ctx context.Context, st *store.Store, deed *models.Deed, assignments [][]int) error {
	return st.InTx(ctx, func(tx *store.Store) error {
		data, err := tx.LoadDeedData(ctx, deed)
		if err != nil {
			return err
		}
		rows := data.Schedule
		n := len(deed.SurveyMapping)
		if len(assignments) < n {
			n = len(assignments)
		}
		type pair struct {
			m models.SurveyMapping
			a []int
		}
		pairs := make([]pair, n)
		for i := 0; i < n; i++ {
			pairs[i] = pair{deed.SurveyMapping[i], assignments[i]}
		}
		// Latest schedule first, so earlier indexes stay valid.
		for i := 0; i < len(pairs); i++ {
			for j := i + 1; j < len(pairs); j++ {
				if pairs[j].m.Start > pairs[i].m.Start {
					pairs[i], pairs[j] = pairs[j], pairs[i]
				}
			}
		}
		for _, p := range pairs {
			start, count := p.m.Start, p.m.Count
			if start >= len(rows) {
				return fmt.Errorf("survey mapping starts at row %d of %d", start, len(rows))
			}
			repl, _ := extract.OldSurveyRows(rows[start], p.m.OldSurveys, p.m.Subs, p.a, p.m.Areas)
			end := min(start+count, len(rows))
			rows = append(append(append([]models.ScheduleItem{}, rows[:start]...), repl...), rows[end:]...)
		}
		if err := tx.ClearSection(ctx, store.RowTable["schedule"], deed.ID); err != nil {
			return err
		}
		for i := range rows {
			r := rows[i]
			r.ID, r.DeedID, r.Position = 0, deed.ID, 0
			if err := tx.InsertRow(ctx, store.RowTable["schedule"], &r); err != nil {
				return err
			}
		}
		deed.SurveyMapping = []models.SurveyMapping{}
		return tx.SaveDeed(ctx, deed, "survey_mapping")
	})
}

package deedforms

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/db"
	"github.com/shahinshabab/deedflow-go/internal/deeds"
	"github.com/shahinshabab/deedflow-go/internal/forms"
	"github.com/shahinshabab/deedflow-go/internal/models"
)

// Builder makes the wizard's forms; it carries what dynamic choices need.
type Builder struct {
	Ctx context.Context
	DD  *deeds.Dropdowns
	// Today is the office's current date (a function so tests can fix it).
	Today func() time.Time
}

func (b *Builder) today() time.Time {
	if b.Today != nil {
		return b.Today()
	}
	return deeds.Today(time.Local)
}

// portalSpec is PortalChoicesMixin's class attributes.
type portalSpec struct {
	page        string
	fields      [][2]string // form field -> portal dropdown id, in order
	textIsLabel map[string]bool
	// fixed names the fields with choices of their own (they only act as parents).
	fixed map[string]bool
}

// current is PortalChoicesMixin._current: the field's value as it stands -
// submitted, else saved/initial.
func current(f *forms.Form, name string, locked bool) string {
	if f.Bound && !locked {
		return f.Data.Get(f.AddPrefix(name))
	}
	return f.Initial[name]
}

func currentText(f *forms.Form, spec portalSpec, name string, locked bool) string {
	value := current(f, name, locked)
	if !spec.textIsLabel[name] {
		return value
	}
	fl := f.Field(name)
	if fl != nil && value != "" {
		for _, c := range fl.Choices {
			if c.Value == value {
				return c.Label
			}
		}
	}
	return value
}

// applyPortalChoices offers the registration portal's own scraped options in
// the fields of spec, cascading the way the portal does.
func (b *Builder) applyPortalChoices(f *forms.Form, spec portalSpec, locked bool) {
	toForm := map[string]string{}
	for _, p := range spec.fields {
		toForm[p[1]] = p[0]
	}
	for _, p := range spec.fields {
		name, portalField := p[0], p[1]
		old := f.Field(name)
		if old == nil || spec.fixed[name] {
			continue
		}
		parents, ok := b.DD.ParentFields(b.Ctx, spec.page, portalField)
		if !ok {
			continue
		}
		if len(parents) > 0 {
			any := false
			for _, pp := range parents {
				if _, in := toForm[pp]; in {
					any = true
				}
			}
			if !any {
				continue
			}
		}
		var kept []string
		for _, pp := range parents {
			if _, in := toForm[pp]; in {
				kept = append(kept, pp)
			}
		}
		parents = kept
		known := map[string]string{}
		for _, pp := range parents {
			known[pp] = currentText(f, spec, toForm[pp], locked)
		}
		texts, _ := b.DD.OptionTexts(b.Ctx, spec.page, portalField, known)
		saved := f.Initial[name]
		if saved != "" && !contains(texts, saved) && !f.Bound {
			if match, found := deeds.BestMatch(saved, deeds.TextOptions(texts), 0.75, nil); found {
				f.Initial[name] = match
				saved = match
			}
		}
		if saved != "" && !contains(texts, saved) {
			texts = append([]string{saved}, texts...)
		}
		choices := []forms.ChoiceOpt{{Value: "", Label: "---"}}
		for _, t := range texts {
			choices = append(choices, forms.ChoiceOpt{Value: t, Label: t})
		}
		parentForm := make([]string, len(parents))
		for i, pp := range parents {
			parentForm[i] = toForm[pp]
		}
		nf := &forms.Field{
			Name: name, Label: old.Label, Kind: forms.Choice, Widget: forms.Select, Required: old.Required,
			HelpText: old.HelpText, Choices: choices, Disabled: old.Disabled,
			Attrs: map[string]string{
				"class":                  "form-select",
				"data-portal-page":       spec.page,
				"data-portal-field":      portalField,
				"data-portal-parents":    strings.Join(parentForm, ","),
				"data-portal-parent-ids": strings.Join(parents, ","),
			},
		}
		for i, fl := range f.Fields {
			if fl.Name == name {
				f.Fields[i] = nf
			}
		}
	}
	for name := range spec.textIsLabel {
		if fl := f.Field(name); fl != nil {
			fl.Attrs["data-portal-text"] = "label"
		}
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// --- registration ----------------------------------------------------------

var registrationSpec = portalSpec{
	page: "registration",
	fields: [][2]string{{"district", "ddDistrict"}, {"sro", "ddSro"}, {"taluk", "ddTaluk"},
		{"book", "selddbookno"}, {"deed_type", "selddMainTtype"}, {"deed_sub_type", "selddTtype"}},
	textIsLabel: map[string]bool{"book": true},
	fixed:       map[string]bool{"book": true},
}

// RegistrationOptions says how to build the registration form.
type RegistrationOptions struct {
	Data   url.Values
	Locked bool // the portal issued a token: nothing can change
}

// Registration builds the registration page's form (prefix "registration").
func (b *Builder) Registration(prefix string, row *models.RegistrationDetail, o RegistrationOptions) *forms.Form {
	districts := []forms.ChoiceOpt{{Value: "", Label: "-- Select District --"}}
	for _, d := range deeds.DistrictList() {
		districts = append(districts, forms.ChoiceOpt{Value: d, Label: d})
	}
	book := choiceChar("book", false, false, deeds.BookChoices, "")
	for i, c := range book.Choices {
		if c.Value != "" {
			n, _ := strconv.Atoi(c.Value)
			book.Choices[i].Label = deeds.BookPortalLabel(n)
		}
	}
	sro := plainChoice("sro", "Sub Registrar Office", true, []forms.ChoiceOpt{{Value: "", Label: "-- Select District First --"}})
	f := forms.New(prefix,
		plainChoice("district", "District", true, districts),
		sro,
		plainChoice("taluk", "Taluk", true, []forms.ChoiceOpt{{Value: "", Label: "-- Select SRO First --"}}),
		book,
		plainChoice("deed_type", "Transaction Type", true, []forms.ChoiceOpt{{Value: "", Label: "-- Select Book First --"}}),
		plainChoice("deed_sub_type", "Sub Doc Type", true, []forms.ChoiceOpt{{Value: "", Label: "-- Select Transaction Type First --"}}),
	)
	bootstrap(f)
	saved := row != nil && row.ID != 0
	if saved {
		f.InitialFromRow(row)
	}
	if o.Data != nil {
		f.Bind(o.Data)
	}
	// An older deed (or one straight from the AI pipeline) may hold the
	// everyday deed-type word, which isn't one of the portal's Transaction
	// Types: show the portal's labels it maps to instead.
	if !f.Bound && saved && deeds.IsKnownDeedType(row.DeedType) {
		m := deeds.PortalRegistrationType(row.DeedType, row.DeedSubType)
		f.Initial["deed_type"] = m.RegistrationType
		f.Initial["deed_sub_type"] = m.RegistrationSubType
	}
	cur := func(name string) string { return current(f, name, o.Locked) }
	setDependent(f, "sro", "-- Select District First --", saved, o.Locked, func() []string { return deeds.SROsForDistrict(cur("district")) })
	setDependent(f, "taluk", "-- Select SRO First --", saved, o.Locked, func() []string { return deeds.TaluksForSRO(cur("district"), cur("sro")) })
	setDependent(f, "deed_type", "-- Select Book First --", saved, o.Locked, func() []string { return deeds.DeedTypesForBook(cur("book")) })
	setDependent(f, "deed_sub_type", "-- Select Transaction Type First --", saved, o.Locked, func() []string {
		var names []string
		for _, s := range deeds.DeedSubtypesForType(cur("book"), cur("deed_type")) {
			names = append(names, s.Name)
		}
		return names
	})
	b.applyPortalChoices(f, registrationSpec, o.Locked)
	if o.Locked {
		for _, fl := range f.Fields {
			fl.Disabled = true
			fl.Required = false
		}
	}
	return f
}

// setDependent is RegistrationDetailForm._set_dependent_choices: the options
// a parent's current value leads to, always including the value already
// selected so a perfectly good, already-cascaded value stays valid.
func setDependent(f *forms.Form, name, placeholder string, saved, locked bool, options func() []string) {
	cur := current(f, name, locked)
	var opts []string
	if cur != "" || f.Bound || saved {
		opts = options()
	}
	if cur != "" && !contains(opts, cur) {
		opts = append([]string{cur}, opts...)
	}
	choices := []forms.ChoiceOpt{{Value: "", Label: placeholder}}
	for _, o := range opts {
		choices = append(choices, forms.ChoiceOpt{Value: o, Label: o})
	}
	fl := f.Field(name)
	fl.Choices = choices
	fl.Required = true
}

// --- presenter -------------------------------------------------------------

// Presenter builds the presenter page's form (prefix "presenter").
func (b *Builder) Presenter(prefix string, row *models.Presenter, data url.Values, minimumDocumentValue int64) *forms.Form {
	f := forms.New(prefix,
		choiceChar("presenter_type", false, false, deeds.PartyCapacityChoices, ""),
		dateField("presenter_date", true),
		charField("name", 255, false),
		choiceChar("gender", true, true, deeds.GenderChoices, ""),
		smallUint("age", true, ""),
		choiceChar("relation", true, true, deeds.RelationChoices, ""),
		charField("relative_name", 255, true),
		charField("job", 255, true),
		charField("house_name", 255, true),
		charField("place", 255, true),
		charField("post", 255, true),
		charField("pin", 6, true),
		charField("mobile", 15, true),
		choiceChar("id_type", true, true, deeds.IDTypeChoices, ""),
		charField("id_number", 50, true),
		uint32f("document_value", true),
		smallUint("document_sheet_count", true, ""),
		choiceChar("stamp_mode", true, true, deeds.StampModeChoices, ""),
		dateField("stamp_date", true),
		smallUint("document_copies", true, "0"),
		uint32f("document_fees", true),
	)
	bootstrap(f)
	if row != nil && row.ID != 0 {
		f.InitialFromRow(row)
	}
	if data != nil {
		f.Bind(data)
	}
	if minimumDocumentValue > 0 {
		f.Field("document_value").HelpText = fmt.Sprintf("At least %s - the properties' fair value times the land transferred.", commas(minimumDocumentValue))
	}
	f.Clean = func(f *forms.Form) {
		today := b.today()
		if d := f.DateVal("presenter_date"); d != nil && d.Before(today) {
			f.AddError("presenter_date", "The portal only accepts today's date or a later one.")
		}
		if d := f.DateVal("stamp_date"); d != nil && d.After(today) {
			f.AddError("stamp_date", "The stamp date can't be in the future - use today's date or an earlier one.")
		}
		if v := f.IntVal("document_value"); v != nil && minimumDocumentValue > 0 && *v < minimumDocumentValue {
			f.AddError("document_value", fmt.Sprintf("The portal won't accept less than %s for these properties "+
				"(their fair value times the land being transferred).", commas(minimumDocumentValue)))
		}
	}
	return f
}

// PresenterDefaults is what the Presenter page shows when opened: today as
// the presenting date when none is saved or the saved one has passed, today
// as the stamp date when blank, and the default number of filing sheets.
func (b *Builder) PresenterDefaults(f *forms.Form, row *models.Presenter) {
	today := b.today()
	iso := today.Format("2006-01-02")
	if row == nil || row.PresenterDate == nil || row.PresenterDate.Before(today) {
		f.Initial["presenter_date"] = iso
	}
	if row == nil || row.StampDate == nil {
		f.Initial["stamp_date"] = iso
	}
	if row == nil || row.DocumentSheetCount == nil || *row.DocumentSheetCount == 0 {
		f.Initial["document_sheet_count"] = strconv.Itoa(deeds.DefaultFilingSheets)
	}
}

// --- formset rows ------------------------------------------------------------

var priorSpec = portalSpec{
	page:   "prior_document",
	fields: [][2]string{{"document_type", "documentType"}, {"district", "district"}, {"sro", "sro"}},
	fixed:  map[string]bool{"document_type": true},
}

func (b *Builder) priorDocumentBuild(prefix string, row any) *forms.Form {
	f := forms.New(prefix,
		choiceChar("document_type", false, false, deeds.PriorDocumentTypeChoices, ""),
		plainChoice("district", "District", false, districtChoices()),
		plainChoice("sro", "Sub Registrar Office", false, allSROChoices()),
		smallUint("year", true, ""),
		smallUint("book", true, ""),
		charField("number", 50, true),
		uint32f("volume", true),
		uint32f("page_from", true),
		uint32f("page_to", true),
		textField("remarks", true, 2),
	)
	bootstrap(f)
	return f
}

func (b *Builder) priorDocumentFinish(f *forms.Form, row any) {
	saved := f.Initial["document_type"]
	if saved != "" && !f.Bound && !deeds.HasValue(deeds.PriorDocumentTypeChoices, saved) {
		registered := f.Initial["sro"] != "" || truthyInt(f.Initial["book"]) || truthyInt(f.Initial["volume"])
		if m := deeds.MatchPriorDocumentType(saved, registered); m != "" {
			f.Initial["document_type"] = m
		}
	}
	b.applyPortalChoices(f, priorSpec, false)
}

func truthyInt(s string) bool { return s != "" && s != "0" }

var partySpec = func(page string) portalSpec {
	return portalSpec{page: page, fields: [][2]string{{"district", "district"}, {"taluk", "thaluk"}, {"village", "village"}}}
}

func (b *Builder) partyBuild(prefix string, row any) *forms.Form {
	f := forms.New(prefix,
		choiceChar("capacity", false, false, deeds.PartyCapacityChoices, ""),
		charField("name", 255, false),
		choiceChar("gender", true, true, deeds.GenderChoices, ""),
		smallUint("age", true, ""),
		choiceChar("relation", true, true, deeds.RelationChoices, ""),
		charField("relative_name", 255, true),
		charField("job", 255, true),
		plainChoice("district", "District", false, districtChoices()),
		plainChoice("taluk", "Taluk", false, allTalukChoices()),
		charField("village", 255, true),
		charField("house_name", 255, true),
		charField("post", 255, true),
		charField("place", 255, true),
		charField("pin", 6, true),
		charField("mobile", 15, true),
		choiceChar("id_type", true, true, deeds.IDTypeChoices, ""),
		charField("id_number", 50, true),
	)
	bootstrap(f)
	return f
}

var scheduleSpec = portalSpec{
	page: "property",
	fields: [][2]string{{"district", "district"}, {"sro", "sro"}, {"taluk", "thaluk"}, {"village", "village"},
		{"desam", "desam"}, {"local_body_type", "lbodytype"}, {"local_body_name", "lbodyname"},
		{"resurvey_block", "reBlockNo"}, {"land_type", "landType"}},
}

// ScheduleFullWidth are the fields spanning the whole row.
var ScheduleFullWidth = []string{"details", "boundary_east", "boundary_north", "boundary_west", "boundary_south"}

func (b *Builder) scheduleBuild(prefix string, row any) *forms.Form {
	f := forms.New(prefix,
		charField("number", 10, true),
		plainChoice("district", "District", false, districtChoices()),
		plainChoice("sro", "Sub Registrar Office", false, allSROChoices()),
		plainChoice("taluk", "Taluk", false, allTalukChoices()),
		charField("village", 255, true),
		charField("desam", 255, true),
		charField("local_body_type", 255, true),
		charField("local_body_name", 255, true),
		charField("resurvey_block", 50, true),
		charField("resurvey_number", 50, true),
		charField("resurvey_sub", 50, true),
		charField("old_survey", 50, true),
		charField("old_sub", 50, true),
		charField("land_type", 255, true),
		charField("tp_number", 50, true),
		decimalField("total_hectare"), decimalField("total_are"), decimalField("total_sqm"),
		decimalField("transfer_hectare"), decimalField("transfer_are"), decimalField("transfer_sqm"),
		charField("pokkuvaravu", 10, true), charField("undivided_share", 10, true),
		charField("share", 10, true), charField("share_out_of", 10, true),
		uint32f("price", true),
		textField("details", true, 3),
		uint32f("fair_value", true),
		textInputField("boundary_east", true), textInputField("boundary_north", true),
		textInputField("boundary_west", true), textInputField("boundary_south", true),
		charField("has_building", 10, true), charField("has_well", 10, true), charField("paattam", 10, true),
	)
	bootstrap(f)
	f.FullWidth = ScheduleFullWidth
	fv := f.Field("fair_value")
	fv.Required = true // the portal refuses a property without one
	fv.HelpText = "Per are."
	f.Clean = func(f *forms.Form) {
		var it models.ScheduleItem
		it.UndividedShare = f.Str("undivided_share")
		if v := f.IntVal("fair_value"); v != nil {
			n := int32(*v)
			it.FairValue = &n
		}
		it.TransferHectare, it.TransferAre, it.TransferSqm = f.DecimalVal("transfer_hectare"), f.DecimalVal("transfer_are"), f.DecimalVal("transfer_sqm")
		it.TotalHectare, it.TotalAre, it.TotalSqm = f.DecimalVal("total_hectare"), f.DecimalVal("total_are"), f.DecimalVal("total_sqm")
		minimum, ok := deeds.ItemMinimumPrice(&it)
		price := f.IntVal("price")
		// A schedule's extra old survey row (no resurvey number) goes in at 0.
		continuation := f.Str("resurvey_number") == "" && price != nil && *price == 0
		if ok && minimum != 0 && price != nil && *price < minimum && !continuation {
			f.AddError("price", fmt.Sprintf("The portal won't accept less than %s - the fair value (%s per are) times the land being transferred.",
				commas(minimum), commas(*f.IntVal("fair_value"))))
		}
	}
	return f
}

func (b *Builder) witnessBuild(prefix string, row any) *forms.Form {
	f := forms.New(prefix,
		choiceChar("witness_type", false, false, deeds.WitnessTypeChoices, ""),
		choiceChar("portal_witness_type", false, true, deeds.WitnessPortalTypeChoices, "WB"),
		charField("name", 255, false),
		choiceChar("gender", true, true, deeds.GenderChoices, ""),
		choiceChar("relation", true, true, deeds.RelationChoices, ""),
		charField("relative_name", 255, true),
		charField("house_name", 255, true),
		charField("desam", 255, true),
		charField("place", 255, true),
		charField("job", 255, true),
	)
	bootstrap(f)
	return f
}

// SectionForm builds one repeatable row's form (the detail page's read-only
// forms use it too).
func (b *Builder) SectionForm(section, prefix string, row any, data url.Values) *forms.Form {
	var f *forms.Form
	var finish func(*forms.Form, any)
	switch section {
	case "documents":
		f, finish = b.priorDocumentBuild(prefix, row), b.priorDocumentFinish
	case "executants":
		f, finish = b.partyBuild(prefix, row), func(f *forms.Form, _ any) { b.applyPortalChoices(f, partySpec("executant"), false) }
	case "claimants":
		f, finish = b.partyBuild(prefix, row), func(f *forms.Form, _ any) { b.applyPortalChoices(f, partySpec("claimant"), false) }
	case "schedule":
		f, finish = b.scheduleBuild(prefix, row), func(f *forms.Form, _ any) { b.applyPortalChoices(f, scheduleSpec, false) }
	case "witnesses":
		f, finish = b.witnessBuild(prefix, row), nil
	default:
		panic("unknown section " + section)
	}
	if forms.PKOf(row) != 0 {
		f.InitialFromRow(row)
	}
	if data != nil {
		f.Bind(data)
	}
	if finish != nil {
		finish(f, row)
	}
	return f
}

// FormSetOptions are the per-section formset settings.
type FormSetOptions struct {
	// DocumentValue is the presenter's document value; the schedule's prices
	// must add up to it.
	DocumentValue *int64
}

// SectionFormSet builds the formset of one repeatable wizard step.
func (b *Builder) SectionFormSet(section string, deedID int64, existing []any, data url.Values, o FormSetOptions) *forms.FormSet {
	fs := &forms.FormSet{
		Prefix: section, ParentID: deedID, ParentField: "deed", Table: tableFor(section),
		Bound: data != nil, Data: data, Existing: existing, Extra: 1, CanDelete: true,
		NewRow: newRowFor(section),
	}
	switch section {
	case "documents":
		fs.Build, fs.Finish = b.priorDocumentBuild, b.priorDocumentFinish
	case "executants":
		fs.Build = b.partyBuild
		fs.Finish = func(f *forms.Form, _ any) { b.applyPortalChoices(f, partySpec("executant"), false) }
		fs.MinNum, fs.ValidateMin = 1, true
	case "claimants":
		fs.Build = b.partyBuild
		fs.Finish = func(f *forms.Form, _ any) { b.applyPortalChoices(f, partySpec("claimant"), false) }
		fs.MinNum, fs.ValidateMin = 1, true
	case "schedule":
		fs.Build = b.scheduleBuild
		fs.Finish = func(f *forms.Form, _ any) { b.applyPortalChoices(f, scheduleSpec, false) }
		fs.MinNum, fs.ValidateMin = 1, true
		fs.Clean = func(fs *forms.FormSet) {
			if o.DocumentValue == nil || *o.DocumentValue == 0 || fs.HasFormErrors() {
				return
			}
			var total int64
			for _, rf := range fs.Forms {
				if len(rf.Cleaned) == 0 {
					continue
				}
				if del, _ := rf.Cleaned["DELETE"].(bool); del {
					continue
				}
				if v := rf.IntVal("price"); v != nil {
					total += *v
				}
			}
			if total != *o.DocumentValue {
				fs.AddNonFormError(fmt.Sprintf("The property prices add up to %s, but the document value on the Presenter page is %s - the portal needs them to be equal.",
					commas(total), commas(*o.DocumentValue)))
			}
		}
	case "witnesses":
		fs.Build = b.witnessBuild
	default:
		panic("unknown section " + section)
	}
	return fs.Init()
}

var _ = db.Decimal{}

package deeds

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/shahinshabab/deedflow-go/internal/models"
	"github.com/shahinshabab/deedflow-go/internal/pyjson"
)

// DeedData is a deed with everything hanging off it.
type DeedData struct {
	Deed           *models.Deed
	Registration   *models.RegistrationDetail
	Presenter      *models.Presenter
	PriorDocuments []models.PriorDocument
	Executants     []models.Party
	Claimants      []models.Party
	Schedule       []models.ScheduleItem
	Witnesses      []models.Witness
	Screenshots    []models.DeedScreenshot
}

// The portal payload: exactly the field values selenium automation types, as
// plain strings. The JSON names are the Python dict keys (the fingerprints
// that decide which pages were edited hash this JSON).

type RegistrationPayload struct {
	District            string `json:"district"`
	SRO                 string `json:"sro"`
	Taluk               string `json:"taluk"`
	BookLabel           string `json:"book_label"`
	RegistrationType    string `json:"registration_type"`
	RegistrationSubType string `json:"registration_sub_type"`
}

type PresenterPayload struct {
	Type           string `json:"type"`
	Date           string `json:"date"`
	Name           string `json:"name"`
	Gender         string `json:"gender"`
	Age            string `json:"age"`
	Relative       string `json:"relative"`
	Relation       string `json:"relation"`
	Job            string `json:"job"`
	HouseName      string `json:"house_name"`
	Place          string `json:"place"`
	Post           string `json:"post"`
	Pin            string `json:"pin"`
	Mobile         string `json:"mobile"`
	IDType         string `json:"id_type"`
	IDNumber       string `json:"id_number"`
	DocumentValue  string `json:"document_value"`
	StampDate      string `json:"stamp_date"`
	DocumentSheet  string `json:"document_sheet"`
	DocumentFees   string `json:"document_fees"`
	StampMode      string `json:"stamp_mode"`
	DocumentCopies int    `json:"document_copies"`
}

type PriorDocPayload struct {
	Type     string `json:"type"`
	Year     string `json:"year"`
	Book     string `json:"book"`
	Number   string `json:"number"`
	Volume   string `json:"volume"`
	PageFrom string `json:"pagefrom"`
	PageTo   string `json:"pageto"`
	District string `json:"district"`
	SRO      string `json:"sro"`
	Remarks  string `json:"remarks"`
}

type PartyPayload struct {
	Name          string `json:"name"`
	Age           string `json:"age"`
	Relative      string `json:"relative"`
	Relation      string `json:"relation"`
	Job           string `json:"job"`
	HouseName     string `json:"house_name"`
	Post          string `json:"post"`
	Place         string `json:"place"`
	Pin           string `json:"pin"`
	Mobile        string `json:"mobile"`
	Gender        string `json:"gender"`
	District      string `json:"district"`
	Taluk         string `json:"taluk"`
	Village       string `json:"village"`
	IDType        string `json:"id_type"`
	IDNumber      string `json:"id_number"`
	OutsideKerala bool   `json:"outside_kerala,omitempty"`
}

// ExecutantPayload is a party with the capacity it signs in.
type ExecutantPayload struct {
	PartyPayload
	Type string `json:"type"`
}

type SchedulePayload struct {
	Number              string `json:"number"`
	District            string `json:"district"`
	SRO                 string `json:"sro"`
	Taluk               string `json:"taluk"`
	Village             string `json:"village"`
	Desam               string `json:"desam"`
	LBody               string `json:"lobody"`
	LBodyName           string `json:"lbodyname"`
	ResurveyBlock       string `json:"resurveyblock"`
	ResurveyNumber      string `json:"resurveynumber"`
	ResurveySub         string `json:"resurveysub"`
	OldSurvey           string `json:"oldsurvey"`
	OldSub              string `json:"oldsub"`
	TPNumber            string `json:"tpnumber"`
	LandType            string `json:"landtype"`
	TransactionTypeCode string `json:"transaction_type_code"`
	TotalHector         string `json:"totalhector"`
	TotalAre            string `json:"totalare"`
	TotalMeter          string `json:"totalmeter"`
	TransferHector      string `json:"transferhector"`
	TransferAre         string `json:"transferare"`
	TransferMeter       string `json:"transfermeter"`
	Price               string `json:"price"`
	FairValue           string `json:"fairvalue"`
	Details             string `json:"details"`
	East                string `json:"east"`
	North               string `json:"north"`
	West                string `json:"west"`
	South               string `json:"south"`
	Pokuvarav           string `json:"pokuvarav"`
	UndividedShare      string `json:"undividedshare"`
	Share               string `json:"share"`
	ShareOutOf          string `json:"shareoutof"`
}

type WitnessPayload struct {
	Name      string `json:"name"`
	Gender    string `json:"gender"`
	Relation  string `json:"relation"`
	RName     string `json:"rname"`
	HouseName string `json:"house_name"`
	Job       string `json:"job"`
	Place     string `json:"place"`
	Desam     string `json:"desam"`
	WType     string `json:"wtype"`
}

// Payload is everything sent to the portal for one deed.
type Payload struct {
	Registration         RegistrationPayload `json:"registration"`
	Presenter            PresenterPayload    `json:"presenter"`
	PriorDocuments       []PriorDocPayload   `json:"prior_documents"`
	Executants           []ExecutantPayload  `json:"executants"`
	Claimants            []PartyPayload      `json:"claimants"`
	Schedule             []SchedulePayload   `json:"schedule"`
	AttestingWitnesses   []WitnessPayload    `json:"attesting_witnesses"`
	IdentifyingWitnesses []WitnessPayload    `json:"identifying_witnesses"`
}

// --- conversion helpers (the _s/_plain/_digits family) -------------------------

func s(v string) string { return strings.TrimSpace(v) }

func sInt16(p *int16) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(int(*p))
}

func sInt32(p *int32) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(int(*p))
}

// plain keeps letters (any script, with their vowel signs), digits and single
// spaces only - punctuation becomes a space - for the free-text fields the
// portal checks for "invalid characters".
func plain(v string) string {
	var b strings.Builder
	for _, r := range s(v) {
		if unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsNumber(r) || r == '‌' || r == '‍' {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	return CollapseSpaces(b.String())
}

func digits(v string) string {
	var b strings.Builder
	for _, r := range s(v) {
		if unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func mobile(v string) string {
	d := []rune(digits(v))
	if len(d) > 10 {
		d = d[len(d)-10:]
	}
	return string(d)
}

var spaceOrDash = regexp.MustCompile(`[\s-]`)

func isDigits(v string) bool {
	if v == "" {
		return false
	}
	for _, r := range v {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// idNumber: Aadhaar is often written "1234 5678 9012" - the portal wants the
// bare 12 digits. Other ID numbers are left as written.
func idNumber(v string) string {
	raw := s(v)
	compact := spaceOrDash.ReplaceAllString(raw, "")
	if isDigits(compact) {
		return compact
	}
	return raw
}

func decOf(v string) *big.Rat { return decimalOf(s(v)) }

// area normalizes an area into the portal's hectare / are / sq.m split. The
// portal's hectare and are fields accept whole digits only and its own
// validateAre() carries anything over 99 are into hectares, so the whole area
// is converted to square metres and split back (1 ha = 100 are, 1 are = 100
// sq.m): whole hectares, whole are (0-99), and the remainder in sq.m (up to 4
// decimals).
func area(hectare, are, sqm string) (string, string, string) {
	if s(hectare) == "" && s(are) == "" && s(sqm) == "" {
		return "", "", ""
	}
	total := new(big.Rat).Mul(decOf(hectare), big.NewRat(10000, 1))
	total.Add(total, new(big.Rat).Mul(decOf(are), big.NewRat(100, 1)))
	total.Add(total, decOf(sqm))
	wholeH := truncRat(new(big.Rat).Quo(total, big.NewRat(10000, 1)))
	rest := new(big.Rat).Sub(total, new(big.Rat).SetInt(new(big.Int).Mul(wholeH, big.NewInt(10000))))
	wholeA := truncRat(new(big.Rat).Quo(rest, big.NewRat(100, 1)))
	sq := new(big.Rat).Sub(rest, new(big.Rat).SetInt(new(big.Int).Mul(wholeA, big.NewInt(100))))
	return wholeH.String(), wholeA.String(), formatQuantized(sq)
}

func truncRat(r *big.Rat) *big.Int {
	q := new(big.Int).Quo(r.Num(), r.Denom()) // truncates toward zero
	return q
}

// formatQuantized is format(d.quantize(Decimal("0.0001")).normalize(), "f"):
// rounded half-even to 4 places, trailing zeros dropped.
func formatQuantized(r *big.Rat) string {
	scaled := new(big.Rat).Mul(r, big.NewRat(10000, 1))
	n := roundHalfEven(scaled)
	neg := n.Sign() < 0
	n.Abs(n)
	str := n.String()
	for len(str) < 5 {
		str = "0" + str
	}
	whole, frac := str[:len(str)-4], strings.TrimRight(str[len(str)-4:], "0")
	out := whole
	if frac != "" {
		out += "." + frac
	}
	if neg && out != "0" {
		out = "-" + out
	}
	return out
}

func roundHalfEven(r *big.Rat) *big.Int {
	num, den := new(big.Int).Set(r.Num()), r.Denom()
	q, rem := new(big.Int).QuoRem(num, den, new(big.Int))
	twice := new(big.Int).Mul(new(big.Int).Abs(rem), big.NewInt(2))
	switch twice.Cmp(den) {
	case 1:
		if num.Sign() < 0 {
			q.Sub(q, big.NewInt(1))
		} else {
			q.Add(q, big.NewInt(1))
		}
	case 0:
		if q.Bit(0) == 1 {
			if num.Sign() < 0 {
				q.Sub(q, big.NewInt(1))
			} else {
				q.Add(q, big.NewInt(1))
			}
		}
	}
	return q
}

func date(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("02/01/2006")
}

var portalSpecialChars = regexp.MustCompile("[<>'!$%\\-();=#^&|/\\\\\":_@*\\[\\]{},.+?~`]")

// PortalPlainText replaces the characters the portal's _isSpclChar() refuses
// in a free-text field with spaces.
func PortalPlainText(text string) string {
	return CollapseSpaces(portalSpecialChars.ReplaceAllString(text, " "))
}

// BoundaryFallback is the office's text for a boundary the deed leaves blank
// ("......"): "add the boundaries".
const BoundaryFallback = "അതിരുകള് ചേര്ക്കുക"

func boundary(v string) string {
	if t := PortalPlainText(v); t != "" {
		return t
	}
	return BoundaryFallback
}

// firstSurveyValue: the portal's survey number / sub-division fields take one
// value of [0-9A-Za-z/] only: a parcel cut from several old survey numbers
// ("100, 101" / "1, 1B") sends the first, and a draft's blank dots are dropped.
func firstSurveyValue(v string) string {
	str := s(v)
	end := len(str)
	for i, r := range str {
		if r == ',' || isPySpace(r) {
			end = i
			break
		}
	}
	first := str[:end]
	var b strings.Builder
	for _, r := range first {
		if r < 128 && (r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r == '/') {
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), "/")
}

var keralaDistricts = func() map[string]bool {
	m := map[string]bool{}
	for _, d := range KeralaDistricts {
		m[Normalize(d.Name)] = true
	}
	return m
}()

// outsideKerala: Kerala PIN codes run 670000-695999; without a PIN, a district
// that isn't one of Kerala's decides.
func outsideKerala(pin, district string) bool {
	if len(pin) == 6 {
		n, err := strconv.Atoi(pin[:3])
		if err == nil {
			return !(n >= 670 && n <= 695)
		}
	}
	return district != "" && !keralaDistricts[Normalize(district)]
}

func partyPayload(p *models.Party) PartyPayload {
	pin, district := digits(p.Pin), s(p.District)
	out := PartyPayload{
		Name: s(p.Name), Age: sInt16(p.Age), Relative: s(p.RelativeName), Relation: s(p.Relation),
		Job: plain(p.Job), HouseName: s(p.HouseName), Post: s(p.Post), Place: s(p.Place), Pin: digits(p.Pin),
		Mobile: mobile(p.Mobile), Gender: s(p.Gender), District: s(p.District), Taluk: s(p.Taluk), Village: s(p.Village),
		IDType: s(p.IDType), IDNumber: idNumber(p.IDNumber),
	}
	if outsideKerala(pin, district) {
		out.OutsideKerala = true
	}
	return out
}

var digitRun = func(str string) []string {
	var runs []string
	var cur strings.Builder
	for _, r := range str {
		if unicode.IsDigit(r) {
			cur.WriteRune(r)
		} else if cur.Len() > 0 {
			runs = append(runs, cur.String())
			cur.Reset()
		}
	}
	if cur.Len() > 0 {
		runs = append(runs, cur.String())
	}
	return runs
}

// sequentialNumbers: the portal wants schedule numbers in sequence from 1
// ("Pattika number should be in Sequential Order"); rows sharing a number keep
// sharing it.
func sequentialNumbers(items []SchedulePayload) {
	renumbered := map[string]int{}
	for i := range items {
		n, ok := renumbered[items[i].Number]
		if !ok {
			n = len(renumbered) + 1
			renumbered[items[i].Number] = n
		}
		items[i].Number = strconv.Itoa(n)
	}
}

// ContinuationRows are the positions of a schedule's extra old survey /
// sub-division rows: a row repeating an earlier row's schedule number with no
// resurvey number.
func ContinuationRows(schedule []SchedulePayload) map[int]bool {
	seen := map[string]bool{}
	rows := map[int]bool{}
	for i, it := range schedule {
		if seen[it.Number] && it.ResurveyNumber == "" {
			rows[i] = true
		}
		seen[it.Number] = true
	}
	return rows
}

func hasAlpha(v string) bool {
	for _, r := range v {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

func hasDigit(v string) bool {
	for _, r := range v {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

var allZeros = regexp.MustCompile(`^0+$`)

// saysNoResurvey: a resurvey number in words with no digits ("ഇല്ല", "Nil") or
// 0 is the deed saying the land was never resurveyed.
func saysNoResurvey(v string) bool {
	text := s(v)
	words := hasAlpha(text) && !hasDigit(text)
	return words || allZeros.MatchString(firstSurveyValue(text))
}

// OldSurveyOnly: land the government never resurveyed - an old survey number
// with the resurvey block at "---" and no resurvey number.
func OldSurveyOnly(it SchedulePayload) bool {
	return it.OldSurvey != "" && it.ResurveyBlock == "" && it.ResurveyNumber == ""
}

func schedulePayload(it *models.ScheduleItem, transCode string) SchedulePayload {
	th, ta, tm := area(decPtr(it.TotalHectare), decPtr(it.TotalAre), decPtr(it.TotalSqm))
	xh, xa, xm := area(decPtr(it.TransferHectare), decPtr(it.TransferAre), decPtr(it.TransferSqm))
	rb, rn, rs := s(it.ResurveyBlock), firstSurveyValue(it.ResurveyNumber), firstSurveyValue(it.ResurveySub)
	if saysNoResurvey(it.ResurveyNumber) {
		rb, rn, rs = "", "", ""
	}
	number := s(it.Number)
	if runs := digitRun(number); len(runs) > 0 {
		number = runs[0]
	}
	pok := s(it.Pokkuvaravu)
	if pok == "" {
		pok = "No"
	}
	und := s(it.UndividedShare)
	if und == "" {
		und = "No"
	}
	return SchedulePayload{
		Number: number, District: s(it.District), SRO: s(it.SRO), Taluk: s(it.Taluk), Village: s(it.Village),
		Desam: s(it.Desam), LBody: s(it.LocalBodyType), LBodyName: s(it.LocalBodyName),
		ResurveyBlock: rb, ResurveyNumber: rn, ResurveySub: rs,
		OldSurvey: firstSurveyValue(it.OldSurvey), OldSub: firstSurveyValue(it.OldSub), TPNumber: s(it.TPNumber),
		LandType: s(it.LandType), TransactionTypeCode: transCode,
		TotalHector: th, TotalAre: ta, TotalMeter: tm, TransferHector: xh, TransferAre: xa, TransferMeter: xm,
		Price: digits(sInt32(it.Price)), FairValue: digits(sInt32(it.FairValue)), Details: s(it.Details),
		East: boundary(it.BoundaryEast), North: boundary(it.BoundaryNorth), West: boundary(it.BoundaryWest),
		South: boundary(it.BoundarySouth), Pokuvarav: pok, UndividedShare: und, Share: s(it.Share), ShareOutOf: s(it.ShareOutOf),
	}
}

// --- gaps ------------------------------------------------------------------------
//
// Draft deeds leave fields blank ("......") that the portal won't save without.
// These are the office's placeholders for them: obviously-fake values in the
// portal's own formats, so a token with one is easy to spot and fix when it's
// opened again. They're applied only to what's sent - the deed itself keeps
// its blanks, so the review page still shows what's missing.

const (
	PlaceholderAge          = "18"
	PlaceholderMobile       = "9999999999"
	PlaceholderHouse        = "വിലാസം ചേർക്കുക"
	PlaceholderJob          = "തൊഴിൽ ചേർക്കുക"
	PlaceholderSurveyNumber = "9999"
	PlaceholderPriorNumber  = "9999"
	PlaceholderPriorYear    = "1900"
	PriorDocumentNote       = "പി ആര് ശെരിയാക്കുക"
	PlaceholderFairValue    = "1000" // per are
	PlaceholderPrice        = "9999"
)

// The relative's name when the deed leaves it out: the relation itself.
var relativeByRelation = map[string]string{"മകന്‍": "അച്ഛൻ", "മകള്‍": "അച്ഛൻ", "ഭാര്യ": "ഭർത്താവ്"}

type idPlaceholder struct {
	pattern     *regexp.Regexp
	placeholder string
}

// Placeholder ID per type, each in that ID's real format so the portal's own
// format check passes.
var idPlaceholders = func() map[string]idPlaceholder {
	c := IDTypeChoices
	mk := func(p string) *regexp.Regexp { return regexp.MustCompile("^(?:" + p + ")$") }
	return map[string]idPlaceholder{
		c[0].Value: {mk(`[0-9]{12}`), "999999999999"},           // Aadhaar
		c[1].Value: {mk(`.{6,}`), "KL9999999999999"},            // Driving licence
		c[2].Value: {mk(`[A-Z]{3}[0-9]{7}`), "TJO9999999"},      // Voter ID (EPIC)
		c[3].Value: {mk(`[A-Z]{5}[0-9]{4}[A-Z]`), "ZZZZZ9999Z"}, // PAN
		c[4].Value: {mk(`[A-Z][0-9]{7}`), "A9999999"},           // Passport
		c[5].Value: {mk(`[0-9]{10}`), "9999999999"},             // Ration card
	}
}()

var anyID = idPlaceholder{regexp.MustCompile(`^(?:.+)$`), "9999999999"}

// partyGaps fills the blanks of a party-like record; job is filled only when
// withJob (the presenter payload has a job key too, so it always is).
func partyGaps(age, mob, house, job, relative, relation, idType, idNum *string, withJob bool) {
	if *age == "" {
		*age = PlaceholderAge
	}
	if *mob == "" {
		*mob = PlaceholderMobile
	}
	if *house == "" {
		*house = PlaceholderHouse
	}
	if withJob && *job == "" {
		*job = PlaceholderJob
	}
	if *relative == "" {
		if r, ok := relativeByRelation[*relation]; ok {
			*relative = r
		} else {
			*relative = "ബന്ധു"
		}
	}
	if *idType == "" {
		*idType = IDTypeChoices[0].Value
	}
	ph, ok := idPlaceholders[*idType]
	if !ok {
		ph = anyID
	}
	if !ph.pattern.MatchString(strings.ToUpper(*idNum)) {
		*idNum = ph.placeholder
	}
}

func fillPartyGaps(p *PartyPayload) {
	partyGaps(&p.Age, &p.Mobile, &p.HouseName, &p.Job, &p.Relative, &p.Relation, &p.IDType, &p.IDNumber, true)
}

func fillPriorDocumentGaps(d *PriorDocPayload) {
	registered := d.Type == "രജിസ്റ്റർ/ഫയൽ ചെയ്ത" || d.Type == "വിൽ"
	gaps := d.Number == "" || d.Year == "" || (registered && d.Book == "")
	if d.Number == "" {
		d.Number = PlaceholderPriorNumber
	}
	if d.Year == "" {
		d.Year = PlaceholderPriorYear
	}
	if registered && d.Book == "" {
		d.Book = "1"
	}
	if gaps {
		d.Remarks = strings.TrimSpace(d.Remarks + " " + PriorDocumentNote)
	}
}

func whole(v string) int64 {
	r := decOf(v)
	return truncRat(r).Int64()
}

// FillGaps puts the office's placeholders into whatever the deed left blank.
func FillGaps(p *Payload) {
	pr := &p.Presenter
	partyGaps(&pr.Age, &pr.Mobile, &pr.HouseName, &pr.Job, &pr.Relative, &pr.Relation, &pr.IDType, &pr.IDNumber, true)
	for i := range p.Executants {
		fillPartyGaps(&p.Executants[i].PartyPayload)
	}
	for i := range p.Claimants {
		fillPartyGaps(&p.Claimants[i])
	}
	for i := range p.PriorDocuments {
		fillPriorDocumentGaps(&p.PriorDocuments[i])
	}
	cont := ContinuationRows(p.Schedule)
	for i := range p.Schedule {
		it := &p.Schedule[i]
		if !cont[i] && !OldSurveyOnly(*it) && it.ResurveyNumber == "" {
			it.ResurveyNumber = PlaceholderSurveyNumber
		}
		if it.FairValue == "" {
			it.FairValue = PlaceholderFairValue
		}
		if it.Price == "" {
			// The least the portal accepts: fair value x the land transferred.
			if m, ok := MinimumPrice(it.FairValue,
				Area{it.TransferHector, it.TransferAre, it.TransferMeter},
				Area{it.TotalHector, it.TotalAre, it.TotalMeter}); ok && m != 0 {
				it.Price = strconv.FormatInt(m, 10)
			} else {
				it.Price = PlaceholderPrice
			}
		}
	}
	// The portal wants the document value to equal the prices' total.
	if pr.DocumentValue == "" {
		var total int64
		for _, it := range p.Schedule {
			total += whole(it.Price)
		}
		if total != 0 {
			pr.DocumentValue = strconv.FormatInt(total, 10)
		} else {
			pr.DocumentValue = PlaceholderPrice
		}
	}
	if pr.DocumentFees == "" {
		pr.DocumentFees = pr.DocumentValue
	}
}

// ErrIncomplete means a deed has no registration or presenter yet.
var ErrIncomplete = errors.New("the deed has no registration or presenter yet")

// BuildPayload turns a deed into the portal's field values.
func BuildPayload(d *DeedData) (*Payload, error) {
	if d.Registration == nil || d.Presenter == nil {
		return nil, ErrIncomplete
	}
	reg := d.Registration
	info := PortalRegistrationType(reg.DeedType, reg.DeedSubType)
	transCode := SubtypeTransactionCode(reg.DeedType, reg.DeedSubType)
	pr := d.Presenter

	witness := func(w *models.Witness) WitnessPayload {
		wt := s(w.PortalWitnessType)
		if wt == "" {
			wt = "WB"
		}
		return WitnessPayload{Name: s(w.Name), Gender: s(w.Gender), Relation: s(w.Relation), RName: s(w.RelativeName),
			HouseName: s(w.HouseName), Job: plain(w.Job), Place: s(w.Place), Desam: s(w.Desam), WType: wt}
	}

	p := &Payload{
		Registration: RegistrationPayload{
			District: s(reg.District), SRO: s(reg.SRO), Taluk: s(reg.Taluk), BookLabel: BookPortalLabel(int(reg.Book)),
			RegistrationType: info.RegistrationType, RegistrationSubType: info.RegistrationSubType,
		},
		Presenter: PresenterPayload{
			Type: s(pr.PresenterType), Date: date(pr.PresenterDate), Name: s(pr.Name), Gender: s(pr.Gender),
			Age: sInt16(pr.Age), Relative: s(pr.RelativeName), Relation: s(pr.Relation), Job: plain(pr.Job),
			HouseName: s(pr.HouseName), Place: s(pr.Place), Post: s(pr.Post), Pin: digits(pr.Pin), Mobile: mobile(pr.Mobile),
			IDType: s(pr.IDType), IDNumber: idNumber(pr.IDNumber), DocumentValue: sInt32(pr.DocumentValue),
			StampDate: date(pr.StampDate), DocumentSheet: sInt16(pr.DocumentSheetCount), DocumentFees: sInt32(pr.DocumentFees),
			StampMode: s(pr.StampMode), DocumentCopies: int(pr.DocumentCopies),
		},
		PriorDocuments:       []PriorDocPayload{},
		Executants:           []ExecutantPayload{},
		Claimants:            []PartyPayload{},
		Schedule:             []SchedulePayload{},
		AttestingWitnesses:   []WitnessPayload{},
		IdentifyingWitnesses: []WitnessPayload{},
	}
	for i := range d.PriorDocuments {
		doc := &d.PriorDocuments[i]
		typ := MatchPriorDocumentType(doc.DocumentType, false)
		if typ == "" {
			typ = s(doc.DocumentType)
		}
		p.PriorDocuments = append(p.PriorDocuments, PriorDocPayload{
			Type: typ, Year: sInt16(doc.Year), Book: sInt16(doc.Book),
			Number: PortalPlainText(doc.Number), // "LR6 83758/78": the portal refuses / here
			Volume: sInt32(doc.Volume), PageFrom: sInt32(doc.PageFrom), PageTo: sInt32(doc.PageTo),
			District: s(doc.District), SRO: s(doc.SRO), Remarks: PortalPlainText(doc.Remarks),
		})
	}
	for i := range d.Executants {
		e := &d.Executants[i]
		p.Executants = append(p.Executants, ExecutantPayload{PartyPayload: partyPayload(e), Type: s(e.Capacity)})
	}
	for i := range d.Claimants {
		p.Claimants = append(p.Claimants, partyPayload(&d.Claimants[i]))
	}
	for i := range d.Schedule {
		p.Schedule = append(p.Schedule, schedulePayload(&d.Schedule[i], transCode))
	}
	sequentialNumbers(p.Schedule)
	for i := range d.Witnesses {
		w := &d.Witnesses[i]
		switch w.WitnessType {
		case "attesting":
			p.AttestingWitnesses = append(p.AttestingWitnesses, witness(w))
		case "identifying":
			p.IdentifyingWitnesses = append(p.IdentifyingWitnesses, witness(w))
		}
	}
	FillGaps(p)
	return p, nil
}

// --- sections, fingerprints, row edits ---------------------------------------------

// Section is one portal category: its key, label and the wizard step editing it.
type Section struct{ Key, Label, Step string }

// PortalSections are the categories a deed is submitted in, in portal order.
var PortalSections = []Section{
	{"registration", "Registration", "registration"},
	{"presenter", "Presenter", "presenter"},
	{"prior_documents", "Prior Documents", "documents"},
	{"executants", "Executants", "executants"},
	{"claimants", "Claimants", "claimants"},
	{"schedule", "Schedule", "schedule"},
	{"witnesses", "Witnesses", "witnesses"},
}

// SectionLabel is a section's display name.
func SectionLabel(key string) string {
	for _, sec := range PortalSections {
		if sec.Key == key {
			return sec.Label
		}
	}
	return key
}

// SectionForStep maps a wizard step to its section key.
func SectionForStep(step string) string {
	for _, sec := range PortalSections {
		if sec.Step == step {
			return sec.Key
		}
	}
	return ""
}

func fingerprintOf(v any) string {
	sum := sha256.Sum256([]byte(pyjson.MustDumps(v)))
	return hex.EncodeToString(sum[:])
}

// SectionFingerprints hashes exactly what would be sent to the portal for each
// section. Comparing against what was last confirmed saved tells which
// sections were really edited (not just opened and re-saved unchanged).
func SectionFingerprints(p *Payload) map[string]string {
	return map[string]string{
		"registration":    fingerprintOf(p.Registration),
		"presenter":       fingerprintOf(p.Presenter),
		"prior_documents": fingerprintOf(p.PriorDocuments),
		"executants":      fingerprintOf(p.Executants),
		"claimants":       fingerprintOf(p.Claimants),
		"schedule":        fingerprintOf(p.Schedule),
		"witnesses":       fingerprintOf([]any{p.AttestingWitnesses, p.IdentifyingWitnesses}),
	}
}

// RowLists are the payload lists of each repeatable section (witnesses are two
// tabs: attesting and identifying).
var RowLists = []struct {
	Section string
	Lists   []string
}{
	{"prior_documents", []string{"prior_documents"}},
	{"executants", []string{"executants"}},
	{"claimants", []string{"claimants"}},
	{"schedule", []string{"schedule"}},
	{"witnesses", []string{"attesting_witnesses", "identifying_witnesses"}},
}

// ListsOf returns the payload lists of a section (nil if not repeatable).
func ListsOf(section string) []string {
	for _, r := range RowLists {
		if r.Section == section {
			return r.Lists
		}
	}
	return nil
}

// RowListLabels name a row list's rows.
var RowListLabels = map[string]string{
	"prior_documents": "Prior document", "executants": "Executant", "claimants": "Claimant",
	"schedule": "Property", "attesting_witnesses": "Attesting witness", "identifying_witnesses": "Identifying witness",
}

// RowCount is the number of rows of a payload list.
func (p *Payload) RowCount(list string) int {
	switch list {
	case "prior_documents":
		return len(p.PriorDocuments)
	case "executants":
		return len(p.Executants)
	case "claimants":
		return len(p.Claimants)
	case "schedule":
		return len(p.Schedule)
	case "attesting_witnesses":
		return len(p.AttestingWitnesses)
	case "identifying_witnesses":
		return len(p.IdentifyingWitnesses)
	}
	return 0
}

func (p *Payload) row(list string, i int) any {
	switch list {
	case "prior_documents":
		return p.PriorDocuments[i]
	case "executants":
		return p.Executants[i]
	case "claimants":
		return p.Claimants[i]
	case "schedule":
		return p.Schedule[i]
	case "attesting_witnesses":
		return p.AttestingWitnesses[i]
	default:
		return p.IdentifyingWitnesses[i]
	}
}

// RowKey is what identifies a row in the portal's saved-rows table (and so
// lets the edit flow check it's about to change the right one): the person's
// name, or a document's / property's number.
func (p *Payload) RowKey(list string, i int) string {
	switch list {
	case "prior_documents":
		return p.PriorDocuments[i].Number
	case "schedule":
		it := p.Schedule[i]
		switch {
		case it.ResurveyNumber != "":
			return it.ResurveyNumber
		case it.OldSurvey != "":
			return it.OldSurvey
		}
		return it.Number
	case "executants":
		return p.Executants[i].Name
	case "claimants":
		return p.Claimants[i].Name
	case "attesting_witnesses":
		return p.AttestingWitnesses[i].Name
	default:
		return p.IdentifyingWitnesses[i].Name
	}
}

// RowChange is one edited or added row.
type RowChange struct {
	Index  int
	Kind   string // "edit" or "add"
	OldKey string
}

// DescribeRowChange: "Executant 2 (സഫിയ)", or "new Executant 3 (...)" for an added row.
func DescribeRowChange(list string, ch RowChange, p *Payload) string {
	key := p.RowKey(list, ch.Index)
	label := fmt.Sprintf("%s %d", RowListLabels[list], ch.Index+1)
	if key != "" {
		label += " (" + key + ")"
	}
	if ch.Kind == "edit" {
		return label
	}
	return "new " + label
}

// RowFingerprints: payload list -> [{fp, key}] per row, in order.
func RowFingerprints(p *Payload) map[string][]models.RowFP {
	out := map[string][]models.RowFP{}
	for _, r := range RowLists {
		for _, list := range r.Lists {
			rows := make([]models.RowFP, p.RowCount(list))
			for i := range rows {
				rows[i] = models.RowFP{FP: fingerprintOf(p.row(list, i)), Key: p.RowKey(list, i)}
			}
			out[list] = rows
		}
	}
	return out
}

// RowPlan is payload list -> its changed rows, in list order.
type RowPlan struct {
	Lists   []string
	Changes map[string][]RowChange
}

// PlanRowEdits splits the edited sections of an already-submitted deed into
// the ones that can be sent row by row and the ones that can't. A section goes
// row by row when the portal's rows for it are known (recorded at the last
// confirmed submission) and the change is only edits of existing rows and/or
// rows added at the end. Removed rows, or no record of the old rows, mean the
// whole tab is redone; so do PR documents (saving one can make the portal add
// executants of its own) and the presenter.
func PlanRowEdits(snapshot models.Snapshot, p *Payload, sections []string) (RowPlan, []string) {
	plan := RowPlan{Changes: map[string][]RowChange{}}
	var whole []string
	now := RowFingerprints(p)
	for _, section := range sections {
		lists := ListsOf(section)
		skip := lists == nil || section == "prior_documents"
		if !skip {
			for _, name := range lists {
				if _, ok := snapshot.Rows[name]; !ok {
					skip = true
				}
			}
		}
		if skip {
			whole = append(whole, section)
			continue
		}
		sectionChanges := map[string][]RowChange{}
		ok := true
		for _, name := range lists {
			then, cur := snapshot.Rows[name], now[name]
			if len(cur) < len(then) {
				ok = false
				break
			}
			var changes []RowChange
			for i := range then {
				if then[i].FP != cur[i].FP {
					changes = append(changes, RowChange{Index: i, Kind: "edit", OldKey: then[i].Key})
				}
			}
			for i := len(then); i < len(cur); i++ {
				changes = append(changes, RowChange{Index: i, Kind: "add"})
			}
			if len(changes) > 0 {
				sectionChanges[name] = changes
			}
		}
		if !ok {
			whole = append(whole, section)
			continue
		}
		for _, name := range lists {
			if ch, has := sectionChanges[name]; has {
				plan.Lists = append(plan.Lists, name)
				plan.Changes[name] = ch
			}
		}
	}
	return plan, whole
}

// PendingSections: sections whose current data isn't what's on the portal yet
// - all of them before the first submission, afterwards only the edited ones
// (or ones whose last submission attempt failed).
func PendingSections(d *DeedData) []string {
	p, err := BuildPayload(d)
	if err != nil {
		// Deed not complete enough to build a payload (e.g. still a draft).
		var all []string
		for _, sec := range PortalSections {
			all = append(all, sec.Key)
		}
		return all
	}
	current := SectionFingerprints(p)
	var out []string
	for _, sec := range PortalSections {
		if d.Deed.PortalSnapshot.Sections[sec.Key] != current[sec.Key] {
			out = append(out, sec.Key)
		}
	}
	return out
}

// PendingChanges is what a resubmission will send, for display: rows edited
// one by one (e.g. "Executant 2 (സഫിയ)") and pages redone as a whole.
func PendingChanges(d *DeedData) (rows, wholeLabels []string) {
	sections := PendingSections(d)
	p, err := BuildPayload(d)
	if err != nil {
		for _, sec := range sections {
			wholeLabels = append(wholeLabels, SectionLabel(sec))
		}
		return nil, wholeLabels
	}
	plan, whole := PlanRowEdits(d.Deed.PortalSnapshot, p, sections)
	for _, list := range plan.Lists {
		for _, ch := range plan.Changes[list] {
			rows = append(rows, DescribeRowChange(list, ch, p))
		}
	}
	for _, sec := range whole {
		wholeLabels = append(wholeLabels, SectionLabel(sec))
	}
	return rows, wholeLabels
}

// RecordRows copies the current rows of sections into the snapshot (once those
// sections are confirmed on the portal as they now stand).
func RecordRows(snap models.Snapshot, p *Payload, sections []string) models.Snapshot {
	snap = snap.Clone()
	cur := RowFingerprints(p)
	if snap.Rows == nil {
		snap.Rows = map[string][]models.RowFP{}
	}
	for _, sec := range sections {
		for _, name := range ListsOf(sec) {
			snap.Rows[name] = cur[name]
		}
	}
	return snap
}

// EnsurePortalBaseline, for a deed that already has a token but no recorded
// fingerprints (submitted before edit-tracking existed): a completed deed is
// on the portal as it stands, so its current data becomes the baseline; and
// registration is always taken as-is once a token exists, since the portal
// can't change it on that token anyway. Likewise the row-by-row record of any
// section that's in sync with the portal but was confirmed before rows were
// recorded. It returns the new snapshot and whether it changed.
func EnsurePortalBaseline(d *DeedData) (models.Snapshot, bool) {
	snap := d.Deed.PortalSnapshot
	if d.Deed.TokenNumber == "" {
		return snap, false
	}
	p, err := BuildPayload(d)
	if err != nil {
		return snap, false
	}
	next := snap.Clone()
	current := SectionFingerprints(p)
	if next.IsEmpty() && d.Deed.Status == models.StatusCompleted {
		for k, v := range current {
			next.Sections[k] = v
		}
	} else if _, ok := next.Sections["registration"]; !ok {
		next.Sections["registration"] = current["registration"]
	}
	var inSync []string
	for _, r := range RowLists {
		if next.Sections[r.Section] != current[r.Section] {
			continue
		}
		for _, name := range r.Lists {
			if _, ok := next.Rows[name]; !ok {
				inSync = append(inSync, r.Section)
				break
			}
		}
	}
	if len(inSync) > 0 {
		next = RecordRows(next, p, inSync)
	}
	if fingerprintOf(next) == fingerprintOf(snap) {
		return snap, false
	}
	return next, true
}

// --- preflight --------------------------------------------------------------------

type requiredField struct {
	get   func(*PresenterPayload) string
	label string
}

var presenterRequired = []requiredField{
	{func(p *PresenterPayload) string { return p.Name }, "name"},
	{func(p *PresenterPayload) string { return p.Age }, "age"},
	{func(p *PresenterPayload) string { return p.Mobile }, "mobile number"},
	{func(p *PresenterPayload) string { return p.IDNumber }, "ID card number"},
	{func(p *PresenterPayload) string { return p.DocumentValue }, "document value"},
	{func(p *PresenterPayload) string { return p.DocumentFees }, "amount for stamp duty and fees"},
}

// comma formats n with thousands separators (Python's {n:,}).
func comma(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	str := strconv.FormatInt(n, 10)
	var parts []string
	for len(str) > 3 {
		parts = append([]string{str[len(str)-3:]}, parts...)
		str = str[:len(str)-3]
	}
	parts = append([]string{str}, parts...)
	out := strings.Join(parts, ",")
	if neg {
		out = "-" + out
	}
	return out
}

var nonDigits = regexp.MustCompile(`\D`)

// PreflightProblems lists data the portal is known to reject, caught before
// anything is sent: a rejection halfway through leaves a token with some pages
// saved and others not. sections limits the check to what's being sent (nil:
// everything). today is the office's current date.
func PreflightProblems(p *Payload, sections []string, today time.Time) []string {
	sending := func(keys ...string) bool {
		if sections == nil {
			return true
		}
		for _, k := range keys {
			for _, s := range sections {
				if s == k {
					return true
				}
			}
		}
		return false
	}
	problems := []string{}
	reg := p.Registration
	if sending("registration") {
		var missing []string
		if reg.District == "" {
			missing = append(missing, "district")
		}
		if reg.SRO == "" {
			missing = append(missing, "SRO")
		}
		if reg.Taluk == "" {
			missing = append(missing, "taluk")
		}
		if len(missing) > 0 {
			problems = append(problems, fmt.Sprintf("Registration: no %s - the portal requires them.", strings.Join(missing, ", ")))
		}
	}
	pr := &p.Presenter
	value := whole(pr.DocumentValue)
	if sending("presenter") {
		var missing []string
		for _, f := range presenterRequired {
			if strings.TrimSpace(f.get(pr)) == "" {
				missing = append(missing, f.label)
			}
		}
		if len(missing) > 0 {
			pron := "it"
			if len(missing) > 1 {
				pron = "them"
			}
			problems = append(problems, fmt.Sprintf("Presenter: %s missing - the portal requires %s.", strings.Join(missing, ", "), pron))
		}
	}
	type partyRef struct {
		name, job, idType, idNumber string
	}
	checkParties := func(section, label string, parties []partyRef, checkJob bool) {
		if !sending(section) {
			return
		}
		for _, party := range parties {
			if checkJob && strings.TrimSpace(party.job) == "" {
				problems = append(problems, fmt.Sprintf("%s %s: no occupation - the portal requires one.", label, party.name))
			}
			number := nonDigits.ReplaceAllString(party.idNumber, "")
			if party.idType == IDTypeChoices[0].Value && number != "" && len([]rune(number)) != 12 {
				problems = append(problems, fmt.Sprintf("%s %s: Aadhaar number %s has %d digits - the portal requires 12.",
					label, party.name, party.idNumber, len([]rune(number))))
			}
		}
	}
	checkParties("presenter", "Presenter", []partyRef{{pr.Name, pr.Job, pr.IDType, pr.IDNumber}}, false)
	var ex, cl []partyRef
	for _, e := range p.Executants {
		ex = append(ex, partyRef{e.Name, e.Job, e.IDType, e.IDNumber})
	}
	for _, c := range p.Claimants {
		cl = append(cl, partyRef{c.Name, c.Job, c.IDType, c.IDNumber})
	}
	checkParties("executants", "Executant", ex, true)
	checkParties("claimants", "Claimant", cl, true)

	if sending("prior_documents") {
		for _, doc := range p.PriorDocuments {
			var missing []string
			if strings.TrimSpace(doc.Number) == "" {
				missing = append(missing, "number")
			}
			if strings.TrimSpace(doc.Year) == "" {
				missing = append(missing, "year")
			}
			if len(missing) > 0 {
				n, y := doc.Number, doc.Year
				if n == "" {
					n = "?"
				}
				if y == "" {
					y = "?"
				}
				problems = append(problems, fmt.Sprintf("Prior document %s/%s: no %s - the portal requires both.", n, y, strings.Join(missing, " or ")))
			}
			if (doc.Type == "രജിസ്റ്റർ/ഫയൽ ചെയ്ത" || doc.Type == "വിൽ") && strings.TrimSpace(doc.Book) == "" {
				problems = append(problems, fmt.Sprintf("Prior document %s/%s: no book number - the portal requires one for a registered document or will.", doc.Number, doc.Year))
			}
		}
	}
	if sending("presenter") && pr.Date != "" {
		if presented, err := time.Parse("02/01/2006", pr.Date); err == nil && presented.Before(today) {
			problems = append(problems, fmt.Sprintf("Presenter: the presentation date (%s) is in the past - the portal only accepts today's date or a later one.", pr.Date))
		}
	}
	if sending("presenter") && pr.StampDate != "" {
		if stamped, err := time.Parse("02/01/2006", pr.StampDate); err == nil && stamped.After(today) {
			problems = append(problems, fmt.Sprintf("Presenter: the stamp date (%s) is in the future - the portal only accepts today's date or an earlier one.", pr.StampDate))
		}
	}
	if sending("presenter") && pr.StampMode == "Physical Paper" {
		fees := whole(pr.DocumentFees)
		if value != 0 && fees < value {
			problems = append(problems, fmt.Sprintf("Presenter: the amount assessed (%d) must be at least the document value (%d).", fees, value))
		}
	}
	if sending("presenter", "schedule") && value != 0 {
		var total int64
		for _, it := range p.Schedule {
			total += whole(it.Price)
		}
		if len(p.Schedule) > 0 && total != value {
			problems = append(problems, fmt.Sprintf("Schedule: the property prices add up to %d, but the document value is %d - the portal requires them to be equal.", total, value))
		}
	}
	cont := ContinuationRows(p.Schedule)
	numOrQ := func(n string) string {
		if n == "" {
			return "?"
		}
		return n
	}
	if sending("schedule") {
		for i, it := range p.Schedule {
			if !cont[i] && whole(it.TotalHector) == 0 && whole(it.TotalAre) == 0 && whole(it.TotalMeter) == 0 {
				problems = append(problems, fmt.Sprintf("Schedule: property %s has no area - the portal requires one.", numOrQ(it.Number)))
			}
			if it.ResurveyNumber == "" && !cont[i] && !OldSurveyOnly(it) {
				problems = append(problems, fmt.Sprintf("Schedule: property %s has no resurvey number - the portal requires one.", numOrQ(it.Number)))
			}
			if it.LandType == "" {
				problems = append(problems, fmt.Sprintf("Schedule: property %s has no land type - the portal requires one.", numOrQ(it.Number)))
			}
			if it.FairValue == "" {
				problems = append(problems, fmt.Sprintf("Schedule: property %s has no fair value - the portal requires one.", numOrQ(it.Number)))
			}
		}
	}
	// The portal only checks this on the Property page, after the Presentation
	// page is already saved - so it's checked here, before either is sent.
	if sending("presenter", "schedule") {
		for i, it := range p.Schedule {
			// The portal values a share itself (see ItemMinimumPrice), and an
			// extra old survey row goes in at 0.
			if it.UndividedShare == "Yes" || cont[i] {
				continue
			}
			minimum, ok := MinimumPrice(it.FairValue,
				Area{it.TransferHector, it.TransferAre, it.TransferMeter},
				Area{it.TotalHector, it.TotalAre, it.TotalMeter})
			if ok && minimum != 0 && whole(it.Price) < minimum {
				problems = append(problems, fmt.Sprintf(
					"Schedule: property %s is priced at %d, below its fair value (%s per are) times the land transferred (%d) - the portal won't accept less.",
					numOrQ(it.Number), whole(it.Price), it.FairValue, minimum))
			}
		}
	}
	return problems
}

// NFC is unicode NFC normalization.
func NFC(s string) string { return norm.NFC.String(s) }

package extract

import (
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/shahinshabab/deedflow-go/internal/db"
	"github.com/shahinshabab/deedflow-go/internal/deeds"
	"github.com/shahinshabab/deedflow-go/internal/models"
	"github.com/shahinshabab/deedflow-go/internal/ordjson"
	"github.com/shahinshabab/deedflow-go/internal/textconv"
	"github.com/shahinshabab/deedflow-go/internal/translit"
)

// tableError is raised (via panic) where Python would hit an IndexError or
// AttributeError on an odd table; ExtractSchedule skips that table.
type tableError string

func fail(format string, args ...any) { panic(tableError(fmt.Sprintf(format, args...))) }

// pv is one value of a parsed schedule cell: a string or a list of strings.
type pv struct {
	s      string
	l      []string
	isList bool
}

func pySplit(s string) []string { return strings.FieldsFunc(s, isPySpace) }

func isPySpace(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }

func pyStrip(s string) string { return strings.TrimFunc(s, isPySpace) }

// customParse splits a cell like "3 5,6 7" into ["3", ["5", "6"], "7"]; a
// lone letter belongs to the token before it ("1 B" is "1B").
func customParse(cell *string) []pv {
	if cell == nil {
		return nil
	}
	var result []pv
	for _, token := range pySplit(*cell) {
		r, n := utf8.DecodeRuneInString(token)
		switch {
		case n == len(token) && unicode.IsLetter(r) && len(result) > 0 && !result[len(result)-1].isList:
			result[len(result)-1].s += token
		case strings.Contains(token, ","):
			var subs []string
			for _, sub := range strings.Split(token, ",") {
				if sub = pyStrip(sub); sub != "" {
					subs = append(subs, sub)
				}
			}
			result = append(result, pv{l: subs, isList: true})
		default:
			result = append(result, pv{s: token})
		}
	}
	return result
}

var digitWords = map[string]int{
	"പൂജ്യം": 0, "ഒന്ന്": 1, "രണ്ട്": 2,
	"മൂന്ന്": 3, "നാല്": 4, "അഞ്ച്": 5,
	"ആറ്": 6, "ഏഴ്": 7, "എട്ട്": 8,
	"ഒമ്പത്": 9, "ഒൻപത്": 9, "പത്ത്": 10,
}

const sp = `[\s\p{Z}\x{85}\x{1c}-\x{1f}]`
const nsp = `[^\s\p{Z}\x{85}\x{1c}-\x{1f}]`

var areaWords = regexp.MustCompile(`(` + nsp + `+)` + sp + `+\x{0d2a}\x{0d4b}\x{0d2f}\x{0d3f}[\x{0d7b}\x{0d28}\x{0d4d}\x{200d}]+[\x{0d31}\x{0d4d}]+\x{0d4d}?` +
	sp + `+((?:` + nsp + `+` + sp + `+)+?)\x{0d06}\x{0d7c}`)

// areaFromWords reads a spelled-out area ("നാല് പോയിൻറ് രണ്ട് അഞ്ച് ആർ" -> 4.25).
func areaFromWords(text string) (float64, bool) {
	for _, m := range areaWords.FindAllStringSubmatchIndex(text, -1) {
		whole := text[m[2]:m[3]]
		decimals := text[m[4]:m[5]]
		before := pySplit(text[:m[0]])
		compound := len(before) > 0 &&
			(strings.HasSuffix(before[len(before)-1], "ത്തി") || strings.HasSuffix(before[len(before)-1], "ത്"))
		w, ok := digitWords[whole]
		if !ok || compound {
			continue
		}
		var digits strings.Builder
		valid := false
		fields := pySplit(decimals)
		if len(fields) > 0 {
			valid = true
		}
		for _, f := range fields {
			d, ok := digitWords[f]
			if !ok {
				valid = false
				break
			}
			digits.WriteString(strconv.Itoa(d))
		}
		if valid {
			v, err := strconv.ParseFloat(fmt.Sprintf("%d.%s", w, digits.String()), 64)
			if err == nil {
				return v, true
			}
		}
	}
	return 0, false
}

func joinValues(values []pv) string {
	var flat []string
	for _, v := range values {
		if v.isList {
			flat = append(flat, v.l...)
		} else {
			flat = append(flat, v.s)
		}
	}
	return strings.Join(flat, ", ")
}

var areMarker = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)` + sp + `*B\x{c0}`)

// customParseAre extracts the numeric area values (marked with the legacy
// font's "ബ" glyph).
func customParseAre(cell *string) []float64 {
	if cell == nil {
		return nil
	}
	var out []float64
	for _, m := range areMarker.FindAllStringSubmatch(*cell, -1) {
		v, _ := strconv.ParseFloat(m[1], 64)
		out = append(out, v)
	}
	return out
}

var noResurveyWords = map[string]bool{"ഇല്ല": true, "nil": true, "na": true, "n/a": true, "none": true, "no": true, "0": true}

func isNoResurvey(value, converted string) bool {
	for _, text := range []string{value, converted} {
		text = pyStrip(strings.ToLower(text))
		if strings.Trim(text, "-–—_ ") == "" || noResurveyWords[pyStrip(strings.TrimRight(text, "."))] {
			return true
		}
	}
	return false
}

var wsRun = regexp.MustCompile(sp + `+`)

func cleanText(text string) string {
	for _, sym := range []string{"\n", "(", ")", ",", "/"} {
		text = strings.ReplaceAll(text, sym, " ")
	}
	return pyStrip(wsRun.ReplaceAllString(text, " "))
}

var descriptionEnd = regexp.MustCompile(`\x{0d2e}\x{0d40}\x{0d31}\x{0d4d}\x{0d31}(?:\x{0d7c}|\x{0d30}\x{0d4d}\x{200d}?)` + sp + `*\x{0d05}\x{0d33}\x{0d35}\x{0d3f}(?:\x{0d7d}|\x{0d32}\x{0d4d}\x{200d}?)`)

// description is the property description, up to "മീറ്റർ അളവിൽ".
func description(text string) string {
	loc := descriptionEnd.FindStringIndex(text)
	if loc != nil {
		text = text[:loc[0]]
	}
	return cleanText(text)
}

func pyFloorDiv(a, b float64) float64 {
	mod := math.Mod(a, b)
	div := (a - mod) / b
	if mod != 0 && (b < 0) != (mod < 0) {
		div -= 1
	}
	if div != 0 {
		fl := math.Floor(div)
		if div-fl > 0.5 {
			fl++
		}
		return fl
	}
	return math.Copysign(0, a/b)
}

func pyMod(a, b float64) float64 {
	mod := math.Mod(a, b)
	if mod != 0 && (b < 0) != (mod < 0) {
		mod += b
	}
	return mod
}

// splitAres: an area in ares (0.21 = 21 sq.m) -> (hectare, are, sq.m).
func splitAres(v float64) (int64, int64, int64) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, 0, 0
	}
	return int64(pyFloorDiv(v, 100)), int64(pyMod(v, 100)), int64(math.RoundToEven((v - math.Trunc(v)) * 100))
}

// surveyValues: "66, 67" / "1 2 4" -> ["66", "67"] / ["1", "2", "4"].
func surveyValues(value string) []string {
	var out []string
	for _, v := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || isPySpace(r) }) {
		if v != "" && v != "0" {
			out = append(out, v)
		}
	}
	return out
}

// DefaultAssignment says which old survey each old sub-division belongs to
// (an index into olds per sub) and whether that is only a guess.
func DefaultAssignment(olds, subs []string) ([]int, bool) {
	out := make([]int, len(subs))
	if len(olds) <= 1 || len(olds) == len(subs) {
		for i := range subs {
			out[i] = min(i, max(len(olds)-1, 0))
		}
		return out, false
	}
	for i := range subs {
		out[i] = min(i, len(olds)-1)
	}
	return out, true
}

// OldSurveyRows makes one schedule row per old survey number and
// sub-division (see the Python docstring); base is the schedule as given.
// The rows after the first get price 0 (priceZero marks them).
func OldSurveyRows(base models.ScheduleItem, olds, subs []string, assignment []int, areas []float64) (rows []models.ScheduleItem, priceZero []bool) {
	if len(olds) == 0 {
		olds = []string{base.OldSurvey}
	}
	groups := make([][]string, len(olds))
	for i := range olds {
		for j, sub := range subs {
			if j < len(assignment) && assignment[j] == i {
				groups[i] = append(groups[i], sub)
			}
		}
		if len(groups[i]) == 0 {
			groups[i] = []string{""}
		}
	}
	for gi, old := range olds {
		for si, sub := range groups[gi] {
			row := base
			row.OldSurvey, row.OldSub = old, sub
			zero := false
			if len(rows) > 0 {
				row.ResurveyBlock, row.ResurveyNumber, row.ResurveySub = "", "", ""
				zero = true
				z := int32(0)
				row.Price = &z
				area := 0.0
				if si == 0 && gi < len(areas) && len(areas) > 1 {
					area = areas[gi]
				}
				h, a, s := splitAres(area)
				row.TotalHectare, row.TotalAre, row.TotalSqm = dec(h), dec(a), dec(s)
				row.TransferHectare, row.TransferAre, row.TransferSqm = dec(h), dec(a), dec(s)
			}
			rows = append(rows, row)
			priceZero = append(priceZero, zero)
		}
	}
	return rows, priceZero
}

func dec(n int64) *db.Decimal {
	d := db.DecimalFromInt(n)
	return &d
}

// SurveyMapping marks a parcel whose old surveys cannot be paired with their
// sub-divisions from the deed alone.
type SurveyMapping struct {
	Count      int
	OldSurveys []string
	Subs       []string
	Assignment []int
	Areas      []float64
}

// Entry is one schedule row extracted from the deed's table.
type Entry struct {
	models.ScheduleItem
	NatureOfRight string
	BuyerTPNumber string
	// PriceSet is true for the rows after a parcel's first, whose price is 0
	// rather than the document price.
	PriceSet      bool
	SurveyMapping *SurveyMapping
}

// TableExtractor parses the deed's fixed-layout property schedule.
type TableExtractor struct {
	conv *textconv.Converter
	prop *ordjson.Map
}

// NewTableExtractor makes an extractor over the bundled reference data.
func NewTableExtractor() *TableExtractor {
	return &TableExtractor{conv: textconv.Default(), prop: deeds.PropertyData()}
}

func (t *TableExtractor) mal(cell *string) string {
	if cell == nil {
		return ""
	}
	return t.conv.ConvertASCIIToUnicode(*cell)
}

// row cells during parsing/expansion: nil (None), string, parsed list, number.
type prow []any

func str(cell any) string {
	switch v := cell.(type) {
	case nil:
		return ""
	case string:
		return v
	}
	fail("cell is not a string: %T", cell)
	return ""
}

func (t *TableExtractor) transliterate(s string) string { return translit.MalayalamToITRANS(s) }

func (t *TableExtractor) parseRawTable(table Table) []prow {
	parsed := make([]prow, len(table))
	for i, row := range table {
		pr := make(prow, len(row))
		for j, c := range row {
			if c == nil {
				pr[j] = nil
			} else {
				pr[j] = *c
			}
		}
		if len(row) >= 5 {
			switch {
			case i >= 8 && i <= 11:
				pr[4] = customParse(row[4])
			case i == 12:
				pr[4] = customParseAre(row[4])
			}
		}
		parsed[i] = pr
	}
	return parsed
}

func (t *TableExtractor) expandTable(table []prow) [][]prow {
	if len(table) < 12 {
		fail("table too short")
	}
	var lists [4][]pv
	for i := 8; i < 12; i++ {
		if len(table[i]) < 5 {
			fail("row %d too short", i)
		}
		l, ok := table[i][4].([]pv)
		if !ok {
			fail("row %d not parsed", i)
		}
		lists[i-8] = l
	}
	var areList []float64
	if len(table) > 12 {
		if len(table[12]) < 5 {
			fail("row 12 too short")
		}
		l, ok := table[12][4].([]float64)
		if !ok {
			fail("row 12 not parsed")
		}
		areList = l
	}
	outer := len(lists[0])
	if outer == 0 {
		for _, l := range lists {
			outer = max(outer, len(l))
		}
	}
	for i, lst := range lists {
		if len(lst) > outer {
			joined := append(append([]pv{}, lst[:outer-1]...), pv{s: joinValues(lst[outer-1:])})
			lists[i] = joined
		}
	}

	var expanded [][]prow
	for k := 0; k < outer; k++ {
		var inners [4][]*string // nil entries are Python None
		runs := 1
		for li, lst := range lists {
			if k < len(lst) {
				inner := lst[k]
				if inner.isList {
					for i := range inner.l {
						inners[li] = append(inners[li], &inner.l[i])
					}
				} else {
					s := inner.s
					inners[li] = []*string{&s}
				}
			} else {
				inners[li] = []*string{nil}
			}
			runs = max(runs, len(inners[li]))
		}
		for r := 0; r < runs; r++ {
			newRow := make([]prow, 0, len(table))
			for i, row := range table {
				cell := append(prow(nil), row...)
				switch {
				case i >= 8 && i <= 11:
					values := inners[i-8]
					if r < len(values) && values[r] != nil {
						cell[4] = *values[r]
					} else {
						cell[4] = 0
					}
				case i == 12:
					if r == 0 && k == outer-1 && len(areList) > outer {
						cell[4] = append([]float64(nil), areList[k:]...)
					} else if r == 0 && k < len(areList) {
						cell[4] = areList[k]
					} else {
						cell[4] = 0.0
					}
				}
				newRow = append(newRow, cell)
			}
			expanded = append(expanded, newRow)
		}
	}
	return expanded
}

func cellOf(row []prow, i, j int) any {
	if i >= len(row) || j >= len(row[i]) {
		fail("row %d has no cell %d", i, j)
	}
	return row[i][j]
}

func sval(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case string:
		return x
	case int:
		return strconv.Itoa(x)
	}
	return fmt.Sprint(v)
}

func rawCell(row []prow, i, j int) *string {
	switch v := cellOf(row, i, j).(type) {
	case nil:
		return nil
	case string:
		return &v
	case int:
		s := strconv.Itoa(v)
		return &s
	}
	fail("unexpected cell type at %d,%d", i, j)
	return nil
}

func keys(m *ordjson.Map) []string {
	if m == nil {
		return nil
	}
	return m.KeyList()
}

func obj(m *ordjson.Map, key string) *ordjson.Map {
	if m == nil {
		return nil
	}
	return m.Obj(key)
}

func best(input string, choices []string) string {
	v, _ := deeds.FindBestMatch(input, choices)
	return v
}

func (t *TableExtractor) rowToEntry(row []prow) (Entry, []float64) {
	var areas []float64
	switch v := cellOf(row, 12, 4).(type) {
	case []float64:
		areas = v
	case float64:
		areas = []float64{v}
	case int:
		areas = []float64{float64(v)}
	default:
		fail("unexpected area cell %T", v)
	}
	if len(areas) > 0 && areas[0] == 0 && len(row) > 16 {
		cell := rawCell(row, 16, 2)
		if v, ok := areaFromWords(t.mal(cell)); ok && v != 0 {
			areas[0] = v
		} else {
			areas[0] = 0
		}
	}
	first := 0.0
	if len(areas) > 0 {
		first = areas[0]
	}
	hectare, are, sqm := splitAres(first)

	prop := t.prop
	district := best(t.mal(rawCell(row, 1, 4)), keys(prop))
	sroMap := obj(obj(prop, district), "sros")
	sro := best(t.mal(rawCell(row, 2, 4)), keys(sroMap))
	talukMap := obj(obj(sroMap, sro), "thaluks")
	taluk := best(t.mal(rawCell(row, 3, 4)), keys(talukMap))
	villageMap := obj(obj(talukMap, taluk), "villages")
	vcell := rawCell(row, 4, 4)
	if vcell == nil {
		fail("village cell is empty")
	}
	village := best(strings.SplitN(t.mal(vcell), "/", 2)[0], keys(villageMap))
	desamMap := obj(obj(villageMap, village), "desams")
	desam := best(t.mal(rawCell(row, 5, 4)), keys(desamMap))
	localBodies := obj(obj(prop, district), "local_bodies")
	panchayats := keys(obj(obj(localBodies, "പഞ്ചായത്ത്"), "lbody_names"))
	localBody := best(t.transliterate(t.mal(rawCell(row, 6, 4))), panchayats)

	resurvey := make([]string, 3)
	for n, idx := range []int{7, 8, 9} {
		raw := cellOf(row, idx, 4)
		var val string
		switch x := raw.(type) {
		case nil:
			val = ""
		case string:
			val = x
		case int:
			val = strconv.Itoa(x)
		default:
			fail("unexpected resurvey cell %T", raw)
		}
		conv := t.conv.ConvertASCIIToUnicode(sval(raw))
		if isNoResurvey(val, conv) {
			resurvey[n] = ""
		} else {
			resurvey[n] = val
		}
	}
	if resurvey[1] == "" {
		resurvey = []string{"", "", ""}
	}

	e := Entry{}
	it := &e.ScheduleItem
	if c := cellOf(row, 0, 4); c != nil {
		it.Number = str(c)
	}
	it.District, it.SRO, it.Taluk, it.Village, it.Desam = district, sro, taluk, village, desam
	it.LocalBodyType = "പഞ്ചായത്ത്"
	it.LocalBodyName = localBody
	it.ResurveyBlock, it.ResurveyNumber, it.ResurveySub = resurvey[0], resurvey[1], resurvey[2]
	it.OldSurvey = oldVal(cellOf(row, 10, 4))
	it.OldSub = oldVal(cellOf(row, 11, 4))
	it.TotalHectare, it.TotalAre, it.TotalSqm = dec(hectare), dec(are), dec(sqm)
	it.TransferHectare, it.TransferAre, it.TransferSqm = dec(hectare), dec(are), dec(sqm)
	if len(row) > 13 {
		e.NatureOfRight = t.mal(rawCell(row, 13, 4))
	}
	if len(row) > 14 {
		if c := rawCell(row, 14, 4); c == nil {
			it.TPNumber = "None" // str(None), as the Python wrote it
		} else {
			it.TPNumber = t.mal(c)
		}
	}
	if len(row) > 15 {
		e.BuyerTPNumber = t.mal(rawCell(row, 15, 4))
	}
	if len(row) > 16 {
		it.Details = description(t.mal(rawCell(row, 16, 2)))
	}
	bounds := []*string{&it.BoundaryEast, &it.BoundaryNorth, &it.BoundaryWest, &it.BoundarySouth}
	for n, dst := range bounds {
		if len(row) > 17+n {
			*dst = cleanText(t.mal(rawCell(row, 17+n, 3)))
		}
	}
	return e, areas
}

func oldVal(v any) string {
	switch x := v.(type) {
	case nil:
		return "0"
	case string:
		return x
	case int:
		return strconv.Itoa(x)
	}
	return fmt.Sprint(v)
}

// ExtractSchedule runs parse -> expand -> structure over the raw tables and
// returns one entry per old survey number / sub-division.
func (t *TableExtractor) ExtractSchedule(tables []Table) []Entry {
	var entries []Entry
	for _, table := range tables {
		entries = append(entries, t.extractTable(table)...)
	}
	return entries
}

func (t *TableExtractor) extractTable(table Table) (out []Entry) {
	defer func() {
		if r := recover(); r != nil {
			if te, ok := r.(tableError); ok {
				slog.Error("Failed to parse a schedule table; skipping it.", "reason", string(te))
				return
			}
			panic(r)
		}
	}()
	parsed := t.parseRawTable(table)
	for _, row := range t.expandTable(parsed) {
		entry, areas := t.rowToEntry(row)
		olds, subs := surveyValues(entry.OldSurvey), surveyValues(entry.OldSub)
		assignment, ambiguous := DefaultAssignment(olds, subs)
		rows, zero := OldSurveyRows(entry.ScheduleItem, olds, subs, assignment, areas)
		for i, r := range rows {
			e := entry
			e.ScheduleItem = r
			e.PriceSet = zero[i]
			if i == 0 {
				e.SurveyMapping = nil
				if ambiguous {
					e.SurveyMapping = &SurveyMapping{Count: len(rows), OldSurveys: olds, Subs: subs, Assignment: assignment, Areas: areas}
				}
			}
			out = append(out, e)
		}
	}
	return out
}

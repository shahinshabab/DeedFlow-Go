package deeds

import (
	_ "embed"
	"log/slog"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/shahinshabab/deedflow-go/internal/fuzzy"
	"github.com/shahinshabab/deedflow-go/internal/ordjson"
)

//go:embed data/property_data.json
var propertyJSON []byte

//go:embed data/book_data.json
var bookJSON []byte

var (
	propOnce, bookOnce sync.Once
	propData, bookData *ordjson.Map
)

// PropertyData is the district -> SRO -> taluk -> village -> desam -> local
// body reference dataset.
func PropertyData() *ordjson.Map {
	propOnce.Do(func() {
		v, err := ordjson.Parse(propertyJSON)
		if err != nil {
			slog.Error("loading property reference data", "err", err)
			propData = &ordjson.Map{M: map[string]any{}}
			return
		}
		propData, _ = v.(*ordjson.Map)
	})
	return propData
}

// BookData is the book -> transaction type -> sub-type dataset.
func BookData() *ordjson.Map {
	bookOnce.Do(func() {
		v, err := ordjson.Parse(bookJSON)
		if err != nil {
			slog.Error("loading book reference data", "err", err)
			bookData = &ordjson.Map{M: map[string]any{}}
			return
		}
		bookData, _ = v.(*ordjson.Map)
	})
	return bookData
}

// DistrictList lists the districts in dataset order.
func DistrictList() []string { return PropertyData().KeyList() }

// SROsForDistrict lists a district's SROs.
func SROsForDistrict(district string) []string {
	return PropertyData().Obj(district).Obj("sros").KeyList()
}

// TaluksForSRO lists the taluks of a district's SRO.
func TaluksForSRO(district, sro string) []string {
	return PropertyData().Obj(district).Obj("sros").Obj(sro).Obj("thaluks").KeyList()
}

// bookEntry finds the book whose book_value is bookValue.
func bookEntry(bookValue string) *ordjson.Map {
	data := BookData()
	for _, k := range data.Keys {
		if e := data.Obj(k); e.Str("book_value") == bookValue {
			return e
		}
	}
	return nil
}

// DeedTypesForBook lists the transaction types of a book number.
func DeedTypesForBook(book string) []string {
	return bookEntry(book).Obj("types").KeyList()
}

// Subtype is a sub-type name and its portal value.
type Subtype struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// DeedSubtypesForType lists the sub-types of a book + transaction type.
func DeedSubtypesForType(book, deedType string) []Subtype {
	out := []Subtype{}
	t := bookEntry(book).Obj("types").Obj(deedType)
	if t == nil {
		return out
	}
	list, _ := t.M["subtypes"].([]any)
	for _, it := range list {
		if m, ok := it.(*ordjson.Map); ok {
			out = append(out, Subtype{Name: m.Str("subtype_name"), Value: m.Str("subtype_value")})
		}
	}
	return out
}

// --- matching ---------------------------------------------------------------

var atomicChillus = map[rune]string{'ൺ': "ണ്", 'ൻ': "ന്", 'ർ': "ര്", 'ൽ': "ല്", 'ൾ': "ള്", 'ൿ': "ക്"}

func isPySpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// CollapseSpaces is " ".join(s.split()): runs of whitespace become one space.
func CollapseSpaces(s string) string {
	var b strings.Builder
	pendingSpace := false
	for _, r := range s {
		if isPySpace(r) {
			pendingSpace = b.Len() > 0
			continue
		}
		if pendingSpace {
			b.WriteByte(' ')
			pendingSpace = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Normalize makes two spellings of the same Malayalam word comparable:
// chillu letters as consonant + virama, no zero-width joiners, single spaces,
// lower case.
func Normalize(text string) string {
	text = norm.NFC.String(text)
	var b strings.Builder
	for _, r := range text {
		if unicode.Is(unicode.Cf, r) {
			continue
		}
		if rep, ok := atomicChillus[r]; ok {
			b.WriteString(rep)
			continue
		}
		b.WriteRune(r)
	}
	return strings.ToLower(CollapseSpaces(b.String()))
}

// FindBestMatch returns the best fuzzy match for input among choices ("" and
// false when there are none).
func FindBestMatch(input string, choices []string) (string, bool) {
	if len(choices) == 0 {
		return "", false
	}
	if input == "" {
		return choices[0], true
	}
	byNorm := map[string]string{}
	var order []string
	for _, c := range choices {
		n := Normalize(c)
		if _, ok := byNorm[n]; !ok {
			byNorm[n] = c
			order = append(order, n)
		}
	}
	wanted := Normalize(input)
	if c, ok := byNorm[wanted]; ok {
		return c, true
	}
	best, _, _ := fuzzy.ExtractOne(wanted, order)
	return byNorm[best], true
}

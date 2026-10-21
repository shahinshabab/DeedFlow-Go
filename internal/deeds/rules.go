package deeds

import (
	"math/big"
	"strings"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/db"
	"github.com/shahinshabab/deedflow-go/internal/models"
)

// DefaultFilingSheets is the number of filing sheets the portal gets when the
// user hasn't given one.
const DefaultFilingSheets = 5

// decimalOf parses a decimal-ish string, 0 when blank or invalid.
func decimalOf(s string) *big.Rat {
	s = strings.TrimSpace(s)
	if s == "" {
		return new(big.Rat)
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok || strings.Contains(s, "/") {
		return new(big.Rat)
	}
	return r
}

func decPtr(d *db.Decimal) string {
	if d == nil || !d.Valid() {
		return ""
	}
	return d.String()
}

// AreaInAres turns hectare / are / sq.m into ares (1 ha = 100 are, 1 are = 100 sq.m).
func AreaInAres(hectare, are, sqm string) *big.Rat {
	h := new(big.Rat).Mul(decimalOf(hectare), big.NewRat(100, 1))
	h.Add(h, decimalOf(are))
	h.Add(h, new(big.Rat).Quo(decimalOf(sqm), big.NewRat(100, 1)))
	return h
}

func hasArea(vals ...string) bool {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" && s != "None" {
			return true
		}
	}
	return false
}

// Area is a hectare / are / sq.m triple as strings ("" when unset).
type Area struct{ Hectare, Are, Sqm string }

// MinimumPrice is the lowest price the portal accepts for one property: its
// fair value (per are) times the area being transferred - or the whole area
// when no transferred area is given. ok is false when there's nothing to
// compute from.
func MinimumPrice(fairValue string, transfer, total Area) (int64, bool) {
	fv := decimalOf(fairValue)
	if strings.TrimSpace(fairValue) == "" || fv.Sign() == 0 {
		return 0, false
	}
	area := transfer
	if !hasArea(transfer.Hectare, transfer.Are, transfer.Sqm) {
		area = total
	}
	if !hasArea(area.Hectare, area.Are, area.Sqm) {
		return 0, false
	}
	product := new(big.Rat).Mul(fv, AreaInAres(area.Hectare, area.Are, area.Sqm))
	return ceilRat(product), true
}

func ceilRat(r *big.Rat) int64 {
	q := new(big.Int).Div(r.Num(), r.Denom())
	if new(big.Rat).SetInt(q).Cmp(r) < 0 {
		q.Add(q, big.NewInt(1))
	}
	return q.Int64()
}

// ItemMinimumPrice is MinimumPrice for a schedule item. ok is false for an
// undivided share: the portal works out what a share is worth itself, so the
// deed's own price goes in as written.
func ItemMinimumPrice(it *models.ScheduleItem) (int64, bool) {
	if it.UndividedShare == "Yes" {
		return 0, false
	}
	fair := ""
	if it.FairValue != nil {
		fair = intStr(int64(*it.FairValue))
	}
	return MinimumPrice(fair,
		Area{decPtr(it.TransferHectare), decPtr(it.TransferAre), decPtr(it.TransferSqm)},
		Area{decPtr(it.TotalHectare), decPtr(it.TotalAre), decPtr(it.TotalSqm)})
}

// MinimumDocumentValue is the sum of every property's minimum price - the
// least the document value can be, since the portal also wants the prices to
// add up to it.
func MinimumDocumentValue(items []models.ScheduleItem) (int64, bool) {
	var sum int64
	any := false
	for i := range items {
		if m, ok := ItemMinimumPrice(&items[i]); ok && m != 0 {
			sum += m
			any = true
		}
	}
	return sum, any
}

// ApplyPresenterDefaults fills in what the portal needs on the Presentation
// page right before it's sent: a past (or blank) presenting date becomes
// today, a blank stamp date becomes today, and blank filing sheets become the
// default. It returns the changed column names (not saved here).
func ApplyPresenterDefaults(p *models.Presenter, today time.Time) []string {
	var changed []string
	if p.PresenterDate == nil || p.PresenterDate.Before(today) {
		t := today
		p.PresenterDate = &t
		changed = append(changed, "presenter_date")
	}
	if p.StampDate == nil {
		t := today
		p.StampDate = &t
		changed = append(changed, "stamp_date")
	}
	if p.DocumentSheetCount == nil || *p.DocumentSheetCount == 0 {
		n := int16(DefaultFilingSheets)
		p.DocumentSheetCount = &n
		changed = append(changed, "document_sheet_count")
	}
	return changed
}

// Today is the current date in the office's time zone, as a date at midnight UTC
// (the way DATE columns are read).
func Today(loc *time.Location) time.Time {
	n := time.Now().In(loc)
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
}

func intStr(n int64) string { return big.NewInt(n).String() }

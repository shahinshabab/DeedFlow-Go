package ledger

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/shahinshabab/deedflow-go/internal/db"
	"github.com/shahinshabab/deedflow-go/internal/models"
)

type column struct {
	Name  string
	Width float64
}

var receiptColumns = []column{
	{"Token", 13}, {"Applicant name", 26}, {"Place (SRO)", 16}, {"Presentation date", 14},
	{"Presenter name (Malayalam)", 26}, {"Presenter place", 18}, {"Presenter address", 22},
	{"Document amount", 15}, {"Transaction no.", 18}, {"GRN", 22}, {"Payment mode", 16}, {"Amount (Rs)", 13},
	{"Bank reference no.", 18}, {"Bank CIN", 24}, {"Receipt date & time", 19}, {"Status", 24},
}

var expenseColumns = []column{{"Date", 12}, {"Category", 18}, {"Details", 40}, {"Paid by", 14}, {"Amount (Rs)", 14},
	{"Bills", 8}, {"Added by", 16}}

const brandGreen = "1C6E4A"

func joinAttr(receipts []models.PaymentReceipt, get func(models.PaymentReceipt) string) string {
	var parts []string
	for _, r := range receipts {
		if v := get(r); v != "" {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, ", ")
}

// number is a decimal as a spreadsheet number (nil: an empty cell).
func number(d *db.Decimal) any {
	if d == nil || !d.Valid() {
		return nil
	}
	return d.Float64()
}

// blankNil turns empty strings into empty cells (openpyxl writes none).
func blankNil(row []any) []any {
	for i, v := range row {
		if s, ok := v.(string); ok && s == "" {
			row[i] = nil
		}
	}
	return row
}

func receiptRow(item models.RegisteredToken, receipts []models.PaymentReceipt, loc *time.Location) []any {
	var latest *time.Time
	for _, r := range receipts {
		if r.PaidAt != nil && (latest == nil || r.PaidAt.After(*latest)) {
			t := *r.PaidAt
			latest = &t
		}
	}
	var paid any
	if latest != nil {
		l := latest.In(loc)
		paid = time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), l.Minute(), l.Second(), 0, time.UTC)
	}
	var statuses []string
	seen := map[string]bool{}
	for _, r := range receipts {
		if r.Status != "" && !seen[r.Status] {
			seen[r.Status] = true
			statuses = append(statuses, r.Status)
		}
	}
	status := strings.Join(statuses, "; ")
	if status == "" && len(receipts) == 0 {
		status = "No receipt"
	}
	var presented any
	if item.PresentationDate != nil {
		d := item.PresentationDate
		presented = time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
	}
	return []any{
		item.Token, item.ApplicantName, item.SRO, presented,
		item.PresenterNameML, item.PresenterPlaceML, item.PresenterAddrML,
		number(item.DocumentAmount),
		joinAttr(receipts, func(r models.PaymentReceipt) string { return r.TransactionNo }),
		joinAttr(receipts, func(r models.PaymentReceipt) string { return r.GRN }),
		joinAttr(receipts, func(r models.PaymentReceipt) string { return r.PaymentMode }),
		number(models.TotalOf(receipts)),
		joinAttr(receipts, func(r models.PaymentReceipt) string { return r.BankReference }),
		joinAttr(receipts, func(r models.PaymentReceipt) string { return r.BankCIN }),
		paid, status,
	}
}

func styles(f *excelize.File) (header, date, money, datetime int, err error) {
	if header, err = f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "FFFFFF"},
		Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{brandGreen}},
		Alignment: &excelize.Alignment{Vertical: "center", WrapText: true},
	}); err != nil {
		return
	}
	dateFmt, moneyFmt, dtFmt := "DD-MM-YYYY", "#,##0.00", "DD-MM-YYYY HH:MM:SS"
	if date, err = f.NewStyle(&excelize.Style{CustomNumFmt: &dateFmt}); err != nil {
		return
	}
	if money, err = f.NewStyle(&excelize.Style{CustomNumFmt: &moneyFmt}); err != nil {
		return
	}
	datetime, err = f.NewStyle(&excelize.Style{CustomNumFmt: &dtFmt})
	return
}

func colName(i int) string {
	n, _ := excelize.ColumnNumberToName(i)
	return n
}

func writeReceiptSheet(f *excelize.File, sheet string, tokens []models.RegisteredToken, kind string, loc *time.Location) error {
	header, dateStyle, moneyStyle, dtStyle, err := styles(f)
	if err != nil {
		return err
	}
	names := make([]any, len(receiptColumns))
	for i, c := range receiptColumns {
		names[i] = c.Name
		if err := f.SetColWidth(sheet, colName(i+1), colName(i+1), c.Width); err != nil {
			return err
		}
	}
	if err := f.SetSheetRow(sheet, "A1", &names); err != nil {
		return err
	}
	if err := f.SetCellStyle(sheet, "A1", colName(len(receiptColumns))+"1", header); err != nil {
		return err
	}
	for i, item := range tokens {
		row := blankNil(receiptRow(item, item.ReceiptsOf(kind), loc))
		cell, _ := excelize.CoordinatesToCellName(1, i+2)
		if err := f.SetSheetRow(sheet, cell, &row); err != nil {
			return err
		}
	}
	if n := len(tokens); n > 0 {
		last := n + 1
		for col, style := range map[int]int{4: dateStyle, 8: moneyStyle, 12: moneyStyle, 15: dtStyle} {
			if err := f.SetCellStyle(sheet, fmt.Sprintf("%s2", colName(col)), fmt.Sprintf("%s%d", colName(col), last), style); err != nil {
				return err
			}
		}
	}
	if err := f.SetPanes(sheet, &excelize.Panes{Freeze: true, Split: true, XSplit: 1, YSplit: 1,
		TopLeftCell: "B2", ActivePane: "bottomRight"}); err != nil {
		return err
	}
	last := len(tokens) + 1
	return f.AutoFilter(sheet, fmt.Sprintf("A1:%s%d", colName(len(receiptColumns)), last), nil)
}

// BuildReceiptsWorkbook is the Accounts Excel download: one sheet for stamp
// duty payments, one for registration fee payments, one row per token in each.
func BuildReceiptsWorkbook(tokens []models.RegisteredToken, loc *time.Location) ([]byte, error) {
	f := excelize.NewFile()
	defer f.Close()
	if err := f.SetSheetName("Sheet1", "Stamp payment"); err != nil {
		return nil, err
	}
	if _, err := f.NewSheet("Fee payment"); err != nil {
		return nil, err
	}
	if err := writeReceiptSheet(f, "Stamp payment", tokens, models.KindStampDuty, loc); err != nil {
		return nil, err
	}
	if err := writeReceiptSheet(f, "Fee payment", tokens, models.KindRegistrationFee, loc); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// BuildExpensesWorkbook is the Payments Excel download.
func BuildExpensesWorkbook(expenses []models.Expense) ([]byte, error) {
	f := excelize.NewFile()
	defer f.Close()
	const sheet = "Payments"
	if err := f.SetSheetName("Sheet1", sheet); err != nil {
		return nil, err
	}
	header, dateStyle, moneyStyle, _, err := styles(f)
	if err != nil {
		return nil, err
	}
	names := make([]any, len(expenseColumns))
	for i, c := range expenseColumns {
		names[i] = c.Name
		if err := f.SetColWidth(sheet, colName(i+1), colName(i+1), c.Width); err != nil {
			return nil, err
		}
	}
	if err := f.SetSheetRow(sheet, "A1", &names); err != nil {
		return nil, err
	}
	if err := f.SetCellStyle(sheet, "A1", colName(len(expenseColumns))+"1", header); err != nil {
		return nil, err
	}
	total := db.DecimalFromInt(0)
	for i, e := range expenses {
		by := ""
		if e.CreatedBy != nil {
			by = e.CreatedBy.Username
		}
		date := time.Date(e.Date.Year(), e.Date.Month(), e.Date.Day(), 0, 0, 0, 0, time.UTC)
		row := blankNil([]any{date, e.CategoryLabel(), e.Description, modeLabel(e), e.Amount.Float64(), len(e.Bills), by})
		cell, _ := excelize.CoordinatesToCellName(1, i+2)
		if err := f.SetSheetRow(sheet, cell, &row); err != nil {
			return nil, err
		}
		total = total.Add(e.Amount)
	}
	last := len(expenses) + 1
	bold, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	if err != nil {
		return nil, err
	}
	totalRow := blankNil([]any{"", "", "", "Total", total.Float64()})
	cell, _ := excelize.CoordinatesToCellName(1, last+1)
	if err := f.SetSheetRow(sheet, cell, &totalRow); err != nil {
		return nil, err
	}
	if err := f.SetCellStyle(sheet, fmt.Sprintf("D%d", last+1), fmt.Sprintf("E%d", last+1), bold); err != nil {
		return nil, err
	}
	if last > 1 {
		if err := f.SetCellStyle(sheet, "A2", fmt.Sprintf("A%d", last), dateStyle); err != nil {
			return nil, err
		}
	}
	if err := f.SetCellStyle(sheet, "E2", fmt.Sprintf("E%d", last+1), moneyStyle); err != nil {
		return nil, err
	}
	if err := f.SetPanes(sheet, &excelize.Panes{Freeze: true, Split: false, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"}); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func modeLabel(e models.Expense) string {
	if e.PaymentMode == "" {
		return ""
	}
	return e.ModeLabel()
}

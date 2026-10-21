package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/shahinshabab/deedflow-go/internal/db"
	"github.com/shahinshabab/deedflow-go/internal/models"
)

var (
	tokenCols   = db.Columns[models.RegisteredToken]("")
	receiptCols = db.Columns[models.PaymentReceipt]("")
	runCols     = db.Columns[models.ReceiptSyncRun]("")
	expenseCols = db.Columns[models.Expense]("")
	billCols    = db.Columns[models.ExpenseBill]("")
)

// likePattern is a "contains" pattern for ILIKE, with its wildcards escaped.
func likePattern(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(s) + "%"
}

// --- registered tokens and their receipts --------------------------------------

// TokenFilter narrows the Accounts list: the search box, the place filter and
// the presentation date range.
type TokenFilter struct {
	Q, Place string
	From, To *time.Time
}

type where struct {
	parts []string
	args  []any
}

func (w *where) add(sql string, args ...any) {
	for _, a := range args {
		w.args = append(w.args, a)
		sql = strings.Replace(sql, "?", fmt.Sprintf("$%d", len(w.args)), 1)
	}
	w.parts = append(w.parts, sql)
}

func (w *where) sql() string { return strings.Join(w.parts, " AND ") }

func tokenWhere(orgID int64, f TokenFilter) *where {
	w := &where{}
	w.add("t.organization_id = ?", orgID)
	if f.Q != "" {
		w.add(`(t.token ILIKE ? ESCAPE '\' OR t.applicant_name ILIKE ? ESCAPE '\' OR t.presenter_name_ml ILIKE ? ESCAPE '\'
			OR t.sro ILIKE ? ESCAPE '\' OR EXISTS (SELECT 1 FROM ledger_paymentreceipt r WHERE r.token_id = t.id
			AND (r.transaction_no ILIKE ? ESCAPE '\' OR r.grn ILIKE ? ESCAPE '\')))`,
			likePattern(f.Q), likePattern(f.Q), likePattern(f.Q), likePattern(f.Q), likePattern(f.Q), likePattern(f.Q))
	}
	if f.Place != "" {
		w.add("UPPER(t.sro) = UPPER(?)", f.Place)
	}
	if f.From != nil {
		w.add("t.presentation_date >= ?", *f.From)
	}
	if f.To != nil {
		w.add("t.presentation_date <= ?", *f.To)
	}
	return w
}

func (s *Store) loadReceipts(ctx context.Context, tokens []models.RegisteredToken) error {
	if len(tokens) == 0 {
		return nil
	}
	ids := make([]int64, len(tokens))
	index := map[int64]int{}
	for i, t := range tokens {
		ids[i] = t.ID
		index[t.ID] = i
	}
	receipts, err := db.All[models.PaymentReceipt](ctx, s.q(), `SELECT `+receiptCols+` FROM ledger_paymentreceipt
		WHERE token_id = ANY($1) ORDER BY kind, paid_at, transaction_no`, ids)
	if err != nil {
		return err
	}
	for _, r := range receipts {
		i := index[r.TokenID]
		tokens[i].Receipts = append(tokens[i].Receipts, r)
	}
	return nil
}

// CountRegisteredTokens counts the tokens a filter matches.
func (s *Store) CountRegisteredTokens(ctx context.Context, orgID int64, f TokenFilter) (int, error) {
	w := tokenWhere(orgID, f)
	var n int
	err := s.q().QueryRow(ctx, `SELECT count(*) FROM ledger_registeredtoken t WHERE `+w.sql(), w.args...).Scan(&n)
	return n, err
}

// ListRegisteredTokens returns the organization's tokens, newest first, with
// their receipts. limit <= 0 means all of them.
func (s *Store) ListRegisteredTokens(ctx context.Context, orgID int64, f TokenFilter, limit, offset int) ([]models.RegisteredToken, error) {
	w := tokenWhere(orgID, f)
	sql := `SELECT ` + db.Columns[models.RegisteredToken]("t") + ` FROM ledger_registeredtoken t WHERE ` + w.sql() +
		` ORDER BY t.presentation_date DESC, t.token DESC`
	if limit > 0 {
		sql += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)
	}
	tokens, err := db.All[models.RegisteredToken](ctx, s.q(), sql, w.args...)
	if err != nil {
		return nil, err
	}
	return tokens, s.loadReceipts(ctx, tokens)
}

// TokenPlaces lists the distinct places (SROs) of the organization's tokens.
func (s *Store) TokenPlaces(ctx context.Context, orgID int64) ([]string, error) {
	rows, err := s.q().Query(ctx, `SELECT DISTINCT sro FROM ledger_registeredtoken WHERE organization_id = $1 AND sro <> '' ORDER BY sro`, orgID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// RegisteredTokenByNumber finds one of the organization's tokens, with its receipts.
func (s *Store) RegisteredTokenByNumber(ctx context.Context, orgID int64, token string) (*models.RegisteredToken, error) {
	t, err := db.One[models.RegisteredToken](ctx, s.q(), `SELECT `+tokenCols+` FROM ledger_registeredtoken
		WHERE organization_id = $1 AND token = $2`, orgID, token)
	if errors.Is(err, db.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	list := []models.RegisteredToken{*t}
	if err := s.loadReceipts(ctx, list); err != nil {
		return nil, err
	}
	return &list[0], nil
}

// LastReceiptRun is the organization's most recent collection run, or nil.
func (s *Store) LastReceiptRun(ctx context.Context, orgID int64) (*models.ReceiptSyncRun, error) {
	r, err := db.One[models.ReceiptSyncRun](ctx, s.q(), `SELECT `+runCols+` FROM ledger_receiptsyncrun
		WHERE organization_id = $1 ORDER BY started_at DESC, id DESC LIMIT 1`, orgID)
	if errors.Is(err, db.ErrNotFound) {
		return nil, nil
	}
	return r, err
}

// CreateReceiptRun starts a run record.
func (s *Store) CreateReceiptRun(ctx context.Context, run *models.ReceiptSyncRun) error {
	if run.Status == "" {
		run.Status = models.RunRunning
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = now()
	}
	_, err := db.Insert(ctx, s.q(), "ledger_receiptsyncrun", run)
	return err
}

// SaveReceiptRun writes a run record back.
func (s *Store) SaveReceiptRun(ctx context.Context, run *models.ReceiptSyncRun, cols ...string) error {
	return db.Update(ctx, s.q(), "ledger_receiptsyncrun", run, cols...)
}

// NotCollectedTokens are the tokens whose receipts aren't all collected, newest first.
func (s *Store) NotCollectedTokens(ctx context.Context, orgID int64, limit int) ([]models.RegisteredToken, error) {
	sql := `SELECT ` + tokenCols + ` FROM ledger_registeredtoken WHERE organization_id = $1 AND status <> 'collected'
		ORDER BY presentation_date DESC, token DESC`
	if limit > 0 {
		sql += fmt.Sprintf(" LIMIT %d", limit)
	}
	return db.All[models.RegisteredToken](ctx, s.q(), sql, orgID)
}

// CountNotCollected counts the tokens whose receipts aren't all collected.
func (s *Store) CountNotCollected(ctx context.Context, orgID int64) (int, error) {
	var n int
	err := s.q().QueryRow(ctx, `SELECT count(*) FROM ledger_registeredtoken WHERE organization_id = $1 AND status <> 'collected'`, orgID).Scan(&n)
	return n, err
}

// CountCollectedSince counts the tokens collected at or after since.
func (s *Store) CountCollectedSince(ctx context.Context, orgID int64, since time.Time) (int, error) {
	var n int
	err := s.q().QueryRow(ctx, `SELECT count(*) FROM ledger_registeredtoken WHERE organization_id = $1 AND collected_at >= $2`, orgID, since).Scan(&n)
	return n, err
}

// HasDoneRunSince reports whether a run since the given time finished with everything collected.
func (s *Store) HasDoneRunSince(ctx context.Context, orgID int64, since time.Time) (bool, error) {
	var ok bool
	err := s.q().QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ledger_receiptsyncrun WHERE organization_id = $1
		AND started_at >= $2 AND status = 'done')`, orgID, since).Scan(&ok)
	return ok, err
}

// RecordRegistered notes the tokens My Token showed as Registered: a new one
// is added with the name and date My Token gave; a known one only gets what
// it was missing.
func (s *Store) RecordRegistered(ctx context.Context, orgID int64, token, name string, date *time.Time) error {
	_, err := s.q().Exec(ctx, `INSERT INTO ledger_registeredtoken
		(organization_id, token, presenter_name_ml, presentation_date, presenter_place_ml, presenter_address_ml,
		 presenter_city_ml, applicant_name, sro, status, attempts, last_error, first_seen_at)
		VALUES ($1, $2, $3, $4, '', '', '', '', '', 'pending', 0, '', now())
		ON CONFLICT (organization_id, token) DO UPDATE SET
		  presenter_name_ml = CASE WHEN ledger_registeredtoken.presenter_name_ml = '' THEN EXCLUDED.presenter_name_ml
		                           ELSE ledger_registeredtoken.presenter_name_ml END,
		  presentation_date = COALESCE(ledger_registeredtoken.presentation_date, EXCLUDED.presentation_date)`,
		orgID, token, name, date)
	return err
}

// SaveRegisteredToken writes the named columns of a token (all if none).
func (s *Store) SaveRegisteredToken(ctx context.Context, t *models.RegisteredToken, cols ...string) error {
	return db.Update(ctx, s.q(), "ledger_registeredtoken", t, cols...)
}

// UpsertReceipt stores a receipt, replacing the one with the same
// (token, kind, transaction number).
func (s *Store) UpsertReceipt(ctx context.Context, r *models.PaymentReceipt) error {
	r.CollectedAt = now()
	_, err := s.q().Exec(ctx, `INSERT INTO ledger_paymentreceipt
		(token_id, kind, transaction_no, applicant_name, sro, grn, amount, payment_mode, bank_reference, bank_cin,
		 paid_at, status, fields, collected_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (token_id, kind, transaction_no) DO UPDATE SET
		  applicant_name = EXCLUDED.applicant_name, sro = EXCLUDED.sro, grn = EXCLUDED.grn, amount = EXCLUDED.amount,
		  payment_mode = EXCLUDED.payment_mode, bank_reference = EXCLUDED.bank_reference, bank_cin = EXCLUDED.bank_cin,
		  paid_at = EXCLUDED.paid_at, status = EXCLUDED.status, fields = EXCLUDED.fields, collected_at = EXCLUDED.collected_at`,
		r.TokenID, r.Kind, r.TransactionNo, r.ApplicantName, r.SRO, r.GRN, r.Amount, r.PaymentMode, r.BankReference,
		r.BankCIN, r.PaidAt, r.Status, r.Fields, r.CollectedAt)
	return err
}

// --- expenses (the office's own payments) ---------------------------------------

// ExpenseFilter narrows the Payments list.
type ExpenseFilter struct {
	Q, Category string
	From, To    *time.Time
}

func expenseWhere(orgID int64, f ExpenseFilter) *where {
	w := &where{}
	w.add("e.organization_id = ?", orgID)
	if f.Q != "" {
		w.add(`(e.description ILIKE ? ESCAPE '\' OR EXISTS (SELECT 1 FROM ledger_expensebill b WHERE b.expense_id = e.id
			AND b.original_name ILIKE ? ESCAPE '\'))`, likePattern(f.Q), likePattern(f.Q))
	}
	if f.Category != "" {
		w.add("e.category = ?", f.Category)
	}
	if f.From != nil {
		w.add("e.date >= ?", *f.From)
	}
	if f.To != nil {
		w.add("e.date <= ?", *f.To)
	}
	return w
}

// CountExpenses counts the payments a filter matches.
func (s *Store) CountExpenses(ctx context.Context, orgID int64, f ExpenseFilter) (int, error) {
	w := expenseWhere(orgID, f)
	var n int
	err := s.q().QueryRow(ctx, `SELECT count(*) FROM ledger_expense e WHERE `+w.sql(), w.args...).Scan(&n)
	return n, err
}

// ListExpenses returns payments newest first, with their bills and who added
// them. limit <= 0 means all of them.
func (s *Store) ListExpenses(ctx context.Context, orgID int64, f ExpenseFilter, limit, offset int) ([]models.Expense, error) {
	w := expenseWhere(orgID, f)
	sql := `SELECT ` + db.Columns[models.Expense]("e") + ` FROM ledger_expense e WHERE ` + w.sql() + ` ORDER BY e.date DESC, e.id DESC`
	if limit > 0 {
		sql += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)
	}
	list, err := db.All[models.Expense](ctx, s.q(), sql, w.args...)
	if err != nil {
		return nil, err
	}
	return list, s.loadExpenseDetails(ctx, list)
}

func (s *Store) loadExpenseDetails(ctx context.Context, list []models.Expense) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]int64, len(list))
	index := map[int64]int{}
	for i, e := range list {
		ids[i] = e.ID
		index[e.ID] = i
	}
	bills, err := db.All[models.ExpenseBill](ctx, s.q(), `SELECT `+billCols+` FROM ledger_expensebill
		WHERE expense_id = ANY($1) ORDER BY uploaded_at, id`, ids)
	if err != nil {
		return err
	}
	for _, b := range bills {
		i := index[b.ExpenseID]
		list[i].Bills = append(list[i].Bills, b)
	}
	users := map[int64]*models.User{}
	for i := range list {
		id := list[i].CreatedByID
		if id == nil {
			continue
		}
		u, ok := users[*id]
		if !ok {
			var err error
			if u, err = s.UserByID(ctx, *id); err != nil && !errors.Is(err, db.ErrNotFound) {
				return err
			}
			users[*id] = u
		}
		list[i].CreatedBy = u
	}
	return nil
}

// CategoryTotal is one category's sum.
type CategoryTotal struct {
	Category string
	Total    db.Decimal
}

// ExpenseTotalsByCategory sums the payments a filter matches, biggest first.
func (s *Store) ExpenseTotalsByCategory(ctx context.Context, orgID int64, f ExpenseFilter) ([]CategoryTotal, error) {
	w := expenseWhere(orgID, f)
	rows, err := s.q().Query(ctx, `SELECT e.category, SUM(e.amount)::text FROM ledger_expense e WHERE `+w.sql()+
		` GROUP BY e.category ORDER BY SUM(e.amount) DESC`, w.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CategoryTotal
	for rows.Next() {
		var c CategoryTotal
		if err := rows.Scan(&c.Category, &c.Total); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ExpenseByID finds one of the organization's payments, with bills.
func (s *Store) ExpenseByID(ctx context.Context, orgID, id int64) (*models.Expense, error) {
	e, err := db.One[models.Expense](ctx, s.q(), `SELECT `+expenseCols+` FROM ledger_expense WHERE id = $1 AND organization_id = $2`, id, orgID)
	if errors.Is(err, db.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	list := []models.Expense{*e}
	if err := s.loadExpenseDetails(ctx, list); err != nil {
		return nil, err
	}
	return &list[0], nil
}

// SaveExpense inserts a payment or writes an existing one back.
func (s *Store) SaveExpense(ctx context.Context, e *models.Expense) error {
	e.UpdatedAt = now()
	if e.ID == 0 {
		e.CreatedAt = e.UpdatedAt
		_, err := db.Insert(ctx, s.q(), "ledger_expense", e)
		return err
	}
	return db.Update(ctx, s.q(), "ledger_expense", e, "date", "category", "amount", "payment_mode", "description", "updated_at")
}

// DeleteExpense removes a payment (its bills go with it).
func (s *Store) DeleteExpense(ctx context.Context, id int64) error {
	return s.InTx(ctx, func(tx *Store) error {
		if _, err := tx.q().Exec(ctx, `DELETE FROM ledger_expensebill WHERE expense_id = $1`, id); err != nil {
			return err
		}
		_, err := tx.q().Exec(ctx, `DELETE FROM ledger_expense WHERE id = $1`, id)
		return err
	})
}

// AddBill records a stored bill file.
func (s *Store) AddBill(ctx context.Context, b *models.ExpenseBill) error {
	b.UploadedAt = now()
	_, err := db.Insert(ctx, s.q(), "ledger_expensebill", b)
	return err
}

// DeleteBill removes one bill row.
func (s *Store) DeleteBill(ctx context.Context, id int64) error {
	_, err := s.q().Exec(ctx, `DELETE FROM ledger_expensebill WHERE id = $1`, id)
	return err
}

// BillForOrganization finds a bill if it belongs to one of the organization's payments.
func (s *Store) BillForOrganization(ctx context.Context, orgID, id int64) (*models.ExpenseBill, error) {
	b, err := db.One[models.ExpenseBill](ctx, s.q(), `SELECT `+db.Columns[models.ExpenseBill]("b")+` FROM ledger_expensebill b
		JOIN ledger_expense e ON e.id = b.expense_id WHERE b.id = $1 AND e.organization_id = $2`, id, orgID)
	if errors.Is(err, db.ErrNotFound) {
		return nil, nil
	}
	return b, err
}

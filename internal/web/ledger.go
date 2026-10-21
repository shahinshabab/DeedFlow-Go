package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/db"
	"github.com/shahinshabab/deedflow-go/internal/forms"
	"github.com/shahinshabab/deedflow-go/internal/jobs"
	"github.com/shahinshabab/deedflow-go/internal/ledger"
	"github.com/shahinshabab/deedflow-go/internal/models"
	"github.com/shahinshabab/deedflow-go/internal/store"
)

// The Accounts section: the registered tokens' payment receipts collected from
// the portal, and the office's own payments (expenses) with their bills.

func (a *App) registerLedger() {
	a.RouteOrdered("ledger:list", "accounts/", a.LoginRequired(a.accountsList))
	a.RouteOrdered("ledger:export", "accounts/export/", a.LoginRequired(a.accountsExport))
	a.RouteOrdered("ledger:collect", "accounts/collect/", a.OrgAdminRequired(a.collectNow))
	a.RouteOrdered("ledger:payments", "accounts/payments/", a.LoginRequired(a.paymentsList))
	a.RouteOrdered("ledger:payment_add", "accounts/payments/add/", a.LoginRequired(a.paymentAdd))
	a.RouteOrdered("ledger:payments_export", "accounts/payments/export/", a.LoginRequired(a.paymentsExport))
	a.RouteOrdered("ledger:payment_edit", "accounts/payments/<int:pk>/", a.LoginRequired(a.paymentEdit))
	a.RouteOrdered("ledger:payment_delete", "accounts/payments/<int:pk>/delete/", a.LoginRequired(a.paymentDelete))
	a.RouteOrdered("ledger:payment_bill", "accounts/payments/bills/<int:pk>/", a.LoginRequired(a.paymentBill))
	a.RouteOrdered("ledger:receipts", "accounts/<str:token>/receipts/", a.LoginRequired(a.tokenReceipts))
	a.Router.MountOrdered("accounts/")
}

// orgOr404 is the user's organization; a user without one gets a 404.
func (c *Ctx) orgOr404() *models.Organization {
	org := c.Org()
	if org == nil {
		c.NotFound()
	}
	return org
}

var runStatusLabels = map[string]string{models.RunRunning: "Running", models.RunDone: "Done",
	models.RunPartial: "Some pending", models.RunFailed: "Failed"}

var tokenStatusLabels = map[string]string{models.TokenPending: "Pending", models.TokenCollected: "Collected",
	models.TokenFailed: "Failed"}

func receiptObj(r models.PaymentReceipt) map[string]any {
	var pairs [][]string
	_ = json.Unmarshal(r.Fields, &pairs)
	fields := make([]map[string]string, 0, len(pairs))
	for _, p := range pairs {
		if len(p) >= 2 {
			fields = append(fields, map[string]string{"label": p[0], "value": p[1]})
		}
	}
	return ObjectMap(r, map[string]any{"fields": fields})
}

func receiptObjs(list []models.PaymentReceipt) []any {
	out := make([]any, len(list))
	for i, r := range list {
		out[i] = receiptObj(r)
	}
	return out
}

// tokenObj is a registered token as the templates read it (stamp_receipts,
// fee_total, receipt_groups ... are its Django properties and filters).
func tokenObj(t models.RegisteredToken) map[string]any {
	stamp, fee := t.StampReceipts(), t.FeeReceipts()
	extra := map[string]any{
		"name":               t.Name(),
		"stamp_receipts":     receiptObjs(stamp),
		"fee_receipts":       receiptObjs(fee),
		"get_status_display": tokenStatusLabels[t.Status],
		"receipt_groups": []map[string]any{
			{"label": "Stamp duty", "receipts": receiptObjs(stamp)},
			{"label": "Registration fee", "receipts": receiptObjs(fee)},
		},
	}
	if total := models.TotalOf(stamp); total != nil {
		extra["stamp_total"] = total
	}
	if total := models.TotalOf(fee); total != nil {
		extra["fee_total"] = total
	}
	return ObjectMap(t, extra)
}

func tokenFilterFrom(c *Ctx) (store.TokenFilter, map[string]any) {
	q := strings.TrimSpace(c.Query("q"))
	place := strings.TrimSpace(c.Query("place"))
	from, to := safeDate(c.Query("from")), safeDate(c.Query("to"))
	return store.TokenFilter{Q: q, Place: place, From: from, To: to}, map[string]any{
		"q": q, "place": place, "date_from": c.Query("from"), "date_to": c.Query("to"),
		"filtered": q != "" || place != "" || from != nil || to != nil,
	}
}

func (a *App) accountsList(c *Ctx) {
	org := c.orgOr404()
	if org == nil {
		return
	}
	filter, ctx := tokenFilterFrom(c)
	total, err := a.Store.CountRegisteredTokens(c.Context(), org.ID, filter)
	if err != nil {
		c.ServerError(err)
		return
	}
	pager := NewPager(total, 25, c.Query("page"))
	tokens, err := a.Store.ListRegisteredTokens(c.Context(), org.ID, filter, pager.PerPage, pager.Offset())
	if err != nil {
		c.ServerError(err)
		return
	}
	items := make([]any, len(tokens))
	for i, t := range tokens {
		items[i] = tokenObj(t)
	}
	places, err := a.Store.TokenPlaces(c.Context(), org.ID)
	if err != nil {
		c.ServerError(err)
		return
	}
	run, err := a.Store.LastReceiptRun(c.Context(), org.ID)
	if err != nil {
		c.ServerError(err)
		return
	}
	cred, err := a.Store.PortalCredentialFor(c.Context(), org.ID)
	if err != nil {
		c.ServerError(err)
		return
	}
	data := Data{
		"page": pager.Map(items), "places": places, "query_string": queryWithout(c.R.URL.RawQuery, "page"),
		"last_run":           nil,
		"collecting_enabled": cred != nil && cred.CollectReceipts,
		"has_portal_login":   cred != nil && cred.PortalUsername != "",
	}
	if run != nil {
		data["last_run"] = ObjectMap(run, map[string]any{"get_status_display": runStatusLabels[run.Status]})
	}
	for k, v := range ctx {
		data[k] = v
	}
	c.Render("ledger/list.html", data)
}

func (a *App) tokenReceipts(c *Ctx) {
	org := c.orgOr404()
	if org == nil {
		return
	}
	item, err := a.Store.RegisteredTokenByNumber(c.Context(), org.ID, c.PathValue("token"))
	if err != nil {
		c.ServerError(err)
		return
	}
	if item == nil {
		c.NotFound()
		return
	}
	tpl := "ledger/receipts.html"
	if c.Query("partial") != "" {
		tpl = "ledger/_receipts.html"
	}
	c.Render(tpl, Data{"item": tokenObj(*item)})
}

func (a *App) sendWorkbook(c *Ctx, data []byte, err error, prefix string) {
	if err != nil {
		c.ServerError(err)
		return
	}
	today := time.Now().In(a.Cfg.TimeZone).Format("2006-01-02")
	c.W.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.W.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.xlsx"`, prefix, today))
	_, _ = c.W.Write(data)
}

func (a *App) accountsExport(c *Ctx) {
	org := c.orgOr404()
	if org == nil {
		return
	}
	filter, _ := tokenFilterFrom(c)
	tokens, err := a.Store.ListRegisteredTokens(c.Context(), org.ID, filter, 0, 0)
	if err != nil {
		c.ServerError(err)
		return
	}
	data, err := ledger.BuildReceiptsWorkbook(tokens, a.Cfg.TimeZone)
	a.sendWorkbook(c, data, err, "accounts")
}

func (a *App) collectNow(c *Ctx) {
	if c.R.Method != http.MethodPost {
		http.Error(c.W, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	org := c.Org()
	cred, err := a.Store.PortalCredentialFor(c.Context(), org.ID)
	if err != nil {
		c.ServerError(err)
		return
	}
	if cred == nil || cred.PortalUsername == "" {
		c.Message("error", "Set the portal login on the Organization page first.")
	} else {
		if _, err := a.Jobs.Enqueue(c.Context(), jobs.KindCollectReceipts, map[string]any{"org_id": org.ID, "attempt": 0}, timeZero); err != nil {
			c.ServerError(err)
			return
		}
		c.Message("success", "Collecting receipts from the portal now. Refresh this page in a few minutes.")
	}
	c.SaveSession()
	c.RedirectTo("ledger:list")
}

// --- Payments: the office's own expenses, with their bills -------------------------

func expenseFilterFrom(c *Ctx) (store.ExpenseFilter, map[string]any) {
	q := strings.TrimSpace(c.Query("q"))
	category := strings.TrimSpace(c.Query("category"))
	valid := false
	for _, ch := range models.ExpenseCategories {
		if ch[0] == category {
			valid = true
		}
	}
	if !valid {
		category = ""
	}
	from, to := safeDate(c.Query("from")), safeDate(c.Query("to"))
	return store.ExpenseFilter{Q: q, Category: category, From: from, To: to}, map[string]any{
		"q": q, "category": category, "date_from": c.Query("from"), "date_to": c.Query("to"),
		"filtered": q != "" || category != "" || from != nil || to != nil,
	}
}

func choiceMaps(choices [][2]string) []map[string]string {
	out := make([]map[string]string, len(choices))
	for i, c := range choices {
		out[i] = map[string]string{"value": c[0], "label": c[1]}
	}
	return out
}

func billObj(b models.ExpenseBill) Obj {
	name := b.OriginalName
	if name == "" {
		name = b.File
	}
	return ObjectMap(b, map[string]any{"pk": b.ID, "is_image": !strings.HasSuffix(strings.ToLower(b.File), ".pdf"),
		"__str__": name})
}

func expenseObj(e models.Expense) map[string]any {
	bills := make([]any, len(e.Bills))
	for i, b := range e.Bills {
		bills[i] = billObj(b)
	}
	return ObjectMap(e, map[string]any{"pk": e.ID, "bills": bills,
		"get_category_display": e.CategoryLabel(), "get_payment_mode_display": e.ModeLabel()})
}

func (a *App) paymentsList(c *Ctx) {
	org := c.orgOr404()
	if org == nil {
		return
	}
	filter, ctx := expenseFilterFrom(c)
	total, err := a.Store.CountExpenses(c.Context(), org.ID, filter)
	if err != nil {
		c.ServerError(err)
		return
	}
	pager := NewPager(total, 25, c.Query("page"))
	expenses, err := a.Store.ListExpenses(c.Context(), org.ID, filter, pager.PerPage, pager.Offset())
	if err != nil {
		c.ServerError(err)
		return
	}
	items := make([]any, len(expenses))
	for i, e := range expenses {
		items[i] = expenseObj(e)
	}
	totals, err := a.Store.ExpenseTotalsByCategory(c.Context(), org.ID, filter)
	if err != nil {
		c.ServerError(err)
		return
	}
	var sum = zeroDecimal()
	byCategory := make([]map[string]any, len(totals))
	for i, t := range totals {
		label := t.Category
		for _, ch := range models.ExpenseCategories {
			if ch[0] == t.Category {
				label = ch[1]
			}
		}
		byCategory[i] = map[string]any{"label": label, "total": t.Total}
		sum = sum.Add(t.Total)
	}
	data := Data{
		"page": pager.Map(items), "categories": choiceMaps(models.ExpenseCategories), "total": sum,
		"by_category": byCategory, "query_string": queryWithout(c.R.URL.RawQuery, "page"),
	}
	for k, v := range ctx {
		data[k] = v
	}
	c.Render("ledger/payments.html", data)
}

// Bill uploads: what is accepted.
var billExtensions = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".heic": true, ".heif": true, ".pdf": true}

const (
	billMaxBytes = 15 * 1024 * 1024
	billMaxFiles = 10
)

type uploadedBill struct {
	Name string
	Size int64
	Open func() (io.ReadCloser, error)
}

// cleanBills checks uploaded bill files and returns the error messages (none
// when they are all fine).
func cleanBills(files []uploadedBill, existing int) []string {
	var errs []string
	if existing+len(files) > billMaxFiles {
		errs = append(errs, fmt.Sprintf("Up to %d bills per payment.", billMaxFiles))
	}
	for _, f := range files {
		ext := strings.ToLower(path.Ext(f.Name))
		switch {
		case !billExtensions[ext]:
			errs = append(errs, fmt.Sprintf("%s: only photos (JPG, PNG, WEBP, HEIC) or PDF files.", f.Name))
		case f.Size > billMaxBytes:
			errs = append(errs, fmt.Sprintf("%s: larger than 15 MB.", f.Name))
		}
	}
	return errs
}

func newExpenseForm(e *models.Expense, today time.Time) *forms.Form {
	date := &forms.Field{Name: "date", Label: "Date", Kind: forms.Date, Widget: forms.DateInput, Required: true,
		Attrs: map[string]string{"class": "form-control"}}
	category := forms.ChoiceField("category", "Category", true, forms.WithBlank("Choose a category", expenseChoices(models.ExpenseCategories)))
	category.Attrs = map[string]string{"class": "form-select"}
	amount := &forms.Field{Name: "amount", Label: "Amount (Rs)", Kind: forms.Decimal, Widget: forms.NumberInput, Required: true,
		MaxDigits: 12, DecimalPlaces: 2,
		Attrs: map[string]string{"class": "form-control", "min": "0.01", "inputmode": "decimal"}}
	mode := forms.ChoiceField("payment_mode", "Paid by", false, forms.WithBlank("Not recorded", expenseChoices(models.ExpenseModes)))
	mode.Attrs = map[string]string{"class": "form-select"}
	description := forms.CharField("description", "Details", false, 255)
	description.Attrs = map[string]string{"class": "form-control", "placeholder": "e.g. October rent, Anil's salary, printer ink"}
	description.HelpText = "What it was for, e.g. the month, the staff member or the shop."
	f := forms.New("", date, category, amount, mode, description)
	f.Initial = map[string]string{}
	if e != nil {
		f.Initial["date"] = e.Date.Format("2006-01-02")
		f.Initial["category"] = e.Category
		f.Initial["amount"] = e.Amount.String()
		f.Initial["payment_mode"] = e.PaymentMode
		f.Initial["description"] = e.Description
	} else {
		f.Initial["date"] = today.Format("2006-01-02")
	}
	f.Clean = func(f *forms.Form) {
		if d := f.DecimalVal("amount"); d != nil && d.Cmp(zeroDecimal()) <= 0 {
			f.AddError("amount", "Enter an amount above zero.")
		}
	}
	return f
}

func expenseChoices(c [][2]string) []forms.ChoiceOpt {
	out := make([]forms.ChoiceOpt, len(c))
	for i, ch := range c {
		out[i] = forms.ChoiceOpt{Value: ch[0], Label: ch[1]}
	}
	return out
}

func canDeleteExpense(c *Ctx, e *models.Expense) bool {
	return c.IsOrgAdmin() || (e.CreatedByID != nil && *e.CreatedByID == c.User.ID)
}

func (a *App) expenseForm(c *Ctx, expense *models.Expense) {
	org := c.orgOr404()
	if org == nil {
		return
	}
	var existing []models.ExpenseBill
	if expense != nil {
		existing = expense.Bills
	}
	form := newExpenseForm(expense, time.Now().In(a.Cfg.TimeZone))
	var billErrors []string
	if c.R.Method == http.MethodPost {
		post := c.PostForm()
		form.Bind(post)
		var files []uploadedBill
		if c.R.MultipartForm != nil {
			for _, h := range c.R.MultipartForm.File["bills"] {
				h := h
				files = append(files, uploadedBill{Name: path.Base(strings.ReplaceAll(h.Filename, "\\", "/")), Size: h.Size,
					Open: func() (io.ReadCloser, error) { return h.Open() }})
			}
		}
		remove := map[int64]bool{}
		for _, v := range post["remove_bill"] {
			for _, b := range existing {
				if strconv.FormatInt(b.ID, 10) == v {
					remove[b.ID] = true
				}
			}
		}
		billErrors = cleanBills(files, len(existing)-len(remove))
		if form.IsValid() && len(billErrors) == 0 {
			item, err := a.saveExpense(c, org, expense, form, existing, remove, files)
			if err != nil {
				c.ServerError(err)
				return
			}
			c.Message("success", fmt.Sprintf("Saved: %s, ₹%s.", item.CategoryLabel(), item.Amount.String()))
			c.SaveSession()
			if post.Get("next") == "add" {
				c.RedirectTo("ledger:payment_add")
			} else {
				c.RedirectTo("ledger:payments")
			}
			return
		}
	}
	bills := make([]any, len(existing))
	for i, b := range existing {
		bills[i] = billObj(b)
	}
	data := Data{"form": form.Template(), "expense": nil, "bills": bills, "bill_errors": billErrors,
		"can_delete": expense != nil && canDeleteExpense(c, expense)}
	if expense != nil {
		data["expense"] = expenseObj(*expense)
	}
	c.Render("ledger/payment_form.html", data)
}

func (a *App) saveExpense(c *Ctx, org *models.Organization, expense *models.Expense, form *forms.Form,
	existing []models.ExpenseBill, remove map[int64]bool, files []uploadedBill) (*models.Expense, error) {
	item := expense
	if item == nil {
		uid := c.User.ID
		item = &models.Expense{OrganizationID: org.ID, CreatedByID: &uid}
	}
	if d := form.DateVal("date"); d != nil {
		item.Date = *d
	}
	item.Category = form.Str("category")
	if d := form.DecimalVal("amount"); d != nil {
		item.Amount = *d
	}
	// Django keeps a stored value when its field with a default is missing from
	// the POST altogether (construct_instance): payment_mode and description.
	if _, sent := form.Data["payment_mode"]; sent || expense == nil {
		item.PaymentMode = form.Str("payment_mode")
	}
	if _, sent := form.Data["description"]; sent || expense == nil {
		item.Description = form.Str("description")
	}
	var removedFiles []string
	err := a.Store.InTx(c.Context(), func(tx *store.Store) error {
		if err := tx.SaveExpense(c.Context(), item); err != nil {
			return err
		}
		for _, b := range existing {
			if remove[b.ID] {
				if err := tx.DeleteBill(c.Context(), b.ID); err != nil {
					return err
				}
				removedFiles = append(removedFiles, b.File)
			}
		}
		for _, f := range files {
			rc, err := f.Open()
			if err != nil {
				return err
			}
			data, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return err
			}
			name := store.NewUUID()
			name = strings.ReplaceAll(name, "-", "") + strings.ToLower(path.Ext(f.Name))
			stored, err := a.SaveMedia(fmt.Sprintf("expense_bills/%d/%s", org.ID, name), data)
			if err != nil {
				return err
			}
			orig := f.Name
			if r := []rune(orig); len(r) > 255 {
				orig = string(r[:255])
			}
			if err := tx.AddBill(c.Context(), &models.ExpenseBill{ExpenseID: item.ID, File: stored, OriginalName: orig}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, name := range removedFiles {
		a.removeMedia(name)
	}
	return item, nil
}

func (a *App) removeMedia(name string) {
	if full, err := a.MediaPath(name); err == nil {
		if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
			a.Log.Warn("could not remove a stored file", "file", name, "err", err)
		}
	}
}

func (a *App) paymentAdd(c *Ctx) { a.expenseForm(c, nil) }

func (c *Ctx) expenseOr404(org *models.Organization) *models.Expense {
	pk, err := strconv.ParseInt(c.PathValue("pk"), 10, 64)
	if err != nil {
		c.NotFound()
		return nil
	}
	e, err := c.App.Store.ExpenseByID(c.Context(), org.ID, pk)
	if err != nil {
		c.ServerError(err)
		return nil
	}
	if e == nil {
		c.NotFound()
	}
	return e
}

func (a *App) paymentEdit(c *Ctx) {
	org := c.orgOr404()
	if org == nil {
		return
	}
	if e := c.expenseOr404(org); e != nil {
		a.expenseForm(c, e)
	}
}

func (a *App) paymentDelete(c *Ctx) {
	if c.R.Method != http.MethodPost {
		http.Error(c.W, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	org := c.orgOr404()
	if org == nil {
		return
	}
	e := c.expenseOr404(org)
	if e == nil {
		return
	}
	if !canDeleteExpense(c, e) {
		c.Message("error", "Only your organization's admin or the person who added it can delete this payment.")
		c.SaveSession()
		c.RedirectTo("ledger:payment_edit", e.ID)
		return
	}
	if err := a.Store.DeleteExpense(c.Context(), e.ID); err != nil {
		c.ServerError(err)
		return
	}
	for _, b := range e.Bills {
		a.removeMedia(b.File)
	}
	c.Message("success", "Payment deleted.")
	c.SaveSession()
	c.RedirectTo("ledger:payments")
}

// paymentBill serves a bill's file, only to members of the organization it belongs to.
func (a *App) paymentBill(c *Ctx) {
	org := c.orgOr404()
	if org == nil {
		return
	}
	pk, err := strconv.ParseInt(c.PathValue("pk"), 10, 64)
	if err != nil {
		c.NotFound()
		return
	}
	bill, err := a.Store.BillForOrganization(c.Context(), org.ID, pk)
	if err != nil {
		c.ServerError(err)
		return
	}
	if bill == nil {
		c.NotFound()
		return
	}
	full, err := a.MediaPath(bill.File)
	if err != nil {
		c.NotFound()
		return
	}
	f, err := os.Open(full)
	if err != nil {
		c.NotFound()
		return
	}
	defer f.Close()
	info, _ := f.Stat()
	ctype := mime.TypeByExtension(strings.ToLower(filepath.Ext(bill.File)))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	name := bill.OriginalName
	if name == "" {
		name = filepath.Base(bill.File)
	}
	disposition := "inline"
	if c.Query("download") != "" {
		disposition = "attachment"
	}
	c.W.Header().Set("Content-Type", ctype)
	c.W.Header().Set("Content-Disposition", contentDisposition(disposition, name))
	c.W.Header().Set("Cache-Control", "private, no-store")
	if info != nil {
		c.W.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	}
	_, _ = io.Copy(c.W, f)
}

func (a *App) paymentsExport(c *Ctx) {
	org := c.orgOr404()
	if org == nil {
		return
	}
	filter, _ := expenseFilterFrom(c)
	expenses, err := a.Store.ListExpenses(c.Context(), org.ID, filter, 0, 0)
	if err != nil {
		c.ServerError(err)
		return
	}
	data, err := ledger.BuildExpensesWorkbook(expenses)
	a.sendWorkbook(c, data, err, "payments")
}

func zeroDecimal() db.Decimal { return db.DecimalFromInt(0) }

// contentDisposition is Django's content_disposition_header: a quoted
// filename for plain ASCII names, the RFC 5987 form for anything else.
func contentDisposition(disposition, name string) string {
	ascii := true
	for _, r := range name {
		if r > 127 {
			ascii = false
		}
	}
	if ascii {
		escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(name)
		return fmt.Sprintf(`%s; filename="%s"`, disposition, escaped)
	}
	return fmt.Sprintf("%s; filename*=utf-8''%s", disposition, url.PathEscape(name))
}

// Package models holds the row types of the DeedFlow tables. Table and column
// names are the Django project's, so one database serves both.
package models

import (
	"encoding/json"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/db"
)

// User is a row of auth_user.
type User struct {
	ID          int64      `db:"id"`
	Password    string     `db:"password"`
	LastLogin   *time.Time `db:"last_login"`
	IsSuperuser bool       `db:"is_superuser"`
	Username    string     `db:"username"`
	FirstName   string     `db:"first_name"`
	LastName    string     `db:"last_name"`
	Email       string     `db:"email"`
	IsStaff     bool       `db:"is_staff"`
	IsActive    bool       `db:"is_active"`
	DateJoined  time.Time  `db:"date_joined"`
}

// FullName is Django's get_full_name().
func (u *User) FullName() string {
	name := u.FirstName + " " + u.LastName
	for len(name) > 0 && name[0] == ' ' {
		name = name[1:]
	}
	for len(name) > 0 && name[len(name)-1] == ' ' {
		name = name[:len(name)-1]
	}
	return name
}

// DisplayName is the full name, else the username.
func (u *User) DisplayName() string {
	if n := u.FullName(); n != "" {
		return n
	}
	return u.Username
}

// Organization is an office using DeedFlow.
type Organization struct {
	ID        int64     `db:"id"`
	Name      string    `db:"name"`
	CreatedAt time.Time `db:"created_at"`
}

// Membership roles.
const (
	RoleAdmin = "admin"
	RoleStaff = "staff"
)

// Membership says which organization a user belongs to, and as what.
type Membership struct {
	ID             int64     `db:"id"`
	Role           string    `db:"role"`
	CreatedAt      time.Time `db:"created_at"`
	OrganizationID int64     `db:"organization_id"`
	UserID         int64     `db:"user_id"`
}

// IsAdmin reports whether the member administers the organization.
func (m *Membership) IsAdmin() bool { return m.Role == RoleAdmin }

// PortalCredential is the organization's Registration Department login.
type PortalCredential struct {
	ID               int64     `db:"id"`
	PortalUsername   string    `db:"portal_username"`
	PortalPassword   string    `db:"portal_password"` // Fernet ciphertext
	UpdatedAt        time.Time `db:"updated_at"`
	MyDeedsMenuIndex int16     `db:"my_deeds_menu_index"`
	NewDeedMenuIndex int16     `db:"new_deed_menu_index"`
	OrganizationID   int64     `db:"organization_id"`
	AlertEmail       string    `db:"alert_email"`
	CollectReceipts  bool      `db:"collect_receipts"`
}

// Deed statuses.
const (
	StatusDraft          = "draft"
	StatusExtracting     = "extracting"
	StatusStructuring    = "structuring"
	StatusReadyForReview = "ready_for_review"
	StatusSubmitting     = "submitting"
	StatusCompleted      = "completed"
	StatusFailed         = "failed"
)

// StatusChoices are the (value, label) pairs of Deed.status.
var StatusChoices = [][2]string{
	{StatusDraft, "Draft"},
	{StatusExtracting, "Extracting PDF"},
	{StatusStructuring, "Structuring with AI"},
	{StatusReadyForReview, "Ready for review"},
	{StatusSubmitting, "Submitting to portal"},
	{StatusCompleted, "Completed"},
	{StatusFailed, "Failed"},
}

// StatusLabel is the display name of a status value.
func StatusLabel(s string) string {
	for _, c := range StatusChoices {
		if c[0] == s {
			return c[1]
		}
	}
	return s
}

// Deed is one deed registration case.
type Deed struct {
	ID             int64           `db:"id"`
	PublicID       string          `db:"public_id"`
	OwnerID        int64           `db:"owner_id"`
	OrganizationID *int64          `db:"organization_id"`
	Title          string          `db:"title"`
	IsTest         bool            `db:"is_test"`
	Status         string          `db:"status"`
	Progress       int16           `db:"progress"`
	TokenNumber    string          `db:"token_number"`
	SourcePDF      *string         `db:"source_pdf"`
	ExtractedText  string          `db:"extracted_text"`
	AIRawResponse  json.RawMessage `db:"ai_raw_response"`
	ResultMessage  string          `db:"result_message"`
	PortalSnapshot Snapshot        `db:"portal_snapshot"`
	SurveyMapping  []SurveyMapping `db:"survey_mapping"`
	CreatedAt      time.Time       `db:"created_at"`
	UpdatedAt      time.Time       `db:"updated_at"`
}

// String is Deed.__str__: the title, else the token, else "Deed #id".
func (d *Deed) String() string {
	if d.Title != "" {
		return d.Title
	}
	if d.TokenNumber != "" {
		return d.TokenNumber
	}
	return "Deed #" + itoa(d.ID)
}

// RegistrationDetail is the deed's registration page.
type RegistrationDetail struct {
	ID          int64  `db:"id"`
	DeedID      int64  `db:"deed_id"`
	District    string `db:"district"`
	SRO         string `db:"sro"`
	Taluk       string `db:"taluk"`
	Book        int32  `db:"book"`
	DeedType    string `db:"deed_type"`
	DeedSubType string `db:"deed_sub_type"`
}

// Presenter is the person presenting the deed.
type Presenter struct {
	ID                 int64      `db:"id"`
	DeedID             int64      `db:"deed_id"`
	Name               string     `db:"name"`
	PresenterType      string     `db:"presenter_type"`
	HouseName          string     `db:"house_name"`
	RelativeName       string     `db:"relative_name"`
	Relation           string     `db:"relation"`
	Gender             string     `db:"gender"`
	Place              string     `db:"place"`
	Post               string     `db:"post"`
	Pin                string     `db:"pin"`
	Age                *int16     `db:"age"`
	Mobile             string     `db:"mobile"`
	Job                string     `db:"job"`
	IDType             string     `db:"id_type"`
	IDNumber           string     `db:"id_number"`
	PresenterDate      *time.Time `db:"presenter_date"`
	DocumentSheetCount *int16     `db:"document_sheet_count"`
	StampMode          string     `db:"stamp_mode"`
	StampDate          *time.Time `db:"stamp_date"`
	DocumentCopies     int16      `db:"document_copies"`
	DocumentValue      *int32     `db:"document_value"`
	DocumentFees       *int32     `db:"document_fees"`
}

// PriorDocument is a previously registered document the deed cites.
type PriorDocument struct {
	ID           int64  `db:"id"`
	DeedID       int64  `db:"deed_id"`
	DocumentType string `db:"document_type"`
	District     string `db:"district"`
	SRO          string `db:"sro"`
	Number       string `db:"number"`
	Year         *int16 `db:"year"`
	Book         *int16 `db:"book"`
	Volume       *int32 `db:"volume"`
	PageFrom     *int32 `db:"page_from"`
	PageTo       *int32 `db:"page_to"`
	Remarks      string `db:"remarks"`
	Position     int32  `db:"position"`
}

// Party is the shared shape of an Executant and a Claimant.
type Party struct {
	ID           int64  `db:"id"`
	DeedID       int64  `db:"deed_id"`
	Name         string `db:"name"`
	Capacity     string `db:"capacity"`
	HouseName    string `db:"house_name"`
	Age          *int16 `db:"age"`
	Job          string `db:"job"`
	RelativeName string `db:"relative_name"`
	Relation     string `db:"relation"`
	Gender       string `db:"gender"`
	Place        string `db:"place"`
	Post         string `db:"post"`
	Pin          string `db:"pin"`
	District     string `db:"district"`
	Taluk        string `db:"taluk"`
	Village      string `db:"village"`
	IDType       string `db:"id_type"`
	IDNumber     string `db:"id_number"`
	Mobile       string `db:"mobile"`
	Position     int32  `db:"position"`
}

// ScheduleItem is one item of land being transferred.
type ScheduleItem struct {
	ID              int64       `db:"id"`
	DeedID          int64       `db:"deed_id"`
	Number          string      `db:"number"`
	District        string      `db:"district"`
	SRO             string      `db:"sro"`
	Taluk           string      `db:"taluk"`
	Village         string      `db:"village"`
	Desam           string      `db:"desam"`
	LocalBodyType   string      `db:"local_body_type"`
	LocalBodyName   string      `db:"local_body_name"`
	ResurveyBlock   string      `db:"resurvey_block"`
	ResurveyNumber  string      `db:"resurvey_number"`
	ResurveySub     string      `db:"resurvey_sub"`
	OldSurvey       string      `db:"old_survey"`
	OldSub          string      `db:"old_sub"`
	TPNumber        string      `db:"tp_number"`
	LandType        string      `db:"land_type"`
	TotalHectare    *db.Decimal `db:"total_hectare"`
	TotalAre        *db.Decimal `db:"total_are"`
	TotalSqm        *db.Decimal `db:"total_sqm"`
	TransferHectare *db.Decimal `db:"transfer_hectare"`
	TransferAre     *db.Decimal `db:"transfer_are"`
	TransferSqm     *db.Decimal `db:"transfer_sqm"`
	Price           *int32      `db:"price"`
	FairValue       *int32      `db:"fair_value"`
	Pokkuvaravu     string      `db:"pokkuvaravu"`
	UndividedShare  string      `db:"undivided_share"`
	Share           string      `db:"share"`
	ShareOutOf      string      `db:"share_out_of"`
	Details         string      `db:"details"`
	BoundaryEast    string      `db:"boundary_east"`
	BoundaryNorth   string      `db:"boundary_north"`
	BoundaryWest    string      `db:"boundary_west"`
	BoundarySouth   string      `db:"boundary_south"`
	HasBuilding     string      `db:"has_building"`
	HasWell         string      `db:"has_well"`
	Paattam         string      `db:"paattam"`
	Position        int32       `db:"position"`
}

// Witness is an attesting or identifying witness.
type Witness struct {
	ID                int64  `db:"id"`
	DeedID            int64  `db:"deed_id"`
	WitnessType       string `db:"witness_type"`
	PortalWitnessType string `db:"portal_witness_type"`
	Name              string `db:"name"`
	Gender            string `db:"gender"`
	Relation          string `db:"relation"`
	RelativeName      string `db:"relative_name"`
	HouseName         string `db:"house_name"`
	Job               string `db:"job"`
	Place             string `db:"place"`
	Desam             string `db:"desam"`
	Position          int32  `db:"position"`
}

// DeedScreenshot is a screenshot taken during portal submission.
type DeedScreenshot struct {
	ID        int64     `db:"id"`
	DeedID    int64     `db:"deed_id"`
	Label     string    `db:"label"`
	Image     string    `db:"image"`
	CreatedAt time.Time `db:"created_at"`
}

// PortalDropdown is one dropdown's options as the portal offers them.
type PortalDropdown struct {
	ID        int64           `db:"id"`
	Page      string          `db:"page"`
	Field     string          `db:"field"`
	Label     string          `db:"label"`
	Parents   json.RawMessage `db:"parents"`
	ParentKey string          `db:"parent_key"`
	Options   json.RawMessage `db:"options"`
	ScrapedAt time.Time       `db:"scraped_at"`
}

// FilingSheet is a deed written out as a filing sheet.
type FilingSheet struct {
	ID             int64           `db:"id"`
	PublicID       string          `db:"public_id"`
	OwnerID        int64           `db:"owner_id"`
	OrganizationID *int64          `db:"organization_id"`
	Name           string          `db:"name"`
	SourceFormat   string          `db:"source_format"`
	Sheet          json.RawMessage `db:"sheet"`
	Content        string          `db:"content"`
	Settings       json.RawMessage `db:"settings"`
	Pages          int32           `db:"pages"`
	CreatedAt      time.Time       `db:"created_at"`
	UpdatedAt      time.Time       `db:"updated_at"`
}

// RegisteredToken is a registered token whose receipts are collected.
type RegisteredToken struct {
	ID               int64       `db:"id"`
	OrganizationID   int64       `db:"organization_id"`
	Token            string      `db:"token"`
	PresenterNameML  string      `db:"presenter_name_ml"`
	PresentationDate *time.Time  `db:"presentation_date"`
	PresenterPlaceML string      `db:"presenter_place_ml"`
	PresenterAddrML  string      `db:"presenter_address_ml"`
	PresenterCityML  string      `db:"presenter_city_ml"`
	PresentedAt      *time.Time  `db:"presented_at"`
	DocumentAmount   *db.Decimal `db:"document_amount"`
	ApplicantName    string      `db:"applicant_name"`
	SRO              string      `db:"sro"`
	Status           string      `db:"status"`
	Attempts         int16       `db:"attempts"`
	LastError        string      `db:"last_error"`
	FirstSeenAt      time.Time   `db:"first_seen_at"`
	CollectedAt      *time.Time  `db:"collected_at"`

	Receipts []PaymentReceipt `db:"-"`
}

// Registered token statuses.
const (
	TokenPending   = "pending"
	TokenCollected = "collected"
	TokenFailed    = "failed"
)

// Receipt kinds.
const (
	KindStampDuty       = "SD"
	KindRegistrationFee = "RF"
)

// Name is the applicant's name, else the presenter's (Malayalam) name.
func (t *RegisteredToken) Name() string {
	if t.ApplicantName != "" {
		return t.ApplicantName
	}
	return t.PresenterNameML
}

// ReceiptsOf returns the token's receipts of one kind.
func (t *RegisteredToken) ReceiptsOf(kind string) []PaymentReceipt {
	var out []PaymentReceipt
	for _, r := range t.Receipts {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

// StampReceipts are the stamp duty receipts.
func (t *RegisteredToken) StampReceipts() []PaymentReceipt { return t.ReceiptsOf(KindStampDuty) }

// FeeReceipts are the registration fee receipts.
func (t *RegisteredToken) FeeReceipts() []PaymentReceipt { return t.ReceiptsOf(KindRegistrationFee) }

// TotalOf sums receipt amounts; nil when there are no receipts.
func TotalOf(receipts []PaymentReceipt) *db.Decimal {
	if len(receipts) == 0 {
		return nil
	}
	sum := db.DecimalFromInt(0)
	for _, r := range receipts {
		if r.Amount != nil {
			sum = sum.Add(*r.Amount)
		}
	}
	return &sum
}

// PaymentReceipt is one stamp duty or registration fee receipt.
type PaymentReceipt struct {
	ID            int64           `db:"id"`
	TokenID       int64           `db:"token_id"`
	Kind          string          `db:"kind"`
	TransactionNo string          `db:"transaction_no"`
	ApplicantName string          `db:"applicant_name"`
	SRO           string          `db:"sro"`
	GRN           string          `db:"grn"`
	Amount        *db.Decimal     `db:"amount"`
	PaymentMode   string          `db:"payment_mode"`
	BankReference string          `db:"bank_reference"`
	BankCIN       string          `db:"bank_cin"`
	PaidAt        *time.Time      `db:"paid_at"`
	Status        string          `db:"status"`
	Fields        json.RawMessage `db:"fields"`
	CollectedAt   time.Time       `db:"collected_at"`
}

// KindLabel is the display name of a receipt kind.
func (r *PaymentReceipt) KindLabel() string {
	if r.Kind == KindStampDuty {
		return "Stamp duty"
	}
	return "Registration fee"
}

// Receipt sync run statuses.
const (
	RunRunning = "running"
	RunDone    = "done"
	RunPartial = "partial"
	RunFailed  = "failed"
)

// ReceiptSyncRun is one run of the nightly receipt collection.
type ReceiptSyncRun struct {
	ID              int64      `db:"id"`
	OrganizationID  int64      `db:"organization_id"`
	Attempt         int16      `db:"attempt"`
	Status          string     `db:"status"`
	StartedAt       time.Time  `db:"started_at"`
	FinishedAt      *time.Time `db:"finished_at"`
	RegisteredFound int32      `db:"registered_found"`
	Collected       int32      `db:"collected"`
	Pending         int32      `db:"pending"`
	Message         string     `db:"message"`
	AlertSentTo     string     `db:"alert_sent_to"`
}

// Expense categories and payment modes.
var (
	ExpenseCategories = [][2]string{
		{"rent", "Room rent"}, {"electricity", "Electricity bill"}, {"telephone", "Telephone bill"},
		{"salary", "Staff salary"}, {"other", "Other expenses"},
	}
	ExpenseModes = [][2]string{
		{"cash", "Cash"}, {"upi", "UPI"}, {"bank", "Bank transfer"}, {"cheque", "Cheque"}, {"card", "Card"},
	}
)

func choiceLabel(choices [][2]string, v string) string {
	for _, c := range choices {
		if c[0] == v {
			return c[1]
		}
	}
	return v
}

// Expense is a payment the office made for itself.
type Expense struct {
	ID             int64      `db:"id"`
	OrganizationID int64      `db:"organization_id"`
	Date           time.Time  `db:"date"`
	Category       string     `db:"category"`
	Amount         db.Decimal `db:"amount"`
	PaymentMode    string     `db:"payment_mode"`
	Description    string     `db:"description"`
	CreatedByID    *int64     `db:"created_by_id"`
	CreatedAt      time.Time  `db:"created_at"`
	UpdatedAt      time.Time  `db:"updated_at"`

	Bills     []ExpenseBill `db:"-"`
	CreatedBy *User         `db:"-"`
}

// CategoryLabel is the display name of the category.
func (e *Expense) CategoryLabel() string { return choiceLabel(ExpenseCategories, e.Category) }

// ModeLabel is the display name of the payment mode.
func (e *Expense) ModeLabel() string { return choiceLabel(ExpenseModes, e.PaymentMode) }

// ExpenseBill is a photo or file of a bill.
type ExpenseBill struct {
	ID           int64     `db:"id"`
	ExpenseID    int64     `db:"expense_id"`
	File         string    `db:"file"`
	OriginalName string    `db:"original_name"`
	UploadedAt   time.Time `db:"uploaded_at"`
}

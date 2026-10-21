package store

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/db"
	"github.com/shahinshabab/deedflow-go/internal/deeds"
	"github.com/shahinshabab/deedflow-go/internal/models"
)

// NewUUID makes a random (version 4) UUID string.
func NewUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

var (
	deedCols     = db.Columns[models.Deed]("")
	regCols      = db.Columns[models.RegistrationDetail]("")
	presenterCol = db.Columns[models.Presenter]("")
	priorCols    = db.Columns[models.PriorDocument]("")
	partyCols    = db.Columns[models.Party]("")
	schedCols    = db.Columns[models.ScheduleItem]("")
	witnessCols  = db.Columns[models.Witness]("")
	shotCols     = db.Columns[models.DeedScreenshot]("")
)

// Viewer says which deeds a user may see: all of their organization's, or -
// for a user outside any organization - only their own.
type Viewer struct {
	UserID int64
	OrgID  *int64
}

func (v Viewer) clause(alias string, argN int) (string, any) {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	if v.OrgID != nil {
		return fmt.Sprintf("%sorganization_id = $%d", prefix, argN), *v.OrgID
	}
	return fmt.Sprintf("%sowner_id = $%d", prefix, argN), v.UserID
}

// ViewerFor works out the Viewer of a user.
func (s *Store) ViewerFor(ctx context.Context, userID int64) (Viewer, error) {
	m, err := s.MembershipOf(ctx, userID)
	if err != nil {
		return Viewer{}, err
	}
	v := Viewer{UserID: userID}
	if m != nil {
		id := m.OrganizationID
		v.OrgID = &id
	}
	return v, nil
}

// CreateDeed inserts a deed. Like Deed.save() in Django, a deed without an
// organization takes its owner's.
func (s *Store) CreateDeed(ctx context.Context, d *models.Deed) error {
	if d.PublicID == "" {
		d.PublicID = NewUUID()
	}
	if d.Status == "" {
		d.Status = models.StatusDraft
	}
	if d.OrganizationID == nil {
		m, err := s.MembershipOf(ctx, d.OwnerID)
		if err != nil {
			return err
		}
		if m != nil {
			id := m.OrganizationID
			d.OrganizationID = &id
		}
	}
	if d.PortalSnapshot.Sections == nil {
		d.PortalSnapshot = models.NewSnapshot()
	}
	if d.SurveyMapping == nil {
		d.SurveyMapping = []models.SurveyMapping{}
	}
	d.CreatedAt, d.UpdatedAt = now(), now()
	_, err := db.Insert(ctx, s.q(), "deeds_deed", d)
	return err
}

// SaveDeed writes the named columns (all if none) and bumps updated_at.
func (s *Store) SaveDeed(ctx context.Context, d *models.Deed, cols ...string) error {
	d.UpdatedAt = now()
	if len(cols) > 0 {
		cols = append(cols, "updated_at")
	}
	return db.Update(ctx, s.q(), "deeds_deed", d, cols...)
}

// SaveDeedQuiet writes the named columns without touching updated_at (the
// stale-run reapers go by updated_at, which only progress updates move).
func (s *Store) SaveDeedQuiet(ctx context.Context, d *models.Deed, cols ...string) error {
	return db.Update(ctx, s.q(), "deeds_deed", d, cols...)
}

// TouchDeed bumps updated_at alone (the run's heartbeat).
func (s *Store) TouchDeed(ctx context.Context, id int64) error {
	_, err := s.q().Exec(ctx, `UPDATE deeds_deed SET updated_at = now() WHERE id = $1`, id)
	return err
}

// DeedByID loads a deed regardless of viewer (workers).
func (s *Store) DeedByID(ctx context.Context, id int64) (*models.Deed, error) {
	return db.One[models.Deed](ctx, s.q(), `SELECT `+deedCols+` FROM deeds_deed WHERE id = $1`, id)
}

// DeedByPublicID loads a deed the viewer may see.
func (s *Store) DeedByPublicID(ctx context.Context, v Viewer, publicID string) (*models.Deed, error) {
	clause, arg := v.clause("", 2)
	return db.One[models.Deed](ctx, s.q(), `SELECT `+deedCols+` FROM deeds_deed WHERE public_id = $1 AND `+clause, publicID, arg)
}

// DeedVisible reports whether the viewer may see a deed (the websocket check).
func (s *Store) DeedVisible(ctx context.Context, v Viewer, publicID string) (bool, error) {
	clause, arg := v.clause("", 2)
	var ok bool
	err := s.q().QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM deeds_deed WHERE public_id = $1 AND `+clause+`)`, publicID, arg).Scan(&ok)
	return ok, err
}

// LoadDeedData loads a deed's registration, presenter and all its rows.
func (s *Store) LoadDeedData(ctx context.Context, d *models.Deed) (*deeds.DeedData, error) {
	out := &deeds.DeedData{Deed: d}
	q := s.q()
	var err error
	if r, err := db.One[models.RegistrationDetail](ctx, q, `SELECT `+regCols+` FROM deeds_registrationdetail WHERE deed_id = $1`, d.ID); err == nil {
		out.Registration = r
	} else if err != db.ErrNotFound {
		return nil, err
	}
	if p, err := db.One[models.Presenter](ctx, q, `SELECT `+presenterCol+` FROM deeds_presenter WHERE deed_id = $1`, d.ID); err == nil {
		out.Presenter = p
	} else if err != db.ErrNotFound {
		return nil, err
	}
	if out.PriorDocuments, err = db.All[models.PriorDocument](ctx, q, `SELECT `+priorCols+` FROM deeds_priordocument WHERE deed_id = $1 ORDER BY position, id`, d.ID); err != nil {
		return nil, err
	}
	if out.Executants, err = db.All[models.Party](ctx, q, `SELECT `+partyCols+` FROM deeds_executant WHERE deed_id = $1 ORDER BY position, id`, d.ID); err != nil {
		return nil, err
	}
	if out.Claimants, err = db.All[models.Party](ctx, q, `SELECT `+partyCols+` FROM deeds_claimant WHERE deed_id = $1 ORDER BY position, id`, d.ID); err != nil {
		return nil, err
	}
	if out.Schedule, err = db.All[models.ScheduleItem](ctx, q, `SELECT `+schedCols+` FROM deeds_scheduleitem WHERE deed_id = $1 ORDER BY position, id`, d.ID); err != nil {
		return nil, err
	}
	if out.Witnesses, err = db.All[models.Witness](ctx, q, `SELECT `+witnessCols+` FROM deeds_witness WHERE deed_id = $1 ORDER BY position, id`, d.ID); err != nil {
		return nil, err
	}
	if out.Screenshots, err = db.All[models.DeedScreenshot](ctx, q, `SELECT `+shotCols+` FROM deeds_deedscreenshot WHERE deed_id = $1 ORDER BY created_at, id`, d.ID); err != nil {
		return nil, err
	}
	return out, nil
}

// SaveRegistration inserts or updates the deed's registration page.
func (s *Store) SaveRegistration(ctx context.Context, r *models.RegistrationDetail) error {
	if r.ID == 0 {
		var id int64
		err := s.q().QueryRow(ctx, `SELECT id FROM deeds_registrationdetail WHERE deed_id = $1`, r.DeedID).Scan(&id)
		if err == nil {
			r.ID = id
		}
	}
	if r.ID == 0 {
		_, err := db.Insert(ctx, s.q(), "deeds_registrationdetail", r)
		return err
	}
	return db.Update(ctx, s.q(), "deeds_registrationdetail", r)
}

// SavePresenter inserts or updates the deed's presenter.
func (s *Store) SavePresenter(ctx context.Context, p *models.Presenter) error {
	if p.ID == 0 {
		var id int64
		if err := s.q().QueryRow(ctx, `SELECT id FROM deeds_presenter WHERE deed_id = $1`, p.DeedID).Scan(&id); err == nil {
			p.ID = id
		}
	}
	if p.ID == 0 {
		_, err := db.Insert(ctx, s.q(), "deeds_presenter", p)
		return err
	}
	return db.Update(ctx, s.q(), "deeds_presenter", p)
}

// RowTable names the table of each repeatable section.
var RowTable = map[string]string{
	"documents": "deeds_priordocument", "executants": "deeds_executant", "claimants": "deeds_claimant",
	"schedule": "deeds_scheduleitem", "witnesses": "deeds_witness",
}

// InsertRow inserts one repeatable row (a pointer to a model struct).
func (s *Store) InsertRow(ctx context.Context, table string, row any) error {
	_, err := db.Insert(ctx, s.q(), table, row)
	return err
}

// UpdateRow writes one repeatable row back.
func (s *Store) UpdateRow(ctx context.Context, table string, row any, cols ...string) error {
	return db.Update(ctx, s.q(), table, row, cols...)
}

// DeleteRows removes rows by id from a section table (only of the given deed).
func (s *Store) DeleteRows(ctx context.Context, table string, deedID int64, ids ...int64) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.q().Exec(ctx, `DELETE FROM `+db.Quote(table)+` WHERE deed_id = $1 AND id = ANY($2)`, deedID, ids)
	return err
}

// SetPosition numbers a row's place among its deed's rows.
func (s *Store) SetPosition(ctx context.Context, table string, id int64, position int) error {
	_, err := s.q().Exec(ctx, `UPDATE `+db.Quote(table)+` SET "position" = $2 WHERE id = $1`, id, position)
	return err
}

// ClearSection deletes all of a deed's rows in a section table.
func (s *Store) ClearSection(ctx context.Context, table string, deedID int64) error {
	_, err := s.q().Exec(ctx, `DELETE FROM `+db.Quote(table)+` WHERE deed_id = $1`, deedID)
	return err
}

// AddScreenshot records a stored screenshot image.
func (s *Store) AddScreenshot(ctx context.Context, deedID int64, label, image string) (*models.DeedScreenshot, error) {
	sh := &models.DeedScreenshot{DeedID: deedID, Label: label, Image: image, CreatedAt: now()}
	_, err := db.Insert(ctx, s.q(), "deeds_deedscreenshot", sh)
	return sh, err
}

// ScreenshotVisible reports whether a stored image belongs to a deed the viewer may see.
func (s *Store) ScreenshotVisible(ctx context.Context, v Viewer, image string) (bool, error) {
	clause, arg := v.clause("d", 2)
	var ok bool
	err := s.q().QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM deeds_deedscreenshot sh JOIN deeds_deed d ON d.id = sh.deed_id
		WHERE sh.image = $1 AND `+clause+`)`, image, arg).Scan(&ok)
	return ok, err
}

// SourcePDFVisible reports whether a stored PDF belongs to a deed the viewer may see.
func (s *Store) SourcePDFVisible(ctx context.Context, v Viewer, name string) (bool, error) {
	clause, arg := v.clause("d", 2)
	var ok bool
	err := s.q().QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM deeds_deed d WHERE d.source_pdf = $1 AND `+clause+`)`, name, arg).Scan(&ok)
	return ok, err
}

// ClaimForSubmission atomically moves a deed to "submitting" if it is in one
// of the allowed statuses: two quick clicks (or two tabs) must not start two
// parallel runs against the same deed. It reports whether this call won.
func (s *Store) ClaimForSubmission(ctx context.Context, id int64, allowed []string) (bool, error) {
	tag, err := s.q().Exec(ctx, `UPDATE deeds_deed SET status = 'submitting', progress = 0 WHERE id = $1 AND status = ANY($2)`, id, allowed)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// DeedListItem is a row of the My Deeds list.
type DeedListItem struct {
	Deed          models.Deed
	PresenterName string
	PresenterDate *time.Time
	OwnerName     string
}

// DeedListFilter narrows the My Deeds list.
type DeedListFilter struct {
	Q        string
	Status   string
	From, To *time.Time
	TestOnly bool
	Limit    int
	Offset   int
}

// ListDeeds returns the viewer's non-test deeds (newest first) and the total count.
func (s *Store) ListDeeds(ctx context.Context, v Viewer, f DeedListFilter) ([]DeedListItem, int, error) {
	clause, arg := v.clause("d", 1)
	where := []string{clause, fmt.Sprintf("d.is_test = %v", f.TestOnly)}
	args := []any{arg}
	add := func(cond string, val any) {
		args = append(args, val)
		where = append(where, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(args))))
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		args = append(args, "%"+q+"%")
		n := len(args)
		cond := fmt.Sprintf("(d.token_number ILIKE $%d OR p.name ILIKE $%d OR d.title ILIKE $%d", n, n, n)
		// "Deed #12" / "12" also finds deed 12 (shown that way when it has no token yet).
		num := strings.TrimLeft(strings.TrimSpace(strings.ReplaceAll(strings.TrimLeft(q, "#"), "Deed", "")), "#")
		if isAllDigits(num) {
			args = append(args, num)
			cond += fmt.Sprintf(" OR d.id = $%d::bigint", len(args))
		}
		where = append(where, cond+")")
	}
	if f.Status != "" {
		add("d.status = ?", f.Status)
	}
	if f.From != nil {
		add("p.presenter_date >= ?", *f.From)
	}
	if f.To != nil {
		add("p.presenter_date <= ?", *f.To)
	}
	base := ` FROM deeds_deed d LEFT JOIN deeds_presenter p ON p.deed_id = d.id JOIN auth_user u ON u.id = d.owner_id WHERE ` + strings.Join(where, " AND ")
	var total int
	if err := s.q().QueryRow(ctx, `SELECT count(*)`+base, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, f.Limit, f.Offset)
	rows, err := s.q().Query(ctx, `SELECT `+db.Columns[models.Deed]("d")+`, COALESCE(p.name, ''), p.presenter_date,
		COALESCE(NULLIF(trim(u.first_name || ' ' || u.last_name), ''), u.username)`+base+
		fmt.Sprintf(` ORDER BY d.created_at DESC, d.id DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []DeedListItem
	for rows.Next() {
		var it DeedListItem
		ptrs := append(db.FieldPtrs(&it.Deed), &it.PresenterName, &it.PresenterDate, &it.OwnerName)
		if err := rows.Scan(ptrs...); err != nil {
			return nil, 0, err
		}
		out = append(out, it)
	}
	return out, total, rows.Err()
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) < 18
}

// RecentTestDeeds lists the viewer's latest test deeds.
func (s *Store) RecentTestDeeds(ctx context.Context, v Viewer, limit int) ([]models.Deed, error) {
	clause, arg := v.clause("", 1)
	return db.All[models.Deed](ctx, s.q(), `SELECT `+deedCols+` FROM deeds_deed WHERE is_test AND `+clause+
		` ORDER BY created_at DESC LIMIT $2`, arg, limit)
}

// DeedByTokenNumber finds a non-test deed of the viewer holding a token.
func (s *Store) DeedByTokenNumber(ctx context.Context, v Viewer, token string) (*models.Deed, error) {
	clause, arg := v.clause("", 2)
	d, err := db.One[models.Deed](ctx, s.q(), `SELECT `+deedCols+` FROM deeds_deed WHERE token_number = $1 AND NOT is_test AND `+clause+` LIMIT 1`, token, arg)
	if err == db.ErrNotFound {
		return nil, nil
	}
	return d, err
}

// MarkStaleProcessing fails a deed whose PDF processing was cut off (see the
// reaper in the web layer); it reports whether it changed the deed.
func (s *Store) MarkStaleProcessing(ctx context.Context, id int64, message string) (bool, error) {
	tag, err := s.q().Exec(ctx, `UPDATE deeds_deed SET status = 'failed', result_message = $2, updated_at = now()
		WHERE id = $1 AND status IN ('extracting', 'structuring')`, id, message)
	return err == nil && tag.RowsAffected() > 0, err
}

// MarkStaleSubmission fails a deed whose submission was cut off.
func (s *Store) MarkStaleSubmission(ctx context.Context, id int64, message string) (bool, error) {
	tag, err := s.q().Exec(ctx, `UPDATE deeds_deed SET status = 'failed', result_message = $2, updated_at = now()
		WHERE id = $1 AND status = 'submitting'`, id, message)
	return err == nil && tag.RowsAffected() > 0, err
}

// ResetForReprocess puts a failed deed back to extracting for another run.
func (s *Store) ResetForReprocess(ctx context.Context, id int64) error {
	_, err := s.q().Exec(ctx, `UPDATE deeds_deed SET status = 'extracting', progress = 0, result_message = '', updated_at = now() WHERE id = $1`, id)
	return err
}

// TestDeedsOf lists a user's seeded test deeds, oldest first.
func (s *Store) TestDeedsOf(ctx context.Context, username string) ([]models.Deed, error) {
	return db.All[models.Deed](ctx, s.q(), `SELECT `+deedCols+` FROM deeds_deed
		WHERE owner_id = (SELECT id FROM auth_user WHERE username = $1) AND is_test AND title LIKE 'Test deed%' ORDER BY id`, username)
}

// DeedHoldingToken is the most recently updated deed that holds a portal token.
func (s *Store) DeedHoldingToken(ctx context.Context, token string) (*models.Deed, error) {
	d, err := db.One[models.Deed](ctx, s.q(), `SELECT `+deedCols+` FROM deeds_deed WHERE token_number = $1
		ORDER BY updated_at DESC LIMIT 1`, token)
	if errors.Is(err, db.ErrNotFound) {
		return nil, nil
	}
	return d, err
}

// DeleteScreenshots removes a deed's stored screenshot rows.
func (s *Store) DeleteScreenshots(ctx context.Context, deedID int64) error {
	_, err := s.q().Exec(ctx, `DELETE FROM deeds_deedscreenshot WHERE deed_id = $1`, deedID)
	return err
}

// RecentScreenshots lists a deed's newest screenshots first.
func (s *Store) RecentScreenshots(ctx context.Context, deedID int64, limit int) ([]models.DeedScreenshot, error) {
	return db.All[models.DeedScreenshot](ctx, s.q(), `SELECT `+shotCols+` FROM deeds_deedscreenshot
		WHERE deed_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2`, deedID, limit)
}

// StartSubmission puts a deed in the "submitting" state without touching its heartbeat.
func (s *Store) StartSubmission(ctx context.Context, id int64) error {
	_, err := s.q().Exec(ctx, `UPDATE deeds_deed SET status = 'submitting', progress = 0 WHERE id = $1`, id)
	return err
}

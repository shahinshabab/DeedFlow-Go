package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/db"
	"github.com/shahinshabab/deedflow-go/internal/models"
)

var filingCols = db.Columns[models.FilingSheet]("")

// FilingSheetListItem is a row of the Filing sheets list (the columns it shows).
type FilingSheetListItem struct {
	PublicID  string
	Name      string
	CreatedAt time.Time
	Pages     int32
}

// CreateFilingSheet stores a new filing sheet for its owner's organization.
func (s *Store) CreateFilingSheet(ctx context.Context, f *models.FilingSheet) error {
	if f.PublicID == "" {
		f.PublicID = NewUUID()
	}
	if f.OrganizationID == nil {
		m, err := s.MembershipOf(ctx, f.OwnerID)
		if err != nil {
			return err
		}
		if m != nil {
			id := m.OrganizationID
			f.OrganizationID = &id
		}
	}
	if f.Settings == nil {
		f.Settings = json.RawMessage(`{}`)
	}
	f.CreatedAt, f.UpdatedAt = now(), now()
	_, err := db.Insert(ctx, s.q(), "filereader_filingsheet", f)
	return err
}

// FilingSheetByPublicID is a sheet the viewer may see, or nil.
func (s *Store) FilingSheetByPublicID(ctx context.Context, v Viewer, publicID string) (*models.FilingSheet, error) {
	clause, arg := v.clause("", 2)
	f, err := db.One[models.FilingSheet](ctx, s.q(),
		fmt.Sprintf(`SELECT %s FROM filereader_filingsheet WHERE public_id = $1 AND %s`, filingCols, clause), publicID, arg)
	if errors.Is(err, db.ErrNotFound) {
		return nil, nil
	}
	return f, err
}

// ListFilingSheets pages through the sheets the viewer may see, newest first,
// optionally only those whose name contains q.
func (s *Store) ListFilingSheets(ctx context.Context, v Viewer, q string, limit, offset int) ([]FilingSheetListItem, int, error) {
	clause, arg := v.clause("", 1)
	where := clause
	args := []any{arg}
	if q != "" {
		args = append(args, likePattern(q))
		where += fmt.Sprintf(` AND name ILIKE $%d ESCAPE '\'`, len(args))
	}
	var total int
	if err := s.q().QueryRow(ctx, `SELECT count(*) FROM filereader_filingsheet WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	rows, err := s.q().Query(ctx, fmt.Sprintf(`SELECT public_id, name, created_at, pages FROM filereader_filingsheet
		WHERE %s ORDER BY created_at DESC, id DESC LIMIT $%d OFFSET $%d`, where, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []FilingSheetListItem
	for rows.Next() {
		var it FilingSheetListItem
		if err := rows.Scan(&it.PublicID, &it.Name, &it.CreatedAt, &it.Pages); err != nil {
			return nil, 0, err
		}
		out = append(out, it)
	}
	return out, total, rows.Err()
}

// SaveFilingSheet stores the corrected text and settings, and returns the new updated_at.
func (s *Store) SaveFilingSheet(ctx context.Context, id int64, content string, settings json.RawMessage, pages int32) (time.Time, error) {
	at := now().Truncate(time.Microsecond)
	_, err := s.q().Exec(ctx, `UPDATE filereader_filingsheet SET content = $1, settings = $2, pages = $3, updated_at = $4 WHERE id = $5`,
		content, settings, pages, at, id)
	return at, err
}

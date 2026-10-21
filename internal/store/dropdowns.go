package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/shahinshabab/deedflow-go/internal/models"
)

// DropdownFirst returns the first stored row (by id) of a portal dropdown, or nil.
func (s *Store) DropdownFirst(ctx context.Context, page, field string) (*models.PortalDropdown, error) {
	var d models.PortalDropdown
	err := s.q().QueryRow(ctx, `SELECT id, page, field, label, parents, parent_key, options, scraped_at FROM deeds_portaldropdown
		WHERE page = $1 AND field = $2 ORDER BY id LIMIT 1`, page, field).
		Scan(&d.ID, &d.Page, &d.Field, &d.Label, &d.Parents, &d.ParentKey, &d.Options, &d.ScrapedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return &d, err
}

// DropdownOptionsByKey returns the options stored for an exact parent key.
func (s *Store) DropdownOptionsByKey(ctx context.Context, page, field, key string) (json.RawMessage, bool, error) {
	var opts json.RawMessage
	err := s.q().QueryRow(ctx, `SELECT options FROM deeds_portaldropdown WHERE page = $1 AND field = $2 AND parent_key = $3 ORDER BY id LIMIT 1`,
		page, field, key).Scan(&opts)
	if err == pgx.ErrNoRows {
		return nil, false, nil
	}
	return opts, err == nil, err
}

// DropdownOptionsByPattern returns the options of the first row whose parent
// key matches a (POSIX) regular expression.
func (s *Store) DropdownOptionsByPattern(ctx context.Context, page, field, pattern string) (json.RawMessage, bool, error) {
	var opts json.RawMessage
	err := s.q().QueryRow(ctx, `SELECT options FROM deeds_portaldropdown WHERE page = $1 AND field = $2 AND parent_key ~ $3 ORDER BY id LIMIT 1`,
		page, field, pattern).Scan(&opts)
	if err == pgx.ErrNoRows {
		return nil, false, nil
	}
	return opts, err == nil, err
}

// ReplacePortalDropdowns swaps the stored rows of a page for new ones.
func (s *Store) ReplacePortalDropdowns(ctx context.Context, page string, rows []models.PortalDropdown) error {
	return s.InTx(ctx, func(tx *Store) error {
		if _, err := tx.q().Exec(ctx, `DELETE FROM deeds_portaldropdown WHERE page = $1`, page); err != nil {
			return err
		}
		batch := &pgx.Batch{}
		for _, r := range rows {
			batch.Queue(`INSERT INTO deeds_portaldropdown (page, field, label, parents, parent_key, options, scraped_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`, page, r.Field, r.Label, r.Parents, r.ParentKey, r.Options, r.ScrapedAt)
		}
		res := tx.q().(interface {
			SendBatch(context.Context, *pgx.Batch) pgx.BatchResults
		}).SendBatch(ctx, batch)
		defer res.Close()
		for range rows {
			if _, err := res.Exec(); err != nil {
				return err
			}
		}
		return nil
	})
}

var _ = time.Now

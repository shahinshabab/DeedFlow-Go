// Package store is the application's SQL: one method per question the
// handlers and workers ask of the database.
package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/shahinshabab/deedflow-go/internal/db"
)

// Store gives access to the tables.
type Store struct {
	DB *db.DB
	tx pgx.Tx
}

// New wraps a database.
func New(d *db.DB) *Store { return &Store{DB: d} }

func (s *Store) q() db.Querier {
	if s.tx != nil {
		return s.tx
	}
	return s.DB.Pool
}

// InTx runs fn with a Store bound to one transaction; it commits if fn returns nil.
func (s *Store) InTx(ctx context.Context, fn func(*Store) error) error {
	if s.tx != nil {
		return fn(s)
	}
	return s.DB.Tx(ctx, func(tx pgx.Tx) error { return fn(&Store{DB: s.DB, tx: tx}) })
}

func now() time.Time { return time.Now() }

// ctxTimeout is the default budget of a single store call from a request.
func ctxTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 30*time.Second)
}

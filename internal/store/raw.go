package store

import "github.com/shahinshabab/deedflow-go/internal/db"

// Querier gives the admin site's generic queries direct access to the
// database (or to the transaction this Store is bound to).
func (s *Store) Querier() db.Querier { return s.q() }

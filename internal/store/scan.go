package store

import (
	"reflect"

	"github.com/shahinshabab/deedflow-go/internal/db"
)

// structPtrs returns pointers to v's `db`-tagged fields, in column order, for
// scanning a joined row into several structs.
func structPtrs(v any) []any {
	return db.FieldPtrs(v)
}

var _ = reflect.TypeOf

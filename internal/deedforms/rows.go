package deedforms

import (
	"github.com/shahinshabab/deedflow-go/internal/models"
	"github.com/shahinshabab/deedflow-go/internal/store"
)

func tableFor(section string) string { return store.RowTable[section] }

func newRowFor(section string) func() any {
	switch section {
	case "documents":
		return func() any { return &models.PriorDocument{} }
	case "executants", "claimants":
		return func() any { return &models.Party{} }
	case "schedule":
		return func() any { return &models.ScheduleItem{} }
	case "witnesses":
		return func() any { return &models.Witness{PortalWitnessType: "WB"} }
	}
	panic("unknown section " + section)
}

// Rows converts a typed slice into the []any a formset takes.
func Rows[T any](rows []T) []any {
	out := make([]any, len(rows))
	for i := range rows {
		out[i] = &rows[i]
	}
	return out
}

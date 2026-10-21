package db

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
)

// The store code is hand-written SQL; these helpers only remove the
// column-list boilerplate. A struct field maps to a column through its `db`
// tag; fields tagged `db:"-"` or without a tag are ignored.

type colInfo struct {
	name    string
	index   int
	decimal bool // numeric columns are read as text so numeric(10,4) keeps "1.5000"
}

var colCache sync.Map // reflect.Type -> []colInfo

func columnsOf(t reflect.Type) []colInfo {
	if c, ok := colCache.Load(t); ok {
		return c.([]colInfo)
	}
	var cols []colInfo
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("db")
		if tag == "" || tag == "-" {
			continue
		}
		ft := t.Field(i).Type
		for ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}
		cols = append(cols, colInfo{name: tag, index: i, decimal: ft == reflect.TypeOf(Decimal{})})
	}
	colCache.Store(t, cols)
	return cols
}

func structType(v any) reflect.Type {
	t := reflect.TypeOf(v)
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t
}

// Quote quotes a SQL identifier.
func Quote(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

// Columns is the quoted, comma-separated column list of T, optionally
// prefixed with a table alias ("d" -> d."id", d."title", ...).
func Columns[T any](alias string) string {
	var zero T
	cols := columnsOf(structType(zero))
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = Quote(c.name)
		if alias != "" {
			parts[i] = alias + "." + parts[i]
		}
		if c.decimal {
			parts[i] += "::text"
		}
	}
	return strings.Join(parts, ", ")
}

// One runs a query expected to return one row and scans it into a T.
// It returns ErrNotFound when there is none.
func One[T any](ctx context.Context, q Querier, sql string, args ...any) (*T, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	v, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByNameLax[T])
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return v, nil
}

// All runs a query and scans every row into a T.
func All[T any](ctx context.Context, q Querier, sql string, args ...any) ([]T, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByNameLax[T])
}

// Insert writes v's tagged fields (except the "id" column) into table and
// returns the new id; the id is also stored into v if it has an `id` field.
func Insert(ctx context.Context, q Querier, table string, v any) (int64, error) {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Ptr {
		return 0, fmt.Errorf("db.Insert needs a pointer")
	}
	rv = rv.Elem()
	var names, marks []string
	var args []any
	idIndex := -1
	for _, c := range columnsOf(rv.Type()) {
		if c.name == "id" {
			idIndex = c.index
			continue
		}
		names = append(names, Quote(c.name))
		args = append(args, rv.Field(c.index).Interface())
		marks = append(marks, fmt.Sprintf("$%d", len(args)))
	}
	sql := fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s) RETURNING id`, Quote(table), strings.Join(names, ", "), strings.Join(marks, ", "))
	var id int64
	if err := q.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
		return 0, err
	}
	if idIndex >= 0 {
		f := rv.Field(idIndex)
		if f.CanSet() {
			f.SetInt(id)
		}
	}
	return id, nil
}

// Update writes the named columns of v (all tagged columns except "id" if
// none are named) to the row with v's id.
func Update(ctx context.Context, q Querier, table string, v any, only ...string) error {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	want := map[string]bool{}
	for _, n := range only {
		want[n] = true
	}
	var sets []string
	var args []any
	var id any
	for _, c := range columnsOf(rv.Type()) {
		if c.name == "id" {
			id = rv.Field(c.index).Interface()
			continue
		}
		if len(want) > 0 && !want[c.name] {
			continue
		}
		args = append(args, rv.Field(c.index).Interface())
		sets = append(sets, fmt.Sprintf("%s = $%d", Quote(c.name), len(args)))
	}
	if id == nil || len(sets) == 0 {
		return fmt.Errorf("db.Update: nothing to update on %s", table)
	}
	args = append(args, id)
	sql := fmt.Sprintf(`UPDATE %s SET %s WHERE id = $%d`, Quote(table), strings.Join(sets, ", "), len(args))
	_, err := q.Exec(ctx, sql, args...)
	return err
}

// FieldPtrs returns pointers to the `db`-tagged fields of the struct v points
// to, in the order Columns lists them.
func FieldPtrs(v any) []any {
	rv := reflect.ValueOf(v).Elem()
	cols := columnsOf(rv.Type())
	out := make([]any, len(cols))
	for i, c := range cols {
		out[i] = rv.Field(c.index).Addr().Interface()
	}
	return out
}

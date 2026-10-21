package forms

import (
	"fmt"
	"reflect"
	"strconv"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/db"
)

// Model rows are structs whose `db` tags name the columns; a form field is
// bound to the struct field whose column is the field's name (ModelForm).

func fieldByColumn(v reflect.Value, col string) (reflect.Value, bool) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		if t.Field(i).Tag.Get("db") == col {
			return v.Field(i), true
		}
	}
	return reflect.Value{}, false
}

func rowValue(row any) reflect.Value {
	v := reflect.ValueOf(row)
	for v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	return v
}

// FormatValue renders a struct field the way a form's initial value reads.
func FormatValue(v reflect.Value) string {
	for v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return ""
		}
		v = v.Elem()
	}
	switch x := v.Interface().(type) {
	case string:
		return x
	case bool:
		if x {
			return "on"
		}
		return ""
	case time.Time:
		return x.Format("2006-01-02")
	case db.Decimal:
		return x.String()
	}
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(v.Uint(), 10)
	}
	return fmt.Sprint(v.Interface())
}

// InitialFromRow fills the form's initial values from a model row.
func (f *Form) InitialFromRow(row any) {
	rv := rowValue(row)
	for _, fl := range f.Fields {
		if sf, ok := fieldByColumn(rv, fl.Name); ok {
			f.Initial[fl.Name] = FormatValue(sf)
		}
	}
}

// ApplyToRow is construct_instance: it copies the cleaned values onto the row.
func (f *Form) ApplyToRow(row any) error {
	rv := rowValue(row)
	for _, fl := range f.Fields {
		sf, ok := fieldByColumn(rv, fl.Name)
		if !ok {
			continue
		}
		val, present := f.Cleaned[fl.Name]
		if !present {
			continue
		}
		if err := setField(sf, val); err != nil {
			return fmt.Errorf("%s: %w", fl.Name, err)
		}
	}
	return nil
}

func setField(sf reflect.Value, val any) error {
	switch x := val.(type) {
	case string:
		if sf.Kind() == reflect.String {
			sf.SetString(x)
			return nil
		}
		// A choice field holding a number (Registration.book).
		if n, err := strconv.ParseInt(x, 10, 64); err == nil && isInt(sf.Kind()) {
			sf.SetInt(n)
			return nil
		}
		if x == "" && isInt(sf.Kind()) {
			return nil
		}
	case bool:
		if sf.Kind() == reflect.Bool {
			sf.SetBool(x)
			return nil
		}
	case *int64:
		return setInt(sf, x)
	case *db.Decimal:
		if sf.Kind() == reflect.Ptr && sf.Type().Elem() == reflect.TypeOf(db.Decimal{}) {
			if x == nil {
				sf.Set(reflect.Zero(sf.Type()))
			} else {
				d := *x
				sf.Set(reflect.ValueOf(&d))
			}
			return nil
		}
	case *time.Time:
		if sf.Kind() == reflect.Ptr && sf.Type().Elem() == reflect.TypeOf(time.Time{}) {
			if x == nil {
				sf.Set(reflect.Zero(sf.Type()))
			} else {
				t := *x
				sf.Set(reflect.ValueOf(&t))
			}
			return nil
		}
	}
	return fmt.Errorf("cannot store %T in %s", val, sf.Type())
}

func isInt(k reflect.Kind) bool {
	return k >= reflect.Int && k <= reflect.Int64
}

func setInt(sf reflect.Value, x *int64) error {
	if sf.Kind() == reflect.Ptr {
		if x == nil {
			sf.Set(reflect.Zero(sf.Type()))
			return nil
		}
		nv := reflect.New(sf.Type().Elem())
		if !isInt(nv.Elem().Kind()) {
			return fmt.Errorf("not an integer field")
		}
		nv.Elem().SetInt(*x)
		sf.Set(nv)
		return nil
	}
	if isInt(sf.Kind()) {
		if x == nil {
			sf.SetInt(0) // a NOT NULL column with a default keeps it
			return nil
		}
		sf.SetInt(*x)
		return nil
	}
	return fmt.Errorf("not an integer field")
}

// PKOf reads a row's ID field.
func PKOf(row any) int64 {
	rv := rowValue(row)
	if sf, ok := fieldByColumn(rv, "id"); ok {
		return sf.Int()
	}
	return 0
}

func setColumn(row any, col string, v any) {
	rv := rowValue(row)
	if sf, ok := fieldByColumn(rv, col); ok {
		switch x := v.(type) {
		case int64:
			if isInt(sf.Kind()) {
				sf.SetInt(x)
			}
		case int32:
			if isInt(sf.Kind()) {
				sf.SetInt(int64(x))
			}
		}
	}
}

// ColumnInt reads an integer column of a row.
func ColumnInt(row any, col string) int64 {
	rv := rowValue(row)
	if sf, ok := fieldByColumn(rv, col); ok && isInt(sf.Kind()) {
		return sf.Int()
	}
	return 0
}

// SetColumnInt writes an integer column of a row.
func SetColumnInt(row any, col string, n int64) { setColumn(row, col, n) }

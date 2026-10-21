package db

import (
	"database/sql/driver"
	"fmt"
	"math/big"
	"strings"
)

// Decimal is an exact decimal number (Postgres numeric), the stand-in for
// Python's Decimal. The text it was created from is kept so a value read from
// numeric(10,4) is shown as "1.5200", like Django does.
type Decimal struct {
	rat  *big.Rat
	text string
}

// ParseDecimal reads a decimal such as "12", "1.52" or "-0.5".
func ParseDecimal(s string) (Decimal, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Decimal{}, fmt.Errorf("empty decimal")
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok || strings.Contains(s, "/") {
		return Decimal{}, fmt.Errorf("invalid decimal %q", s)
	}
	return Decimal{rat: r, text: s}, nil
}

// MustDecimal is ParseDecimal for constants.
func MustDecimal(s string) Decimal {
	d, err := ParseDecimal(s)
	if err != nil {
		panic(err)
	}
	return d
}

// DecimalFromInt makes a Decimal from an integer.
func DecimalFromInt(n int64) Decimal {
	return Decimal{rat: new(big.Rat).SetInt64(n), text: fmt.Sprint(n)}
}

// Scan implements sql.Scanner (pgx hands numeric over as text).
func (d *Decimal) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*d = Decimal{}
		return nil
	case string:
		x, err := ParseDecimal(v)
		*d = x
		return err
	case []byte:
		x, err := ParseDecimal(string(v))
		*d = x
		return err
	case int64:
		*d = DecimalFromInt(v)
		return nil
	case float64:
		x, err := ParseDecimal(fmt.Sprint(v))
		*d = x
		return err
	}
	return fmt.Errorf("cannot scan %T into Decimal", src)
}

// Value implements driver.Valuer.
func (d Decimal) Value() (driver.Value, error) {
	if d.rat == nil {
		return nil, nil
	}
	return d.String(), nil
}

// IsZero reports whether the Decimal is unset or equal to 0.
func (d Decimal) IsZero() bool { return d.rat == nil || d.rat.Sign() == 0 }

// Valid reports whether the Decimal holds a value.
func (d Decimal) Valid() bool { return d.rat != nil }

// Rat returns the exact value (0 when unset).
func (d Decimal) Rat() *big.Rat {
	if d.rat == nil {
		return new(big.Rat)
	}
	return new(big.Rat).Set(d.rat)
}

// String is the original text, or a plain decimal rendering.
func (d Decimal) String() string {
	if d.text != "" {
		return d.text
	}
	if d.rat == nil {
		return ""
	}
	return d.rat.FloatString(4)
}

// Fixed renders the value with exactly places decimals (rounding half up).
func (d Decimal) Fixed(places int) string {
	return d.Rat().FloatString(places)
}

// Float64 is the nearest float.
func (d Decimal) Float64() float64 {
	f, _ := d.Rat().Float64()
	return f
}

// Add returns d + o.
func (d Decimal) Add(o Decimal) Decimal {
	r := new(big.Rat).Add(d.Rat(), o.Rat())
	return Decimal{rat: r}
}

// Mul returns d * o.
func (d Decimal) Mul(o Decimal) Decimal {
	r := new(big.Rat).Mul(d.Rat(), o.Rat())
	return Decimal{rat: r}
}

// Cmp compares d and o.
func (d Decimal) Cmp(o Decimal) int { return d.Rat().Cmp(o.Rat()) }

// Ceil returns the smallest integer >= d.
func (d Decimal) Ceil() int64 {
	r := d.Rat()
	q := new(big.Int).Div(r.Num(), r.Denom()) // floor for positive denominators
	if new(big.Rat).SetInt(q).Cmp(r) < 0 {
		q.Add(q, big.NewInt(1))
	}
	return q.Int64()
}

package web

import (
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Django form-field cleaning for the admin's forms: the parsing, the
// validators and their error messages.

const (
	msgRequired = "This field is required."
)

// pyStrip is Python's str.strip(): it removes Unicode whitespace.
func pyStrip(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return unicode.IsSpace(r) || r == 0x1c || r == 0x1d || r == 0x1e || r == 0x1f })
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }

// parseInt is Python's int(str) after IntegerField strips a trailing ".0".
var reDecimalSuffix = regexp.MustCompile(`\.0*\s*$`)

func pyIntBig(s string) (*big.Int, bool) {
	s = pyStrip(s)
	if s == "" {
		return nil, false
	}
	neg := false
	switch s[0] {
	case '+':
		s = s[1:]
	case '-':
		neg, s = true, s[1:]
	}
	if s == "" || s[0] == '_' || s[len(s)-1] == '_' || strings.Contains(s, "__") {
		return nil, false
	}
	s = strings.ReplaceAll(s, "_", "")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case unicode.IsDigit(r):
			// other scripts' decimal digits
			b.WriteRune('0' + digitValue(r))
		default:
			return nil, false
		}
	}
	n, ok := new(big.Int).SetString(b.String(), 10)
	if !ok {
		return nil, false
	}
	if neg {
		n.Neg(n)
	}
	return n, true
}

// digitValue is the numeric value of a Unicode decimal digit.
func digitValue(r rune) rune {
	// Unicode decimal digits come in runs of ten starting at a multiple of 16
	// or at the zero of their script; walk back to the run's start.
	z := r
	for unicode.IsDigit(z - 1) {
		z--
		if r-z >= 10 {
			break
		}
	}
	return (r - z) % 10
}

// cleanInteger is IntegerField.to_python plus its min/max validators.
func cleanInteger(raw string, min, max *int64) (*int64, string) {
	s := pyStrip(raw)
	if s == "" {
		return nil, ""
	}
	s = reDecimalSuffix.ReplaceAllString(s, "")
	n, ok := pyIntBig(s)
	if !ok {
		return nil, "Enter a whole number."
	}
	if !n.IsInt64() {
		// outside every column: report the bound it breaks
		if max != nil && n.Sign() > 0 {
			return nil, fmt.Sprintf("Ensure this value is less than or equal to %d.", *max)
		}
		if min != nil && n.Sign() < 0 {
			return nil, fmt.Sprintf("Ensure this value is greater than or equal to %d.", *min)
		}
		return nil, "Enter a whole number."
	}
	v := n.Int64()
	if max != nil && v > *max {
		return nil, fmt.Sprintf("Ensure this value is less than or equal to %d.", *max)
	}
	if min != nil && v < *min {
		return nil, fmt.Sprintf("Ensure this value is greater than or equal to %d.", *min)
	}
	return &v, ""
}

// --- decimals ---------------------------------------------------------------------------------

// pyDecimal is a parsed decimal.Decimal: digits and exponent as as_tuple() gives them.
type pyDecimal struct {
	neg    bool
	digits []int
	exp    int
}

var reDecimal = regexp.MustCompile(`^([+-]?)(\d[\d_]*(?:\.[\d_]*)?|\.\d[\d_]*)(?:[eE]([+-]?\d+))?$`)

// parseDecimal is Python's Decimal(str): ok is false for what raises
// InvalidOperation; finite is false for NaN and infinities.
func parseDecimal(s string) (d pyDecimal, finite bool, ok bool) {
	s = pyStrip(s)
	low := strings.ToLower(strings.TrimLeft(s, "+-"))
	switch low {
	case "inf", "infinity", "nan", "snan":
		return pyDecimal{}, false, true
	}
	m := reDecimal.FindStringSubmatch(s)
	if m == nil {
		return d, false, false
	}
	if strings.Contains(m[2], "__") || strings.HasSuffix(m[2], "_") || strings.Contains(m[2], "_.") || strings.Contains(m[2], "._") || strings.HasPrefix(m[2], "_") {
		return d, false, false
	}
	num := strings.ReplaceAll(m[2], "_", "")
	whole, frac, _ := strings.Cut(num, ".")
	d.neg = m[1] == "-"
	all := whole + frac
	all = strings.TrimLeft(all, "0")
	d.exp = -len(frac)
	if m[3] != "" {
		e, err := strconv.Atoi(m[3])
		if err != nil {
			return d, false, false
		}
		d.exp += e
	}
	if all == "" {
		d.digits = []int{0}
	} else {
		for _, c := range all {
			d.digits = append(d.digits, int(c-'0'))
		}
	}
	return d, true, true
}

func (d pyDecimal) plain() string {
	var b strings.Builder
	if d.neg {
		b.WriteByte('-')
	}
	ds := make([]byte, len(d.digits))
	for i, v := range d.digits {
		ds[i] = byte('0' + v)
	}
	s := string(ds)
	switch {
	case d.exp >= 0:
		if s != "0" {
			s += strings.Repeat("0", d.exp)
		}
		b.WriteString(s)
	case -d.exp >= len(s):
		b.WriteString("0." + strings.Repeat("0", -d.exp-len(s)) + s)
	default:
		b.WriteString(s[:len(s)+d.exp] + "." + s[len(s)+d.exp:])
	}
	return b.String()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// cleanDecimal is DecimalField: to_python, the min/max checks and DecimalValidator.
func cleanDecimal(raw string, maxDigits, places *int) (string, bool, string) {
	s := pyStrip(raw)
	if s == "" {
		return "", false, ""
	}
	d, finite, ok := parseDecimal(s)
	if !ok || !finite {
		return "", false, "Enter a number."
	}
	var digits, decimals int
	if d.exp >= 0 {
		digits = len(d.digits)
		if !(len(d.digits) == 1 && d.digits[0] == 0) {
			digits += d.exp
		}
		decimals = 0
	} else {
		if -d.exp > len(d.digits) {
			digits, decimals = -d.exp, -d.exp
		} else {
			digits, decimals = len(d.digits), -d.exp
		}
	}
	whole := digits - decimals
	if maxDigits != nil && digits > *maxDigits {
		return "", false, fmt.Sprintf("Ensure that there are no more than %d %s in total.", *maxDigits, plural(*maxDigits, "digit", "digits"))
	}
	if places != nil && decimals > *places {
		return "", false, fmt.Sprintf("Ensure that there are no more than %d %s.", *places, plural(*places, "decimal place", "decimal places"))
	}
	if maxDigits != nil && places != nil && whole > *maxDigits-*places {
		n := *maxDigits - *places
		return "", false, fmt.Sprintf("Ensure that there are no more than %d %s before the decimal point.", n, plural(n, "digit", "digits"))
	}
	return d.plain(), true, ""
}

// decimalEqual compares two decimal strings numerically.
func decimalEqual(a, b string) bool {
	x, ok1 := new(big.Rat).SetString(a)
	y, ok2 := new(big.Rat).SetString(b)
	return ok1 && ok2 && x.Cmp(y) == 0
}

// --- dates and times -----------------------------------------------------------------------

var dateFormats = []string{"2006-01-02", "01/02/2006", "01/02/06"}

func parseDateInput(s string) (time.Time, bool) {
	s = pyStrip(s)
	for _, layout := range dateFormats {
		if t, err := time.Parse(layout, s); err == nil {
			return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), true
		}
	}
	return time.Time{}, false
}

var timeFormats = []string{"15:04:05", "15:04:05.999999", "15:04"}

func parseTimeInput(s string) (time.Duration, bool) {
	s = pyStrip(s)
	for _, layout := range timeFormats {
		if t, err := time.Parse(layout, s); err == nil {
			return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute +
				time.Duration(t.Second())*time.Second + time.Duration(t.Nanosecond()), true
		}
	}
	return 0, false
}

// --- text -------------------------------------------------------------------------------------

func cleanChar(raw string, strip bool, minLen, maxLen *int) (string, string) {
	s := raw
	if strip {
		s = pyStrip(s)
	}
	if s == "" {
		return "", ""
	}
	if minLen != nil && runeLen(s) < *minLen {
		return s, fmt.Sprintf("Ensure this value has at least %d %s (it has %d).", *minLen, plural(*minLen, "character", "characters"), runeLen(s))
	}
	if maxLen != nil && runeLen(s) > *maxLen {
		return s, fmt.Sprintf("Ensure this value has at most %d %s (it has %d).", *maxLen, plural(*maxLen, "character", "characters"), runeLen(s))
	}
	if strings.ContainsRune(s, 0) {
		return s, "Null characters are not allowed."
	}
	return s, ""
}

var reUsername = regexp.MustCompile(`^[\p{L}\p{N}\p{M}_.@+\-]+$`)

func isUsername(s string) bool { return reUsername.MatchString(s) }

func nfkc(s string) string { return norm.NFKC.String(s) }

// --- email ----------------------------------------------------------------------------------------

var (
	reEmailUser = regexp.MustCompile("(?i)^([-!#$%&'*+/=?^_`{}|~0-9A-Z]+(\\.[-!#$%&'*+/=?^_`{}|~0-9A-Z]+)*|\"([\\x01-\\x08\\x0b\\x0c\\x0e-\\x1f!#-\\[\\]-\\x7f]|\\\\[\\x01-\\x09\\x0b\\x0c\\x0e-\\x7f])*\")$")
	reEmailDom  = regexp.MustCompile(`(?i)^((?:[A-Z0-9](?:[A-Z0-9-]{0,61}[A-Z0-9])?\.)+)([A-Z0-9-]{2,63})$`)
	reEmailLit  = regexp.MustCompile(`^\[(.*)\]$`)
)

func validEmailDomain(d string) bool {
	if m := reEmailDom.FindStringSubmatch(d); m != nil && !strings.HasSuffix(m[2], "-") {
		return true
	}
	return false
}

// validEmail is django.core.validators.EmailValidator.
func validEmail(v string) bool {
	if v == "" || !strings.Contains(v, "@") {
		return false
	}
	i := strings.LastIndex(v, "@")
	user, domain := v[:i], v[i+1:]
	if !reEmailUser.MatchString(user) {
		return false
	}
	if domain == "localhost" {
		return true
	}
	if validEmailDomain(domain) {
		return true
	}
	if m := reEmailLit.FindStringSubmatch(domain); m != nil {
		return validIP(m[1])
	}
	// internationalized domain names are checked in their ASCII form
	if ascii, err := idnaASCII(domain); err == nil && ascii != domain {
		return validEmailDomain(ascii)
	}
	return false
}

func validIP(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) == 4 {
		for _, p := range parts {
			n, err := strconv.Atoi(p)
			if err != nil || n < 0 || n > 255 || p == "" {
				return false
			}
		}
		return true
	}
	return strings.Contains(s, ":") && !strings.ContainsAny(s, " ")
}

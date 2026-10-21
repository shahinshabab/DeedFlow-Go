package web

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"
)

// djangoDate formats t with a Django `date` filter format string.
func djangoDate(t time.Time, format string) string {
	var b strings.Builder
	escape := false
	for _, r := range format {
		if escape {
			b.WriteRune(r)
			escape = false
			continue
		}
		switch r {
		case '\\':
			escape = true
		case 'd':
			fmt.Fprintf(&b, "%02d", t.Day())
		case 'j':
			b.WriteString(strconv.Itoa(t.Day()))
		case 'D':
			b.WriteString(t.Weekday().String()[:3])
		case 'l':
			b.WriteString(t.Weekday().String())
		case 'w':
			b.WriteString(strconv.Itoa(int(t.Weekday())))
		case 'm':
			fmt.Fprintf(&b, "%02d", int(t.Month()))
		case 'n':
			b.WriteString(strconv.Itoa(int(t.Month())))
		case 'M':
			b.WriteString(t.Month().String()[:3])
		case 'b':
			b.WriteString(strings.ToLower(t.Month().String()[:3]))
		case 'F':
			b.WriteString(t.Month().String())
		case 'y':
			fmt.Fprintf(&b, "%02d", t.Year()%100)
		case 'Y':
			fmt.Fprintf(&b, "%04d", t.Year())
		case 'H':
			fmt.Fprintf(&b, "%02d", t.Hour())
		case 'G':
			b.WriteString(strconv.Itoa(t.Hour()))
		case 'h':
			fmt.Fprintf(&b, "%02d", hour12(t))
		case 'g':
			b.WriteString(strconv.Itoa(hour12(t)))
		case 'i':
			fmt.Fprintf(&b, "%02d", t.Minute())
		case 's':
			fmt.Fprintf(&b, "%02d", t.Second())
		case 'A':
			b.WriteString(map[bool]string{true: "PM", false: "AM"}[t.Hour() >= 12])
		case 'a':
			b.WriteString(map[bool]string{true: "p.m.", false: "a.m."}[t.Hour() >= 12])
		case 'P':
			switch {
			case t.Hour() == 0 && t.Minute() == 0:
				b.WriteString("midnight")
			case t.Hour() == 12 && t.Minute() == 0:
				b.WriteString("noon")
			default:
				if t.Minute() == 0 {
					fmt.Fprintf(&b, "%d %s", hour12(t), map[bool]string{true: "p.m.", false: "a.m."}[t.Hour() >= 12])
				} else {
					fmt.Fprintf(&b, "%d:%02d %s", hour12(t), t.Minute(), map[bool]string{true: "p.m.", false: "a.m."}[t.Hour() >= 12])
				}
			}
		case 'N':
			months := []string{"Jan.", "Feb.", "March", "April", "May", "June", "July", "Aug.", "Sept.", "Oct.", "Nov.", "Dec."}
			b.WriteString(months[t.Month()-1])
		case 'U':
			b.WriteString(strconv.FormatInt(t.Unix(), 10))
		case 'e', 'T':
			name, _ := t.Zone()
			b.WriteString(name)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func hour12(t time.Time) int {
	h := t.Hour() % 12
	if h == 0 {
		return 12
	}
	return h
}

// djangoFloatformat implements the floatformat filter for the arguments the
// templates use: "N", "-N" and a trailing "g" for thousands grouping.
func djangoFloatformat(value any, arg string) string {
	var r *big.Rat
	switch v := value.(type) {
	case nil:
		return ""
	case int:
		r = new(big.Rat).SetInt64(int64(v))
	case int32:
		r = new(big.Rat).SetInt64(int64(v))
	case int64:
		r = new(big.Rat).SetInt64(v)
	case float64:
		r = new(big.Rat).SetFloat64(v)
	case fmt.Stringer:
		r, _ = new(big.Rat).SetString(strings.TrimSpace(v.String()))
	case string:
		r, _ = new(big.Rat).SetString(strings.TrimSpace(v))
	}
	if r == nil {
		return ""
	}
	group := strings.HasSuffix(arg, "g")
	arg = strings.TrimSuffix(arg, "g")
	places := -1
	if arg != "" {
		if n, err := strconv.Atoi(arg); err == nil {
			places = n
		}
	}
	whole := r.IsInt()
	neg := places < 0
	if neg {
		places = -places
	}
	if places < 0 {
		places = 1
		neg = true
	}
	var s string
	if neg && whole {
		s = r.FloatString(0)
	} else {
		s = r.FloatString(places)
	}
	if group {
		s = groupThousands(s)
	}
	if s == "-0" || strings.HasPrefix(s, "-0.") && strings.Trim(s[1:], "0.") == "" {
		s = s[1:]
	}
	return s
}

func groupThousands(s string) string {
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign, s = "-", s[1:]
	}
	intPart, frac, hasFrac := strings.Cut(s, ".")
	var parts []string
	for len(intPart) > 3 {
		parts = append([]string{intPart[len(intPart)-3:]}, parts...)
		intPart = intPart[:len(intPart)-3]
	}
	parts = append([]string{intPart}, parts...)
	out := sign + strings.Join(parts, ",")
	if hasFrac {
		out += "." + frac
	}
	return out
}

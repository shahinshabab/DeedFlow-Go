package pmd

import (
	"encoding/base64"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Call is one librevenge drawing call as pmd2json records it: the method, its
// property list (for those that have one) and, for text, the hex of the text.
// String values hold the Latin-1 code point of every byte, like the JSON that
// pmd2json writes (\u00XX per non-ASCII byte).
type Call struct {
	M    string
	P    PropList
	HasP bool
	Hex  string
}

// PropList is a librevenge property list; iteration (and so JSON output) is in key order.
type PropList map[string]Prop

// Prop is a property value in its string form, or a vector of property lists.
type Prop struct {
	S     string
	Vec   []PropList
	IsVec bool
}

// fmtD is librevenge's double to string conversion.
func fmtD(v float64) string {
	switch {
	case math.IsNaN(v):
		if math.Signbit(v) {
			return "-nan"
		}
		return "nan"
	case math.IsInf(v, 1):
		return "inf"
	case math.IsInf(v, -1):
		return "-inf"
	case math.Abs(v) < 0.0001:
		return "0.0000"
	}
	return strconv.FormatFloat(v, 'f', 4, 64)
}

func (p PropList) str(k, v string)          { p[k] = Prop{S: v} }
func (p PropList) inch(k string, v float64) { p[k] = Prop{S: fmtD(v) + "in"} }
func (p PropList) pt(k string, v float64)   { p[k] = Prop{S: fmtD(v) + "pt"} }
func (p PropList) pct(k string, v float64)  { p[k] = Prop{S: fmtD(v*100) + "%"} }
func (p PropList) gen(k string, v float64)  { p[k] = Prop{S: fmtD(v)} }
func (p PropList) num(k string, v int)      { p[k] = Prop{S: strconv.Itoa(v)} }
func (p PropList) vec(k string, v []PropList) {
	if v == nil {
		v = []PropList{}
	}
	p[k] = Prop{Vec: v, IsVec: true}
}
func (p PropList) boolean(k string, v bool) {
	if v {
		p[k] = Prop{S: "true"}
	} else {
		p[k] = Prop{S: "false"}
	}
}
func (p PropList) binary(k string, v []byte) {
	p[k] = Prop{S: base64.StdEncoding.EncodeToString(v)}
}

// utf8Skip is glib's g_utf8_skip, which librevenge uses to step through strings.
func utf8Skip(c byte) int {
	switch {
	case c >= 0xfc && c <= 0xfd:
		return 6
	case c >= 0xf8 && c <= 0xfb:
		return 5
	case c >= 0xf0 && c <= 0xf7:
		return 4
	case c >= 0xe0 && c <= 0xef:
		return 3
	case c >= 0xc0 && c <= 0xdf:
		return 2
	}
	return 1
}

// rvngString is what an RVNGString built from a C string keeps of it: it
// walks the bytes by their UTF-8 lead lengths without validating them and
// drops everything from a lead byte that claims more bytes than are left. The
// kept bytes come back as their Latin-1 code points, as in pmd2json's JSON.
func rvngString(b string) string {
	var sb strings.Builder
	for i := 0; i < len(b); {
		n := utf8Skip(b[i])
		if i+n > len(b) {
			break
		}
		for j := i; j < i+n; j++ {
			sb.WriteRune(rune(b[j]))
		}
		i += n
	}
	return sb.String()
}

func jsonEscape(s string) string {
	var sb strings.Builder
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			sb.WriteByte('\\')
			sb.WriteRune(r)
		case r < 0x20 || r >= 0x80:
			fmt.Fprintf(&sb, "\\u%04x", r)
		default:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

func (p PropList) json(sb *strings.Builder) {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	sb.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(`"` + jsonEscape(k) + `":`)
		v := p[k]
		if v.IsVec {
			sb.WriteByte('[')
			for j, e := range v.Vec {
				if j > 0 {
					sb.WriteByte(',')
				}
				e.json(sb)
			}
			sb.WriteByte(']')
		} else {
			sb.WriteString(`"` + jsonEscape(v.S) + `"`)
		}
	}
	sb.WriteByte('}')
}

// JSON renders the calls exactly as pmd2json prints them.
func JSON(calls []Call) []byte {
	var sb strings.Builder
	sb.WriteString(`{"calls":[`)
	for i, c := range calls {
		if i == 0 {
			sb.WriteString("\n")
		} else {
			sb.WriteString(",\n")
		}
		sb.WriteString(`{"m":"` + c.M + `"`)
		if c.HasP {
			sb.WriteString(`,"p":`)
			c.P.json(&sb)
		}
		if c.M == "text" {
			sb.WriteString(`,"hex":"` + c.Hex + `"`)
		}
		sb.WriteString("}")
	}
	sb.WriteString("\n]}\n")
	return []byte(sb.String())
}

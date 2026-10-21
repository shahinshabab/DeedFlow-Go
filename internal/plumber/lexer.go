package plumber

import (
	"strconv"
)

type tokKind int

const (
	tEOF tokKind = iota
	tNum
	tStr
	tName
	tOp
	tArrOpen
	tArrClose
	tDictOpen
	tDictClose
)

type token struct {
	kind tokKind
	num  float64
	str  []byte // string content, or the name / operator text
}

func (t token) text() string { return string(t.str) }

// lexer tokenizes content streams and CMaps (PostScript-like syntax).
type lexer struct {
	data []byte
	pos  int
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == 0
}

func isDelim(c byte) bool {
	switch c {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

func (l *lexer) skipSpace() {
	for l.pos < len(l.data) {
		c := l.data[l.pos]
		switch {
		case isSpace(c):
			l.pos++
		case c == '%':
			for l.pos < len(l.data) && l.data[l.pos] != '\n' && l.data[l.pos] != '\r' {
				l.pos++
			}
		default:
			return
		}
	}
}

func (l *lexer) next() token {
	l.skipSpace()
	if l.pos >= len(l.data) {
		return token{kind: tEOF}
	}
	c := l.data[l.pos]
	switch c {
	case '[':
		l.pos++
		return token{kind: tArrOpen}
	case ']':
		l.pos++
		return token{kind: tArrClose}
	case '{', '}':
		l.pos++
		return token{kind: tOp, str: []byte{c}}
	case '(':
		return l.literalString()
	case '<':
		if l.pos+1 < len(l.data) && l.data[l.pos+1] == '<' {
			l.pos += 2
			return token{kind: tDictOpen}
		}
		return l.hexString()
	case '>':
		if l.pos+1 < len(l.data) && l.data[l.pos+1] == '>' {
			l.pos += 2
			return token{kind: tDictClose}
		}
		l.pos++
		return l.next()
	case ')':
		l.pos++
		return l.next()
	case '/':
		l.pos++
		start := l.pos
		for l.pos < len(l.data) && !isSpace(l.data[l.pos]) && !isDelim(l.data[l.pos]) {
			l.pos++
		}
		return token{kind: tName, str: unescapeName(l.data[start:l.pos])}
	}
	start := l.pos
	for l.pos < len(l.data) && !isSpace(l.data[l.pos]) && !isDelim(l.data[l.pos]) {
		l.pos++
	}
	word := l.data[start:l.pos]
	if f, ok := parseNumber(word); ok {
		return token{kind: tNum, num: f}
	}
	return token{kind: tOp, str: word}
}

func parseNumber(w []byte) (float64, bool) {
	if len(w) == 0 {
		return 0, false
	}
	c := w[0]
	if !(c == '+' || c == '-' || c == '.' || (c >= '0' && c <= '9')) {
		return 0, false
	}
	// pdfminer-style tolerance: "--5" reads as -5, "5-3" fails.
	s := string(w)
	for len(s) > 1 && (s[0] == '-' || s[0] == '+') && (s[1] == '-' || s[1] == '+') {
		s = s[1:]
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

func unescapeName(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		if b[i] == '#' && i+2 < len(b) {
			if v, err := strconv.ParseUint(string(b[i+1:i+3]), 16, 8); err == nil {
				out = append(out, byte(v))
				i += 2
				continue
			}
		}
		out = append(out, b[i])
	}
	return out
}

func (l *lexer) literalString() token {
	l.pos++ // (
	depth := 1
	var out []byte
	for l.pos < len(l.data) {
		c := l.data[l.pos]
		l.pos++
		switch c {
		case '\\':
			if l.pos >= len(l.data) {
				break
			}
			e := l.data[l.pos]
			l.pos++
			switch e {
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 't':
				out = append(out, '\t')
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, '\f')
			case '\r':
				if l.pos < len(l.data) && l.data[l.pos] == '\n' {
					l.pos++
				}
			case '\n':
			case '0', '1', '2', '3', '4', '5', '6', '7':
				v := int(e - '0')
				for k := 0; k < 2 && l.pos < len(l.data) && l.data[l.pos] >= '0' && l.data[l.pos] <= '7'; k++ {
					v = v*8 + int(l.data[l.pos]-'0')
					l.pos++
				}
				out = append(out, byte(v))
			default:
				out = append(out, e)
			}
		case '(':
			depth++
			out = append(out, c)
		case ')':
			depth--
			if depth == 0 {
				return token{kind: tStr, str: out}
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return token{kind: tStr, str: out}
}

func (l *lexer) hexString() token {
	l.pos++ // <
	var digits []byte
	for l.pos < len(l.data) {
		c := l.data[l.pos]
		l.pos++
		if c == '>' {
			break
		}
		switch {
		case c >= '0' && c <= '9':
			digits = append(digits, c-'0')
		case c >= 'a' && c <= 'f':
			digits = append(digits, c-'a'+10)
		case c >= 'A' && c <= 'F':
			digits = append(digits, c-'A'+10)
		}
	}
	if len(digits)%2 == 1 {
		digits = append(digits, 0)
	}
	out := make([]byte, len(digits)/2)
	for i := range out {
		out[i] = digits[2*i]<<4 | digits[2*i+1]
	}
	return token{kind: tStr, str: out}
}

// value reads one operand starting with tok (arrays and dictionaries are
// read whole): float64, []byte, name, []any, map[string]any.
type name string

func (l *lexer) value(tok token) any {
	switch tok.kind {
	case tNum:
		return tok.num
	case tStr:
		return tok.str
	case tName:
		return name(tok.str)
	case tArrOpen:
		arr := []any{}
		for {
			t := l.next()
			if t.kind == tArrClose || t.kind == tEOF {
				return arr
			}
			if t.kind == tOp {
				switch string(t.str) {
				case "true":
					arr = append(arr, true)
				case "false":
					arr = append(arr, false)
				case "null":
					arr = append(arr, nil)
				}
				continue
			}
			arr = append(arr, l.value(t))
		}
	case tDictOpen:
		m := map[string]any{}
		for {
			k := l.next()
			if k.kind == tDictClose || k.kind == tEOF {
				return m
			}
			if k.kind != tName {
				continue
			}
			v := l.next()
			if v.kind == tDictClose || v.kind == tEOF {
				return m
			}
			if v.kind == tOp {
				switch string(v.str) {
				case "true":
					m[string(k.str)] = true
				case "false":
					m[string(k.str)] = false
				}
				continue
			}
			m[string(k.str)] = l.value(v)
		}
	}
	return nil
}

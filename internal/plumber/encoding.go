// Package plumber extracts characters, lines, rectangles and tables from PDF
// pages the way pdfplumber (on pdfminer.six) does. The deed pipeline depends
// on pdfplumber's exact output - which characters, in which order, with which
// spaces - so this is a port of its algorithms, not an approximation.
package plumber

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// name2unicode converts an Adobe glyph name to text (pdfminer's name2unicode).
func name2unicode(name string) (string, bool) {
	if i := strings.Index(name, "."); i >= 0 {
		name = name[:i]
	}
	if parts := strings.Split(name, "_"); len(parts) > 1 {
		var b strings.Builder
		for _, p := range parts {
			t, ok := name2unicode(p)
			if !ok {
				return "", false
			}
			b.WriteString(t)
		}
		return b.String(), true
	}
	if t, ok := glyphNames[name]; ok {
		return t, true
	}
	switch {
	case strings.HasPrefix(name, "uni"):
		hex := strings.Trim(name, "uni") // Python's str.strip("uni") strips those characters
		if isHex(hex) && len(hex)%4 == 0 {
			var b strings.Builder
			for i := 0; i < len(hex); i += 4 {
				n, _ := strconv.ParseUint(hex[i:i+4], 16, 32)
				if n > 55295 && n < 57344 {
					return "", false
				}
				b.WriteRune(rune(n))
			}
			return b.String(), true
		}
	case strings.HasPrefix(name, "u"):
		hex := strings.Trim(name, "u")
		if isHex(hex) && len(hex) >= 4 && len(hex) <= 6 {
			n, _ := strconv.ParseUint(hex, 16, 32)
			if (n > 55295 && n < 57344) || n > utf8.MaxRune {
				return "", false
			}
			return string(rune(n)), true
		}
	}
	return "", false
}

func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// encodingTable builds a code -> text table from a base encoding and a
// Differences array (EncodingDB.get_encoding).
func encodingTable(base string, diff []diffItem) map[int]string {
	std, ok := stdEncodings[base]
	if !ok {
		std = stdEncodings["StandardEncoding"]
	}
	table := make(map[int]string, 256)
	for code, t := range std {
		if t != "" {
			table[code] = t
		}
	}
	cid := 0
	for _, d := range diff {
		if d.isNum {
			cid = d.num
			continue
		}
		if t, ok := name2unicode(d.name); ok {
			table[cid] = t
		}
		cid++
	}
	return table
}

type diffItem struct {
	isNum bool
	num   int
	name  string
}

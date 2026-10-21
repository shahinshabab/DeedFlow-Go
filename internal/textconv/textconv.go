// Package textconv converts the legacy 8-bit "ASCII" Malayalam font encoding
// used by the office's deed-writing software into real Unicode Malayalam. It
// is a line-for-line port of the Django project's TextConverter: the glyph map
// and vowel-sign reordering rules were tuned against real documents, so the
// behaviour (quirks included) is deliberately preserved.
package textconv

import (
	"strings"

	"golang.org/x/text/unicode/norm"
)

// Converter holds the maps (immutable once built; safe for concurrent use).
type Converter struct {
	fontMap      map[string]string
	reverse      map[string]string
	preBaseASCII map[string]bool
	postBaseASCI map[string]bool
}

var replacements = [][2]string{
	{"-v", "v"}, {" v", "v"}, {"-n", "n"}, {" n", "n"},
	{"-m", "m"}, {" m", "m"}, {"-o", "o"}, {" o", "o"},
	{"-p", "p"}, {" p", "p"}, {"-q", "q"}, {" q", "q"},
	{"-r", "r"}, {" r", "r"}, {"-u", "u"}, {" u", "u"},
	{"-y", "y"}, {" y", "y"}, {"-z", "z"}, {" z", "z"},
	{"s-", "s"}, {"s ", "s"}, {"t-", "t"}, {"t ", "t"},
	{"ss-", "ss"}, {"ss ", "ss"},
}

// New builds a converter.
func New() *Converter {
	c := &Converter{
		fontMap:      make(map[string]string, len(FontMap)),
		reverse:      make(map[string]string, len(FontMap)),
		preBaseASCII: map[string]bool{},
		postBaseASCI: map[string]bool{},
	}
	for _, p := range FontMap {
		c.fontMap[p.Key] = p.Value
		c.reverse[p.Value] = p.Key // the last key of a duplicated value wins, like the Python dict
	}
	for _, s := range preBaseSigns {
		if k, ok := c.reverse[s]; ok {
			c.preBaseASCII[k] = true
		}
	}
	for _, s := range postBaseSigns {
		if k, ok := c.reverse[s]; ok {
			c.postBaseASCI[k] = true
		}
	}
	return c
}

var defaultConverter = New()

// Default is a shared converter.
func Default() *Converter { return defaultConverter }

// FontMapGet looks up a glyph code (used by the filing sheet's reverse conversion).
func (c *Converter) FontMapGet(code string) (string, bool) {
	v, ok := c.fontMap[code]
	return v, ok
}

// Normalize applies the clean-up replacements.
func (c *Converter) Normalize(s string) string {
	for _, p := range NormalizerMap {
		s = strings.ReplaceAll(s, p.Key, p.Value)
	}
	return s
}

func (c *Converter) applyReplacements(s string) string {
	for _, p := range replacements {
		s = strings.ReplaceAll(s, p[0], p[1])
	}
	return s
}

func isSep(r rune) bool { return r == '-' || r == ' ' || r == '\n' }

// ConvertASCIIToUnicode converts legacy-font text to Unicode (NFC).
func (c *Converter) ConvertASCIIToUnicode(input string) string {
	if input == "" {
		return input
	}
	processed := c.applyReplacements(input)

	var out []rune
	var stack []string // pending pre-base glyphs
	flush := func() {
		for i := len(stack) - 1; i >= 0; i-- {
			out = append(out, []rune(stack[i])...)
		}
		stack = stack[:0]
	}
	for _, cur := range processed {
		s := string(cur)
		if len(stack) > 0 && isSep(cur) {
			continue
		}
		if len(stack) > 0 && cur == 'v' {
			out = append(out, cur)
			continue
		}
		switch {
		case c.preBaseASCII[s]:
			if len(stack) > 0 && stack[len(stack)-1] == "s" && cur == 's' {
				stack[len(stack)-1] = "ss"
			} else {
				stack = append(stack, s)
			}
		case c.postBaseASCI[s]:
			for len(out) > 0 && isSep(out[len(out)-1]) {
				out = out[:len(out)-1]
			}
			if len(out) > 0 && out[len(out)-1] == 'v' {
				before := out[:len(out)-1]
				if len(before) > 0 {
					last := before[len(before)-1]
					out = append(append(append([]rune{}, before[:len(before)-1]...), 'v'), last)
				}
			}
			out = append(out, cur)
			flush()
		default:
			if cur == 'v' && len(out) > 0 && out[len(out)-1] == '.' {
				out = append(out[:len(out)-1], 'v', '.')
			} else {
				out = append(out, cur)
			}
			flush()
		}
	}
	flush()

	var sb strings.Builder
	for _, r := range out {
		if mapped, ok := c.fontMap[string(r)]; ok {
			sb.WriteString(mapped)
		} else {
			sb.WriteRune(r)
		}
	}
	return norm.NFC.String(c.Normalize(sb.String()))
}

package filereader

import (
	"regexp"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/shahinshabab/deedflow-go/internal/extract"
	"github.com/shahinshabab/deedflow-go/internal/textconv"
)

// Decides which text runs are typed in a legacy "ASCII" Malayalam font
// (ML-TT and friends) and converts those to real Unicode Malayalam, with the
// same converter the deed extraction pipeline uses. Desktop-publishing files
// keep the font name next to every run of text, so the font name is usually
// enough; a font with an unfamiliar name only counts as legacy when its text
// also looks like legacy glyph codes.

var legacyName = regexp.MustCompile(`(?i)^(f?mlb?|ml-?tt)[-_ ]|ml-?tt`)

func isMalayalam(r rune) bool { return r >= 0x0d00 && r <= 0x0d7f }

func hasMalayalam(s string) bool {
	for _, r := range s {
		if isMalayalam(r) {
			return true
		}
	}
	return false
}

// pyIsSpace is Python's str.isspace for one character (and so its \s).
func pyIsSpace(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }

// IsLegacyFont decides whether a run in this font is legacy Malayalam.
func IsLegacyFont(fontName, sampleText string) bool {
	name := strings.TrimFunc(fontName, pyIsSpace)
	if name == "" || extract.IsStandardLatinFont(name) {
		return false
	}
	if legacyName.MatchString(name) {
		return true
	}
	// Unknown font: legacy only if its text has the legacy font's tell-tale
	// Latin-1 glyphs or conjunct symbols, which plain English never uses.
	for _, c := range sampleText {
		if c > 127 || strings.ContainsRune(legacySymbols, c) {
			return true
		}
	}
	return false
}

// ToUnicode turns legacy glyph codes into Unicode Malayalam. Text that
// already holds Unicode Malayalam is returned unchanged.
func ToUnicode(text string) string {
	if text == "" || hasMalayalam(text) {
		return text
	}
	return textconv.Default().ConvertASCIIToUnicode(text)
}

// --- Unicode back to the legacy font's codes ---------------------------------
//
// For showing (and printing) text in ML-TT Karthika itself, which only draws
// its own glyph codes. This is the converter run backwards: where the font
// has two codes for one letter, the one ML-TT Karthika files use is picked.

var (
	unitsOnce sync.Once
	units     map[string]string
)

func nfc(s string) string { return norm.NFC.String(s) }

func legacyUnits() map[string]string {
	unitsOnce.Do(func() {
		u := map[string]string{}
		conv := textconv.Default()
		for _, p := range textconv.FontMap {
			code := p.Key
			letter := nfc(conv.Normalize(p.Value))
			if len([]rune(code)) != 1 || letter == "" {
				continue
			}
			lr := []rune(letter)
			if !isMalayalam(lr[0]) {
				continue
			}
			first := string(lr[0])
			if _, ok := legacyPostSigns[first]; ok {
				continue
			}
			if _, ok := legacyPreSigns[first]; ok {
				continue
			}
			if _, ok := legacyOther[letter]; ok {
				continue
			}
			if len(lr) >= 2 {
				if two := string(lr[:2]); two == "്യ" || two == "്വ" || two == "്ര" {
					continue
				}
			} else if letter == "്" {
				// letter[:2] of a one-character string is the string itself, never a pair
			}
			if strings.Contains(legacyUnused, code) {
				continue
			}
			if _, ok := u[letter]; !ok {
				u[letter] = code
			}
		}
		for k, v := range legacyPreferred {
			u[k] = v
		}
		for k, v := range legacyExtra {
			u[k] = v
		}
		units = u
	})
	return units
}

func isConsonant(ch rune) bool { return (ch >= 0x0d15 && ch <= 0x0d39) || ch == 0x0d3a }

// letterAt is the longest letter or conjunct with a code of its own starting at i.
func letterAt(text []rune, i int) (string, int) {
	u := legacyUnits()
	for size := 5; size >= 1; size-- {
		if i+size > len(text) {
			continue
		}
		if code, ok := u[string(text[i:i+size])]; ok {
			return code, size
		}
	}
	return "", 0
}

type clusterLetter struct {
	code   string
	ra     bool
	post   string
	virama bool
}

func malayalamToLegacy(s string) string {
	text := []rune(s)
	n := len(text)
	var out strings.Builder
	i := 0
	for i < n {
		ch := text[i]
		if strings.ContainsRune(legacyJoiners, ch) {
			i++
			continue
		}
		code, size := letterAt(text, i)
		if code == "" || (!isConsonant(ch) && ch != 0x0d7b) {
			if code != "" {
				out.WriteString(code)
				i += size
			} else {
				c := string(ch)
				if v, ok := legacyOther[c]; ok {
					out.WriteString(v)
				} else if v, ok := legacyPostSigns[c]; ok {
					out.WriteString(v)
				} else if v, ok := legacyPreSigns[c]; ok {
					out.WriteString(v)
				} else {
					out.WriteString(c)
				}
				i++
			}
			continue
		}
		// A consonant cluster: letters joined by a visible virama, each
		// with its own ra, ya and va signs. A vowel sign written before
		// the letter goes before the cluster's last letter, as typed.
		var letters []*clusterLetter
		for {
			letter := &clusterLetter{code: code}
			letters = append(letters, letter)
			i += size
			for i+2 <= n && isSignPair(text[i], text[i+1]) {
				if text[i+1] == 0x0d30 {
					letter.ra = true
				} else if text[i+1] == 0x0d2f {
					letter.post += "y"
				} else {
					letter.post += "z"
				}
				i += 2
			}
			if i+1 < n && string(text[i]) == legacyVirama && isConsonant(text[i+1]) {
				c2, s2 := letterAt(text, i+1)
				if c2 != "" {
					code, size = c2, s2
					i++
					letters = append(letters, &clusterLetter{virama: true}) // the visible virama
					continue
				}
			}
			break
		}
		pre, post := "", ""
		if i < n {
			c := string(text[i])
			if v, ok := legacyPreSigns[c]; ok {
				pre = v
				i++
			} else if v, ok := legacySplitSigns[c]; ok {
				pre, post = v[0], v[1]
				i++
			} else if v, ok := legacyPostSigns[c]; ok {
				post = v
				i++
			}
		}
		for k, letter := range letters {
			if letter.virama {
				out.WriteString("v")
				continue
			}
			last := k == len(letters)-1
			if last {
				out.WriteString(pre)
			}
			if letter.ra {
				out.WriteString("{")
			}
			out.WriteString(letter.code + letter.post)
			if last {
				out.WriteString(post)
			}
		}
	}
	return out.String()
}

// isSignPair is whether the two characters are virama + ra, ya or va.
func isSignPair(a, b rune) bool {
	return a == 0x0d4d && (b == 0x0d30 || b == 0x0d2f || b == 0x0d35)
}

func safeInLegacy(ch rune) bool {
	legacyUnits()
	if pyIsSpace(ch) {
		return true
	}
	if _, ok := textconv.Default().FontMapGet(string(ch)); ok {
		return false
	}
	return ch != '-' && ch < 0x2000
}

// Piece is a run of text and whether it is in the legacy font's codes.
type Piece struct {
	Text   string
	Legacy bool
}

func isRunChar(r rune) bool { return isMalayalam(r) || r == 0x200c || r == 0x200d }

// FromUnicode turns Unicode text into pieces: Malayalam turned into the
// legacy font's codes, and Latin words (which that font would draw as
// Malayalam) kept as they are, to be set in a Latin font.
func FromUnicode(text string) []Piece {
	text = nfc(text)
	var pieces []Piece
	rs := []rune(text)
	for start := 0; start < len(rs); {
		mal := isRunChar(rs[start])
		end := start
		for end < len(rs) && isRunChar(rs[end]) == mal {
			end++
		}
		run := string(rs[start:end])
		start = end
		if mal {
			// _MALAYALAM.match(run) or run[0] in _JOINERS
			if len(pieces) > 0 && pieces[len(pieces)-1].Legacy {
				pieces[len(pieces)-1].Text += malayalamToLegacy(run)
			} else {
				pieces = append(pieces, Piece{malayalamToLegacy(run), true})
			}
			continue
		}
		rr := []rune(run)
		for ps := 0; ps < len(rr); {
			sp := pyIsSpace(rr[ps])
			pe := ps
			for pe < len(rr) && pyIsSpace(rr[pe]) == sp {
				pe++
			}
			part := string(rr[ps:pe])
			ps = pe
			legacyOK := true
			for _, c := range part {
				if !safeInLegacy(c) {
					legacyOK = false
					break
				}
			}
			if len(pieces) > 0 && (pieces[len(pieces)-1].Legacy == legacyOK || sp) {
				pieces[len(pieces)-1].Text += part
			} else {
				pieces = append(pieces, Piece{part, legacyOK})
			}
		}
	}
	return pieces
}

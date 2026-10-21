package plumber

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

const (
	defaultXTolerance = 3.0
	defaultYTolerance = 3.0
)

var ligatures = map[string]string{
	"ﬀ": "ff", "ﬃ": "ffi", "ﬄ": "ffl", "ﬁ": "fi", "ﬂ": "fl", "ﬆ": "st", "ﬅ": "st",
}

// pyIsSpace mirrors Python's str.isspace for a whole string.
func pyIsSpace(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !pyIsSpaceRune(r) {
			return false
		}
	}
	return true
}

func pyIsSpaceRune(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// pyStrip mirrors Python's str.strip().
func pyStrip(s string) string { return strings.TrimFunc(s, pyIsSpaceRune) }

// box is anything with a pdfplumber-style bbox.
type bbox struct{ x0, top, x1, bottom float64 }

func charBox(c *Char) bbox { return bbox{c.X0, c.Top, c.X1, c.Bottom} }

func mergeBoxes(bs []bbox) bbox {
	r := bs[0]
	for _, b := range bs[1:] {
		r.x0, r.top = math.Min(r.x0, b.x0), math.Min(r.top, b.top)
		r.x1, r.bottom = math.Max(r.x1, b.x1), math.Max(r.bottom, b.bottom)
	}
	return r
}

// clusterList is pdfplumber's cluster_list.
func clusterList(xs []float64, tol float64) [][]float64 {
	sorted := append([]float64(nil), xs...)
	sort.Float64s(sorted)
	if tol == 0 || len(sorted) < 2 {
		out := make([][]float64, len(sorted))
		for i, x := range sorted {
			out[i] = []float64{x}
		}
		return out
	}
	var groups [][]float64
	cur := []float64{sorted[0]}
	last := sorted[0]
	for _, x := range sorted[1:] {
		if x <= last+tol {
			cur = append(cur, x)
		} else {
			groups = append(groups, cur)
			cur = []float64{x}
		}
		last = x
	}
	return append(groups, cur)
}

// clusterIndex is cluster_objects over indices: it returns groups of indices
// into keys (stable, ordered by cluster unless preserveOrder).
func clusterIndex(keys []float64, tol float64, preserveOrder bool) [][]int {
	seen := map[float64]bool{}
	var uniq []float64
	for _, k := range keys {
		if !seen[k] {
			seen[k] = true
			uniq = append(uniq, k)
		}
	}
	cl := map[float64]int{}
	for i, grp := range clusterList(uniq, tol) {
		for _, v := range grp {
			cl[v] = i
		}
	}
	idx := make([]int, len(keys))
	for i := range idx {
		idx[i] = i
	}
	if !preserveOrder {
		sort.SliceStable(idx, func(a, b int) bool { return cl[keys[idx[a]]] < cl[keys[idx[b]]] })
	}
	var out [][]int
	for n, i := range idx {
		if n > 0 && cl[keys[idx[n-1]]] == cl[keys[i]] {
			out[len(out)-1] = append(out[len(out)-1], i)
		} else {
			out = append(out, []int{i})
		}
	}
	return out
}

// Word is a run of characters, as WordExtractor yields.
type Word struct {
	Text                string
	X0, X1, Top, Bottom float64
	Upright             bool
	Chars               []*Char
}

func (w *Word) box() bbox { return bbox{w.X0, w.Top, w.X1, w.Bottom} }

// wordExtractor with pdfplumber's defaults (ltr / ttb, x/y tolerance 3).
type wordExtractor struct {
	xTol, yTol float64
	// splitAt is the string of punctuation that splits words ("" by default).
	splitAt string
}

func defaultExtractor() wordExtractor {
	return wordExtractor{xTol: defaultXTolerance, yTol: defaultYTolerance}
}

func charBeginsNewWord(prev, cur *Char, xTol, yTol float64) bool {
	ay, cy := prev.Top, cur.Top
	ax, bx, cx := prev.X0, prev.X1, cur.X0
	return cx < ax || cx > bx+xTol || math.Abs(cy-ay) > yTol
}

func (e wordExtractor) charsToWords(ordered []*Char) [][]*Char {
	var words [][]*Char
	var cur []*Char
	flush := func() {
		if len(cur) > 0 {
			words = append(words, cur)
		}
		cur = nil
	}
	for _, c := range ordered {
		switch {
		case pyIsSpace(c.Text):
			flush()
		case strings.Contains(e.splitAt, c.Text):
			flush()
			cur = []*Char{c}
			flush()
		case len(cur) > 0 && charBeginsNewWord(cur[len(cur)-1], c, e.xTol, e.yTol):
			flush()
			cur = []*Char{c}
		default:
			cur = append(cur, c)
		}
	}
	flush()
	return words
}

func (e wordExtractor) mergeChars(chars []*Char) Word {
	bs := make([]bbox, len(chars))
	var sb strings.Builder
	for i, c := range chars {
		bs[i] = charBox(c)
		if x, ok := ligatures[c.Text]; ok {
			sb.WriteString(x)
		} else {
			sb.WriteString(c.Text)
		}
	}
	b := mergeBoxes(bs)
	return Word{Text: sb.String(), X0: b.x0, X1: b.x1, Top: b.top, Bottom: b.bottom, Upright: chars[0].Upright, Chars: chars}
}

// extractWords is WordExtractor.iter_extract_tuples for the default
// direction settings.
func (e wordExtractor) extractWords(chars []*Char) []Word {
	var out []Word
	for start := 0; start < len(chars); {
		end := start
		for end < len(chars) && chars[end].Upright == chars[start].Upright {
			end++
		}
		group := chars[start:end]
		start = end
		upright := group[0].Upright
		// Upright text: lines run top-to-bottom, chars left-to-right.
		// Rotated text swaps the roles (line_dir_rotated=ltr, char_dir_rotated=ttb).
		var keys []float64
		tol := e.yTol
		for _, c := range group {
			if upright {
				keys = append(keys, c.Top)
			} else {
				keys = append(keys, c.X0)
			}
		}
		if !upright {
			tol = e.xTol
		}
		for _, line := range clusterIndex(keys, tol, false) {
			lc := make([]*Char, len(line))
			for i, ix := range line {
				lc[i] = group[ix]
			}
			if upright {
				sort.SliceStable(lc, func(a, b int) bool { return lc[a].X0 < lc[b].X0 })
			} else {
				sort.SliceStable(lc, func(a, b int) bool {
					if lc[a].Top != lc[b].Top {
						return lc[a].Top < lc[b].Top
					}
					return lc[a].Bottom < lc[b].Bottom
				})
			}
			for _, w := range e.charsToWordsDir(lc, upright) {
				out = append(out, e.mergeChars(w))
			}
		}
	}
	return out
}

// charsToWordsDir handles rotated text (ttb chars) as well as upright.
func (e wordExtractor) charsToWordsDir(ordered []*Char, upright bool) [][]*Char {
	if upright {
		return e.charsToWords(ordered)
	}
	var words [][]*Char
	var cur []*Char
	flush := func() {
		if len(cur) > 0 {
			words = append(words, cur)
		}
		cur = nil
	}
	for _, c := range ordered {
		switch {
		case pyIsSpace(c.Text):
			flush()
		case strings.Contains(e.splitAt, c.Text):
			flush()
			cur = []*Char{c}
			flush()
		case len(cur) > 0:
			p := cur[len(cur)-1]
			// direction "ttb": x/y tolerances swap.
			if c.Top < p.Top || c.Top > p.Bottom+e.yTol || math.Abs(c.X0-p.X0) > e.xTol {
				flush()
				cur = []*Char{c}
			} else {
				cur = append(cur, c)
			}
		default:
			cur = append(cur, c)
		}
	}
	flush()
	return words
}

type tmEntry struct {
	r  rune
	ch *Char // nil for layout-implied whitespace
}

// TextMap maps each rune of the text to its source char.
type TextMap struct {
	entries []tmEntry
	String  string
}

func newTextMap(entries []tmEntry) *TextMap {
	var sb strings.Builder
	for _, e := range entries {
		sb.WriteRune(e.r)
	}
	return &TextMap{entries: entries, String: sb.String()}
}

// charsToTextmap is chars_to_textmap with presorted=True and layout=False
// (what Page.extract_text() does by default).
func charsToTextmap(chars []*Char) *TextMap {
	if len(chars) == 0 {
		return newTextMap(nil)
	}
	words := defaultExtractor().extractWords(chars)
	if len(words) == 0 {
		return newTextMap(nil)
	}
	keys := make([]float64, len(words))
	for i, w := range words {
		keys[i] = w.Top
	}
	var entries []tmEntry
	for i, line := range clusterIndex(keys, defaultYTolerance, true) {
		if i > 0 {
			entries = append(entries, tmEntry{'\n', nil})
		}
		lineLen := 0
		for _, wi := range line {
			w := words[wi]
			if lineLen > 0 {
				entries = append(entries, tmEntry{' ', nil})
				lineLen++
			}
			for _, c := range w.Chars {
				letters := c.Text
				if x, ok := ligatures[c.Text]; ok {
					letters = x
				}
				for _, r := range letters {
					entries = append(entries, tmEntry{r, c})
					lineLen++
				}
			}
		}
	}
	return newTextMap(entries)
}

// TextLine is one extract_text_lines result.
type TextLine struct {
	Text                string
	X0, Top, X1, Bottom float64
	Chars               []*Char
}

// ExtractTextLines is Page.extract_text_lines(strip=…, return_chars=True).
func (t *TextMap) ExtractTextLines(strip bool) []TextLine {
	var out []TextLine
	runes := make([]rune, len(t.entries))
	for i, e := range t.entries {
		runes[i] = e.r
	}
	pos := 0
	for pos <= len(runes) {
		end := pos
		for end < len(runes) && runes[end] != '\n' {
			end++
		}
		lo, hi := pos, end
		if strip {
			for lo < hi && runes[lo] == ' ' {
				lo++
			}
			for hi > lo && runes[hi-1] == ' ' {
				hi--
			}
		}
		if hi > lo {
			text := string(runes[lo:hi])
			if pyStrip(text) != "" {
				var chars []*Char
				var bs []bbox
				for _, e := range t.entries[lo:hi] {
					if e.ch != nil {
						chars = append(chars, e.ch)
						bs = append(bs, charBox(e.ch))
					}
				}
				tl := TextLine{Text: text, Chars: chars}
				if len(bs) > 0 {
					b := mergeBoxes(bs)
					tl.X0, tl.Top, tl.X1, tl.Bottom = b.x0, b.top, b.x1, b.bottom
				}
				out = append(out, tl)
			}
		}
		pos = end + 1
	}
	return out
}

func (p *Page) charPtrs() []*Char {
	out := make([]*Char, len(p.Chars))
	for i := range p.Chars {
		out[i] = &p.Chars[i]
	}
	return out
}

// ExtractText is Page.extract_text() with default settings.
func (p *Page) ExtractText() string { return charsToTextmap(p.charPtrs()).String }

// ExtractTextLines is Page.extract_text_lines(strip=…).
func (p *Page) ExtractTextLines(strip bool) []TextLine {
	return charsToTextmap(p.charPtrs()).ExtractTextLines(strip)
}

// ExtractWords is Page.extract_words() with default settings.
func (p *Page) ExtractWords() []Word { return defaultExtractor().extractWords(p.charPtrs()) }

// extractTextPlain is utils.extract_text for chars of a table cell
// (layout=False): words, clustered into lines by top, joined by space/newline.
func extractTextPlain(chars []*Char) string {
	if len(chars) == 0 {
		return ""
	}
	words := defaultExtractor().extractWords(chars)
	keys := make([]float64, len(words))
	for i, w := range words {
		keys[i] = w.Top
	}
	var lines []string
	for _, line := range clusterIndex(keys, defaultYTolerance, false) {
		parts := make([]string, len(line))
		for i, wi := range line {
			parts[i] = words[wi].Text
		}
		lines = append(lines, strings.Join(parts, " "))
	}
	return strings.Join(lines, "\n")
}

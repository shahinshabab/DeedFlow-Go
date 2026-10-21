package filereader

import (
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Turns a read document into a filing sheet: the deed's text in reading order
// as one continuous run, with its tables, for writing out on ruled pages and
// correcting on the Filing sheet page.
//
// `Size` (points) and `Gap` (line spacing, times the size) are the body
// text's, and `Margins` (points: top, right, bottom, left) start from the
// main text frames' sides.
//
// What changes on the way:
//   - Spaces and tabs that only pushed text into place become one space, and
//     everything outside a table runs on as one paragraph, without underlines
//     or changes of size.
//   - A story threaded through several frames comes out whole, where its first
//     frame is.
//   - Page numbers, blank frames, and a short line repeated on page after page
//     (a signature mark) after its first time, are left out.
//   - Tables drawn with lines (see tables.go) become tables, and a label set on
//     its side down a column becomes an ordinary full-width row above the rows
//     it ran beside.

var spacesRe = regexp.MustCompile("[ \t ​]+")

// pageNumber is re.match(r"^\W*\d{1,3}\W*$", text) on Unicode text.
func pageNumber(text string) bool {
	rs := []rune(text)
	i := 0
	for i < len(rs) && !isWordChar(rs[i]) {
		i++
	}
	digits := 0
	for i < len(rs) && unicode.IsDigit(rs[i]) {
		i++
		digits++
	}
	if digits < 1 || digits > 3 {
		return false
	}
	for ; i < len(rs); i++ {
		if isWordChar(rs[i]) {
			return false
		}
	}
	return true
}

func isWordChar(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r) }

func isAlnum(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }

// FSpan is a run of text in a filing sheet.
type FSpan struct {
	T      string `json:"t"`
	Bold   bool   `json:"bold"`
	Italic bool   `json:"italic"`

	underline bool
	size      float64
}

// FPara is a paragraph of a filing sheet.
type FPara struct {
	Kind   string  `json:"kind"`
	Align  string  `json:"align"`
	Indent bool    `json:"indent"`
	Spans  []FSpan `json:"spans"`
}

// FCell is a cell of a filing sheet's table.
type FCell struct {
	Blocks  []*FPara `json:"blocks"`
	Col     int      `json:"col"`
	Colspan int      `json:"colspan"`
	Rowspan int      `json:"rowspan"`
	Align   string   `json:"align"`
}

// FTable is a table of a filing sheet.
type FTable struct {
	Kind string    `json:"kind"`
	Cols []float64 `json:"cols"`
	Rows [][]FCell `json:"rows"`
}

// FBlock is a paragraph or a table.
type FBlock struct {
	Para  *FPara
	Table *FTable
}

// MarshalJSON writes whichever one it holds.
func (b FBlock) MarshalJSON() ([]byte, error) {
	if b.Table != nil {
		return json.Marshal(b.Table)
	}
	return json.Marshal(b.Para)
}

// Sheet is a filing sheet.
type Sheet struct {
	Name    string     `json:"name"`
	Format  string     `json:"format"`
	Pages   int        `json:"pages"`
	Size    float64    `json:"size"`
	Gap     float64    `json:"gap"`
	Margins [4]float64 `json:"margins"`
	Blocks  []FBlock   `json:"blocks"`
}

// cleanSpans squeezes the layout spacing out of spans; nil when only spaces.
func cleanSpans(spans []Span) []FSpan {
	var out []FSpan
	for _, s := range spans {
		t := spacesRe.ReplaceAllString(strings.ReplaceAll(strings.ReplaceAll(s.T, PageNumberMark, ""), "\n", " "), " ")
		if t == "" {
			continue
		}
		if len(out) > 0 && strings.HasPrefix(t, " ") && strings.HasSuffix(out[len(out)-1].T, " ") {
			t = t[1:]
		}
		if t != "" {
			out = append(out, FSpan{T: t, Bold: s.Bold, Italic: s.Italic, underline: s.Underline, size: s.Size})
		}
	}
	blank := func(s FSpan) bool { return strings.TrimFunc(s.T, pyIsSpace) == "" }
	for len(out) > 0 && blank(out[0]) {
		out = out[1:]
	}
	for len(out) > 0 && blank(out[len(out)-1]) {
		out = out[:len(out)-1]
	}
	if len(out) > 0 {
		out[0].T = strings.TrimLeftFunc(out[0].T, pyIsSpace)
		out[len(out)-1].T = strings.TrimRightFunc(out[len(out)-1].T, pyIsSpace)
	}
	return out
}

func paragraph(b Block, align string) *FPara {
	spans := cleanSpans(b.Spans)
	any := false
	for _, s := range spans {
		for _, c := range s.T {
			if isAlnum(c) {
				any = true
			}
		}
	}
	if !any {
		return nil
	}
	var rawB strings.Builder
	for _, s := range b.Spans {
		rawB.WriteString(s.T)
	}
	raw := rawB.String()
	// Text pushed along with spaces is centred or indented by hand, so a
	// left-aligned line led by a long run of spaces was meant to be centred.
	lead := utf8.RuneCountInString(raw) - utf8.RuneCountInString(strings.TrimLeft(raw, " \t"))
	if align == "" {
		align = b.Align
	}
	if align == "" {
		align = "left"
	}
	indent := b.Indent > 1 || strings.HasPrefix(raw, "\t") || (3 <= lead && lead < 12)
	if align == "left" && lead >= 12 {
		align, indent = "center", false
	}
	return &FPara{Kind: "p", Align: align, Indent: indent && (align == "left" || align == "justify"), Spans: spans}
}

type fblock struct {
	para  *FPara
	table *FTable
}

func paragraphs(blocks []Block) []fblock {
	var out []fblock
	for _, b := range blocks {
		switch b.Kind {
		case "p":
			if p := paragraph(b, ""); p != nil {
				out = append(out, fblock{para: p})
			}
		case "table":
			out = append(out, fblock{table: storyTable(b)})
		}
	}
	return out
}

func onlyParas(blocks []fblock) []*FPara {
	var out []*FPara
	for _, b := range blocks {
		if b.para != nil {
			out = append(out, b.para)
		}
	}
	return out
}

func sideways(rotate float64) bool {
	return int(math.RoundToEven(math.Abs(rotate)/90))%2 == 1
}

// tcell is a cell while a table is being put together.
type tcell struct {
	blocks  []*FPara
	side    []*FPara
	colspan int
	rowspan int
	col     int
}

// storyTable is a table that was a real table in the file (IDML).
func storyTable(b Block) *FTable {
	ncols := len(b.Cols)
	if ncols == 0 {
		ncols = 1
		first := true
		for _, r := range b.Rows {
			sum := 0
			for _, c := range r.Cells {
				cs := c.Colspan
				if cs == 0 {
					cs = 1
				}
				sum += cs
			}
			if first || sum > ncols {
				ncols = sum
				first = false
			}
		}
	}
	var rows [][]*tcell
	for _, r := range b.Rows {
		var row []*tcell
		for _, c := range r.Cells {
			blocks := onlyParas(paragraphs(c.Blocks))
			side := sideways(c.Rotate)
			cs, rs := c.Colspan, c.Rowspan
			if cs == 0 {
				cs = 1
			}
			if rs == 0 {
				rs = 1
			}
			tc := &tcell{colspan: cs, rowspan: rs}
			if side {
				tc.side = blocks
			} else {
				tc.blocks = blocks
			}
			row = append(row, tc)
		}
		rows = append(rows, row)
	}
	placeColumns(rows)
	widths := b.Cols
	if len(widths) == 0 {
		widths = make([]float64, ncols)
		for i := range widths {
			widths[i] = 1
		}
	}
	return finishTable(unturn(rows, ncols), widths)
}

// placeColumns works out each cell's first column from the row and column spans.
func placeColumns(rows [][]*tcell) {
	taken := map[[2]int]bool{}
	for r, row := range rows {
		c := 0
		for _, cell := range row {
			for taken[[2]int{r, c}] {
				c++
			}
			cell.col = c
			for dr := 0; dr < cell.rowspan; dr++ {
				for dc := 0; dc < cell.colspan; dc++ {
					taken[[2]int{r + dr, c + dc}] = true
				}
			}
			c += cell.colspan
		}
	}
}

// lineTable is a table found from ruled lines on a page.
func lineTable(t *Table, stories *Stories) *FTable {
	rows := make([][]*tcell, len(t.Rows))
	for _, cell := range t.Cells {
		var blocks, side []*FPara
		for _, item := range cell.Items {
			paras := onlyParas(paragraphs(stories.Get(item.Story)))
			if sideways(item.Rotate) {
				side = append(side, paras...)
			} else {
				blocks = append(blocks, paras...)
			}
		}
		rows[cell.Row] = append(rows[cell.Row], &tcell{blocks: blocks, side: side, colspan: cell.Colspan,
			rowspan: cell.Rowspan, col: cell.Col})
	}
	return finishTable(unturn(rows, len(t.Cols)), t.Cols)
}

func removeCell(row []*tcell, c *tcell) []*tcell {
	for i, x := range row {
		if x == c {
			return append(row[:i:i], row[i+1:]...)
		}
	}
	return row
}

var whitespaceRe = regexp.MustCompile(`[\t-\r \x1c-\x1f\x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}]+`)

// unturn: text set on its side in a cell becomes a row of its own, above the
// rows the cell ran down beside, reaching from the cell's column to the
// right-hand edge. Cells to its left that reach the new row (a row number)
// grow to cover it. A cell left with no other text goes, and the cells to its
// right on each row it covered widen to fill its place.
func unturn(rows [][]*tcell, ncols int) [][]*tcell {
	r := 0
	for r < len(rows) {
		var turned *tcell
		for _, c := range rows[r] {
			if len(c.side) > 0 {
				turned = c
				break
			}
		}
		if turned == nil {
			r++
			continue
		}
		col, side := turned.col, turned.side
		turned.side = nil
		if len(turned.blocks) == 0 {
			rows[r] = removeCell(rows[r], turned)
			for dr := 0; dr < turned.rowspan; dr++ {
				if r+dr >= len(rows) {
					break
				}
				var nxt *tcell
				for _, c := range rows[r+dr] {
					if c.col == col+turned.colspan {
						nxt = c
						break
					}
				}
				if nxt != nil {
					nxt.col = col
					nxt.colspan += turned.colspan
				} else {
					rows[r+dr] = append(rows[r+dr], &tcell{colspan: turned.colspan, rowspan: 1, col: col})
				}
			}
		}
		// Cells from rows above that run past this one grow by the new row;
		// the new row stops short of any to its right.
		var newRow []*tcell
		end := ncols
		for rr := 0; rr < r; rr++ {
			for _, c := range rows[rr] {
				if rr+c.rowspan > r {
					c.rowspan++
					if c.col >= col {
						end = min(end, c.col)
					}
				}
			}
		}
		for _, c := range append([]*tcell(nil), rows[r]...) {
			if c.col < col {
				c.rowspan++
				rows[r] = removeCell(rows[r], c)
				newRow = append(newRow, c)
			}
		}
		var texts []string
		for _, b := range side {
			var sb strings.Builder
			for _, s := range b.Spans {
				sb.WriteString(s.T)
			}
			texts = append(texts, sb.String())
		}
		text := strings.TrimFunc(whitespaceRe.ReplaceAllString(strings.Join(texts, " "), " "), pyIsSpace)
		first := side[0].Spans[0]
		first.T = text
		newRow = append(newRow, &tcell{
			blocks:  []*FPara{{Kind: "p", Align: "left", Indent: false, Spans: []FSpan{first}}},
			colspan: max(end-col, 1), rowspan: 1, col: col})
		rows = append(rows, nil)
		copy(rows[r+1:], rows[r:])
		rows[r] = newRow
		r++
	}
	return rows
}

func finishTable(rows [][]*tcell, widths []float64) *FTable {
	total := 0.0
	for _, w := range widths {
		total += w
	}
	if total == 0 {
		total = 1
	}
	out := make([][]FCell, 0, len(rows))
	for _, row := range rows {
		sorted := append([]*tcell(nil), row...)
		sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].col < sorted[j].col })
		cells := []FCell{}
		for _, c := range sorted {
			align := "left"
			if len(c.blocks) > 0 {
				align = c.blocks[0].Align
			}
			for _, b := range c.blocks {
				b.Indent = false
				if b.Align == "justify" {
					b.Align = "left"
				}
			}
			if align == "justify" {
				align = "left"
			}
			blocks := c.blocks
			if blocks == nil {
				blocks = []*FPara{}
			}
			cells = append(cells, FCell{Blocks: blocks, Col: c.col, Colspan: c.colspan, Rowspan: c.rowspan, Align: align})
		}
		out = append(out, cells)
	}
	cols := make([]float64, len(widths))
	for i, w := range widths {
		cols[i] = pyRound(w/total*100, 2)
	}
	return &FTable{Kind: "table", Cols: cols, Rows: out}
}

func blocksText(blocks []fblock) string {
	var parts []string
	for _, b := range blocks {
		if b.para != nil {
			var sb strings.Builder
			for _, s := range b.para.Spans {
				sb.WriteString(s.T)
			}
			parts = append(parts, sb.String())
		}
	}
	return strings.TrimFunc(strings.Join(parts, " "), pyIsSpace)
}

func medianOf(values []float64, def float64) float64 {
	if len(values) == 0 {
		return def
	}
	v := append([]float64(nil), values...)
	sort.Float64s(v)
	return v[len(v)/2]
}

// counter is collections.Counter with most_common(1): the first of the
// highest counts in insertion order.
type counter struct {
	keys []float64
	n    map[float64]int
}

func newCounter() *counter { return &counter{n: map[float64]int{}} }

func (c *counter) add(k float64, n int) {
	if _, ok := c.n[k]; !ok {
		c.keys = append(c.keys, k)
	}
	c.n[k] += n
}

func (c *counter) mostCommon() (float64, bool) {
	best, bestN, ok := 0.0, 0, false
	for _, k := range c.keys {
		if !ok || c.n[k] > bestN {
			best, bestN, ok = k, c.n[k], true
		}
	}
	return best, ok
}

type placedEntry struct {
	y, x   float64
	blocks []fblock
}

// BuildFiling makes the filing sheet of a read document.
func BuildFiling(doc *Doc) *Sheet {
	stories := doc.Stories
	use := StoryFrameCounts(doc)

	// The body text's type size and line spacing are the ones most of the
	// text is set in; the frames of the long stories give the margins.
	sizes, gaps := newCounter(), newCounter()
	longStories := map[string]bool{}
	for _, sid := range stories.Keys() {
		chars := 0
		for _, b := range stories.Get(sid) {
			if b.Kind != "p" {
				continue
			}
			for _, s := range b.Spans {
				n := utf8.RuneCountInString(strings.TrimFunc(s.T, pyIsSpace))
				chars += n
				if n > 0 && s.Size != 0 {
					sizes.add(s.Size, n)
					if b.Leading != nil && *b.Leading != 0 {
						gaps.add(pyRound(*b.Leading/s.Size, 2), n)
					}
				}
			}
		}
		if chars >= 200 {
			longStories[sid] = true
		}
	}
	size, ok := sizes.mostCommon()
	if !ok {
		size = 12.0
	}
	// Legacy fonts draw small for their size, so their tight spacing would
	// crowd the Unicode font the sheet starts in.
	gap := 1.5
	if g, ok := gaps.mostCommon(); ok {
		gap = g
	}
	gap = math.Min(math.Max(gap, 1.5), 3.0)

	var rights, lefts []float64
	for _, p := range doc.Pages {
		for _, i := range p.Items {
			if i.Type == "text" && longStories[i.Story] {
				rights = append(rights, p.Width-i.X-i.W)
				lefts = append(lefts, i.X)
			}
		}
	}
	// Top and bottom are an inch: a deed's first pages start low, below
	// the stamp, which a filing sheet doesn't need.
	margins := [4]float64{72, medianOf(rights, 72), 72, medianOf(lefts, 72)}
	for i, m := range margins {
		margins[i] = pyRound(math.Min(math.Max(m, 0), 200), 1)
	}

	var entries [][]fblock // blocks in reading order
	done := map[string]bool{}
	for _, page := range doc.Pages {
		found := FindTables(page, stories, use)
		skip := map[*Item]bool{}
		for _, t := range found {
			for _, i := range t.Texts {
				skip[i] = true
			}
		}
		var placed []placedEntry
		for _, t := range found {
			placed = append(placed, placedEntry{t.Y, t.X, []fblock{{table: lineTable(t, stories)}}})
		}
		for _, item := range page.Items {
			if item.Type != "text" || skip[item] {
				continue
			}
			sid := item.Story
			if done[sid] {
				continue
			}
			if !item.Repeat {
				done[sid] = true
			}
			blocks := paragraphs(stories.Get(sid))
			if len(blocks) == 0 {
				continue
			}
			text := blocksText(blocks)
			if pageNumber(text) && len(blocks) == 1 && use[sid] == 1 {
				continue
			}
			// A frame turned on its side reads by where its middle is.
			y := item.Y
			if item.Rotate != 0 {
				y = item.Y + math.Max(item.H, 0)/2
			}
			placed = append(placed, placedEntry{y, item.X, blocks})
		}
		sort.SliceStable(placed, func(i, j int) bool {
			ri, rj := math.RoundToEven(placed[i].y/4), math.RoundToEven(placed[j].y/4)
			if ri != rj {
				return ri < rj
			}
			return placed[i].x < placed[j].x
		})
		for _, p := range placed {
			entries = append(entries, p.blocks)
		}
	}

	allParas := func(blocks []fblock) bool {
		for _, b := range blocks {
			if b.para == nil {
				return false
			}
		}
		return true
	}
	// Short lines that come back on page after page (the executant's
	// signature mark): keep the first.
	counts := map[string]int{}
	for _, blocks := range entries {
		if allParas(blocks) {
			counts[blocksText(blocks)]++
		}
	}
	seen := map[string]bool{}
	var out []fblock
	for _, blocks := range entries {
		if allParas(blocks) {
			text := blocksText(blocks)
			if text != "" && utf8.RuneCountInString(text) <= 40 && counts[text] >= 3 {
				if seen[text] {
					continue
				}
				seen[text] = true
			}
		}
		out = append(out, blocks...)
	}

	// Outside tables the text is one continuous run, one space between what
	// were paragraphs, in one size: on a ruled sheet every line of writing
	// sits on a rule.
	plain := func(spans []FSpan) []FSpan {
		o := make([]FSpan, len(spans))
		for i, s := range spans {
			o[i] = FSpan{T: s.T, Bold: s.Bold, Italic: s.Italic}
		}
		return o
	}
	var merged []FBlock
	for _, b := range out {
		switch {
		case b.table != nil:
			for _, row := range b.table.Rows {
				for _, cell := range row {
					for _, p := range cell.Blocks {
						p.Spans = plain(p.Spans)
					}
				}
			}
			merged = append(merged, FBlock{Table: b.table})
		case len(merged) > 0 && merged[len(merged)-1].Para != nil:
			last := merged[len(merged)-1].Para
			last.Spans = append(last.Spans, FSpan{T: " "})
			last.Spans = append(last.Spans, plain(b.para.Spans)...)
		default:
			merged = append(merged, FBlock{Para: &FPara{Kind: "p", Align: "justify", Indent: false, Spans: plain(b.para.Spans)}})
		}
	}
	for _, b := range merged {
		if b.Para == nil {
			continue
		}
		var joined []FSpan
		for _, sp := range b.Para.Spans {
			if n := len(joined); n > 0 && joined[n-1].Bold == sp.Bold && joined[n-1].Italic == sp.Italic {
				joined[n-1].T += sp.T
			} else {
				joined = append(joined, sp)
			}
		}
		for i := range joined {
			joined[i].T = spacesRe.ReplaceAllString(joined[i].T, " ")
		}
		b.Para.Spans = joined
	}
	if merged == nil {
		merged = []FBlock{}
	}
	return &Sheet{Name: doc.Name, Format: doc.Format, Pages: len(doc.Pages), Size: pyRound(size, 1),
		Gap: pyRound(gap, 2), Margins: margins, Blocks: merged}
}

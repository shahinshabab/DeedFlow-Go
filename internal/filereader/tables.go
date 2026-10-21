package filereader

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"unicode/utf8"
)

// Finds tables that were drawn with lines on a page (a box with rules across
// and down it, and a text block in each cell), which is how tables are made
// in PageMaker, so they can be rebuilt as real tables.

const (
	tableTolerance = 3.0  // points two rules may be apart and still be one
	tableMinSize   = 30.0 // smallest box (points) worth treating as a table
)

// Table is a table found from ruled lines. Paths are the lines the table
// replaces and Texts the text blocks that became cell contents, so the caller
// can leave both out.
type Table struct {
	X, Y, W, H float64
	Cols, Rows []float64
	Cells      []*TableCell
	Border     float64
	Rule       float64
	Paths      []*Item
	Texts      []*Item
}

// TableCell is one cell of a found table.
type TableCell struct {
	Row, Col, Rowspan, Colspan int
	Items                      []*Item
}

type pt struct{ x, y float64 }

type subPath struct {
	points []pt
	closed bool
}

var (
	cmdRe = regexp.MustCompile(`([MLAZ])([^MLAZ]*)`)
	numRe = regexp.MustCompile(`-?[\d.]+(?:e-?\d+)?`)
)

func parseFloat(s string) float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		panic(fmt.Errorf("could not convert string to float: %q", s))
	}
	return f
}

// pathPoints is the corner points of each subpath of an SVG-ish "M x y L x y ... Z".
func pathPoints(d string) []*subPath {
	var paths []*subPath
	var current *subPath
	for _, m := range cmdRe.FindAllStringSubmatch(d, -1) {
		cmd, args := m[1], m[2]
		var nums []float64
		for _, n := range numRe.FindAllString(args, -1) {
			nums = append(nums, parseFloat(n))
		}
		last2 := func() pt { return pt{nums[len(nums)-2], nums[len(nums)-1]} }
		switch {
		case cmd == "M":
			current = &subPath{points: []pt{last2()}}
			paths = append(paths, current)
		case (cmd == "L" || cmd == "A") && current != nil && len(nums) >= 2:
			current.points = append(current.points, last2())
		case cmd == "Z" && current != nil:
			current.closed = true
		}
	}
	return paths
}

type segment struct {
	kind      byte // 'h' or 'v'
	pos, a, b float64
}

// segments are the straight horizontal and vertical rules in a path.
func segments(item *Item) []segment {
	var out []segment
	for _, sub := range pathPoints(item.D) {
		pts := append([]pt(nil), sub.points...)
		if sub.closed {
			pts = append(pts, sub.points[0])
		}
		for i := 0; i+1 < len(pts); i++ {
			x1, y1, x2, y2 := pts[i].x, pts[i].y, pts[i+1].x, pts[i+1].y
			dx, dy := math.Abs(x2-x1), math.Abs(y2-y1)
			if dy <= tableTolerance && dx > dy*10 {
				out = append(out, segment{'h', (y1 + y2) / 2, math.Min(x1, x2), math.Max(x1, x2)})
			} else if dx <= tableTolerance && dy > dx*10 {
				out = append(out, segment{'v', (x1 + x2) / 2, math.Min(y1, y2), math.Max(y1, y2)})
			}
		}
	}
	return out
}

type box4 struct{ x0, y0, x1, y1 float64 }

// boxOf is an axis-aligned rectangle path's box.
func boxOf(item *Item) (box4, bool) {
	subs := pathPoints(item.D)
	if len(subs) != 1 {
		return box4{}, false
	}
	pts := subs[0].points
	if len(pts) > 0 && pts[len(pts)-1] == pts[0] {
		pts = pts[:len(pts)-1]
	}
	if !subs[0].closed || len(pts) != 4 {
		return box4{}, false
	}
	if len(segments(item)) != 4 {
		return box4{}, false
	}
	b := box4{pts[0].x, pts[0].y, pts[0].x, pts[0].y}
	for _, p := range pts[1:] {
		b.x0, b.y0 = math.Min(b.x0, p.x), math.Min(b.y0, p.y)
		b.x1, b.y1 = math.Max(b.x1, p.x), math.Max(b.y1, p.y)
	}
	if b.x1-b.x0 < tableMinSize || b.y1-b.y0 < tableMinSize {
		return box4{}, false
	}
	return b, true
}

func cluster(values []float64) []float64 {
	v := append([]float64(nil), values...)
	sort.Float64s(v)
	var groups [][]float64
	for _, x := range v {
		if n := len(groups); n > 0 && x-groups[n-1][len(groups[n-1])-1] <= tableTolerance {
			groups[n-1] = append(groups[n-1], x)
		} else {
			groups = append(groups, []float64{x})
		}
	}
	out := make([]float64, len(groups))
	for i, g := range groups {
		sum := 0.0
		for _, x := range g {
			sum += x
		}
		out[i] = sum / float64(len(g))
	}
	return out
}

func covered(segs []segment, kind byte, pos, at float64) bool {
	for _, s := range segs {
		if s.kind == kind && math.Abs(s.pos-pos) <= tableTolerance && s.a-tableTolerance <= at && at <= s.b+tableTolerance {
			return true
		}
	}
	return false
}

// center is the middle of a text item. Rotation is about the centre, so the
// centre doesn't move.
func center(item *Item) (float64, float64) {
	return item.X + item.W/2, item.Y + math.Max(item.H, 0)/2
}

// anchor is where a text block's text is: its left edge for left-aligned text
// (a row number in a narrow column often sits in a wider block), its right
// edge for right-aligned, else its centre. A block reaching outside the table
// (text pushed along with spaces) goes by its centre.
func anchor(item *Item, align string, b box4) (float64, float64) {
	cx, cy := center(item)
	if item.Rotate != 0 {
		return cx, cy
	}
	if (align == "left" || align == "justify") && b.x0 <= item.X+3 && item.X+3 <= b.x1 {
		return item.X + 3, cy
	}
	if align == "right" && b.x0 <= item.X+item.W-3 && item.X+item.W-3 <= b.x1 {
		return item.X + item.W - 3, cy
	}
	return cx, cy
}

// textWidth is roughly how wide a text block's text would run on one line, in points.
func textWidth(blocks []Block) float64 {
	width := 0.0
	for _, b := range blocks {
		if b.Kind != "p" {
			continue
		}
		for _, s := range b.Spans {
			size := s.Size
			if size == 0 {
				size = 12
			}
			width += float64(utf8.RuneCountInString(s.T)) * size * 0.5
		}
	}
	return width
}

func samePath(a, b *Item) bool {
	return a.Type == b.Type && a.D == b.D && a.Fill == b.Fill && a.Stroke == b.Stroke && a.SW == b.SW
}

func tableInBox(boxItem *Item, b box4, paths, texts []*Item, storyUse map[string]int, stories *Stories) *Table {
	x0, y0, x1, y1 := b.x0, b.y0, b.x1, b.y1

	inside := func(s segment) bool {
		if s.kind == 'h' {
			return y0+tableTolerance < s.pos && s.pos < y1-tableTolerance && s.a < x1+tableTolerance && s.b > x0-tableTolerance
		}
		return x0+tableTolerance < s.pos && s.pos < x1-tableTolerance && s.a < y1+tableTolerance && s.b > y0-tableTolerance
	}

	var rules []segment
	var used []*Item
	for _, item := range paths {
		if item == boxItem {
			continue
		}
		segs := segments(item)
		all := len(segs) > 0
		for _, s := range segs {
			if !inside(s) {
				all = false
				break
			}
		}
		if all {
			rules = append(rules, segs...)
			used = append(used, item)
		}
	}
	if len(rules) == 0 {
		return nil
	}
	segs := append(append([]segment(nil), rules...), segments(boxItem)...)
	xv := []float64{x0, x1}
	yv := []float64{y0, y1}
	for _, r := range rules {
		if r.kind == 'v' {
			xv = append(xv, r.pos)
		} else {
			yv = append(yv, r.pos)
		}
	}
	xs, ys := cluster(xv), cluster(yv)
	nrows, ncols := len(ys)-1, len(xs)-1
	if nrows*ncols < 2 {
		return nil
	}

	// Neighbouring grid cells with no rule between them are one cell.
	parent := make([]int, nrows*ncols)
	for i := range parent {
		parent[i] = i
	}
	var find func(i int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	for r := 0; r < nrows; r++ {
		for c := 0; c < ncols; c++ {
			midY, midX := (ys[r]+ys[r+1])/2, (xs[c]+xs[c+1])/2
			if c+1 < ncols && !covered(segs, 'v', xs[c+1], midY) {
				parent[find(r*ncols+c+1)] = find(r*ncols + c)
			}
			if r+1 < nrows && !covered(segs, 'h', ys[r+1], midX) {
				parent[find((r+1)*ncols+c)] = find(r*ncols + c)
			}
		}
	}
	var groupOrder []int
	groups := map[int][][2]int{}
	for i := 0; i < nrows*ncols; i++ {
		root := find(i)
		if _, ok := groups[root]; !ok {
			groupOrder = append(groupOrder, root)
		}
		groups[root] = append(groups[root], [2]int{i / ncols, i % ncols})
	}
	var cells []*TableCell
	for _, root := range groupOrder {
		members := groups[root]
		minR, maxR, minC, maxC := members[0][0], members[0][0], members[0][1], members[0][1]
		for _, m := range members {
			minR, maxR = min(minR, m[0]), max(maxR, m[0])
			minC, maxC = min(minC, m[1]), max(maxC, m[1])
		}
		rowspan, colspan := maxR-minR+1, maxC-minC+1
		if rowspan*colspan != len(members) {
			return nil // not a grid a table can hold
		}
		cells = append(cells, &TableCell{Row: minR, Col: minC, Rowspan: rowspan, Colspan: colspan})
	}
	sort.SliceStable(cells, func(i, j int) bool {
		if cells[i].Row != cells[j].Row {
			return cells[i].Row < cells[j].Row
		}
		return cells[i].Col < cells[j].Col
	})

	cellAt := func(x, y float64) *TableCell {
		r, c := -1, -1
		for i := 0; i < nrows; i++ {
			if ys[i] <= y && y < ys[i+1] {
				r = i
				break
			}
		}
		for i := 0; i < ncols; i++ {
			if xs[i] <= x && x < xs[i+1] {
				c = i
				break
			}
		}
		if r < 0 || c < 0 {
			return nil
		}
		root := find(r*ncols + c)
		first := -1
		for i := 0; i < nrows*ncols; i++ {
			if find(i) == root {
				first = i
				break
			}
		}
		r0, c0 := first/ncols, first%ncols
		for _, cell := range cells {
			if cell.Row == r0 && cell.Col == c0 {
				return cell
			}
		}
		panic("table cell not found")
	}
	widthOf := func(cell *TableCell) float64 { return xs[cell.Col+cell.Colspan] - xs[cell.Col] }

	var taken []*Item
	for _, item := range texts {
		// A block that's one link of a story threaded elsewhere stays put.
		if storyUse[item.Story] != 1 {
			continue
		}
		blocks := stories.Get(item.Story)
		var first Block
		if len(blocks) > 0 && blocks[0].Kind == "p" {
			first = blocks[0]
		}
		align := first.Align
		if align == "" {
			align = "left"
		}
		lead := ""
		for _, s := range first.Spans {
			lead += s.T
		}
		if lead != "" {
			if r, _ := utf8.DecodeRuneInString(lead); pyIsSpace(r) {
				align = "spaced" // pushed along with spaces: goes by its centre
			}
		}
		ax, ay := anchor(item, align, b)
		cell := cellAt(ax, ay)
		// A row number sits at its block's left edge, but a block of running
		// text that starts a little into a narrow column (a property
		// description beside its number) belongs where most of it is: the
		// cell under its centre.
		if cell != nil && textWidth(blocks) > 2*widthOf(cell) {
			cx, cy := center(item)
			if alt := cellAt(cx, cy); alt != nil {
				cell = alt
			}
		}
		if cell != nil {
			cell.Items = append(cell.Items, item)
			taken = append(taken, item)
		}
	}
	for _, cell := range cells {
		items := cell.Items
		sort.SliceStable(items, func(i, j int) bool {
			_, ci := center(items[i])
			_, cj := center(items[j])
			ri, rj := math.RoundToEven(ci), math.RoundToEven(cj)
			if ri != rj {
				return ri < rj
			}
			return items[i].X < items[j].X
		})
	}

	rule := 0.25
	for i, it := range used {
		w := it.SW
		if w == 0 {
			w = 1.0
		}
		if i == 0 || w < rule {
			rule = w
		}
	}
	border := boxItem.SW
	if border == 0 {
		border = 1.0
	}
	t := &Table{X: x0, Y: y0, W: x1 - x0, H: y1 - y0, Cells: cells, Border: border, Rule: rule,
		Paths: append([]*Item{boxItem}, used...), Texts: taken}
	for i := 0; i+1 < len(xs); i++ {
		t.Cols = append(t.Cols, xs[i+1]-xs[i])
	}
	for i := 0; i+1 < len(ys); i++ {
		t.Rows = append(t.Rows, ys[i+1]-ys[i])
	}
	return t
}

// FindTables are the tables drawn on a page, whose text is in stories.
// storyUse counts the text blocks showing each story in the whole document
// (StoryFrameCounts); without it, only this page is counted.
func FindTables(page *Page, stories *Stories, storyUse map[string]int) []*Table {
	var paths, texts []*Item
	for _, i := range page.Items {
		if i.Type == "path" && i.Stroke != "" && i.Stroke != "none" {
			paths = append(paths, i)
		}
		if i.Type == "text" {
			texts = append(texts, i)
		}
	}
	if storyUse == nil {
		storyUse = map[string]int{}
		for _, item := range texts {
			storyUse[item.Story]++
		}
	}
	var tables []*Table
	for _, item := range paths {
		b, ok := boxOf(item)
		if !ok {
			continue
		}
		dup := false
		for _, t := range tables {
			for _, p := range t.Paths {
				if samePath(item, p) {
					dup = true
				}
			}
		}
		if dup {
			continue
		}
		if t := tableInBox(item, b, paths, texts, storyUse, stories); t != nil {
			tables = append(tables, t)
		}
	}
	return tables
}

// StoryFrameCounts is how many text blocks show each story, across the whole document.
func StoryFrameCounts(doc *Doc) map[string]int {
	counts := map[string]int{}
	for _, page := range doc.Pages {
		for _, item := range page.Items {
			if item.Type == "text" {
				counts[item.Story]++
			}
		}
	}
	return counts
}

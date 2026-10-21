package plumber

import (
	"sort"
)

const (
	snapTolerance         = 3.0
	joinTolerance         = 3.0
	edgeMinLength         = 3.0
	edgeMinLengthPrefilt  = 1.0
	intersectionTolerance = 3.0
)

// edge mirrors the dict fields pdfplumber's table finder reads.
type edge struct {
	x0, x1, top, bottom float64
	width, height       float64
	orient              byte // 'h', 'v' or 0 (neither)
}

func (e edge) key() [4]float64 { return [4]float64{e.x0, e.top, e.x1, e.bottom} }

func lineEdge(l *Line) edge {
	e := edge{x0: l.X0, x1: l.X1, top: l.Top, bottom: l.Bottom, width: l.W, height: l.H}
	if l.Top == l.Bottom {
		e.orient = 'h'
	} else {
		e.orient = 'v'
	}
	return e
}

func rectEdges(r *Rect) []edge {
	return []edge{
		{x0: r.X0, x1: r.X1, top: r.Top, bottom: r.Top, width: r.W, height: 0, orient: 'h'},
		{x0: r.X0, x1: r.X1, top: r.Top + r.H, bottom: r.Bottom, width: r.W, height: 0, orient: 'h'},
		{x0: r.X0, x1: r.X0, top: r.Top, bottom: r.Bottom, width: 0, height: r.H, orient: 'v'},
		{x0: r.X1, x1: r.X1, top: r.Top, bottom: r.Bottom, width: 0, height: r.H, orient: 'v'},
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func curveEdges(c *Curve) []edge {
	var out []edge
	for i := 0; i+1 < len(c.Pts); i++ {
		p0, p1 := c.Pts[i], c.Pts[i+1]
		e := edge{
			x0: minF(p0[0], p1[0]), x1: maxF(p0[0], p1[0]),
			top: minF(p0[1], p1[1]), bottom: maxF(p0[1], p1[1]),
			width: abs(p0[0] - p1[0]), height: abs(p0[1] - p1[1]),
		}
		switch {
		case p0[0] == p1[0]:
			e.orient = 'v'
		case p0[1] == p1[1]:
			e.orient = 'h'
		}
		out = append(out, e)
	}
	return out
}

func minF(a, b float64) float64 {
	if b < a {
		return b
	}
	return a
}

func maxF(a, b float64) float64 {
	if b > a {
		return b
	}
	return a
}

func filterEdges(edges []edge, orient byte, minLength float64) []edge {
	var out []edge
	for _, e := range edges {
		if orient != 0 && e.orient != orient {
			continue
		}
		dim := e.width
		if e.orient == 'v' {
			dim = e.height
		}
		if dim >= minLength {
			out = append(out, e)
		}
	}
	return out
}

func snapEdges(edges []edge, xTol, yTol float64) []edge {
	var v, h []edge
	for _, e := range edges {
		if e.orient == 'v' {
			v = append(v, e)
		} else {
			h = append(h, e)
		}
	}
	return append(snapObjects(v, true, xTol), snapObjects(h, false, yTol)...)
}

// snapObjects clusters by x0 (horizontal=true) or top and moves each edge to
// its cluster average, using move_object's arithmetic.
func snapObjects(es []edge, byX0 bool, tol float64) []edge {
	keys := make([]float64, len(es))
	for i, e := range es {
		if byX0 {
			keys[i] = e.x0
		} else {
			keys[i] = e.top
		}
	}
	var out []edge
	for _, cl := range clusterIndex(keys, tol, false) {
		sum := 0.0
		for _, i := range cl {
			sum += keys[i]
		}
		avg := sum / float64(len(cl))
		for _, i := range cl {
			e := es[i]
			d := avg - keys[i]
			if byX0 {
				e.x0 += d
				e.x1 += d
			} else {
				e.top += d
				e.bottom += d
			}
			out = append(out, e)
		}
	}
	return out
}

func joinEdgeGroup(es []edge, orient byte, tol float64) []edge {
	sorted := append([]edge(nil), es...)
	if orient == 'h' {
		sort.SliceStable(sorted, func(a, b int) bool { return sorted[a].x0 < sorted[b].x0 })
	} else {
		sort.SliceStable(sorted, func(a, b int) bool { return sorted[a].top < sorted[b].top })
	}
	joined := []edge{sorted[0]}
	for _, e := range sorted[1:] {
		last := &joined[len(joined)-1]
		if orient == 'h' {
			if e.x0 <= last.x1+tol {
				if e.x1 > last.x1 {
					last.width = e.x1 - last.x0
					last.x1 = e.x1
				}
			} else {
				joined = append(joined, e)
			}
		} else {
			if e.top <= last.bottom+tol {
				if e.bottom > last.bottom {
					diff := e.bottom - last.bottom
					last.height = last.height + diff
					last.bottom = e.bottom
				}
			} else {
				joined = append(joined, e)
			}
		}
	}
	return joined
}

func mergeEdges(edges []edge, snapX, snapY, joinX, joinY float64) []edge {
	if snapX > 0 || snapY > 0 {
		edges = snapEdges(edges, snapX, snapY)
	}
	group := func(e edge) (int, float64) {
		if e.orient == 'h' {
			return 0, e.top
		}
		return 1, e.x0
	}
	sorted := append([]edge(nil), edges...)
	sort.SliceStable(sorted, func(a, b int) bool {
		ga, va := group(sorted[a])
		gb, vb := group(sorted[b])
		if ga != gb {
			return ga < gb
		}
		return va < vb
	})
	var out []edge
	for i := 0; i < len(sorted); {
		g, v := group(sorted[i])
		j := i + 1
		for j < len(sorted) {
			g2, v2 := group(sorted[j])
			if g2 != g || v2 != v {
				break
			}
			j++
		}
		if g == 0 {
			out = append(out, joinEdgeGroup(sorted[i:j], 'h', joinX)...)
		} else {
			out = append(out, joinEdgeGroup(sorted[i:j], 'v', joinY)...)
		}
		i = j
	}
	return out
}

type point [2]float64

type isect struct{ v, h [][4]float64 }

func edgesToIntersections(edges []edge, xTol, yTol float64) map[point]*isect {
	var vs, hs []edge
	for _, e := range edges {
		switch e.orient {
		case 'v':
			vs = append(vs, e)
		case 'h':
			hs = append(hs, e)
		}
	}
	sort.SliceStable(vs, func(a, b int) bool {
		if vs[a].x0 != vs[b].x0 {
			return vs[a].x0 < vs[b].x0
		}
		return vs[a].top < vs[b].top
	})
	sort.SliceStable(hs, func(a, b int) bool {
		if hs[a].top != hs[b].top {
			return hs[a].top < hs[b].top
		}
		return hs[a].x0 < hs[b].x0
	})
	out := map[point]*isect{}
	for _, v := range vs {
		for _, h := range hs {
			if v.top <= h.top+yTol && v.bottom >= h.top-yTol && v.x0 >= h.x0-xTol && v.x0 <= h.x1+xTol {
				p := point{v.x0, h.top}
				is := out[p]
				if is == nil {
					is = &isect{}
					out[p] = is
				}
				is.v = append(is.v, v.key())
				is.h = append(is.h, h.key())
			}
		}
	}
	return out
}

type cell [4]float64 // x0, top, x1, bottom

func intersectionsToCells(ix map[point]*isect) []cell {
	connects := func(p1, p2 point) bool {
		common := func(a, b [][4]float64) bool {
			set := map[[4]float64]bool{}
			for _, k := range a {
				set[k] = true
			}
			for _, k := range b {
				if set[k] {
					return true
				}
			}
			return false
		}
		if p1[0] == p2[0] && common(ix[p1].v, ix[p2].v) {
			return true
		}
		if p1[1] == p2[1] && common(ix[p1].h, ix[p2].h) {
			return true
		}
		return false
	}
	pts := make([]point, 0, len(ix))
	for p := range ix {
		pts = append(pts, p)
	}
	sort.Slice(pts, func(a, b int) bool {
		if pts[a][0] != pts[b][0] {
			return pts[a][0] < pts[b][0]
		}
		return pts[a][1] < pts[b][1]
	})
	var cells []cell
	for i, pt := range pts {
		if i == len(pts)-1 {
			continue
		}
		rest := pts[i+1:]
		var below, right []point
		for _, q := range rest {
			if q[0] == pt[0] {
				below = append(below, q)
			}
			if q[1] == pt[1] {
				right = append(right, q)
			}
		}
	search:
		for _, b := range below {
			if !connects(pt, b) {
				continue
			}
			for _, r := range right {
				if !connects(pt, r) {
					continue
				}
				br := point{r[0], b[1]}
				if _, ok := ix[br]; ok && connects(br, r) && connects(br, b) {
					cells = append(cells, cell{pt[0], pt[1], br[0], br[1]})
					break search
				}
			}
		}
	}
	return cells
}

func cellsToTables(cells []cell) [][]cell {
	corners := func(c cell) [4]point {
		return [4]point{{c[0], c[1]}, {c[0], c[3]}, {c[2], c[1]}, {c[2], c[3]}}
	}
	remaining := append([]cell(nil), cells...)
	cur := map[point]bool{}
	var curCells []cell
	var tables [][]cell
	for len(remaining) > 0 {
		initial := len(curCells)
		snapshot := append([]cell(nil), remaining...)
		for _, c := range snapshot {
			cc := corners(c)
			take := false
			if len(curCells) == 0 {
				take = true
			} else {
				for _, p := range cc {
					if cur[p] {
						take = true
						break
					}
				}
			}
			if take {
				for _, p := range cc {
					cur[p] = true
				}
				curCells = append(curCells, c)
				for k, r := range remaining {
					if r == c {
						remaining = append(remaining[:k], remaining[k+1:]...)
						break
					}
				}
			}
		}
		if len(curCells) == initial {
			tables = append(tables, append([]cell(nil), curCells...))
			cur = map[point]bool{}
			curCells = nil
		}
	}
	if len(curCells) > 0 {
		tables = append(tables, curCells)
	}
	minKey := func(t []cell) (float64, float64) {
		bt, bx := t[0][1], t[0][0]
		for _, c := range t[1:] {
			if c[1] < bt || (c[1] == bt && c[0] < bx) {
				bt, bx = c[1], c[0]
			}
		}
		return bt, bx
	}
	sort.SliceStable(tables, func(a, b int) bool {
		ta, xa := minKey(tables[a])
		tb, xb := minKey(tables[b])
		if ta != tb {
			return ta < tb
		}
		return xa < xb
	})
	var out [][]cell
	for _, t := range tables {
		if len(t) > 1 {
			out = append(out, t)
		}
	}
	return out
}

// Table is one detected table.
type Table struct {
	page  *Page
	Cells []cell
}

func (p *Page) pageEdges() []edge {
	var es []edge
	for i := range p.Lines {
		es = append(es, lineEdge(&p.Lines[i]))
	}
	for i := range p.Rects {
		es = append(es, rectEdges(&p.Rects[i])...)
	}
	for i := range p.Curves {
		es = append(es, curveEdges(&p.Curves[i])...)
	}
	return es
}

// FindTables is Page.find_tables() with default (lines) settings.
func (p *Page) FindTables() []*Table {
	all := p.pageEdges()
	v := filterEdges(all, 'v', edgeMinLengthPrefilt)
	h := filterEdges(all, 'h', edgeMinLengthPrefilt)
	edges := append(append([]edge(nil), v...), h...)
	edges = mergeEdges(edges, snapTolerance, snapTolerance, joinTolerance, joinTolerance)
	edges = filterEdges(edges, 0, edgeMinLength)
	ix := edgesToIntersections(edges, intersectionTolerance, intersectionTolerance)
	cells := intersectionsToCells(ix)
	var out []*Table
	for _, g := range cellsToTables(cells) {
		out = append(out, &Table{page: p, Cells: g})
	}
	return out
}

func (t *Table) rows() [][]*cell {
	sorted := append([]cell(nil), t.Cells...)
	sort.SliceStable(sorted, func(a, b int) bool {
		if sorted[a][1] != sorted[b][1] {
			return sorted[a][1] < sorted[b][1]
		}
		return sorted[a][0] < sorted[b][0]
	})
	xsSet := map[float64]bool{}
	var xs []float64
	for _, c := range t.Cells {
		if !xsSet[c[0]] {
			xsSet[c[0]] = true
			xs = append(xs, c[0])
		}
	}
	sort.Float64s(xs)
	var rows [][]*cell
	for i := 0; i < len(sorted); {
		j := i
		xd := map[float64]*cell{}
		for j < len(sorted) && sorted[j][1] == sorted[i][1] {
			c := sorted[j]
			xd[c[0]] = &c
			j++
		}
		row := make([]*cell, len(xs))
		for k, x := range xs {
			row[k] = xd[x]
		}
		rows = append(rows, row)
		i = j
	}
	return rows
}

func rowBBox(row []*cell) cell {
	var b cell
	first := true
	for _, c := range row {
		if c == nil {
			continue
		}
		if first {
			b, first = *c, false
			continue
		}
		b[0], b[1] = minF(b[0], c[0]), minF(b[1], c[1])
		b[2], b[3] = maxF(b[2], c[2]), maxF(b[3], c[3])
	}
	return b
}

func charInBBox(c *Char, b cell) bool {
	v := (c.Top + c.Bottom) / 2
	h := (c.X0 + c.X1) / 2
	return h >= b[0] && h < b[2] && v >= b[1] && v < b[3]
}

// Extract is Table.extract() with default text settings; nil marks the
// missing cells of ragged rows.
func (t *Table) Extract() [][]*string {
	chars := t.page.charPtrs()
	var out [][]*string
	for _, row := range t.rows() {
		rb := rowBBox(row)
		var rowChars []*Char
		for _, c := range chars {
			if charInBBox(c, rb) {
				rowChars = append(rowChars, c)
			}
		}
		arr := make([]*string, len(row))
		for i, c := range row {
			if c == nil {
				continue
			}
			var cc []*Char
			for _, ch := range rowChars {
				if charInBBox(ch, *c) {
					cc = append(cc, ch)
				}
			}
			s := extractTextPlain(cc)
			arr[i] = &s
		}
		out = append(out, arr)
	}
	return out
}

// ExtractTables is Page.extract_tables() with default settings.
func (p *Page) ExtractTables() [][][]*string {
	var out [][][]*string
	for _, t := range p.FindTables() {
		out = append(out, t.Extract())
	}
	return out
}

// BBox of the whole table.
func (t *Table) BBox() (x0, top, x1, bottom float64) {
	b := t.Cells[0]
	for _, c := range t.Cells[1:] {
		b[0], b[1] = minF(b[0], c[0]), minF(b[1], c[1])
		b[2], b[3] = maxF(b[2], c[2]), maxF(b[3], c[3])
	}
	return b[0], b[1], b[2], b[3]
}

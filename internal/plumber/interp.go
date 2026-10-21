package plumber

import (
	"bytes"
	"errors"
	"fmt"
	"math"

	pdf "github.com/shahinshabab/deedflow-go/internal/plumber/rawpdf"
)

type matrix [6]float64

var identity = matrix{1, 0, 0, 1, 0, 0}

// multMatrix is pdfminer's mult_matrix(m1, m0): m1 applied first, then m0.
func multMatrix(m1, m0 matrix) matrix {
	a1, b1, c1, d1, e1, f1 := m1[0], m1[1], m1[2], m1[3], m1[4], m1[5]
	a0, b0, c0, d0, e0, f0 := m0[0], m0[1], m0[2], m0[3], m0[4], m0[5]
	return matrix{a0*a1 + c0*b1, b0*a1 + d0*b1, a0*c1 + c0*d1, b0*c1 + d0*d1, a0*e1 + c0*f1 + e0, b0*e1 + d0*f1 + f0}
}

func applyPt(m matrix, x, y float64) (float64, float64) {
	return m[0]*x + m[2]*y + m[4], m[1]*x + m[3]*y + m[5]
}

func translateMatrix(m matrix, x, y float64) matrix {
	m[4], m[5] = x*m[0]+y*m[2]+m[4], x*m[1]+y*m[3]+m[5]
	return m
}

// Char is one character of a page, with pdfplumber's attributes. Coordinates
// are in points from the top-left corner of the page.
type Char struct {
	Text     string
	FontName string
	Size     float64
	Upright  bool
	X0, X1   float64
	Top      float64
	Bottom   float64
	Adv      float64
}

// Width and Height of the character box.
func (c *Char) Width() float64  { return c.X1 - c.X0 }
func (c *Char) Height() float64 { return c.Bottom - c.Top }

// Line is a stroked straight segment.
type Line struct {
	X0, X1, Top, Bottom float64
	W, H                float64 // raw pdfminer width / height
	LineWidth           float64
	Pts                 [][2]float64
}

// Rect is a rectangle path.
type Rect struct {
	X0, X1, Top, Bottom float64
	W, H                float64 // raw pdfminer width / height
	LineWidth           float64
	Stroke, Fill        bool
	Pts                 [][2]float64
}

// Curve is any other path.
type Curve struct {
	X0, X1, Top, Bottom float64
	W, H                float64 // raw pdfminer width / height
	LineWidth           float64
	Pts                 [][2]float64
}

// Image is a picture placed on the page.
type Image struct {
	X0, X1, Top, Bottom float64
	Name                string
}

// Page holds the objects of one PDF page.
type Page struct {
	Number        int
	Width, Height float64
	Chars         []Char
	Lines         []Line
	Rects         []Rect
	Curves        []Curve
	Images        []Image
	rotation      int
	mbX0, mbTop   float64
	mediaH        float64
}

// Doc is an opened PDF.
type Doc struct {
	r     *pdf.Reader
	fonts map[string]*pdfFont
	pages map[int]*Page
}

// Open reads a PDF held in memory.
func Open(data []byte) (doc *Doc, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("opening PDF: %v", r)
		}
	}()
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		// Encrypted with a non-empty password, or damaged.
		return nil, err
	}
	return &Doc{r: r, fonts: map[string]*pdfFont{}, pages: map[int]*Page{}}, nil
}

// NumPages is the number of pages.
func (d *Doc) NumPages() int { return d.r.NumPages() }

type pathSeg struct {
	op  byte
	pts [][2]float64 // operands (control points then end point), user space
}

type textState struct {
	font                           *pdfFont
	fontsize, charspace, wordspace float64
	scaling, leading, rise         float64
	render                         int
	matrix                         matrix
	linematrix                     [2]float64
}

type saved struct {
	ctm       matrix
	ts        textState
	linewidth float64
}

type interp struct {
	doc       *Doc
	page      *Page
	ctm       matrix
	ts        textState
	linewidth float64
	gstack    []saved
	path      []pathSeg
	fonts     map[string]*pdfFont
	xobjects  pdf.Value
	resources pdf.Value
	depth     int
}

// Page interprets one page (1-based); results are cached.
func (d *Doc) Page(n int) (page *Page, err error) {
	if p, ok := d.pages[n]; ok {
		return p, nil
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("reading page %d: %v", n, r)
		}
	}()
	obj := d.r.PageObject(n)
	if obj.IsNull() {
		return nil, fmt.Errorf("page %d not found", n)
	}
	rot := 0
	if v := pdf.Inherited(obj, "Rotate"); !v.IsNull() {
		rot = ((int(v.Float64()) % 360) + 360) % 360
	}
	mb := pdf.Inherited(obj, "MediaBox").Floats()
	if len(mb) != 4 {
		mb = []float64{0, 0, 612, 792}
	}
	// pdfplumber's _normalize_box / _invert_box.
	x0, x1 := math.Min(mb[0], mb[2]), math.Max(mb[0], mb[2])
	y0, y1 := math.Min(mb[1], mb[3]), math.Max(mb[1], mb[3])
	nb := [4]float64{x0, y0, x1, y1}
	if rot == 90 || rot == 270 {
		nb = [4]float64{y0, x0, y1, x1}
	}
	mbHeight := nb[3] - nb[1]
	inv := [4]float64{nb[0], mbHeight - nb[3], nb[2], mbHeight - nb[1]}

	p := &Page{Number: n, rotation: rot, mbX0: inv[0], mbTop: inv[1], mediaH: mbHeight}
	p.Width, p.Height = inv[2]-inv[0], inv[3]-inv[1]

	// pdfminer's page-level CTM from the raw MediaBox.
	var ctm matrix
	switch rot {
	case 90:
		ctm = matrix{0, -1, 1, 0, -mb[1], mb[2]}
	case 180:
		ctm = matrix{-1, 0, 0, -1, mb[2], mb[3]}
	case 270:
		ctm = matrix{0, 1, -1, 0, mb[3], -mb[0]}
	default:
		ctm = matrix{1, 0, 0, 1, -mb[0], -mb[1]}
	}
	it := &interp{doc: d, page: p, ctm: ctm, fonts: map[string]*pdfFont{}}
	it.resetState()
	res := pdf.Inherited(obj, "Resources")
	it.setResources(res)
	contents := obj.Key("Contents")
	var data []byte
	switch contents.Kind() {
	case pdf.Stream:
		data, _ = contents.Bytes()
	case pdf.Array:
		for i := 0; i < contents.Len(); i++ {
			chunk, _ := contents.Index(i).Bytes()
			data = append(data, chunk...)
			data = append(data, '\n')
		}
	}
	it.run(data)
	d.pages[n] = p
	return p, nil
}

func (it *interp) resetState() {
	it.ts = textState{scaling: 100, matrix: identity}
	it.linewidth = 0
	it.gstack = nil
	it.path = nil
}

func (it *interp) setResources(res pdf.Value) {
	it.resources = res
	it.xobjects = res.Key("XObject")
	it.fonts = map[string]*pdfFont{}
	fonts := res.Key("Font")
	for _, k := range fonts.Keys() {
		spec := fonts.Key(k)
		if spec.Kind() != pdf.Dict {
			continue
		}
		id, gen := spec.ObjectID()
		cacheKey := fmt.Sprintf("%d.%d:%s", id, gen, k)
		if id == 0 {
			cacheKey = ""
		}
		if cacheKey != "" {
			if f, ok := it.doc.fonts[cacheKey]; ok {
				it.fonts[k] = f
				continue
			}
		}
		func() {
			defer func() { recover() }()
			f := loadFont(spec)
			if f != nil {
				it.fonts[k] = f
				if cacheKey != "" {
					it.doc.fonts[cacheKey] = f
				}
			}
		}()
	}
}

// toPage converts layout coordinates (y up) to pdfplumber's top-left ones.
func (it *interp) top(y float64) float64 { return (it.page.Height - y) + it.page.mbTop }

func (it *interp) pt(x, y float64) [2]float64 {
	return [2]float64{it.page.mbX0 + x, it.page.mbTop + it.page.Height - y}
}

func num(v any) (float64, bool) {
	f, ok := v.(float64)
	return f, ok
}

func nums(args []any, n int) ([]float64, bool) {
	if len(args) < n {
		return nil, false
	}
	args = args[len(args)-n:]
	out := make([]float64, n)
	for i, a := range args {
		f, ok := num(a)
		if !ok {
			return nil, false
		}
		out[i] = f
	}
	return out, true
}

var errDepth = errors.New("form nesting too deep")

// run interprets a content stream.
func (it *interp) run(data []byte) {
	l := &lexer{data: data}
	var args []any
	for {
		t := l.next()
		if t.kind == tEOF {
			return
		}
		if t.kind != tOp {
			args = append(args, l.value(t))
			if len(args) > 4096 {
				args = args[len(args)-512:]
			}
			continue
		}
		op := string(t.str)
		switch op {
		case "true", "false", "null":
			args = append(args, nil)
			continue
		case "BI":
			it.inlineImage(l)
			args = nil
			continue
		}
		it.operator(op, args)
		args = args[:0]
	}
}

func (it *interp) operator(op string, args []any) {
	ts := &it.ts
	switch op {
	case "q":
		it.gstack = append(it.gstack, saved{it.ctm, *ts, it.linewidth})
	case "Q":
		if n := len(it.gstack); n > 0 {
			s := it.gstack[n-1]
			it.gstack = it.gstack[:n-1]
			it.ctm, it.ts, it.linewidth = s.ctm, s.ts, s.linewidth
		}
	case "cm":
		if v, ok := nums(args, 6); ok {
			it.ctm = multMatrix(matrix{v[0], v[1], v[2], v[3], v[4], v[5]}, it.ctm)
		}
	case "w":
		if v, ok := nums(args, 1); ok {
			it.linewidth = v[0]
		}
	// path construction
	case "m", "l":
		if v, ok := nums(args, 2); ok {
			it.path = append(it.path, pathSeg{op[0], [][2]float64{{v[0], v[1]}}})
		}
	case "c":
		if v, ok := nums(args, 6); ok {
			it.path = append(it.path, pathSeg{'c', [][2]float64{{v[0], v[1]}, {v[2], v[3]}, {v[4], v[5]}}})
		}
	case "v", "y":
		if v, ok := nums(args, 4); ok {
			it.path = append(it.path, pathSeg{op[0], [][2]float64{{v[0], v[1]}, {v[2], v[3]}}})
		}
	case "h":
		it.path = append(it.path, pathSeg{op: 'h'})
	case "re":
		if v, ok := nums(args, 4); ok {
			x, y, w, h := v[0], v[1], v[2], v[3]
			it.path = append(it.path,
				pathSeg{'m', [][2]float64{{x, y}}}, pathSeg{'l', [][2]float64{{x + w, y}}},
				pathSeg{'l', [][2]float64{{x + w, y + h}}}, pathSeg{'l', [][2]float64{{x, y + h}}}, pathSeg{op: 'h'})
		}
	// painting
	case "S":
		it.paint(true, false)
	case "s":
		it.path = append(it.path, pathSeg{op: 'h'})
		it.paint(true, false)
	case "f", "F", "f*":
		it.paint(false, true)
	case "B", "B*":
		it.paint(true, true)
	case "b", "b*":
		it.path = append(it.path, pathSeg{op: 'h'})
		it.paint(true, true)
	case "n":
		it.path = nil
	// text objects and state
	case "BT":
		ts.matrix, ts.linematrix = identity, [2]float64{}
	case "Tc":
		if v, ok := nums(args, 1); ok {
			ts.charspace = v[0]
		}
	case "Tw":
		if v, ok := nums(args, 1); ok {
			ts.wordspace = v[0]
		}
	case "Tz":
		if v, ok := nums(args, 1); ok {
			ts.scaling = v[0]
		}
	case "TL":
		if v, ok := nums(args, 1); ok {
			ts.leading = -v[0]
		}
	case "Tr":
		if v, ok := nums(args, 1); ok {
			ts.render = int(v[0])
		}
	case "Ts":
		if v, ok := nums(args, 1); ok {
			ts.rise = v[0]
		}
	case "Tf":
		if len(args) >= 2 {
			fname, _ := args[len(args)-2].(name)
			size, _ := num(args[len(args)-1])
			ts.font = it.fonts[string(fname)]
			ts.fontsize = size
		}
	case "Td":
		if v, ok := nums(args, 2); ok {
			it.td(v[0], v[1])
		}
	case "TD":
		if v, ok := nums(args, 2); ok {
			it.td(v[0], v[1])
			ts.leading = v[1]
		}
	case "Tm":
		if v, ok := nums(args, 6); ok {
			ts.matrix = matrix{v[0], v[1], v[2], v[3], v[4], v[5]}
			ts.linematrix = [2]float64{}
		}
	case "T*":
		it.nextLine()
	case "Tj":
		if len(args) >= 1 {
			if s, ok := args[len(args)-1].([]byte); ok {
				it.show([]any{s})
			}
		}
	case "TJ":
		if len(args) >= 1 {
			if arr, ok := args[len(args)-1].([]any); ok {
				it.show(arr)
			}
		}
	case "'":
		if len(args) >= 1 {
			if s, ok := args[len(args)-1].([]byte); ok {
				it.nextLine()
				it.show([]any{s})
			}
		}
	case `"`:
		if len(args) >= 3 {
			aw, _ := num(args[len(args)-3])
			ac, _ := num(args[len(args)-2])
			ts.wordspace, ts.charspace = aw, ac
			if s, ok := args[len(args)-1].([]byte); ok {
				it.show([]any{s})
			}
		}
	case "Do":
		if len(args) >= 1 {
			if n, ok := args[len(args)-1].(name); ok {
				it.doXObject(string(n))
			}
		}
	}
}

func (it *interp) td(tx, ty float64) {
	m := it.ts.matrix
	m[4] = tx*m[0] + ty*m[2] + m[4]
	m[5] = tx*m[1] + ty*m[3] + m[5]
	it.ts.matrix = m
	it.ts.linematrix = [2]float64{}
}

func (it *interp) nextLine() {
	m := it.ts.matrix
	m[4] = it.ts.leading*m[2] + m[4]
	m[5] = it.ts.leading*m[3] + m[5]
	it.ts.matrix = m
	it.ts.linematrix = [2]float64{}
}

// show renders a string / TJ array (PDFTextDevice.render_string).
func (it *interp) show(seq []any) {
	ts := &it.ts
	font := ts.font
	if font == nil {
		return
	}
	mat := multMatrix(ts.matrix, it.ctm)
	fontsize := ts.fontsize
	scaling := ts.scaling * 0.01
	charspace := ts.charspace * scaling
	wordspace := ts.wordspace * scaling
	rise := ts.rise
	if font.isMultibyte() {
		wordspace = 0
	}
	dxscale := 0.001 * fontsize * scaling
	x, y := ts.linematrix[0], ts.linematrix[1]
	needCharspace := false
	for _, obj := range seq {
		switch v := obj.(type) {
		case float64:
			x -= v * dxscale
			needCharspace = true
		case []byte:
			for _, cid := range font.decode(v) {
				if needCharspace {
					x += charspace
				}
				x += it.renderChar(translateMatrix(mat, x, y), font, fontsize, scaling, rise, cid)
				if cid == 32 && wordspace != 0 {
					x += wordspace
				}
				needCharspace = true
			}
		}
	}
	ts.linematrix = [2]float64{x, y}
}

// renderChar creates the LTChar and returns its advance.
func (it *interp) renderChar(m matrix, font *pdfFont, fontsize, scaling, rise float64, cid int) float64 {
	text, ok := font.toUnichr(cid)
	if !ok {
		text = fmt.Sprintf("(cid:%d)", cid)
	}
	textwidth := font.charWidth(cid)
	adv := textwidth * fontsize * scaling
	descent := font.getDescent() * fontsize
	llx, lly := 0.0, descent+rise
	urx, ury := adv, descent+rise+fontsize
	a, b, c, d := m[0], m[1], m[2], m[3]
	upright := 0 < a*d*scaling && b*c <= 0
	// apply_matrix_rect: bound of all four transformed corners.
	cx := [4]float64{}
	cy := [4]float64{}
	cx[0], cy[0] = applyPt(m, llx, lly)
	cx[1], cy[1] = applyPt(m, urx, lly)
	cx[2], cy[2] = applyPt(m, urx, ury)
	cx[3], cy[3] = applyPt(m, llx, ury)
	x0, x1 := math.Min(math.Min(cx[0], cx[1]), math.Min(cx[2], cx[3])), math.Max(math.Max(cx[0], cx[1]), math.Max(cx[2], cx[3]))
	y0, y1 := math.Min(math.Min(cy[0], cy[1]), math.Min(cy[2], cy[3])), math.Max(math.Max(cy[0], cy[1]), math.Max(cy[2], cy[3]))
	ch := Char{
		Text: text, FontName: font.name, Upright: upright, Adv: adv,
		X0: x0, X1: x1, Top: it.top(y1), Bottom: it.top(y0),
		Size: y1 - y0,
	}
	if it.page.mbX0 != 0 {
		ch.X0 += it.page.mbX0
		ch.X1 += it.page.mbX0
	}
	it.page.Chars = append(it.page.Chars, ch)
	return adv
}

// paint is PDFLayoutAnalyzer.paint_path.
func (it *interp) paint(stroke, fill bool) {
	path := it.path
	it.path = nil
	it.paintPath(path, stroke, fill)
}

func (it *interp) paintPath(path []pathSeg, stroke, fill bool) {
	if len(path) == 0 || path[0].op != 'm' {
		return
	}
	var shape []byte
	for _, s := range path {
		shape = append(shape, s.op)
	}
	if bytes.Count(shape, []byte{'m'}) > 1 {
		start := 0
		for i := 1; i <= len(path); i++ {
			if i == len(path) || path[i].op == 'm' {
				it.paintPath(path[start:i], stroke, fill)
				start = i
			}
		}
		return
	}
	// Each segment's position is its last operand pair ('h' repeats the start).
	pts := make([][2]float64, len(path))
	for i, s := range path {
		var x, y float64
		if s.op == 'h' {
			x, y = path[0].pts[0][0], path[0].pts[0][1]
		} else {
			last := s.pts[len(s.pts)-1]
			x, y = last[0], last[1]
		}
		tx, ty := applyPt(it.ctm, x, y)
		pts[i] = [2]float64{tx, ty}
	}
	sh := string(shape)
	// Drop a redundant "l" on a path closed with "h".
	if len(sh) > 3 && sh[len(sh)-2:] == "lh" && pts[len(pts)-2] == pts[0] {
		sh = sh[:len(sh)-2] + "h"
		pts = pts[:len(pts)-1]
	}
	conv := make([][2]float64, len(pts))
	minX, maxX, minY, maxY := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
	for i, p := range pts {
		conv[i] = it.pt(p[0], p[1])
		minX, maxX = math.Min(minX, p[0]), math.Max(maxX, p[0])
		minY, maxY = math.Min(minY, p[1]), math.Max(maxY, p[1])
	}
	x0, x1 := minX, maxX
	if it.page.mbX0 != 0 {
		x0 += it.page.mbX0
		x1 += it.page.mbX0
	}
	top, bottom := it.top(maxY), it.top(minY)

	switch sh {
	case "mlh", "ml":
		it.page.Lines = append(it.page.Lines, Line{W: maxX - minX, H: maxY - minY, X0: x0, X1: x1, Top: top, Bottom: bottom,
			LineWidth: it.linewidth, Pts: [][2]float64{conv[0], conv[1]}})
	case "mlllh", "mllll":
		p := pts
		closed := p[0] == p[4]
		square := (p[0][0] == p[1][0] && p[1][1] == p[2][1] && p[2][0] == p[3][0] && p[3][1] == p[0][1]) ||
			(p[0][1] == p[1][1] && p[1][0] == p[2][0] && p[2][1] == p[3][1] && p[3][0] == p[0][0])
		if closed && square {
			// LTRect: pts are the four corners of the (x0,y0)-(x1,y1) box.
			rx0, ry0, rx1, ry1 := p[0][0], p[0][1], p[2][0], p[2][1]
			corners := [][2]float64{{rx0, ry0}, {rx1, ry0}, {rx1, ry1}, {rx0, ry1}}
			cp := make([][2]float64, 4)
			for i, c := range corners {
				cp[i] = it.pt(c[0], c[1])
			}
			it.page.Rects = append(it.page.Rects, Rect{W: maxX - minX, H: maxY - minY, X0: x0, X1: x1, Top: top, Bottom: bottom,
				LineWidth: it.linewidth, Stroke: stroke, Fill: fill, Pts: cp})
			return
		}
		fallthrough
	default:
		it.page.Curves = append(it.page.Curves, Curve{W: maxX - minX, H: maxY - minY, X0: x0, X1: x1, Top: top, Bottom: bottom,
			LineWidth: it.linewidth, Pts: conv})
	}
}

func (it *interp) doXObject(id string) {
	if it.depth > 12 {
		return
	}
	x := it.xobjects.Key(id)
	if x.Kind() != pdf.Stream {
		return
	}
	switch x.Key("Subtype").Name() {
	case "Form":
		if x.Key("BBox").IsNull() {
			return
		}
		m := identity
		if mm := x.Key("Matrix").Floats(); len(mm) == 6 {
			m = matrix{mm[0], mm[1], mm[2], mm[3], mm[4], mm[5]}
		}
		sub := &interp{doc: it.doc, page: it.page, ctm: multMatrix(m, it.ctm), depth: it.depth + 1}
		sub.resetState()
		res := x.Key("Resources")
		if res.IsNull() {
			res = it.resources
		}
		sub.setResources(res)
		data, err := x.Bytes()
		if err != nil {
			return
		}
		sub.run(data)
	case "Image":
		if x.Key("Width").IsNull() || x.Key("Height").IsNull() {
			return
		}
		it.addImage(id)
	}
}

// addImage places a unit-square image through the CTM (LTFigure bbox).
func (it *interp) addImage(name string) {
	xs := make([]float64, 0, 4)
	ys := make([]float64, 0, 4)
	for _, c := range [][2]float64{{0, 0}, {1, 0}, {0, 1}, {1, 1}} {
		x, y := applyPt(it.ctm, c[0], c[1])
		xs = append(xs, x)
		ys = append(ys, y)
	}
	x0, x1 := math.Min(math.Min(xs[0], xs[1]), math.Min(xs[2], xs[3])), math.Max(math.Max(xs[0], xs[1]), math.Max(xs[2], xs[3]))
	y0, y1 := math.Min(math.Min(ys[0], ys[1]), math.Min(ys[2], ys[3])), math.Max(math.Max(ys[0], ys[1]), math.Max(ys[2], ys[3]))
	if it.page.mbX0 != 0 {
		x0 += it.page.mbX0
		x1 += it.page.mbX0
	}
	it.page.Images = append(it.page.Images, Image{X0: x0, X1: x1, Top: it.top(y1), Bottom: it.top(y0), Name: name})
}

// inlineImage skips a BI ... ID <data> EI block, recording the image.
func (it *interp) inlineImage(l *lexer) {
	// dictionary entries up to ID
	for {
		t := l.next()
		if t.kind == tEOF {
			return
		}
		if t.kind == tOp && string(t.str) == "ID" {
			break
		}
	}
	// one whitespace byte follows ID, then the data until whitespace + EI + delimiter
	if l.pos < len(l.data) && isSpace(l.data[l.pos]) {
		l.pos++
	}
	for i := l.pos; i+1 < len(l.data); i++ {
		if l.data[i] == 'E' && l.data[i+1] == 'I' && (i == 0 || isSpace(l.data[i-1])) &&
			(i+2 >= len(l.data) || isSpace(l.data[i+2])) {
			l.pos = i + 2
			it.addImage("inline")
			return
		}
	}
	l.pos = len(l.data)
}

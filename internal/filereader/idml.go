package filereader

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Reads InDesign Markup Language packages (.idml) - what InDesign writes with
// File > Save As / Export > InDesign Markup. An IDML file is a zip of XML: the
// spreads hold the pages and the frames on them (with their exact positions),
// the stories hold the text, and the styles fill in whatever a piece of text
// doesn't set itself.
//
// InDesign's own .indd format is undocumented and isn't read directly.

// PageNumberMark stands in for an automatic page number; the page puts the
// real number in.
const PageNumberMark = ""

var frameTags = map[string]bool{"TextFrame": true, "Rectangle": true, "Oval": true, "Polygon": true,
	"GraphicLine": true, "Group": true}

// --- geometry ---------------------------------------------------------------

type matrix [6]float64

var identity = matrix{1, 0, 0, 1, 0, 0}

func pyFloat(s string) (float64, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f, err == nil
}

// parseMatrix reads an InDesign "a b c d tx ty" transform.
func parseMatrix(text string, ok bool) matrix {
	if !ok || text == "" {
		text = "1 0 0 1 0 0"
	}
	fields := strings.Fields(text)
	if len(fields) != 6 {
		return identity
	}
	var m matrix
	for i, f := range fields {
		v, ok := pyFloat(f)
		if !ok {
			return identity
		}
		m[i] = v
	}
	return m
}

// mul is m applied after n.
func mul(m, n matrix) matrix {
	a, b, c, d, e, f := n[0], n[1], n[2], n[3], n[4], n[5]
	A, B, C, D, E, F := m[0], m[1], m[2], m[3], m[4], m[5]
	return matrix{a*A + b*C, a*B + b*D, c*A + d*C, c*B + d*D, e*A + f*C + E, e*B + f*D + F}
}

func invert(m matrix) matrix {
	a, b, c, d, e, f := m[0], m[1], m[2], m[3], m[4], m[5]
	det := a*d - b*c
	if det == 0 {
		det = 1e-9
	}
	return matrix{d / det, -b / det, -c / det, a / det, (c*f - d*e) / det, (b*e - a*f) / det}
}

func apply(m matrix, x, y float64) (float64, float64) {
	return m[0]*x + m[2]*y + m[4], m[1]*x + m[3]*y + m[5]
}

type rawPath struct {
	pts    []pt
	closed bool
}

// pathPointsOf is the sub-paths of an item, in the item's own coordinates.
func pathPointsOf(el *xnode) []rawPath {
	var paths []rawPath
	for _, gpt := range el.iter("GeometryPathType") {
		var pts []pt
		for _, pp := range gpt.iter("PathPointType") {
			f := strings.Fields(pp.attr("Anchor"))
			if len(f) != 2 {
				continue
			}
			x, ok1 := pyFloat(f[0])
			y, ok2 := pyFloat(f[1])
			if !ok1 || !ok2 {
				continue
			}
			pts = append(pts, pt{x, y})
		}
		if len(pts) > 0 {
			paths = append(paths, rawPath{pts, gpt.attr("PathOpen") == "false"})
		}
	}
	return paths
}

// --- colours and styles -----------------------------------------------------

type resources struct {
	colors     map[string]string
	paraStyles map[string]*xnode
	charStyles map[string]*xnode
}

func newResources(zr *zip.Reader) (*resources, error) {
	res := &resources{colors: map[string]string{}, paraStyles: map[string]*xnode{}, charStyles: map[string]*xnode{}}
	for _, f := range zr.File {
		switch {
		case strings.HasPrefix(f.Name, "Resources/Graphic"):
			root, err := parseZipXML(f)
			if err != nil {
				return nil, err
			}
			res.readColors(root)
		case strings.HasPrefix(f.Name, "Resources/Styles"):
			root, err := parseZipXML(f)
			if err != nil {
				return nil, err
			}
			for _, el := range root.iter("ParagraphStyle") {
				res.paraStyles[el.attr("Self")] = el
			}
			for _, el := range root.iter("CharacterStyle") {
				res.charStyles[el.attr("Self")] = el
			}
		}
	}
	return res, nil
}

func parseZipXML(f *zip.File) (*xnode, error) {
	data, err := readZipFile(f)
	if err != nil {
		return nil, err
	}
	return parseXML(data)
}

func readZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func (r *resources) readColors(root *xnode) {
	for _, el := range root.iter("Color") {
		var values []float64
		for _, v := range strings.Fields(el.attr("ColorValue")) {
			if f, ok := pyFloat(v); ok {
				values = append(values, f)
			}
		}
		space := "CMYK"
		if s, ok := el.get("Space"); ok {
			space = s
		}
		var rgb []float64
		if space == "RGB" && len(values) == 3 {
			rgb = values
		} else if space == "CMYK" && len(values) == 4 {
			c, m, y, k := values[0]/100, values[1]/100, values[2]/100, values[3]/100
			rgb = []float64{255 * (1 - c) * (1 - k), 255 * (1 - m) * (1 - k), 255 * (1 - y) * (1 - k)}
		}
		if rgb != nil {
			ch := func(v float64) int { return int(math.Max(0, math.Min(255, math.RoundToEven(v)))) }
			r.colors[el.attr("Self")] = fmt.Sprintf("#%02x%02x%02x", ch(rgb[0]), ch(rgb[1]), ch(rgb[2]))
		}
	}
	if _, ok := r.colors["Color/Black"]; !ok {
		r.colors["Color/Black"] = "#000000"
	}
	if _, ok := r.colors["Color/Paper"]; !ok {
		r.colors["Color/Paper"] = "#ffffff"
	}
}

func (r *resources) color(ref string, def string) string {
	if ref == "" || strings.HasSuffix(ref, "/None") || ref == "n" {
		return def
	}
	if c, ok := r.colors[ref]; ok {
		return c
	}
	return def
}

// styleChain is the style named ref and every style it's based on, nearest first.
func styleChain(styles map[string]*xnode, ref string) []*xnode {
	var chain []*xnode
	seen := map[string]bool{}
	for ref != "" && styles[ref] != nil && !seen[ref] {
		seen[ref] = true
		el := styles[ref]
		chain = append(chain, el)
		based := el.find("Properties/BasedOn")
		if based != nil && based.Text != "" {
			ref = based.Text
		} else {
			ref = ""
		}
	}
	return chain
}

// lookup finds a text attribute across a run, its character style, its
// paragraph and its paragraph style, in that order.
func lookup(elements []*xnode, attr, prop string) (string, bool) {
	for _, el := range elements {
		if el == nil {
			continue
		}
		if v, ok := el.Attr[attr]; ok {
			return v, true
		}
		if prop != "" {
			if p := el.find("Properties/" + prop); p != nil && p.Text != "" {
				return p.Text, true
			}
		}
	}
	return "", false
}

func floatOr(value string, ok bool, def float64) float64 {
	if !ok {
		return def
	}
	if f, ok := pyFloat(value); ok {
		return f
	}
	return def
}

var alignMap = map[string]string{"LeftAlign": "left", "CenterAlign": "center", "RightAlign": "right",
	"LeftJustified": "justify", "CenterJustified": "justify", "RightJustified": "justify",
	"FullyJustified": "justify", "ToBindingSide": "left", "AwayFromBindingSide": "right"}

// --- stories ----------------------------------------------------------------

type storyReader struct{ res *resources }

func (s *storyReader) read(story *xnode) []Block {
	blocks := []Block{}
	s.walk(story, &blocks)
	return blocks
}

func (s *storyReader) walk(parent *xnode, blocks *[]Block) {
	for _, psr := range parent.Children {
		switch psr.Tag {
		case "ParagraphStyleRange":
			s.paragraphRange(psr, blocks)
		case "XMLElement", "Story", "Note":
			s.walk(psr, blocks)
		}
	}
}

func (s *storyReader) newParagraph(pctx []*xnode) Block {
	b := NewParagraph()
	just, _ := lookup(pctx, "Justification", "")
	if a, ok := alignMap[just]; ok {
		b.Align = a
	}
	f := func(attr string) float64 { v, ok := lookup(pctx, attr, ""); return floatOr(v, ok, 0) }
	b.Indent, b.Left, b.Right = f("FirstLineIndent"), f("LeftIndent"), f("RightIndent")
	b.Before, b.After = f("SpaceBefore"), f("SpaceAfter")
	if leading, ok := lookup(pctx, "Leading", "Leading"); ok && leading != "" && leading != "Auto" {
		if v, ok := pyFloat(leading); ok {
			b.Leading = &v
		}
	}
	return b
}

func (s *storyReader) paragraphRange(psr *xnode, blocks *[]Block) {
	pctx := append([]*xnode{psr}, styleChain(s.res.paraStyles, psr.attr("AppliedParagraphStyle"))...)
	para := s.newParagraph(pctx)
	for _, csr := range psr.Children {
		switch csr.Tag {
		case "CharacterStyleRange":
			para = s.charRange(csr, pctx, para, blocks)
		case "XMLElement":
			for _, inner := range csr.iter("CharacterStyleRange") {
				para = s.charRange(inner, pctx, para, blocks)
			}
		}
	}
	if len(para.Spans) > 0 {
		*blocks = append(*blocks, para)
	}
}

func (s *storyReader) charRange(csr *xnode, pctx []*xnode, para Block, blocks *[]Block) Block {
	cstyle := styleChain(s.res.charStyles, csr.attr("AppliedCharacterStyle"))
	ctx := append(append([]*xnode{csr}, cstyle...), pctx...)
	font, _ := lookup(ctx, "AppliedFont", "AppliedFont")
	fontStyle, _ := lookup(ctx, "FontStyle", "")
	fs := strings.ToLower(fontStyle)
	sizeV, sizeOK := lookup(ctx, "PointSize", "")
	underline, _ := lookup(ctx, "Underline", "")
	fillV, _ := lookup(ctx, "FillColor", "")
	size := floatOr(sizeV, sizeOK, 12.0)
	bold := strings.Contains(fs, "bold") || strings.Contains(fs, "black")
	italic := strings.Contains(fs, "italic") || strings.Contains(fs, "oblique")
	color := s.res.color(fillV, "#000000")
	mkSpan := func(text string) Span {
		return NewSpan(text, font, size, bold, italic, underline == "true", color, nil, true)
	}

	var buf strings.Builder
	flush := func() {
		if buf.Len() > 0 {
			para.Spans = append(para.Spans, mkSpan(buf.String()))
			buf.Reset()
		}
	}
	for _, child := range csr.Children {
		switch child.Tag {
		case "Content":
			buf.WriteString(child.Text)
		case "Br":
			flush()
			if len(para.Spans) == 0 {
				para.Spans = append(para.Spans, mkSpan("")) // an empty line keeps its height
			}
			*blocks = append(*blocks, para)
			para = s.newParagraph(pctx)
		case "Table":
			flush()
			if len(para.Spans) > 0 {
				*blocks = append(*blocks, para)
				para = s.newParagraph(pctx)
			}
			*blocks = append(*blocks, s.table(child))
		}
	}
	flush()
	return para
}

func (s *storyReader) table(table *xnode) Block {
	var cols []float64
	for _, c := range table.findall("Column") {
		v, ok := c.get("SingleColumnWidth")
		cols = append(cols, floatOr(v, ok, 72.0))
	}
	rowsN := len(table.findall("Row"))
	if rowsN == 0 {
		bv, bok := table.get("BodyRowCount")
		hv, hok := table.get("HeaderRowCount")
		rowsN = int(floatOr(bv, bok, 0) + floatOr(hv, hok, 0))
	}
	rows := make([]Row, rowsN)
	for _, cell := range table.findall("Cell") {
		name, ok := cell.get("Name")
		if !ok {
			name = "0:0"
		}
		parts := strings.Split(name, ":")
		if len(parts) != 2 {
			continue
		}
		c, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
		r, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err1 != nil || err2 != nil {
			continue
		}
		if r >= len(rows) {
			continue
		}
		if r < 0 {
			r += len(rows)
			if r < 0 {
				panic(fmt.Errorf("list index out of range"))
			}
		}
		blocks := []Block{}
		s.walk(cell, &blocks)
		csV, csOK := cell.get("ColumnSpan")
		rsV, rsOK := cell.get("RowSpan")
		raV, raOK := cell.get("RotationAngle")
		rot := math.Mod(-floatOr(raV, raOK, 0), 360)
		if rot < 0 {
			rot += 360
		}
		rot += 0
		rows[r].Cells = append(rows[r].Cells, Cell{
			Col: c, Blocks: blocks, Colspan: int(floatOr(csV, csOK, 1)), Rowspan: int(floatOr(rsV, rsOK, 1)),
			// InDesign turns cell text anticlockwise; the model clockwise.
			Rotate: rot,
		})
	}
	return Block{Kind: "table", Cols: cols, Rows: rows}
}

var aceRe = regexp.MustCompile(`<\?ACE \d+\?>`)

// stripPIs: special characters are processing instructions in IDML: <?ACE 7?>
// is a tab-like "indent here", <?ACE 8?> a right-indent tab, <?ACE 18?> the
// page number. Turn the ones that carry text into text first, because
// XML parsers drop processing instructions.
func stripPIs(data []byte) []byte {
	text := string(data)
	text = strings.ReplaceAll(text, "<?ACE 8?>", "\t")
	text = strings.ReplaceAll(text, "<?ACE 18?>", PageNumberMark)
	return []byte(aceRe.ReplaceAllString(text, ""))
}

func readStoryXML(f *zip.File) (*xnode, error) {
	data, err := readZipFile(f)
	if err != nil {
		return nil, err
	}
	root, err := parseXML(stripPIs(data))
	if err != nil {
		return nil, err
	}
	if root.Tag != "Story" {
		return root.find("Story"), nil
	}
	return root, nil
}

// --- spreads ----------------------------------------------------------------

func boundsOf(points []pt) (x0, y0, x1, y1 float64) {
	x0, y0, x1, y1 = points[0].x, points[0].y, points[0].x, points[0].y
	for _, p := range points[1:] {
		x0, y0 = math.Min(x0, p.x), math.Min(y0, p.y)
		x1, y1 = math.Max(x1, p.x), math.Max(y1, p.y)
	}
	return
}

type idmlPage struct {
	el            *xnode
	matrix        matrix
	left, top     float64
	width, height float64
	inverse       matrix
	items         []*Item
	masterItems   []*Item
}

func newIDMLPage(el *xnode, spread matrix) *idmlPage {
	v, ok := el.get("ItemTransform")
	p := &idmlPage{el: el, matrix: mul(spread, parseMatrix(v, ok))}
	bounds := "0 0 842 595"
	if b, ok := el.get("GeometricBounds"); ok {
		bounds = b
	}
	f := strings.Fields(bounds)
	if len(f) != 4 {
		panic(fmt.Errorf("bad GeometricBounds %q", bounds))
	}
	var n [4]float64
	for i := range n {
		x, ok := pyFloat(f[i])
		if !ok {
			panic(fmt.Errorf("bad GeometricBounds %q", bounds))
		}
		n[i] = x
	}
	top, left, bottom, right := n[0], n[1], n[2], n[3]
	p.left, p.top = left, top
	p.width, p.height = right-left, bottom-top
	p.inverse = invert(p.matrix)
	return p
}

// toPage is the matrix that maps an item's own coordinates to this page's.
func (p *idmlPage) toPage(m matrix) matrix {
	shift := matrix{1, 0, 0, 1, -p.left, -p.top}
	return mul(shift, mul(p.inverse, m))
}

func (p *idmlPage) contains(sx, sy float64) bool {
	x, y := apply(p.inverse, sx, sy)
	return p.left-1 <= x && x <= p.left+p.width+1 && p.top-1 <= y && y <= p.top+p.height+1
}

func shapeItems(el *xnode, m matrix, res *resources) []*Item {
	var out []*Item
	stroke := res.color(el.attr("StrokeColor"), "none")
	def := 0.0
	if stroke != "none" {
		def = 1.0
	}
	wv, wok := el.get("StrokeWeight")
	weight := floatOr(wv, wok, def)
	if weight <= 0 {
		stroke = "none"
	}
	fill := res.color(el.attr("FillColor"), "none")
	if stroke == "none" && fill == "none" {
		return out
	}
	for _, rp := range pathPointsOf(el) {
		tp := make([]pt, len(rp.pts))
		for i, p := range rp.pts {
			x, y := apply(m, p.x, p.y)
			tp[i] = pt{x, y}
		}
		var d string
		if el.Tag == "Oval" && len(tp) >= 4 {
			x0, y0, x1, y1 := boundsOf(tp)
			rx, ry := (x1-x0)/2, (y1-y0)/2
			d = fmt.Sprintf("M%.2f %.2f a%.2f %.2f 0 1 0 %.2f 0 a%.2f %.2f 0 1 0 %.2f 0",
				x0, y0+ry, rx, ry, 2*rx, rx, ry, -2*rx)
		} else {
			var sb strings.Builder
			for i, p := range tp {
				cmd := "L"
				if i == 0 {
					cmd = "M"
				} else {
					sb.WriteString(" ")
				}
				fmt.Fprintf(&sb, "%s%.2f %.2f", cmd, p.x, p.y)
			}
			d = sb.String()
			if rp.closed {
				d += " Z"
			}
		}
		out = append(out, &Item{Type: "path", D: d, Fill: fill, Stroke: stroke, SW: pyRound(weight, 2)})
	}
	return out
}

var radToDeg = func() float64 { pi := math.Pi; return 180.0 / pi }()

// frameBox is the position, size and rotation of a frame on its page.
func frameBox(el *xnode, m matrix) (Item, bool) {
	paths := pathPointsOf(el)
	if len(paths) == 0 {
		return Item{}, false
	}
	var all []pt
	for _, p := range paths {
		all = append(all, p.pts...)
	}
	x0, y0, x1, y1 := boundsOf(all)
	ox, oy := apply(m, x0, y0)
	a, b, c, d := m[0], m[1], m[2], m[3]
	sx, sy := math.Hypot(a, b), math.Hypot(c, d)
	return Item{X: ox, Y: oy, W: (x1 - x0) * sx, H: (y1 - y0) * sy, Rotate: math.Atan2(b, a) * radToDeg,
		Origin: "top left"}, true
}

type spreadReader struct{ res *resources }

// collect walks the page items under el, adding each to the page it sits on.
func (r *spreadReader) collect(el *xnode, m matrix, pages []*idmlPage) {
	for _, child := range el.Children {
		if !frameTags[child.Tag] {
			continue
		}
		if child.attr("Visible") == "false" {
			continue
		}
		v, ok := child.get("ItemTransform")
		cm := mul(m, parseMatrix(v, ok))
		if child.Tag == "Group" {
			r.collect(child, cm, pages)
			continue
		}
		page := r.pageFor(child, cm, pages)
		if page == nil {
			continue
		}
		pm := page.toPage(cm)
		if child.Tag == "TextFrame" {
			r.textFrame(child, pm, page)
		} else {
			page.items = append(page.items, shapeItems(child, pm, r.res)...)
			r.graphic(child, pm, page)
			// Frames pasted inside another frame are page items too.
			r.collect(child, cm, pages)
		}
	}
}

func (r *spreadReader) pageFor(el *xnode, m matrix, pages []*idmlPage) *idmlPage {
	paths := pathPointsOf(el)
	if len(paths) == 0 {
		return nil
	}
	var all []pt
	for _, p := range paths {
		all = append(all, p.pts...)
	}
	x0, y0, x1, y1 := boundsOf(all)
	cx, cy := apply(m, (x0+x1)/2, (y0+y1)/2)
	for _, page := range pages {
		if page.contains(cx, cy) {
			return page
		}
	}
	return nil
}

func (r *spreadReader) textFrame(el *xnode, m matrix, page *idmlPage) {
	box, ok := frameBox(el, m)
	story := el.attr("ParentStory")
	if !ok || story == "" {
		return
	}
	pref := el.find("TextFramePreference")
	inset := [4]float64{}
	valign := "top"
	if pref != nil {
		raw, hasRaw := pref.get("InsetSpacing")
		if !hasRaw {
			if ins := pref.find("Properties/InsetSpacing"); ins != nil {
				if len(ins.Children) > 0 {
					var parts []string
					for _, v := range ins.Children {
						t := v.Text
						if t == "" {
							t = "0"
						}
						parts = append(parts, t)
					}
					raw, hasRaw = strings.Join(parts, " "), true
				} else {
					raw, hasRaw = ins.Text, ins.Text != ""
				}
			}
		}
		if hasRaw && raw != "" {
			var vals []float64
			for _, v := range strings.Fields(raw) {
				f, _ := pyFloat(v)
				vals = append(vals, f)
			}
			if len(vals) == 1 {
				inset = [4]float64{vals[0], vals[0], vals[0], vals[0]}
			} else {
				for i := 0; i < 4 && i < len(vals); i++ {
					inset[i] = vals[i]
				}
			}
		}
		switch pref.attr("VerticalJustification") {
		case "CenterAlign":
			valign = "center"
		case "BottomAlign":
			valign = "bottom"
		}
	}
	item := box
	item.Type, item.Story, item.Inset, item.Valign, item.Nowrap = "text", story, inset, valign, false
	page.items = append(page.items, &item)
}

func (r *spreadReader) graphic(el *xnode, m matrix, page *idmlPage) {
	var image *xnode
	for _, t := range []string{"Image", "PDF", "EPS", "ImportedPage"} {
		if g := el.find(t); g != nil {
			image = g
			break
		}
	}
	if image == nil {
		return
	}
	box, ok := frameBox(el, m)
	if !ok {
		return
	}
	name := ""
	if link := image.find("Link"); link != nil {
		uri := link.attr("LinkResourceURI")
		name = uri[strings.LastIndex(uri, "/")+1:]
	}
	if name == "" {
		name = "Image"
	}
	item := box
	item.Type, item.Label = "image", name
	page.items = append(page.items, &item)
}

// ReadIDML reads an InDesign IDML package.
func ReadIDML(data []byte) (doc *Doc, err error) {
	notIDML := readerErr("This doesn't look like an IDML file. In InDesign, use File > Export and " +
		"choose \"InDesign Markup (IDML)\".")
	zr, zerr := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if zerr != nil {
		return nil, notIDML
	}
	var designFile *zip.File
	for _, f := range zr.File {
		if f.Name == "designmap.xml" {
			designFile = f
		}
	}
	if designFile == nil {
		return nil, notIDML
	}
	design, derr := parseZipXML(designFile)
	if derr != nil {
		return nil, notIDML
	}
	defer func() {
		if r := recover(); r != nil {
			doc, err = nil, fmt.Errorf("reading IDML: %v", r)
		}
	}()
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	res, rerr := newResources(zr)
	if rerr != nil {
		return nil, rerr
	}
	doc = NewDoc("InDesign (IDML)")

	parts := func(kind string) []string {
		var out []string
		for _, el := range design.Children {
			if localName(el.Tag) == kind && el.attr("src") != "" {
				out = append(out, el.attr("src"))
			}
		}
		return out
	}
	spreadFiles, masterFiles, storyFiles := parts("Spread"), parts("MasterSpread"), parts("Story")

	readRoot := func(name string) (*xnode, error) {
		f, ok := files[name]
		if !ok {
			return nil, fmt.Errorf("KeyError: %s", name)
		}
		data, err := readZipFile(f)
		if err != nil {
			return nil, err
		}
		return parseXML(stripPIs(data))
	}

	// Master pages, keyed by master spread id, each a list of idmlPage with
	// their items (letterheads, borders, page numbers).
	masters := map[string][]*idmlPage{}
	reader := &spreadReader{res: res}
	for _, name := range masterFiles {
		root, err := readRoot(name)
		if err != nil {
			return nil, err
		}
		ms := root.find("MasterSpread")
		if ms == nil {
			continue
		}
		v, ok := ms.get("ItemTransform")
		sm := parseMatrix(v, ok)
		var mpages []*idmlPage
		for _, p := range ms.findall("Page") {
			mpages = append(mpages, newIDMLPage(p, sm))
		}
		reader.collect(ms, sm, mpages)
		masters[ms.attr("Self")] = mpages
	}

	var pages []*idmlPage
	for _, name := range spreadFiles {
		root, err := readRoot(name)
		if err != nil {
			return nil, err
		}
		spread := root.find("Spread")
		if spread == nil {
			continue
		}
		v, ok := spread.get("ItemTransform")
		sm := parseMatrix(v, ok)
		var spages []*idmlPage
		for _, p := range spread.findall("Page") {
			spages = append(spages, newIDMLPage(p, sm))
		}
		reader.collect(spread, sm, spages)
		for _, page := range spages {
			mpages := masters[page.el.attr("AppliedMaster")]
			if len(mpages) > 0 {
				// Facing pages: a left-hand page (left of the spine, x < 0)
				// takes the master's left page, a right-hand one its right.
				lx, _ := apply(page.matrix, page.left, page.top)
				master := mpages[len(mpages)-1]
				if lx < -1 || len(mpages) == 1 {
					master = mpages[0]
				}
				// Master frames repeat on every page using it, so each copy
				// shows the frame's whole text instead of flowing on.
				for _, item := range master.items {
					c := *item
					c.Repeat = true
					page.masterItems = append(page.masterItems, &c)
				}
			}
			pages = append(pages, page)
		}
	}

	stories := NewStories()
	sr := &storyReader{res: res}
	for _, name := range storyFiles {
		f, ok := files[name]
		if !ok {
			continue
		}
		story, err := readStoryXML(f)
		if err != nil {
			continue
		}
		if story == nil {
			continue
		}
		stories.Set(story.attr("Self"), sr.read(story))
	}

	used := map[string]bool{}
	hasImage := false
	for _, page := range pages {
		items := append(append([]*Item(nil), page.masterItems...), page.items...)
		doc.Pages = append(doc.Pages, &Page{Width: page.width, Height: page.height, Items: items})
		for _, item := range items {
			if item.Type == "text" {
				used[item.Story] = true
			}
			if item.Type == "image" {
				hasImage = true
			}
		}
	}
	for _, sid := range stories.Keys() {
		if used[sid] {
			doc.Stories.Set(sid, stories.Get(sid))
		}
	}
	if len(doc.Pages) == 0 {
		return nil, readerErr("No pages were found in this IDML file.")
	}
	if hasImage {
		doc.Warnings = append(doc.Warnings, "Pictures are linked from outside the IDML file, so they're shown as boxes.")
	}
	return doc, nil
}

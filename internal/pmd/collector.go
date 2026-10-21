package pmd

import (
	"encoding/hex"
	"fmt"
	"math"
	"strings"
)

const em2pt = 11.95516799999881

// collector is PMDCollector: it gathers what the parser finds and then
// "draws" it into a list of recorded calls.
type collector struct {
	pageWidth, pageHeight unit
	hasPage               bool
	pages                 [][]*shape
	colors                []color
	fonts                 []font
	doubleSided           bool
	calls                 []Call
}

func (c *collector) addPage() int {
	c.pages = append(c.pages, nil)
	return len(c.pages) - 1
}

func (c *collector) addShape(pageID int, s *shape) {
	c.pages[pageID] = append(c.pages[pageID], s)
}

func (c *collector) call(m string, p PropList) {
	c.calls = append(c.calls, Call{M: m, P: p, HasP: p != nil})
}

func (c *collector) text(b []byte) {
	c.calls = append(c.calls, Call{M: "text", Hex: hex.EncodeToString(b)})
}

// --- output shapes -------------------------------------------------------------------

type outShape struct {
	closed                bool
	kind                  uint8
	points                []inchPoint
	rotation, skew        float64
	left, top, right, bot float64
	fill                  fillProps
	stroke                strokeProps
	text                  string
	chars                 []charProps
	paras                 []paraProps
	bitmap                []byte
	width, height         float64
	storyID               uint32
}

func (o *outShape) point(i int) inchPoint {
	if i < len(o.points) {
		return o.points[i]
	}
	return inchPoint{}
}

var errEmptyLineSet = fmt.Errorf("empty line set")

// boundingBox is OutputShape::getBoundingBox, which throws for a shape
// without points or bitmap.
func (o *outShape) boundingBox() (inchPoint, inchPoint) {
	if len(o.points) == 0 && len(o.bitmap) == 0 {
		panic(errEmptyLineSet)
	}
	return inchPoint{o.left, o.top}, inchPoint{o.right, o.bot}
}

func newOutputShape(s *shape, translate inchPoint) *outShape {
	o := &outShape{closed: s.isClosed(), kind: s.kind, rotation: s.rotation(), skew: s.skew()}
	tl, br := s.bboxTL, s.bboxBR
	o.left = tl.x.inches() + translate.x
	o.top = tl.y.inches() + translate.y
	o.right = br.x.inches() + translate.x
	o.bot = br.y.inches() + translate.y
	rot, skew := o.rotation, o.skew

	switch s.kind {
	case shapeTypeTextbox, shapeTypeBitmap:
		if s.kind == shapeTypeTextbox {
			o.text, o.chars, o.paras, o.storyID = s.text, s.charProperties(), s.paraProperties(), s.storyID
		} else {
			o.bitmap = s.bitmap
		}
		if rot == 0 && skew == 0 {
			o.points = append(o.points, inchPoint{tl.x.inches() + translate.x, tl.y.inches() + translate.y})
			o.width = math.Abs(br.x.inches() - tl.x.inches())
			o.height = math.Abs(br.y.inches() - tl.y.inches())
		} else {
			xtl, xbr := s.xformTopLeft(), s.xformBotRight()
			width := math.Abs(xbr.x.inches() - xtl.x.inches())
			height := math.Abs(xbr.y.inches() - xtl.y.inches())
			o.width, o.height = width, height
			rp := s.rotatingPoint()
			x := rp.x.inches() + translate.x
			y := rp.y.inches() + translate.y
			x += (width*math.Cos(rot) - height*math.Sin(rot) - width) / 2.0
			y += (width*math.Sin(rot) + height*math.Cos(rot) - height) / 2.0
			o.points = append(o.points, inchPoint{x, y})
		}
		return o
	}

	o.fill, o.stroke = s.fillProperties(), s.strokeProperties()
	pts := s.shapePoints()
	if s.kind == shapeTypeLine || s.kind == shapeTypePoly || s.kind == shapeTypeRect {
		if rot == 0 && skew == 0 {
			for _, p := range pts {
				o.points = append(o.points, inchPoint{p.x.inches() + translate.x, p.y.inches() + translate.y})
			}
			return o
		}
		xtl, xbr := s.xformTopLeft(), s.xformBotRight()
		width := math.Abs(xbr.x.inches() - xtl.x.inches())
		height := math.Abs(xbr.y.inches() - xtl.y.inches())
		if s.kind == shapeTypeRect {
			rp := s.rotatingPoint()
			x1 := rp.x.inches() + translate.x
			y1 := rp.y.inches() + translate.y
			x2 := x1 + width*math.Cos(rot)
			y2 := y1 + width*math.Sin(rot)
			x4 := x1 - height*math.Sin(rot)
			y4 := y1 + height*math.Cos(rot)
			x3 := x4 + width*math.Cos(rot)
			y3 := y4 + width*math.Sin(rot)
			x3 += height * math.Cos(rot) * math.Sin(skew) / math.Cos(skew)
			y3 += height * math.Sin(rot) * math.Sin(skew) / math.Cos(skew)
			x4 += height * math.Cos(rot) * math.Sin(skew) / math.Cos(skew)
			y4 += height * math.Sin(rot) * math.Sin(skew) / math.Cos(skew)
			o.points = append(o.points, inchPoint{x1, y1}, inchPoint{x2, y2}, inchPoint{x3, y3}, inchPoint{x4, y4})
		} else {
			tx := (br.x.inches()+tl.x.inches())/2 + translate.x
			ty := (br.y.inches()+tl.y.inches())/2 + translate.y
			for _, p := range pts {
				temp := p.x.inches() + math.Tan(skew)*p.y.inches()
				x := temp*math.Cos(rot) - p.y.inches()*math.Sin(rot) + tx
				y := temp*math.Sin(rot) + p.y.inches()*math.Cos(rot) + ty
				o.points = append(o.points, inchPoint{x, y})
			}
		}
		return o
	}

	// ellipse
	cx := (pts[0].x.inches()+pts[1].x.inches())/2 + translate.x
	cy := (pts[0].y.inches()+pts[1].y.inches())/2 + translate.y
	var rx, ry float64
	if rot == 0 && skew == 0 {
		rx = math.Abs(pts[1].x.inches()-pts[0].x.inches()) / 2
		ry = math.Abs(pts[1].y.inches()-pts[0].y.inches()) / 2
	} else {
		xtl, xbr := s.xformTopLeft(), s.xformBotRight()
		rx = math.Abs(xbr.x.inches()-xtl.x.inches()) / 2
		ry = math.Abs(xbr.y.inches()-xtl.y.inches()) / 2
	}
	o.points = append(o.points, inchPoint{cx, cy}, inchPoint{rx, ry})
	return o
}

// --- painting ------------------------------------------------------------------------------

func hexColor(r, g, b uint16) string { return fmt.Sprintf("#%02x%02x%02x", r, g, b) }

func (c *collector) writeBorder(p PropList, name string, stroke strokeProps) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%fpt ", float64(stroke.strokeWidth)/5.0)
	switch stroke.strokeType {
	case strokeLightLight, strokeDarkLight, strokeLightDark, strokeLightDarkLight:
		sb.WriteString("double")
	case strokeDashed:
		sb.WriteString("dashed")
	case strokeSquareDots, strokeCircularDots:
		sb.WriteString("dotted")
	default:
		sb.WriteString("solid")
	}
	sb.WriteString(" ")
	if int(stroke.strokeColor) < len(c.colors) {
		col := c.colors[stroke.strokeColor]
		sb.WriteString(hexColor(col.red, col.green, col.blue))
	} else {
		sb.WriteString("#000000")
	}
	p.str(name, sb.String())
}

// fillAndStroke adds the properties polygons, polylines and paths share.
func (c *collector) fillAndStroke(p PropList, o *outShape) {
	switch o.fill.fillType {
	case fillSolid:
		p.str("draw:fill", "solid")
	default:
		p.str("draw:fill", "none")
	}
	if int(o.fill.fillColor) < len(c.colors) {
		col := c.colors[o.fill.fillColor]
		p.str("draw:fill-color", hexColor(col.red, col.green, col.blue))
	}
	if o.fill.fillColor == 0 {
		p.num("draw:opacity", 0)
	} else {
		p.num("draw:opacity", int(o.fill.fillTint))
	}
	switch o.stroke.strokeType {
	case strokeDashed:
		p.str("draw:stroke", "dash")
	default:
		p.str("draw:stroke", "solid")
	}
	p.pt("svg:stroke-width", float64(o.stroke.strokeWidth)/5.0)
	if int(o.stroke.strokeColor) < len(c.colors) {
		col := c.colors[o.stroke.strokeColor]
		p.str("svg:stroke-color", hexColor(col.red, col.green, col.blue))
	}
	p.pct("svg:stroke-opacity", float64(o.stroke.strokeTint)/100.0)
}

func (c *collector) flushText(cur *[]byte) {
	if len(*cur) > 0 {
		c.text(*cur)
		*cur = (*cur)[:0]
	}
}

func (c *collector) writeTextSpan(text string, charStart, charEnd int) {
	charEnd++
	if charEnd > len(text) {
		charEnd = len(text)
	}
	var cur []byte
	wasSpace := false
	for i := charStart; i < charEnd; i++ {
		ch := text[i]
		switch {
		case ch == '\t':
			c.flushText(&cur)
			c.calls = append(c.calls, Call{M: "text", Hex: "09"})
		case ch == '\r':
			c.flushText(&cur)
			c.calls = append(c.calls, Call{M: "text", Hex: "0a"})
		case ch == ' ':
			if wasSpace {
				c.flushText(&cur)
				c.calls = append(c.calls, Call{M: "text", Hex: "20"})
			} else {
				cur = append(cur, ch)
			}
		case ch < 0x20:
			// control characters without a known use in PageMaker are dropped
		case ch >= 0x80:
			cur = append(cur, 0xC0|(ch>>6), 0x80|(ch&0x3F))
		default:
			cur = append(cur, ch)
		}
		wasSpace = ch == ' '
	}
	c.flushText(&cur)
}

func (c *collector) paintShape(o *outShape) {
	switch {
	case o.kind == shapeTypeLine || o.kind == shapeTypePoly || o.kind == shapeTypeRect:
		p := PropList{}
		verts := make([]PropList, 0, len(o.points))
		for _, pt := range o.points {
			v := PropList{}
			v.inch("svg:x", pt.x)
			v.inch("svg:y", pt.y)
			verts = append(verts, v)
		}
		p.vec("svg:points", verts)
		c.fillAndStroke(p, o)
		if o.closed {
			c.call("drawPolygon", p)
		} else {
			c.call("drawPolyline", p)
		}
	case o.kind == shapeTypeTextbox:
		c.paintTextbox(o)
	case o.kind == shapeTypeBitmap:
		p := PropList{}
		p.inch("svg:x", o.point(0).x)
		p.inch("svg:y", o.point(0).y)
		p.inch("svg:width", o.width)
		p.inch("svg:height", o.height)
		if o.rotation != 0.0 {
			p.gen("librevenge:rotate", o.rotation*180/math.Pi)
		}
		p.str("librevenge:mime-type", "image/tiff")
		p.binary("office:binary-data", o.bitmap)
		c.call("drawGraphicObject", p)
	default:
		c.paintEllipse(o)
	}
}

func (c *collector) paintEllipse(o *outShape) {
	cx, cy := o.point(0).x, o.point(0).y
	rx, ry := o.point(1).x, o.point(1).y
	rotation := o.rotation
	sx := cx - rx*math.Cos(rotation)
	sy := cy - rx*math.Sin(rotation)
	ex := cx + rx*math.Cos(rotation)
	ey := cy + rx*math.Sin(rotation)

	node := func(action string) PropList {
		n := PropList{}
		n.str("librevenge:path-action", action)
		return n
	}
	arc := func(large bool, x, y float64) PropList {
		n := node("A")
		n.inch("svg:rx", rx)
		n.inch("svg:ry", ry)
		n.gen("librevenge:rotate", rotation*180/math.Pi)
		n.boolean("librevenge:large-arc", large)
		n.boolean("librevenge:sweep", false)
		n.inch("svg:x", x)
		n.inch("svg:y", y)
		return n
	}
	m := node("M")
	m.inch("svg:x", sx)
	m.inch("svg:y", sy)
	p := PropList{}
	p.vec("svg:d", []PropList{m, arc(false, ex, ey), arc(true, sx, sy), node("Z")})
	c.fillAndStroke(p, o)
	c.call("drawPath", p)
}

func (c *collector) paintTextbox(o *outShape) {
	tb := PropList{}
	tb.inch("svg:x", o.point(0).x)
	tb.inch("svg:y", o.point(0).y)
	tb.inch("svg:width", o.width)
	tb.inch("svg:height", o.height)
	tb.str("draw:stroke", "none")
	tb.str("draw:fill", "none")
	tb.inch("librevenge:rotate", o.rotation*180/math.Pi)
	tb.num("librevenge:story-id", int(int32(o.storyID)))
	c.call("startTextObject", tb)

	var paraStart, paraEnd uint16
	for _, pp := range o.paras {
		paraEnd = paraStart + pp.length - 1

		pr := PropList{}
		switch pp.align {
		case 1:
			pr.str("fo:text-align", "right")
		case 2:
			pr.str("fo:text-align", "center")
		case 3:
			pr.str("fo:text-align", "justify")
		case 4:
			pr.str("fo:text-align", "justify")
			pr.str("fo:text-align-last", "justify")
		default:
			pr.str("fo:text-align", "left")
		}
		if pp.afterIndent != 0 {
			pr.inch("fo:margin-bottom", float64(pp.afterIndent)/shapeUnitsPerInch)
		}
		if pp.beforeIndent != 0 {
			pr.inch("fo:margin-top", float64(pp.beforeIndent)/shapeUnitsPerInch)
		}
		if pp.firstIndent != 0 {
			pr.inch("fo:text-indent", float64(pp.firstIndent)/shapeUnitsPerInch)
		}
		if pp.leftIndent != 0 {
			pr.inch("fo:margin-left", float64(pp.leftIndent)/shapeUnitsPerInch)
		}
		if pp.rightIndent != 0 {
			pr.inch("fo:margin-right", float64(pp.rightIndent)/shapeUnitsPerInch)
		}
		pr.num("fo:orphans", int(int16(pp.orphans)))
		pr.num("fo:widows", int(int16(pp.widows)))
		if pp.keepTogether {
			pr.str("fo:keep-together", "always")
		} else {
			pr.str("fo:keep-together", "auto")
		}
		if pp.keepWithNext > 0 {
			pr.str("fo:keep-with-next", "always")
		} else {
			pr.str("fo:keep-with-next", "auto")
		}
		pr.boolean("fo:hyphenate", pp.hyphenate)
		if pp.hyphenate {
			if pp.hyphensCount > 0 {
				pr.num("fo:hyphenation-ladder-count", int(int16(pp.hyphensCount)))
			} else {
				pr.str("fo:hyphenation-ladder-count", "no-limit")
			}
		}
		if pp.ruleAbove != nil {
			c.writeBorder(pr, "fo:border-top", *pp.ruleAbove)
		}
		if pp.ruleBelow != nil {
			c.writeBorder(pr, "fo:border-bottom", *pp.ruleBelow)
		}
		c.call("openParagraph", pr)

		var charStart, charEnd uint16
		for _, cp := range o.chars {
			charEndTemp := charStart + cp.length - 1
			if paraStart > charStart {
				charStart = paraStart
			}
			if charEndTemp > paraEnd {
				charEnd = paraEnd
			} else {
				charEnd = charEndTemp
			}
			if charStart <= charEnd && paraStart <= charEndTemp {
				c.call("openSpan", c.spanProps(cp))
				c.writeTextSpan(o.text, int(charStart), int(charEnd))
				c.calls = append(c.calls, Call{M: "closeSpan"})
			}
			charStart = charEnd + 1
		}
		c.calls = append(c.calls, Call{M: "closeParagraph"})
		paraStart = paraEnd + 1
	}
	c.calls = append(c.calls, Call{M: "endTextObject"})
}

func (c *collector) spanProps(cp charProps) PropList {
	p := PropList{}
	p.pt("fo:font-size", float64(cp.fontSize)/10)
	if cp.leading != 0 {
		p.pt("fo:line-height", float64(cp.leading)/10)
	}
	if int(cp.fontFace) < len(c.fonts) {
		p.str("style:font-name", rvngString(c.fonts[cp.fontFace].name))
	}
	if int(cp.fontColor) < len(c.colors) {
		col := c.colors[cp.fontColor]
		tint := float64(cp.tint) / 100
		bg := (1 - tint) * 255
		p.str("fo:color", hexColor(
			uint16(int32(float64(col.red)*tint+bg)),
			uint16(int32(float64(col.green)*tint+bg)),
			uint16(int32(float64(col.blue)*tint+bg))))
	}
	if cp.bold {
		p.str("fo:font-weight", "bold")
	}
	if cp.italic {
		p.str("fo:font-style", "italic")
	}
	if cp.underline {
		p.str("style:text-underline-type", "single")
	}
	if cp.outline {
		p.boolean("style:text-outline", true)
	}
	if cp.shadow {
		p.str("fo:text-shadow", "1pt 1pt")
	}
	if cp.strike {
		p.str("style:text-line-through-style", "solid")
	}
	if cp.super || cp.sub {
		pos := int32(cp.superPos)
		if cp.sub {
			pos = -int32(cp.subPos)
		}
		p.str("style:text-position", fmt.Sprintf("%.1f%% %.1f%%", float64(pos)/10.0, float64(cp.superSubSize)/10.0))
	}
	if cp.smallCaps {
		p.str("fo:font-variant", "small-caps")
	}
	if cp.allCaps {
		p.str("fo:text-transform", "uppercase")
	}
	if cp.kerning != 0 {
		p.str("style:letter-kerning", "true")
		p.pt("fo:letter-spacing", (float64(cp.kerning)/1000)*em2pt)
	}
	return p
}

// --- pages -------------------------------------------------------------------------------------

func (c *collector) writePage(shapes []*outShape) {
	p := PropList{}
	if c.hasPage {
		p.inch("svg:width", c.pageWidth.inches())
		p.inch("svg:height", c.pageHeight.inches())
	}
	c.call("startPage", p)
	for _, s := range shapes {
		c.paintShape(s)
	}
	c.calls = append(c.calls, Call{M: "endPage"})
}

func (c *collector) shapesByPageTwoSided() [][]*outShape {
	n := len(c.pages)
	if n == 0 {
		panic(fmt.Errorf("length_error")) // 0 * 2 - 1 wraps around in the C++ vector size
	}
	out := make([][]*outShape, n*2-1)
	centerX := c.pageWidth.inches() / 2
	centerY := c.pageHeight.inches() / 2
	left := inchPoint{centerX * 2, centerY}
	right := inchPoint{0, centerY}
	for i, page := range c.pages {
		for _, s := range page {
			r := newOutputShape(s, right)
			if _, rbr := r.boundingBox(); rbr.x >= 0 {
				out[i] = append(out[i], r)
				continue
			}
			if i > 0 {
				l := newOutputShape(s, left)
				if ltl, _ := l.boundingBox(); ltl.x <= centerX*2 {
					out[i-1] = append(out[i-1], l)
				}
			}
		}
	}
	if len(out) > 1 && len(out[len(out)-1]) == 0 {
		out = out[:len(out)-1]
	}
	return out
}

func (c *collector) shapesByPageOneSided() [][]*outShape {
	out := make([][]*outShape, len(c.pages))
	t := inchPoint{c.pageWidth.inches() / 2, c.pageHeight.inches() / 2}
	for i, page := range c.pages {
		for _, s := range page {
			out[i] = append(out[i], newOutputShape(s, t))
		}
	}
	return out
}

// draw is PMDCollector::draw; it reports false where the C++ throws.
func (c *collector) draw() (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			c.calls = nil
			ok = false
		}
	}()
	var byPage [][]*outShape
	if c.doubleSided {
		byPage = c.shapesByPageTwoSided()
	} else {
		byPage = c.shapesByPageOneSided()
	}
	for i := range c.pages {
		c.writePage(byPage[i])
	}
	return true
}

package filereader

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"fmt"
	"hash/crc32"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Writes a model document as an InDesign IDML file, which InDesign opens and
// can save as .indd.
//
// Every page keeps its text blocks, threads and rules where they were, in the
// original fonts (legacy Malayalam text is written in its own font, so read
// the document with convert=false). Tables drawn with lines are rebuilt as
// real InDesign tables (see tables.go).

const (
	idmlDOM    = "8.0"
	idmlPkg    = `xmlns:idPkg="http://ns.adobe.com/AdobeInDesign/idml/1.0/packaging"`
	idmlHeader = "<?xml version=\"1.0\" encoding=\"UTF-8\" standalone=\"yes\"?>\n"

	// Fixed ids. InDesign's own are "u" and a hex number, which these follow.
	idmlLayer   = "ub1"
	idmlSection = "ub2"
	idmlBacking = "ub3"
)

var idmlAlign = map[string]string{"left": "LeftAlign", "center": "CenterAlign", "right": "RightAlign", "justify": "LeftJustified"}

// num is a number as IDML writes it.
func num(v float64) string {
	v = pyRound(v, 3)
	if v == math.Trunc(v) {
		return strconv.FormatInt(int64(v), 10)
	}
	return pyRepr(v)
}

func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// quoteAttr is xml.sax.saxutils.quoteattr.
func quoteAttr(data string) string {
	data = xmlEscape(data)
	data = strings.NewReplacer("\n", "&#10;", "\r", "&#13;", "\t", "&#9;").Replace(data)
	if strings.Contains(data, "\"") {
		if strings.Contains(data, "'") {
			data = "\"" + strings.ReplaceAll(data, "\"", "&quot;") + "\""
		} else {
			data = "'" + data + "'"
		}
	} else {
		data = "\"" + data + "\""
	}
	return data
}

// attrs writes name=value pairs in order, leaving out nil values.
func attrs(kv ...any) string {
	var sb strings.Builder
	for i := 0; i+1 < len(kv); i += 2 {
		v := kv[i+1]
		if v == nil {
			continue
		}
		var s string
		switch x := v.(type) {
		case string:
			s = x
		case int:
			s = strconv.Itoa(x)
		case float64:
			s = pyRepr(x)
		default:
			s = fmt.Sprint(x)
		}
		sb.WriteString(" " + kv[i].(string) + "=" + quoteAttr(s))
	}
	return sb.String()
}

var hexColorRe = regexp.MustCompile(`^#([0-9a-fA-F]{6})$`)

type idmlColors struct{ extra map[string][3]int }

func (c *idmlColors) ref(value string) string {
	if value == "" || value == "none" {
		return "Swatch/None"
	}
	m := hexColorRe.FindStringSubmatch(value)
	if m == nil {
		return "Color/Black"
	}
	h := strings.ToLower(m[1])
	if h == "000000" {
		return "Color/Black"
	}
	if h == "ffffff" {
		return "Color/Paper"
	}
	name := "Color/R" + h
	var rgb [3]int
	for i := 0; i < 3; i++ {
		v, _ := strconv.ParseInt(h[i*2:i*2+2], 16, 32)
		rgb[i] = int(v)
	}
	if c.extra == nil {
		c.extra = map[string][3]int{}
	}
	c.extra[name] = rgb
	return name
}

func (c *idmlColors) xml() string {
	out := []string{
		`<Color Self="Color/Black" Model="Process" Space="CMYK" ColorValue="0 0 0 100" ` +
			`ColorOverride="Specialblack" AlternateSpace="NoAlternateColor" AlternateColorValue="" ` +
			`Name="Black" ColorEditable="false" ColorRemovable="false" Visible="true" SwatchCreatorID="7937"/>`,
		`<Color Self="Color/Paper" Model="Process" Space="CMYK" ColorValue="0 0 0 0" ` +
			`ColorOverride="Specialpaper" AlternateSpace="NoAlternateColor" AlternateColorValue="" ` +
			`Name="Paper" ColorEditable="true" ColorRemovable="false" Visible="true" SwatchCreatorID="7937"/>`,
		`<Color Self="Color/Registration" Model="Registration" Space="CMYK" ColorValue="100 100 100 100" ` +
			`ColorOverride="Specialregistration" AlternateSpace="NoAlternateColor" AlternateColorValue="" ` +
			`Name="Registration" ColorEditable="false" ColorRemovable="false" Visible="true" ` +
			`SwatchCreatorID="7937"/>`,
	}
	names := make([]string, 0, len(c.extra))
	for n := range c.extra {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		rgb := c.extra[name]
		out = append(out, fmt.Sprintf(`<Color Self=%s Model="Process" Space="RGB" ColorValue="%d %d %d" ColorOverride="Normal" `+
			`AlternateSpace="NoAlternateColor" AlternateColorValue="" Name=%s ColorEditable="true" `+
			`ColorRemovable="true" Visible="true" SwatchCreatorID="7937"/>`,
			quoteAttr(name), rgb[0], rgb[1], rgb[2], quoteAttr(strings.SplitN(name, "/", 2)[1])))
	}
	for _, ink := range []struct {
		name    string
		angle   int
		density string
		order   int
	}{{"Cyan", 75, "0.61", 1}, {"Magenta", 15, "0.76", 2}, {"Yellow", 0, "0.16", 3}, {"Black", 45, "1.7", 4}} {
		out = append(out, fmt.Sprintf(`<Ink Self="Ink/$ID/Process %s" Name="$ID/Process %s" Angle="%d" ConvertToProcess="false" `+
			`Frequency="70" NeutralDensity="%s" PrintInk="true" TrapOrder="%d" InkType="Normal"/>`,
			ink.name, ink.name, ink.angle, ink.density, ink.order))
	}
	out = append(out, `<Swatch Self="Swatch/None" Name="None" ColorEditable="false" ColorRemovable="false" `+
		`Visible="true" SwatchCreatorID="7937"/>`)
	out = append(out, `<StrokeStyle Self="StrokeStyle/$ID/Solid" Name="$ID/Solid"/>`)
	return strings.Join(out, "\n")
}

func fontStyle(s Span) string {
	switch {
	case s.Bold && s.Italic:
		return "Bold Italic"
	case s.Bold:
		return "Bold"
	case s.Italic:
		return "Italic"
	}
	return "Regular"
}

// Windows lists a font once per script ("Times New Roman Baltic"), and
// PageMaker saved that name; InDesign only knows the font's own.
var fontSuffixRe = regexp.MustCompile(` (Baltic|CE|Cyr|Greek|Tur|\(Arabic\)|\(Hebrew\)|\(Vietnamese\))$`)

func fontName(name string) string { return fontSuffixRe.ReplaceAllString(name, "") }

// cleanText drops the control characters XML can't hold.
func cleanText(text string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 0 && r <= 8) || (r >= 0x0b && r <= 0x1f) || r == 0xfffe || r == 0xffff {
			return -1
		}
		return r
	}, text)
}

type storyWriter struct {
	colors *idmlColors
	uid    func() string
}

// paragraphs are the ParagraphStyleRanges for blocks; each but the last ends
// with a return, and the last one too when lastBr.
func (w *storyWriter) paragraphs(blocks []Block, lastBr bool) string {
	var sb strings.Builder
	for i, b := range blocks {
		end := lastBr || i < len(blocks)-1
		if b.Kind == "table" {
			continue // PageMaker has no tables in its text; tables come from ruled lines (see table())
		}
		sb.WriteString(w.paragraph(b, end))
	}
	return sb.String()
}

func (w *storyWriter) paragraph(b Block, end bool) string {
	a, ok := idmlAlign[b.Align]
	if !ok {
		a = "LeftAlign"
	}
	pa := attrs("AppliedParagraphStyle", "ParagraphStyle/$ID/NormalParagraphStyle",
		"Justification", a, "FirstLineIndent", num(b.Indent), "LeftIndent", num(b.Left),
		"RightIndent", num(b.Right), "SpaceBefore", num(b.Before), "SpaceAfter", num(b.After),
		"Hyphenation", "false")
	var runs strings.Builder
	var leading float64
	if b.Leading != nil {
		leading = *b.Leading
	}
	for j, s := range b.Spans {
		br := ""
		if end && j == len(b.Spans)-1 {
			br = "<Br/>"
		}
		runs.WriteString(w.run(s, leading, xmlEscape(cleanText(s.T)), br))
	}
	if len(b.Spans) == 0 {
		br := ""
		if end {
			br = "<Br/>"
		}
		runs.WriteString(w.run(Span{}, leading, "", br))
	}
	return "<ParagraphStyleRange" + pa + ">" + runs.String() + "</ParagraphStyleRange>\n"
}

func (w *storyWriter) run(s Span, leading float64, content, br string) string {
	size := s.Size
	if size == 0 {
		size = 12
	}
	color := s.Color
	if color == "" {
		color = "#000000"
	}
	var underline any
	if s.Underline {
		underline = "true"
	}
	ra := attrs("AppliedCharacterStyle", "CharacterStyle/$ID/[No character style]",
		"PointSize", num(size), "FontStyle", fontStyle(s), "Underline", underline,
		"FillColor", w.colors.ref(color))
	props := ""
	if leading != 0 {
		props += `<Leading type="unit">` + num(leading) + `</Leading>`
	}
	if s.Font != "" {
		props += `<AppliedFont type="string">` + xmlEscape(fontName(s.Font)) + `</AppliedFont>`
	}
	if props != "" {
		props = "<Properties>" + props + "</Properties>"
	}
	body := ""
	if content != "" {
		body = "<Content>" + content + "</Content>"
	}
	return "<CharacterStyleRange" + ra + ">" + props + body + br + "</CharacterStyleRange>"
}

// table is a story holding one table, from FindTables().
func (w *storyWriter) table(t *Table, stories *Stories) string {
	tid := w.uid()
	rows, cols := t.Rows, t.Cols
	nrows, ncols := len(rows), len(cols)
	parts := []string{"<Table" + attrs("Self", tid, "HeaderRowCount", 0, "FooterRowCount", 0, "BodyRowCount", nrows,
		"ColumnCount", ncols, "AppliedTableStyle", "TableStyle/$ID/[Basic Table]",
		"TableDirection", "LeftToRightDirection") + ">"}
	for i, h := range rows {
		parts = append(parts, "<Row"+attrs("Self", fmt.Sprintf("%sRow%d", tid, i), "Name", i,
			"SingleRowHeight", num(h), "MinimumHeight", num(h), "AutoGrow", "true")+"/>")
	}
	for i, wd := range cols {
		parts = append(parts, "<Column"+attrs("Self", fmt.Sprintf("%sColumn%d", tid, i), "Name", i,
			"SingleColumnWidth", num(wd))+"/>")
	}
	for index, cell := range t.Cells {
		r, c := cell.Row, cell.Col
		pick := func(edge bool) float64 {
			if edge {
				return t.Border
			}
			return t.Rule
		}
		type edgeW struct {
			side string
			w    float64
		}
		edges := []edgeW{
			{"Top", pick(r == 0)}, {"Left", pick(c == 0)},
			{"Bottom", pick(r+cell.Rowspan == nrows)}, {"Right", pick(c+cell.Colspan == ncols)},
		}
		var strokes []any
		for _, e := range edges {
			strokes = append(strokes, e.side+"EdgeStrokeWeight", num(e.w), e.side+"EdgeStrokeColor", "Color/Black",
				e.side+"EdgeStrokeType", "StrokeStyle/$ID/Solid", e.side+"EdgeStrokeTint", 100)
		}
		// Narrow columns (row numbers) keep almost all their width.
		sum := 0.0
		for k := c; k < c+cell.Colspan && k < len(cols); k++ {
			sum += cols[k]
		}
		side := 1.5
		if sum < 30 {
			side = 0.5
		}
		rotate := 0.0
		for _, i := range cell.Items {
			if i.Rotate != 0 {
				rotate = i.Rotate
				break
			}
		}
		var blocks []Block
		for _, item := range cell.Items {
			blocks = append(blocks, cellBlocks(stories.Get(item.Story))...)
		}
		var rot any
		if rotate != 0 {
			// The model turns clockwise; InDesign's cell rotation goes the other way.
			n := -int(math.RoundToEven(rotate/90)) * 90
			n = ((n % 360) + 360) % 360
			rot = num(float64(n))
		}
		kv := []any{"Self", fmt.Sprintf("%si%d", tid, index), "Name", fmt.Sprintf("%d:%d", c, r),
			"RowSpan", cell.Rowspan, "ColumnSpan", cell.Colspan, "AppliedCellStyle", "CellStyle/$ID/[None]",
			"AppliedCellStylePriority", 0, "VerticalJustification", "CenterAlign", "TopInset", 1, "BottomInset", 1,
			"LeftInset", side, "RightInset", side, "RotationAngle", rot}
		kv = append(kv, strokes...)
		parts = append(parts, "<Cell"+attrs(kv...)+">"+w.paragraphs(blocks, false)+"</Cell>")
	}
	parts = append(parts, "</Table>")
	return "<ParagraphStyleRange" + attrs("AppliedParagraphStyle", "ParagraphStyle/$ID/NormalParagraphStyle",
		"SpaceBefore", 0, "SpaceAfter", 0) + "><CharacterStyleRange" +
		attrs("AppliedCharacterStyle", "CharacterStyle/$ID/[No character style]", "PointSize", 1) + ">" +
		strings.Join(parts, "") + "</CharacterStyleRange></ParagraphStyleRange>\n"
}

// cellBlocks is a text block's paragraphs, tidied for a table cell: spaces
// that only pushed the text into place, and blank paragraphs, are dropped.
func cellBlocks(blocks []Block) []Block {
	var out []Block
	for _, b := range blocks {
		if b.Kind != "p" {
			continue
		}
		spans := append([]Span(nil), b.Spans...)
		for len(spans) > 0 && strings.TrimFunc(spans[0].T, pyIsSpace) == "" {
			spans = spans[1:]
		}
		for len(spans) > 0 && strings.TrimFunc(spans[len(spans)-1].T, pyIsSpace) == "" {
			spans = spans[:len(spans)-1]
		}
		if len(spans) == 0 {
			continue
		}
		spans[0].T = strings.TrimLeftFunc(spans[0].T, pyIsSpace)
		spans[len(spans)-1].T = strings.TrimRightFunc(spans[len(spans)-1].T, pyIsSpace)
		nb := b
		nb.Spans = spans
		nb.Indent, nb.Left, nb.Right, nb.Before, nb.After = 0, 0, 0, 0, 0
		out = append(out, nb)
	}
	return out
}

func storyXML(sid, body string) string {
	return idmlHeader + "<idPkg:Story " + idmlPkg + ` DOMVersion="` + idmlDOM + `"><Story` +
		attrs("Self", sid, "AppliedTOCStyle", "n", "TrackChanges", "false", "StoryTitle", "$ID/",
			"AppliedNamedGrid", "n") + `>` +
		`<StoryPreference OpticalMarginAlignment="false" OpticalMarginSize="12" FrameType="TextFrameType" ` +
		`StoryOrientation="Horizontal" StoryDirection="LeftToRightDirection"/>` + "\n" + body +
		"</Story></idPkg:Story>"
}

var degToRad = func() float64 { pi := math.Pi; return pi / 180.0 }()

// frameGeometry is the PathGeometry around the box's centre and the
// ItemTransform that puts it on the spread (top is the page's top in spread
// coordinates).
func frameGeometry(x, y, w, h, rotate, top float64) (string, string) {
	hw, hh := w/2, h/2
	pts := []pt{{-hw, -hh}, {-hw, hh}, {hw, hh}, {hw, -hh}}
	a := rotate * degToRad
	cos, sin := math.Cos(a), math.Sin(a)
	var vals []string
	for _, v := range []float64{cos, sin, -sin, cos, x + hw, top + y + hh} {
		vals = append(vals, num(v))
	}
	return strings.Join(vals, " "), pathGeometry(pts, true)
}

func pathGeometry(points []pt, closed bool) string {
	var anchors strings.Builder
	for _, p := range points {
		a := num(p.x) + " " + num(p.y)
		fmt.Fprintf(&anchors, `<PathPointType Anchor="%s" LeftDirection="%s" RightDirection="%s"/>`, a, a, a)
	}
	open := "true"
	if closed {
		open = "false"
	}
	return `<Properties><PathGeometry><GeometryPathType PathOpen="` + open + `"><PathPointArray>` + anchors.String() +
		`</PathPointArray></GeometryPathType></PathGeometry></Properties>`
}

type frameStub struct {
	self, story, prev, next string
	item                    *Item
	top                     float64
}

type spreadItem struct {
	stub *frameStub
	xml  string
}

type spreadOut struct {
	index         int
	width, height float64
	items         []spreadItem
}

type idmlWriter struct {
	doc       *Doc
	colors    *idmlColors
	stories   map[string]string
	storyKeys []string
	ids       int
	firstPage string
}

func (w *idmlWriter) uid() string {
	w.ids++
	return fmt.Sprintf("u%x", w.ids+0x1000)
}

func (w *idmlWriter) addStory(sid, xml string) {
	if _, ok := w.stories[sid]; !ok {
		w.storyKeys = append(w.storyKeys, sid)
	}
	w.stories[sid] = xml
}

type namedFile struct{ name, xml string }

func (w *idmlWriter) build() []namedFile {
	doc := w.doc
	storyUse := StoryFrameCounts(doc)
	sw := &storyWriter{colors: w.colors, uid: w.uid}
	frames := map[string][]*frameStub{}
	var frameOrder []string
	var spreads []spreadOut
	for index, page := range doc.Pages {
		width, height := page.Width, page.Height
		top := -height / 2
		found := FindTables(page, doc.Stories, storyUse)
		skip := map[*Item]bool{}
		for _, t := range found {
			for _, i := range t.Paths {
				skip[i] = true
			}
			for _, i := range t.Texts {
				skip[i] = true
			}
		}
		var items []spreadItem
		for _, item := range page.Items {
			if skip[item] {
				continue
			}
			switch item.Type {
			case "text":
				stub := &frameStub{self: w.uid(), item: item, top: top}
				if _, ok := frames[item.Story]; !ok {
					frameOrder = append(frameOrder, item.Story)
				}
				frames[item.Story] = append(frames[item.Story], stub)
				items = append(items, spreadItem{stub: stub})
			case "path":
				items = append(items, spreadItem{xml: w.path(item, top)})
			}
		}
		for _, t := range found {
			sid := w.uid()
			w.addStory(sid, storyXML(sid, sw.table(t, doc.Stories)))
			transform, geometry := frameGeometry(t.X, t.Y, t.W+1, t.H+1, 0, top)
			items = append(items, spreadItem{xml: w.textFrameXML(w.uid(), sid, "n", "n", transform, geometry, t.W+1, true)})
		}
		spreads = append(spreads, spreadOut{index, width, height, items})
	}

	// Stories, and each chain of frames threaded in page order.
	for _, key := range frameOrder {
		chain := frames[key]
		sid := w.uid()
		w.addStory(sid, storyXML(sid, sw.paragraphs(doc.Stories.Get(key), false)))
		for i, stub := range chain {
			stub.story = sid
			stub.prev = "n"
			if i > 0 {
				stub.prev = chain[i-1].self
			}
			stub.next = "n"
			if i+1 < len(chain) {
				stub.next = chain[i+1].self
			}
		}
	}

	var files []namedFile
	for _, sp := range spreads {
		var body []string
		for _, it := range sp.items {
			if it.stub != nil {
				item := it.stub.item
				h := item.H
				if !(h > 0) {
					h = 27
				}
				transform, geometry := frameGeometry(item.X, item.Y, item.W, h, item.Rotate, it.stub.top)
				body = append(body, w.textFrameXML(it.stub.self, it.stub.story, it.stub.prev, it.stub.next,
					transform, geometry, item.W, !(item.H > 0)))
			} else {
				body = append(body, it.xml)
			}
		}
		sid := w.uid()
		pageID := w.uid()
		if w.firstPage == "" {
			w.firstPage = pageID
		}
		pageXML := "<Page" + attrs("Self", pageID, "Name", sp.index+1, "AppliedMaster", "n",
			"ItemTransform", "1 0 0 1 0 "+num(-sp.height/2),
			"GeometricBounds", "0 0 "+num(sp.height)+" "+num(sp.width), "MasterPageTransform", "1 0 0 1 0 0",
			"OverrideList", "", "TabOrder", "", "GridStartingPoint", "TopOutside", "UseMasterGrid", "true") +
			"><MarginPreference" + attrs("ColumnCount", 1, "ColumnGutter", 12, "Top", 36, "Bottom", 36, "Left", 36,
			"Right", 36, "ColumnDirection", "Horizontal", "ColumnsPositions", "0 "+num(sp.width-72)) + "/></Page>"
		xml := idmlHeader + "<idPkg:Spread " + idmlPkg + ` DOMVersion="` + idmlDOM + `"><Spread` +
			attrs("Self", sid, "PageCount", 1, "BindingLocation", 0, "AllowPageShuffle", "true",
				"ItemTransform", "1 0 0 1 0 "+num(float64(sp.index)*(sp.height+36)),
				"ShowMasterItems", "true", "FlattenerOverride", "Default") + ">" + pageXML + "\n" +
			strings.Join(body, "\n") + "</Spread></idPkg:Spread>"
		files = append(files, namedFile{fmt.Sprintf("Spreads/Spread_s%d.xml", sp.index+1), xml})
	}
	return files
}

func (w *idmlWriter) textFrameXML(selfID, story, prev, next, transform, geometry string, width float64, autosize bool) string {
	var ast, aref any
	if autosize {
		ast, aref = "HeightOnly", "TopCenterPoint"
	}
	pref := attrs("TextColumnCount", 1, "TextColumnFixedWidth", num(width), "UseFixedColumnWidth", "false",
		"AutoSizingType", ast, "AutoSizingReferencePoint", aref)
	return "<TextFrame" + attrs("Self", selfID, "ParentStory", story, "PreviousTextFrame", prev, "NextTextFrame", next,
		"ContentType", "TextType", "ItemLayer", idmlLayer, "ItemTransform", transform,
		"FillColor", "Swatch/None", "StrokeColor", "Swatch/None", "StrokeWeight", 0) + ">" + geometry +
		"<TextFramePreference" + pref + "/></TextFrame>"
}

func (w *idmlWriter) path(item *Item, top float64) string {
	var out []string
	for _, sub := range pathPoints(item.D) {
		pts := make([]pt, len(sub.points))
		for i, p := range sub.points {
			pts[i] = pt{p.x, top + p.y}
		}
		if sub.closed && len(pts) > 1 && pts[len(pts)-1] == pts[0] {
			pts = pts[:len(pts)-1]
		}
		if len(pts) < 2 {
			continue
		}
		closed := sub.closed && len(pts) > 2
		tag := "Polygon"
		if !closed && len(pts) == 2 {
			tag = "GraphicLine"
		}
		sw := item.SW
		if sw == 0 {
			sw = 1
		}
		out = append(out, "<"+tag+attrs("Self", w.uid(), "ContentType", "Unassigned", "ItemLayer", idmlLayer,
			"ItemTransform", "1 0 0 1 0 0", "StrokeWeight", num(sw),
			"StrokeColor", w.colors.ref(item.Stroke), "FillColor", w.colors.ref(item.Fill))+">"+
			pathGeometry(pts, closed)+"</"+tag+">")
	}
	return strings.Join(out, "\n")
}

func designMap(spreadFiles []namedFile, storyIDs []string, npages int, firstPage string) string {
	var sb strings.Builder
	sb.WriteString(idmlHeader)
	sb.WriteString(`<?aid style="50" type="document" readerVersion="6.0" featureSet="257" product="8.0(370)" ?>` + "\n")
	fmt.Fprintf(&sb, "<Document %s DOMVersion=\"%s\" Self=\"d\" StoryList=%s ZeroPoint=\"0 0\" ActiveLayer=\"%s\">\n",
		idmlPkg, idmlDOM, quoteAttr(strings.Join(append(append([]string(nil), storyIDs...), idmlBacking), " ")), idmlLayer)
	sb.WriteString("<idPkg:Graphic src=\"Resources/Graphic.xml\"/>\n<idPkg:Fonts src=\"Resources/Fonts.xml\"/>\n" +
		"<idPkg:Styles src=\"Resources/Styles.xml\"/>\n<idPkg:Preferences src=\"Resources/Preferences.xml\"/>\n" +
		"<idPkg:Tags src=\"XML/Tags.xml\"/>\n")
	fmt.Fprintf(&sb, `<Layer Self="%s" Name="Layer 1" Visible="true" Locked="false" IgnoreWrap="false" ShowGuides="true" `+
		`LockGuides="false" UI="true" Expendable="true" Printable="true"><Properties>`+
		`<LayerColor type="enumeration">LightBlue</LayerColor></Properties></Layer>`+"\n", idmlLayer)
	for _, f := range spreadFiles {
		fmt.Fprintf(&sb, "<idPkg:Spread src=\"%s\"/>\n", f.name)
	}
	fmt.Fprintf(&sb, `<Section Self="%s" Length="%d" Name="" ContinueNumbering="true" IncludeSectionPrefix="false" `+
		`Marker="" PageStart="%s" SectionPrefix=""><Properties>`+
		`<PageNumberStyle type="enumeration">Arabic</PageNumberStyle></Properties></Section>`+"\n",
		idmlSection, npages, firstPage)
	sb.WriteString("<idPkg:BackingStory src=\"XML/BackingStory.xml\"/>\n")
	for _, sid := range storyIDs {
		fmt.Fprintf(&sb, "<idPkg:Story src=\"Stories/Story_%s.xml\"/>\n", sid)
	}
	sb.WriteString("</Document>")
	return sb.String()
}

func idmlStyles() string {
	return idmlHeader + "<idPkg:Styles " + idmlPkg + ` DOMVersion="` + idmlDOM + `">` + "\n" +
		`<RootCharacterStyleGroup Self="ub4"><CharacterStyle Self="CharacterStyle/$ID/[No character style]" ` +
		`Imported="false" Name="$ID/[No character style]"/></RootCharacterStyleGroup>` + "\n" +
		`<RootParagraphStyleGroup Self="ub5">` +
		`<ParagraphStyle Self="ParagraphStyle/$ID/[No paragraph style]" Imported="false" ` +
		`Name="$ID/[No paragraph style]" PointSize="12" Hyphenation="false">` +
		`<Properties><Leading type="enumeration">Auto</Leading>` +
		`<AppliedFont type="string">Times New Roman</AppliedFont></Properties></ParagraphStyle>` +
		`<ParagraphStyle Self="ParagraphStyle/$ID/NormalParagraphStyle" Imported="false" ` +
		`Name="$ID/NormalParagraphStyle" NextStyle="ParagraphStyle/$ID/NormalParagraphStyle">` +
		`<Properties><BasedOn type="string">$ID/[No paragraph style]</BasedOn></Properties></ParagraphStyle>` +
		`</RootParagraphStyleGroup>` + "\n" +
		`<RootCellStyleGroup Self="ub6"><CellStyle Self="CellStyle/$ID/[None]" Name="$ID/[None]"/>` +
		`</RootCellStyleGroup>` + "\n" +
		`<RootTableStyleGroup Self="ub7"><TableStyle Self="TableStyle/$ID/[No table style]" ` +
		`Name="$ID/[No table style]"/><TableStyle Self="TableStyle/$ID/[Basic Table]" Name="$ID/[Basic Table]"/>` +
		`</RootTableStyleGroup>` + "\n" +
		`<RootObjectStyleGroup Self="ub8"><ObjectStyle Self="ObjectStyle/$ID/[None]" Name="$ID/[None]"/>` +
		`<ObjectStyle Self="ObjectStyle/$ID/[Normal Graphics Frame]" Name="$ID/[Normal Graphics Frame]"/>` +
		`<ObjectStyle Self="ObjectStyle/$ID/[Normal Text Frame]" Name="$ID/[Normal Text Frame]"/>` +
		`<ObjectStyle Self="ObjectStyle/$ID/[Normal Grid]" Name="$ID/[Normal Grid]"/>` +
		`</RootObjectStyleGroup>` + "\n" +
		`</idPkg:Styles>`
}

// zipWriter writes entries the way Python's zipfile does on a seekable
// target: sizes known up front, no data descriptors.
type zipWriter struct{ zw *zip.Writer }

func (z *zipWriter) add(name string, data []byte, deflate bool, modified time.Time) error {
	fh := &zip.FileHeader{Name: name, Method: zip.Store, Modified: modified}
	body := data
	if deflate {
		var buf bytes.Buffer
		fw, _ := flate.NewWriter(&buf, flate.DefaultCompression)
		fw.Write(data)
		fw.Close()
		body = buf.Bytes()
		fh.Method = zip.Deflate
	}
	fh.CRC32 = crc32.ChecksumIEEE(data)
	fh.UncompressedSize64 = uint64(len(data))
	fh.CompressedSize64 = uint64(len(body))
	w, err := z.zw.CreateRaw(fh)
	if err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

// WriteIDML is the IDML file for doc, as bytes.
func WriteIDML(doc *Doc) ([]byte, error) {
	w := &idmlWriter{doc: doc, colors: &idmlColors{}, stories: map[string]string{}}
	spreadFiles := w.build()
	storyIDs := w.storyKeys
	first := &Page{Width: 595.3, Height: 841.9}
	if len(doc.Pages) > 0 {
		first = doc.Pages[0]
	}
	var buf bytes.Buffer
	z := &zipWriter{zw: zip.NewWriter(&buf)}
	now := time.Now()
	epoch := time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
	type entry struct {
		name, data string
	}
	entries := []entry{
		{"META-INF/container.xml", idmlHeader +
			`<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">` +
			`<rootfiles><rootfile full-path="designmap.xml" media-type="text/xml"/></rootfiles></container>`},
		{"designmap.xml", designMap(spreadFiles, storyIDs, len(doc.Pages), w.firstPage)},
		{"Resources/Graphic.xml", idmlHeader + "<idPkg:Graphic " + idmlPkg + ` DOMVersion="` + idmlDOM + `">` + "\n" +
			w.colors.xml() + "\n</idPkg:Graphic>"},
		{"Resources/Fonts.xml", idmlHeader + "<idPkg:Fonts " + idmlPkg + ` DOMVersion="` + idmlDOM + `"/>`},
		{"Resources/Styles.xml", idmlStyles()},
		{"Resources/Preferences.xml", idmlHeader + "<idPkg:Preferences " + idmlPkg + ` DOMVersion="` + idmlDOM + `">` +
			"<DocumentPreference" + attrs("PageWidth", num(first.Width), "PageHeight", num(first.Height),
			"PagesPerDocument", len(doc.Pages), "FacingPages", "false") + "/></idPkg:Preferences>"},
		{"XML/Tags.xml", idmlHeader + "<idPkg:Tags " + idmlPkg + ` DOMVersion="` + idmlDOM + `"/>`},
		{"XML/BackingStory.xml", idmlHeader + "<idPkg:BackingStory " + idmlPkg + ` DOMVersion="` + idmlDOM + `">` +
			`<XmlStory Self="` + idmlBacking + `" AppliedTOCStyle="n" TrackChanges="false" StoryTitle="$ID/" AppliedNamedGrid="n">` +
			`<ParagraphStyleRange AppliedParagraphStyle="ParagraphStyle/$ID/NormalParagraphStyle">` +
			`<CharacterStyleRange AppliedCharacterStyle="CharacterStyle/$ID/[No character style]"/>` +
			`</ParagraphStyleRange></XmlStory></idPkg:BackingStory>`},
	}
	for _, f := range spreadFiles {
		entries = append(entries, entry{f.name, f.xml})
	}
	for _, sid := range storyIDs {
		entries = append(entries, entry{"Stories/Story_" + sid + ".xml", w.stories[sid]})
	}
	if err := z.add("mimetype", []byte("application/vnd.adobe.indesign-idml-package"), false, epoch); err != nil {
		return nil, err
	}
	for _, e := range entries {
		if err := z.add(e.name, []byte(e.data), true, now); err != nil {
			return nil, err
		}
	}
	if err := z.zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

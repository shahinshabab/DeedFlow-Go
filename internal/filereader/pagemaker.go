package filereader

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/tiff"

	"github.com/shahinshabab/deedflow-go/internal/pmd"
)

// Reads Adobe PageMaker files (.pmd, .p65, .pm6 ...) through the pmd package,
// which runs the PageMaker parser LibreOffice uses and records its drawing
// calls.

const ptPerIn = 72.0

var lengthRe = regexp.MustCompile(`^\s*(-?[\d.]+(?:e-?\d+)?)\s*([a-z%]*)\s*$`)
var numberRe = regexp.MustCompile(`^\s*(-?[\d.]+)`)

// length turns '1.5in' / '12pt' / '0.5cm' / '3' into points.
func length(value string, ok bool, def float64) float64 {
	if !ok {
		return def
	}
	m := lengthRe.FindStringSubmatch(value)
	if m == nil {
		return def
	}
	number, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return def
	}
	unit := m[2]
	switch unit {
	case "in":
		return number * ptPerIn
	case "pt":
		return number
	case "cm":
		return number * (ptPerIn / 2.54)
	case "mm":
		return number * (ptPerIn / 25.4)
	case "twip":
		return number * (1 / 20.0)
	case "":
		return number * ptPerIn
	}
	return number * 1.0
}

func number(value string, ok bool, def float64) float64 {
	if !ok {
		return def
	}
	m := numberRe.FindStringSubmatch(value)
	if m == nil {
		return def
	}
	f, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return def
	}
	return f
}

type props pmd.PropList

func (p props) get(k string) (string, bool) {
	v, ok := p[k]
	if !ok || v.IsVec {
		return "", false
	}
	return v.S, true
}

func (p props) str(k string) string { s, _ := p.get(k); return s }

func (p props) length(k string, def float64) float64 { s, ok := p.get(k); return length(s, ok, def) }
func (p props) number(k string, def float64) float64 { s, ok := p.get(k); return number(s, ok, def) }

var colorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func (p props) color(k, def string) string {
	v, ok := p.get(k)
	if ok && v != "" && colorRe.MatchString(v) {
		return v
	}
	return def
}

var cp1252High = map[byte]rune{
	0x80: 0x20AC, 0x82: 0x201A, 0x83: 0x0192, 0x84: 0x201E, 0x85: 0x2026, 0x86: 0x2020, 0x87: 0x2021,
	0x88: 0x02C6, 0x89: 0x2030, 0x8A: 0x0160, 0x8B: 0x2039, 0x8C: 0x0152, 0x8E: 0x017D, 0x91: 0x2018,
	0x92: 0x2019, 0x93: 0x201C, 0x94: 0x201D, 0x95: 0x2022, 0x96: 0x2013, 0x97: 0x2014, 0x98: 0x02DC,
	0x99: 0x2122, 0x9A: 0x0161, 0x9B: 0x203A, 0x9C: 0x0153, 0x9E: 0x017E, 0x9F: 0x0178,
}

// rawText: pmd hands over each byte of the file as the Latin-1 code point of
// the same value; get the bytes back and read them as Windows-1252, the code
// page PageMaker on Windows typed in. Bytes 0x81/0x8d/0x8f/0x90/0x9d have no
// cp1252 meaning and stay as their Latin-1 code point.
func rawText(hexText string) string {
	b, err := hex.DecodeString(hexText)
	if err != nil {
		panic(fmt.Errorf("bad hex text: %w", err))
	}
	var sb strings.Builder
	for len(b) > 0 {
		r, n := utf8.DecodeRune(b)
		b = b[n:]
		if r > 0xff {
			panic(fmt.Errorf("text outside Latin-1"))
		}
		if mapped, ok := cp1252High[byte(r)]; ok {
			sb.WriteRune(mapped)
		} else {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

type shapeStyle struct {
	fill, stroke string
	sw           float64
}

func shapeStyleOf(p props) shapeStyle {
	fill := "none"
	if p.str("draw:fill") == "solid" && p.number("draw:opacity", 1) > 0 {
		fill = p.color("draw:fill-color", "none")
	}
	stroke := "none"
	if p.str("draw:stroke") != "none" {
		stroke = p.color("svg:stroke-color", "#000000")
	}
	w := p.length("svg:stroke-width", 1.0)
	if w < 0.25 {
		w = 0.25
	}
	return shapeStyle{fill, stroke, pyRound(w, 2)}
}

func pathD(nodes []pmd.PropList) string {
	var parts []string
	for _, n := range nodes {
		p := props(n)
		action := p.str("librevenge:path-action")
		x, y := p.length("svg:x", 0), p.length("svg:y", 0)
		switch action {
		case "M", "L":
			parts = append(parts, fmt.Sprintf("%s%.2f %.2f", action, x, y))
		case "A":
			large, sweep := 0, 0
			if p.str("librevenge:large-arc") == "true" {
				large = 1
			}
			if p.str("librevenge:sweep") == "true" {
				sweep = 1
			}
			parts = append(parts, fmt.Sprintf("A%.2f %.2f %.2f %d %d %.2f %.2f",
				p.length("svg:rx", 0), p.length("svg:ry", 0), p.number("librevenge:rotate", 0), large, sweep, x, y))
		case "Z":
			parts = append(parts, "Z")
		}
	}
	return strings.Join(parts, " ")
}

func imageItem(p props) *Item {
	item := &Item{Type: "image", X: p.length("svg:x", 0), Y: p.length("svg:y", 0),
		W: p.length("svg:width", 0), H: p.length("svg:height", 0),
		Rotate: p.number("librevenge:rotate", 0), Origin: "center"}
	if src, ok := tiffToPNG(p.str("office:binary-data")); ok {
		item.Src = src
	} else {
		item.Label = "Image"
	}
	return item
}

// tiffToPNG decodes the picture and returns it as a PNG data URI, scaled to
// fit 1600 pixels.
func tiffToPNG(b64 string) (string, bool) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", false
	}
	var img image.Image
	func() {
		defer func() {
			if r := recover(); r != nil {
				img = nil
			}
		}()
		img, _, err = image.Decode(bytes.NewReader(raw))
	}()
	if err != nil || img == nil {
		return "", false
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > 1600 || h > 1600 {
		scale := 1600.0 / float64(max(w, h))
		nw, nh := max(int(float64(w)*scale), 1), max(int(float64(h)*scale), 1)
		dst := image.NewNRGBA(image.Rect(0, 0, nw, nh))
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Over, nil)
		img, b = dst, dst.Bounds()
	}
	// RGBA for pictures with transparency (and palettes), RGB otherwise.
	hasAlpha := false
	switch img.(type) {
	case *image.NRGBA, *image.RGBA, *image.NRGBA64, *image.RGBA64, *image.Paletted:
		hasAlpha = true
	}
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Src)
	if !hasAlpha {
		for i := 3; i < len(dst.Pix); i += 4 {
			dst.Pix[i] = 0xff
		}
	}
	var out image.Image = dst
	var buf bytes.Buffer
	if err := png.Encode(&buf, out); err != nil {
		return "", false
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), true
}

// buildDocument turns the recorded drawing calls into a model document. With
// convert false, text in legacy fonts keeps its original characters.
func buildDocument(calls []pmd.Call, convert bool) *Doc {
	doc := NewDoc("PageMaker")
	var page *Page
	var frame *Item // text item being filled
	var blocks *[]Block
	var para *Block
	var spanProps props
	var runs []string
	var leadings []float64

	closeSpan := func() {
		if para != nil && len(runs) > 0 {
			text := strings.Join(runs, "")
			p := spanProps
			font := p.str("style:font-name")
			para.Spans = append(para.Spans, NewSpan(
				text, font, p.length("fo:font-size", 12.0),
				p.str("fo:font-weight") == "bold", p.str("fo:font-style") == "italic",
				p.str("style:text-underline-type") != "",
				p.color("fo:color", "#000000"), nil, convert))
			leadings = append(leadings, p.length("fo:line-height", 0))
		}
		runs = nil
	}

	for _, call := range calls {
		m := call.M
		p := props(call.P)
		switch {
		case m == "startPage":
			page = &Page{Width: p.length("svg:width", 595), Height: p.length("svg:height", 842)}
			doc.Pages = append(doc.Pages, page)
		case page == nil:
			continue
		case m == "startTextObject":
			b := []Block{}
			blocks = &b
			story, _ := p.get("librevenge:story-id")
			frame = &Item{Type: "text", X: p.length("svg:x", 0), Y: p.length("svg:y", 0),
				W: p.length("svg:width", 0), H: p.length("svg:height", 0),
				// libpagemaker's angle already turns clockwise on the page
				// (its own shape maths treats it so), like the model's.
				Rotate: p.number("librevenge:rotate", 0), Origin: "center", Nowrap: false, thread: story}
		case m == "openParagraph" && blocks != nil:
			para0 := NewParagraph()
			switch p.str("fo:text-align") {
			case "right", "center", "justify":
				para0.Align = p.str("fo:text-align")
			}
			para0.Indent = p.length("fo:text-indent", 0)
			para0.Left = p.length("fo:margin-left", 0)
			para0.Right = p.length("fo:margin-right", 0)
			para0.Before = p.length("fo:margin-top", 0)
			para0.After = p.length("fo:margin-bottom", 0)
			para = &para0
		case m == "openSpan":
			spanProps = p
			runs = nil
		case m == "text":
			runs = append(runs, rawText(call.Hex))
		case m == "closeSpan":
			closeSpan()
		case m == "closeParagraph" && para != nil:
			closeSpan()
			*blocks = append(*blocks, splitParagraph(*para, leadings)...)
			para, leadings = nil, nil
		case m == "endTextObject" && frame != nil:
			frame.blocks = *blocks
			page.Items = append(page.Items, frame)
			frame, blocks = nil, nil
		case m == "drawPolygon" || m == "drawPolyline":
			pts := p["svg:points"].Vec
			if len(pts) >= 2 {
				var sb strings.Builder
				for i, pt := range pts {
					pp := props(pt)
					cmd := "L"
					if i == 0 {
						cmd = "M"
					} else {
						sb.WriteString(" ")
					}
					fmt.Fprintf(&sb, "%s%.2f %.2f", cmd, pp.length("svg:x", 0), pp.length("svg:y", 0))
				}
				if m == "drawPolygon" {
					sb.WriteString(" Z")
				}
				st := shapeStyleOf(p)
				page.Items = append(page.Items, &Item{Type: "path", D: sb.String(), Fill: st.fill, Stroke: st.stroke, SW: st.sw})
			}
		case m == "drawPath":
			st := shapeStyleOf(p)
			page.Items = append(page.Items, &Item{Type: "path", D: pathD(p["svg:d"].Vec), Fill: st.fill, Stroke: st.stroke, SW: st.sw})
		case m == "drawGraphicObject":
			page.Items = append(page.Items, imageItem(p))
		}
	}
	linkStories(doc)
	return doc
}

// splitParagraph: the parser opens a paragraph per run of paragraph settings,
// which can hold many of the story's paragraphs, each ended by a return (a
// line break here). Splits it into those paragraphs. Leading belongs to the
// characters in PageMaker and a line is as tall as the largest on it, so each
// paragraph takes the largest of its own.
func splitParagraph(para Block, leadings []float64) []Block {
	var out []Block
	newCurrent := func() Block {
		c := para
		c.Spans = nil
		return c
	}
	current := newCurrent()
	lead := 0.0

	finish := func(span *Span, spanLead float64) {
		if len(current.Spans) == 0 && span != nil {
			// A blank line still takes a line, in its return's font.
			s := *span
			s.T = ""
			current.Spans = append(current.Spans, s)
			if spanLead != 0 {
				v := spanLead
				current.Leading = &v
			} else {
				current.Leading = nil
			}
		} else if lead != 0 {
			v := lead
			current.Leading = &v
		}
		out = append(out, current)
	}

	for i, span := range para.Spans {
		if i >= len(leadings) {
			break
		}
		spanLead := leadings[i]
		pieces := strings.Split(span.T, "\n")
		for j, piece := range pieces {
			if j > 0 {
				sp := span
				finish(&sp, spanLead)
				current, lead = newCurrent(), 0.0
			}
			if piece != "" {
				s := span
				s.T = piece
				current.Spans = append(current.Spans, s)
				if spanLead > lead {
					lead = spanLead
				}
			}
		}
	}
	// The return that ends the last paragraph leaves an empty one behind.
	if len(current.Spans) > 0 || len(out) == 0 {
		finish(nil, 0.0)
	}
	return out
}

// linkStories: PageMaker threads one story through several text blocks, and
// the parser gives every block in the thread the story's whole text, along
// with the thread's id. Blocks with the same id are one chain: the text is
// kept once and flowed from block to block, in page order, by the page.
// Separate blocks that merely have the same text (a signature repeated at
// the foot of each page) keep their own copy.
func linkStories(doc *Doc) {
	byThread := map[string]string{}
	for _, page := range doc.Pages {
		for _, item := range page.Items {
			if item.Type != "text" {
				continue
			}
			blocks, thread := item.blocks, item.thread
			item.blocks, item.thread = nil, ""
			if thread != "" && thread != "0" {
				if id, ok := byThread[thread]; ok {
					item.Story = id
					continue
				}
			}
			item.Story = "s" + strconv.Itoa(doc.Stories.Len()+1)
			doc.Stories.Set(item.Story, blocks)
			if thread != "" && thread != "0" {
				byThread[thread] = item.Story
			}
		}
	}
}

// ReadPageMaker reads a PageMaker file. With convert false, text in legacy
// fonts keeps its original characters (for writing it back out).
func ReadPageMaker(data []byte, convert bool) (*Doc, error) {
	calls, res := pmd.Read(data)
	if res == pmd.Unsupported {
		return nil, readerErr("This doesn't look like a PageMaker file this reader understands " +
			"(PageMaker 6.0 to 7.0 files are supported).")
	}
	doc := buildDocument(calls, convert)
	if res != pmd.OK {
		doc.Warnings = append(doc.Warnings, "Part of this file couldn't be read, so some content may be missing.")
	}
	if len(doc.Pages) == 0 {
		return nil, readerErr("No pages were found in this PageMaker file.")
	}
	return doc, nil
}

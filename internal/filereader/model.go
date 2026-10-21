// Package filereader reads desktop-publishing files (PageMaker, InDesign
// IDML, PDF) into one common document model, draws them on the File reader
// page, turns them into filing sheets, and writes PageMaker documents back
// out for InDesign (IDML and JSX).
//
// The model: all lengths are in points (1/72 inch), measured from the page's
// top-left corner. `Rotate` is in degrees clockwise, about the box's centre
// when `Origin` is "center" and its top-left corner otherwise. `Repeat` marks
// a master-page frame copied onto every page: it shows its whole story on
// each. A story is the text of one frame, or of a chain of linked frames:
// every text item naming the same story id is one link in that chain, in
// page order, and the page flows the story's text from one to the next.
package filereader

import (
	"math"
	"strconv"
	"strings"
	"unicode/utf16"
)

// ReaderError is a file that can't be read; the message is shown to the user.
type ReaderError struct{ Msg string }

func (e *ReaderError) Error() string { return e.Msg }

func readerErr(msg string) *ReaderError { return &ReaderError{msg} }

// Span is a run of text in one style.
type Span struct {
	T         string
	Font      string
	Size      float64
	Bold      bool
	Italic    bool
	Underline bool
	Color     string
	Legacy    bool
}

// NewSpan makes a span. Text in a legacy font is converted to Unicode unless
// convert is false (for writing it back out in the same font). isLegacy nil
// means "work it out from the font and text".
func NewSpan(text, font string, size float64, bold, italic, underline bool, color string, isLegacy *bool, convert bool) Span {
	var legacy bool
	if isLegacy != nil {
		legacy = *isLegacy
	} else {
		legacy = IsLegacyFont(font, text)
	}
	t := text
	if legacy && convert {
		t = ToUnicode(text)
	}
	return Span{T: t, Font: font, Size: pyRound(size, 2), Bold: bold, Italic: italic, Underline: underline,
		Color: color, Legacy: legacy}
}

// Block is a paragraph ("p") or a table ("table").
type Block struct {
	Kind string

	// paragraphs
	Align   string
	Indent  float64
	Left    float64
	Right   float64
	Before  float64
	After   float64
	Leading *float64
	Spans   []Span

	// tables
	Cols []float64
	Rows []Row
}

// Row is a table row.
type Row struct{ Cells []Cell }

// Cell is a table cell.
type Cell struct {
	Col     int
	Blocks  []Block
	Colspan int
	Rowspan int
	Rotate  float64
}

// NewParagraph makes an empty paragraph with the given settings.
func NewParagraph() Block { return Block{Kind: "p", Align: "left"} }

// Item is something on a page: a text frame, a path or an image.
type Item struct {
	Type string // "text", "path" or "image"

	X, Y, W, H float64
	Rotate     float64
	Origin     string // "center" or "top left"; empty when the reader doesn't say

	// text
	Story  string
	Inset  [4]float64
	Valign string // empty when the reader doesn't say
	Nowrap bool
	Repeat bool

	// path
	D      string
	Fill   string
	Stroke string
	SW     float64

	// image
	Src   string
	Label string

	// PageMaker text frames before their stories are linked
	blocks []Block
	thread string
}

// Page is one page of a document.
type Page struct {
	Width, Height float64
	Items         []*Item
}

// Stories is an ordered map from story id to its blocks.
type Stories struct {
	keys []string
	m    map[string][]Block
}

// NewStories makes an empty set of stories.
func NewStories() *Stories { return &Stories{m: map[string][]Block{}} }

// Set adds or replaces a story, keeping the position of an existing id.
func (s *Stories) Set(id string, blocks []Block) {
	if _, ok := s.m[id]; !ok {
		s.keys = append(s.keys, id)
	}
	s.m[id] = blocks
}

// Get returns a story's blocks (nil when there is none).
func (s *Stories) Get(id string) []Block {
	if s == nil {
		return nil
	}
	return s.m[id]
}

// Has says whether a story exists.
func (s *Stories) Has(id string) bool {
	if s == nil {
		return false
	}
	_, ok := s.m[id]
	return ok
}

// Keys lists the story ids in insertion order.
func (s *Stories) Keys() []string {
	if s == nil {
		return nil
	}
	return s.keys
}

// Len is the number of stories.
func (s *Stories) Len() int {
	if s == nil {
		return 0
	}
	return len(s.keys)
}

// Doc is a read document.
type Doc struct {
	Format   string
	Name     string
	Pages    []*Page
	Stories  *Stories
	Warnings []string
}

// NewDoc makes an empty document of a format.
func NewDoc(format string) *Doc {
	return &Doc{Format: format, Stories: NewStories(), Warnings: []string{}}
}

// --- numbers --------------------------------------------------------------------

// pyRound is Python's round(v, nd) for floats.
func pyRound(v float64, nd int) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return v
	}
	r, err := strconv.ParseFloat(strconv.FormatFloat(v, 'f', nd, 64), 64)
	if err != nil {
		return v
	}
	if r == 0 && math.Signbit(v) {
		return math.Copysign(0, -1)
	}
	return r
}

// pyRepr is Python's repr(float).
func pyRepr(v float64) string {
	switch {
	case math.IsNaN(v):
		return "nan"
	case math.IsInf(v, 1):
		return "inf"
	case math.IsInf(v, -1):
		return "-inf"
	case v == 0:
		if math.Signbit(v) {
			return "-0.0"
		}
		return "0.0"
	}
	e := strconv.FormatFloat(v, 'e', -1, 64) // d.ddde±XX
	mant, expo, _ := strings.Cut(e, "e")
	exp, _ := strconv.Atoi(expo)
	decpt := exp + 1
	if -4 < decpt && decpt <= 16 {
		f := strconv.FormatFloat(v, 'f', -1, 64)
		if !strings.Contains(f, ".") {
			f += ".0"
		}
		return f
	}
	sign := ""
	if exp < 0 {
		sign, exp = "-", -exp
	} else {
		sign = "+"
	}
	es := strconv.Itoa(exp)
	if len(es) < 2 {
		es = "0" + es
	}
	return mant + "e" + sign + es
}

// --- JSON, written the way Python's json.dumps writes it ------------------------

type enc struct {
	b     strings.Builder
	ascii bool
}

func (e *enc) raw(s string) { e.b.WriteString(s) }

func (e *enc) str(s string) {
	e.b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			e.b.WriteString(`\"`)
		case '\\':
			e.b.WriteString(`\\`)
		case '\n':
			e.b.WriteString(`\n`)
		case '\r':
			e.b.WriteString(`\r`)
		case '\t':
			e.b.WriteString(`\t`)
		case '\b':
			e.b.WriteString(`\b`)
		case '\f':
			e.b.WriteString(`\f`)
		default:
			switch {
			case r >= 0x20 && r < 0x7f:
				e.b.WriteRune(r)
			case !e.ascii && r >= 0x80:
				e.b.WriteRune(r)
			case r < 0x10000:
				e.b.WriteString(`\u`)
				e.b.WriteString(hex4(uint16(r)))
			default:
				r1, r2 := utf16.EncodeRune(r)
				e.b.WriteString(`\u` + hex4(uint16(r1)) + `\u` + hex4(uint16(r2)))
			}
		}
	}
	e.b.WriteByte('"')
}

func hex4(v uint16) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[v>>12], digits[v>>8&15], digits[v>>4&15], digits[v&15]})
}

func (e *enc) num(v float64) { e.b.WriteString(pyRepr(v)) }
func (e *enc) integer(v int) { e.b.WriteString(strconv.Itoa(v)) }
func (e *enc) boolean(v bool) {
	if v {
		e.b.WriteString("true")
	} else {
		e.b.WriteString("false")
	}
}

// obj writes an object from (key, value-writer) pairs.
type kv struct {
	k string
	f func()
}

func (e *enc) obj(compact bool, pairs ...kv) {
	sep, colon := ", ", ": "
	if compact {
		sep, colon = ",", ":"
	}
	e.b.WriteByte('{')
	for i, p := range pairs {
		if i > 0 {
			e.b.WriteString(sep)
		}
		e.str(p.k)
		e.b.WriteString(colon)
		p.f()
	}
	e.b.WriteByte('}')
}

func (e *enc) list(compact bool, n int, item func(i int)) {
	sep := ", "
	if compact {
		sep = ","
	}
	e.b.WriteByte('[')
	for i := 0; i < n; i++ {
		if i > 0 {
			e.b.WriteString(sep)
		}
		item(i)
	}
	e.b.WriteByte(']')
}

func (e *enc) floats(compact bool, v []float64) {
	e.list(compact, len(v), func(i int) { e.num(v[i]) })
}

func (e *enc) spans(compact bool, spans []Span) {
	e.list(compact, len(spans), func(i int) {
		s := spans[i]
		e.obj(compact,
			kv{"t", func() { e.str(s.T) }}, kv{"font", func() { e.str(s.Font) }}, kv{"size", func() { e.num(s.Size) }},
			kv{"bold", func() { e.boolean(s.Bold) }}, kv{"italic", func() { e.boolean(s.Italic) }},
			kv{"underline", func() { e.boolean(s.Underline) }}, kv{"color", func() { e.str(s.Color) }},
			kv{"legacy", func() { e.boolean(s.Legacy) }})
	})
}

func (e *enc) blocks(compact bool, blocks []Block) {
	e.list(compact, len(blocks), func(i int) {
		b := blocks[i]
		if b.Kind == "table" {
			e.obj(compact,
				kv{"kind", func() { e.str("table") }},
				kv{"cols", func() { e.floats(compact, b.Cols) }},
				kv{"rows", func() {
					e.list(compact, len(b.Rows), func(r int) {
						row := b.Rows[r]
						e.obj(compact, kv{"cells", func() {
							e.list(compact, len(row.Cells), func(c int) {
								cell := row.Cells[c]
								e.obj(compact,
									kv{"col", func() { e.integer(cell.Col) }},
									kv{"blocks", func() { e.blocks(compact, cell.Blocks) }},
									kv{"colspan", func() { e.integer(cell.Colspan) }},
									kv{"rowspan", func() { e.integer(cell.Rowspan) }},
									kv{"rotate", func() { e.num(cell.Rotate) }})
							})
						}})
					})
				}})
			return
		}
		e.obj(compact,
			kv{"kind", func() { e.str("p") }}, kv{"align", func() { e.str(b.Align) }},
			kv{"indent", func() { e.num(b.Indent) }}, kv{"left", func() { e.num(b.Left) }},
			kv{"right", func() { e.num(b.Right) }}, kv{"before", func() { e.num(b.Before) }},
			kv{"after", func() { e.num(b.After) }},
			kv{"leading", func() {
				if b.Leading == nil {
					e.raw("null")
				} else {
					e.num(*b.Leading)
				}
			}},
			kv{"spans", func() { e.spans(compact, b.Spans) }})
	})
}

func (e *enc) item(compact bool, it *Item) {
	var pairs []kv
	add := func(k string, f func()) { pairs = append(pairs, kv{k, f}) }
	add("type", func() { e.str(it.Type) })
	switch it.Type {
	case "path":
		add("d", func() { e.str(it.D) })
		add("fill", func() { e.str(it.Fill) })
		add("stroke", func() { e.str(it.Stroke) })
		add("sw", func() { e.num(it.SW) })
	default:
		add("x", func() { e.num(it.X) })
		add("y", func() { e.num(it.Y) })
		add("w", func() { e.num(it.W) })
		add("h", func() { e.num(it.H) })
		add("rotate", func() { e.num(it.Rotate) })
		if it.Origin != "" {
			add("origin", func() { e.str(it.Origin) })
		}
		if it.Type == "text" {
			add("story", func() { e.str(it.Story) })
			add("inset", func() { e.floats(compact, it.Inset[:]) })
			if it.Valign != "" {
				add("valign", func() { e.str(it.Valign) })
			}
			add("nowrap", func() { e.boolean(it.Nowrap) })
		} else {
			if it.Src != "" {
				add("src", func() { e.str(it.Src) })
			} else {
				add("label", func() { e.str(it.Label) })
			}
		}
	}
	if it.Repeat {
		add("repeat", func() { e.boolean(true) })
	}
	e.obj(compact, pairs...)
}

// JSON is the document as the File reader page embeds it.
func (d *Doc) JSON() []byte {
	e := &enc{ascii: true}
	e.obj(false,
		kv{"format", func() { e.str(d.Format) }},
		kv{"pages", func() {
			e.list(false, len(d.Pages), func(i int) {
				p := d.Pages[i]
				e.obj(false,
					kv{"width", func() { e.num(p.Width) }}, kv{"height", func() { e.num(p.Height) }},
					kv{"items", func() { e.list(false, len(p.Items), func(j int) { e.item(false, p.Items[j]) }) }})
			})
		}},
		kv{"stories", func() {
			keys := d.Stories.Keys()
			e.b.WriteByte('{')
			for i, k := range keys {
				if i > 0 {
					e.b.WriteString(", ")
				}
				e.str(k)
				e.b.WriteString(": ")
				e.blocks(false, d.Stories.Get(k))
			}
			e.b.WriteByte('}')
		}},
		kv{"warnings", func() {
			e.list(false, len(d.Warnings), func(i int) { e.str(d.Warnings[i]) })
		}},
		kv{"name", func() { e.str(d.Name) }})
	return []byte(e.b.String())
}

// MarshalJSON lets the document go through encoding/json untouched.
func (d *Doc) MarshalJSON() ([]byte, error) { return d.JSON(), nil }

// Rounded cuts every length in the document to 2 decimals, to keep the page small.
func (d *Doc) Rounded() {
	r := func(v *float64) { *v = pyRound(*v, 2) }
	var blocks func(bs []Block)
	blocks = func(bs []Block) {
		for i := range bs {
			b := &bs[i]
			if b.Kind == "table" {
				for k := range b.Cols {
					r(&b.Cols[k])
				}
				for ri := range b.Rows {
					for ci := range b.Rows[ri].Cells {
						c := &b.Rows[ri].Cells[ci]
						r(&c.Rotate)
						blocks(c.Blocks)
					}
				}
				continue
			}
			r(&b.Indent)
			r(&b.Left)
			r(&b.Right)
			r(&b.Before)
			r(&b.After)
			if b.Leading != nil {
				v := pyRound(*b.Leading, 2)
				b.Leading = &v
			}
			for k := range b.Spans {
				r(&b.Spans[k].Size)
			}
		}
	}
	for _, p := range d.Pages {
		r(&p.Width)
		r(&p.Height)
		for _, it := range p.Items {
			r(&it.X)
			r(&it.Y)
			r(&it.W)
			r(&it.H)
			r(&it.Rotate)
			r(&it.SW)
			for k := range it.Inset {
				r(&it.Inset[k])
			}
		}
	}
	for _, k := range d.Stories.Keys() {
		blocks(d.Stories.m[k])
	}
}

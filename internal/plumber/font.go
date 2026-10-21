package plumber

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"

	pdf "github.com/shahinshabab/deedflow-go/internal/plumber/rawpdf"
)

type fontMetrics struct {
	Ascent, Descent float64
	Widths          map[string]float64
}

// pdfFont is pdfminer's PDFFont family in one type: what it takes to turn the
// bytes of a shown string into characters and advance widths.
type pdfFont struct {
	name         string // the descriptor's FontName, "unknown" if absent
	multibyte    bool
	vertical     bool
	widths       map[int]float64
	textWidths   map[string]float64 // standard fonts: width by character
	defaultWidth float64
	ascent       float64
	descent      float64
	hscale       float64
	vscale       float64
	encoding     map[int]string // simple fonts: code -> text
	unicodeMap   map[int]string // from ToUnicode (nil when absent)
	identityUni  bool           // ToUnicode given as an Identity name: code point = cid
}

func (f *pdfFont) isMultibyte() bool { return f.multibyte }

// decode splits a shown string into character codes.
func (f *pdfFont) decode(b []byte) []int {
	if !f.multibyte {
		out := make([]int, len(b))
		for i, c := range b {
			out[i] = int(c)
		}
		return out
	}
	// Identity-H/V: two bytes per code.
	out := make([]int, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		out = append(out, int(b[i])<<8|int(b[i+1]))
	}
	return out
}

func (f *pdfFont) toUnichr(cid int) (string, bool) {
	if f.multibyte {
		if f.identityUni {
			return string(rune(cid)), true
		}
		if f.unicodeMap != nil {
			if t, ok := f.unicodeMap[cid]; ok {
				return t, true
			}
		}
		return "", false
	}
	if f.unicodeMap != nil {
		if t, ok := f.unicodeMap[cid]; ok {
			return t, true
		}
	}
	t, ok := f.encoding[cid]
	return t, ok
}

// charWidth is the advance of a character in text space units (1 = font size).
func (f *pdfFont) charWidth(cid int) float64 {
	if w, ok := f.widths[cid]; ok {
		return w * f.hscale
	}
	if f.textWidths != nil {
		if t, ok := f.toUnichr(cid); ok {
			if w, ok := f.textWidths[t]; ok {
				return w * f.hscale
			}
		}
	}
	return f.defaultWidth * f.hscale
}

func (f *pdfFont) getDescent() float64 { return f.descent * f.vscale }

func numOr(v pdf.Value, def float64) float64 {
	switch v.Kind() {
	case pdf.Integer, pdf.Real:
		return v.Float64()
	}
	return def
}

func parseDiffs(enc pdf.Value) []diffItem {
	d := enc.Key("Differences")
	var out []diffItem
	for i := 0; i < d.Len(); i++ {
		it := d.Index(i)
		switch it.Kind() {
		case pdf.Integer, pdf.Real:
			out = append(out, diffItem{isNum: true, num: int(it.Float64())})
		case pdf.Name:
			out = append(out, diffItem{name: it.Name()})
		}
	}
	return out
}

var type1Dup = regexp.MustCompile(`dup\s+(\d+)\s*/(\S+)\s+put`)

// loadFont builds the font for a font dictionary (PDFResourceManager.get_font).
func loadFont(spec pdf.Value) *pdfFont {
	sub := spec.Key("Subtype").Name()
	switch sub {
	case "Type0":
		desc := spec.Key("DescendantFonts")
		if desc.Len() == 0 {
			return nil
		}
		return loadCIDFont(desc.Index(0), spec.Key("Encoding"), spec.Key("ToUnicode"))
	case "CIDFontType0", "CIDFontType2":
		return loadCIDFont(spec, spec.Key("Encoding"), spec.Key("ToUnicode"))
	case "Type3":
		return loadType3(spec)
	}
	return loadSimple(spec)
}

func loadSimple(spec pdf.Value) *pdfFont {
	f := &pdfFont{hscale: 0.001, vscale: 0.001, name: "unknown"}
	basefont := "unknown"
	if bf := spec.Key("BaseFont"); bf.Kind() == pdf.Name {
		basefont = bf.Name()
	}
	var descriptor pdf.Value
	if m, ok := std14Metrics[basefont]; ok {
		f.name = basefont
		f.ascent, f.descent = m.Ascent, m.Descent
		f.textWidths = m.Widths
		f.widths = map[int]float64{}
		f.defaultWidth = 0
	} else {
		descriptor = spec.Key("FontDescriptor")
		first := int(numOr(spec.Key("FirstChar"), 0))
		f.widths = map[int]float64{}
		w := spec.Key("Widths")
		if w.Kind() == pdf.Array {
			for i := 0; i < w.Len(); i++ {
				f.widths[first+i] = numOr(w.Index(i), 0)
			}
		} else {
			for i := 0; i < 256; i++ {
				f.widths[first+i] = 0
			}
		}
		fillDescriptor(f, descriptor)
	}

	// Encoding: a name, or a dictionary with BaseEncoding and Differences.
	defaultEnc := "StandardEncoding"
	if spec.Key("Subtype").Name() == "TrueType" {
		defaultEnc = "WinAnsiEncoding"
	}
	enc := spec.Key("Encoding")
	switch enc.Kind() {
	case pdf.Dict:
		base := defaultEnc
		if b := enc.Key("BaseEncoding"); b.Kind() == pdf.Name {
			base = b.Name()
		}
		f.encoding = encodingTable(base, parseDiffs(enc))
	case pdf.Name:
		f.encoding = encodingTable(enc.Name(), nil)
	default:
		f.encoding = encodingTable(defaultEnc, nil)
	}
	if tu := spec.Key("ToUnicode"); tu.Kind() == pdf.Stream {
		if data, err := tu.Bytes(); err == nil {
			f.unicodeMap = parseToUnicode(data)
		}
	}
	// No /Encoding but an embedded Type 1 program: its header carries one.
	if enc.Kind() == pdf.Null && descriptor.Key("FontFile").Kind() == pdf.Stream {
		if data, err := descriptor.Key("FontFile").Bytes(); err == nil {
			if n := int(descriptor.Key("FontFile").Key("Length1").Int64()); n > 0 && n < len(data) {
				data = data[:n]
			}
			table := map[int]string{}
			for _, m := range type1Dup.FindAllStringSubmatch(string(data), -1) {
				code, _ := strconv.Atoi(m[1])
				if t, ok := name2unicode(m[2]); ok {
					table[code] = t
				}
			}
			if len(table) > 0 {
				f.encoding = table
			}
		}
	}
	return f
}

func fillDescriptor(f *pdfFont, d pdf.Value) {
	if n := d.Key("FontName"); n.Kind() == pdf.Name {
		f.name = n.Name()
	} else if n.Kind() == pdf.String {
		f.name = n.RawString()
	}
	f.ascent = numOr(d.Key("Ascent"), 0)
	f.descent = numOr(d.Key("Descent"), 0)
	f.defaultWidth = numOr(d.Key("MissingWidth"), 0)
	// PDF RM 9.8.1 specifies /Descent should always be negative; PScript5.dll
	// writes it positive, which would break text analysis.
	if f.descent > 0 {
		f.descent = -f.descent
	}
}

func loadType3(spec pdf.Value) *pdfFont {
	f := &pdfFont{hscale: 0.001, vscale: 0.001, name: "unknown", widths: map[int]float64{}}
	first := int(numOr(spec.Key("FirstChar"), 0))
	w := spec.Key("Widths")
	if w.Kind() == pdf.Array {
		for i := 0; i < w.Len(); i++ {
			f.widths[first+i] = numOr(w.Index(i), 0)
		}
	} else {
		for i := 0; i < 256; i++ {
			f.widths[first+i] = 0
		}
	}
	bbox := spec.Key("FontBBox").Floats()
	if d := spec.Key("FontDescriptor"); d.Kind() == pdf.Dict {
		fillDescriptor(f, d)
		if bb := d.Key("FontBBox").Floats(); len(bb) == 4 {
			bbox = bb
		}
	}
	if len(bbox) == 4 {
		f.descent, f.ascent = bbox[1], bbox[3]
	}
	if m := spec.Key("FontMatrix").Floats(); len(m) == 6 {
		f.hscale = m[0] + m[2]
		f.vscale = m[1] + m[3]
	}
	enc := spec.Key("Encoding")
	switch enc.Kind() {
	case pdf.Dict:
		base := "StandardEncoding"
		if b := enc.Key("BaseEncoding"); b.Kind() == pdf.Name {
			base = b.Name()
		}
		f.encoding = encodingTable(base, parseDiffs(enc))
	case pdf.Name:
		f.encoding = encodingTable(enc.Name(), nil)
	default:
		f.encoding = encodingTable("StandardEncoding", nil)
	}
	if tu := spec.Key("ToUnicode"); tu.Kind() == pdf.Stream {
		if data, err := tu.Bytes(); err == nil {
			f.unicodeMap = parseToUnicode(data)
		}
	}
	return f
}

func loadCIDFont(spec, encoding, toUnicode pdf.Value) *pdfFont {
	f := &pdfFont{hscale: 0.001, vscale: 0.001, name: "unknown", multibyte: true, widths: map[int]float64{}}
	if d := spec.Key("FontDescriptor"); d.Kind() == pdf.Dict {
		fillDescriptor(f, d)
	}
	cmapName := ""
	switch encoding.Kind() {
	case pdf.Name:
		cmapName = encoding.Name()
	case pdf.Stream, pdf.Dict:
		cmapName = encoding.Key("CMapName").Name()
	}
	f.vertical = strings.HasSuffix(cmapName, "-V") || cmapName == "V"
	ordering := ""
	if o := spec.Key("CIDSystemInfo").Key("Ordering"); o.Kind() == pdf.String {
		ordering = o.RawString()
	}
	switch toUnicode.Kind() {
	case pdf.Stream:
		if data, err := toUnicode.Bytes(); err == nil {
			f.unicodeMap = parseToUnicode(data)
		}
	case pdf.Name:
		if strings.Contains(ordering, "Identity") || strings.Contains(toUnicode.Name(), "Identity") || strings.Contains(cmapName, "Identity") {
			f.identityUni = true
		}
	}
	// Widths: /W as [c [w w ..]] and [c1 c2 w] entries; /DW the default.
	f.defaultWidth = numOr(spec.Key("DW"), 1000)
	w := spec.Key("W")
	var run []float64
	for i := 0; i < w.Len(); i++ {
		it := w.Index(i)
		switch it.Kind() {
		case pdf.Array:
			if len(run) > 0 {
				c1 := int(run[len(run)-1])
				for j := 0; j < it.Len(); j++ {
					f.widths[c1+j] = numOr(it.Index(j), 0)
				}
				run = nil
			}
		case pdf.Integer, pdf.Real:
			run = append(run, it.Float64())
			if len(run) == 3 {
				for c := int(run[0]); c <= int(run[1]) && c-int(run[0]) < 70000; c++ {
					f.widths[c] = run[2]
				}
				run = nil
			}
		}
	}
	return f
}

// --- ToUnicode CMaps ---------------------------------------------------------------

func nunpack(b []byte) int {
	n := 0
	for _, c := range b {
		n = n<<8 | int(c)
	}
	return n
}

func utf16beDecode(b []byte) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, uint16(b[i])<<8|uint16(b[i+1]))
	}
	return string(utf16.Decode(u))
}

// parseToUnicode reads a ToUnicode CMap's bfchar / bfrange entries (what
// pdfminer's CMapParser puts into a FileUnicodeMap).
func parseToUnicode(data []byte) map[int]string {
	m := map[int]string{}
	l := &lexer{data: data}
	var operands []any
	for {
		t := l.next()
		if t.kind == tEOF {
			return m
		}
		if t.kind != tOp {
			operands = append(operands, l.value(t))
			continue
		}
		switch op := string(t.str); op {
		case "endbfchar":
			for i := 0; i+1 < len(operands); i += 2 {
				src, ok1 := operands[i].([]byte)
				switch dst := operands[i+1].(type) {
				case []byte:
					if ok1 {
						m[nunpack(src)] = utf16beDecode(dst)
					}
				case name:
					if ok1 {
						if t, ok := name2unicode(string(dst)); ok {
							m[nunpack(src)] = t
						}
					}
				}
			}
			operands = nil
		case "endbfrange":
			for i := 0; i+2 < len(operands); i += 3 {
				s, ok1 := operands[i].([]byte)
				e, ok2 := operands[i+1].([]byte)
				if !ok1 || !ok2 || len(s) != len(e) {
					continue
				}
				s1, e1 := nunpack(s), nunpack(e)
				if e1-s1 > 70000 {
					continue
				}
				switch dst := operands[i+2].(type) {
				case []any:
					for k := 0; k <= e1-s1 && k < len(dst); k++ {
						if b, ok := dst[k].([]byte); ok {
							m[s1+k] = utf16beDecode(b)
						}
					}
				case []byte:
					if len(dst) == 0 {
						continue
					}
					prefix, variable := dst, []byte(nil)
					if len(dst) > 4 {
						prefix, variable = dst[:len(dst)-4], dst[len(dst)-4:]
					} else {
						prefix, variable = nil, dst
					}
					base := nunpack(variable)
					for k := 0; k <= e1-s1; k++ {
						v := uint32(base + k)
						packed := []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
						x := append(append([]byte{}, prefix...), packed[4-len(variable):]...)
						m[s1+k] = utf16beDecode(x)
					}
				}
			}
			operands = nil
		case "beginbfchar", "beginbfrange", "begincodespacerange", "endcodespacerange", "begincidrange", "endcidrange",
			"begincidchar", "endcidchar", "begincmap", "endcmap", "def", "usecmap", "beginnotdefrange", "endnotdefrange":
			operands = nil
		}
	}
}

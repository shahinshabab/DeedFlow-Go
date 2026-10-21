package pmd

import (
	"fmt"
	"math"
)

// parser is PMDParser: it reads the table of contents, then the fonts,
// colours, transforms, the global info and the pages with their shapes.
type parser struct {
	in             *stream
	length         int
	col            *collector
	records        map[uint16][]int
	bigEndian      bool
	recordsInOrder []recordContainer
	xforms         map[uint32]xform
}

type tocState struct {
	parsed map[int]bool
	seqNum uint
}

func newParser(in *stream, col *collector) *parser {
	return &parser{in: in, length: in.length(), col: col, records: map[uint16][]int{}, xforms: map[uint32]xform{}}
}

// --- record iteration (PMDParser::RecordIterator) --------------------------------

// withSeqNum yields the containers of a sequence number. The C++ iterator
// steps once before its first look, so the container at index 0 never matches.
func (p *parser) withSeqNum(seq uint16) []recordContainer {
	var out []recordContainer
	n := len(p.recordsInOrder)
	if n == 0 {
		return nil
	}
	i := 0
	for {
		i++
		for i < n && p.recordsInOrder[i].seqNum != uint(seq) {
			i++
		}
		if i >= n {
			return out
		}
		out = append(out, p.recordsInOrder[i])
	}
}

func (p *parser) ofType(recType uint16) []recordContainer {
	var out []recordContainer
	for _, idx := range p.records[recType] {
		out = append(out, p.recordsInOrder[idx])
	}
	return out
}

// --- reading helpers ------------------------------------------------------------------

func (p *parser) seekToRecord(c recordContainer, index uint) {
	offset := c.offset
	if index > 0 {
		size, ok := recordSize(c.recType)
		if !ok {
			panic(&parseError{fmt.Sprintf("Tried to parse record %d of unknown size.\n", c.recType)})
		}
		offset += uint32(size) * uint32(index)
	}
	p.in.seekTo(int64(offset))
}

func (p *parser) readPoint() point {
	x := unit(p.in.s16(p.bigEndian))
	y := unit(p.in.s16(p.bigEndian))
	if p.bigEndian {
		return point{y, x}
	}
	return point{x, y}
}

func (p *parser) readDims() (x, y int16) {
	dim1 := p.in.s16(p.bigEndian)
	dim2 := p.in.s16(p.bigEndian)
	if p.bigEndian {
		return dim2, dim1
	}
	return dim1, dim2
}

func (p *parser) readRule() *strokeProps {
	flags := p.in.u16(p.bigEndian)
	if flags&0x1 == 0 {
		p.in.skip(18)
		return nil
	}
	s := newStroke()
	s.strokeType = p.in.u8()
	p.in.skip(1)
	s.strokeWidth = uint16(p.in.u32(p.bigEndian) >> 8)
	s.strokeColor = uint8(p.in.u16(p.bigEndian))
	s.strokeTint = uint8(p.in.u16(p.bigEndian))
	p.in.skip(6)
	return &s
}

func (p *parser) getXForm(id uint32) xform {
	if id != math.MaxUint32 && id != 0 {
		if x, ok := p.xforms[id]; ok {
			return x
		}
	}
	return p.xforms[0]
}

// --- parts ---------------------------------------------------------------------------------

func (p *parser) parseGlobalInfo(c recordContainer) {
	p.seekToRecord(c, 0)
	opts := uint(p.in.u8())
	p.in.skip(0x35)
	left, top := p.readDims()
	right, bottom := p.readDims()
	if p.bigEndian {
		p.col.doubleSided = opts&0x40 != 0
	} else {
		p.col.doubleSided = opts&0x2 != 0
	}
	p.col.pageWidth = unit(int(right) - int(left))
	p.col.pageHeight = unit(int(bottom) - int(top))
	p.col.hasPage = true
}

func (p *parser) parseLine(c recordContainer, index uint, pageID int) {
	p.seekToRecord(c, index)
	stroke := newStroke()
	p.in.skip(4)
	stroke.strokeColor = p.in.u8()
	p.in.skip(1)
	tl := p.readPoint()
	br := p.readPoint()
	mirrored := false
	p.in.skip(0x18)
	temp := p.in.u16(p.bigEndian)
	if temp != 257 && temp != 0 {
		mirrored = true
	}
	p.in.skip(6)
	stroke.strokeType = p.in.u8()
	p.in.skip(1)
	stroke.strokeWidth = p.in.u16(p.bigEndian)
	p.in.skip(1)
	stroke.strokeTint = p.in.u8()
	p.in.skip(6)
	stroke.strokeOverprint = p.in.u8()
	p.col.addShape(pageID, &shape{kind: shapeTypeLine, bboxTL: tl, bboxBR: br, mirrored: mirrored, stroke: stroke})
}

func (p *parser) parseTextBox(c recordContainer, index uint, pageID int) {
	p.seekToRecord(c, index)
	p.in.skip(6)
	tl := p.readPoint()
	br := p.readPoint()

	var textBoxText, textBoxChars, textBoxPara uint16

	p.in.skip(0xe)
	xformID := p.in.u32(p.bigEndian)
	textBlockID := p.in.u32(p.bigEndian)
	xf := p.getXForm(xformID)

	for _, tb := range p.ofType(recTextBlock) {
		for i := uint(0); i < uint(tb.numRecords); i++ {
			p.seekToRecord(tb, i)
			p.in.skip(0x20)
			id := p.in.u32(p.bigEndian)
			if id == textBlockID {
				p.seekToRecord(tb, i) // return to the beginning of the record
				p.in.u16(p.bigEndian)
				p.in.u16(p.bigEndian)
				textBoxText = p.in.u16(p.bigEndian)
				textBoxChars = p.in.u16(p.bigEndian)
				textBoxPara = p.in.u16(p.bigEndian)
				p.in.u16(p.bigEndian)
				break
			}
		}
	}

	var text []byte
	for _, tc := range p.withSeqNum(textBoxText) {
		p.seekToRecord(tc, 0)
		for i := uint(0); i < uint(tc.numRecords); i++ {
			text = append(text, p.in.u8())
		}
	}

	var chars []charProps
	for _, cc := range p.withSeqNum(textBoxChars) {
		for i := uint(0); i < uint(cc.numRecords); i++ {
			p.seekToRecord(cc, i)
			var props charProps
			props.length = p.in.u16(p.bigEndian)
			props.fontFace = p.in.u16(p.bigEndian)
			props.fontSize = p.in.u16(p.bigEndian)
			props.leading = p.in.u16(p.bigEndian) // DeedFlow patch: was skipped
			props.fontColor = p.in.u16(p.bigEndian)
			flags := uint(p.in.u16(p.bigEndian))
			props.bold = flags&0x1 != 0
			props.italic = flags&0x2 != 0
			props.underline = flags&0x4 != 0
			props.outline = flags&0x8 != 0
			props.shadow = flags&0x10 != 0
			props.strike = flags&0x100 != 0
			props.super = flags&0x200 != 0
			props.sub = flags&0x400 != 0
			props.allCaps = flags&0x800 != 0
			props.smallCaps = flags&0x1000 != 0
			p.in.skip(4)
			props.kerning = p.in.s16(p.bigEndian)
			p.in.skip(2)
			props.superSubSize = p.in.u16(p.bigEndian)
			props.subPos = p.in.u16(p.bigEndian)
			props.superPos = p.in.u16(p.bigEndian)
			p.in.skip(2)
			props.tint = p.in.u16(p.bigEndian)
			chars = append(chars, props)
		}
	}

	var paras []paraProps
	for _, pc := range p.withSeqNum(textBoxPara) {
		for i := uint(0); i < uint(pc.numRecords); i++ {
			p.seekToRecord(pc, i)
			var props paraProps
			props.length = p.in.u16(p.bigEndian)
			flags := uint(p.in.u8())
			props.hyphenate = flags&0x8 != 0
			props.align = p.in.u8()
			p.in.skip(6)
			props.leftIndent = p.in.u16(p.bigEndian)
			props.firstIndent = p.in.u16(p.bigEndian)
			props.rightIndent = p.in.u16(p.bigEndian)
			props.beforeIndent = p.in.u16(p.bigEndian) // Above Para Spacing
			props.afterIndent = p.in.u16(p.bigEndian)  // Below Para Spacing
			p.in.skip(18)
			props.hyphensCount = uint16(p.in.u8())
			p.in.skip(1)
			keepOpts := uint(p.in.u16(p.bigEndian))
			props.keepTogether = keepOpts&0x1 != 0
			props.keepWithNext = uint16((keepOpts >> 1) & 0x3)
			props.widows = uint16((keepOpts >> 4) & 0x3)
			props.orphans = uint16((keepOpts >> 7) & 0x3)
			p.in.skip(2)
			props.ruleAbove = p.readRule()
			props.ruleBelow = p.readRule()
			paras = append(paras, props)
		}
	}

	p.col.addShape(pageID, &shape{kind: shapeTypeTextbox, bboxTL: tl, bboxBR: br, xf: xf, text: string(text),
		chars: chars, paras: paras, storyID: textBlockID, stroke: newStroke()})
}

// parseFilled reads what rectangles, polygons and ellipses share up to the
// stroke and fill properties.
func (p *parser) readFillStroke(fill *fillProps, stroke *strokeProps) {
	stroke.strokeType = p.in.u8()
	p.in.skip(2)
	stroke.strokeWidth = p.in.u16(p.bigEndian)
	p.in.skip(1)
	fill.fillType = p.in.u8()
	p.in.skip(1)
	stroke.strokeColor = p.in.u8()
	p.in.skip(1)
	stroke.strokeOverprint = p.in.u8()
	p.in.skip(1)
	stroke.strokeTint = p.in.u8()
}

func (p *parser) parseRectangle(c recordContainer, index uint, pageID int) {
	p.seekToRecord(c, index)
	var fill fillProps
	stroke := newStroke()

	p.in.skip(2)
	fill.fillOverprint = p.in.u8()
	p.in.skip(1)
	fill.fillColor = p.in.u8()
	p.in.skip(1)
	tl := p.readPoint()
	br := p.readPoint()
	p.in.skip(14)
	xformID := p.in.u32(p.bigEndian)
	p.readFillStroke(&fill, &stroke)
	p.in.skip(0xb3)
	fill.fillTint = p.in.u8()
	p.col.addShape(pageID, &shape{kind: shapeTypeRect, bboxTL: tl, bboxBR: br, xf: p.getXForm(xformID), fill: fill, stroke: stroke})
}

func (p *parser) parsePolygon(c recordContainer, index uint, pageID int) {
	p.seekToRecord(c, index)
	var fill fillProps
	stroke := newStroke()

	p.in.skip(2)
	fill.fillOverprint = p.in.u8()
	p.in.skip(1)
	fill.fillColor = p.in.u8()
	p.in.skip(1)
	tl := p.readPoint()
	br := p.readPoint()
	p.in.skip(14)
	xformID := p.in.u32(p.bigEndian)
	p.readFillStroke(&fill, &stroke)
	p.in.skip(1)
	lineSetSeq := p.in.u16(p.bigEndian)
	p.in.skip(8)
	closedMarker := p.in.u8()
	p.in.skip(0xa7)
	fill.fillTint = p.in.u8()

	closed := true
	switch closedMarker {
	case polygonOpen:
		closed = false
	}

	var points []point
	for _, ls := range p.withSeqNum(lineSetSeq) {
		for i := uint(0); i < uint(ls.numRecords); i++ {
			p.seekToRecord(ls, i)
			points = append(points, p.readPoint())
		}
	}
	p.col.addShape(pageID, &shape{kind: shapeTypePoly, points: points, closed: closed, bboxTL: tl, bboxBR: br,
		xf: p.getXForm(xformID), fill: fill, stroke: stroke})
}

func (p *parser) parseEllipse(c recordContainer, index uint, pageID int) {
	p.seekToRecord(c, index)
	var fill fillProps
	stroke := newStroke()

	p.in.skip(2)
	fill.fillOverprint = p.in.u8()
	p.in.skip(1)
	fill.fillColor = p.in.u8()
	p.in.skip(1)
	tl := p.readPoint()
	br := p.readPoint()
	p.in.skip(14)
	xformID := p.in.u32(p.bigEndian)
	p.readFillStroke(&fill, &stroke)
	p.in.skip(0xb3)
	fill.fillTint = p.in.u8()
	p.col.addShape(pageID, &shape{kind: shapeTypeEllipse, bboxTL: tl, bboxBR: br, xf: p.getXForm(xformID), fill: fill, stroke: stroke})
}

func (p *parser) parseBitmap(c recordContainer, index uint, pageID int) {
	var bitmap []byte
	p.seekToRecord(c, index)
	p.in.skip(6)
	tl := p.readPoint()
	br := p.readPoint()
	p.in.skip(14)
	xformID := p.in.u32(p.bigEndian)
	p.in.skip(16)
	seq := p.in.u16(p.bigEndian)
	xf := p.getXForm(xformID)

	first := p.withSeqNum(seq)
	if len(first) == 0 {
		panic(&parseError{fmt.Sprintf("Record of type %d not found at seqNum %d", recTIFF, seq)})
	}
	for _, t := range first {
		p.seekToRecord(t, 0)
		bitmap = append(bitmap, p.in.bytesN(uint64(t.numRecords))...)
	}
	second := p.withSeqNum(uint16(int(seq) + 1))
	if len(second) == 0 {
		panic(&parseError{fmt.Sprintf("Record of type %d not found at seqNum %d", recTIFF, seq)})
	}
	for _, t := range second {
		p.seekToRecord(t, 0)
		bitmap = append(bitmap, p.in.bytesN(uint64(t.numRecords))...)
	}
	p.col.addShape(pageID, &shape{kind: shapeTypeBitmap, bboxTL: tl, bboxBR: br, xf: xf, bitmap: bitmap, stroke: newStroke()})
}

func (p *parser) parseShapes(seq uint16, pageID int) {
	for _, c := range p.withSeqNum(seq) {
		for i := uint(0); i < uint(c.numRecords); i++ {
			p.seekToRecord(c, i)
			switch p.in.u8() {
			case lineRecord:
				p.parseLine(c, i, pageID)
			case rectangleRecord:
				p.parseRectangle(c, i, pageID)
			case polygonRecord:
				p.parsePolygon(c, i, pageID)
			case ellipseRecord:
				p.parseEllipse(c, i, pageID)
			case textRecord:
				p.parseTextBox(c, i, pageID)
			case bitmapRecord, metafileRecord:
				p.parseBitmap(c, i, pageID)
			default:
				continue
			}
		}
	}
}

func (p *parser) parseFonts() {
	var fontIndex uint
	for _, c := range p.ofType(recFonts) {
		for i := uint(0); i < uint(c.numRecords); i++ {
			p.seekToRecord(c, i)
			var name []byte
			t := p.in.u8()
			for t != 0 {
				name = append(name, t)
				t = p.in.u8()
			}
			p.col.fonts = append(p.col.fonts, font{fontIndex, string(name)})
			fontIndex++
		}
	}
}

func (p *parser) parseColors() {
	for _, c := range p.ofType(recColors) {
		for i := uint(0); i < uint(c.numRecords); i++ {
			p.seekToRecord(c, i)
			p.in.skip(0x22)
			model := p.in.u8()
			var red, green, blue uint8
			p.in.skip(3)
			if model == colorRGB {
				red = p.in.u8()
				green = p.in.u8()
				blue = p.in.u8()
			} else if model == colorCMYK || model == colorHLS { // HLS is also stored in CMYK format
				cyan := p.in.u16(p.bigEndian)
				magenta := p.in.u16(p.bigEndian)
				yellow := p.in.u16(p.bigEndian)
				black := p.in.u16(p.bigEndian)
				const max = 65535.0
				red = uint8(255 * (1 - math.Min(1.0, float64(cyan)/max+float64(black)/max)))
				green = uint8(255 * (1 - math.Min(1.0, float64(magenta)/max+float64(black)/max)))
				blue = uint8(255 * (1 - math.Min(1.0, float64(yellow)/max+float64(black)/max)))
			}
			p.col.colors = append(p.col.colors, color{i, uint16(red), uint16(green), uint16(blue)})
		}
	}
}

func (p *parser) parseXforms() {
	for _, c := range p.ofType(recXForm) {
		for i := uint(0); i < uint(c.numRecords); i++ {
			p.seekToRecord(c, i)
			rotation := p.in.u32(p.bigEndian)
			skew := p.in.u32(p.bigEndian)
			p.in.skip(2)
			tl := p.readPoint()
			br := p.readPoint()
			rot := p.readPoint()
			id := p.in.u32(p.bigEndian)
			if _, exists := p.xforms[id]; !exists {
				p.xforms[id] = xform{rotation, skew, tl, br, rot, id}
			}
		}
	}
	if _, exists := p.xforms[0]; !exists {
		p.xforms[0] = xform{} // the default transform
	}
}

func (p *parser) parsePages(c recordContainer) {
	p.seekToRecord(c, 0)
	p.in.skip(8)
	p.in.u16(p.bigEndian)
	for i := uint(0); i < uint(c.numRecords); i++ {
		p.seekToRecord(c, i)
		p.in.skip(2)
		shapesSeq := p.in.u16(p.bigEndian)
		pageID := p.col.addPage()
		p.parseShapes(shapesSeq, pageID)
	}
}

func (p *parser) parseHeader() (tocOffset uint32, tocLength uint16) {
	p.in.seekTo(endiannessMarkerOffset)
	marker := p.in.u16(false)
	switch marker {
	case endiannessMarker:
		p.bigEndian = false
	case warpedEndiannessMarker:
		p.bigEndian = true
	default:
		panic(&parseError{"Endianness marker is corrupt in PMD header."})
	}
	func() {
		defer streamToParse("Can't find the table of contents length in the header.")
		p.in.seekTo(tocLengthOffset)
		tocLength = p.in.u16(p.bigEndian)
	}()
	func() {
		defer streamToParse("Can't find the table of contents offset in the header.")
		p.in.seekTo(tocOffsetOffset)
		tocOffset = p.in.u32(p.bigEndian)
	}()
	return
}

// streamToParse turns a stream failure into a parse failure (the try/catch
// around the header reads).
func streamToParse(msg string) {
	if r := recover(); r != nil {
		if r == errEndOfStream || r == errSeek {
			panic(&parseError{msg})
		}
		panic(r)
	}
}

func (p *parser) readNextRecordFromTOC(st *tocState, subRecord bool, subRecordType uint16) {
	p.in.skip(1)
	recType := uint16(p.in.u8())
	numRecs := p.in.u16(p.bigEndian)
	offset := p.in.u32(p.bigEndian)
	p.in.skip(2)

	var subType uint16
	if !subRecord && (recType != 0 || numRecs == 0) {
		p.in.skip(1)
		subType = uint16(p.in.u8())
		p.in.skip(4)
	}

	switch {
	case recType == 0 && numRecs == 0:
		st.seqNum++ // empty record
	case !subRecord && recType == 1:
		p.readTOC(st, offset, uint(numRecs), true, subType)
		st.seqNum++
	case !subRecord && recType == 0:
		p.readTOC(st, offset, uint(numRecs), false, 0)
	default:
		if numRecs != 0 && offset != 0 {
			if subRecord && recType != subRecordType {
				if subRecordType != 0 { // can only happen in a broken file -- better ignore
					recType = subRecordType
				}
			}
			p.recordsInOrder = append(p.recordsInOrder, recordContainer{recType, offset, st.seqNum, numRecs})
			p.records[recType] = append(p.records[recType], len(p.recordsInOrder)-1)
		}
		if !subRecord {
			st.seqNum++
		}
	}
}

func (p *parser) readTOC(st *tocState, offset uint32, records uint, subRecords bool, subRecordType uint16) {
	if st.parsed[p.in.pos] {
		return // the block was already read; the file is probably broken
	}
	st.parsed[p.in.pos] = true
	if records == 0 || offset == 0 {
		return
	}
	orig := p.in.pos
	p.in.seekTo(int64(offset))
	minRecordSize := uint64(16)
	if subRecords {
		minRecordSize = 10
	}
	maxPossible := (uint64(p.length) - uint64(offset)) / minRecordSize
	n := uint64(records)
	if maxPossible < n {
		n = maxPossible
	}
	for i := uint64(0); i < n; i++ {
		p.readNextRecordFromTOC(st, subRecords, subRecordType)
	}
	p.in.seekTo(int64(orig))
}

func (p *parser) parseTOC(offset uint32, length uint16) {
	defer func() { _ = recover() }() // "Error reading the table of contents! Some or all records will be missing."
	st := &tocState{parsed: map[int]bool{}}
	p.readTOC(st, offset, uint(length), false, 0)
}

func (p *parser) parse() {
	tocOffset, tocLength := p.parseHeader()
	p.parseTOC(tocOffset, tocLength)
	p.parseFonts()
	p.parseColors()
	p.parseXforms()

	if idx := p.records[recGlobalInfo]; len(idx) > 0 {
		p.parseGlobalInfo(p.recordsInOrder[idx[0]])
	} else {
		panic(&parseError{fmt.Sprintf("Record not found: %d", recGlobalInfo)})
	}
	if idx := p.records[recPage]; len(idx) > 0 {
		p.parsePages(p.recordsInOrder[idx[0]])
	} else {
		panic(&parseError{fmt.Sprintf("Record not found: %d", recPage)})
	}
}

package pmd

import "math"

// Record types.
const (
	recFontsParent = 0x01
	recPage        = 0x05
	recPara        = 0x0b
	recTIFF        = 0x0e
	recLineSet     = 0x11
	recFonts       = 0x13
	recColors      = 0x15
	recGlobalInfo  = 0x18
	recShape       = 0x19
	recTextBlock   = 0x1a
	recChars       = 0x1c
	recXForm       = 0x28
)

// Colour models.
const (
	colorCMYK = 0x08
	colorHLS  = 0x10
	colorRGB  = 0x18
)

// Shape records.
const (
	textRecord      = 0x01
	lineRecord      = 0x03
	rectangleRecord = 0x04
	ellipseRecord   = 0x05
	bitmapRecord    = 0x06
	metafileRecord  = 0x0a
	polygonRecord   = 0x0c
)

// Polygon flags.
const (
	regularPolygon = 0x00
	polygonOpen    = 0x01
	polygonClosed  = 0x03
)

const (
	endiannessMarker       = 0x99FF
	warpedEndiannessMarker = 0xFF99
	tocOffsetOffset        = 0x30
	tocLengthOffset        = 0x2E
	endiannessMarkerOffset = 0x06
	shapeUnitsPerInch      = 1440
)

// Shape types.
const (
	shapeTypeLine    = 1
	shapeTypePoly    = 2
	shapeTypeRect    = 3
	shapeTypeEllipse = 4
	shapeTypeTextbox = 5
	shapeTypeBitmap  = 6
)

// Fill and stroke constants.
const (
	fillNone  = 0
	fillPaper = 1
	fillSolid = 2
)

const (
	strokeNormal         = 0
	strokeLightLight     = 1
	strokeDarkLight      = 2
	strokeLightDark      = 3
	strokeLightDarkLight = 4
	strokeDashed         = 5
	strokeSquareDots     = 6
	strokeCircularDots   = 7
)

// recordSize is getRecordSize: the size of one record of a type, or 0 if unknown.
func recordSize(recType uint16) (uint, bool) {
	switch recType {
	case recShape:
		return 258, true
	case recGlobalInfo:
		return 2496, true
	case recPage:
		return 472, true
	case recLineSet:
		return 4, true
	case recXForm:
		return 26, true
	case recTextBlock:
		return 36, true
	case recChars:
		return 30, true
	case recPara:
		return 80, true
	case recFonts:
		return 94, true
	case recFontsParent:
		return 10, true
	case recColors:
		return 210, true
	}
	return 0, false
}

type recordContainer struct {
	recType    uint16
	offset     uint32
	seqNum     uint
	numRecords uint16
}

// unit is a PMDShapeUnit: 1/1440 inch.
type unit int

func (u unit) inches() float64 { return float64(u) / float64(shapeUnitsPerInch) }

type point struct{ x, y unit }

type inchPoint struct{ x, y float64 }

type color struct {
	i                uint
	red, green, blue uint16
}

type font struct {
	i    uint
	name string
}

type fillProps struct {
	fillType, fillColor, fillOverprint, fillTint uint8
}

type strokeProps struct {
	strokeType                               uint8
	strokeWidth                              uint16
	strokeColor, strokeOverprint, strokeTint uint8
}

func newStroke() strokeProps { return strokeProps{strokeType: strokeNormal} }

type paraProps struct {
	length                               uint16
	align                                uint8
	leftIndent, firstIndent, rightIndent uint16
	beforeIndent, afterIndent            uint16
	orphans, widows, keepWithNext        uint16
	keepTogether, hyphenate              bool
	hyphensCount                         uint16
	ruleAbove, ruleBelow                 *strokeProps
}

type charProps struct {
	length, fontFace, fontSize, leading, fontColor uint16
	bold, italic, underline, outline, shadow       bool
	strike, super, sub, smallCaps, allCaps         bool
	kerning                                        int16
	superSubSize, superPos, subPos, tint           uint16
}

type xform struct {
	rotationDegree, skewDegree uint32
	topLeft, botRight          point
	rotatingPoint              point
	id                         uint32
}

// piVal keeps pi a variable, so pi/180 is rounded the way the C++ double math is.
var piVal = math.Pi

type shape struct {
	kind     uint8
	points   []point // polygon vertices
	closed   bool
	mirrored bool
	bboxTL   point
	bboxBR   point
	xf       xform
	fill     fillProps
	stroke   strokeProps
	text     string
	chars    []charProps
	paras    []paraProps
	storyID  uint32
	bitmap   []byte
}

func (s *shape) rotation() float64 {
	if s.kind == shapeTypeLine {
		return 0
	}
	temp := int32(s.xf.rotationDegree)
	return -1 * float64(temp) / 1000 * (piVal / 180)
}

func (s *shape) skew() float64 {
	if s.kind == shapeTypeLine {
		return 0
	}
	temp := int32(s.xf.skewDegree)
	return -1 * float64(temp) / 1000 * (piVal / 180)
}

func (s *shape) xformTopLeft() point {
	if s.kind == shapeTypeLine {
		return point{}
	}
	return s.xf.topLeft
}

func (s *shape) xformBotRight() point {
	if s.kind == shapeTypeLine {
		return point{}
	}
	return s.xf.botRight
}

func (s *shape) rotatingPoint() point {
	if s.kind == shapeTypeLine {
		return point{}
	}
	return s.xf.rotatingPoint
}

func (s *shape) isClosed() bool {
	switch s.kind {
	case shapeTypeLine:
		return false
	case shapeTypePoly:
		return s.closed
	}
	return true
}

func (s *shape) shapePoints() []point {
	switch s.kind {
	case shapeTypeLine:
		if s.mirrored {
			return []point{{s.bboxBR.x, s.bboxTL.y}, {s.bboxTL.x, s.bboxBR.y}}
		}
		return []point{s.bboxTL, s.bboxBR}
	case shapeTypePoly:
		return s.points
	case shapeTypeTextbox:
		return []point{s.bboxTL}
	case shapeTypeRect, shapeTypeBitmap:
		return []point{s.bboxTL, {s.bboxBR.x, s.bboxTL.y}, s.bboxBR, {s.bboxTL.x, s.bboxBR.y}}
	case shapeTypeEllipse:
		return []point{s.bboxTL, s.bboxBR}
	}
	return nil
}

func (s *shape) fillProperties() fillProps {
	switch s.kind {
	case shapeTypeLine:
		return fillProps{fillType: fillSolid}
	case shapeTypePoly, shapeTypeRect, shapeTypeEllipse:
		return s.fill
	}
	return fillProps{}
}

func (s *shape) strokeProperties() strokeProps {
	switch s.kind {
	case shapeTypeLine, shapeTypePoly, shapeTypeRect, shapeTypeEllipse:
		return s.stroke
	}
	return newStroke()
}

func (s *shape) charProperties() []charProps {
	if s.kind == shapeTypeTextbox {
		return s.chars
	}
	return make([]charProps, 1)
}

func (s *shape) paraProperties() []paraProps {
	if s.kind == shapeTypeTextbox {
		return s.paras
	}
	return make([]paraProps, 1)
}

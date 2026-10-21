package pmd

import "errors"

// errEndOfStream is EndOfStreamException; errSeek is SeekFailedException.
var (
	errEndOfStream = errors.New("end of stream")
	errSeek        = errors.New("seek failed")
)

// parseError is PMDParseException and its relatives: the file is not usable.
type parseError struct{ msg string }

func (e *parseError) Error() string { return e.msg }

// stream is the RVNGStringStream the parser reads: a byte buffer with a
// position, whose reads and seeks behave as librevenge's do.
type stream struct {
	data []byte
	pos  int
}

func (s *stream) isEnd() bool { return s.pos >= len(s.data) }

func (s *stream) check() {
	if s.isEnd() {
		panic(errEndOfStream)
	}
}

func (s *stream) take(n int) []byte {
	s.check()
	if s.pos+n > len(s.data) {
		s.pos = len(s.data)
		panic(errEndOfStream)
	}
	b := s.data[s.pos : s.pos+n]
	s.pos += n
	return b
}

func (s *stream) u8() uint8 { return s.take(1)[0] }
func (s *stream) s8() int8  { return int8(s.u8()) }

func (s *stream) u16(big bool) uint16 {
	b := s.take(2)
	if big {
		return uint16(b[1]) | uint16(b[0])<<8
	}
	return uint16(b[0]) | uint16(b[1])<<8
}
func (s *stream) s16(big bool) int16 { return int16(s.u16(big)) }

func (s *stream) u32(big bool) uint32 {
	b := s.take(4)
	if big {
		return uint32(b[3]) | uint32(b[2])<<8 | uint32(b[1])<<16 | uint32(b[0])<<24
	}
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

func (s *stream) bytesN(n uint64) []byte {
	s.check()
	if uint64(s.pos)+n > uint64(len(s.data)) {
		s.pos = len(s.data)
		panic(errEndOfStream)
	}
	b := s.data[s.pos : s.pos+int(n)]
	s.pos += int(n)
	return b
}

// seekTo is seek(): out of range clamps the position and fails.
func (s *stream) seekTo(pos int64) {
	switch {
	case pos < 0:
		s.pos = 0
		panic(errSeek)
	case pos > int64(len(s.data)):
		s.pos = len(s.data)
		panic(errSeek)
	}
	s.pos = int(pos)
}

func (s *stream) skip(n int64) {
	s.check()
	s.seekTo(int64(s.pos) + n)
}

func (s *stream) length() int { return len(s.data) }

package pmd

import "sort"

// The PageMaker file is an OLE2 compound document with a "PageMaker" stream.
// This is a port of librevenge's OLE storage reader (RVNGOLEStream.cpp, a
// derivative of POLE), keeping its sanity checks and its leniency toward
// damaged files, over an RVNGFileStream-like byte source.

const (
	oleAvail   = 0xffffffff
	oleEof     = 0xfffffffe
	oleBat     = 0xfffffffd
	oleMetaBat = 0xfffffffc
)

var oleMagic = []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}

func readU16(b []byte) uint16 { return uint16(b[0]) | uint16(b[1])<<8 }
func readU32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

type oleHeader struct {
	numBat, startDirent, threshold uint32
	startSbat, numSbat             uint32
	shiftSbat, sizeSbat            uint32
	shiftBbat, sizeBbat            uint32
	startMbat, numMbat             uint32
	blocksBbat                     [109]uint32
}

func (h *oleHeader) computeBlockSizes() {
	h.sizeBbat = uint32(1) << h.shiftBbat
	h.sizeSbat = uint32(1) << h.shiftSbat
}

func (h *oleHeader) valid(fileSize uint64) bool {
	if h.threshold != 4096 {
		return false
	}
	// there must be at least the header, one bat sector and one dirent sector
	if fileSize/uint64(h.sizeBbat) < 3 {
		return false
	}
	if fileSize/uint64(h.sizeBbat) < uint64(h.numMbat) {
		return false
	}
	if h.numBat == 0 {
		return false
	}
	if h.numBat > 109 && uint64(h.numBat) > uint64(h.numMbat)*uint64(h.sizeBbat/4-1)+109 {
		return false
	}
	if h.numBat < 109 && h.numMbat != 0 {
		return false
	}
	if h.shiftSbat > h.shiftBbat {
		return false
	}
	if h.shiftBbat <= 6 {
		return false
	}
	if h.shiftBbat >= 31 {
		return false
	}
	return true
}

type allocTable struct {
	blockSize uint32
	data      []uint64
}

func newAllocTable() *allocTable {
	t := &allocTable{blockSize: 4096}
	t.resize(128)
	return t
}

func (t *allocTable) resize(n int) {
	for len(t.data) < n {
		t.data = append(t.data, oleAvail)
	}
	t.data = t.data[:n]
}

func (t *allocTable) load(buf []byte, length int) {
	t.resize(length / 4)
	for i := range t.data {
		t.data[i] = uint64(readU32(buf[i*4:]))
	}
}

func (t *allocTable) follow(start uint64) []uint64 {
	var chain []uint64
	if start >= uint64(len(t.data)) {
		return chain
	}
	seen := map[uint64]bool{}
	p := start
	for p < uint64(len(t.data)) {
		if p == oleEof || p == oleBat || p == oleMetaBat {
			break
		}
		if seen[p] {
			break
		}
		seen[p] = true
		chain = append(chain, p)
		p = t.data[p]
	}
	return chain
}

type dirEntry struct {
	valid bool
	typ   byte
	size  uint64
	start uint64
	right uint32
	left  uint32
	child uint32
	name  string
}

func (e *dirEntry) isDir() bool { return e.typ == 1 || e.typ == 5 }

// simpleName is DirEntry::name: a leading control character (or high byte,
// as char is signed) is dropped.
func (e *dirEntry) simpleName() string {
	if len(e.name) > 0 && int8(e.name[0]) < 32 {
		return e.name[1:]
	}
	return e.name
}

func loadDirEntry(buf []byte) dirEntry {
	e := dirEntry{right: 0xffffffff, left: 0xffffffff, child: 0xffffffff}
	e.typ = buf[0x42]
	nameLen := int(readU16(buf[0x40:]))
	if nameLen > 64 {
		nameLen = 64
	}
	if nameLen == 2 && e.typ == 5 && readU16(buf) == 0x5200 {
		e.name = "R"
	} else {
		var nm []byte
		for j := 0; j < nameLen && buf[j] != 0; j += 2 {
			nm = append(nm, buf[j])
		}
		e.name = string(nm)
	}
	e.valid = true
	e.start = uint64(readU32(buf[0x74:]))
	e.size = uint64(readU32(buf[0x78:]))
	e.left = readU32(buf[0x44:])
	e.right = readU32(buf[0x48:])
	e.child = readU32(buf[0x4c:])
	if e.typ != 2 && e.typ != 1 && e.typ != 5 {
		e.valid = false
	}
	if nameLen < 1 {
		e.valid = false
	}
	return e
}

// Storage is an opened OLE2 compound document (librevenge's IStorage).
type Storage struct {
	data     []byte
	hdr      oleHeader
	bbat     *allocTable
	sbat     *allocTable
	sbBlocks []uint64
	entries  []dirEntry
}

// fileRead is RVNGFileStream seek+read: bytes at pos, as many as are there.
func (s *Storage) fileRead(pos uint64, n uint64) []byte {
	lpos := int64(pos)
	if lpos < 0 {
		lpos = 0
	}
	if lpos > int64(len(s.data)) {
		lpos = int64(len(s.data))
	}
	end := uint64(lpos) + n
	if n > uint64(len(s.data)) || end > uint64(len(s.data)) {
		end = uint64(len(s.data))
	}
	return s.data[lpos:end]
}

func (s *Storage) loadBigBlocks(blocks []uint64, out []byte) int {
	maxlen := uint64(len(out))
	if len(blocks) < 1 || maxlen == 0 {
		return 0
	}
	bs := uint64(s.bbat.blockSize)
	var bytes uint64
	for i := 0; i < len(blocks) && bytes < maxlen; i++ {
		pos := bs * (blocks[i] + 1)
		p := bs
		if maxlen-bytes < bs {
			p = maxlen - bytes
		}
		got := s.fileRead(pos, p)
		copy(out[bytes:], got)
		bytes += uint64(len(got))
	}
	return int(bytes)
}

func (s *Storage) loadBigBlock(block uint64, out []byte) int {
	return s.loadBigBlocks([]uint64{block}, out)
}

// loadSmallBlock is IStorage::loadSmallBlocks for one block, into out (at most
// maxlen bytes are meant to be written).
func (s *Storage) loadSmallBlock(block uint64, out []byte, maxlen uint64) {
	bbs := uint64(s.bbat.blockSize)
	pos := block * uint64(s.sbat.blockSize)
	bbindex := pos / bbs
	if bbindex >= uint64(len(s.sbBlocks)) {
		return
	}
	tmp := make([]byte, bbs)
	readData := uint64(s.loadBigBlock(s.sbBlocks[bbindex], tmp))
	offset := pos % bbs
	p := bbs - offset
	if maxlen < p {
		p = maxlen
	}
	if uint64(s.sbat.blockSize) < p {
		p = uint64(s.sbat.blockSize)
	}
	if readData < p {
		p = readData
	}
	copy(out, tmp[offset:offset+p])
}

// OpenStorage loads the compound document structure (IStorage::load); ok is
// false unless librevenge would call the data structured.
func OpenStorage(data []byte) (*Storage, bool) {
	s := &Storage{data: data, bbat: newAllocTable(), sbat: newAllocTable()}
	if len(data) < 512 {
		return nil, false
	}
	h := &s.hdr
	h.shiftBbat = uint32(readU16(data[0x1e:]))
	if h.shiftBbat > 31 {
		h.shiftBbat = 31
	}
	h.shiftSbat = uint32(readU16(data[0x20:]))
	if h.shiftSbat > 31 {
		h.shiftSbat = 31
	}
	h.numBat = readU32(data[0x2c:])
	h.startDirent = readU32(data[0x30:])
	h.threshold = readU32(data[0x38:])
	h.startSbat = readU32(data[0x3c:])
	h.numSbat = readU32(data[0x40:])
	h.startMbat = readU32(data[0x44:])
	h.numMbat = readU32(data[0x48:])
	for j := 0; j < 109; j++ {
		h.blocksBbat[j] = readU32(data[0x4c+j*4:])
	}
	if string(data[:8]) != string(oleMagic) {
		return nil, false
	}
	h.computeBlockSizes()
	s.bbat.blockSize = h.sizeBbat
	s.sbat.blockSize = h.sizeSbat

	if !h.valid(uint64(len(data))) {
		return nil, false
	}

	// blocks allocated to store the big bat: the first 109 are in the header,
	// the rest in the meta bat
	blocks := make([]uint64, h.numBat)
	for j := 0; j < 109 && uint32(j) < h.numBat; j++ {
		blocks[j] = uint64(h.blocksBbat[j])
	}
	if h.numBat > 109 && h.numMbat > 0 {
		buffer2 := make([]byte, s.bbat.blockSize)
		k := 109
		for r := uint32(0); r < h.numMbat && k > 0; r++ {
			var sector uint64
			if r == 0 {
				sector = uint64(h.startMbat)
			} else {
				k--
				sector = blocks[k]
			}
			readData := s.loadBigBlock(sector, buffer2)
			for off := 0; off < int(s.bbat.blockSize); off += 4 {
				if uint32(k) >= h.numBat || off >= readData {
					break
				}
				blocks[k] = uint64(readU32(buffer2[off:]))
				k++
			}
		}
	}

	// the big bat
	if len(blocks) > 0 {
		buffer := make([]byte, uint64(len(blocks))*uint64(s.bbat.blockSize))
		n := s.loadBigBlocks(blocks, buffer)
		s.bbat.load(buffer, n)
	}
	// the small bat
	blocks = s.bbat.follow(uint64(h.startSbat))
	if len(blocks) > 0 {
		buffer := make([]byte, uint64(len(blocks))*uint64(s.bbat.blockSize))
		n := s.loadBigBlocks(blocks, buffer)
		s.sbat.load(buffer, n)
	}
	// the directory tree
	blocks = s.bbat.follow(uint64(h.startDirent))
	if len(blocks) == 0 {
		return nil, false
	}
	buffer := make([]byte, uint64(len(blocks))*uint64(s.bbat.blockSize))
	n := s.loadBigBlocks(blocks, buffer)
	for i := 0; i < n/128; i++ {
		s.entries = append(s.entries, loadDirEntry(buffer[i*128:]))
	}
	if n < 0x74+4 {
		return nil, false
	}
	s.sbBlocks = s.bbat.follow(uint64(readU32(buffer[0x74:])))
	return s, true
}

func (s *Storage) entry(i uint32) *dirEntry {
	if uint64(i) >= uint64(len(s.entries)) {
		return nil
	}
	return &s.entries[i]
}

// siblings is DirTree::get_siblings: the node and what hangs off it to either
// side, in index order.
func (s *Storage) siblings(ind uint32) []uint32 {
	seen := map[uint32]bool{}
	var walk func(i uint32)
	walk = func(i uint32) {
		if seen[i] {
			return
		}
		seen[i] = true
		e := s.entry(i)
		if e == nil {
			return
		}
		cnt := uint32(len(s.entries))
		if e.left > 0 && e.left < cnt {
			walk(e.left)
		}
		if e.right > 0 && e.right < cnt {
			walk(e.right)
		}
	}
	walk(ind)
	out := make([]uint32, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (s *Storage) subStreamList(ind uint32, prefix string, res *[]string, seen map[uint32]bool, isRoot bool) {
	if seen[ind] {
		return
	}
	seen[ind] = true
	p := s.entry(ind)
	if p == nil || !p.valid {
		return
	}
	name := prefix
	if ind != 0 && !isRoot {
		if len(p.name) == 0 {
			return
		}
		name += p.name
	}
	if !p.isDir() {
		*res = append(*res, name)
		return
	}
	if ind != 0 {
		name += "/"
	}
	if ind != 0 {
		*res = append(*res, name)
	} else {
		*res = append(*res, "/")
	}
	if p.child >= uint32(len(s.entries)) {
		return
	}
	for _, sib := range s.siblings(p.child) {
		s.subStreamList(sib, name, res, seen, false)
	}
}

// subStreamNames is Storage::getSubStreamNamesList: every path in the
// document, with control characters (and, as char is signed, high bytes) removed.
func (s *Storage) subStreamNames() []string {
	var res []string
	s.subStreamList(0, "", &res, map[uint32]bool{}, true)
	for i, r := range res {
		var b []byte
		for j := 0; j < len(r); j++ {
			if int8(r[j]) >= 32 {
				b = append(b, r[j])
			}
		}
		res[i] = string(b)
	}
	return res
}

// HasStream is RVNGInputStream::existsSubStream.
func (s *Storage) HasStream(name string) bool {
	for _, n := range s.subStreamNames() {
		if n == name {
			return true
		}
	}
	return false
}

// findChild is DirTree::find_child below the root.
func (s *Storage) findChild(name string) uint32 {
	p := s.entry(0)
	if p == nil || !p.valid {
		return 0
	}
	for _, sib := range s.siblings(p.child) {
		e := s.entry(sib)
		if e == nil {
			continue
		}
		if e.simpleName() == name {
			return sib
		}
	}
	return 0
}

// Stream is getSubStreamByName for a top-level stream: nil, false where
// librevenge hands back no stream (not found, empty, or unreadable).
func (s *Storage) Stream(name string) ([]byte, bool) {
	lookup := name
	if len(lookup) > 0 && int8(lookup[0]) < 32 {
		lookup = lookup[1:]
	}
	ind := s.findChild(lookup)
	if ind == 0 {
		return nil, false
	}
	e := s.entry(ind)
	if e.isDir() {
		return nil, false // librevenge would rebuild a sub-document; PageMaker never has one
	}
	size := e.size
	big := size >= uint64(s.hdr.threshold)
	var blocks []uint64
	var blockSize uint32
	if big {
		blocks = s.bbat.follow(e.start)
		blockSize = s.bbat.blockSize
	} else {
		blocks = s.sbat.follow(e.start)
		blockSize = s.sbat.blockSize
	}
	maxSize := uint64(uint32(uint64(blockSize) * uint64(len(blocks))))
	if size > maxSize {
		size = maxSize
	}
	if size == 0 {
		return nil, false
	}

	out := make([]byte, size)
	var total uint64
	index := uint64(0)
	if index >= uint64(len(blocks)) {
		return nil, false
	}
	if !big {
		sbs := uint64(blockSize)
		buf := make([]byte, sbs)
		for total < size && index < uint64(len(blocks)) {
			s.loadSmallBlock(blocks[index], buf, uint64(s.bbat.blockSize))
			count := sbs
			if count > size-total {
				count = size - total
			}
			copy(out[total:], buf[:count])
			total += count
			index++
		}
	} else {
		bbs := uint64(blockSize)
		buf := make([]byte, bbs)
		for total < size && index < uint64(len(blocks)) {
			s.loadBigBlock(blocks[index], buf)
			count := bbs
			if count > size-total {
				count = size - total
			}
			copy(out[total:], buf[:count])
			total += count
			index++
		}
	}
	if total != size {
		return nil, false
	}
	return out, true
}

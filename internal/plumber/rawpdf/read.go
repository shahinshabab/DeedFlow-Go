// Copyright 2014 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package pdf implements reading of PDF files.
//
// # Overview
//
// PDF is Adobe's Portable Document Format, ubiquitous on the internet.
// A PDF document is a complex data format built on a fairly simple structure.
// This package exposes the simple structure along with some wrappers to
// extract basic information. If more complex information is needed, it is
// possible to extract that information by interpreting the structure exposed
// by this package.
//
// Specifically, a PDF is a data structure built from Values, each of which has
// one of the following Kinds:
//
//	Null, for the null object.
//	Integer, for an integer.
//	Real, for a floating-point number.
//	Bool, for a boolean value.
//	Name, for a name constant (as in /Helvetica).
//	String, for a string constant.
//	Dict, for a dictionary of name-value pairs.
//	Array, for an array of values.
//	Stream, for an opaque data stream and associated header dictionary.
//
// The accessors on Value—Int64, Float64, Bool, Name, and so on—return
// a view of the data as the given type. When there is no appropriate view,
// the accessor returns a zero result. For example, the Name accessor returns
// the empty string if called on a Value v for which v.Kind() != Name.
// Returning zero values this way, especially from the Dict and Array accessors,
// which themselves return Values, makes it possible to traverse a PDF quickly
// without writing any error checking. On the other hand, it means that mistakes
// can go unreported.
//
// The basic structure of the PDF file is exposed as the graph of Values.
//
// Most richer data structures in a PDF file are dictionaries with specific interpretations
// of the name-value pairs. The Font and Page wrappers make the interpretation
// of a specific Value as the corresponding type easier. They are only helpers, though:
// they are implemented only in terms of the Value API and could be moved outside
// the package. Equally important, traversal of other PDF data structures can be implemented
// in other packages as needed.
package rawpdf

// BUG(rsc): The package is incomplete, although it has been used successfully on some
// large real-world PDF files.

// BUG(rsc): There is no support for closing open PDF files. If you drop all references to a Reader,
// the underlying reader will eventually be garbage collected.

// BUG(rsc): The library makes no attempt at efficiency. A value cache maintained in the Reader
// would probably help significantly.

// BUG(rsc): The support for reading encrypted files is weak.

// BUG(rsc): The Value API does not support error reporting. The intent is to allow users to
// set an error reporting callback in Reader, but that code has not been implemented.

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rc4"
	"encoding/ascii85"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
)

// DebugOn is responsible for logging messages into stdout. If problems arise during reading, set it true.
var DebugOn = false

// A Reader is a single PDF file open for reading.
type Reader struct {
	f          io.ReaderAt
	end        int64
	xref       []xref
	trailer    dict
	trailerptr objptr
	key        []byte
	useAES     bool
}

type xref struct {
	ptr      objptr
	inStream bool
	stream   objptr
	offset   int64
}

// Open opens a file for reading.
func Open(file string) (*os.File, *Reader, error) {
	f, err := os.Open(file)
	if err != nil {
		// f is nil here; calling f.Close() would panic.
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	reader, err := NewReader(f, fi.Size())
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, reader, err
}

// NewReader opens a file for reading, using the data in f with the given total size.
func NewReader(f io.ReaderAt, size int64) (*Reader, error) {
	return NewReaderEncrypted(f, size, nil)
}

// NewReaderEncrypted opens a file for reading, using the data in f with the given total size.
// If the PDF is encrypted, NewReaderEncrypted calls pw repeatedly to obtain passwords
// to try. If pw returns the empty string, NewReaderEncrypted stops trying to decrypt
// the file and returns an error.
func NewReaderEncrypted(f io.ReaderAt, size int64, pw func() string) (r *Reader, err error) {
	defer func() {
		if x := recover(); x != nil {
			r = nil
			if e, ok := x.(error); ok {
				err = e
			} else {
				err = fmt.Errorf("malformed PDF: %v", x)
			}
		}
	}()

	r = &Reader{
		f:   f,
		end: size,
	}
	head := make([]byte, 8)
	f.ReadAt(head, 0)
	if !bytes.HasPrefix(head, []byte("%PDF-")) {
		return nil, fmt.Errorf("not a PDF file: invalid header")
	}
	xref, trailerptr, trailer, err := r.loadXref()
	if err != nil {
		// Damaged or truncated file: rebuild the table by scanning for objects.
		var rerr error
		xref, trailerptr, trailer, rerr = r.rebuildXref()
		if rerr != nil {
			return nil, err
		}
	}
	r.xref = xref
	r.trailer = trailer
	r.trailerptr = trailerptr
	if trailer["Encrypt"] == nil {
		return r, nil
	}
	err = r.initEncrypt("")
	if err == nil {
		return r, nil
	}
	if pw == nil || err != ErrInvalidPassword {
		return nil, err
	}
	for {
		next := pw()
		if next == "" {
			break
		}
		if r.initEncrypt(next) == nil {
			return r, nil
		}
	}
	return nil, err
}

// Trailer returns the file's Trailer value.
func (r *Reader) Trailer() Value {
	return Value{r, r.trailerptr, r.trailer}
}

func readXref(r *Reader, b *buffer) ([]xref, objptr, dict, error) {
	tok := b.readToken()
	if tok == keyword("xref") {
		return readXrefTable(r, b)
	}
	if _, ok := tok.(int64); ok {
		b.unreadToken(tok)
		return readXrefStream(r, b)
	}
	return nil, objptr{}, nil, fmt.Errorf("malformed PDF: cross-reference table not found: %v", tok)
}

// readPrevXrefs walks a /Prev chain of cross-reference sections. first is the
// /Prev entry of the most recently read section; parse is called with a buffer
// positioned at the start of each previous section and must return that
// section's own /Prev entry (or nil) to continue the chain.
func readPrevXrefs(r *Reader, first object, parse func(b *buffer) (object, error)) error {
	for prev := first; prev != nil; {
		off, ok := prev.(int64)
		if !ok {
			return fmt.Errorf("malformed PDF: xref Prev is not integer: %v", prev)
		}
		b := newBuffer(io.NewSectionReader(r.f, off, r.end-off), off)
		next, err := parse(b)
		if err != nil {
			return err
		}
		prev = next
	}
	return nil
}

func readXrefStream(r *Reader, b *buffer) ([]xref, objptr, dict, error) {
	obj1 := b.readObject()
	obj, ok := obj1.(objdef)
	if !ok {
		return nil, objptr{}, nil, fmt.Errorf("malformed PDF: cross-reference table not found: %v", objfmt(obj1))
	}
	strmptr := obj.ptr
	strm, ok := obj.obj.(stream)
	if !ok {
		return nil, objptr{}, nil, fmt.Errorf("malformed PDF: cross-reference table not found: %v", objfmt(obj))
	}
	if strm.hdr["Type"] != name("XRef") {
		return nil, objptr{}, nil, fmt.Errorf("malformed PDF: xref stream does not have type XRef")
	}
	size, ok := strm.hdr["Size"].(int64)
	if !ok {
		return nil, objptr{}, nil, fmt.Errorf("malformed PDF: xref stream missing Size")
	}
	table := make([]xref, size)

	table, err := readXrefStreamData(r, strm, table, size)
	if err != nil {
		return nil, objptr{}, nil, fmt.Errorf("malformed PDF: %v", err)
	}

	err = readPrevXrefs(r, strm.hdr["Prev"], func(b *buffer) (object, error) {
		obj1 := b.readObject()
		obj, ok := obj1.(objdef)
		if !ok {
			return nil, fmt.Errorf("malformed PDF: xref prev stream not found: %v", objfmt(obj1))
		}
		prevstrm, ok := obj.obj.(stream)
		if !ok {
			return nil, fmt.Errorf("malformed PDF: xref prev stream not found: %v", objfmt(obj))
		}
		prev := Value{r, objptr{}, prevstrm}
		if prev.Kind() != Stream {
			return nil, fmt.Errorf("malformed PDF: xref prev stream is not stream: %v", prev)
		}
		if prev.Key("Type").Name() != "XRef" {
			return nil, fmt.Errorf("malformed PDF: xref prev stream does not have type XRef")
		}
		psize := prev.Key("Size").Int64()
		if psize > size {
			return nil, fmt.Errorf("malformed PDF: xref prev stream larger than last stream")
		}
		var dataErr error
		table, dataErr = readXrefStreamData(r, prev.data.(stream), table, psize)
		if dataErr != nil {
			return nil, fmt.Errorf("malformed PDF: reading xref prev stream: %v", dataErr)
		}
		return prevstrm.hdr["Prev"], nil
	})
	if err != nil {
		return nil, objptr{}, nil, err
	}

	return table, strmptr, strm.hdr, nil
}

func readXrefStreamData(r *Reader, strm stream, table []xref, size int64) ([]xref, error) {
	index, _ := strm.hdr["Index"].(array)
	if index == nil {
		index = array{int64(0), size}
	}
	if len(index)%2 != 0 {
		return nil, fmt.Errorf("invalid Index array %v", objfmt(index))
	}
	ww, ok := strm.hdr["W"].(array)
	if !ok {
		return nil, fmt.Errorf("xref stream missing W array")
	}

	var w []int
	for _, x := range ww {
		i, ok := x.(int64)
		if !ok || int64(int(i)) != i {
			return nil, fmt.Errorf("invalid W array %v", objfmt(ww))
		}
		w = append(w, int(i))
	}
	if len(w) < 3 {
		return nil, fmt.Errorf("invalid W array %v", objfmt(ww))
	}

	v := Value{r, objptr{}, strm}
	wtotal := 0
	for _, wid := range w {
		wtotal += wid
	}
	buf := make([]byte, wtotal)
	data := v.Reader()
	for len(index) > 0 {
		start, ok1 := index[0].(int64)
		n, ok2 := index[1].(int64)
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("malformed Index pair %v %v %T %T", objfmt(index[0]), objfmt(index[1]), index[0], index[1])
		}
		index = index[2:]
		for i := 0; i < int(n); i++ {
			_, err := io.ReadFull(data, buf)
			if err != nil {
				return nil, fmt.Errorf("error reading xref stream: %v", err)
			}
			v1 := decodeInt(buf[0:w[0]])
			if w[0] == 0 {
				v1 = 1
			}
			v2 := decodeInt(buf[w[0] : w[0]+w[1]])
			v3 := decodeInt(buf[w[0]+w[1] : w[0]+w[1]+w[2]])
			x := int(start) + i
			table = ensureXrefLen(table, x)
			if table[x].ptr != (objptr{}) {
				continue
			}
			switch v1 {
			case 0:
				table[x] = xref{ptr: objptr{0, 65535}}
			case 1:
				table[x] = xref{ptr: objptr{uint32(x), uint16(v3)}, offset: int64(v2)}
			case 2:
				table[x] = xref{ptr: objptr{uint32(x), 0}, inStream: true, stream: objptr{uint32(v2), 0}, offset: int64(v3)}
			default:
				if DebugOn {
					fmt.Printf("invalid xref stream type %d: %x\n", v1, buf)
				}
			}
		}
	}
	return table, nil
}

func decodeInt(b []byte) int {
	x := 0
	for _, c := range b {
		x = x<<8 | int(c)
	}
	return x
}

// ensureXrefLen grows table, if needed, so that table[x] is a valid element.
// The previous idiom (repeatedly append(table[:cap(table)], xref{})) was
// correct but hard to follow, so this replaces it with an explicit resize.
func ensureXrefLen(table []xref, x int) []xref {
	if x < len(table) {
		return table
	}
	return append(table, make([]xref, x-len(table)+1)...)
}

func readXrefTable(r *Reader, b *buffer) ([]xref, objptr, dict, error) {
	var table []xref

	table, err := readXrefTableData(b, table)
	if err != nil {
		return nil, objptr{}, nil, fmt.Errorf("malformed PDF: %v", err)
	}

	trailer, ok := b.readObject().(dict)
	if !ok {
		return nil, objptr{}, nil, fmt.Errorf("malformed PDF: xref table not followed by trailer dictionary")
	}

	err = readPrevXrefs(r, trailer["Prev"], func(b *buffer) (object, error) {
		if tok := b.readToken(); tok != keyword("xref") {
			return nil, fmt.Errorf("malformed PDF: xref Prev does not point to xref")
		}
		var dataErr error
		table, dataErr = readXrefTableData(b, table)
		if dataErr != nil {
			return nil, fmt.Errorf("malformed PDF: %v", dataErr)
		}
		prevTrailer, ok := b.readObject().(dict)
		if !ok {
			return nil, fmt.Errorf("malformed PDF: xref Prev table not followed by trailer dictionary")
		}
		return prevTrailer["Prev"], nil
	})
	if err != nil {
		return nil, objptr{}, nil, err
	}

	size, ok := trailer[name("Size")].(int64)
	if !ok {
		return nil, objptr{}, nil, fmt.Errorf("malformed PDF: trailer missing /Size entry")
	}

	if size < int64(len(table)) {
		table = table[:size]
	}

	return table, objptr{}, trailer, nil
}

func readXrefTableData(b *buffer, table []xref) ([]xref, error) {
	for {
		tok := b.readToken()
		if tok == keyword("trailer") {
			break
		}
		start, ok1 := tok.(int64)
		n, ok2 := b.readToken().(int64)
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("malformed xref table")
		}
		for i := 0; i < int(n); i++ {
			off, ok1 := b.readToken().(int64)
			gen, ok2 := b.readToken().(int64)
			alloc, ok3 := b.readToken().(keyword)
			if !ok1 || !ok2 || !ok3 || alloc != keyword("f") && alloc != keyword("n") {
				return nil, fmt.Errorf("malformed xref table")
			}
			x := int(start) + i
			table = ensureXrefLen(table, x)
			if alloc == "n" && table[x].offset == 0 {
				table[x] = xref{ptr: objptr{uint32(x), uint16(gen)}, offset: int64(off)}
			}
		}
	}
	return table, nil
}

func findLastLine(buf []byte, s string) int {
	bs := []byte(s)
	max := len(buf)
	for {
		i := bytes.LastIndex(buf[:max], bs)
		if i <= 0 || i+len(bs) >= len(buf) {
			return -1
		}
		if (buf[i-1] == '\n' || buf[i-1] == '\r') && (buf[i+len(bs)] == '\n' || buf[i+len(bs)] == '\r') {
			return i
		}
		max = i
	}
}

// A Value is a single PDF value, such as an integer, dictionary, or array.
// The zero Value is a PDF null (Kind() == Null, IsNull() = true).
type Value struct {
	r    *Reader
	ptr  objptr
	data interface{}
}

// IsNull reports whether the value is a null. It is equivalent to Kind() == Null.
func (v Value) IsNull() bool {
	return v.data == nil
}

// A ValueKind specifies the kind of data underlying a Value.
type ValueKind int

// The PDF value kinds.
const (
	Null ValueKind = iota
	Bool
	Integer
	Real
	String
	Name
	Dict
	Array
	Stream
)

// Kind reports the kind of value underlying v.
func (v Value) Kind() ValueKind {
	switch v.data.(type) {
	default:
		return Null
	case bool:
		return Bool
	case int64:
		return Integer
	case float64:
		return Real
	case string:
		return String
	case name:
		return Name
	case dict:
		return Dict
	case array:
		return Array
	case stream:
		return Stream
	}
}

// String returns a textual representation of the value v.
// Note that String is not the accessor for values with Kind() == String.
// To access such values, see RawString, Text, and TextFromUTF16.
func (v Value) String() string {
	return objfmt(v.data)
}

func objfmt(x interface{}) string {
	switch x := x.(type) {
	default:
		return fmt.Sprint(x)
	case string:
		if isPDFDocEncoded(x) {
			return strconv.Quote(pdfDocDecode(x))
		}
		if isUTF16(x) {
			return strconv.Quote(utf16Decode(x[2:]))
		}
		return strconv.Quote(x)
	case name:
		return "/" + string(x)
	case dict:
		var keys []string
		for k := range x {
			keys = append(keys, string(k))
		}
		sort.Strings(keys)
		var buf bytes.Buffer
		buf.WriteString("<<")
		for i, k := range keys {
			elem := x[name(k)]
			if i > 0 {
				buf.WriteString(" ")
			}
			buf.WriteString("/")
			buf.WriteString(k)
			buf.WriteString(" ")
			buf.WriteString(objfmt(elem))
		}
		buf.WriteString(">>")
		return buf.String()

	case array:
		var buf bytes.Buffer
		buf.WriteString("[")
		for i, elem := range x {
			if i > 0 {
				buf.WriteString(" ")
			}
			buf.WriteString(objfmt(elem))
		}
		buf.WriteString("]")
		return buf.String()

	case stream:
		return fmt.Sprintf("%v@%d", objfmt(x.hdr), x.offset)

	case objptr:
		return fmt.Sprintf("%d %d R", x.id, x.gen)

	case objdef:
		return fmt.Sprintf("{%d %d obj}%v", x.ptr.id, x.ptr.gen, objfmt(x.obj))
	}
}

// Bool returns v's boolean value.
// If v.Kind() != Bool, Bool returns false.
func (v Value) Bool() bool {
	x, ok := v.data.(bool)
	if !ok {
		return false
	}
	return x
}

// Int64 returns v's int64 value.
// If v.Kind() != Int64, Int64 returns 0.
func (v Value) Int64() int64 {
	x, ok := v.data.(int64)
	if !ok {
		return 0
	}
	return x
}

// Float64 returns v's float64 value, converting from integer if necessary.
// If v.Kind() != Float64 and v.Kind() != Int64, Float64 returns 0.
func (v Value) Float64() float64 {
	x, ok := v.data.(float64)
	if !ok {
		x, ok := v.data.(int64)
		if ok {
			return float64(x)
		}
		return 0
	}
	return x
}

// RawString returns v's string value.
// If v.Kind() != String, RawString returns the empty string.
func (v Value) RawString() string {
	x, ok := v.data.(string)
	if !ok {
		return ""
	}
	return x
}

// Text returns v's string value interpreted as a “text string” (defined in the PDF spec)
// and converted to UTF-8.
// If v.Kind() != String, Text returns the empty string.
func (v Value) Text() string {
	x, ok := v.data.(string)
	if !ok {
		return ""
	}
	if isPDFDocEncoded(x) {
		return pdfDocDecode(x)
	}
	if isUTF16(x) {
		return utf16Decode(x[2:])
	}
	return x
}

// TextFromUTF16 returns v's string value interpreted as big-endian UTF-16
// and then converted to UTF-8.
// If v.Kind() != String or if the data is not valid UTF-16, TextFromUTF16 returns
// the empty string.
func (v Value) TextFromUTF16() string {
	x, ok := v.data.(string)
	if !ok {
		return ""
	}
	if len(x)%2 == 1 {
		return ""
	}
	if x == "" {
		return ""
	}
	return utf16Decode(x)
}

// Name returns v's name value.
// If v.Kind() != Name, Name returns the empty string.
// The returned name does not include the leading slash:
// if v corresponds to the name written using the syntax /Helvetica,
// Name() == "Helvetica".
func (v Value) Name() string {
	x, ok := v.data.(name)
	if !ok {
		return ""
	}
	return string(x)
}

// Key returns the value associated with the given name key in the dictionary v.
// Like the result of the Name method, the key should not include a leading slash.
// If v is a stream, Key applies to the stream's header dictionary.
// If v.Kind() != Dict and v.Kind() != Stream, Key returns a null Value.
func (v Value) Key(key string) Value {
	x, ok := v.data.(dict)
	if !ok {
		strm, ok := v.data.(stream)
		if !ok {
			return Value{}
		}
		x = strm.hdr
	}
	return v.r.resolve(v.ptr, x[name(key)])
}

// Keys returns a sorted list of the keys in the dictionary v.
// If v is a stream, Keys applies to the stream's header dictionary.
// If v.Kind() != Dict and v.Kind() != Stream, Keys returns nil.
func (v Value) Keys() []string {
	x, ok := v.data.(dict)
	if !ok {
		strm, ok := v.data.(stream)
		if !ok {
			return nil
		}
		x = strm.hdr
	}
	keys := []string{} // not nil
	for k := range x {
		keys = append(keys, string(k))
	}
	sort.Strings(keys)
	return keys
}

// Index returns the i'th element in the array v.
// If v.Kind() != Array or if i is outside the array bounds,
// Index returns a null Value.
func (v Value) Index(i int) Value {
	x, ok := v.data.(array)
	if !ok || i < 0 || i >= len(x) {
		return Value{}
	}
	return v.r.resolve(v.ptr, x[i])
}

// Len returns the length of the array v.
// If v.Kind() != Array, Len returns 0.
func (v Value) Len() int {
	x, ok := v.data.(array)
	if !ok {
		return 0
	}
	return len(x)
}

func (r *Reader) resolve(parent objptr, x interface{}) Value {
	if ptr, ok := x.(objptr); ok {
		if ptr.id >= uint32(len(r.xref)) {
			return Value{}
		}
		xref := r.xref[ptr.id]
		if xref.ptr != ptr || !xref.inStream && xref.offset == 0 {
			return Value{}
		}
		var obj object
		if xref.inStream {
			strm := r.resolve(parent, xref.stream)
		Search:
			for {
				if strm.Kind() != Stream {
					panic("not a stream")
				}
				if strm.Key("Type").Name() != "ObjStm" {
					panic("not an object stream")
				}
				n := int(strm.Key("N").Int64())
				first := strm.Key("First").Int64()
				if first == 0 {
					panic("missing First")
				}
				b := newBuffer(strm.Reader(), 0)
				b.allowEOF = true
				for i := 0; i < n; i++ {
					id, _ := b.readToken().(int64)
					off, _ := b.readToken().(int64)
					if uint32(id) == ptr.id {
						b.seekForward(first + off)
						x = b.readObject()
						break Search
					}
				}
				ext := strm.Key("Extends")
				if ext.Kind() != Stream {
					panic("cannot find object in stream")
				}
				strm = ext
			}
		} else {
			b := newBuffer(io.NewSectionReader(r.f, xref.offset, r.end-xref.offset), xref.offset)
			b.key = r.key
			b.useAES = r.useAES
			obj = b.readObject()
			def, ok := obj.(objdef)
			if !ok {
				panic(fmt.Errorf("loading %v: found %T instead of objdef", ptr, obj))
				//return Value{}
			}
			if def.ptr != ptr {
				panic(fmt.Errorf("loading %v: found %v", ptr, def.ptr))
			}
			x = def.obj
		}
		parent = ptr
	}

	switch x := x.(type) {
	case nil, bool, int64, float64, name, dict, array, stream:
		return Value{r, parent, x}
	case string:
		return Value{r, parent, x}
	default:
		panic(fmt.Errorf("unexpected value type %T in resolve", x))
	}
}

type errorReadCloser struct {
	err error
}

func (e *errorReadCloser) Read([]byte) (int, error) {
	return 0, e.err
}

func (e *errorReadCloser) Close() error {
	return e.err
}

// Reader returns the data contained in the stream v.
// If v.Kind() != Stream, Reader returns a ReadCloser that
// responds to all reads with a “stream not present” error.
func (v Value) Reader() io.ReadCloser {
	x, ok := v.data.(stream)
	if !ok {
		return &errorReadCloser{fmt.Errorf("stream not present")}
	}
	streamLen := v.Key("Length").Int64()
	// Handle empty streams - return empty reader without applying filters.
	// This avoids zlib "unexpected EOF" errors on 0-length FlateDecode streams.
	if streamLen == 0 {
		return io.NopCloser(bytes.NewReader(nil))
	}
	var rd io.Reader
	rd = io.NewSectionReader(v.r.f, x.offset, streamLen)
	if v.r.key != nil {
		rd = decryptStream(v.r.key, v.r.useAES, x.ptr, rd)
	}
	filter := v.Key("Filter")
	param := v.Key("DecodeParms")
	switch filter.Kind() {
	default:
		panic(fmt.Errorf("unsupported filter %v", filter))
	case Null:
		// ok
	case Name:
		rd = applyFilter(rd, filter.Name(), param)
	case Array:
		for i := 0; i < filter.Len(); i++ {
			rd = applyFilter(rd, filter.Index(i).Name(), param.Index(i))
		}
	}

	return io.NopCloser(rd)
}

func applyFilter(rd io.Reader, name string, param Value) io.Reader {
	switch name {
	default:
		// Image filters (DCT, JPX, CCITT, JBIG2) and anything unknown: hand the
		// raw bytes through. Only fonts' and pages' streams matter here, and
		// they are always Flate / ASCII85 / ASCIIHex / RunLength / LZW.
		return rd
	case "FlateDecode", "Fl":
		zr, err := zlib.NewReader(rd)
		if err != nil {
			panic(err)
		}
		return applyPredictor(zr, param)
	case "LZWDecode", "LZW":
		early := 1
		if p := param.Key("EarlyChange"); p.Kind() != Null {
			early = int(p.Int64())
		}
		return applyPredictor(newLZWReader(rd, early), param)
	case "ASCII85Decode", "A85":
		return ascii85.NewDecoder(newAlphaReader(rd))
	case "ASCIIHexDecode", "AHx":
		return newASCIIHexReader(rd)
	case "RunLengthDecode", "RL":
		return newRunLengthReader(rd)
	}
}

// applyPredictor undoes a PNG (10-15) or TIFF (2) predictor.
func applyPredictor(rd io.Reader, param Value) io.Reader {
	pred := int(param.Key("Predictor").Int64())
	if pred <= 1 {
		return rd
	}
	columns := int(param.Key("Columns").Int64())
	if columns <= 0 {
		columns = 1
	}
	colors := int(param.Key("Colors").Int64())
	if colors <= 0 {
		colors = 1
	}
	bpc := int(param.Key("BitsPerComponent").Int64())
	if bpc <= 0 {
		bpc = 8
	}
	bpp := (colors*bpc + 7) / 8
	rowBytes := (columns*colors*bpc + 7) / 8
	data, err := io.ReadAll(rd)
	if err != nil && len(data) == 0 {
		panic(err)
	}
	if pred == 2 {
		return bytes.NewReader(tiffPredictor(data, rowBytes, colors, bpc))
	}
	return bytes.NewReader(pngPredictor(data, rowBytes, bpp))
}

func tiffPredictor(data []byte, rowBytes, colors, bpc int) []byte {
	if bpc != 8 {
		return data
	}
	out := append([]byte(nil), data...)
	for row := 0; row+rowBytes <= len(out); row += rowBytes {
		for i := colors; i < rowBytes; i++ {
			out[row+i] += out[row+i-colors]
		}
	}
	return out
}

func pngPredictor(data []byte, rowBytes, bpp int) []byte {
	stride := rowBytes + 1
	rows := (len(data) + stride - 1) / stride
	out := make([]byte, 0, rows*rowBytes)
	prev := make([]byte, rowBytes)
	for r := 0; r < rows; r++ {
		start := r * stride
		end := start + stride
		if end > len(data) {
			end = len(data)
		}
		line := data[start:end]
		if len(line) == 0 {
			break
		}
		filter := line[0]
		cur := make([]byte, rowBytes)
		copy(cur, line[1:])
		for i := 0; i < rowBytes; i++ {
			var left, up, upLeft byte
			if i >= bpp {
				left = cur[i-bpp]
				upLeft = prev[i-bpp]
			}
			up = prev[i]
			switch filter {
			case 1:
				cur[i] += left
			case 2:
				cur[i] += up
			case 3:
				cur[i] += byte((int(left) + int(up)) / 2)
			case 4:
				cur[i] += paeth(left, up, upLeft)
			}
		}
		out = append(out, cur...)
		prev = cur
	}
	return out
}

func paeth(a, b, c byte) byte {
	p := int(a) + int(b) - int(c)
	pa, pb, pc := abs(p-int(a)), abs(p-int(b)), abs(p-int(c))
	if pa <= pb && pa <= pc {
		return a
	}
	if pb <= pc {
		return b
	}
	return c
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

type asciiHexReader struct {
	r   io.Reader
	buf []byte
	eod bool
}

func newASCIIHexReader(r io.Reader) io.Reader { return &asciiHexReader{r: r} }

func (a *asciiHexReader) Read(p []byte) (int, error) {
	if a.eod {
		return 0, io.EOF
	}
	tmp := make([]byte, len(p)*2)
	n, err := a.r.Read(tmp)
	out := 0
	for i := 0; i < n && out < len(p); i++ {
		c := tmp[i]
		var v byte
		switch {
		case c == '>':
			a.eod = true
			if len(a.buf) == 1 {
				p[out] = a.buf[0] << 4
				out++
				a.buf = nil
			}
			return out, nil
		case c >= '0' && c <= '9':
			v = c - '0'
		case c >= 'a' && c <= 'f':
			v = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			v = c - 'A' + 10
		default:
			continue
		}
		a.buf = append(a.buf, v)
		if len(a.buf) == 2 {
			p[out] = a.buf[0]<<4 | a.buf[1]
			out++
			a.buf = a.buf[:0]
		}
	}
	if err == io.EOF && out > 0 {
		return out, nil
	}
	return out, err
}

type runLengthReader struct {
	r    *bufio.Reader
	pend []byte
	eod  bool
}

func newRunLengthReader(r io.Reader) io.Reader { return &runLengthReader{r: bufio.NewReader(r)} }

func (rl *runLengthReader) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		if len(rl.pend) > 0 {
			m := copy(p[n:], rl.pend)
			n += m
			rl.pend = rl.pend[m:]
			continue
		}
		if rl.eod {
			break
		}
		l, err := rl.r.ReadByte()
		if err != nil {
			if n > 0 {
				return n, nil
			}
			return 0, err
		}
		switch {
		case l == 128:
			rl.eod = true
		case l < 128:
			buf := make([]byte, int(l)+1)
			if _, err := io.ReadFull(rl.r, buf); err != nil {
				rl.eod = true
			}
			rl.pend = buf
		default:
			b, err := rl.r.ReadByte()
			if err != nil {
				rl.eod = true
				break
			}
			rl.pend = bytes.Repeat([]byte{b}, 257-int(l))
		}
	}
	if n == 0 && rl.eod {
		return 0, io.EOF
	}
	return n, nil
}

// newLZWReader decodes PDF LZW data (MSB first, 9-12 bit codes, EarlyChange).
func newLZWReader(r io.Reader, early int) io.Reader {
	data, _ := io.ReadAll(r)
	return bytes.NewReader(lzwDecode(data, early))
}

func lzwDecode(data []byte, early int) []byte {
	var out []byte
	table := make([][]byte, 258, 4096)
	reset := func() {
		table = table[:258]
		for i := 0; i < 256; i++ {
			table[i] = []byte{byte(i)}
		}
	}
	reset()
	bits, nbits, width := uint32(0), 0, 9
	var prev []byte
	pos := 0
	for {
		for nbits < width {
			if pos >= len(data) {
				return out
			}
			bits = bits<<8 | uint32(data[pos])
			pos++
			nbits += 8
		}
		code := int(bits>>(uint(nbits-width))) & (1<<uint(width) - 1)
		nbits -= width
		if code == 256 {
			reset()
			width, prev = 9, nil
			continue
		}
		if code == 257 {
			return out
		}
		var entry []byte
		switch {
		case code < len(table) && table[code] != nil:
			entry = table[code]
		case prev != nil && code == len(table):
			entry = append(append([]byte{}, prev...), prev[0])
		default:
			return out
		}
		out = append(out, entry...)
		if prev != nil && len(table) < 4096 {
			table = append(table, append(append([]byte{}, prev...), entry[0]))
		}
		prev = entry
		switch n := len(table) + early; {
		case n >= 2048:
			width = 12
		case n >= 1024:
			width = 11
		case n >= 512:
			width = 10
		default:
			width = 9
		}
	}
}

var passwordPad = []byte{
	0x28, 0xBF, 0x4E, 0x5E, 0x4E, 0x75, 0x8A, 0x41, 0x64, 0x00, 0x4E, 0x56, 0xFF, 0xFA, 0x01, 0x08,
	0x2E, 0x2E, 0x00, 0xB6, 0xD0, 0x68, 0x3E, 0x80, 0x2F, 0x0C, 0xA9, 0xFE, 0x64, 0x53, 0x69, 0x7A,
}

// PDF encryption parameters. See PDF 32000-1:2008, §7.6.
const (
	// minKeyBits and maxKeyBits bound the /Length value (in bits) in the
	// encryption dictionary.
	minKeyBits = 40
	maxKeyBits = 128

	// ouEntryLen is the byte length of the O and U entries for encryption
	// revisions 2 through 4.
	ouEntryLen = 32

	// md5KeyIterations is the number of MD5 rounds used to derive the file
	// key for revisions >= 3 (Algorithm 2, step (b)).
	md5KeyIterations = 50

	// rc4UIterations is the number of RC4 rounds used to derive U for
	// revisions >= 3 (Algorithm 5).
	rc4UIterations = 19
)

func (r *Reader) initEncrypt(password string) error {
	// See PDF 32000-1:2008, §7.6.
	encrypt, _ := r.resolve(objptr{}, r.trailer["Encrypt"]).data.(dict)
	if encrypt["Filter"] != name("Standard") {
		return fmt.Errorf("unsupported PDF: encryption filter %v", objfmt(encrypt["Filter"]))
	}
	n, _ := encrypt["Length"].(int64)
	if n == 0 {
		n = minKeyBits
	}
	if n%8 != 0 || n > maxKeyBits || n < minKeyBits {
		return fmt.Errorf("malformed PDF: %d-bit encryption key", n)
	}
	V, _ := encrypt["V"].(int64)
	if V != 1 && V != 2 && (V != 4 || !okayV4(encrypt)) {
		return fmt.Errorf("unsupported PDF: encryption version V=%d; %v", V, objfmt(encrypt))
	}

	ids, ok := r.trailer["ID"].(array)
	if !ok || len(ids) < 1 {
		return fmt.Errorf("malformed PDF: missing ID in trailer")
	}
	idstr, ok := ids[0].(string)
	if !ok {
		return fmt.Errorf("malformed PDF: missing ID in trailer")
	}
	ID := []byte(idstr)

	R, _ := encrypt["R"].(int64)
	if R < 2 {
		return fmt.Errorf("malformed PDF: encryption revision R=%d", R)
	}
	if R > 4 {
		return fmt.Errorf("unsupported PDF: encryption revision R=%d", R)
	}
	O, _ := encrypt["O"].(string)
	U, _ := encrypt["U"].(string)
	if len(O) != ouEntryLen || len(U) != ouEntryLen {
		return fmt.Errorf("malformed PDF: missing O= or U= encryption parameters")
	}
	p, _ := encrypt["P"].(int64)
	P := uint32(p)

	// TODO: Password should be converted to Latin-1.
	pw := []byte(password)
	h := md5.New()
	if len(pw) >= len(passwordPad) {
		h.Write(pw[:len(passwordPad)])
	} else {
		h.Write(pw)
		h.Write(passwordPad[:len(passwordPad)-len(pw)])
	}
	h.Write([]byte(O))
	h.Write([]byte{byte(P), byte(P >> 8), byte(P >> 16), byte(P >> 24)})
	h.Write([]byte(ID))
	key := h.Sum(nil)

	keyLen := int(n / 8) // encryption key length in bytes
	if R >= 3 {
		for i := 0; i < md5KeyIterations; i++ {
			h.Reset()
			h.Write(key[:keyLen])
			key = h.Sum(key[:0])
		}
		key = key[:keyLen]
	} else {
		key = key[:minKeyBits/8]
	}

	c, err := rc4.NewCipher(key)
	if err != nil {
		return fmt.Errorf("malformed PDF: invalid RC4 key: %v", err)
	}

	var u []byte
	if R == 2 {
		u = make([]byte, len(passwordPad))
		copy(u, passwordPad)
		c.XORKeyStream(u, u)
	} else {
		h.Reset()
		h.Write(passwordPad)
		h.Write([]byte(ID))
		u = h.Sum(nil)
		c.XORKeyStream(u, u)

		for i := 1; i <= rc4UIterations; i++ {
			key1 := make([]byte, len(key))
			copy(key1, key)
			for j := range key1 {
				key1[j] ^= byte(i)
			}
			c, _ = rc4.NewCipher(key1)
			c.XORKeyStream(u, u)
		}
	}

	if !bytes.HasPrefix([]byte(U), u) {
		return ErrInvalidPassword
	}

	r.key = key
	r.useAES = V == 4

	return nil
}

var ErrInvalidPassword = fmt.Errorf("encrypted PDF: invalid password")

func okayV4(encrypt dict) bool {
	cf, ok := encrypt["CF"].(dict)
	if !ok {
		return false
	}
	stmf, ok := encrypt["StmF"].(name)
	if !ok {
		return false
	}
	strf, ok := encrypt["StrF"].(name)
	if !ok {
		return false
	}
	if stmf != strf {
		return false
	}
	cfparam, ok := cf[stmf].(dict)
	if !ok {
		return false
	}
	if cfparam["AuthEvent"] != nil && cfparam["AuthEvent"] != name("DocOpen") {
		return false
	}
	if cfparam["Length"] != nil && cfparam["Length"] != int64(16) {
		return false
	}
	if cfparam["CFM"] != name("AESV2") {
		return false
	}
	return true
}

func cryptKey(key []byte, useAES bool, ptr objptr) []byte {
	h := md5.New()
	h.Write(key)
	h.Write([]byte{byte(ptr.id), byte(ptr.id >> 8), byte(ptr.id >> 16), byte(ptr.gen), byte(ptr.gen >> 8)})
	if useAES {
		h.Write([]byte("sAlT"))
	}
	// PDF 32000-1:2008 Algorithm 1 step e: the object key is the first
	// min(n/8+5, 16) bytes of the digest. Returning the whole digest is only
	// right for a 128-bit key; a 40-bit key needs 10 bytes.
	n := len(key) + 5
	if n > 16 {
		n = 16
	}
	return h.Sum(nil)[:n]
}

func decryptString(key []byte, useAES bool, ptr objptr, x string) string {
	key = cryptKey(key, useAES, ptr)
	if useAES {
		s := []byte(x)
		if len(s) < aes.BlockSize {
			panic("Encrypted text shorter that AES block size")
		}

		block, _ := aes.NewCipher(key)
		iv := s[:aes.BlockSize]
		s = s[aes.BlockSize:]

		stream := cipher.NewCBCDecrypter(block, iv)
		stream.CryptBlocks(s, s)
		x = string(s)
	} else {
		c, _ := rc4.NewCipher(key)
		data := []byte(x)
		c.XORKeyStream(data, data)
		x = string(data)
	}
	return x
}

func decryptStream(key []byte, useAES bool, ptr objptr, rd io.Reader) io.Reader {
	key = cryptKey(key, useAES, ptr)
	if useAES {
		cb, err := aes.NewCipher(key)
		if err != nil {
			panic("AES: " + err.Error())
		}
		iv := make([]byte, 16)
		io.ReadFull(rd, iv)
		cbc := cipher.NewCBCDecrypter(cb, iv)
		rd = &cbcReader{cbc: cbc, rd: rd, buf: make([]byte, 16)}
	} else {
		c, _ := rc4.NewCipher(key)
		rd = &cipher.StreamReader{S: c, R: rd}
	}
	return rd
}

type cbcReader struct {
	cbc  cipher.BlockMode
	rd   io.Reader
	buf  []byte
	pend []byte
}

func (r *cbcReader) Read(b []byte) (n int, err error) {
	if len(r.pend) == 0 {
		_, err = io.ReadFull(r.rd, r.buf)
		if err != nil {
			return 0, err
		}
		r.cbc.CryptBlocks(r.buf, r.buf)
		r.pend = r.buf
	}
	n = copy(b, r.pend)
	r.pend = r.pend[n:]
	return n, nil
}

// loadXref reads the cross-reference data the file's startxref points to.
func (r *Reader) loadXref() (xrefs []xref, ptr objptr, trailer dict, err error) {
	defer func() {
		if x := recover(); x != nil {
			if e, ok := x.(error); ok {
				err = e
			} else {
				err = fmt.Errorf("malformed PDF: %v", x)
			}
		}
	}()
	const endChunk = 1024
	n := int64(endChunk)
	if r.end < n {
		n = r.end
	}
	buf := make([]byte, n)
	r.f.ReadAt(buf, r.end-n)
	i := bytes.LastIndex(buf, []byte("startxref"))
	if i < 0 {
		return nil, objptr{}, nil, fmt.Errorf("malformed PDF file: missing final startxref")
	}
	pos := r.end - n + int64(i)
	b := newBuffer(io.NewSectionReader(r.f, pos, r.end-pos), pos)
	if b.readToken() != keyword("startxref") {
		return nil, objptr{}, nil, fmt.Errorf("malformed PDF file: missing startxref")
	}
	startxref, ok := b.readToken().(int64)
	if !ok || startxref <= 0 || startxref >= r.end {
		return nil, objptr{}, nil, fmt.Errorf("malformed PDF file: bad startxref")
	}
	b = newBuffer(io.NewSectionReader(r.f, startxref, r.end-startxref), startxref)
	xrefs, ptr, trailer, err = readXref(r, b)
	if err == nil {
		// Sanity check: the catalog must be reachable.
		r.xref, r.trailer, r.trailerptr = xrefs, trailer, ptr
		if trailer["Root"] == nil {
			err = fmt.Errorf("malformed PDF: trailer has no Root")
		}
	}
	return
}

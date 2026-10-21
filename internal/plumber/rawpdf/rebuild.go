package rawpdf

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strconv"
)

var objHeader = regexp.MustCompile(`(?:^|[\r\n\t ])(\d+)[ \t\r\n]+(\d+)[ \t\r\n]+obj\b`)
var catalogRe = regexp.MustCompile(`/Type\s*/Catalog\b`)
var objStmRe = regexp.MustCompile(`/Type\s*/ObjStm\b`)

// rebuildXref reconstructs the cross-reference table by scanning the whole
// file for "N G obj" headers and trailer dictionaries.
func (r *Reader) rebuildXref() (table []xref, trailerptr objptr, trailer dict, err error) {
	defer func() {
		if x := recover(); x != nil {
			err = fmt.Errorf("rebuilding PDF structure: %v", x)
		}
	}()
	data := make([]byte, r.end)
	if _, e := r.f.ReadAt(data, 0); e != nil && e != io.EOF {
		return nil, objptr{}, nil, e
	}
	type found struct {
		ptr objptr
		off int64
		end int
	}
	var objs []found
	matches := objHeader.FindAllSubmatchIndex(data, -1)
	for i, m := range matches {
		id, _ := strconv.ParseUint(string(data[m[2]:m[3]]), 10, 32)
		gen, _ := strconv.ParseUint(string(data[m[4]:m[5]]), 10, 16)
		end := len(data)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		objs = append(objs, found{objptr{uint32(id), uint16(gen)}, int64(m[2]), end})
	}
	if len(objs) == 0 {
		return nil, objptr{}, nil, fmt.Errorf("no objects found")
	}
	maxID := uint32(0)
	for _, o := range objs {
		if o.ptr.id > maxID {
			maxID = o.ptr.id
		}
	}
	table = make([]xref, maxID+1)
	for _, o := range objs {
		table[o.ptr.id] = xref{ptr: o.ptr, offset: o.off}
	}

	// Trailer dictionaries: later keys win.
	trailer = dict{}
	for pos := 0; ; {
		i := bytes.Index(data[pos:], []byte("trailer"))
		if i < 0 {
			break
		}
		at := int64(pos + i + len("trailer"))
		pos += i + len("trailer")
		func() {
			defer func() { recover() }()
			b := newBuffer(io.NewSectionReader(r.f, at, r.end-at), at)
			if d, ok := b.readObject().(dict); ok {
				for k, v := range d {
					trailer[k] = v
				}
			}
		}()
	}
	r.xref, r.trailer = table, trailer

	// Cross-reference streams carry the trailer keys in their dictionary, and
	// object streams hold further objects.
	for _, o := range objs {
		head := data[o.off:min(o.end, int(o.off)+400)]
		isObjStm := objStmRe.Match(head)
		isXRef := bytes.Contains(head, []byte("/XRef"))
		if !isObjStm && !isXRef {
			continue
		}
		func() {
			defer func() { recover() }()
			v := r.resolve(objptr{}, o.ptr)
			if v.Kind() != Stream {
				return
			}
			hdr := v.data.(stream).hdr
			if isXRef && hdr["Type"] == name("XRef") {
				for _, k := range []name{"Root", "Info", "Encrypt", "ID"} {
					if hdr[k] != nil {
						trailer[k] = hdr[k]
					}
				}
			}
			if isObjStm && v.Key("Type").Name() == "ObjStm" {
				n := int(v.Key("N").Int64())
				b := newBuffer(v.Reader(), 0)
				b.allowEOF = true
				for i := 0; i < n; i++ {
					id, ok := b.readToken().(int64)
					if !ok {
						break
					}
					b.readToken()
					if id < 0 || id > 1<<24 {
						continue
					}
					table = ensureXrefLen(table, int(id))
					if table[id].offset == 0 && !table[id].inStream {
						table[id] = xref{ptr: objptr{uint32(id), 0}, inStream: true, stream: o.ptr}
					}
				}
				r.xref = table
			}
		}()
	}
	r.xref = table

	if trailer["Root"] == nil {
		for _, o := range objs {
			if catalogRe.Match(data[o.off:min(o.end, int(o.off)+400)]) {
				trailer["Root"] = o.ptr
			}
		}
	}
	if trailer["Root"] == nil {
		return nil, objptr{}, nil, fmt.Errorf("no catalog found")
	}
	r.trailer = trailer
	return table, objptr{}, trailer, nil
}

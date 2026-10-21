package rawpdf

import (
	"fmt"
	"io"
)

// NumPages returns the number of pages in the document.
func (r *Reader) NumPages() int {
	return int(r.Trailer().Key("Root").Key("Pages").Key("Count").Int64())
}

// PageObject returns the page dictionary of the given 1-based page number, or
// a null Value. Unlike a count-trusting lookup, it walks the page tree.
func (r *Reader) PageObject(num int) Value {
	n := 0
	var walk func(node Value, depth int) (Value, bool)
	walk = func(node Value, depth int) (Value, bool) {
		if depth > 64 {
			return Value{}, false
		}
		kids := node.Key("Kids")
		if kids.Kind() != Array {
			// a leaf page
			n++
			if n == num {
				return node, true
			}
			return Value{}, false
		}
		for i := 0; i < kids.Len(); i++ {
			kid := kids.Index(i)
			if kid.IsNull() {
				continue
			}
			if v, ok := walk(kid, depth+1); ok {
				return v, true
			}
		}
		return Value{}, false
	}
	v, _ := walk(r.Trailer().Key("Root").Key("Pages"), 0)
	return v
}

// Inherited returns the value of an inheritable page attribute (Resources,
// MediaBox, CropBox, Rotate) from the page or its ancestors.
func Inherited(page Value, key string) Value {
	for v := page; !v.IsNull(); v = v.Key("Parent") {
		if r := v.Key(key); !r.IsNull() {
			return r
		}
	}
	return Value{}
}

// ObjectID is the number of the indirect object the value was read from (the
// nearest enclosing one for a direct value) - enough to tell fonts apart.
func (v Value) ObjectID() (uint32, uint16) { return v.ptr.id, v.ptr.gen }

// Floats returns the numbers of an array value.
func (v Value) Floats() []float64 {
	if v.Kind() != Array {
		return nil
	}
	out := make([]float64, 0, v.Len())
	for i := 0; i < v.Len(); i++ {
		out = append(out, v.Index(i).Float64())
	}
	return out
}

// Bytes returns the decoded content of a stream.
func (v Value) Bytes() (data []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("reading stream: %v", r)
		}
	}()
	rc := v.Reader()
	defer rc.Close()
	return io.ReadAll(rc)
}

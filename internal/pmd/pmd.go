// Package pmd reads Adobe PageMaker 6.0 to 7.0 files (.pmd, .p65). It is a
// Go port of libpagemaker 0.0.4 with the DeedFlow patches (raw 8-bit text,
// paragraph leading, story ids), and it produces the same drawing calls as
// the pmd2json helper the Django project built from that library.
package pmd

// IsSupported reports whether data is a structured file with a PageMaker stream.
func IsSupported(data []byte) bool {
	st, ok := OpenStorage(data)
	return ok && st.HasStream("PageMaker")
}

// Result says how reading went, in the terms of pmd2json's exit status.
type Result int

const (
	Unsupported Result = iota // exit 3: not a PageMaker document
	OK                        // exit 0
	Failed                    // exit 4: parsing or drawing failed; calls hold nothing
)

// Read parses the document into drawing calls.
func Read(data []byte) ([]Call, Result) {
	st, ok := OpenStorage(data)
	if !ok || !st.HasStream("PageMaker") {
		return nil, Unsupported
	}
	raw, found := st.Stream("PageMaker")
	if !found {
		return nil, Failed // the parser is handed no stream and gives up
	}
	col := &collector{}
	if !parseAll(&stream{data: raw}, col) {
		return nil, Failed
	}
	if !col.draw() {
		return col.calls, Failed
	}
	return col.calls, OK
}

func parseAll(in *stream, col *collector) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			if _, isParse := r.(*parseError); isParse || r == errEndOfStream || r == errSeek {
				ok = false
				return
			}
			panic(r)
		}
	}()
	newParser(in, col).parse()
	return true
}

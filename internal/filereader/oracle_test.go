package filereader

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"testing"
)

// docFromJSON reads a model document as the Python project's dicts dump it.
func docFromJSON(t testing.TB, raw json.RawMessage) *Doc {
	t.Helper()
	var in struct {
		Format string `json:"format"`
		Name   string `json:"name"`
		Pages  []struct {
			Width  float64 `json:"width"`
			Height float64 `json:"height"`
			Items  []struct {
				Type   string     `json:"type"`
				X      float64    `json:"x"`
				Y      float64    `json:"y"`
				W      float64    `json:"w"`
				H      float64    `json:"h"`
				Rotate float64    `json:"rotate"`
				Origin string     `json:"origin"`
				Story  string     `json:"story"`
				Inset  [4]float64 `json:"inset"`
				Valign string     `json:"valign"`
				Nowrap bool       `json:"nowrap"`
				Repeat bool       `json:"repeat"`
				D      string     `json:"d"`
				Fill   string     `json:"fill"`
				Stroke string     `json:"stroke"`
				SW     float64    `json:"sw"`
				Src    string     `json:"src"`
				Label  string     `json:"label"`
			} `json:"items"`
		} `json:"pages"`
		Stories  json.RawMessage `json:"stories"`
		Warnings []string        `json:"warnings"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	doc := NewDoc(in.Format)
	doc.Name = in.Name
	doc.Warnings = in.Warnings
	for _, p := range in.Pages {
		page := &Page{Width: p.Width, Height: p.Height}
		for _, i := range p.Items {
			page.Items = append(page.Items, &Item{Type: i.Type, X: i.X, Y: i.Y, W: i.W, H: i.H, Rotate: i.Rotate,
				Origin: i.Origin, Story: i.Story, Inset: i.Inset, Valign: i.Valign, Nowrap: i.Nowrap, Repeat: i.Repeat,
				D: i.D, Fill: i.Fill, Stroke: i.Stroke, SW: i.SW, Src: i.Src, Label: i.Label})
		}
		doc.Pages = append(doc.Pages, page)
	}
	// stories keep the file's key order
	dec := json.NewDecoder(bytes.NewReader(in.Stories))
	if _, err := dec.Token(); err != nil {
		t.Fatal(err)
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		var blocks []json.RawMessage
		if err := dec.Decode(&blocks); err != nil {
			t.Fatal(err)
		}
		doc.Stories.Set(keyTok.(string), blocksFromJSON(t, blocks))
	}
	return doc
}

func blocksFromJSON(t testing.TB, raws []json.RawMessage) []Block {
	blocks := []Block{}
	for _, raw := range raws {
		var b struct {
			Kind    string  `json:"kind"`
			Align   string  `json:"align"`
			Indent  float64 `json:"indent"`
			Left    float64 `json:"left"`
			Right   float64 `json:"right"`
			Before  float64 `json:"before"`
			After   float64 `json:"after"`
			Leading *float64
			Spans   []struct {
				T         string  `json:"t"`
				Font      string  `json:"font"`
				Size      float64 `json:"size"`
				Bold      bool    `json:"bold"`
				Italic    bool    `json:"italic"`
				Underline bool    `json:"underline"`
				Color     string  `json:"color"`
				Legacy    bool    `json:"legacy"`
			} `json:"spans"`
			Cols []float64 `json:"cols"`
			Rows []struct {
				Cells []struct {
					Col     int               `json:"col"`
					Blocks  []json.RawMessage `json:"blocks"`
					Colspan int               `json:"colspan"`
					Rowspan int               `json:"rowspan"`
					Rotate  float64           `json:"rotate"`
				} `json:"cells"`
			} `json:"rows"`
		}
		if err := json.Unmarshal(raw, &b); err != nil {
			t.Fatal(err)
		}
		var lead struct {
			Leading *float64 `json:"leading"`
		}
		json.Unmarshal(raw, &lead)
		blk := Block{Kind: b.Kind, Align: b.Align, Indent: b.Indent, Left: b.Left, Right: b.Right, Before: b.Before,
			After: b.After, Leading: lead.Leading, Cols: b.Cols}
		for _, s := range b.Spans {
			blk.Spans = append(blk.Spans, Span{T: s.T, Font: s.Font, Size: s.Size, Bold: s.Bold, Italic: s.Italic,
				Underline: s.Underline, Color: s.Color, Legacy: s.Legacy})
		}
		for _, r := range b.Rows {
			row := Row{}
			for _, c := range r.Cells {
				row.Cells = append(row.Cells, Cell{Col: c.Col, Blocks: blocksFromJSON(t, c.Blocks), Colspan: c.Colspan,
					Rowspan: c.Rowspan, Rotate: c.Rotate})
			}
			blk.Rows = append(blk.Rows, row)
		}
		blocks = append(blocks, blk)
	}
	return blocks
}

func zipEntries(t testing.TB, data []byte) map[string]string {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = string(b)
	}
	return out
}

func normalize(t testing.TB, v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

type oracleCase struct {
	File        string            `json:"file"`
	Doc         json.RawMessage   `json:"doc"`
	Filing      json.RawMessage   `json:"filing"`
	IDML        map[string]string `json:"idml"`
	JSX         string            `json:"jsx"`
	FilingError string            `json:"filing_error"`
	IDMLError   string            `json:"idml_error"`
	JSXError    string            `json:"jsx_error"`
	Error       string            `json:"error"`
	Crash       string            `json:"crash"`
}

func checkOutputs(t *testing.T, name string, c oracleCase, doc *Doc) {
	t.Helper()
	if c.FilingError == "" {
		var want any
		json.Unmarshal(c.Filing, &want)
		got := normalize(t, BuildFiling(doc))
		if !reflect.DeepEqual(got, want) {
			gb, _ := json.MarshalIndent(got, "", " ")
			wb, _ := json.MarshalIndent(want, "", " ")
			t.Errorf("%s: filing sheet differs\n got: %.1500s\nwant: %.1500s", name, gb, wb)
		}
	}
	if c.IDMLError == "" {
		data, err := WriteIDML(doc)
		if err != nil {
			t.Fatal(err)
		}
		got := zipEntries(t, data)
		for k, w := range c.IDML {
			g, ok := got[k]
			if !ok {
				t.Errorf("%s: idml lacks %s", name, k)
				continue
			}
			if g != w {
				t.Errorf("%s: idml %s differs\n got: %.1200s\nwant: %.1200s", name, k, g, w)
			}
		}
		for k := range got {
			if _, ok := c.IDML[k]; !ok {
				t.Errorf("%s: idml has extra %s", name, k)
			}
		}
	}
	if c.JSXError == "" {
		got := string(WriteJSX(doc, "My \"deed\" 1"))
		if got != c.JSX {
			i := 0
			for i < len(got) && i < len(c.JSX) && got[i] == c.JSX[i] {
				i++
			}
			lo := max(i-100, 0)
			t.Errorf("%s: jsx differs at %d\n got: %.300q\nwant: %.300q", name, i, got[lo:min(i+200, len(got))], c.JSX[lo:min(i+200, len(c.JSX))])
		}
	}
}

func runOracle(t *testing.T, path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skip(err)
	}
	var cases []oracleCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	failed := 0
	for i, c := range cases {
		if c.Doc == nil {
			continue
		}
		before := t.Failed()
		checkOutputs(t, path+"#"+itoa(i), c, docFromJSON(t, c.Doc))
		if !before && t.Failed() {
			failed++
			if failed >= 3 {
				t.Fatal("too many differences")
			}
		}
	}
	t.Logf("%d cases", len(cases))
}

func TestModelsAgainstDjango(t *testing.T) {
	if p := os.Getenv("FILEREADER_ORACLE"); p != "" {
		runOracle(t, p)
		return
	}
	runOracle(t, "testdata/oracle_models.json")
}

type fileCase struct {
	oracleCase
	RawDoc  json.RawMessage   `json:"raw_doc"`
	RawIDML map[string]string `json:"raw_idml"`
	RawJSX  string            `json:"raw_jsx"`
}

// TestFilesAgainstDjango reads real files (FILEREADER_FILES is their
// directory, FILEREADER_FILES_ORACLE the Django output for them); by
// default it runs the small set kept in testdata.
func TestFilesAgainstDjango(t *testing.T) {
	dir, oracle := os.Getenv("FILEREADER_FILES"), os.Getenv("FILEREADER_FILES_ORACLE")
	if dir == "" || oracle == "" {
		dir, oracle = "testdata/files", "testdata/oracle_files.json"
	}
	raw, err := os.ReadFile(oracle)
	if err != nil {
		t.Fatal(err)
	}
	var cases []fileCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	bad := 0
	for _, c := range cases {
		data, err := os.ReadFile(dir + "/" + c.File)
		if err != nil {
			t.Fatal(err)
		}
		doc, rerr := ReadFile(data, c.File)
		switch {
		case c.Error != "":
			if rerr == nil || rerr.Error() != c.Error {
				t.Errorf("%s: error %v, want %q", c.File, rerr, c.Error)
				bad++
			}
			continue
		case c.Crash != "":
			if rerr == nil {
				t.Errorf("%s: Django crashed (%s), Go read it", c.File, c.Crash)
				bad++
			}
			continue
		case rerr != nil:
			t.Errorf("%s: %v", c.File, rerr)
			bad++
			continue
		}
		var want any
		json.Unmarshal(c.Doc, &want)
		got := normalize(t, doc)
		if !reflect.DeepEqual(got, want) {
			gb, _ := json.Marshal(got)
			wb, _ := json.Marshal(want)
			i := 0
			for i < len(gb) && i < len(wb) && gb[i] == wb[i] {
				i++
			}
			t.Errorf("%s: document differs near %d\n got: %.400s\nwant: %.400s", c.File, i, gb[max(i-150, 0):min(i+250, len(gb))], wb[max(i-150, 0):min(i+250, len(wb))])
			bad++
			continue
		}
		before := t.Failed()
		checkOutputs(t, c.File, c.oracleCase, docFromJSON(t, c.Doc))
		if c.RawDoc != nil {
			rdoc, err := ReadPageMaker(data, false)
			if err != nil {
				t.Errorf("%s: raw read: %v", c.File, err)
			} else {
				rdoc.Name = c.File
				idmlData, _ := WriteIDML(rdoc)
				got := zipEntries(t, idmlData)
				for k, w := range c.RawIDML {
					if got[k] != w {
						t.Errorf("%s: raw idml %s differs\n got: %.600s\nwant: %.600s", c.File, k, got[k], w)
						break
					}
				}
				if j := string(WriteJSX(rdoc, "My \"deed\" 1")); j != c.RawJSX {
					t.Errorf("%s: raw jsx differs", c.File)
				}
			}
		}
		if !before && t.Failed() {
			bad++
		}
		if bad >= 5 {
			t.Fatal("too many differences")
		}
	}
	t.Logf("%d files", len(cases))
}

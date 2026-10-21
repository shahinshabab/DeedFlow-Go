package plumber

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

type expChar struct {
	Text     string  `json:"text"`
	Fontname string  `json:"fontname"`
	X0       float64 `json:"x0"`
	X1       float64 `json:"x1"`
	Top      float64 `json:"top"`
	Bottom   float64 `json:"bottom"`
	Size     float64 `json:"size"`
	Upright  bool    `json:"upright"`
	Adv      float64 `json:"adv"`
}

type expBox struct {
	X0, X1, Top, Bottom, Width, Height float64
	Linewidth                          float64
	Pts                                [][2]float64
}

type expPage struct {
	Width, Height float64
	Chars         []expChar
	Lines         []expBox
	Rects         []expBox
	Curves        []expBox
	Text          string
	TextLines     []struct {
		Text                string
		X0, Top, X1, Bottom float64
		Nchars              int
	} `json:"text_lines"`
	TextLinesNostrip []string      `json:"text_lines_nostrip"`
	Tables           [][][]*string `json:"tables"`
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestAgainstPdfplumber(t *testing.T) {
	raw, err := os.ReadFile("testdata/pdfs/expected.json")
	if err != nil {
		t.Skip("no golden data")
	}
	var exp map[string][]expPage
	if err := json.Unmarshal(raw, &exp); err != nil {
		t.Fatal(err)
	}
	for name, pages := range exp {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata/pdfs", name))
			if err != nil {
				t.Fatal(err)
			}
			doc, err := Open(data)
			if err != nil {
				t.Fatal(err)
			}
			if doc.NumPages() != len(pages) {
				t.Fatalf("pages %d != %d", doc.NumPages(), len(pages))
			}
			for i, ep := range pages {
				p, err := doc.Page(i + 1)
				if err != nil {
					t.Fatal(err)
				}
				if !near(p.Width, ep.Width) || !near(p.Height, ep.Height) {
					t.Errorf("p%d size %v x %v want %v x %v", i+1, p.Width, p.Height, ep.Width, ep.Height)
				}
				if len(p.Chars) != len(ep.Chars) {
					t.Errorf("p%d chars %d want %d", i+1, len(p.Chars), len(ep.Chars))
				} else {
					bad := 0
					for k, c := range p.Chars {
						e := ep.Chars[k]
						if c.Text != e.Text || c.FontName != e.Fontname || !near(c.X0, e.X0) || !near(c.X1, e.X1) ||
							!near(c.Top, e.Top) || !near(c.Bottom, e.Bottom) || !near(c.Size, e.Size) ||
							c.Upright != e.Upright || !near(c.Adv, e.Adv) {
							if bad < 5 {
								t.Errorf("p%d char %d: got %+v want %+v", i+1, k, c, e)
							}
							bad++
						}
					}
				}
				cmp := func(kind string, got []expBox, want []expBox) {
					if len(got) != len(want) {
						t.Errorf("p%d %s %d want %d", i+1, kind, len(got), len(want))
						return
					}
					for k := range got {
						g, w := got[k], want[k]
						if !near(g.X0, w.X0) || !near(g.X1, w.X1) || !near(g.Top, w.Top) || !near(g.Bottom, w.Bottom) ||
							!near(g.Width, w.Width) || !near(g.Height, w.Height) {
							t.Errorf("p%d %s %d: got %+v want %+v", i+1, kind, k, g, w)
						}
					}
				}
				var ls, rs, cs []expBox
				for _, l := range p.Lines {
					ls = append(ls, expBox{X0: l.X0, X1: l.X1, Top: l.Top, Bottom: l.Bottom, Width: l.W, Height: l.H})
				}
				for _, l := range p.Rects {
					rs = append(rs, expBox{X0: l.X0, X1: l.X1, Top: l.Top, Bottom: l.Bottom, Width: l.W, Height: l.H})
				}
				for _, l := range p.Curves {
					cs = append(cs, expBox{X0: l.X0, X1: l.X1, Top: l.Top, Bottom: l.Bottom, Width: l.W, Height: l.H})
				}
				cmp("lines", ls, ep.Lines)
				cmp("rects", rs, ep.Rects)
				cmp("curves", cs, ep.Curves)
				if got := p.ExtractText(); got != ep.Text {
					t.Errorf("p%d text:\n got %q\nwant %q", i+1, got, ep.Text)
				}
				tl := p.ExtractTextLines(true)
				if len(tl) != len(ep.TextLines) {
					t.Errorf("p%d text lines %d want %d", i+1, len(tl), len(ep.TextLines))
				} else {
					for k, l := range tl {
						w := ep.TextLines[k]
						if l.Text != w.Text || !near(l.X0, w.X0) || !near(l.Top, w.Top) || !near(l.X1, w.X1) || !near(l.Bottom, w.Bottom) || len(l.Chars) != w.Nchars {
							t.Errorf("p%d line %d: got %q %v want %q %v", i+1, k, l.Text, l, w.Text, w)
						}
					}
				}
				ns := p.ExtractTextLines(false)
				if len(ns) != len(ep.TextLinesNostrip) {
					t.Errorf("p%d nostrip %d want %d", i+1, len(ns), len(ep.TextLinesNostrip))
				} else {
					for k, l := range ns {
						if l.Text != ep.TextLinesNostrip[k] {
							t.Errorf("p%d nostrip %d: %q want %q", i+1, k, l.Text, ep.TextLinesNostrip[k])
						}
					}
				}
				tabs := p.ExtractTables()
				if tabs == nil {
					tabs = [][][]*string{}
				}
				gj, _ := json.Marshal(tabs)
				wj, _ := json.Marshal(ep.Tables)
				if string(gj) != string(wj) {
					t.Errorf("p%d tables:\n got %s\nwant %s", i+1, gj, wj)
				}
			}
		})
	}
}

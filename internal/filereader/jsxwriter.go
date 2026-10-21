package filereader

import (
	"math"
	"strconv"
	"strings"
)

// Writes a model document as an InDesign script (.jsx) that builds the same
// document inside InDesign, for versions before CS4 that can't open IDML (CS3
// among them). Run it from the Scripts panel and it saves the new document as
// .indd on the desktop.
//
// It lays things out like the IDML writer: text blocks, threads and rules
// where they were, in the original fonts (read the document with
// convert=false), and tables drawn with lines rebuilt as real tables.

func r2(v float64) float64 { return pyRound(v, 2) }

type jsxJSON struct{ e *enc }

func (j jsxJSON) paras(blocks []Block) {
	var ps []Block
	for _, b := range blocks {
		if b.Kind == "p" {
			ps = append(ps, b)
		}
	}
	e := j.e
	e.list(true, len(ps), func(i int) {
		b := ps[i]
		align := b.Align
		if align == "" {
			align = "left"
		}
		leading := 0.0
		if b.Leading != nil {
			leading = r2(*b.Leading)
		}
		e.obj(true,
			kv{"align", func() { e.str(align) }}, kv{"indent", func() { e.num(r2(b.Indent)) }},
			kv{"left", func() { e.num(r2(b.Left)) }}, kv{"right", func() { e.num(r2(b.Right)) }},
			kv{"before", func() { e.num(r2(b.Before)) }}, kv{"after", func() { e.num(r2(b.After)) }},
			kv{"leading", func() {
				if leading == 0 {
					e.raw("null")
				} else {
					e.num(leading)
				}
			}},
			kv{"runs", func() {
				e.list(true, len(b.Spans), func(k int) {
					s := b.Spans[k]
					size := s.Size
					if size == 0 {
						size = 12
					}
					t := strings.ReplaceAll(strings.ReplaceAll(s.T, "\r", " "), "\n", " ")
					e.obj(true,
						kv{"t", func() { e.str(t) }}, kv{"font", func() { e.str(fontName(s.Font)) }},
						kv{"style", func() { e.str(fontStyle(s)) }}, kv{"size", func() { e.num(r2(size)) }},
						kv{"u", func() { e.boolean(s.Underline) }})
				})
			}})
	})
}

func (j jsxJSON) paths(item *Item) []func() {
	var out []func()
	e := j.e
	for _, sub := range pathPoints(item.D) {
		pts := make([][2]float64, len(sub.points))
		for i, p := range sub.points {
			pts[i] = [2]float64{r2(p.x), r2(p.y)}
		}
		if sub.closed && len(pts) > 1 && pts[len(pts)-1] == pts[0] {
			pts = pts[:len(pts)-1]
		}
		if len(pts) >= 2 {
			closed := sub.closed && len(pts) > 2
			sw := item.SW
			if sw == 0 {
				sw = 1
			}
			filled := item.Fill != "" && item.Fill != "none"
			pp := pts
			out = append(out, func() {
				e.obj(true,
					kv{"pts", func() {
						e.list(true, len(pp), func(i int) {
							e.list(true, 2, func(k int) { e.num(pp[i][k]) })
						})
					}},
					kv{"closed", func() { e.boolean(closed) }}, kv{"sw", func() { e.num(r2(sw)) }},
					kv{"fill", func() { e.boolean(filled) }})
			})
		}
	}
	return out
}

func (j jsxJSON) data(doc *Doc) {
	e := j.e
	storyUse := StoryFrameCounts(doc)
	used := map[string]bool{}
	var pages []func()
	for _, page := range doc.Pages {
		found := FindTables(page, doc.Stories, storyUse)
		skip := map[*Item]bool{}
		for _, t := range found {
			for _, i := range t.Paths {
				skip[i] = true
			}
			for _, i := range t.Texts {
				skip[i] = true
			}
		}
		var frames []func()
		var lines []func()
		for _, item := range page.Items {
			if skip[item] {
				continue
			}
			it := item
			switch item.Type {
			case "text":
				used[item.Story] = true
				frames = append(frames, func() {
					e.obj(true, kv{"x", func() { e.num(r2(it.X)) }}, kv{"y", func() { e.num(r2(it.Y)) }},
						kv{"w", func() { e.num(r2(it.W)) }},
						kv{"h", func() {
							if it.H > 0 {
								e.num(r2(it.H))
							} else {
								e.integer(0)
							}
						}},
						kv{"rotate", func() { e.num(r2(it.Rotate)) }}, kv{"story", func() { e.str(it.Story) }})
				})
			case "path":
				lines = append(lines, j.paths(item)...)
			}
		}
		var tables []func()
		for _, t := range found {
			t := t
			var cells []func()
			for _, cell := range t.Cells {
				cell := cell
				var blocks []Block
				for _, item := range cell.Items {
					blocks = append(blocks, cellBlocks(doc.Stories.Get(item.Story))...)
				}
				rotate := 0.0
				for _, i := range cell.Items {
					if i.Rotate != 0 {
						rotate = i.Rotate
						break
					}
				}
				cells = append(cells, func() {
					e.obj(true, kv{"r", func() { e.integer(cell.Row) }}, kv{"c", func() { e.integer(cell.Col) }},
						kv{"rs", func() { e.integer(cell.Rowspan) }}, kv{"cs", func() { e.integer(cell.Colspan) }},
						kv{"rotate", func() {
							// InDesign turns cell text anticlockwise; the model clockwise.
							if rotate != 0 {
								n := -int(math.RoundToEven(rotate/90)) * 90
								e.integer(((n % 360) + 360) % 360)
							} else {
								e.integer(0)
							}
						}},
						kv{"paras", func() { j.paras(blocks) }})
				})
			}
			tables = append(tables, func() {
				e.obj(true, kv{"x", func() { e.num(r2(t.X)) }}, kv{"y", func() { e.num(r2(t.Y)) }},
					kv{"w", func() { e.num(r2(t.W)) }}, kv{"h", func() { e.num(r2(t.H)) }},
					kv{"cols", func() {
						e.list(true, len(t.Cols), func(i int) { e.num(r2(t.Cols[i])) })
					}},
					kv{"rows", func() {
						e.list(true, len(t.Rows), func(i int) { e.num(r2(t.Rows[i])) })
					}},
					kv{"border", func() { e.num(r2(t.Border)) }}, kv{"rule", func() { e.num(r2(t.Rule)) }},
					kv{"cells", func() { e.list(true, len(cells), func(i int) { cells[i]() }) }})
			})
		}
		pg := page
		pages = append(pages, func() {
			e.obj(true, kv{"w", func() { e.num(r2(pg.Width)) }}, kv{"h", func() { e.num(r2(pg.Height)) }},
				kv{"frames", func() { e.list(true, len(frames), func(i int) { frames[i]() }) }},
				kv{"lines", func() { e.list(true, len(lines), func(i int) { lines[i]() }) }},
				kv{"tables", func() { e.list(true, len(tables), func(i int) { tables[i]() }) }})
		})
	}
	e.obj(true,
		kv{"pages", func() { e.list(true, len(pages), func(i int) { pages[i]() }) }},
		kv{"stories", func() {
			e.raw("{")
			first := true
			for _, k := range doc.Stories.Keys() {
				if !used[k] {
					continue
				}
				if !first {
					e.raw(",")
				}
				first = false
				e.str(k)
				e.raw(":")
				j.paras(doc.Stories.Get(k))
			}
			e.raw("}")
		}})
}

// JSXTitled is a PageMaker document and the title to save it under.
type JSXTitled struct {
	Title string
	Doc   *Doc
}

// WriteJSX is the .jsx script for doc, as bytes (plain ASCII).
func WriteJSX(doc *Doc, title string) []byte {
	return WriteJSXMany([]JSXTitled{{title, doc}}, nil)
}

// WriteJSXMany is one .jsx script that builds and saves every document;
// skipped names files that couldn't be read, so the script can say so.
func WriteJSXMany(docs []JSXTitled, skipped []string) []byte {
	titles := make([]string, len(docs))
	for i, d := range docs {
		titles[i] = strings.ReplaceAll(d.Title, "\n", " ")
	}
	e := &enc{ascii: true}
	e.list(true, len(docs), func(i int) {
		e.obj(true, kv{"title", func() { e.str(titles[i]) }}, kv{"data", func() { jsxJSON{e}.data(docs[i].Doc) }})
	})
	data := e.b.String()

	var head, saves string
	if len(titles) == 1 {
		head = "// Builds \"" + strings.ReplaceAll(titles[0], "\"", "'") + "\" in Adobe InDesign (CS3 or later).\n"
		saves = "// It saves the document as .indd on your desktop.\n"
	} else {
		head = "// Builds " + itoa(len(titles)) + " documents in Adobe InDesign (CS3 or later).\n"
		saves = "// It saves them as .indd in a \"PageMaker to InDesign\" folder on your desktop.\n"
	}
	head += "// In InDesign: Window > Automation > Scripts, right-click \"User\" > Reveal in Explorer,\n" +
		"// put this file there, then double-click it in the Scripts panel.\n" + saves + "#target indesign\n"

	se := &enc{ascii: true}
	se.list(false, len(skipped), func(i int) { se.str(skipped[i]) })
	return []byte(head + "var SKIPPED = " + se.b.String() + ";\n" + "var DOCS = " + data + ";\n" + jsxBuilder)
}

func itoa(n int) string { return strconv.Itoa(n) }

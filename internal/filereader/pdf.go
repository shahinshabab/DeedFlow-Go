package filereader

import (
	"fmt"
	"sort"
	"strings"

	"github.com/shahinshabab/deedflow-go/internal/plumber"
)

// Reads PDFs (including InDesign/PageMaker PDF exports): every line of text
// is placed where it sits on the page, with runs in a legacy Malayalam font
// converted to Unicode.

// legacyFonts names the fonts whose text is legacy Malayalam glyph codes,
// judged on all of each font's text in the file (see IsLegacyFont).
func legacyFonts(pages []*plumber.Page) map[string]bool {
	text := map[string]*strings.Builder{}
	var order []string
	for _, page := range pages {
		for i := range page.Chars {
			ch := &page.Chars[i]
			b, ok := text[ch.FontName]
			if !ok {
				b = &strings.Builder{}
				text[ch.FontName] = b
				order = append(order, ch.FontName)
			}
			b.WriteString(ch.Text)
		}
	}
	out := map[string]bool{}
	for _, name := range order {
		if IsLegacyFont(name, text[name].String()) {
			out[name] = true
		}
	}
	return out
}

// lineText is the line's text, with each run in a legacy font converted.
// Spaces pdfplumber put between words (not in `chars`) join the run they're in.
func lineText(line plumber.TextLine, legacy map[string]bool) string {
	type run struct {
		legacy bool
		text   strings.Builder
	}
	var runs []*run
	chars := line.Chars
	i := 0
	for _, ch := range line.Text {
		var isLegacy bool
		if i < len(chars) && chars[i].Text == string(ch) {
			isLegacy = legacy[chars[i].FontName]
			i++
		} else if len(runs) > 0 {
			isLegacy = runs[len(runs)-1].legacy
		}
		if len(runs) > 0 && runs[len(runs)-1].legacy == isLegacy {
			runs[len(runs)-1].text.WriteRune(ch)
		} else {
			r := &run{legacy: isLegacy}
			r.text.WriteRune(ch)
			runs = append(runs, r)
		}
	}
	var sb strings.Builder
	for _, r := range runs {
		if r.legacy {
			sb.WriteString(ToUnicode(r.text.String()))
		} else {
			sb.WriteString(r.text.String())
		}
	}
	return sb.String()
}

func median(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// mode is statistics.mode: the first of the most common values.
func mode(v []string) string {
	counts := map[string]int{}
	var order []string
	for _, x := range v {
		if _, ok := counts[x]; !ok {
			order = append(order, x)
		}
		counts[x]++
	}
	best, bestN := "", 0
	for _, x := range order {
		if counts[x] > bestN {
			best, bestN = x, counts[x]
		}
	}
	return best
}

// ReadPDF reads a PDF.
func ReadPDF(data []byte) (doc *Doc, err error) {
	pd, openErr := plumber.Open(data)
	if openErr != nil {
		return nil, readerErr("This PDF couldn't be opened.")
	}
	defer func() {
		if r := recover(); r != nil {
			doc, err = nil, fmt.Errorf("reading PDF: %v", r)
		}
	}()
	doc = NewDoc("PDF")
	n := pd.NumPages()
	pages := make([]*plumber.Page, 0, n)
	for i := 1; i <= n; i++ {
		p, perr := pd.Page(i)
		if perr != nil {
			return nil, perr
		}
		pages = append(pages, p)
	}
	legacy := legacyFonts(pages)
	emptyPages := 0
	for n, page := range pages {
		var items []*Item
		addEdge := func(x0, top, x1, bottom, lw float64, rect bool) {
			var d string
			if rect {
				d = fmt.Sprintf("M%.2f %.2f H%.2f V%.2f H%.2f Z", x0, top, x1, bottom, x0)
			} else {
				d = fmt.Sprintf("M%.2f %.2f L%.2f %.2f", x0, top, x1, bottom)
			}
			if lw == 0 {
				lw = 0.5
			}
			if lw < 0.25 {
				lw = 0.25
			}
			items = append(items, &Item{Type: "path", D: d, Fill: "none", Stroke: "#000000", SW: pyRound(lw, 2)})
		}
		for _, l := range page.Lines {
			addEdge(l.X0, l.Top, l.X1, l.Bottom, l.LineWidth, false)
		}
		for _, r := range page.Rects {
			addEdge(r.X0, r.Top, r.X1, r.Bottom, r.LineWidth, true)
		}
		for _, img := range page.Images {
			items = append(items, &Item{Type: "image", X: img.X0, Y: img.Top, W: img.X1 - img.X0, H: img.Bottom - img.Top,
				Rotate: 0, Label: "Image"})
		}
		lines := page.ExtractTextLines(false)
		for i, line := range lines {
			text := lineText(line, legacy)
			if strings.TrimFunc(text, pyIsSpace) == "" {
				continue
			}
			chars := line.Chars
			size, font := 10.0, ""
			if len(chars) > 0 {
				sizes := make([]float64, len(chars))
				fonts := make([]string, len(chars))
				for k, c := range chars {
					sizes[k], fonts[k] = c.Size, c.FontName
				}
				size, font = median(sizes), mode(fonts)
			}
			story := fmt.Sprintf("p%dl%d", n, i)
			lower := strings.ToLower(font)
			leading := pyRound(size*1.15, 2)
			para := NewParagraph()
			para.Leading = &leading
			para.Spans = []Span{{T: text, Font: font, Size: pyRound(size, 2), Bold: strings.Contains(lower, "bold"),
				Italic: strings.Contains(lower, "italic"), Underline: false, Color: "#000000", Legacy: text != line.Text}}
			doc.Stories.Set(story, []Block{para})
			items = append(items, &Item{Type: "text", X: line.X0, Y: line.Top - size*0.08, W: line.X1 - line.X0,
				H: line.Bottom - line.Top, Rotate: 0, Story: story, Nowrap: true})
		}
		if len(lines) == 0 {
			emptyPages++
		}
		doc.Pages = append(doc.Pages, &Page{Width: page.Width, Height: page.Height, Items: items})
	}
	if len(doc.Pages) == 0 {
		return nil, readerErr("No pages were found in this PDF.")
	}
	if emptyPages > 0 {
		doc.Warnings = append(doc.Warnings,
			fmt.Sprintf("%d page(s) have no text layer (probably scanned images), so they show up blank.", emptyPages))
	}
	return doc, nil
}

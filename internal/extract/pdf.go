// Package extract turns a deed PDF into text and structured schedule data:
// the Go counterpart of deeds/utils/pdf_extraction.py and table_extraction.py.
package extract

import (
	"bytes"
	"encoding/base64"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/shahinshabab/deedflow-go/internal/fuzzy"
	"github.com/shahinshabab/deedflow-go/internal/plumber"
	"github.com/shahinshabab/deedflow-go/internal/textconv"
)

// Table is one pdfplumber table: rows of cells, nil for a missing cell.
type Table [][]*string

// TextPage is a page without tables, already converted to Unicode.
type TextPage struct {
	PageNumber int
	Text       string
}

// TablePage is a page holding one or more schedule tables.
type TablePage struct {
	PageNumber  int
	Tables      []Table
	ImageBase64 string
}

// Pages is the page classification extract_pages_by_structure returns.
type Pages struct {
	TextPages    []TextPage
	CombinedText string
	TablePages   []TablePage
}

// Font family names marking text set in a standard font rather than the deed
// software's legacy Malayalam font.
var standardFontMarkers = []string{
	"arial", "times", "helvetica", "calibri", "verdana", "courier",
	"cambria", "georgia", "tahoma", "segoe", "roman", "consolas",
}

func isStandardLatinFont(fontname string) bool {
	name := strings.ToLower(fontname)
	for _, m := range standardFontMarkers {
		if strings.Contains(name, m) {
			return true
		}
	}
	return false
}

const latinFontMinChars = 40

const legacyFontSymbols = "\\{}[]|^`~"

// detectLatinFonts names the fonts whose text is plain English by content.
func detectLatinFonts(doc *plumber.Doc) map[string]bool {
	type stat struct{ total, legacy int }
	counts := map[string]*stat{}
	for n := 1; n <= doc.NumPages(); n++ {
		page, err := doc.Page(n)
		if err != nil {
			continue
		}
		for i := range page.Chars {
			ch := &page.Chars[i]
			s := counts[ch.FontName]
			if s == nil {
				s = &stat{}
				counts[ch.FontName] = s
			}
			s.total++
			for _, r := range ch.Text {
				if r > 127 || strings.ContainsRune(legacyFontSymbols, r) {
					s.legacy++
					break
				}
			}
		}
	}
	out := map[string]bool{}
	for name, s := range counts {
		if s.total >= latinFontMinChars && s.legacy == 0 {
			out[name] = true
		}
	}
	return out
}

// convertLineFontAware converts one text line to Unicode, splitting it into
// runs by font: legacy-font runs go through the converter, standard-font runs
// are kept as extracted.
func convertLineFontAware(line plumber.TextLine, conv *textconv.Converter, latin map[string]bool) string {
	type run struct {
		standard bool
		text     string
	}
	var runs []run
	var cur []rune
	curStandard := false
	haveStandard := false
	idx := 0
	for _, ch := range line.Text {
		var isStandard bool
		if idx < len(line.Chars) && line.Chars[idx].Text == string(ch) {
			fn := line.Chars[idx].FontName
			isStandard = latin[fn] || isStandardLatinFont(fn)
			idx++
		} else if haveStandard {
			isStandard = curStandard
		}
		if haveStandard && isStandard != curStandard {
			runs = append(runs, run{curStandard, string(cur)})
			cur = nil
		}
		cur = append(cur, ch)
		curStandard, haveStandard = isStandard, true
	}
	if len(cur) > 0 {
		runs = append(runs, run{curStandard, string(cur)})
	}
	var sb strings.Builder
	for _, r := range runs {
		if r.standard {
			sb.WriteString(r.text)
		} else {
			sb.WriteString(conv.ConvertASCIIToUnicode(r.text))
		}
	}
	return sb.String()
}

// The markers the deed software renders around the substantive body.
const startKeyword = "sImÃw"

var endKeywords = []string{"hkvXp hnh-cWw", "hkvXphnhcWw", "{Ia-\\-¼À"}

// LandTypes are the portal's land-type labels.
var LandTypes = []string{
	"Coastal (belt)", "Commercially Important Plot", "Dry Land",
	"Garden land without road access", "Garden land with road access",
	"Government Property", "Hill Tract without road access",
	"Hill Tract with road access", "Residential plot with Corp.Mun.Panch. road access",
	"Residential plot with NH. PWD road access", "Residential plot without vehicular access",
	"Residential plot with private road access", "Rocky(land)", "Waste(land)",
	"Water logged land", "Wet(land)",
}

func runeIndex(hay, needle []rune, from int) int {
	if len(needle) == 0 {
		return from
	}
	for i := from; i+len(needle) <= len(hay); i++ {
		match := true
		for j := range needle {
			if hay[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func extractTextBetweenKeywords(raw string, start string, ends []string) string {
	rawR := []rune(raw)
	lower := []rune(strings.ToLower(raw))
	startPos := runeIndex(lower, []rune(strings.ToLower(start)), 0)
	if startPos == -1 {
		slog.Warn("Start keyword not found in extracted PDF text", "keyword", start)
		return ""
	}
	endPos := -1
	for _, e := range ends {
		pos := runeIndex(lower, []rune(strings.ToLower(e)), startPos)
		if pos != -1 && (endPos == -1 || pos < endPos) {
			endPos = pos
		}
	}
	if endPos == -1 {
		slog.Warn("No end keyword found after start keyword; returning empty text.")
		return ""
	}
	stop := endPos + len([]rune(ends[0]))
	if stop > len(rawR) {
		stop = len(rawR)
	}
	if startPos > stop {
		return ""
	}
	return string(rawR[startPos:stop])
}

type landPattern struct {
	re       *regexp.Regexp
	landType string
}

// The deed's own road-access wording, mapped onto the portal's labels.
var landTypePatterns = []landPattern{
	{regexp.MustCompile(`(?i)residential[\s\p{Z}]+plot[\s\p{Z}]+with[\s\p{Z}]+(?:the[\s\p{Z}]+)?(?:panchay|grama|munici|corp)`),
		"Residential plot with Corp.Mun.Panch. road access"},
	{regexp.MustCompile(`(?i)residential[\s\p{Z}]+plot[\s\p{Z}]+with[\s\p{Z}]+(?:the[\s\p{Z}]+)?(?:nh|n\.h|pwd|p\.w\.d|national)`),
		"Residential plot with NH. PWD road access"},
	{regexp.MustCompile(`(?i)residential[\s\p{Z}]+plot[\s\p{Z}]+with[\s\p{Z}]+(?:a[\s\p{Z}]+)?private`),
		"Residential plot with private road access"},
	{regexp.MustCompile(`(?i)residential[\s\p{Z}]+plot[\s\p{Z}]+without`),
		"Residential plot without vehicular access"},
}

// ExtractLandType finds the property's land type: first by the deed's own
// wording, then by fuzzy-matching the tail of text against the labels.
func ExtractLandType(text string) string { return extractLandType(text, 900, 75) }

func extractLandType(text string, limit, threshold int) string {
	for _, p := range landTypePatterns {
		if p.re.MatchString(text) {
			slog.Info("Detected land type from the deed's wording", "land_type", p.landType)
			return p.landType
		}
	}
	snippet := []rune(strings.ToLower(text))
	if len(snippet) > limit {
		snippet = snippet[len(snippet)-limit:]
	}
	bestScore, best := 0, ""
	for _, lt := range LandTypes {
		score := fuzzy.PartialRatio(string(snippet), strings.ToLower(lt))
		if score > bestScore && score >= threshold {
			bestScore, best = score, lt
		}
	}
	if best != "" {
		slog.Info("Detected land type", "land_type", best, "score", bestScore)
	} else {
		slog.Info("No land type match found above threshold", "threshold", threshold)
	}
	return best
}

func pageTables(page *plumber.Page) []Table {
	var out []Table
	for _, t := range page.ExtractTables() {
		out = append(out, Table(t))
	}
	return out
}

// ExtractTextAndTables extracts (scoped text, tables by index, land type);
// the keyword-scoped flow the AI test page still uses.
func ExtractTextAndTables(pdfBytes []byte) (string, []Table, string) {
	doc, err := plumber.Open(pdfBytes)
	if err != nil {
		slog.Error("Error extracting text/tables from PDF", "err", err)
		return "", nil, ""
	}
	var text strings.Builder
	var tables []Table
	for n := 1; n <= doc.NumPages(); n++ {
		page, err := doc.Page(n)
		if err != nil {
			slog.Error("Error extracting text/tables from PDF", "err", err)
			return "", nil, ""
		}
		pt := page.ExtractText()
		text.WriteString(pt)
		pg := pageTables(page)
		tables = append(tables, pg...)
		slog.Info("PDF page", "page", n, "chars", len([]rune(pt)), "tables", len(pg))
	}
	scoped := extractTextBetweenKeywords(text.String(), startKeyword, endKeywords)
	land := ExtractLandType(scoped)
	slog.Info("PDF extraction complete", "chars", len([]rune(scoped)), "tables", len(tables), "land_type", land)
	return scoped, tables, land
}

// renderPageImageBase64 renders a page to a base64 PNG with poppler's
// pdftoppm when it is installed ("" otherwise).
func renderPageImageBase64(pdfBytes []byte, pageNum, resolution int) string {
	bin, err := exec.LookPath("pdftoppm")
	if err != nil {
		return ""
	}
	dir, err := os.MkdirTemp("", "deedflow-page")
	if err != nil {
		return ""
	}
	defer os.RemoveAll(dir)
	in := filepath.Join(dir, "in.pdf")
	if err := os.WriteFile(in, pdfBytes, 0o600); err != nil {
		return ""
	}
	out := filepath.Join(dir, "page")
	cmd := exec.Command(bin, "-png", "-singlefile", "-r", strconv.Itoa(resolution),
		"-f", strconv.Itoa(pageNum), "-l", strconv.Itoa(pageNum), in, out)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		slog.Error("Failed to render a PDF page to an image", "page", pageNum, "err", err, "stderr", stderr.String())
		return ""
	}
	png, err := os.ReadFile(out + ".png")
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(png)
}

// ExtractPagesByStructure classifies every page as a table page or a text
// page; text pages are converted to Unicode font-aware and joined.
func ExtractPagesByStructure(pdfBytes []byte, renderImages bool) Pages {
	var res Pages
	conv := textconv.Default()
	doc, err := plumber.Open(pdfBytes)
	if err != nil {
		slog.Error("Error classifying PDF pages by structure", "err", err)
		return res
	}
	func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("Error classifying PDF pages by structure", "err", r)
			}
		}()
		latin := detectLatinFonts(doc)
		for n := 1; n <= doc.NumPages(); n++ {
			page, err := doc.Page(n)
			if err != nil {
				slog.Error("Error classifying PDF pages by structure", "err", err)
				return
			}
			tables := pageTables(page)
			if len(tables) > 0 {
				tp := TablePage{PageNumber: n, Tables: tables}
				if renderImages {
					tp.ImageBase64 = renderPageImageBase64(pdfBytes, n, 150)
				}
				res.TablePages = append(res.TablePages, tp)
				slog.Info("PDF page: table page", "page", n, "tables", len(tables))
				continue
			}
			var lines []string
			for _, l := range page.ExtractTextLines(true) {
				lines = append(lines, convertLineFontAware(l, conv, latin))
			}
			text := strings.Join(lines, "\n")
			res.TextPages = append(res.TextPages, TextPage{PageNumber: n, Text: text})
			slog.Info("PDF page: text page", "page", n, "chars", len([]rune(text)))
		}
	}()
	parts := make([]string, len(res.TextPages))
	for i, p := range res.TextPages {
		parts[i] = p.Text
	}
	res.CombinedText = strings.Join(parts, "\n\n")
	return res
}

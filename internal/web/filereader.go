package web

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shahinshabab/deedflow-go/internal/filereader"
	"github.com/shahinshabab/deedflow-go/internal/models"
)

// The File reader: upload a desktop-publishing file (PageMaker, InDesign IDML,
// PDF) and see every page of it, convert PageMaker files for InDesign, and
// write deeds out as editable filing sheets.

func (a *App) registerFilereader() {
	a.Route("filereader:index", "file-reader/", a.LoginRequired(a.fileReader))
	a.Route("filereader:filing", "file-reader/filing-sheets/", a.LoginRequired(a.filingList))
	a.Route("filereader:filing_new", "file-reader/filing-sheets/new/", a.LoginRequired(a.filingNew))
	a.Route("filereader:filing_edit", "file-reader/filing-sheets/<uuid:public_id>/edit/", a.LoginRequired(a.filingEdit))
	a.Route("filereader:filing_view", "file-reader/filing-sheets/<uuid:public_id>/view/", a.LoginRequired(a.filingView))
	a.Route("filereader:filing_save", "file-reader/filing-sheets/<uuid:public_id>/save/", a.LoginRequired(a.filingSave))
	a.Route("filereader:filing_redirect", "file-reader/filing-sheet/", func(c *Ctx) { c.RedirectTo("filereader:filing") })
	a.Route("filereader:filing_convert", "file-reader/filing-sheet/convert/", a.LoginRequired(a.filingConvert))
}

const maxReaderUploadBytes = 100 * 1024 * 1024

// upload is one uploaded file (data is nil for one over the size limit).
type upload struct {
	name string
	data []byte
}

func readUpload(h *multipart.FileHeader) upload {
	name := path.Base(strings.ReplaceAll(h.Filename, "\\", "/"))
	if h.Size > maxReaderUploadBytes {
		return upload{name: name, data: nil}
	}
	f, err := h.Open()
	if err != nil {
		return upload{name: name}
	}
	defer f.Close()
	data, _ := io.ReadAll(f)
	return upload{name: name, data: data}
}

// lastFile is request.FILES.get(field): the last file sent under the name.
func (c *Ctx) lastFile(field string) (*multipart.FileHeader, bool) {
	if c.R.MultipartForm == nil {
		return nil, false
	}
	files := c.R.MultipartForm.File[field]
	if len(files) == 0 {
		return nil, false
	}
	return files[len(files)-1], true
}

// docContext is the document as the File reader template reads it.
type docContext map[string]any

// MarshalJSON embeds the document the way the reader's script expects.
func (d docContext) MarshalJSON() ([]byte, error) { return d["_json"].([]byte), nil }

func newDocContext(doc *filereader.Doc) docContext {
	pages := make([]any, len(doc.Pages))
	return docContext{
		"name": doc.Name, "format": doc.Format, "warnings": doc.Warnings, "pages": pages,
		"_json": doc.JSON(),
	}
}

func isReaderError(err error) (string, bool) {
	var re *filereader.ReaderError
	if errors.As(err, &re) {
		return re.Msg, true
	}
	return "", false
}

func accepted() string { return strings.Join(filereader.Accepted, ",") }

func (a *App) fileReader(c *Ctx) {
	ctx := Data{"accept": accepted()}
	post := c.R.Method == http.MethodPost
	action := ""
	if post {
		action = c.R.PostFormValue("action")
	}
	if post && (action == "idml" || action == "jsx") {
		if files := c.R.MultipartForm; files != nil && len(files.File["file"]) > 1 {
			if err := a.manyForInDesign(c, action); err != nil {
				msg, _ := isReaderError(err)
				ctx["error"] = msg
				c.Render("filereader/reader.html", ctx)
			}
			return
		}
	}
	var h *multipart.FileHeader
	var have bool
	if post {
		h, have = c.lastFile("file")
	}
	switch {
	case post && !have:
		ctx["error"] = "Choose a file to read."
	case have && h.Size > maxReaderUploadBytes:
		ctx["error"] = "That file is too large (the limit is 100 MB)."
		ctx["filename"] = readUpload(h).name
	case have:
		up := readUpload(h)
		if action == "idml" || action == "jsx" {
			done, err := a.forInDesign(c, up, action)
			if done {
				return
			}
			if msg, ok := isReaderError(err); ok {
				ctx["error"] = msg
			} else {
				a.Log.Error("File reader failed", "file", up.name, "err", err)
				ctx["error"] = "Something went wrong reading this file."
			}
		} else {
			doc, err := safeRead(func() (*filereader.Doc, error) { return filereader.ReadFile(up.data, up.name) })
			if err != nil {
				if msg, ok := isReaderError(err); ok {
					ctx["error"] = msg
				} else {
					a.Log.Error("File reader failed", "file", up.name, "err", err)
					ctx["error"] = "Something went wrong reading this file."
				}
			} else {
				ctx["document"] = newDocContext(doc)
			}
		}
		ctx["filename"] = up.name
	}
	c.Render("filereader/reader.html", ctx)
}

// safeRead runs a reader, turning a panic into an error.
func safeRead(fn func() (*filereader.Doc, error)) (doc *filereader.Doc, err error) {
	defer func() {
		if r := recover(); r != nil {
			doc, err = nil, fmt.Errorf("%v", r)
		}
	}()
	return fn()
}

func pySplitext(name string) (string, string) {
	i := strings.LastIndex(name, ".")
	if i < 0 {
		return name, ""
	}
	// leading dots belong to the stem
	if strings.TrimLeft(name[:i], ".") == "" {
		return name, ""
	}
	return name[:i], name[i:]
}

func docStem(filename string) string {
	stem, _ := pySplitext(path.Base(filename))
	stem = strings.ReplaceAll(stem, `"`, "")
	if stem == "" {
		return "document"
	}
	return stem
}

// forInDesign answers with the PageMaker file for InDesign, laid out the same
// way, in its own fonts, with tables drawn in lines made real tables: as an
// IDML file (InDesign CS4 and later), or as a script that builds the document
// (any version from CS3, which can't open IDML). done is false when an error
// should be shown on the page instead.
func (a *App) forInDesign(c *Ctx, up upload, kind string) (bool, error) {
	if !filereader.IsPageMakerName(up.name) {
		return false, &filereader.ReaderError{Msg: "Only PageMaker files (.pmd, .p65) can be converted to InDesign."}
	}
	doc, err := safeRead(func() (*filereader.Doc, error) { return filereader.ReadPageMaker(up.data, false) })
	if err != nil {
		return false, err
	}
	stem := docStem(up.name)
	if kind == "jsx" {
		c.download("application/javascript", stem+".jsx", filereader.WriteJSX(doc, stem))
		return true, nil
	}
	data, err := filereader.WriteIDML(doc)
	if err != nil {
		return false, err
	}
	c.download("application/vnd.adobe.indesign-idml-package", stem+".idml", data)
	return true, nil
}

func (c *Ctx) download(contentType, filename string, data []byte) {
	c.W.Header().Set("Content-Type", contentType)
	c.W.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.SaveSession()
	_, _ = c.W.Write(data)
}

// manyForInDesign converts several PageMaker files at once: one script that
// builds and saves them all (CS3), or a zip of IDML files. Files that can't
// be read are left out and named.
func (a *App) manyForInDesign(c *Ctx, kind string) error {
	headers := c.R.MultipartForm.File["file"]
	var total int64
	for _, h := range headers {
		total += h.Size
	}
	if total > maxReaderUploadBytes {
		return &filereader.ReaderError{Msg: "Those files are too large together (the limit is 100 MB)."}
	}
	var docs []filereader.JSXTitled
	var skipped []string
	for _, h := range headers {
		up := readUpload(h)
		if !filereader.IsPageMakerName(up.name) {
			skipped = append(skipped, up.name+" (not a PageMaker file)")
			continue
		}
		doc, err := safeRead(func() (*filereader.Doc, error) { return filereader.ReadPageMaker(up.data, false) })
		if err != nil {
			if _, ok := isReaderError(err); !ok {
				a.Log.Error("File reader failed", "file", up.name, "err", err)
			}
			skipped = append(skipped, up.name)
			continue
		}
		docs = append(docs, filereader.JSXTitled{Title: docStem(up.name), Doc: doc})
	}
	if len(docs) == 0 {
		return &filereader.ReaderError{Msg: "None of these files could be converted: " + strings.Join(skipped, ", ")}
	}
	if kind == "jsx" {
		c.download("application/javascript", "PageMaker to InDesign CS3.jsx", filereader.WriteJSXMany(docs, skipped))
		return nil
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	names := map[string]bool{}
	add := func(name string, data []byte) error {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	}
	for _, d := range docs {
		name, n := d.Title, 2
		for names[strings.ToLower(name)] {
			name, n = fmt.Sprintf("%s (%d)", d.Title, n), n+1
		}
		names[strings.ToLower(name)] = true
		data, err := filereader.WriteIDML(d.Doc)
		if err != nil {
			return err
		}
		if err := add(name+".idml", data); err != nil {
			return err
		}
	}
	if len(skipped) > 0 {
		if err := add("Not converted.txt", []byte(strings.Join(skipped, "\r\n")+"\r\n")); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	c.download("application/zip", "PageMaker to InDesign.zip", buf.Bytes())
	return nil
}

// --- filing sheets -----------------------------------------------------------------

func (a *App) filingList(c *Ctx) {
	v, err := c.viewer()
	if err != nil {
		c.ServerError(err)
		return
	}
	q := strings.TrimFunc(c.Query("q"), isSpaceRune)
	_, total, err := a.Store.ListFilingSheets(c.Context(), v, q, 1, 0)
	if err != nil {
		c.ServerError(err)
		return
	}
	pager := NewPager(total, 20, c.Query("page"))
	rows, _, err := a.Store.ListFilingSheets(c.Context(), v, q, pager.PerPage, pager.Offset())
	if err != nil {
		c.ServerError(err)
		return
	}
	objs := make([]any, len(rows))
	for i, r := range rows {
		objs[i] = map[string]any{"public_id": r.PublicID, "name": r.Name, "created_at": r.CreatedAt, "pages": r.Pages}
	}
	c.Render("filereader/filing_list.html", Data{
		"page": pager.Map(objs), "q": q, "query_string": queryWithout(c.R.URL.RawQuery, "page"),
	})
}

func isSpaceRune(r rune) bool {
	return r == ' ' || (r >= '\t' && r <= '\r') || r == 0x1c || r == 0x1d || r == 0x1e || r == 0x1f || r == 0x85 || r == 0xa0
}

func (a *App) filingNew(c *Ctx) {
	ctx := Data{"accept": accepted()}
	post := c.R.Method == http.MethodPost
	var h *multipart.FileHeader
	var have bool
	if post {
		h, have = c.lastFile("file")
	}
	switch {
	case post && !have:
		ctx["error"] = "Choose a file to open."
	case have && h.Size > maxReaderUploadBytes:
		ctx["error"] = "That file is too large (the limit is 100 MB)."
		ctx["filename"] = readUpload(h).name
	case have:
		up := readUpload(h)
		sheet, err := func() (s *filereader.Sheet, err error) {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("%v", r)
				}
			}()
			doc, err := filereader.ReadFile(up.data, up.name)
			if err != nil {
				return nil, err
			}
			return filereader.BuildFiling(doc), nil
		}()
		if err != nil {
			if msg, ok := isReaderError(err); ok {
				ctx["error"] = msg
			} else {
				a.Log.Error("Filing sheet failed", "file", up.name, "err", err)
				ctx["error"] = "Something went wrong reading this file."
			}
			ctx["filename"] = up.name
			break
		}
		raw, err := json.Marshal(sheet)
		if err != nil {
			c.ServerError(err)
			return
		}
		name := up.name
		if name == "" {
			name = "Filing sheet"
		}
		f := &models.FilingSheet{OwnerID: c.User.ID, Name: truncRunes(name, 255), SourceFormat: truncRunes(sheet.Format, 40),
			Sheet: raw, Pages: int32(sheet.Pages)}
		if err := a.Store.CreateFilingSheet(c.Context(), f); err != nil {
			c.ServerError(err)
			return
		}
		c.SaveSession()
		c.RedirectTo("filereader:filing_edit", f.PublicID)
		return
	}
	c.Render("filereader/filing_sheet.html", ctx)
}

func truncRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func (a *App) filingPage(c *Ctx, view bool) {
	id := strings.ToLower(c.PathValue("public_id"))
	if !uuidRe.MatchString(id) {
		c.NotFound()
		return
	}
	v, err := c.viewer()
	if err != nil {
		c.ServerError(err)
		return
	}
	rec, err := a.Store.FilingSheetByPublicID(c.Context(), v, id)
	if err != nil {
		c.ServerError(err)
		return
	}
	if rec == nil {
		c.NotFound()
		return
	}
	var sheet, settings any
	_ = json.Unmarshal(rec.Sheet, &sheet)
	if json.Unmarshal(rec.Settings, &settings) != nil || settings == nil {
		settings = map[string]any{}
	}
	c.Render("filereader/filing_sheet.html", Data{
		"record": map[string]any{"name": rec.Name, "public_id": rec.PublicID, "content": rec.Content,
			"created_at": rec.CreatedAt, "updated_at": rec.UpdatedAt},
		"sheet": sheet, "view": view,
		"saved": map[string]any{"content": rec.Content, "settings": settings},
	})
}

func (a *App) filingEdit(c *Ctx) { a.filingPage(c, false) }
func (a *App) filingView(c *Ctx) { a.filingPage(c, true) }

// maxBodyBytes is Django's DATA_UPLOAD_MAX_MEMORY_SIZE: reading request.body
// past it is a 400.
const maxBodyBytes = 2621440

// readBody is request.body; tooBig says a 400 was already sent.
func (c *Ctx) readBody() (body []byte, tooBig bool) {
	body, _ = io.ReadAll(io.LimitReader(c.R.Body, maxBodyBytes+1))
	if len(body) > maxBodyBytes {
		c.W.Header().Set("Content-Type", "text/html; charset=utf-8")
		c.W.WriteHeader(http.StatusBadRequest)
		_, _ = c.W.Write([]byte("<!doctype html><html lang=\"en\"><head><title></title></head><body><h1>Bad Request (400)</h1><p></p></body></html>"))
		return nil, true
	}
	return body, false
}

const (
	maxSaveChars    = 10_000_000
	maxConvertChars = 2_000_000
)

func (c *Ctx) jsonStatus(status int, v any) {
	c.W.Header().Set("Content-Type", "application/json")
	c.W.WriteHeader(status)
	_ = json.NewEncoder(c.W).Encode(v)
}

// pyStr is Python's str() of a decoded JSON value.
func pyStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return "None"
	case bool:
		if x {
			return "True"
		}
		return "False"
	case json.Number:
		if i, err := x.Int64(); err == nil && !strings.ContainsAny(string(x), ".eE") {
			return strconv.FormatInt(i, 10)
		}
		f, _ := x.Float64()
		return filereader.PyRepr(f)
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = pyRepr(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = pyRepr(k) + ": " + pyRepr(x[k])
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return fmt.Sprint(v)
}

func pyRepr(v any) string {
	if s, ok := v.(string); ok {
		q := "'"
		if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
			q = `"`
		}
		s = strings.ReplaceAll(s, `\`, `\\`)
		if q == "'" {
			s = strings.ReplaceAll(s, "'", `\'`)
		}
		s = strings.NewReplacer("\n", `\n`, "\r", `\r`, "\t", `\t`).Replace(s)
		return q + s + q
	}
	return pyStr(v)
}

// pyInt is int(v) for a decoded JSON value.
func pyInt(v any) (int64, bool) {
	switch x := v.(type) {
	case nil:
		return 0, true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i, true
		}
		f, err := x.Float64()
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || math.Abs(f) > 9e18 {
			return 0, false
		}
		return int64(f), true
	case string:
		i, err := strconv.ParseInt(strings.TrimFunc(x, isSpaceRune), 10, 64)
		return i, err == nil
	}
	return 0, false
}

// pyTruthy is Python's bool() of a decoded JSON value.
func pyTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case json.Number:
		f, _ := x.Float64()
		return f != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

func decodeJSONBody(body []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("extra data")
	}
	return v, nil
}

// filingSave stores the sheet's corrected text and its settings.
//
//	{"content": html, "settings": {...}, "pages": int} -> {"saved": iso time}
func (a *App) filingSave(c *Ctx) {
	if c.R.Method != http.MethodPost {
		http.Error(c.W, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.ToLower(c.PathValue("public_id"))
	if !uuidRe.MatchString(id) {
		c.NotFound()
		return
	}
	v, err := c.viewer()
	if err != nil {
		c.ServerError(err)
		return
	}
	rec, err := a.Store.FilingSheetByPublicID(c.Context(), v, id)
	if err != nil {
		c.ServerError(err)
		return
	}
	if rec == nil {
		c.NotFound()
		return
	}
	body, tooBig := c.readBody()
	if tooBig {
		return
	}
	if len(body) > maxSaveChars {
		c.jsonStatus(http.StatusBadRequest, map[string]string{"error": "This sheet is too large to save."})
		return
	}
	bad := func() { c.jsonStatus(http.StatusBadRequest, map[string]string{"error": "Bad request."}) }
	parsed, err := decodeJSONBody(body)
	obj, isObj := parsed.(map[string]any)
	if err != nil || !isObj {
		bad()
		return
	}
	rawContent, ok := obj["content"]
	if !ok {
		bad()
		return
	}
	content := pyStr(rawContent)
	settings := obj["settings"]
	if !pyTruthy(settings) {
		settings = map[string]any{}
	}
	pages, ok := pyInt(obj["pages"])
	if !pyTruthy(obj["pages"]) {
		pages, ok = 0, true
	}
	if !ok {
		bad()
		return
	}
	settingsMap, isMap := settings.(map[string]any)
	if !isMap || pages < 0 || pages > 10000 {
		bad()
		return
	}
	rawSettings, _ := json.Marshal(settingsMap)
	at, err := a.Store.SaveFilingSheet(c.Context(), rec.ID, content, rawSettings, int32(pages))
	if err != nil {
		c.ServerError(err)
		return
	}
	c.jsonStatus(http.StatusOK, map[string]string{"saved": isoformat(at)})
}

// isoformat is datetime.isoformat() for a UTC time.
func isoformat(t time.Time) string {
	t = t.UTC()
	layout := "2006-01-02T15:04:05"
	if t.Nanosecond()/1000 != 0 {
		layout += ".000000"
	}
	return t.Format(layout) + "+00:00"
}

// filingConvert switches the sheet's text between Unicode Malayalam and the
// codes of the legacy ML-TT fonts, for showing it in ML-TT Karthika and back.
//
//	{"to": "legacy", "texts": [str]} -> {"texts": [[[text, is_legacy], ...]]}
//	{"to": "unicode", "texts": [str]} -> {"texts": [str]}
func (a *App) filingConvert(c *Ctx) {
	if c.R.Method != http.MethodPost {
		http.Error(c.W, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	body, tooBig := c.readBody()
	if tooBig {
		return
	}
	bad := func() { c.jsonStatus(http.StatusBadRequest, map[string]string{"error": "Bad request."}) }
	parsed, err := decodeJSONBody(body)
	obj, isObj := parsed.(map[string]any)
	if err != nil || !isObj {
		bad()
		return
	}
	rawTexts, ok1 := obj["texts"]
	to, ok2 := obj["to"]
	list, isList := rawTexts.([]any)
	if !ok1 || !ok2 || !isList {
		bad()
		return
	}
	texts := make([]string, len(list))
	total := 0
	for i, t := range list {
		texts[i] = pyStr(t)
		total += utf8.RuneCountInString(texts[i])
	}
	toS, _ := to.(string)
	if (toS != "legacy" && toS != "unicode") || total > maxConvertChars {
		bad()
		return
	}
	if toS == "legacy" {
		out := make([][][]any, len(texts))
		for i, t := range texts {
			pieces := filereader.FromUnicode(t)
			out[i] = make([][]any, len(pieces))
			for j, p := range pieces {
				out[i][j] = []any{p.Text, p.Legacy}
			}
		}
		c.jsonStatus(http.StatusOK, map[string]any{"texts": out})
		return
	}
	out := make([]string, len(texts))
	for i, t := range texts {
		if strings.TrimFunc(t, isSpaceRune) != "" {
			out[i] = filereader.ToUnicode(t)
		} else {
			out[i] = t
		}
	}
	c.jsonStatus(http.StatusOK, map[string]any{"texts": out})
}

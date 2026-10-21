package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"io/fs"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/flosch/pongo2/v6"

	"github.com/shahinshabab/deedflow-go/web"
)

// templates wraps the pongo2 set. Templates are written in Django's syntax;
// the custom tags and filters below stand in for Django's {% url %},
// {% static %}, {% csrf_token %}, |date, |floatformat, |json_script ...
type templates struct {
	app *App
	set *pongo2.TemplateSet
}

// current is the app the registered tags talk to. Tags are registered
// globally in pongo2, and there is one App per process.
var current *App

func newTemplates(a *App) (*templates, error) {
	current = a
	sub, err := fs.Sub(web.FS, "templates")
	if err != nil {
		return nil, err
	}
	set := pongo2.NewSet("deedflow", rootLoader{pongo2.NewFSLoader(sub)})
	set.Debug = a.Cfg.Debug
	return &templates{app: a, set: set}, nil
}

// rootLoader resolves every template name from the template root, like Django
// (pongo2's default resolves relative to the including template).
type rootLoader struct{ *pongo2.FSLoader }

func (rootLoader) Abs(base, name string) string { return name }

// loopVars maps Django's forloop attributes to pongo2's capitalised ones.
var loopVars = regexp.MustCompile(`forloop\.(counter0|counter|first|last|revcounter0|revcounter)\b`)

var loopVarNames = map[string]string{"counter0": "Counter0", "counter": "Counter", "first": "First", "last": "Last",
	"revcounter0": "Revcounter0", "revcounter": "Revcounter"}

// Get reads a template, translating forloop.counter and friends so the
// templates can keep Django's spelling.
func (l rootLoader) Get(path string) (io.Reader, error) {
	r, err := l.FSLoader.Get(path)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	data = loopVars.ReplaceAllFunc(data, func(m []byte) []byte {
		return []byte("forloop." + loopVarNames[string(m[len("forloop."):])])
	})
	return bytes.NewReader(data), nil
}

func init() {
	pongo2.SetAutoescape(true)
	must(pongo2.RegisterTag("url", tagURL))
	must(pongo2.RegisterTag("static", tagStatic))
	must(pongo2.RegisterTag("csrf_token", tagCSRF))
	must(pongo2.RegisterTag("load", tagLoad))
	must(pongo2.RegisterTag("legacy_font_faces", tagFontFaces))
	must(pongo2.ReplaceFilter("date", filterDate))
	must(pongo2.ReplaceFilter("floatformat", filterFloatformat))
	must(pongo2.RegisterFilter("json_script", filterJSONScript))
	must(pongo2.RegisterFilter("number", filterNumber))
	must(pongo2.RegisterFilter("tojson", filterToJSON))
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

// --- {% load ... %} (a no-op: everything is always loaded) -------------------

type nodeNoop struct{}

func (nodeNoop) Execute(*pongo2.ExecutionContext, pongo2.TemplateWriter) *pongo2.Error { return nil }

func tagLoad(doc *pongo2.Parser, start *pongo2.Token, arguments *pongo2.Parser) (pongo2.INodeTag, *pongo2.Error) {
	for arguments.Remaining() > 0 {
		arguments.Consume()
	}
	return nodeNoop{}, nil
}

// --- {% static 'path' %} ------------------------------------------------------

type nodeStatic struct{ path pongo2.IEvaluator }

func (n *nodeStatic) Execute(ctx *pongo2.ExecutionContext, w pongo2.TemplateWriter) *pongo2.Error {
	v, err := n.path.Evaluate(ctx)
	if err != nil {
		return err
	}
	w.WriteString(html.EscapeString(current.StaticURL(v.String())))
	return nil
}

func tagStatic(doc *pongo2.Parser, start *pongo2.Token, arguments *pongo2.Parser) (pongo2.INodeTag, *pongo2.Error) {
	expr, err := arguments.ParseExpression()
	if err != nil {
		return nil, err
	}
	return &nodeStatic{path: expr}, nil
}

// --- {% url 'name' arg ... key=value ... %} -----------------------------------

type nodeURL struct {
	name   pongo2.IEvaluator
	args   []pongo2.IEvaluator
	kwargs map[string]pongo2.IEvaluator
}

func (n *nodeURL) Execute(ctx *pongo2.ExecutionContext, w pongo2.TemplateWriter) *pongo2.Error {
	nameV, err := n.name.Evaluate(ctx)
	if err != nil {
		return err
	}
	var u string
	var rerr error
	if len(n.kwargs) > 0 {
		kw := map[string]any{}
		for k, e := range n.kwargs {
			v, err := e.Evaluate(ctx)
			if err != nil {
				return err
			}
			kw[k] = v.Interface()
		}
		u, rerr = current.Router.ReverseKw(nameV.String(), kw)
	} else {
		args := make([]any, len(n.args))
		for i, e := range n.args {
			v, err := e.Evaluate(ctx)
			if err != nil {
				return err
			}
			args[i] = v.Interface()
		}
		u, rerr = current.Router.Reverse(nameV.String(), args...)
	}
	if rerr != nil {
		return ctx.Error(rerr.Error(), nil)
	}
	w.WriteString(html.EscapeString(u))
	return nil
}

func tagURL(doc *pongo2.Parser, start *pongo2.Token, arguments *pongo2.Parser) (pongo2.INodeTag, *pongo2.Error) {
	name, err := arguments.ParseExpression()
	if err != nil {
		return nil, err
	}
	n := &nodeURL{name: name, kwargs: map[string]pongo2.IEvaluator{}}
	for arguments.Remaining() > 0 {
		// key=value or a positional expression
		if arguments.PeekTypeN(0, pongo2.TokenIdentifier) != nil && arguments.PeekN(1, pongo2.TokenSymbol, "=") != nil {
			key := arguments.MatchType(pongo2.TokenIdentifier).Val
			arguments.Match(pongo2.TokenSymbol, "=")
			expr, err := arguments.ParseExpression()
			if err != nil {
				return nil, err
			}
			n.kwargs[key] = expr
			continue
		}
		expr, err := arguments.ParseExpression()
		if err != nil {
			return nil, err
		}
		n.args = append(n.args, expr)
	}
	return n, nil
}

// --- {% csrf_token %} ----------------------------------------------------------

type nodeCSRF struct{}

func (nodeCSRF) Execute(ctx *pongo2.ExecutionContext, w pongo2.TemplateWriter) *pongo2.Error {
	tok, _ := ctx.Public["csrf_token"].(string)
	w.WriteString(`<input type="hidden" name="csrfmiddlewaretoken" value="` + html.EscapeString(tok) + `">`)
	return nil
}

func tagCSRF(doc *pongo2.Parser, start *pongo2.Token, arguments *pongo2.Parser) (pongo2.INodeTag, *pongo2.Error) {
	return nodeCSRF{}, nil
}

// --- {% legacy_font_faces %} --------------------------------------------------

type nodeFontFaces struct{}

func (nodeFontFaces) Execute(ctx *pongo2.ExecutionContext, w pongo2.TemplateWriter) *pongo2.Error {
	var srcs []string
	for _, name := range []string{"ML-TTKarthika", "ML-TT Karthika", "MLTTKarthika", "ML-TTKarthika Normal"} {
		srcs = append(srcs, fmt.Sprintf("local(%q)", name))
	}
	srcs = append(srcs, fmt.Sprintf("url(%q) format(\"truetype\")", current.StaticURL("fonts/ML-TTKarthika.ttf")))
	// There is only the normal weight, and it stands for every weight.
	w.WriteString(fmt.Sprintf(`<style>@font-face{font-family:"ML-TT Karthika";font-weight:100 900;font-display:swap;src:%s;}</style>`,
		strings.Join(srcs, ", ")))
	return nil
}

func tagFontFaces(doc *pongo2.Parser, start *pongo2.Token, arguments *pongo2.Parser) (pongo2.INodeTag, *pongo2.Error) {
	return nodeFontFaces{}, nil
}

// --- filters --------------------------------------------------------------------

func toTime(v any) (time.Time, bool) {
	switch t := v.(type) {
	case time.Time:
		return t, true
	case *time.Time:
		if t != nil {
			return *t, true
		}
	}
	return time.Time{}, false
}

// filterDate is Django's date filter ("d M Y, g:i A" style formats), showing
// moments in the office's time zone.
func filterDate(in *pongo2.Value, param *pongo2.Value) (*pongo2.Value, *pongo2.Error) {
	t, ok := toTime(in.Interface())
	if !ok || t.IsZero() {
		return pongo2.AsValue(""), nil
	}
	if loc := current.Cfg.TimeZone; loc != nil {
		// DATE columns arrive as midnight UTC and must keep their day.
		if t.Location() == time.UTC && t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 {
			t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
		} else {
			t = t.In(loc)
		}
	}
	format := param.String()
	if format == "" {
		format = "N j, Y"
	}
	return pongo2.AsValue(djangoDate(t, format)), nil
}

func filterFloatformat(in *pongo2.Value, param *pongo2.Value) (*pongo2.Value, *pongo2.Error) {
	arg := ""
	if !param.IsNil() {
		arg = param.String()
	}
	return pongo2.AsValue(djangoFloatformat(in.Interface(), arg)), nil
}

func filterNumber(in *pongo2.Value, param *pongo2.Value) (*pongo2.Value, *pongo2.Error) {
	return pongo2.AsValue(djangoFloatformat(in.Interface(), "0g")), nil
}

func filterToJSON(in *pongo2.Value, param *pongo2.Value) (*pongo2.Value, *pongo2.Error) {
	b, err := json.Marshal(in.Interface())
	if err != nil {
		return nil, &pongo2.Error{Sender: "tojson", OrigError: err}
	}
	return pongo2.AsSafeValue(jsonForScript(string(b))), nil
}

// jsonForScript escapes the characters that could end a <script> element.
func jsonForScript(s string) string {
	r := strings.NewReplacer("<", `<`, ">", `>`, "&", `&`, " ", ` `, " ", ` `)
	return r.Replace(s)
}

func filterJSONScript(in *pongo2.Value, param *pongo2.Value) (*pongo2.Value, *pongo2.Error) {
	b, err := json.Marshal(in.Interface())
	if err != nil {
		return nil, &pongo2.Error{Sender: "json_script", OrigError: err}
	}
	out := fmt.Sprintf(`<script id="%s" type="application/json">%s</script>`, html.EscapeString(param.String()), jsonForScript(string(b)))
	return pongo2.AsSafeValue(out), nil
}

// --- rendering ------------------------------------------------------------------

// Data is the context passed to a page template.
type Data = pongo2.Context

// TemplateUser is what templates see as `user`.
func (c *Ctx) templateUser() map[string]any {
	if c.User == nil {
		return map[string]any{"is_authenticated": false}
	}
	u := c.User
	return map[string]any{
		"is_authenticated": true, "id": u.ID, "username": u.Username, "first_name": u.FirstName, "last_name": u.LastName,
		"email": u.Email, "is_staff": u.IsStaff, "is_superuser": u.IsSuperuser, "get_full_name": u.FullName(),
	}
}

// Render executes a template with the common context and writes the page.
func (c *Ctx) Render(name string, data Data) {
	c.RenderStatus(http.StatusOK, name, data)
}

// RenderStatus renders with an explicit status code.
func (c *Ctx) RenderStatus(status int, name string, data Data) {
	ctx := pongo2.Context{
		"user":         c.templateUser(),
		"csrf_token":   c.csrfToken,
		"view_name":    c.ViewName,
		"messages":     c.Session.popMessages(),
		"is_org_admin": c.IsOrgAdmin(),
		"organization": nil,
		"debug":        c.App.Cfg.Debug,
	}
	if org := c.Org(); org != nil {
		ctx["organization"] = map[string]any{"id": org.ID, "name": org.Name}
	}
	for k, v := range data {
		ctx[k] = v
	}
	tpl, err := c.App.tpl.set.FromFile(name)
	if err != nil {
		c.ServerError(fmt.Errorf("template %s: %w", name, err))
		return
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteWriter(ctx, &buf); err != nil {
		c.ServerError(fmt.Errorf("rendering %s: %w", name, err))
		return
	}
	c.SaveSession()
	c.W.Header().Set("Content-Type", "text/html; charset=utf-8")
	c.W.WriteHeader(status)
	_, _ = c.W.Write(buf.Bytes())
}

// RenderString renders a template to a string (emails).
func (a *App) RenderString(name string, data Data) (string, error) {
	tpl, err := a.tpl.set.FromFile(name)
	if err != nil {
		return "", err
	}
	return tpl.Execute(data)
}

// ObjectMap converts a struct to a map keyed by its `db` column names, the
// snake_case names the Django templates use. Extra entries override.
func ObjectMap(v any, extra map[string]any) map[string]any {
	out := map[string]any{}
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("db")
		if tag == "" || tag == "-" {
			continue
		}
		f := rv.Field(i)
		if f.Kind() == reflect.Ptr {
			if f.IsNil() {
				out[tag] = nil
				continue
			}
		}
		out[tag] = f.Interface()
	}
	for k, val := range extra {
		out[k] = val
	}
	return out
}

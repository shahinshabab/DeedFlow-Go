package web

import (
	"bytes"
	"fmt"
	"html"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/flosch/pongo2/v6"
)

// The template tags and filters Django's admin templates use that pongo2 does
// not have: {% translate %}, {% blocktranslate %} (the site is English only),
// the admin's inclusion tags, and a few filters.

func init() {
	must(pongo2.RegisterTag("translate", tagTranslate))
	must(pongo2.RegisterTag("trans", tagTranslate))
	must(pongo2.RegisterTag("blocktranslate", tagBlockTranslate))
	must(pongo2.RegisterTag("blocktrans", tagBlockTranslate))
	must(pongo2.RegisterFilter("admin_urlname", filterAdminURLName))
	must(pongo2.RegisterFilter("admin_urlquote", filterAdminURLQuote))
	must(pongo2.RegisterFilter("unlocalize", filterUnlocalize))
	must(pongo2.RegisterFilter("unordered_list", filterUnorderedList))
	must(pongo2.RegisterFilter("get", filterGet))
	must(pongo2.ReplaceFilter("urlencode", filterURLEncode))
	must(pongo2.ReplaceFilter("capfirst", filterCapfirst))
}

// filterURLEncode is Django's urlencode (urllib.parse.quote with "/" safe).
func filterURLEncode(in *pongo2.Value, param *pongo2.Value) (*pongo2.Value, *pongo2.Error) {
	safeChars := "/"
	if !param.IsNil() {
		safeChars = param.String()
	}
	var b strings.Builder
	for _, c := range []byte(in.String()) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '.', c == '-', c == '~',
			strings.IndexByte(safeChars, c) >= 0:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return pongo2.AsValue(b.String()), nil
}

// --- {% translate "text" [noop] [context "x"] [as var] %} ---------------------------------

type nodeTranslate struct {
	expr    pongo2.IEvaluator
	literal bool
	asVar   string
}

func (n *nodeTranslate) Execute(ctx *pongo2.ExecutionContext, w pongo2.TemplateWriter) *pongo2.Error {
	v, err := n.expr.Evaluate(ctx)
	if err != nil {
		return err
	}
	s := v.String()
	if n.asVar != "" {
		ctx.Private[n.asVar] = pongo2.AsSafeValue(s)
		return nil
	}
	if !n.literal {
		s = html.EscapeString(s)
	}
	w.WriteString(s)
	return nil
}

func tagTranslate(doc *pongo2.Parser, start *pongo2.Token, arguments *pongo2.Parser) (pongo2.INodeTag, *pongo2.Error) {
	n := &nodeTranslate{}
	n.literal = arguments.PeekType(pongo2.TokenString) != nil
	expr, err := arguments.ParseExpression()
	if err != nil {
		return nil, err
	}
	n.expr = expr
	for arguments.Remaining() > 0 {
		switch {
		case arguments.Match(pongo2.TokenKeyword, "as") != nil:
			id := arguments.MatchType(pongo2.TokenIdentifier)
			if id == nil {
				return nil, arguments.Error("Expected an identifier after 'as'.", nil)
			}
			n.asVar = id.Val
		case arguments.Peek(pongo2.TokenIdentifier, "noop") != nil:
			arguments.Consume()
		case arguments.Peek(pongo2.TokenIdentifier, "context") != nil:
			arguments.Consume()
			if _, err := arguments.ParseExpression(); err != nil {
				return nil, err
			}
		default:
			return nil, arguments.Error("Unexpected argument to 'translate'.", nil)
		}
	}
	return n, nil
}

// --- {% blocktranslate [with a=b] [count n=expr] [trimmed] %}...{% plural %}...{% endblocktranslate %} ---

type nodeBlockTranslate struct {
	vars     map[string]pongo2.IEvaluator
	order    []string
	count    string
	countExp pongo2.IEvaluator
	trimmed  bool
	single   *pongo2.NodeWrapper
	plural   *pongo2.NodeWrapper
}

func (n *nodeBlockTranslate) Execute(ctx *pongo2.ExecutionContext, w pongo2.TemplateWriter) *pongo2.Error {
	child := pongo2.NewChildExecutionContext(ctx)
	for _, k := range n.order {
		v, err := n.vars[k].Evaluate(ctx)
		if err != nil {
			return err
		}
		child.Private[k] = v
	}
	body := n.single
	if n.countExp != nil {
		v, err := n.countExp.Evaluate(ctx)
		if err != nil {
			return err
		}
		child.Private[n.count] = v
		if n.plural != nil && !(v.IsInteger() && v.Integer() == 1) {
			body = n.plural
		}
	}
	var buf bytes.Buffer
	if err := body.Execute(child, &buf); err != nil {
		return err
	}
	out := buf.String()
	if n.trimmed {
		var lines []string
		for _, l := range strings.Split(out, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				lines = append(lines, l)
			}
		}
		out = strings.Join(lines, " ")
	}
	w.WriteString(out)
	return nil
}

func tagBlockTranslate(doc *pongo2.Parser, start *pongo2.Token, arguments *pongo2.Parser) (pongo2.INodeTag, *pongo2.Error) {
	n := &nodeBlockTranslate{vars: map[string]pongo2.IEvaluator{}}
	for arguments.Remaining() > 0 {
		switch {
		case arguments.Peek(pongo2.TokenIdentifier, "trimmed") != nil:
			arguments.Consume()
			n.trimmed = true
		case arguments.Peek(pongo2.TokenIdentifier, "count") != nil:
			arguments.Consume()
			id := arguments.MatchType(pongo2.TokenIdentifier)
			if id == nil || arguments.Match(pongo2.TokenSymbol, "=") == nil {
				return nil, arguments.Error("Expected 'count name=expression'.", nil)
			}
			expr, err := arguments.ParseExpression()
			if err != nil {
				return nil, err
			}
			n.count, n.countExp = id.Val, expr
		case arguments.Peek(pongo2.TokenIdentifier, "with") != nil:
			arguments.Consume()
			for arguments.Remaining() > 0 {
				if arguments.Peek(pongo2.TokenIdentifier, "count") != nil || arguments.Peek(pongo2.TokenIdentifier, "trimmed") != nil {
					break
				}
				if arguments.PeekTypeN(0, pongo2.TokenIdentifier) != nil && arguments.PeekN(1, pongo2.TokenSymbol, "=") != nil {
					id := arguments.MatchType(pongo2.TokenIdentifier)
					arguments.Match(pongo2.TokenSymbol, "=")
					expr, err := arguments.ParseExpression()
					if err != nil {
						return nil, err
					}
					n.vars[id.Val] = expr
					n.order = append(n.order, id.Val)
					continue
				}
				expr, err := arguments.ParseExpression()
				if err != nil {
					return nil, err
				}
				if arguments.Match(pongo2.TokenKeyword, "as") == nil {
					return nil, arguments.Error("Expected 'as' in blocktranslate 'with'.", nil)
				}
				id := arguments.MatchType(pongo2.TokenIdentifier)
				if id == nil {
					return nil, arguments.Error("Expected an identifier after 'as'.", nil)
				}
				n.vars[id.Val] = expr
				n.order = append(n.order, id.Val)
			}
		default:
			return nil, arguments.Error("Unexpected argument to 'blocktranslate'.", nil)
		}
	}
	single, endargs, err := doc.WrapUntilTag("plural", "endblocktranslate", "endblocktrans")
	if err != nil {
		return nil, err
	}
	n.single = single
	if single.Endtag == "plural" {
		plural, _, err := doc.WrapUntilTag("endblocktranslate", "endblocktrans")
		if err != nil {
			return nil, err
		}
		n.plural = plural
	}
	_ = endargs
	return n, nil
}

// --- filters ---------------------------------------------------------------------------------------

// filterAdminURLName: opts|admin_urlname:'changelist' -> "admin:app_model_changelist".
func filterAdminURLName(in *pongo2.Value, param *pongo2.Value) (*pongo2.Value, *pongo2.Error) {
	opts, _ := in.Interface().(map[string]any)
	return pongo2.AsValue(fmt.Sprintf("admin:%v_%v_%s", opts["app_label"], opts["model_name"], param.String())), nil
}

// adminQuote is django.contrib.admin.utils.quote.
func adminQuote(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(":/_#?;@&=+$,\"[]<>%\n\\", r) {
			fmt.Fprintf(&b, "_%02X", r)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func adminUnquote(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '_' && i+3 <= len(s) {
			var v int
			if _, err := fmt.Sscanf(s[i+1:i+3], "%02X", &v); err == nil && utf8.ValidRune(rune(v)) {
				b.WriteRune(rune(v))
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func filterAdminURLQuote(in *pongo2.Value, param *pongo2.Value) (*pongo2.Value, *pongo2.Error) {
	return pongo2.AsValue(adminQuote(in.String())), nil
}

func filterUnlocalize(in *pongo2.Value, param *pongo2.Value) (*pongo2.Value, *pongo2.Error) {
	return pongo2.AsValue(in.String()), nil
}

// filterGet reads a key of a map (for keys that are not identifiers).
func filterGet(in *pongo2.Value, param *pongo2.Value) (*pongo2.Value, *pongo2.Error) {
	if m, ok := in.Interface().(map[string]any); ok {
		return pongo2.AsValue(m[param.String()]), nil
	}
	return pongo2.AsValue(nil), nil
}

// filterUnorderedList is Django's unordered_list: nested lists of (already
// escaped) item strings become nested <ul>s.
func filterUnorderedList(in *pongo2.Value, param *pongo2.Value) (*pongo2.Value, *pongo2.Error) {
	var render func(list []any, level int) string
	render = func(list []any, level int) string {
		var out []string
		indent := strings.Repeat("\t", level)
		for i := 0; i < len(list); i++ {
			item := list[i]
			var children []any
			if i+1 < len(list) {
				if sub, ok := list[i+1].([]any); ok {
					children = sub
					i++
				}
			}
			s := fmt.Sprint(item)
			if v, ok := item.(*pongo2.Value); ok {
				s = v.String()
			}
			if children != nil {
				out = append(out, fmt.Sprintf("%s<li>%s\n%s<ul>\n%s\n%s</ul>\n%s</li>", indent, s, indent,
					render(children, level+1), indent, indent))
			} else {
				out = append(out, fmt.Sprintf("%s<li>%s</li>", indent, s))
			}
		}
		return strings.Join(out, "\n")
	}
	list, ok := in.Interface().([]any)
	if !ok {
		return pongo2.AsSafeValue(""), nil
	}
	return pongo2.AsSafeValue(render(list, 1)), nil
}

// urlQuery builds "?a=b&c=d" in the order given.
func urlQuery(pairs [][2]string) string {
	if len(pairs) == 0 {
		return ""
	}
	var b strings.Builder
	for i, p := range pairs {
		if i == 0 {
			b.WriteByte('?')
		} else {
			b.WriteByte('&')
		}
		b.WriteString(url.QueryEscape(p[0]) + "=" + url.QueryEscape(p[1]))
	}
	return b.String()
}

// safeHTML marks HTML text as already escaped; filters that keep a value safe look for it.
type safeHTML string

func (s safeHTML) String() string { return string(s) }

// filterCapfirst is Django's capfirst: it keeps the value safe if it was.
func filterCapfirst(in *pongo2.Value, param *pongo2.Value) (*pongo2.Value, *pongo2.Error) {
	iface := in.Interface()
	if pv, ok := iface.(*pongo2.Value); ok {
		iface = pv.Interface()
	}
	if h, ok := iface.(safeHTML); ok {
		return pongo2.AsSafeValue(safeHTML(capfirst(string(h)))), nil
	}
	return pongo2.AsValue(capfirst(in.String())), nil
}

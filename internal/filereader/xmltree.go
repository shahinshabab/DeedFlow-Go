package filereader

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strings"
)

// xnode is an element of a parsed XML document, like the ones
// xml.etree.ElementTree builds: Tag is "{namespace}name" for namespaced
// elements, Text is the text before the first child.
type xnode struct {
	Tag      string
	Attr     map[string]string
	Text     string
	Children []*xnode
}

func (n *xnode) get(name string) (string, bool) {
	v, ok := n.Attr[name]
	return v, ok
}

func (n *xnode) attr(name string) string { return n.Attr[name] }

// find is Element.find for a plain tag or a "a/b/c" path of direct children.
func (n *xnode) find(path string) *xnode {
	cur := n
	for _, part := range strings.Split(path, "/") {
		var next *xnode
		for _, c := range cur.Children {
			if c.Tag == part {
				next = c
				break
			}
		}
		if next == nil {
			return nil
		}
		cur = next
	}
	return cur
}

// findall is Element.findall for a plain tag.
func (n *xnode) findall(tag string) []*xnode {
	var out []*xnode
	for _, c := range n.Children {
		if c.Tag == tag {
			out = append(out, c)
		}
	}
	return out
}

// iter is Element.iter(tag): the element itself and its descendants, in
// document order, whose tag matches.
func (n *xnode) iter(tag string) []*xnode {
	var out []*xnode
	var walk func(e *xnode)
	walk = func(e *xnode) {
		if e.Tag == tag {
			out = append(out, e)
		}
		for _, c := range e.Children {
			walk(c)
		}
	}
	walk(n)
	return out
}

// localName is the tag without its namespace.
func localName(tag string) string {
	if i := strings.LastIndex(tag, "}"); i >= 0 {
		return tag[i+1:]
	}
	return tag
}

// parseXML parses a document; an error is what ElementTree calls ParseError.
func parseXML(data []byte) (*xnode, error) {
	if err := checkAttrSpacing(data); err != nil {
		return nil, err
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = true
	var root *xnode
	var stack []*xnode
	declared := map[string]bool{}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			// An element or attribute whose prefix was never declared is an
			// error to a namespace-aware parser.
			for _, a := range t.Attr {
				if a.Name.Space == "xmlns" || (a.Name.Space == "" && a.Name.Local == "xmlns") {
					declared[a.Value] = true
				}
			}
			if t.Name.Space != "" && !declared[t.Name.Space] {
				return nil, errNotWellFormed
			}
			for _, a := range t.Attr {
				if a.Name.Space != "" && a.Name.Space != "xmlns" && a.Name.Space != "xml" && !declared[a.Name.Space] {
					return nil, errNotWellFormed
				}
			}
			n := &xnode{Tag: qualified(t.Name), Attr: map[string]string{}}
			for _, a := range t.Attr {
				if a.Name.Space == "xmlns" || (a.Name.Space == "" && a.Name.Local == "xmlns") {
					continue
				}
				n.Attr[qualified(a.Name)] = a.Value
			}
			if len(stack) > 0 {
				parent := stack[len(stack)-1]
				parent.Children = append(parent.Children, n)
			} else if root == nil {
				root = n
			}
			stack = append(stack, n)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) > 0 {
				cur := stack[len(stack)-1]
				if len(cur.Children) == 0 {
					cur.Text += string(t)
				}
			}
		}
	}
	if root == nil {
		return nil, io.ErrUnexpectedEOF
	}
	return root, nil
}

func qualified(n xml.Name) string {
	if n.Space != "" {
		return "{" + n.Space + "}" + n.Local
	}
	return n.Local
}

var errNotWellFormed = errors.New("not well-formed (invalid token)")

// checkAttrSpacing rejects what Go's decoder lets through but a strict XML
// parser (expat, behind ElementTree) does not: attributes that follow each
// other with no white space between them.
func checkAttrSpacing(data []byte) error {
	n := len(data)
	for i := 0; i < n; i++ {
		if data[i] != '<' {
			continue
		}
		rest := data[i:]
		switch {
		case bytes.HasPrefix(rest, []byte("<!--")):
			end := bytes.Index(rest, []byte("-->"))
			if end < 0 {
				return nil
			}
			i += end + 2
			continue
		case bytes.HasPrefix(rest, []byte("<![CDATA[")):
			end := bytes.Index(rest, []byte("]]>"))
			if end < 0 {
				return nil
			}
			i += end + 2
			continue
		case bytes.HasPrefix(rest, []byte("<?")), bytes.HasPrefix(rest, []byte("<!")):
			end := bytes.IndexByte(rest, '>')
			if end < 0 {
				return nil
			}
			i += end
			continue
		}
		// inside a start or end tag
		j := i + 1
		for j < n && data[j] != '>' {
			if data[j] == '"' || data[j] == '\'' {
				q := data[j]
				k := bytes.IndexByte(data[j+1:], q)
				if k < 0 {
					return nil
				}
				j += k + 2
				if j < n && !(data[j] == ' ' || data[j] == '\t' || data[j] == '\n' || data[j] == '\r' || data[j] == '/' || data[j] == '>') {
					return errNotWellFormed
				}
				continue
			}
			j++
		}
		i = j
	}
	return nil
}

package check

import (
	"bytes"
	"encoding/xml"
	"io"
	"strings"
)

const (
	dcNS    = "http://purl.org/dc/elements/1.1/"
	xhtmlNS = "http://www.w3.org/1999/xhtml"
	opsNS   = "http://www.idpf.org/2007/ops"

	renditionNS = "http://www.idpf.org/2013/rendition"
)

// node is an element of a parsed XML document, with its position.
type node struct {
	name      xml.Name
	attrs     []xml.Attr
	kids      []*node
	chars     strings.Builder
	line, col int
}

type parseError struct {
	msg       string
	line, col int
}

// parseXML reads a whole document strictly, as a conforming XML parser must.
func parseXML(data []byte) (*node, *parseError) {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = true
	doc := &node{}
	stack := []*node{doc}
	for {
		line, col := d.InputPos()
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			l, c := d.InputPos()
			msg := err.Error()
			if se, ok := err.(*xml.SyntaxError); ok {
				msg, l = se.Msg, se.Line
			}
			return nil, &parseError{msg: msg, line: l, col: c}
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &node{name: t.Name, attrs: t.Attr, line: line, col: col}
			top := stack[len(stack)-1]
			top.kids = append(top.kids, n)
			stack = append(stack, n)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			stack[len(stack)-1].chars.Write(t)
		}
	}
	if len(doc.kids) == 0 {
		return nil, &parseError{msg: "Premature end of file.", line: 1, col: 1}
	}
	return doc, nil
}

func (n *node) root() *node { return n.kids[0] }

func (n *node) attr(local string) string {
	for _, a := range n.attrs {
		if a.Name.Local == local && (a.Name.Space == "" || a.Name.Space == n.name.Space) {
			return a.Value
		}
	}
	return ""
}

func (n *node) attrNS(space, local string) (string, bool) {
	for _, a := range n.attrs {
		if a.Name.Local == local && a.Name.Space == space {
			return a.Value, true
		}
	}
	return "", false
}

func (n *node) child(local string) *node {
	for _, k := range n.kids {
		if k.name.Local == local {
			return k
		}
	}
	return nil
}

func (n *node) children(local string) []*node {
	var out []*node
	for _, k := range n.kids {
		if k.name.Local == local {
			out = append(out, k)
		}
	}
	return out
}

func (n *node) childrenNS(space, local string) []*node {
	var out []*node
	for _, k := range n.kids {
		if k.name.Local == local && k.name.Space == space {
			out = append(out, k)
		}
	}
	return out
}

// findAll returns every descendant with the local name, in document order.
func (n *node) findAll(local string) []*node {
	var out []*node
	var walk func(*node)
	walk = func(m *node) {
		for _, k := range m.kids {
			if k.name.Local == local {
				out = append(out, k)
			}
			walk(k)
		}
	}
	walk(n)
	return out
}

// walk visits n and every descendant.
func (n *node) walk(visit func(*node)) {
	visit(n)
	for _, k := range n.kids {
		k.walk(visit)
	}
}

// text is the element's own character data plus that of its descendants.
func (n *node) text() string {
	var b strings.Builder
	n.walk(func(m *node) { b.WriteString(m.chars.String()) })
	return b.String()
}

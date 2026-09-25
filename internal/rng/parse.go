package rng

import (
	"fmt"
	"strings"
)

const (
	xmlNS  = "http://www.w3.org/XML/1998/namespace"
	xsdLib = "http://www.w3.org/2001/XMLSchema-datatypes"
)

// ----- syntax tree -----

type ncKind int

const (
	ncName ncKind = iota
	ncNsName
	ncAnyName
	ncChoice
)

// nameClass is a set of expanded names.
type nameClass struct {
	kind      ncKind
	ns, local string
	except    *nameClass
	a, b      *nameClass
}

func (n *nameClass) contains(ns, local string) bool {
	switch n.kind {
	case ncName:
		return n.ns == ns && n.local == local
	case ncNsName:
		return n.ns == ns && (n.except == nil || !n.except.contains(ns, local))
	case ncAnyName:
		return n.except == nil || !n.except.contains(ns, local)
	default:
		return n.a.contains(ns, local) || n.b.contains(ns, local)
	}
}

type param struct{ name, value string }

// node is a pattern in the syntax tree, with names already resolved.
type node struct {
	op     string // element attribute list mixed ref empty text notAllowed value data choice group interleave oneOrMore zeroOrMore optional
	kids   []*node
	nc     *nameClass
	ref    string
	lib    string // datatype library URI
	dt     string // datatype name
	value  string
	params []param
	except *node
	pos    string // file:line, for errors
}

type component struct {
	kind    string // define start include div
	name    string
	combine string // "", "choice", "interleave"
	pattern *node
	href    string
	body    []component
	inherit string // include: the includer's default namespace
	pos     string
}

// ----- parser -----

type parser struct {
	lx        *lexer
	tok       token
	peeked    *token
	ns        map[string]string // prefix -> URI
	defaultNS string
	dts       map[string]string // prefix -> datatype library
}

var keywords = map[string]bool{
	"attribute": true, "default": true, "datatypes": true, "div": true, "element": true,
	"empty": true, "external": true, "grammar": true, "include": true, "inherit": true,
	"list": true, "mixed": true, "namespace": true, "notAllowed": true, "parent": true,
	"start": true, "string": true, "text": true, "token": true,
}

func (p *parser) advance() error {
	if p.peeked != nil {
		p.tok, p.peeked = *p.peeked, nil
		return nil
	}
	t, err := p.lx.next()
	p.tok = t
	return err
}

func (p *parser) peek() (token, error) {
	if p.peeked == nil {
		t, err := p.lx.next()
		if err != nil {
			return token{}, err
		}
		p.peeked = &t
	}
	return *p.peeked, nil
}

func (p *parser) is(kind tokKind, text string) bool {
	return p.tok.kind == kind && p.tok.text == text
}

func (p *parser) isKeyword(k string) bool {
	return p.tok.kind == tIdent && !p.tok.escaped && p.tok.text == k
}

func (p *parser) expect(text string) error {
	if !p.is(tOp, text) {
		return p.errorf("expected %q, found %q", text, p.tok.text)
	}
	return p.advance()
}

func (p *parser) errorf(format string, args ...any) error {
	return fmt.Errorf("%s:%d: %s", p.lx.file, p.tok.line, fmt.Sprintf(format, args...))
}

func (p *parser) pos() string { return fmt.Sprintf("%s:%d", p.lx.file, p.tok.line) }

// skipAnnotations passes over [ ... ] annotations, which carry nothing that
// affects validation.
func (p *parser) skipAnnotations() error {
	for p.is(tOp, "[") {
		depth := 0
		for {
			switch {
			case p.is(tOp, "["):
				depth++
			case p.is(tOp, "]"):
				depth--
			case p.tok.kind == tEOF:
				return p.errorf("unterminated annotation")
			}
			if err := p.advance(); err != nil {
				return err
			}
			if depth == 0 {
				break
			}
		}
	}
	return nil
}

func (p *parser) literal() (string, error) {
	if p.tok.kind != tLiteral {
		return "", p.errorf("expected a literal, found %q", p.tok.text)
	}
	s := p.tok.text
	if err := p.advance(); err != nil {
		return "", err
	}
	for p.is(tOp, "~") { // concatenation
		if err := p.advance(); err != nil {
			return "", err
		}
		if p.tok.kind != tLiteral {
			return "", p.errorf("expected a literal after ~")
		}
		s += p.tok.text
		if err := p.advance(); err != nil {
			return "", err
		}
	}
	return s, nil
}

// parseFile parses one schema file into its grammar components. inheritNS is
// the default namespace of the including file, used when this file declares
// none.
func parseFile(file, src, inheritNS string) ([]component, error) {
	lx, err := newLexer(file, src)
	if err != nil {
		return nil, err
	}
	p := &parser{lx: lx, ns: map[string]string{"xml": xmlNS}, dts: map[string]string{"xsd": xsdLib}, defaultNS: inheritNS}
	if err := p.advance(); err != nil {
		return nil, err
	}
	if err := p.decls(); err != nil {
		return nil, err
	}
	// A file is a grammar (definitions) or a single pattern, which acts as
	// its start.
	if p.startsGrammar() {
		return p.grammarContent(tEOF, "")
	}
	pat, err := p.pattern()
	if err != nil {
		return nil, err
	}
	return []component{{kind: "start", pattern: pat}}, nil
}

func (p *parser) startsGrammar() bool {
	if p.tok.kind == tEOF || p.is(tOp, "[") || p.isKeyword("start") || p.isKeyword("include") || p.isKeyword("div") {
		return true
	}
	if p.tok.kind == tIdent && (!keywords[p.tok.text] || p.tok.escaped) {
		t, err := p.peek()
		return err == nil && t.kind == tOp && (t.text == "=" || t.text == "|=" || t.text == "&=")
	}
	return false
}

func (p *parser) decls() error {
	for {
		if err := p.skipAnnotations(); err != nil {
			return err
		}
		switch {
		case p.isKeyword("namespace"):
			if err := p.advance(); err != nil {
				return err
			}
			prefix := p.tok.text
			if err := p.advance(); err != nil {
				return err
			}
			if err := p.expect("="); err != nil {
				return err
			}
			uri, err := p.nsURI()
			if err != nil {
				return err
			}
			p.ns[prefix] = uri
		case p.isKeyword("default"):
			if err := p.advance(); err != nil {
				return err
			}
			if !p.isKeyword("namespace") {
				return p.errorf("expected namespace after default")
			}
			if err := p.advance(); err != nil {
				return err
			}
			prefix := ""
			if p.tok.kind == tIdent {
				prefix = p.tok.text
				if err := p.advance(); err != nil {
					return err
				}
			}
			if err := p.expect("="); err != nil {
				return err
			}
			uri, err := p.nsURI()
			if err != nil {
				return err
			}
			p.defaultNS = uri
			if prefix != "" {
				p.ns[prefix] = uri
			}
		case p.isKeyword("datatypes"):
			if err := p.advance(); err != nil {
				return err
			}
			prefix := p.tok.text
			if err := p.advance(); err != nil {
				return err
			}
			if err := p.expect("="); err != nil {
				return err
			}
			uri, err := p.literal()
			if err != nil {
				return err
			}
			p.dts[prefix] = uri
		default:
			return nil
		}
	}
}

func (p *parser) nsURI() (string, error) {
	if p.isKeyword("inherit") {
		err := p.advance()
		return p.defaultNS, err
	}
	return p.literal()
}

// grammarContent parses definitions until the closing token.
func (p *parser) grammarContent(end tokKind, endOp string) ([]component, error) {
	var comps []component
	for {
		if err := p.skipAnnotations(); err != nil {
			return nil, err
		}
		if (end == tEOF && p.tok.kind == tEOF) || (end == tOp && p.is(tOp, endOp)) {
			return comps, nil
		}
		pos := p.pos()
		switch {
		case p.isKeyword("div"):
			if err := p.advance(); err != nil {
				return nil, err
			}
			if err := p.expect("{"); err != nil {
				return nil, err
			}
			body, err := p.grammarContent(tOp, "}")
			if err != nil {
				return nil, err
			}
			if err := p.expect("}"); err != nil {
				return nil, err
			}
			comps = append(comps, component{kind: "div", body: body, pos: pos})
		case p.isKeyword("include"):
			if err := p.advance(); err != nil {
				return nil, err
			}
			href, err := p.literal()
			if err != nil {
				return nil, err
			}
			if p.isKeyword("inherit") {
				if err := p.advance(); err != nil {
					return nil, err
				}
				if err := p.expect("="); err != nil {
					return nil, err
				}
				if err := p.advance(); err != nil { // the prefix
					return nil, err
				}
			}
			c := component{kind: "include", href: href, pos: pos, inherit: p.defaultNS}
			if p.is(tOp, "{") {
				if err := p.advance(); err != nil {
					return nil, err
				}
				body, err := p.grammarContent(tOp, "}")
				if err != nil {
					return nil, err
				}
				if err := p.expect("}"); err != nil {
					return nil, err
				}
				c.body = body
			}
			comps = append(comps, c)
		case p.isKeyword("start") || p.tok.kind == tIdent:
			kind, name := "define", p.tok.text
			if p.isKeyword("start") {
				kind, name = "start", ""
			}
			if err := p.advance(); err != nil {
				return nil, err
			}
			combine := ""
			switch {
			case p.is(tOp, "="):
			case p.is(tOp, "|="):
				combine = "choice"
			case p.is(tOp, "&="):
				combine = "interleave"
			default:
				return nil, p.errorf("expected =, |= or &= after %q", name)
			}
			if err := p.advance(); err != nil {
				return nil, err
			}
			pat, err := p.pattern()
			if err != nil {
				return nil, err
			}
			comps = append(comps, component{kind: kind, name: name, combine: combine, pattern: pat, pos: pos})
		default:
			return nil, p.errorf("unexpected %q in grammar", p.tok.text)
		}
	}
}

// pattern parses a sequence of particles joined by one kind of operator.
func (p *parser) pattern() (*node, error) {
	first, err := p.particle()
	if err != nil {
		return nil, err
	}
	op := ""
	kids := []*node{first}
	for p.is(tOp, "|") || p.is(tOp, ",") || p.is(tOp, "&") {
		this := map[string]string{"|": "choice", ",": "group", "&": "interleave"}[p.tok.text]
		if op != "" && op != this {
			return nil, p.errorf("mixed operators without parentheses")
		}
		op = this
		if err := p.advance(); err != nil {
			return nil, err
		}
		k, err := p.particle()
		if err != nil {
			return nil, err
		}
		kids = append(kids, k)
	}
	if op == "" {
		return first, nil
	}
	return &node{op: op, kids: kids, pos: first.pos}, nil
}

func (p *parser) particle() (*node, error) {
	prim, err := p.primary()
	if err != nil {
		return nil, err
	}
	for p.is(tOp, "?") || p.is(tOp, "*") || p.is(tOp, "+") {
		op := map[string]string{"?": "optional", "*": "zeroOrMore", "+": "oneOrMore"}[p.tok.text]
		if err := p.advance(); err != nil {
			return nil, err
		}
		prim = &node{op: op, kids: []*node{prim}, pos: prim.pos}
	}
	return prim, nil
}

func (p *parser) braced() (*node, error) {
	if err := p.expect("{"); err != nil {
		return nil, err
	}
	n, err := p.pattern()
	if err != nil {
		return nil, err
	}
	return n, p.expect("}")
}

func (p *parser) primary() (*node, error) {
	if err := p.skipAnnotations(); err != nil {
		return nil, err
	}
	pos := p.pos()
	switch {
	case p.isKeyword("element") || p.isKeyword("attribute"):
		isElem := p.isKeyword("element")
		if err := p.advance(); err != nil {
			return nil, err
		}
		nc, err := p.nameClass(isElem)
		if err != nil {
			return nil, err
		}
		body, err := p.braced()
		if err != nil {
			return nil, err
		}
		op := "attribute"
		if isElem {
			op = "element"
		}
		return &node{op: op, nc: nc, kids: []*node{body}, pos: pos}, nil
	case p.isKeyword("list") || p.isKeyword("mixed"):
		op := p.tok.text
		if err := p.advance(); err != nil {
			return nil, err
		}
		body, err := p.braced()
		if err != nil {
			return nil, err
		}
		return &node{op: op, kids: []*node{body}, pos: pos}, nil
	case p.isKeyword("empty") || p.isKeyword("text") || p.isKeyword("notAllowed"):
		op := p.tok.text
		return &node{op: op, pos: pos}, p.advance()
	case p.isKeyword("parent") || p.isKeyword("grammar") || p.isKeyword("external"):
		return nil, p.errorf("%q is not supported", p.tok.text)
	case p.is(tOp, "("):
		if err := p.advance(); err != nil {
			return nil, err
		}
		n, err := p.pattern()
		if err != nil {
			return nil, err
		}
		return n, p.expect(")")
	case p.tok.kind == tLiteral:
		v, err := p.literal()
		return &node{op: "value", lib: "", dt: "token", value: v, pos: pos}, err
	case p.isKeyword("string") || p.isKeyword("token"):
		return p.datatype("", p.tok.text, pos)
	case p.tok.kind == tCName:
		prefix, local, _ := strings.Cut(p.tok.text, ":")
		lib, ok := p.dts[prefix]
		if !ok {
			return nil, p.errorf("undeclared datatype prefix %q", prefix)
		}
		return p.datatype(lib, local, pos)
	case p.tok.kind == tIdent:
		name := p.tok.text
		return &node{op: "ref", ref: name, pos: pos}, p.advance()
	}
	return nil, p.errorf("unexpected %q in pattern", p.tok.text)
}

// datatype parses a datatype name with its parameters and except clause, or
// a typed value.
func (p *parser) datatype(lib, name, pos string) (*node, error) {
	if err := p.advance(); err != nil {
		return nil, err
	}
	if p.tok.kind == tLiteral {
		v, err := p.literal()
		return &node{op: "value", lib: lib, dt: name, value: v, pos: pos}, err
	}
	n := &node{op: "data", lib: lib, dt: name, pos: pos}
	if p.is(tOp, "{") {
		if err := p.advance(); err != nil {
			return nil, err
		}
		for !p.is(tOp, "}") {
			if p.tok.kind != tIdent && p.tok.kind != tCName {
				return nil, p.errorf("expected a parameter name")
			}
			pname := p.tok.text
			if err := p.advance(); err != nil {
				return nil, err
			}
			if err := p.expect("="); err != nil {
				return nil, err
			}
			v, err := p.literal()
			if err != nil {
				return nil, err
			}
			n.params = append(n.params, param{pname, v})
		}
		if err := p.advance(); err != nil {
			return nil, err
		}
	}
	if p.is(tOp, "-") {
		if err := p.advance(); err != nil {
			return nil, err
		}
		ex, err := p.primary()
		if err != nil {
			return nil, err
		}
		n.except = ex
	}
	return n, nil
}

// nameClass parses a name class. Unprefixed element names are in the
// default namespace; unprefixed attribute names are in no namespace.
func (p *parser) nameClass(isElem bool) (*nameClass, error) {
	first, err := p.ncPrimary(isElem)
	if err != nil {
		return nil, err
	}
	for p.is(tOp, "|") {
		if err := p.advance(); err != nil {
			return nil, err
		}
		next, err := p.ncPrimary(isElem)
		if err != nil {
			return nil, err
		}
		first = &nameClass{kind: ncChoice, a: first, b: next}
	}
	return first, nil
}

func (p *parser) ncPrimary(isElem bool) (*nameClass, error) {
	if err := p.skipAnnotations(); err != nil {
		return nil, err
	}
	var nc *nameClass
	switch {
	case p.is(tOp, "("):
		if err := p.advance(); err != nil {
			return nil, err
		}
		n, err := p.nameClass(isElem)
		if err != nil {
			return nil, err
		}
		return n, p.expect(")")
	case p.is(tOp, "*"):
		nc = &nameClass{kind: ncAnyName}
	case p.tok.kind == tNsName:
		uri, ok := p.ns[p.tok.text]
		if !ok {
			return nil, p.errorf("undeclared prefix %q", p.tok.text)
		}
		nc = &nameClass{kind: ncNsName, ns: uri}
	case p.tok.kind == tCName:
		prefix, local, _ := strings.Cut(p.tok.text, ":")
		uri, ok := p.ns[prefix]
		if !ok {
			return nil, p.errorf("undeclared prefix %q", prefix)
		}
		nc = &nameClass{kind: ncName, ns: uri, local: local}
	case p.tok.kind == tIdent:
		ns := ""
		if isElem {
			ns = p.defaultNS
		}
		nc = &nameClass{kind: ncName, ns: ns, local: p.tok.text}
	default:
		return nil, p.errorf("expected a name class, found %q", p.tok.text)
	}
	if err := p.advance(); err != nil {
		return nil, err
	}
	if (nc.kind == ncAnyName || nc.kind == ncNsName) && p.is(tOp, "-") {
		if err := p.advance(); err != nil {
			return nil, err
		}
		ex, err := p.ncPrimary(isElem)
		if err != nil {
			return nil, err
		}
		nc.except = ex
	}
	return nc, nil
}

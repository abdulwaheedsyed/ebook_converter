package rng

import (
	"encoding/xml"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"
	"sync"
)

// Schema is a compiled RELAX NG schema. It is safe for concurrent use.
type Schema struct {
	mu    sync.Mutex
	start *node
	defs  map[string]*node

	b    *builder
	root *pat
	sto  map[stoKey]*pat
	stc  map[*pat]*pat
	et   map[*pat]*pat
}

type stoKey struct {
	p         *pat
	ns, local string
}

// Load reads a schema in the compact syntax from fsys, starting at file.
func Load(fsys fs.FS, file string) (*Schema, error) {
	comps, err := (&loader{fsys: fsys}).load(file, "", 0)
	if err != nil {
		return nil, err
	}
	start, defs, err := combine(comps)
	if err != nil {
		return nil, err
	}
	s := &Schema{start: start, defs: defs}
	if err := s.compile(); err != nil {
		return nil, err
	}
	return s, nil
}

// compile builds the pattern graph from the syntax tree. It is also how the
// memo tables are reset: derivatives accumulate as documents are validated,
// and a long-running process should not grow without bound.
func (s *Schema) compile() error {
	c := &compiler{defs: s.defs, b: newBuilder(), refs: map[string]*pat{}, busy: map[string]bool{},
		elems: map[*node]*pat{}, dts: map[string]*datatype{}}
	root, err := c.pattern(s.start)
	if err != nil {
		return err
	}
	for len(c.todo) > 0 {
		e := c.todo[len(c.todo)-1]
		c.todo = c.todo[:len(c.todo)-1]
		if e.p.content, err = c.pattern(e.n); err != nil {
			return err
		}
	}
	s.b, s.root = c.b, root
	s.sto, s.stc, s.et = map[stoKey]*pat{}, map[*pat]*pat{}, map[*pat]*pat{}
	return nil
}

type pending struct {
	p *pat
	n *node
}

type compiler struct {
	defs  map[string]*node
	b     *builder
	refs  map[string]*pat
	busy  map[string]bool
	elems map[*node]*pat
	dts   map[string]*datatype
	todo  []pending
}

func (c *compiler) datatype(lib, name string, params []param) *datatype {
	k := lib + "\x00" + name
	for _, p := range params {
		k += "\x00" + p.name + "=" + p.value
	}
	if d, ok := c.dts[k]; ok {
		return d
	}
	d := newDatatype(lib, name, params)
	c.dts[k] = d
	return d
}

func ncLabel(nc *nameClass) string {
	if nc.kind == ncName {
		return nc.local
	}
	return "*"
}

func (c *compiler) pattern(n *node) (*pat, error) {
	b := c.b
	fold := func(f func(x, y *pat) *pat) (*pat, error) {
		var acc *pat
		for _, k := range n.kids {
			p, err := c.pattern(k)
			if err != nil {
				return nil, err
			}
			if acc == nil {
				acc = p
			} else {
				acc = f(acc, p)
			}
		}
		return acc, nil
	}
	switch n.op {
	case "element":
		if e, ok := c.elems[n]; ok {
			return e, nil
		}
		e := b.element(n.nc, ncLabel(n.nc))
		c.elems[n] = e
		c.todo = append(c.todo, pending{e, n.kids[0]})
		return e, nil
	case "attribute":
		p, err := c.pattern(n.kids[0])
		if err != nil {
			return nil, err
		}
		return b.attribute(n.nc, p), nil
	case "list":
		p, err := c.pattern(n.kids[0])
		if err != nil {
			return nil, err
		}
		return b.list(p), nil
	case "mixed":
		p, err := c.pattern(n.kids[0])
		if err != nil {
			return nil, err
		}
		return b.interleave(p, b.text), nil
	case "ref":
		if p, ok := c.refs[n.ref]; ok {
			return p, nil
		}
		d, ok := c.defs[n.ref]
		if !ok {
			return nil, fmt.Errorf("%s: reference to undefined pattern %q", n.pos, n.ref)
		}
		if c.busy[n.ref] {
			return nil, fmt.Errorf("%s: %q refers to itself other than through an element", n.pos, n.ref)
		}
		c.busy[n.ref] = true
		p, err := c.pattern(d)
		delete(c.busy, n.ref)
		if err != nil {
			return nil, err
		}
		c.refs[n.ref] = p
		return p, nil
	case "empty":
		return b.empty, nil
	case "text":
		return b.text, nil
	case "notAllowed":
		return b.notAllowed, nil
	case "value":
		return b.valuePat(c.datatype(n.lib, n.dt, nil), n.value), nil
	case "data":
		dt := c.datatype(n.lib, n.dt, n.params)
		var ex *pat
		if n.except != nil {
			var err error
			if ex, err = c.pattern(n.except); err != nil {
				return nil, err
			}
		}
		return b.data(dt, ex), nil
	case "choice":
		return fold(b.choice)
	case "group":
		return fold(b.group)
	case "interleave":
		return fold(b.interleave)
	case "oneOrMore", "zeroOrMore", "optional":
		p, err := c.pattern(n.kids[0])
		if err != nil {
			return nil, err
		}
		switch n.op {
		case "oneOrMore":
			return b.oneOrMore(p), nil
		case "zeroOrMore":
			return b.choice(b.oneOrMore(p), b.empty), nil
		default:
			return b.choice(p, b.empty), nil
		}
	}
	return nil, fmt.Errorf("%s: unknown pattern %q", n.pos, n.op)
}

// ----- derivatives -----

type afterOp struct {
	kind  int // 0: interleave(x, o)  1: interleave(o, x)  2: group(x, o)  3: after(x, o)
	other *pat
}

func (s *Schema) apply(op afterOp, x *pat) *pat {
	switch op.kind {
	case 0:
		return s.b.interleave(x, op.other)
	case 1:
		return s.b.interleave(op.other, x)
	case 2:
		return s.b.group(x, op.other)
	}
	return s.b.after(x, op.other)
}

func (s *Schema) applyAfter(op afterOp, p *pat) *pat {
	switch p.k {
	case pAfter:
		return s.b.after(p.a, s.apply(op, p.b))
	case pChoice:
		return s.b.choice(s.applyAfter(op, p.a), s.applyAfter(op, p.b))
	}
	return s.b.notAllowed
}

func (s *Schema) startTagOpen(p *pat, ns, local string) *pat {
	k := stoKey{p, ns, local}
	if r, ok := s.sto[k]; ok {
		return r
	}
	b := s.b
	var r *pat
	switch p.k {
	case pChoice:
		r = b.choice(s.startTagOpen(p.a, ns, local), s.startTagOpen(p.b, ns, local))
	case pElement:
		if p.nc.contains(ns, local) {
			r = b.after(p.content, b.empty)
		} else {
			r = b.notAllowed
		}
	case pInterleave:
		r = b.choice(
			s.applyAfter(afterOp{0, p.b}, s.startTagOpen(p.a, ns, local)),
			s.applyAfter(afterOp{1, p.a}, s.startTagOpen(p.b, ns, local)))
	case pOneOrMore:
		r = s.applyAfter(afterOp{2, b.choice(p, b.empty)}, s.startTagOpen(p.a, ns, local))
	case pGroup:
		r = s.applyAfter(afterOp{2, p.b}, s.startTagOpen(p.a, ns, local))
		if p.a.nullable {
			r = b.choice(r, s.startTagOpen(p.b, ns, local))
		}
	case pAfter:
		r = s.applyAfter(afterOp{3, p.b}, s.startTagOpen(p.a, ns, local))
	default:
		r = b.notAllowed
	}
	s.sto[k] = r
	return r
}

func (s *Schema) attribute(p *pat, ns, local, value string) *pat {
	b := s.b
	switch p.k {
	case pAfter:
		return b.after(s.attribute(p.a, ns, local, value), p.b)
	case pChoice:
		return b.choice(s.attribute(p.a, ns, local, value), s.attribute(p.b, ns, local, value))
	case pGroup:
		return b.choice(b.group(s.attribute(p.a, ns, local, value), p.b), b.group(p.a, s.attribute(p.b, ns, local, value)))
	case pInterleave:
		return b.choice(b.interleave(s.attribute(p.a, ns, local, value), p.b), b.interleave(p.a, s.attribute(p.b, ns, local, value)))
	case pOneOrMore:
		return b.group(s.attribute(p.a, ns, local, value), b.choice(p, b.empty))
	case pAttribute:
		if p.nc.contains(ns, local) && s.valueMatch(p.a, value) {
			return b.empty
		}
	}
	return b.notAllowed
}

func (s *Schema) valueMatch(p *pat, v string) bool {
	return (p.nullable && strings.TrimSpace(v) == "") || s.text(p, v).nullable
}

func (s *Schema) startTagClose(p *pat, lenient bool) *pat {
	if !lenient {
		if r, ok := s.stc[p]; ok {
			return r
		}
	}
	b := s.b
	var r *pat
	switch p.k {
	case pAfter:
		r = b.after(s.startTagClose(p.a, lenient), p.b)
	case pChoice:
		r = b.choice(s.startTagClose(p.a, lenient), s.startTagClose(p.b, lenient))
	case pGroup:
		r = b.group(s.startTagClose(p.a, lenient), s.startTagClose(p.b, lenient))
	case pInterleave:
		r = b.interleave(s.startTagClose(p.a, lenient), s.startTagClose(p.b, lenient))
	case pOneOrMore:
		r = b.oneOrMore(s.startTagClose(p.a, lenient))
	case pAttribute:
		if lenient {
			r = b.empty
		} else {
			r = b.notAllowed
		}
	default:
		r = p
	}
	if !lenient {
		s.stc[p] = r
	}
	return r
}

func (s *Schema) endTag(p *pat, lenient bool) *pat {
	if !lenient {
		if r, ok := s.et[p]; ok {
			return r
		}
	}
	var r *pat
	switch p.k {
	case pAfter:
		if p.a.nullable || lenient {
			r = p.b
		} else {
			r = s.b.notAllowed
		}
	case pChoice:
		r = s.b.choice(s.endTag(p.a, lenient), s.endTag(p.b, lenient))
	default:
		r = s.b.notAllowed
	}
	if !lenient {
		s.et[p] = r
	}
	return r
}

func (s *Schema) text(p *pat, t string) *pat {
	b := s.b
	switch p.k {
	case pChoice:
		return b.choice(s.text(p.a, t), s.text(p.b, t))
	case pInterleave:
		return b.choice(b.interleave(s.text(p.a, t), p.b), b.interleave(p.a, s.text(p.b, t)))
	case pGroup:
		r := b.group(s.text(p.a, t), p.b)
		if p.a.nullable {
			r = b.choice(r, s.text(p.b, t))
		}
		return r
	case pAfter:
		return b.after(s.text(p.a, t), p.b)
	case pOneOrMore:
		return b.group(s.text(p.a, t), b.choice(p, b.empty))
	case pText:
		return p
	case pValue:
		if p.dt.equal(p.value, t) {
			return b.empty
		}
	case pData:
		if p.dt.allows(t) {
			return b.empty
		}
	case pDataExcept:
		if p.dt.allows(t) && !s.text(p.a, t).nullable {
			return b.empty
		}
	case pList:
		q := p.a
		for _, w := range strings.Fields(t) {
			q = s.text(q, w)
		}
		if q.nullable {
			return b.empty
		}
	}
	return b.notAllowed
}

// ----- validation -----

// Error is one validation error.
type Error struct {
	Line, Column int
	Message      string
}

// ParseError reports a document that is not well-formed XML.
type ParseError struct {
	Line    int
	Message string
}

func (e *ParseError) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Message) }

// Validate checks a document against the schema.
func (s *Schema) Validate(r io.Reader) ([]Error, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.b.tab) > 1_000_000 || len(s.sto) > 500_000 {
		if err := s.compile(); err != nil {
			return nil, err
		}
	}

	d := xml.NewDecoder(r)
	d.Strict = true
	var errs []Error
	fail := func(line, col int, format string, args ...any) {
		errs = append(errs, Error{line, col, fmt.Sprintf(format, args...)})
	}

	p := s.root
	var names []string
	skip := 0 // depth inside an element that was not allowed
	var text strings.Builder
	textLine, textCol := 0, 0

	flush := func() {
		t := text.String()
		text.Reset()
		if t == "" {
			return
		}
		if strings.TrimSpace(t) == "" {
			p = s.b.choice(p, s.text(p, t))
			return
		}
		if q := s.text(p, t); q.k != pNotAllowed {
			p = q
			return
		}
		fail(textLine, textCol, "text not allowed here; %s", s.expected(p))
	}

	for {
		line, col := d.InputPos()
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			l, _ := d.InputPos()
			if se, ok := err.(*xml.SyntaxError); ok {
				return errs, &ParseError{se.Line, se.Msg}
			}
			return errs, &ParseError{l, err.Error()}
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if skip > 0 {
				skip++
				continue
			}
			flush()
			q := s.startTagOpen(p, t.Name.Space, t.Name.Local)
			if q.k == pNotAllowed {
				fail(line, col, "element %q not allowed here; %s", t.Name.Local, s.expected(p))
				skip = 1
				continue
			}
			for _, a := range t.Attr {
				if a.Name.Space == "xmlns" || (a.Name.Space == "" && a.Name.Local == "xmlns") {
					continue
				}
				q2 := s.attribute(q, a.Name.Space, a.Name.Local, a.Value)
				if q2.k != pNotAllowed {
					q = q2
					continue
				}
				if s.attributeNamed(q, a.Name.Space, a.Name.Local) {
					fail(line, col, "value of attribute %q is invalid", a.Name.Local)
				} else {
					fail(line, col, "attribute %q not allowed here", a.Name.Local)
				}
			}
			q3 := s.startTagClose(q, false)
			if q3.k == pNotAllowed {
				fail(line, col, "element %q missing one or more required attributes", t.Name.Local)
				q3 = s.startTagClose(q, true)
			}
			p = q3
			names = append(names, t.Name.Local)
		case xml.EndElement:
			if skip > 0 {
				skip--
				continue
			}
			flush()
			q := s.endTag(p, false)
			if q.k == pNotAllowed {
				name := names[len(names)-1]
				fail(line, col, "element %q incomplete; %s", name, s.expectedMissing(p))
				q = s.endTag(p, true)
			}
			p = q
			names = names[:len(names)-1]
			if p.k == pNotAllowed {
				return errs, nil // cannot recover; the errors so far stand
			}
		case xml.CharData:
			if skip == 0 {
				if text.Len() == 0 {
					textLine, textCol = line, col
				}
				text.Write(t)
			}
		}
	}
	return errs, nil
}

// attributeNamed reports whether any attribute pattern reachable in p, other
// than inside element content, accepts the name.
func (s *Schema) attributeNamed(p *pat, ns, local string) bool {
	seen := map[*pat]bool{}
	var walk func(*pat) bool
	walk = func(q *pat) bool {
		if q == nil || seen[q] || q.k == pElement {
			return false
		}
		seen[q] = true
		if q.k == pAttribute {
			return q.nc.contains(ns, local)
		}
		return walk(q.a) || walk(q.b)
	}
	return walk(p)
}

// expected describes what could come next in p, as Jing does: the end tag,
// if the element may end here, and the elements that may start.
func (s *Schema) expected(p *pat) string {
	names := s.firstElements(p)
	end := s.endTag(p, false).k != pNotAllowed
	var parts []string
	if end {
		parts = append(parts, "the element end-tag")
	}
	if len(names) > 0 {
		parts = append(parts, "element "+quoteList(names))
	}
	if len(parts) == 0 {
		return "expected text"
	}
	return "expected " + strings.Join(parts, " or ")
}

func (s *Schema) expectedMissing(p *pat) string {
	names := s.firstElements(p)
	if len(names) == 0 {
		return "missing required content"
	}
	if len(names) == 1 {
		return fmt.Sprintf("missing required element %q", names[0])
	}
	return "expected element " + quoteList(names)
}

// firstElements lists the names of elements that could start in p.
func (s *Schema) firstElements(p *pat) []string {
	set := map[string]bool{}
	seen := map[*pat]bool{}
	var walk func(*pat)
	walk = func(q *pat) {
		if q == nil || seen[q] {
			return
		}
		seen[q] = true
		switch q.k {
		case pElement:
			if q.name != "*" {
				set[q.name] = true
			}
		case pChoice, pInterleave:
			walk(q.a)
			walk(q.b)
		case pGroup:
			walk(q.a)
			if q.a.nullable {
				walk(q.b)
			}
		case pOneOrMore, pAfter:
			walk(q.a)
		}
	}
	walk(p)
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func quoteList(names []string) string {
	const limit = 10
	more := len(names) > limit
	if more {
		names = names[:limit]
	}
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = fmt.Sprintf("%q", n)
	}
	switch {
	case more:
		return strings.Join(q, ", ") + " (and more)"
	case len(q) == 1:
		return q[0]
	}
	return strings.Join(q[:len(q)-1], ", ") + " or " + q[len(q)-1]
}

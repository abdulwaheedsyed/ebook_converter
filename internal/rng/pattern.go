package rng

import "sort"

type kind uint8

const (
	pEmpty kind = iota
	pNotAllowed
	pText
	pChoice
	pInterleave
	pGroup
	pOneOrMore
	pList
	pData
	pDataExcept
	pValue
	pAttribute
	pElement
	pAfter
)

// pat is a pattern. Every pat is made through a builder, which interns it:
// structurally equal patterns are the same pointer, so pointers can key
// memo tables and compare cheaply.
type pat struct {
	k        kind
	a, b     *pat
	nc       *nameClass
	dt       *datatype
	value    string
	nullable bool
	id       int32

	// Elements are identified by their definition, not their structure,
	// since their content may refer back to them.
	content *pat
	name    string // a readable name, for messages
}

type key struct {
	k     kind
	a, b  int32
	nc    *nameClass
	dt    *datatype
	value string
}

type builder struct {
	tab                     map[key]*pat
	n                       int32
	empty, notAllowed, text *pat
}

func newBuilder() *builder {
	b := &builder{tab: map[key]*pat{}}
	b.empty = b.intern(&pat{k: pEmpty, nullable: true})
	b.notAllowed = b.intern(&pat{k: pNotAllowed})
	b.text = b.intern(&pat{k: pText, nullable: true})
	return b
}

func (b *builder) intern(p *pat) *pat {
	k := key{k: p.k, nc: p.nc, dt: p.dt, value: p.value}
	if p.a != nil {
		k.a = p.a.id
	}
	if p.b != nil {
		k.b = p.b.id
	}
	if q, ok := b.tab[k]; ok {
		return q
	}
	b.n++
	p.id = b.n
	b.tab[k] = p
	return p
}

func (b *builder) element(nc *nameClass, name string) *pat {
	b.n++
	return &pat{k: pElement, nc: nc, name: name, id: b.n}
}

// choice keeps its alternatives as a sorted set, so that equal choices
// intern to one pattern however they were built.
func (b *builder) choice(x, y *pat) *pat {
	if x.k == pNotAllowed {
		return y
	}
	if y.k == pNotAllowed || x == y {
		return x
	}
	var set []*pat
	var collect func(*pat)
	collect = func(p *pat) {
		if p.k == pChoice {
			collect(p.a)
			collect(p.b)
			return
		}
		set = append(set, p)
	}
	collect(x)
	collect(y)
	sort.Slice(set, func(i, j int) bool { return set[i].id < set[j].id })
	uniq := set[:1]
	for _, p := range set[1:] {
		if p != uniq[len(uniq)-1] {
			uniq = append(uniq, p)
		}
	}
	r := uniq[len(uniq)-1]
	for i := len(uniq) - 2; i >= 0; i-- {
		l := uniq[i]
		r = b.intern(&pat{k: pChoice, a: l, b: r, nullable: l.nullable || r.nullable})
	}
	return r
}

func (b *builder) group(x, y *pat) *pat {
	switch {
	case x.k == pNotAllowed || y.k == pNotAllowed:
		return b.notAllowed
	case x.k == pEmpty:
		return y
	case y.k == pEmpty:
		return x
	}
	return b.intern(&pat{k: pGroup, a: x, b: y, nullable: x.nullable && y.nullable})
}

func (b *builder) interleave(x, y *pat) *pat {
	switch {
	case x.k == pNotAllowed || y.k == pNotAllowed:
		return b.notAllowed
	case x.k == pEmpty:
		return y
	case y.k == pEmpty:
		return x
	}
	return b.intern(&pat{k: pInterleave, a: x, b: y, nullable: x.nullable && y.nullable})
}

func (b *builder) after(x, y *pat) *pat {
	if x.k == pNotAllowed || y.k == pNotAllowed {
		return b.notAllowed
	}
	return b.intern(&pat{k: pAfter, a: x, b: y})
}

func (b *builder) oneOrMore(x *pat) *pat {
	if x.k == pNotAllowed || x.k == pEmpty {
		return x
	}
	return b.intern(&pat{k: pOneOrMore, a: x, nullable: x.nullable})
}

func (b *builder) list(x *pat) *pat {
	if x.k == pNotAllowed {
		return x
	}
	return b.intern(&pat{k: pList, a: x})
}

func (b *builder) attribute(nc *nameClass, content *pat) *pat {
	return b.intern(&pat{k: pAttribute, nc: nc, a: content})
}

func (b *builder) data(dt *datatype, except *pat) *pat {
	if except != nil {
		return b.intern(&pat{k: pDataExcept, dt: dt, a: except})
	}
	return b.intern(&pat{k: pData, dt: dt})
}

func (b *builder) valuePat(dt *datatype, v string) *pat {
	return b.intern(&pat{k: pValue, dt: dt, value: dt.normalise(v)})
}

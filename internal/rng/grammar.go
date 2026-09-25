package rng

import (
	"fmt"
	"io/fs"
	"path"
)

// definition is one named pattern after includes and divs are resolved.
type definition struct {
	parts []defPart
}

type defPart struct {
	combine string
	pattern *node
	pos     string
}

type loader struct {
	fsys fs.FS
}

// load reads a file and flattens it: divs are opened up and includes
// replaced by the included file's components, less any the include block
// overrides, plus the block's own.
func (l *loader) load(file, inheritNS string, depth int) ([]component, error) {
	if depth > 32 {
		return nil, fmt.Errorf("%s: includes nested too deeply", file)
	}
	src, err := fs.ReadFile(l.fsys, file)
	if err != nil {
		return nil, err
	}
	comps, err := parseFile(file, string(src), inheritNS)
	if err != nil {
		return nil, err
	}
	return l.flatten(file, comps, depth)
}

func (l *loader) flatten(file string, comps []component, depth int) ([]component, error) {
	var out []component
	for _, c := range comps {
		switch c.kind {
		case "div":
			inner, err := l.flatten(file, c.body, depth)
			if err != nil {
				return nil, err
			}
			out = append(out, inner...)
		case "include":
			target := path.Join(path.Dir(file), c.href)
			included, err := l.load(target, c.inherit, depth+1)
			if err != nil {
				return nil, err
			}
			block, err := l.flatten(file, c.body, depth)
			if err != nil {
				return nil, err
			}
			overridden := map[string]bool{}
			overridesStart := false
			for _, b := range block {
				if b.kind == "start" {
					overridesStart = true
				} else if b.kind == "define" {
					overridden[b.name] = true
				}
			}
			for _, ic := range included {
				if (ic.kind == "start" && overridesStart) || (ic.kind == "define" && overridden[ic.name]) {
					continue
				}
				out = append(out, ic)
			}
			out = append(out, block...)
		default:
			out = append(out, c)
		}
	}
	return out, nil
}

// grammar collects definitions by name and combines each into one pattern.
func combine(comps []component) (start *node, defs map[string]*node, err error) {
	groups := map[string]*definition{}
	var startDef definition
	for _, c := range comps {
		part := defPart{combine: c.combine, pattern: c.pattern, pos: c.pos}
		if c.kind == "start" {
			startDef.parts = append(startDef.parts, part)
			continue
		}
		d := groups[c.name]
		if d == nil {
			d = &definition{}
			groups[c.name] = d
		}
		d.parts = append(d.parts, part)
	}
	merge := func(name string, d *definition) (*node, error) {
		if len(d.parts) == 1 {
			return d.parts[0].pattern, nil
		}
		method := ""
		for _, p := range d.parts {
			if p.combine == "" {
				continue
			}
			if method != "" && method != p.combine {
				return nil, fmt.Errorf("%s: %q is combined both by choice and by interleave", p.pos, name)
			}
			method = p.combine
		}
		if method == "" {
			return nil, fmt.Errorf("%s: %q is defined more than once", d.parts[1].pos, name)
		}
		kids := make([]*node, len(d.parts))
		for i, p := range d.parts {
			kids[i] = p.pattern
		}
		return &node{op: method, kids: kids, pos: d.parts[0].pos}, nil
	}
	defs = map[string]*node{}
	for name, d := range groups {
		if defs[name], err = merge(name, d); err != nil {
			return nil, nil, err
		}
	}
	if len(startDef.parts) == 0 {
		return nil, nil, fmt.Errorf("the schema has no start")
	}
	start, err = merge("start", &startDef)
	return start, defs, err
}

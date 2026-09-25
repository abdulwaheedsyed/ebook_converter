package check

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// The rules of epubcheck's epub-xhtml-30.sch, which cover what the
// RELAX NG schema cannot express: forbidden descendants, required
// ancestors, ID references and a few co-occurrence constraints. Errors
// are reported as RSC-005 and warnings as RSC-017, as epubcheck does.

const (
	mathNS = "http://www.w3.org/1998/Math/MathML"
	ssmlNS = "http://www.w3.org/2001/10/synthesis"
	evNS   = "http://www.w3.org/2001/xml-events"
	xmlNS  = "http://www.w3.org/XML/1998/namespace"
)

// disallowedIn maps an element to the ancestors it must not appear inside.
var disallowedIn = map[string][]string{
	"audio":    {"audio", "video"},
	"video":    {"video", "audio"},
	"address":  {"address"},
	"header":   {"address", "header", "footer"},
	"footer":   {"address", "header", "footer"},
	"form":     {"form"},
	"progress": {"progress"},
	"meter":    {"meter"},
	"dfn":      {"dfn"},
	"table":    {"caption"},
	"label":    {"label"},
}

// labelTargets are the elements a label's for attribute may refer to.
var labelTargets = []string{"button", "input", "meter", "output", "progress", "select", "textarea"}

var javascriptTypes = []string{
	"module", "application/ecmascript", "application/javascript", "application/x-ecmascript",
	"application/x-javascript", "text/ecmascript", "text/javascript", "text/javascript1.0",
	"text/javascript1.1", "text/javascript1.2", "text/javascript1.3", "text/javascript1.4",
	"text/javascript1.5", "text/jscript", "text/livescript", "text/x-ecmascript", "text/x-javascript",
}

var utf8ContentType = regexp.MustCompile(`(?i)text/html;\s*charset=utf-8`)

// plainAttr returns an attribute without a namespace, as XPath's @name.
func (n *node) plainAttr(local string) (string, bool) {
	return n.attrNS("", local)
}

func (n *node) has(local string) bool {
	_, ok := n.plainAttr(local)
	return ok
}

func (n *node) is(space, local string) bool {
	return n.name.Space == space && n.name.Local == local
}

func (n *node) isHTML(local string) bool { return n.is(xhtmlNS, local) }

// interactive reports whether n is interactive content, which a and
// button elements must not contain.
func (n *node) interactive() bool {
	if n.name.Space != xhtmlNS {
		return false
	}
	switch n.name.Local {
	case "a", "button", "details", "embed", "iframe", "label", "menu", "select", "textarea":
		return true
	case "audio", "video":
		return n.has("controls")
	case "img", "object":
		return n.has("usemap")
	case "input":
		t, _ := n.plainAttr("type")
		return t != "hidden"
	}
	return false
}

func normalizeSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

func (c *checker) checkSchematron(it *item, root *node) {
	errorf := func(n *node, format string, args ...any) {
		c.report("RSC-005", it.path, n.line, n.col, fmt.Sprintf(format, args...))
	}
	warnf := func(n *node, format string, args ...any) {
		c.report("RSC-017", it.path, n.line, n.col, fmt.Sprintf(format, args...))
	}

	// The document's IDs and map names, with how often each occurs.
	ids := map[string][]*node{}
	mapNames := map[string]int{}
	root.walk(func(n *node) {
		if id, ok := n.plainAttr("id"); ok {
			ids[id] = append(ids[id], n)
		}
		if n.isHTML("map") {
			if name, ok := n.plainAttr("name"); ok {
				mapNames[name]++
			}
		}
	})
	exists := func(id string) bool { return len(ids[id]) > 0 }

	var visit func(n *node, anc []*node)
	visit = func(n *node, anc []*node) {
		nearest := func(match func(*node) bool) *node {
			for i := len(anc) - 1; i >= 0; i-- {
				if match(anc[i]) {
					return anc[i]
				}
			}
			return nil
		}
		html := n.name.Space == xhtmlNS
		local := n.name.Local

		if html {
			switch local {
			case "title":
				if normalizeSpace(n.text()) == "" {
					errorf(n, `Element "title" must not be empty.`)
				}
			case "area":
				if nearest(func(a *node) bool { return a.isHTML("map") }) == nil {
					errorf(n, "The area element must have an ancestor map element.")
				}
			case "img":
				if n.has("ismap") && nearest(func(a *node) bool { return a.isHTML("a") && a.has("href") }) == nil {
					errorf(n, "The img[@ismap] element must have an ancestor a[@href] element.")
				}
			case "bdo":
				if !n.has("dir") {
					errorf(n, "The bdo element must have a dir attribute.")
				}
			case "label":
				if f, ok := n.plainAttr("for"); ok {
					allowed := slices.ContainsFunc(ids[f], func(t *node) bool {
						typ, _ := t.plainAttr("type")
						return slices.Contains(labelTargets, t.name.Local) && !(t.name.Local == "input" && typ == "hidden")
					})
					if !allowed {
						errorf(n, "The for attribute does not refer to an allowed target element (expecting: button|meter|output|progress|select|textarea|input[not(@type='hidden')]).")
					}
				}
			case "output":
				c.idrefs(n, "for", exists, errorf)
			case "input":
				if l, ok := n.plainAttr("list"); ok && !slices.ContainsFunc(ids[l], func(t *node) bool { return t.isHTML("datalist") }) {
					errorf(n, "The list attribute does not refer to an allowed target element (expecting: datalist).")
				}
			case "map":
				name, hasName := n.plainAttr("name")
				if hasName && mapNames[name] > 1 {
					errorf(n, "Duplicate map name %q", name)
				}
				if id, ok := n.plainAttr("id"); ok && hasName && id != name {
					errorf(n, "The id attribute on the map element must have the same value as the name attribute.")
				}
			case "select":
				if !n.has("multiple") {
					selected := 0
					for _, o := range n.findAll("option") {
						if o.isHTML("option") && o.has("selected") {
							selected++
						}
					}
					if selected > 1 {
						errorf(n, "A select element whose multiple attribute is not specified must not have more than one descendant option element with its selected attribute set.")
					}
				}
			case "track":
				if l, ok := n.plainAttr("label"); ok && normalizeSpace(l) == "" {
					errorf(n, "The track element label attribute value must not be the empty string.")
				}
				if n.has("default") && len(anc) > 0 {
					for _, s := range anc[len(anc)-1].kids {
						if s == n {
							break
						}
						if s.isHTML("track") && s.has("default") {
							errorf(n, "There must not be more than one track child of a media element element with the default attribute specified.")
							break
						}
					}
				}
			case "link":
				if n.has("sizes") {
					if rel, _ := n.plainAttr("rel"); rel != "icon" {
						errorf(n, "The sizes attribute must not be specified on link elements that do not have a rel attribute that specifies the icon keyword.")
					}
				}
			case "meta":
				if len(anc) > 0 {
					c.checkMeta(n, anc[len(anc)-1], errorf)
				}
			case "script":
				typ, hasType := n.plainAttr("type")
				if n.has("src") && hasType && !slices.Contains(javascriptTypes, typ) {
					errorf(n, "A 'script' element must not have an 'src' attribute if it has 'type' attribute whose value is neither a JavaScript MIME Type nor 'module'.")
				}
			}

			// Microdata.
			if n.has("itemprop") {
				switch local {
				case "a", "area":
					if !n.has("href") {
						errorf(n, "If the itemprop is specified on an a element, then the href attribute must also be specified.")
					}
				case "iframe", "embed", "object":
					if !n.has("data") {
						errorf(n, "If the itemprop is specified on an iframe, embed or object element, then the data attribute must also be specified.")
					}
				case "audio", "video":
					if !n.has("src") {
						errorf(n, "If the itemprop is specified on an video or audio element, then the src attribute must also be specified.")
					}
				}
			}

			for _, a := range disallowedIn[local] {
				if nearest(func(m *node) bool { return m.isHTML(a) }) != nil {
					errorf(n, "The %s element must not appear inside %s elements.", local, a)
				}
			}
			if n.interactive() {
				for _, a := range []string{"a", "button"} {
					if nearest(func(m *node) bool { return m.isHTML(a) }) != nil {
						errorf(n, "The %s element must not appear inside %s elements.", local, a)
					}
				}
			}

			if f, ok := n.plainAttr("form"); ok && !slices.ContainsFunc(ids[f], func(t *node) bool { return t.isHTML("form") }) {
				errorf(n, "The form attribute does not refer to an allowed target element (expecting: form).")
			}
			if h, ok := n.plainAttr("headers"); ok {
				var th []*node
				for _, a := range anc {
					if a.isHTML("table") {
						th = append(th, a.findAll("th")...)
					}
				}
				for _, ref := range strings.Fields(h) {
					if !slices.ContainsFunc(th, func(t *node) bool { id, _ := t.plainAttr("id"); return t.isHTML("th") && id == ref }) {
						errorf(n, "The headers attribute must refer to th elements in the same table.")
						break
					}
				}
			}
			if lang, ok := n.plainAttr("lang"); ok {
				if xl, ok := n.attrNS(xmlNS, "lang"); ok && !strings.EqualFold(lang, xl) {
					errorf(n, "The lang and xml:lang attributes must have the same value.")
				}
			}
			if role, ok := n.plainAttr("role"); ok {
				roles := strings.Fields(role)
				for _, r := range []string{"doc-endnote", "doc-biblioentry"} {
					if slices.Contains(roles, r) {
						warnf(n, "The %q role is deprecated and should not be used.", r)
					}
				}
				for _, r := range []string{"doc-pagefooter", "doc-pageheader"} {
					if !slices.Contains(roles, r) {
						continue
					}
					for _, a := range []string{"aria-label", "aria-labelledby"} {
						if n.has(a) {
							errorf(n, "the '%s' attribute must not be specified on an element that has a '%s' role.", a, r)
							break
						}
					}
				}
			}
		}

		if n.name.Space == mathNS {
			for _, a := range []string{"xref", "indenttarget"} {
				if ref, ok := n.plainAttr(a); ok && !exists(ref) {
					errorf(n, "The %s attribute must refer to an element in the same document (the ID %q does not exist).", a, ref)
				}
			}
		}
		if n.is(opsNS, "trigger") {
			if ref, ok := n.attrNS(evNS, "observer"); ok && !exists(ref) {
				errorf(n, "The ev:observer attribute must refer to an element in the same document (the ID %q does not exist).", ref)
			}
			if ref, ok := n.plainAttr("ref"); ok && !exists(ref) {
				errorf(n, "The ref attribute must refer to an element in the same document (the ID %q does not exist).", ref)
			}
		}

		for _, a := range []string{"aria-describedby", "aria-flowto", "aria-labelledby", "aria-owns", "aria-controls"} {
			c.idrefs(n, a, exists, errorf)
		}
		if ref, ok := n.plainAttr("aria-activedescendant"); ok {
			found := false
			for _, t := range ids[ref] {
				if t != n && n.contains(t) {
					found = true
					break
				}
			}
			if !found {
				errorf(n, "The aria-activedescendant attribute must refer to a descendant element.")
			}
		}
		if id, ok := n.plainAttr("id"); ok && len(ids[id]) > 1 {
			errorf(n, "Duplicate ID %q", id)
		}
		if _, ok := n.attrNS(ssmlNS, "ph"); ok {
			if nearest(func(m *node) bool { _, ok := m.attrNS(ssmlNS, "ph"); return ok }) != nil {
				errorf(n, "The ssml:ph attribute must not be specified on a descendant of an element that also carries this attribute.")
			}
		}

		anc = append(anc, n)
		for _, k := range n.kids {
			visit(k, anc)
		}
	}
	visit(root, nil)
}

// idrefs checks that every ID in an IDREFS attribute exists.
func (c *checker) idrefs(n *node, attr string, exists func(string) bool, errorf func(*node, string, ...any)) {
	v, ok := n.plainAttr(attr)
	if !ok {
		return
	}
	for _, ref := range strings.Fields(v) {
		if !exists(ref) {
			errorf(n, "The %s attribute must refer to elements in the same document (target ID missing)", attr)
			return
		}
	}
}

// checkMeta applies the encoding declaration rules to a meta element.
func (c *checker) checkMeta(n, parent *node, errorf func(*node, string, ...any)) {
	if eq, _ := n.plainAttr("http-equiv"); strings.ToLower(eq) == "content-type" {
		if content, _ := n.plainAttr("content"); !utf8ContentType.MatchString(normalizeSpace(content)) {
			errorf(n, `The meta element in encoding declaration state (http-equiv='content-type') must have the value "text/html; charset=utf-8"`)
		}
		if slices.ContainsFunc(parent.kids, func(m *node) bool { return m.isHTML("meta") && m.has("charset") }) {
			errorf(n, "A document must not contain both a meta element in encoding declaration state (http-equiv='content-type') and a meta element with the charset attribute present.")
		}
	}
	if n.has("charset") {
		for _, s := range parent.kids {
			if s == n {
				break
			}
			if s.isHTML("meta") && s.has("charset") {
				errorf(n, "There must not be more than one meta element with a charset attribute per document.")
				break
			}
		}
	}
}

// contains reports whether d is n or one of its descendants.
func (n *node) contains(d *node) bool {
	if n == d {
		return true
	}
	for _, k := range n.kids {
		if k.contains(d) {
			return true
		}
	}
	return false
}

package check

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"path"
	"strconv"
	"strings"
)

// htmlElements are the element names of the HTML vocabulary, which XHTML
// content documents draw on.
var htmlElements = map[string]bool{}

func init() {
	for _, e := range strings.Fields(`a abbr address area article aside audio b base bdi bdo blockquote
		body br button canvas caption cite code col colgroup data datalist dd del details dfn dialog
		div dl dt em embed fieldset figcaption figure footer form h1 h2 h3 h4 h5 h6 head header hgroup
		hr html i iframe img input ins kbd label legend li link main map mark menu meta meter nav
		noscript object ol optgroup option output p param picture pre progress q rb rp rt rtc ruby s
		samp script search section select slot small source span strong style sub summary sup table
		tbody td template textarea tfoot th thead time title tr track u ul var video wbr`) {
		htmlElements[e] = true
	}
}

// Attributes that reference other resources, per element.
var refAttrs = map[string][]string{
	"a": {"href"}, "area": {"href"}, "link": {"href"}, "img": {"src"}, "script": {"src"},
	"audio": {"src"}, "video": {"src", "poster"}, "source": {"src"}, "track": {"src"},
	"iframe": {"src"}, "embed": {"src"}, "object": {"data"},
}

func (c *checker) checkXHTML(it *item, data []byte) {
	doc, perr := parseXML(data)
	if perr != nil {
		c.report("RSC-016", it.path, perr.line, perr.col, perr.msg)
		return
	}
	root := doc.root()

	// Elements outside the HTML vocabulary, and deprecated EPUB elements.
	root.walk(func(n *node) {
		switch {
		case n.name.Space == xhtmlNS && !htmlElements[n.name.Local]:
			c.report("RSC-005", it.path, n.line, n.col, `element "`+n.name.Local+`" not allowed here`)
		case n.name.Space == opsNS && (n.name.Local == "switch" || n.name.Local == "trigger"):
			c.report("RSC-017", it.path, n.line, n.col, `The "epub:`+n.name.Local+`" element is deprecated.`)
		}
	})

	if head := root.child("head"); head != nil {
		if t := head.child("title"); t == nil {
			c.report("RSC-017", it.path, head.line, head.col, `The "head" element should have a "title" child element.`)
		}
	}

	if slicesContains(it.props, "nav") {
		c.checkNav(it, root)
	}
	c.checkFeatureProps(it, root)

	// A base URL on another host makes relative references remote.
	remoteBase := false
	if head := root.child("head"); head != nil {
		if b := head.child("base"); b != nil && strings.Contains(b.attr("href"), ":") {
			remoteBase = true
		}
	}
	if c.fxlDocs[it.path] {
		c.checkViewport(it, root)
	}

	for _, n := range collect(root) {
		for _, a := range refAttrs[n.name.Local] {
			href := n.attr(a)
			if href == "" {
				continue
			}
			if why := urlSyntaxError(href); why != "" {
				c.report("RSC-020", it.path, n.line, n.col, href, why)
				continue
			}
			if remoteBase {
				continue
			}
			if strings.HasPrefix(href, "/") && !strings.HasPrefix(href, "//") {
				c.report("RSC-026", it.path, n.line, n.col, href)
			}
			target, ok := resolve(it.path, href)
			if !ok {
				continue
			}
			_, inZip := c.files[target]
			decl, inManifest := c.declared[target]
			switch {
			case !inZip && inManifest:
				// Reported once, as the missing manifest file (RSC-001).
			case !inZip:
				c.report("RSC-007", it.path, n.line, n.col, target)
			case !inManifest:
				c.report("RSC-008", it.path, n.line, n.col, target)
			case n.name.Local == "a" && decl.mediaType == "application/xhtml+xml" && !c.spine[target]:
				c.report("RSC-011", it.path, n.line, n.col)
			}
		}
	}
}

// urlSyntaxError reports why href is not a valid URL, judging it as Java's
// URI parser, which epubcheck uses, does: characters outside the URL syntax
// are refused, and so is a malformed percent escape. Characters beyond ASCII
// are accepted, as that parser accepts them.
func urlSyntaxError(href string) string {
	part := "path"
	for i := 0; i < len(href); i++ {
		b := href[i]
		switch {
		case b == '?' && part == "path":
			part = "query"
		case b == '#' && part != "fragment":
			part = "fragment"
		case b == '%':
			if i+2 >= len(href) || !isHex(href[i+1]) || !isHex(href[i+2]) {
				return "Malformed escape pair in " + part
			}
		case b >= 0x80:
		case b <= 0x20 || strings.IndexByte(`"<>\^`+"`"+`{|}[]#`, b) >= 0:
			return fmt.Sprintf("Illegal character in %s: %q is not allowed", part, b)
		}
	}
	return ""
}

func isHex(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F'
}

func collect(root *node) []*node {
	var out []*node
	root.walk(func(n *node) { out = append(out, n) })
	return out
}

func slicesContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

const (
	mathmlNS = "http://www.w3.org/1998/Math/MathML"
	svgNS    = "http://www.w3.org/2000/svg"
)

// checkFeatureProps compares the features a content document uses with the
// properties its manifest item declares: switch, mathml, svg and scripted
// must be declared when used (OPF-014) and not when unused (OPF-015).
func (c *checker) checkFeatureProps(it *item, root *node) {
	used := map[string]bool{}
	root.walk(func(n *node) {
		switch {
		case n.name.Space == opsNS && n.name.Local == "switch":
			used["switch"] = true
		case n.name.Space == mathmlNS && n.name.Local == "math":
			used["mathml"] = true
		case n.name.Space == svgNS && n.name.Local == "svg":
			used["svg"] = true
		case n.name.Space == xhtmlNS && n.name.Local == "script":
			// Data blocks are not scripts.
			if t := strings.ToLower(n.attr("type")); t == "" || strings.Contains(t, "javascript") || t == "module" || t == "text/ecmascript" {
				used["scripted"] = true
			}
		case n.name.Space == xhtmlNS && n.name.Local == "form":
			used["scripted"] = true
		}
		for _, a := range n.attrs {
			if a.Name.Space == "" && strings.HasPrefix(a.Name.Local, "on") && len(a.Name.Local) > 2 {
				used["scripted"] = true
			}
		}
	})
	for _, p := range []string{"switch", "mathml", "svg", "scripted"} {
		declared := slicesContains(it.props, p)
		switch {
		case used[p] && !declared:
			c.report("OPF-014", it.path, 0, 0, p)
		case declared && !used[p]:
			c.report("OPF-015", it.path, 0, 0, p)
		}
	}
}

// checkNav requires a navigation document to have a table of contents.
func (c *checker) checkNav(it *item, root *node) {
	for _, n := range root.findAll("nav") {
		if t, ok := n.attrNS(opsNS, "type"); ok && slicesContains(strings.Fields(t), "toc") {
			return
		}
	}
	c.report("RSC-005", it.path, root.line, root.col, `the nav file must contain exactly one "nav" element with the epub:type "toc"`)
}

// checkViewport requires a fixed-layout document to declare its size, as
// epubcheck's viewport rules do.
func (c *checker) checkViewport(it *item, root *node) {
	head := root.child("head")
	var metas []*node
	if head != nil {
		for _, m := range head.children("meta") {
			if m.attr("name") == "viewport" {
				metas = append(metas, m)
			}
		}
	}
	if len(metas) == 0 {
		line, col := root.line, root.col
		if head != nil {
			line, col = head.line, head.col
		}
		c.report("HTM-046", it.path, line, col)
		return
	}
	m := metas[0]
	values := map[string][]string{}
	for _, part := range strings.FieldsFunc(m.attr("content"), func(r rune) bool { return r == ',' || r == ';' }) {
		k, v, ok := strings.Cut(part, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || k == "" {
			c.report("HTM-047", it.path, m.line, m.col, m.attr("content"))
			return
		}
		values[strings.ToLower(k)] = append(values[strings.ToLower(k)], v)
	}
	for _, dim := range []string{"width", "height"} {
		vs := values[dim]
		switch {
		case len(vs) == 0:
			c.report("HTM_056", it.path, m.line, m.col, dim)
		case len(vs) > 1:
			c.report("HTM_059", it.path, m.line, m.col, dim, strings.Join(vs, ", "))
		default:
			if n, err := strconv.ParseFloat(vs[0], 64); vs[0] != "device-"+dim && (err != nil || n <= 0) {
				c.report("HTM_057", it.path, m.line, m.col, dim)
			}
		}
	}
}

// checkCSS finds the structural errors a CSS parser cannot recover from:
// unterminated comments, strings and blocks.
func (c *checker) checkCSS(p string, data []byte) {
	depth := 0
	line := 1
	for i := 0; i < len(data); i++ {
		switch b := data[i]; {
		case b == '\n':
			line++
		case b == '/' && i+1 < len(data) && data[i+1] == '*':
			end := bytes.Index(data[i+2:], []byte("*/"))
			if end < 0 {
				c.report("CSS-008", p, line, 0, "unterminated comment")
				return
			}
			line += bytes.Count(data[i:i+2+end], []byte("\n"))
			i += end + 3
		case b == '"' || b == '\'':
			j := i + 1
			for ; j < len(data) && data[j] != b && data[j] != '\n'; j++ {
				if data[j] == '\\' {
					j++
				}
			}
			if j >= len(data) || data[j] == '\n' {
				c.report("CSS-008", p, line, 0, "unterminated string")
				return
			}
			i = j
		case b == '{':
			depth++
		case b == '}':
			if depth == 0 {
				c.report("CSS-008", p, line, 0, `unexpected "}"`)
				return
			}
			depth--
		}
	}
	if depth > 0 {
		c.report("CSS-008", p, line, 0, `Token "EOF" expected "}"`)
	}
}

// sniff names an image's format from its first bytes.
func sniff(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return "image/jpeg"
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return "image/gif"
	case len(data) > 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp"
	case bytes.Contains(data[:min(len(data), 512)], []byte("<svg")):
		return "image/svg+xml"
	}
	return ""
}

var imageExts = map[string][]string{
	"image/jpeg": {".jpg", ".jpeg", ".jpe"}, "image/png": {".png"}, "image/gif": {".gif"},
	"image/webp": {".webp"}, "image/svg+xml": {".svg"},
}

func (c *checker) checkImage(it *item, data []byte) {
	got := sniff(data)
	if got != it.mediaType {
		c.report("OPF-029", it.path, 0, 0, it.path, it.mediaType)
	}
	if got == "" {
		c.report("PKG-021", it.path, 0, 0)
		return
	}
	if got != "image/svg+xml" && got != "image/webp" {
		if _, _, err := image.DecodeConfig(bytes.NewReader(data)); err != nil {
			c.report("PKG-021", it.path, 0, 0)
			return
		}
	}
	if ext := strings.ToLower(path.Ext(it.path)); !slicesContains(imageExts[got], ext) {
		c.report("PKG-022", it.path, 0, 0, strings.TrimPrefix(got, "image/"), ext)
	}
}

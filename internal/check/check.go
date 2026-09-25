package check

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Severity follows epubcheck's levels.
type Severity int

const (
	Suppressed Severity = iota
	Info
	Usage
	Warning
	Error
	Fatal
)

func (s Severity) String() string {
	return [...]string{"SUPPRESSED", "INFO", "USAGE", "WARNING", "ERROR", "FATAL"}[s]
}

// Message is one finding, in epubcheck's terms.
type Message struct {
	ID       string
	Severity Severity
	Path     string // inside the EPUB; empty for the container as a whole
	Line     int
	Column   int
	Text     string
}

// String formats a message the way epubcheck prints one.
func (m Message) String() string {
	loc := m.Path
	if loc == "" {
		loc = "(container)"
	}
	if m.Line > 0 {
		loc += fmt.Sprintf("(%d,%d)", m.Line, m.Column)
	}
	return fmt.Sprintf("%s(%s): %s: %s", m.Severity, m.ID, loc, m.Text)
}

var placeholder = regexp.MustCompile(`%(\d+)\$s`)

// text fills a catalogue message's %1$s-style placeholders.
func text(id string, args []any) string {
	t := catalogue[id].Text
	n := len(placeholder.FindAllString(t, -1))
	for len(args) < n {
		args = append(args, "")
	}
	return fmt.Sprintf(placeholder.ReplaceAllString(t, "%[$1]v"), args...)
}

type checker struct {
	files map[string]*zip.File
	msgs  []Message

	opf  string // package document path
	base string // its directory

	// Manifest items by id, later duplicates winning, as epubcheck resolves
	// them; declared maps each resulting path to its item.
	items    map[string]*item
	declared map[string]*item
	spine    map[string]bool // paths of spine items
	fxl      bool            // the package is pre-paginated
	fxlDocs  map[string]bool // content documents laid out as fixed pages
	checked  map[string]bool // resources already checked, across renditions
}

type item struct {
	id, href, path, mediaType string
	fallback                  string // id of the fallback item
	props                     []string
	line, col                 int
}

func (c *checker) report(id, file string, line, col int, args ...any) {
	def, ok := catalogue[id]
	if !ok {
		panic("check: unknown message " + id)
	}
	c.msgs = append(c.msgs, Message{ID: id, Severity: def.Severity, Path: file, Line: line, Column: col, Text: text(id, args)})
}

func (c *checker) read(name string) ([]byte, bool) {
	f, ok := c.files[name]
	if !ok {
		return nil, false
	}
	rc, err := f.Open()
	if err != nil {
		return nil, false
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	return b, err == nil
}

// EPUB checks a publication and returns what epubcheck would report,
// ordered by file and position. Messages that epubcheck suppresses by
// default are not included.
func EPUB(data []byte) []Message {
	c := &checker{files: map[string]*zip.File{}}
	c.run(data)

	var out []Message
	for _, m := range c.msgs {
		if m.Severity != Suppressed {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Column < b.Column
	})
	return out
}

func (c *checker) run(data []byte) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		c.report("PKG-003", "", 0, 0)
		c.report("PKG-008", "", 0, 0, "the EPUB")
		return
	}
	for _, f := range zr.File {
		c.files[f.Name] = f
	}

	c.checkMimetype(zr.File)
	c.checkFileNames(zr.File)
	opfs := c.checkContainer()
	// A publication can hold several renditions, each with its own package
	// document; each is checked on its own, and a resource the renditions
	// share is checked once.
	c.checked = map[string]bool{}
	for _, opf := range opfs {
		c.opf, c.base = opf, path.Dir(opf)
		c.fxl = false
		if c.checkPackage() {
			c.checkResources()
		}
	}
}

// ----- OCF container -----

func (c *checker) checkMimetype(files []*zip.File) {
	if len(files) == 0 || files[0].Name != "mimetype" {
		c.report("PKG-006", "", 0, 0)
		return
	}
	mt := files[0]
	if len(mt.Extra) > 0 {
		c.report("PKG-005", "", 0, 0, len(mt.Extra))
	}
	if b, ok := c.read("mimetype"); !ok || string(b) != "application/epub+zip" {
		c.report("PKG-007", "", 0, 0)
	}
}

// Characters OCF does not allow in file names.
const forbiddenNameChars = "\"*:<>?\\|\x7f"

func (c *checker) checkFileNames(files []*zip.File) {
	for _, f := range files {
		name := f.Name
		var bad []string
		for _, r := range name {
			if r < 0x20 || strings.ContainsRune(forbiddenNameChars, r) || (r >= 0xE000 && r <= 0xF8FF) || r == 0xFFFD {
				bad = append(bad, fmt.Sprintf("%q", r))
			}
		}
		if len(bad) > 0 {
			c.report("PKG-009", "", 0, 0, name, strings.Join(bad, ", "))
		}
		if strings.HasSuffix(name, ".") {
			c.report("PKG-011", "", 0, 0, name)
		}
		if strings.ContainsAny(name, " \t") {
			c.report("PKG-010", "", 0, 0, name)
		}
		for _, r := range name {
			if r > 0x7e {
				c.report("PKG-012", "", 0, 0, name)
				break
			}
		}
	}
}

// checkContainer returns the package documents the container names.
func (c *checker) checkContainer() []string {
	const name = "META-INF/container.xml"
	data, ok := c.read(name)
	if !ok {
		c.report("RSC-002", "", 0, 0)
		return nil
	}
	doc, err := parseXML(data)
	if err != nil {
		c.report("RSC-016", name, err.line, err.col, err.msg)
		return nil
	}
	var opfs []string
	for _, rf := range doc.findAll("rootfile") {
		if rf.attr("media-type") != "application/oebps-package+xml" {
			continue
		}
		opfs = append(opfs, rf.attr("full-path"))
		// Renditions after the first are alternatives, and should say how a
		// reading system is to choose them.
		if len(opfs) > 1 {
			selected := false
			for _, a := range []string{"media", "layout", "language", "accessMode", "label"} {
				if _, ok := rf.attrNS(renditionNS, a); ok {
					selected = true
				}
			}
			if !selected {
				c.report("RSC-017", name, rf.line, rf.col, "At least one rendition selection attribute should be specified for each non-first rootfile element, which represents a non-default rendition.")
			}
		}
	}
	if len(opfs) == 0 {
		c.report("RSC-003", name, 0, 0)
		return nil
	}
	if _, ok := c.files["META-INF/metadata.xml"]; len(opfs) > 1 && !ok {
		c.report("RSC-019", "", 0, 0)
	}
	var found []string
	for _, p := range opfs {
		if _, ok := c.files[p]; !ok {
			c.report("OPF-002", name, 0, 0, p)
			continue
		}
		found = append(found, p)
	}
	return found
}

// resolve turns an href relative to a document into a path in the archive,
// percent-decoded and without its fragment. It reports false for remote and
// data URLs, and for fragment-only references.
func resolve(from, href string) (string, bool) {
	if i := strings.IndexByte(href, '#'); i >= 0 {
		href = href[:i]
	}
	if i := strings.IndexByte(href, '?'); i >= 0 {
		href = href[:i]
	}
	// Remote and data URLs do not name files in the container.
	if href == "" || strings.Contains(href, ":") || strings.HasPrefix(href, "//") {
		return "", false
	}
	p, err := pathUnescape(href)
	if err != nil {
		p = href
	}
	if strings.HasPrefix(p, "/") { // path-absolute: from the container root
		return strings.TrimPrefix(path.Clean(p), "/"), true
	}
	return path.Join(path.Dir(from), p), true
}

func pathUnescape(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' {
			if i+2 >= len(s) {
				return "", fmt.Errorf("bad escape")
			}
			var v byte
			for _, h := range s[i+1 : i+3] {
				v <<= 4
				switch {
				case h >= '0' && h <= '9':
					v |= byte(h - '0')
				case h >= 'a' && h <= 'f':
					v |= byte(h - 'a' + 10)
				case h >= 'A' && h <= 'F':
					v |= byte(h - 'A' + 10)
				default:
					return "", fmt.Errorf("bad escape")
				}
			}
			b.WriteByte(v)
			i += 2
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String(), nil
}

// ----- package document -----

var (
	modifiedRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`)

	renditionValues = map[string][]string{
		"rendition:layout":      {"reflowable", "pre-paginated"},
		"rendition:orientation": {"landscape", "portrait", "auto"},
		"rendition:spread":      {"none", "landscape", "portrait", "both", "auto"},
	}

	// Prefixes usable in package documents without declaring them (EPUB 3.3).
	reservedPrefixes = map[string]bool{
		"a11y": true, "dcterms": true, "marc": true, "media": true,
		"onix": true, "rendition": true, "schema": true, "xsd": true,
	}

	manifestProps = []string{
		"cover-image", "mathml", "nav", "remote-resources", "scripted", "svg", "switch",
		// Auxiliary vocabularies: region navigation, dictionaries and indexes.
		"data-nav", "dictionary", "glossary", "index", "search-key-map",
	}

	coreMediaTypes = map[string]bool{
		"application/xhtml+xml": true, "image/svg+xml": true,
	}
)

func (c *checker) checkPackage() bool {
	data, _ := c.read(c.opf)
	doc, perr := parseXML(data)
	if perr != nil {
		c.report("RSC-016", c.opf, perr.line, perr.col, perr.msg)
		return false
	}
	pkg := doc.root()
	schema := func(n *node, format string, args ...any) {
		line, col := 0, 0
		if n != nil {
			line, col = n.line, n.col
		}
		c.report("RSC-005", c.opf, line, col, fmt.Sprintf(format, args...))
	}

	// The prefix attribute maps prefixes to vocabularies; Dublin Core's
	// namespace must not be one of them. Prefixes used in property values
	// must be declared there or be reserved.
	declaredPrefix := map[string]bool{}
	f := strings.Fields(pkg.attr("prefix"))
	for i := 0; i+1 < len(f); i += 2 {
		if !strings.HasSuffix(f[i], ":") {
			continue
		}
		if f[i+1] == dcNS {
			c.report("OPF-007c", c.opf, pkg.line, pkg.col)
			continue
		}
		declaredPrefix[strings.TrimSuffix(f[i], ":")] = true
	}
	checkPrefix := func(n *node, attr string) {
		for _, v := range strings.Fields(n.attr(attr)) {
			pre, _, ok := strings.Cut(v, ":")
			if ok && !declaredPrefix[pre] && !reservedPrefixes[pre] {
				c.report("OPF-028", c.opf, n.line, n.col, pre)
			}
		}
	}
	pkg.walk(func(n *node) {
		for _, a := range []string{"property", "rel", "properties", "scheme"} {
			checkPrefix(n, a)
		}
	})

	meta := pkg.child("metadata")
	if meta == nil {
		schema(pkg, `element "metadata" missing`)
		return true
	}

	// Identifiers and the unique identifier.
	uid := pkg.attr("unique-identifier")
	ids := meta.childrenNS(dcNS, "identifier")
	found := false
	for _, id := range ids {
		if strings.TrimSpace(id.text()) == "" {
			schema(id, `element "dc:identifier" must not be empty`)
		}
		if id.attr("id") == uid {
			found = true
		}
	}
	if len(ids) == 0 {
		schema(meta, `missing required element "dc:identifier"`)
	}
	if !found {
		c.report("OPF-030", c.opf, pkg.line, pkg.col, uid)
		if len(ids) > 0 {
			schema(pkg, `package element unique-identifier attribute does not resolve to a dc:identifier element (given reference was "%s")`, uid)
		}
	}

	titles := meta.childrenNS(dcNS, "title")
	if len(titles) == 0 {
		schema(meta, `missing required element "dc:title"`)
	}
	for _, t := range titles {
		if strings.TrimSpace(t.text()) == "" {
			schema(t, `element "dc:title" must not be empty`)
		}
	}

	langs := meta.childrenNS(dcNS, "language")
	if len(langs) == 0 {
		schema(meta, `missing required element "dc:language"`)
	}
	for _, l := range langs {
		if v := strings.TrimSpace(l.text()); v == "" {
			schema(l, `element "dc:language" must not be empty`)
		} else if why := langTagError(v); why != "" {
			c.report("OPF-092", c.opf, l.line, l.col, v, why)
		}
	}

	// Metadata properties.
	count := map[string]int{}
	for _, m := range meta.children("meta") {
		prop := m.attr("property")
		if prop == "" {
			continue
		}
		v := strings.TrimSpace(m.text())
		if m.attr("refines") == "" {
			count[prop]++
		}
		switch {
		case prop == "dcterms:modified" && m.attr("refines") == "":
			if !modifiedRE.MatchString(v) {
				schema(m, `dcterms:modified illegal syntax (expecting: "CCYY-MM-DDThh:mm:ssZ")`)
			}
		case renditionValues[prop] != nil && m.attr("refines") == "":
			if !slices.Contains(renditionValues[prop], v) {
				schema(m, `the value of the "%s" property must be one of %s`, prop, strings.Join(renditionValues[prop], ", "))
			}
			if prop == "rendition:layout" && v == "pre-paginated" {
				c.fxl = true
			}
		}
	}
	switch count["dcterms:modified"] {
	case 0:
		schema(meta, `package dcterms:modified meta element must occur exactly once`)
	case 1:
	default:
		schema(meta, `package dcterms:modified meta element must occur exactly once`)
	}
	for prop := range renditionValues {
		if count[prop] > 1 {
			schema(meta, `the "%s" property must not occur more than one time in the package metadata`, prop)
		}
	}

	// Deprecated forms, which epubcheck warns about.
	if b := pkg.child("bindings"); b != nil {
		c.report("RSC-017", c.opf, b.line, b.col, "Use of the bindings element is deprecated")
	}
	for _, m := range meta.kids {
		for _, a := range []string{"scheme", "datatype", "property", "rel"} {
			if strings.HasPrefix(m.attr(a), "xsd:") {
				c.report("OPF-086c", c.opf, m.line, m.col, "xsd")
			}
		}
	}

	c.checkManifest(pkg, schema)
	c.checkSpine(pkg, schema)
	return true
}

func (c *checker) checkManifest(pkg *node, schema func(*node, string, ...any)) {
	c.items, c.declared = map[string]*item{}, map[string]*item{}
	man := pkg.child("manifest")
	if man == nil {
		schema(pkg, `element "manifest" missing`)
		return
	}
	seenID := map[string]bool{}
	byPath := map[string]int{}
	navs, covers := 0, 0
	var list []*item
	for _, n := range man.children("item") {
		it := &item{
			id: n.attr("id"), href: n.attr("href"), mediaType: n.attr("media-type"), fallback: n.attr("fallback"),
			props: strings.Fields(n.attr("properties")), line: n.line, col: n.col,
		}
		it.path, _ = resolve(c.opf, it.href)
		if seenID[it.id] {
			schema(n, `Duplicate ID "%s"`, it.id)
		}
		seenID[it.id] = true
		c.items[it.id] = it
		list = append(list, it)
		byPath[it.path]++

		for _, p := range it.props {
			switch {
			case strings.Contains(p, ":"):
			case !slices.Contains(manifestProps, p):
				c.report("OPF-027", c.opf, n.line, n.col, p)
			case p == "nav":
				navs++
			case p == "cover-image":
				covers++
				if !strings.HasPrefix(it.mediaType, "image/") {
					c.report("OPF-012", c.opf, n.line, n.col, "cover-image", it.mediaType)
				}
			}
		}
	}
	for p, n := range byPath {
		if n > 1 {
			c.report("OPF-074", c.opf, man.line, man.col, p)
		}
	}
	if navs != 1 {
		schema(man, `Exactly one manifest item must declare the "nav" property (number of "nav" items: %d).`, navs)
	}
	if covers > 1 {
		schema(man, `Multiple occurrences of the "cover-image" property (number of "cover-image" items: %d).`, covers)
	}

	// Later duplicates replace earlier ones, as in epubcheck, so resources
	// only an earlier duplicate declared count as undeclared.
	for _, it := range c.items {
		c.declared[it.path] = it
	}
	for _, it := range list {
		if _, ok := c.files[it.path]; !ok {
			c.report("RSC-001", it.path, 0, 0, it.path)
		}
	}
}

func (c *checker) checkSpine(pkg *node, schema func(*node, string, ...any)) {
	c.spine = map[string]bool{}
	sp := pkg.child("spine")
	if sp == nil {
		schema(pkg, `element "spine" missing`)
		return
	}
	switch sp.attr("page-progression-direction") {
	case "", "ltr", "rtl", "default":
	default:
		schema(sp, `value of attribute "page-progression-direction" is invalid; must be equal to "default", "ltr" or "rtl"`)
	}
	refs := sp.children("itemref")
	if len(refs) == 0 {
		schema(sp, `element "spine" incomplete; missing required element "itemref"`)
	}
	c.fxlDocs = map[string]bool{}
	seen := map[string]bool{}
	for _, r := range refs {
		idref := r.attr("idref")
		it, ok := c.items[idref]
		if !ok {
			c.report("OPF-049", c.opf, r.line, r.col, idref)
			schema(r, `itemref idref "%s" does not resolve to a manifest item`, idref)
			continue
		}
		if seen[idref] {
			schema(r, `Itemref refers to the same manifest entry as a previous itemref`)
		}
		seen[idref] = true
		c.spine[it.path] = true

		// A spine entry can override the package's layout.
		fxl := c.fxl
		for _, p := range strings.Fields(r.attr("properties")) {
			switch p {
			case "rendition:layout-pre-paginated":
				fxl = true
			case "rendition:layout-reflowable":
				fxl = false
			}
		}

		// A spine item that is not a content document needs a fallback
		// chain ending in one; that document is then laid out in its place.
		doc := it
		for hops := 0; doc != nil && !coreMediaTypes[doc.mediaType] && hops < 32; hops++ {
			doc = c.items[doc.fallback]
		}
		if doc == nil || !coreMediaTypes[doc.mediaType] {
			c.report("OPF-043", c.opf, r.line, r.col, it.mediaType)
			continue
		}
		if fxl {
			c.fxlDocs[doc.path] = true
		}
	}
}

// ----- resources -----

func (c *checker) checkResources() {
	paths := make([]string, 0, len(c.declared))
	for p := range c.declared {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if c.checked[p] {
			continue
		}
		c.checked[p] = true
		it := c.declared[p]
		data, ok := c.read(p)
		if !ok {
			continue // reported as RSC-001
		}
		switch {
		case it.mediaType == "application/xhtml+xml":
			c.checkXHTML(it, data)
		case it.mediaType == "text/css":
			c.checkCSS(p, data)
		case strings.HasPrefix(it.mediaType, "image/"):
			c.checkImage(it, data)
		}
	}
}

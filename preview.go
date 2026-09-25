package main

// Reading a finished EPUB back for the GUI's page preview. The preview
// shows what was packaged, not what the converter meant to package, so it
// follows the book's own container, spine and navigation document.

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strconv"
	"strings"
)

// previewBook is the page sequence and contents of an EPUB.
type previewBook struct {
	Title     string         `json:"title"`
	Direction string         `json:"direction"` // "ltr" or "rtl"
	Pages     []previewPage  `json:"pages"`
	TOC       []previewEntry `json:"toc"`
	images    []string       // ZIP path of each page's image
}

type previewPage struct {
	W     int    `json:"w"` // viewport, in CSS pixels
	H     int    `json:"h"`
	Label string `json:"label"`
}

type previewEntry struct {
	Title    string         `json:"title"`
	Page     int            `json:"page"`
	Children []previewEntry `json:"children,omitempty"`
}

// maxPreviewDoc bounds how much of any one XML file is read.
const maxPreviewDoc = 8 << 20

// xnode is a parsed XML element; only what the preview needs.
type xnode struct {
	name  xml.Name
	attrs []xml.Attr
	kids  []*xnode
	text  strings.Builder
}

func parseXNode(r io.Reader) (*xnode, error) {
	d := xml.NewDecoder(io.LimitReader(r, maxPreviewDoc))
	d.Strict = false
	doc := &xnode{}
	stack := []*xnode{doc}
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &xnode{name: t.Name, attrs: t.Attr}
			top := stack[len(stack)-1]
			top.kids = append(top.kids, n)
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			for _, n := range stack[1:] {
				n.text.Write(t)
			}
		}
	}
	if len(doc.kids) == 0 {
		return nil, errors.New("empty document")
	}
	return doc.kids[0], nil
}

func (n *xnode) attr(space, local string) string {
	for _, a := range n.attrs {
		if a.Name.Local == local && (space == "*" || a.Name.Space == space) {
			return a.Value
		}
	}
	return ""
}

// find returns every descendant with the local name, in document order.
func (n *xnode) find(local string) []*xnode {
	var out []*xnode
	for _, k := range n.kids {
		if k.name.Local == local {
			out = append(out, k)
		}
		out = append(out, k.find(local)...)
	}
	return out
}

func (n *xnode) child(local string) *xnode {
	for _, k := range n.kids {
		if k.name.Local == local {
			return k
		}
	}
	return nil
}

// readPreview reads an EPUB's pages and contents.
func readPreview(zr *zip.Reader) (*previewBook, error) {
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	parse := func(name string) (*xnode, error) {
		f, ok := files[name]
		if !ok {
			return nil, fmt.Errorf("%s is missing", name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		n, err := parseXNode(rc)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		return n, nil
	}

	container, err := parse("META-INF/container.xml")
	if err != nil {
		return nil, err
	}
	var opfPath string
	if rf := container.find("rootfile"); len(rf) > 0 {
		opfPath = rf[0].attr("", "full-path")
	}
	opf, err := parse(opfPath)
	if err != nil {
		return nil, err
	}

	type item struct{ href, props string }
	items := map[string]item{}
	navPath := ""
	for _, it := range opf.find("item") {
		p := resolveHref(opfPath, it.attr("", "href"))
		items[it.attr("", "id")] = item{p, it.attr("", "properties")}
		if hasToken(it.attr("", "properties"), "nav") {
			navPath = p
		}
	}

	b := &previewBook{Direction: "ltr"}
	if t := opf.find("title"); len(t) > 0 {
		b.Title = strings.TrimSpace(t[0].text.String())
	}
	spine := opf.child("spine")
	if spine == nil {
		return nil, errors.New("the package has no spine")
	}
	if spine.attr("", "page-progression-direction") == "rtl" {
		b.Direction = "rtl"
	}
	index := map[string]int{} // content document -> page index
	for _, ref := range spine.find("itemref") {
		it, ok := items[ref.attr("", "idref")]
		if !ok {
			continue
		}
		doc, err := parse(it.href)
		if err != nil {
			return nil, err
		}
		p, img := previewPage{}, ""
		for _, m := range doc.find("meta") {
			if m.attr("", "name") == "viewport" {
				p.W, p.H = parseViewport(m.attr("", "content"))
			}
		}
		if imgs := doc.find("img"); len(imgs) > 0 {
			img = imgs[0].attr("", "src")
		} else if imgs := doc.find("image"); len(imgs) > 0 {
			img = imgs[0].attr("*", "href")
		}
		if img == "" {
			return nil, fmt.Errorf("%s has no page image", it.href)
		}
		index[it.href] = len(b.Pages)
		p.Label = strconv.Itoa(len(b.Pages) + 1)
		b.Pages = append(b.Pages, p)
		b.images = append(b.images, resolveHref(it.href, img))
	}
	if len(b.Pages) == 0 {
		return nil, errors.New("the book has no pages")
	}

	// The navigation document gives the contents and the page labels. A
	// book without one still previews.
	if nav, err := parse(navPath); err == nil {
		target := func(li *xnode) (string, int, bool) {
			a := li.child("a")
			if a == nil {
				return "", 0, false
			}
			i, ok := index[resolveHref(navPath, a.attr("", "href"))]
			return strings.Join(strings.Fields(a.text.String()), " "), i, ok
		}
		var list func(ol *xnode) []previewEntry
		list = func(ol *xnode) []previewEntry {
			var out []previewEntry
			for _, li := range ol.kids {
				if li.name.Local != "li" {
					continue
				}
				title, page, ok := target(li)
				if !ok {
					continue
				}
				e := previewEntry{Title: title, Page: page}
				if sub := li.child("ol"); sub != nil {
					e.Children = list(sub)
				}
				out = append(out, e)
			}
			return out
		}
		for _, n := range nav.find("nav") {
			ol := n.child("ol")
			if ol == nil {
				continue
			}
			switch n.attr(epubNS, "type") {
			case "toc":
				b.TOC = list(ol)
			case "page-list":
				for _, e := range list(ol) {
					b.Pages[e.Page].Label = e.Title
				}
			}
		}
	}
	return b, nil
}

const epubNS = "http://www.idpf.org/2007/ops"

// resolveHref resolves a relative URL against the ZIP path of the document
// that contains it, dropping any fragment.
func resolveHref(base, href string) string {
	u, err := url.Parse(href)
	if err != nil || u.IsAbs() || u.Host != "" {
		return ""
	}
	p := u.Path
	if !strings.HasPrefix(p, "/") {
		p = path.Join(path.Dir(base), p)
	}
	return strings.TrimPrefix(path.Clean("/"+p), "/")
}

func hasToken(list, tok string) bool {
	for _, t := range strings.Fields(list) {
		if t == tok {
			return true
		}
	}
	return false
}

// parseViewport reads "width=W, height=H".
func parseViewport(s string) (w, h int) {
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' }) {
		k, v, _ := strings.Cut(part, "=")
		n, _ := strconv.Atoi(strings.TrimSpace(v))
		switch strings.TrimSpace(k) {
		case "width":
			w = n
		case "height":
			h = n
		}
	}
	return w, h
}

// previewImage returns the bytes of page i's image.
func previewImage(zr *zip.Reader, b *previewBook, i int) ([]byte, string, error) {
	name := b.images[i]
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, "", err
		}
		defer rc.Close()
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, io.LimitReader(rc, 64<<20)); err != nil {
			return nil, "", err
		}
		return buf.Bytes(), imageType(name), nil
	}
	return nil, "", fmt.Errorf("%s is missing", name)
}

func imageType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	}
	return "image/jpeg"
}

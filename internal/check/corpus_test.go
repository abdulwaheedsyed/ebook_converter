package check

// A corpus of fixed-layout EPUBs, one valid and the rest each broken in one
// way, used to compare the built-in checks with epubcheck.
//
// The corpus is built here rather than stored, so every case is readable as
// the one change it makes. testdata/epubcheck.json records what epubcheck
// reported for each case; regenerate it with
//
//	EPUBCHECK_UPDATE=1 go test -run TestEpubcheckGolden ./internal/check
//
// which needs epubcheck on PATH. With EPUBCHECK_VERIFY=1 the same test checks
// the recorded results against a live epubcheck without rewriting them.

import (
	"archive/zip"
	"bytes"
	"hash/crc32"
	"image"
	"image/jpeg"
	"strings"
	"testing"
)

// file is one entry of a test EPUB.
type file struct {
	name   string
	data   []byte
	method uint16
	extra  []byte // raw ZIP extra field
}

// book is an editable EPUB: a list of entries, written in order.
type book []file

func (b book) get(name string) []byte {
	for _, f := range b {
		if f.name == name {
			return f.data
		}
	}
	return nil
}

func (b book) set(name string, data []byte) book {
	out := append(book(nil), b...)
	for i, f := range out {
		if f.name == name {
			out[i].data = data
			return out
		}
	}
	return append(out, file{name: name, data: data, method: zip.Deflate})
}

func (b book) edit(name, old, new string) book {
	d := b.get(name)
	if !bytes.Contains(d, []byte(old)) {
		panic("corpus: " + name + " does not contain " + old)
	}
	return b.set(name, bytes.Replace(d, []byte(old), []byte(new), 1))
}

func (b book) remove(name string) book {
	var out book
	for _, f := range b {
		if f.name != name {
			out = append(out, f)
		}
	}
	return out
}

func (b book) rename(old, new string) book {
	out := append(book(nil), b...)
	for i, f := range out {
		if f.name == old {
			out[i].name = new
		}
	}
	return out
}

func (b book) zip() []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range b {
		h := &zip.FileHeader{Name: f.name, Method: f.method, Extra: f.extra}
		if f.method == zip.Store || f.name == "mimetype" {
			// Raw entries with sizes up front: no data descriptor.
			h.CRC32 = crc32.ChecksumIEEE(f.data)
			h.CompressedSize64, h.UncompressedSize64 = uint64(len(f.data)), uint64(len(f.data))
			if f.method == zip.Deflate { // compressed mimetype case
				w, _ := zw.CreateHeader(&zip.FileHeader{Name: f.name, Method: zip.Deflate, Extra: f.extra})
				w.Write(f.data)
				continue
			}
			w, _ := zw.CreateRaw(h)
			w.Write(f.data)
			continue
		}
		w, _ := zw.CreateHeader(h)
		w.Write(f.data)
	}
	zw.Close()
	return buf.Bytes()
}

func tinyJPEG(w, h int) []byte {
	var b bytes.Buffer
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = uint8(128 + i%64)
	}
	jpeg.Encode(&b, img, nil)
	return b.Bytes()
}

const (
	containerXML = `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>
`
	pageCSS = `html, body { margin: 0; padding: 0; width: 64px; height: 48px; }
img { display: block; width: 64px; height: 48px; }
`
	contentOPF = `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="book-id" prefix="rendition: http://www.idpf.org/vocab/rendition/#">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:identifier id="book-id">urn:uuid:5c1f1f2a-3b4d-4e5f-8a9b-0c1d2e3f4a5b</dc:identifier>
    <dc:title>Test Book</dc:title>
    <dc:language>en</dc:language>
    <meta property="dcterms:modified">2026-01-02T03:04:05Z</meta>
    <meta property="rendition:layout">pre-paginated</meta>
    <meta property="rendition:orientation">landscape</meta>
    <meta property="rendition:spread">none</meta>
  </metadata>
  <manifest>
    <item id="img-page-001" href="images/page-001.jpg" media-type="image/jpeg" properties="cover-image"/>
    <item id="page-001" href="text/page-001.xhtml" media-type="application/xhtml+xml"/>
    <item id="img-page-002" href="images/page-002.jpg" media-type="image/jpeg"/>
    <item id="page-002" href="text/page-002.xhtml" media-type="application/xhtml+xml"/>
    <item id="css" href="text/page.css" media-type="text/css"/>
    <item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
  </manifest>
  <spine page-progression-direction="ltr">
    <itemref idref="page-001" properties="rendition:layout-pre-paginated"/>
    <itemref idref="page-002" properties="rendition:layout-pre-paginated"/>
  </spine>
</package>
`
	navXHTML = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" xml:lang="en" lang="en">
<head><title>Test Book</title></head>
<body>
<nav epub:type="toc" id="toc">
<h1>Contents</h1>
<ol>
<li><a href="text/page-001.xhtml">1</a></li>
<li><a href="text/page-002.xhtml">2</a></li>
</ol>
</nav>
</body>
</html>
`
)

func pageXHTML(n int) string {
	return strings.NewReplacer("NNN", []string{"", "001", "002"}[n]).Replace(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" xml:lang="en" lang="en">
<head>
<title>Page NNN</title>
<meta name="viewport" content="width=64, height=48"/>
<link rel="stylesheet" type="text/css" href="page.css"/>
</head>
<body>
<div><img src="../images/page-NNN.jpg" width="64" height="48" alt="Page NNN"/></div>
</body>
</html>
`)
}

// validBook is a small, valid fixed-layout EPUB in the shape Leafbind writes.
func validBook() book {
	return book{
		{name: "mimetype", data: []byte("application/epub+zip"), method: zip.Store},
		{name: "META-INF/container.xml", data: []byte(containerXML), method: zip.Deflate},
		{name: "OEBPS/content.opf", data: []byte(contentOPF), method: zip.Deflate},
		{name: "OEBPS/nav.xhtml", data: []byte(navXHTML), method: zip.Deflate},
		{name: "OEBPS/text/page.css", data: []byte(pageCSS), method: zip.Deflate},
		{name: "OEBPS/text/page-001.xhtml", data: []byte(pageXHTML(1)), method: zip.Deflate},
		{name: "OEBPS/text/page-002.xhtml", data: []byte(pageXHTML(2)), method: zip.Deflate},
		{name: "OEBPS/images/page-001.jpg", data: tinyJPEG(64, 48), method: zip.Store},
		{name: "OEBPS/images/page-002.jpg", data: tinyJPEG(64, 48), method: zip.Store},
	}
}

const opf = "OEBPS/content.opf"

// body inserts markup at the start of page 2's body.
func body(b book, markup string) book {
	return b.edit("OEBPS/text/page-002.xhtml", "<div>", markup+"<div>")
}

// corpusCase is one EPUB of the corpus: an edited book, or raw bytes.
type corpusCase struct {
	name string
	make func() book
	raw  func() []byte
}

func (c corpusCase) data() []byte {
	if c.raw != nil {
		return c.raw()
	}
	return c.make().zip()
}

func corpus() []corpusCase {
	v := validBook
	moveFirst := func(b book, name string) book {
		var first file
		var rest book
		for _, f := range b {
			if f.name == name {
				first = f
			} else {
				rest = append(rest, f)
			}
		}
		return append(book{first}, rest...)
	}
	return []corpusCase{
		{"valid", v, nil},
		{name: "not-a-zip", raw: func() []byte { return []byte("This is not an EPUB, just text.") }},

		// OCF container
		{name: "mimetype-not-first", make: func() book { return moveFirst(v(), "META-INF/container.xml") }},
		{name: "mimetype-compressed", make: func() book { b := v(); b[0].method = zip.Deflate; return b }},
		{name: "mimetype-wrong-content", make: func() book { return v().set("mimetype", []byte("application/epub")) }},
		{name: "mimetype-extra-field", make: func() book { b := v(); b[0].extra = []byte{0xfe, 0xca, 0, 0}; return b }},
		{name: "mimetype-missing", make: func() book { return v().remove("mimetype") }},
		{name: "container-missing", make: func() book { return v().remove("META-INF/container.xml") }},
		{name: "container-no-rootfile", make: func() book {
			return v().edit("META-INF/container.xml", `<rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>`, "")
		}},
		{name: "container-rootfile-missing", make: func() book {
			return v().edit("META-INF/container.xml", `full-path="OEBPS/content.opf"`, `full-path="OEBPS/package.opf"`)
		}},
		{name: "container-malformed", make: func() book { return v().edit("META-INF/container.xml", "</container>", "") }},
		{name: "filename-space", make: func() book {
			return v().rename("OEBPS/images/page-002.jpg", "OEBPS/images/page 002.jpg").
				edit(opf, `href="images/page-002.jpg"`, `href="images/page%20002.jpg"`).
				edit("OEBPS/text/page-002.xhtml", `src="../images/page-002.jpg"`, `src="../images/page%20002.jpg"`)
		}},

		// Package document: metadata
		{name: "opf-malformed", make: func() book { return v().edit(opf, "</package>", "") }},
		{name: "identifier-missing", make: func() book {
			return v().edit(opf, `<dc:identifier id="book-id">urn:uuid:5c1f1f2a-3b4d-4e5f-8a9b-0c1d2e3f4a5b</dc:identifier>`, "")
		}},
		{name: "unique-identifier-mismatch", make: func() book { return v().edit(opf, `unique-identifier="book-id"`, `unique-identifier="other-id"`) }},
		{name: "identifier-empty", make: func() book {
			return v().edit(opf, `>urn:uuid:5c1f1f2a-3b4d-4e5f-8a9b-0c1d2e3f4a5b<`, `><`)
		}},
		{name: "title-missing", make: func() book { return v().edit(opf, "<dc:title>Test Book</dc:title>", "") }},
		{name: "title-empty", make: func() book { return v().edit(opf, "<dc:title>Test Book</dc:title>", "<dc:title></dc:title>") }},
		{name: "language-missing", make: func() book { return v().edit(opf, "<dc:language>en</dc:language>", "") }},
		{name: "language-invalid", make: func() book {
			return v().edit(opf, "<dc:language>en</dc:language>", "<dc:language>not a language</dc:language>")
		}},
		{name: "modified-missing", make: func() book { return v().edit(opf, `<meta property="dcterms:modified">2026-01-02T03:04:05Z</meta>`, "") }},
		{name: "modified-bad-format", make: func() book { return v().edit(opf, ">2026-01-02T03:04:05Z<", ">2026-01-02<") }},
		{name: "modified-whitespace", make: func() book { return v().edit(opf, ">2026-01-02T03:04:05Z<", ">\n      2026-01-02T03:04:05Z\n    <") }},
		{name: "layout-whitespace", make: func() book { return v().edit(opf, ">pre-paginated<", ">\n      pre-paginated\n    <") }},
		{name: "layout-invalid", make: func() book { return v().edit(opf, ">pre-paginated<", ">fixed<") }},
		{name: "orientation-invalid", make: func() book { return v().edit(opf, ">landscape<", ">sideways<") }},
		{name: "spread-invalid", make: func() book {
			return v().edit(opf, `<meta property="rendition:spread">none</meta>`, `<meta property="rendition:spread">sometimes</meta>`)
		}},
		{name: "layout-twice", make: func() book {
			return v().edit(opf, `<meta property="rendition:layout">pre-paginated</meta>`,
				`<meta property="rendition:layout">pre-paginated</meta><meta property="rendition:layout">pre-paginated</meta>`)
		}},

		// Package document: manifest
		{name: "manifest-file-missing", make: func() book { return v().remove("OEBPS/images/page-002.jpg") }},
		{name: "file-not-in-manifest", make: func() book { return v().set("OEBPS/extra.txt", []byte("stray")) }},
		{name: "manifest-duplicate-id", make: func() book { return v().edit(opf, `id="img-page-002"`, `id="img-page-001"`) }},
		{name: "manifest-duplicate-href", make: func() book {
			return v().edit(opf, `<item id="css" href="text/page.css" media-type="text/css"/>`,
				`<item id="css" href="text/page.css" media-type="text/css"/><item id="css2" href="text/page.css" media-type="text/css"/>`)
		}},
		{name: "media-type-mismatch", make: func() book {
			return v().edit(opf, `href="images/page-002.jpg" media-type="image/jpeg"`, `href="images/page-002.jpg" media-type="image/png"`)
		}},
		{name: "nav-property-missing", make: func() book { return v().edit(opf, ` properties="nav"`, "") }},
		{name: "nav-property-twice", make: func() book {
			return v().edit(opf, `<item id="page-002" href="text/page-002.xhtml" media-type="application/xhtml+xml"/>`, `<item id="page-002" href="text/page-002.xhtml" media-type="application/xhtml+xml" properties="nav"/>`)
		}},
		{name: "cover-image-twice", make: func() book {
			return v().edit(opf, `href="images/page-002.jpg" media-type="image/jpeg"`, `href="images/page-002.jpg" media-type="image/jpeg" properties="cover-image"`)
		}},
		{name: "cover-image-not-image", make: func() book {
			return v().edit(opf, ` properties="cover-image"`, "").
				edit(opf, `<item id="css" href="text/page.css" media-type="text/css"/>`, `<item id="css" href="text/page.css" media-type="text/css" properties="cover-image"/>`)
		}},
		{name: "item-property-unknown", make: func() book {
			return v().edit(opf, `<item id="css" href="text/page.css" media-type="text/css"/>`, `<item id="css" href="text/page.css" media-type="text/css" properties="shiny"/>`)
		}},

		// Package document: spine
		{name: "spine-unknown-idref", make: func() book { return v().edit(opf, `<itemref idref="page-002"`, `<itemref idref="page-003"`) }},
		{name: "spine-empty", make: func() book {
			return v().edit(opf, `<itemref idref="page-001" properties="rendition:layout-pre-paginated"/>`, "").
				edit(opf, `<itemref idref="page-002" properties="rendition:layout-pre-paginated"/>`, "")
		}},
		{name: "spine-image-item", make: func() book { return v().edit(opf, `<itemref idref="page-002"`, `<itemref idref="img-page-002"`) }},
		{name: "spine-duplicate-itemref", make: func() book { return v().edit(opf, `<itemref idref="page-002"`, `<itemref idref="page-001"`) }},
		{name: "spine-bad-direction", make: func() book {
			return v().edit(opf, `page-progression-direction="ltr"`, `page-progression-direction="up"`)
		}},

		// Content documents
		{name: "xhtml-malformed", make: func() book { return v().edit("OEBPS/text/page-002.xhtml", "</body>", "") }},
		{name: "xhtml-viewport-missing", make: func() book {
			return v().edit("OEBPS/text/page-002.xhtml", `<meta name="viewport" content="width=64, height=48"/>`, "")
		}},
		{name: "xhtml-viewport-invalid", make: func() book {
			return v().edit("OEBPS/text/page-002.xhtml", `content="width=64, height=48"`, `content="width=wide, height=48"`)
		}},
		{name: "xhtml-image-missing", make: func() book {
			return v().edit("OEBPS/text/page-002.xhtml", `src="../images/page-002.jpg"`, `src="../images/page-009.jpg"`)
		}},
		{name: "xhtml-title-missing", make: func() book { return v().edit("OEBPS/text/page-002.xhtml", "<title>Page 002</title>", "") }},
		{name: "xhtml-unknown-element", make: func() book {
			return v().edit("OEBPS/text/page-002.xhtml", "<div>", "<blink><div>").edit("OEBPS/text/page-002.xhtml", "</div>", "</div></blink>")
		}},
		{name: "xhtml-stylesheet-missing", make: func() book {
			return v().edit("OEBPS/text/page-002.xhtml", `href="page.css"`, `href="missing.css"`)
		}},

		// Navigation document
		{name: "nav-toc-missing", make: func() book { return v().edit("OEBPS/nav.xhtml", `epub:type="toc"`, `epub:type="landmarks"`) }},
		{name: "nav-link-broken", make: func() book {
			return v().edit("OEBPS/nav.xhtml", `href="text/page-002.xhtml"`, `href="text/page-009.xhtml"`)
		}},

		// Patterns found in the W3C/IDPF sample EPUBs
		{name: "itemref-reflowable-override", make: func() book {
			return v().edit(opf, `<itemref idref="page-002" properties="rendition:layout-pre-paginated"/>`, `<itemref idref="page-002" properties="rendition:layout-reflowable"/>`).
				edit("OEBPS/text/page-002.xhtml", `<meta name="viewport" content="width=64, height=48"/>`, "")
		}},
		{name: "spine-image-with-fallback", make: func() book {
			return v().edit(opf, `href="images/page-002.jpg" media-type="image/jpeg"`, `href="images/page-002.jpg" media-type="image/jpeg" fallback="page-002"`).
				edit(opf, `<itemref idref="page-002"`, `<itemref idref="img-page-002"`)
		}},
		{name: "spine-image-fallback-no-viewport", make: func() book {
			return v().edit(opf, `href="images/page-002.jpg" media-type="image/jpeg"`, `href="images/page-002.jpg" media-type="image/jpeg" fallback="page-002"`).
				edit(opf, `<itemref idref="page-002"`, `<itemref idref="img-page-002"`).
				edit("OEBPS/text/page-002.xhtml", `<meta name="viewport" content="width=64, height=48"/>`, "")
		}},
		{name: "link-path-absolute", make: func() book {
			return v().edit("OEBPS/text/page-002.xhtml", "<div>", `<div><a href="/wiki/About">About</a>`)
		}},
		{name: "link-cfi-fragment", make: func() book {
			return v().edit("OEBPS/nav.xhtml", `href="text/page-002.xhtml"`, `href="text/page-002.xhtml#epubcfi(/4[body]/2)"`)
		}},
		{name: "xhtml-epub-switch", make: func() book {
			return v().edit("OEBPS/text/page-002.xhtml", "<div>",
				`<div><epub:switch id="sw"><epub:case required-namespace="http://www.w3.org/1998/Math/MathML"><span>m</span></epub:case><epub:default><span>d</span></epub:default></epub:switch>`)
		}},
		{name: "prefix-maps-dublin-core", make: func() book {
			return v().edit(opf, `prefix="rendition: http://www.idpf.org/vocab/rendition/#"`,
				`prefix="rendition: http://www.idpf.org/vocab/rendition/# dc: http://purl.org/dc/elements/1.1/"`)
		}},
		{name: "metadata-xsd-prefix", make: func() book {
			return v().edit(opf, `<dc:title>Test Book</dc:title>`, `<dc:title>Test Book</dc:title><meta refines="#book-id" property="identifier-type" scheme="xsd:string">15</meta>`)
		}},

		{name: "second-rendition-invalid", make: func() book {
			second := strings.Replace(contentOPF, "<dc:language>en</dc:language>", "<dc:language>not a language</dc:language>", 1)
			return v().edit("META-INF/container.xml", `</rootfiles>`,
				`  <rootfile full-path="OEBPS/second.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>`).set("OEBPS/second.opf", []byte(second))
		}},

		// HTML content models and attributes (the XHTML schema)
		{name: "html-heading-in-paragraph", make: func() book { return body(v(), `<p>Intro <h2>Heading</h2></p>`) }},
		{name: "html-block-in-inline", make: func() book { return body(v(), `<span><div>block</div></span>`) }},
		{name: "html-li-outside-list", make: func() book { return body(v(), `<li>stray</li>`) }},
		{name: "html-text-in-list", make: func() book { return body(v(), `<ul>loose text<li>item</li></ul>`) }},
		{name: "html-img-without-src", make: func() book { return body(v(), `<img alt="x"/>`) }},
		{name: "html-bad-attribute-value", make: func() book { return body(v(), `<p dir="sideways">x</p>`) }},
		{name: "html-unknown-attribute", make: func() book { return body(v(), `<p shiny="yes">x</p>`) }},
		{name: "html-table-without-tbody", make: func() book { return body(v(), `<table><tr><td>1</td></tr></table>`) }},
		{name: "html-td-outside-row", make: func() book { return body(v(), `<table><td>1</td></table>`) }},
		{name: "html-dt-outside-dl", make: func() book { return body(v(), `<dt>term</dt>`) }},
		{name: "html-figcaption-in-middle", make: func() book {
			return body(v(), `<figure><p>a</p><figcaption>c</figcaption><p>b</p></figure>`)
		}},
		{name: "html-mathml-inline", make: func() book {
			return body(v(), `<p><math xmlns="http://www.w3.org/1998/Math/MathML"><mi>x</mi></math></p>`).
				edit(opf, `<item id="page-002" href="text/page-002.xhtml" media-type="application/xhtml+xml"/>`,
					`<item id="page-002" href="text/page-002.xhtml" media-type="application/xhtml+xml" properties="mathml"/>`)
		}},
		{name: "html-mathml-invalid", make: func() book {
			return body(v(), `<p><math xmlns="http://www.w3.org/1998/Math/MathML"><blink>x</blink></math></p>`).
				edit(opf, `<item id="page-002" href="text/page-002.xhtml" media-type="application/xhtml+xml"/>`,
					`<item id="page-002" href="text/page-002.xhtml" media-type="application/xhtml+xml" properties="mathml"/>`)
		}},

		// Rules EPUBCheck states in Schematron rather than RELAX NG
		{name: "html-link-in-link", make: func() book { return body(v(), `<p><a href="page-001.xhtml">a <a href="page-001.xhtml">b</a></a></p>`) }},
		{name: "html-button-in-link", make: func() book { return body(v(), `<p><a href="page-001.xhtml"><button>b</button></a></p>`) }},
		{name: "html-header-in-header", make: func() book { return body(v(), `<header><header>h</header></header>`) }},
		{name: "html-form-in-form", make: func() book { return body(v(), `<form><form><p>x</p></form></form>`) }},
		{name: "html-label-in-label", make: func() book { return body(v(), `<p><label>a <label>b</label></label></p>`) }},
		{name: "html-bdo-without-dir", make: func() book { return body(v(), `<p><bdo>x</bdo></p>`) }},
		{name: "html-title-empty", make: func() book {
			return v().edit("OEBPS/text/page-002.xhtml", "<title>Page 002</title>", "<title></title>")
		}},
		{name: "html-idref-missing", make: func() book { return body(v(), `<p aria-describedby="nowhere">x</p>`) }},
		{name: "html-label-for-missing", make: func() book { return body(v(), `<p><label for="nowhere">x</label></p>`) }},
		{name: "html-area-outside-map", make: func() book { return body(v(), `<p><area href="page-001.xhtml" alt="x"/></p>`) }},

		// Other resources
		{name: "image-corrupt", make: func() book { return v().set("OEBPS/images/page-002.jpg", []byte("this is not a JPEG")) }},
		{name: "css-syntax-error", make: func() book { return v().set("OEBPS/text/page.css", []byte("html, body { margin: 0; ")) }},
	}
}

func TestCorpusIsBuildable(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range corpus() {
		if seen[c.name] {
			t.Errorf("duplicate case %q", c.name)
		}
		seen[c.name] = true
		if len(c.data()) == 0 {
			t.Errorf("%s: empty archive", c.name)
		}
	}
}

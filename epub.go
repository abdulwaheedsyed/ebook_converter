package main

// EPUB 3 fixed-layout packaging.

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"crypto/rand"
	"encoding/xml"
	"fmt"
	"hash/crc32"
	"io"
	"strconv"
	"strings"
	"time"
)

// Book is everything needed to write the package.
type Book struct {
	Title     string
	Lang      string
	Direction string // "rtl" or "ltr"
	Orient    Orientation
	Canvas    Size // normalised canvas; per-page sizes are used in mixed mode
	Mixed     bool
	Pages     []Page
	TOC       []TOCEntry // from the PDF's outline; nil lists every page
	Labels    []string   // page labels for the page list; nil numbers them
	Modified  time.Time
	ID        string // urn:uuid:...
}

// Page is one encoded page image.
type Page struct {
	JPEG   []byte
	Size   Size   // size of the encoded image
	Source Size   // size PDFium rendered
	Orient string // orientation of the source page
	Thumb  []byte // small preview JPEG; first page only, not packaged
}

// esc escapes text for both element content and attribute values;
// xml.EscapeText already escapes both quote characters.
func esc(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

func newUUID() string {
	var u [16]byte
	rand.Read(u[:])
	u[6] = u[6]&0x0f | 0x40 // version 4
	u[8] = u[8]&0x3f | 0x80 // RFC 4122 variant
	return fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}

func pageName(i, total int) string {
	return fmt.Sprintf("page-%0*d", max(3, len(fmt.Sprint(total))), i+1)
}

// writeEPUB writes the package to w.
func writeEPUB(w io.Writer, b *Book) error {
	z := zip.NewWriter(w)
	z.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(out, flate.BestCompression)
	})

	// mimetype MUST be first, stored uncompressed, and carry no extra field.
	// CreateRaw with the sizes known up front writes a plain local header with
	// no data descriptor; a zero Modified keeps the extended-timestamp extra
	// field out.
	mt := []byte("application/epub+zip")
	h := &zip.FileHeader{
		Name:               "mimetype",
		Method:             zip.Store,
		CRC32:              crc32.ChecksumIEEE(mt),
		CompressedSize64:   uint64(len(mt)),
		UncompressedSize64: uint64(len(mt)),
	}
	f, err := z.CreateRaw(h)
	if err != nil {
		return err
	}
	if _, err := f.Write(mt); err != nil {
		return err
	}

	add := func(name string, data []byte, method uint16) error {
		f, err := z.CreateHeader(&zip.FileHeader{Name: name, Method: method, Modified: b.Modified})
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		return err
	}

	if err := add("META-INF/container.xml", []byte(containerXML), zip.Deflate); err != nil {
		return err
	}

	total := len(b.Pages)
	var manifest, spine strings.Builder

	if !b.Mixed {
		if err := add("OEBPS/text/page.css", pageCSS(b.Canvas), zip.Deflate); err != nil {
			return err
		}
	}

	for i, p := range b.Pages {
		name := pageName(i, total)

		// JPEG is already compressed; deflating it again only costs time.
		if err := add("OEBPS/images/"+name+".jpg", p.JPEG, zip.Store); err != nil {
			return err
		}

		css := "page.css"
		if b.Mixed {
			css = name + ".css"
			if err := add("OEBPS/text/"+css, pageCSS(p.Size), zip.Deflate); err != nil {
				return err
			}
		}
		if err := add("OEBPS/text/"+name+".xhtml", pageXHTML(b, i, p.Size, css), zip.Deflate); err != nil {
			return err
		}

		props := ""
		if i == 0 {
			props = ` properties="cover-image"`
		}
		fmt.Fprintf(&manifest, "        <item id=\"img-%s\" href=\"images/%s.jpg\" media-type=\"image/jpeg\"%s/>\n", name, name, props)
		fmt.Fprintf(&manifest, "        <item id=\"%s\" href=\"text/%s.xhtml\" media-type=\"application/xhtml+xml\"/>\n", name, name)
		if b.Mixed {
			fmt.Fprintf(&manifest, "        <item id=\"css-%s\" href=\"text/%s\" media-type=\"text/css\"/>\n", name, css)
		}

		// Every itemref carries rendition:layout-pre-paginated so a reader that
		// ignores the package-level declaration still keeps the page fixed.
		orient := b.Orient.Rendition
		if b.Mixed {
			orient = map[string]string{"square": "auto"}[p.Orient]
			if orient == "" {
				orient = p.Orient
			}
		}
		fmt.Fprintf(&spine, "        <itemref idref=\"%s\" properties=\"rendition:layout-pre-paginated rendition:orientation-%s rendition:spread-none\"/>\n", name, orient)
	}

	if !b.Mixed {
		manifest.WriteString("        <item id=\"css-page\" href=\"text/page.css\" media-type=\"text/css\"/>\n")
	}
	manifest.WriteString("        <item id=\"nav\" href=\"nav.xhtml\" media-type=\"application/xhtml+xml\" properties=\"nav\"/>\n")

	if err := add("OEBPS/nav.xhtml", navXHTML(b), zip.Deflate); err != nil {
		return err
	}
	if err := add("OEBPS/content.opf", contentOPF(b, manifest.String(), spine.String()), zip.Deflate); err != nil {
		return err
	}
	return z.Close()
}

const containerXML = `<?xml version="1.0" encoding="UTF-8"?>
<container
    version="1.0"
    xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
    <rootfiles>
        <rootfile
            full-path="OEBPS/content.opf"
            media-type="application/oebps-package+xml"/>
    </rootfiles>
</container>
`

// pageCSS places the image at an exact pixel size inside a viewport of the
// same exact size. There are no percentages and no object-fit: fill (which
// stretches), so the only scaling is the device fitting the whole viewport to
// the screen, which preserves aspect ratio.
func pageCSS(s Size) []byte {
	return fmt.Appendf(nil, `html, body {
    margin: 0;
    padding: 0;
    width: %[1]dpx;
    height: %[2]dpx;
    overflow: hidden;
    background: #ffffff;
}

.page {
    position: absolute;
    top: 0;
    left: 0;
    width: %[1]dpx;
    height: %[2]dpx;
    margin: 0;
    padding: 0;
}

.page img {
    position: absolute;
    top: 0;
    left: 0;
    width: %[1]dpx;
    height: %[2]dpx;
    margin: 0;
    padding: 0;
}
`, s.W, s.H)
}

func pageXHTML(b *Book, i int, s Size, css string) []byte {
	name := pageName(i, len(b.Pages))
	return fmt.Appendf(nil, `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html
    xmlns="http://www.w3.org/1999/xhtml"
    xmlns:epub="http://www.idpf.org/2007/ops"
    xml:lang="%[1]s"
    lang="%[1]s"
    dir="%[2]s">
<head>
    <title>%[3]s %[4]d</title>
    <meta name="viewport" content="width=%[5]d, height=%[6]d"/>
    <link rel="stylesheet" type="text/css" href="%[7]s"/>
</head>
<body dir="%[2]s">
<div class="page">
    <img
        src="../images/%[8]s.jpg"
        width="%[5]d"
        height="%[6]d"
        alt="%[4]d"/>
</div>
</body>
</html>
`, esc(b.Lang), b.Direction, esc(b.Title), i+1, s.W, s.H, css, name)
}

func navXHTML(b *Book) []byte {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html
    xmlns="http://www.w3.org/1999/xhtml"
    xmlns:epub="http://www.idpf.org/2007/ops"
    xml:lang="%[1]s"
    lang="%[1]s"
    dir="%[2]s">
<head>
    <title>%[3]s</title>
</head>
<body dir="%[2]s">
<nav epub:type="toc" id="toc">
<h1>%[3]s</h1>
<ol>
`, esc(b.Lang), b.Direction, esc(b.Title))
	href := func(i int) string { return "text/" + pageName(i, len(b.Pages)) + ".xhtml" }
	if len(b.TOC) > 0 {
		var list func(entries []TOCEntry)
		list = func(entries []TOCEntry) {
			for _, e := range entries {
				fmt.Fprintf(&buf, "<li><a href=\"%s\">%s</a>", href(e.Page), esc(e.Title))
				if len(e.Children) > 0 {
					buf.WriteString("\n<ol>\n")
					list(e.Children)
					buf.WriteString("</ol>\n")
				}
				buf.WriteString("</li>\n")
			}
		}
		list(b.TOC)
	} else {
		for i := range b.Pages {
			fmt.Fprintf(&buf, "<li><a href=\"%s\">%d</a></li>\n", href(i), i+1)
		}
	}
	buf.WriteString("</ol>\n</nav>\n")

	// The page list lets a reader go to a page by its number, printed
	// labels included.
	buf.WriteString("<nav epub:type=\"page-list\" id=\"page-list\" hidden=\"hidden\">\n<ol>\n")
	for i := range b.Pages {
		label := strconv.Itoa(i + 1)
		if b.Labels != nil {
			label = b.Labels[i]
		}
		fmt.Fprintf(&buf, "<li><a href=\"%s\">%s</a></li>\n", href(i), esc(label))
	}
	buf.WriteString("</ol>\n</nav>\n</body>\n</html>\n")
	return buf.Bytes()
}

// contentOPF writes the package document.
//
// rendition:* values are compared as exact strings. A value written across
// several lines becomes "\n  pre-paginated\n", which does not match, and the
// book silently falls back to reflowable layout -- which is what makes Kindle
// split and re-flow a fixed page. Keep every value on one line.
func contentOPF(b *Book, manifest, spine string) []byte {
	mode := "horizontal-lr"
	if b.Direction == "rtl" {
		mode = "horizontal-rl"
	}
	res := b.Canvas
	if b.Mixed && len(b.Pages) > 0 {
		res = b.Pages[0].Size
	}
	return fmt.Appendf(nil, `<?xml version="1.0" encoding="UTF-8"?>
<package
    xmlns="http://www.idpf.org/2007/opf"
    version="3.0"
    unique-identifier="book-id"
    prefix="rendition: http://www.idpf.org/vocab/rendition/#">

    <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">

        <dc:identifier id="book-id">%[1]s</dc:identifier>
        <dc:title>%[2]s</dc:title>
        <dc:language>%[3]s</dc:language>
        <meta property="dcterms:modified">%[4]s</meta>

        <!-- EPUB 3 fixed layout -->

        <meta property="rendition:layout">pre-paginated</meta>
        <meta property="rendition:orientation">%[5]s</meta>
        <meta property="rendition:spread">none</meta>

        <!-- Kindle fixed layout -->

        <meta name="fixed-layout" content="true"/>
        <meta name="original-resolution" content="%[6]dx%[7]d"/>
        <meta name="orientation-lock" content="%[8]s"/>
        <meta name="zero-gutter" content="true"/>
        <meta name="zero-margin" content="true"/>
        <meta name="RegionMagnification" content="false"/>
        <meta name="primary-writing-mode" content="%[9]s"/>

        <!-- Kindle cover fallback -->

        <meta name="cover" content="img-%[10]s"/>

    </metadata>

    <manifest>
%[11]s    </manifest>

    <spine page-progression-direction="%[12]s">
%[13]s    </spine>

</package>
`, esc(b.ID), esc(b.Title), esc(b.Lang), b.Modified.UTC().Format("2006-01-02T15:04:05Z"),
		b.Orient.Rendition, res.W, res.H, b.Orient.Lock, mode,
		pageName(0, len(b.Pages)), manifest, b.Direction, spine)
}

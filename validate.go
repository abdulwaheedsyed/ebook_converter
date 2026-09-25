package main

// Validation of the finished package.
//
// epubcheck is Java and cannot be compiled in, so the checks that matter for
// this pipeline are done here directly: the ZIP layout, well-formed XML, the
// package metadata, the manifest and spine, and every page image. When an
// external epubcheck is on PATH it is run as well, as an independent opinion.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"image/color"
	"image/jpeg"
	"io"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// Problem is one validation finding.
type Problem struct {
	Code string
	Msg  string
}

func (p Problem) String() string { return p.Code + ": " + p.Msg }

// Expect is what the validator checks the package against.
type Expect struct {
	Pages     int
	Canvas    *Size // nil in mixed mode
	Grayscale bool
}

var (
	// BCP 47 well-formedness, simplified: a primary language subtag followed by
	// alphanumeric subtags, or a private-use tag.
	langRE     = regexp.MustCompile(`^(?:[A-Za-z]{2,3}|[A-Za-z]{5,8})(?:-[A-Za-z0-9]{1,8})*$|^[xX](?:-[A-Za-z0-9]{1,8})+$`)
	modifiedRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`)
	viewportRE = regexp.MustCompile(`^\s*width\s*=\s*(\d+)\s*,\s*height\s*=\s*(\d+)\s*$`)
)

func validLang(s string) bool { return langRE.MatchString(s) }

type opfMeta struct {
	Property string `xml:"property,attr"`
	Name     string `xml:"name,attr"`
	Content  string `xml:"content,attr"`
	Value    string `xml:",chardata"`
}

type opfItem struct {
	ID         string `xml:"id,attr"`
	Href       string `xml:"href,attr"`
	MediaType  string `xml:"media-type,attr"`
	Properties string `xml:"properties,attr"`
}

type opfPackage struct {
	UniqueID string `xml:"unique-identifier,attr"`
	Metadata struct {
		Identifiers []struct {
			ID    string `xml:"id,attr"`
			Value string `xml:",chardata"`
		} `xml:"http://purl.org/dc/elements/1.1/ identifier"`
		Titles    []string  `xml:"http://purl.org/dc/elements/1.1/ title"`
		Languages []string  `xml:"http://purl.org/dc/elements/1.1/ language"`
		Metas     []opfMeta `xml:"meta"`
	} `xml:"metadata"`
	Items []opfItem `xml:"manifest>item"`
	Spine []struct {
		IDRef string `xml:"idref,attr"`
	} `xml:"spine>itemref"`
}

func (p *opfPackage) meta(property string) (string, bool) {
	for _, m := range p.Metadata.Metas {
		if m.Property == property {
			return m.Value, true
		}
	}
	return "", false
}

func (p *opfPackage) named(name string) (string, bool) {
	for _, m := range p.Metadata.Metas {
		if m.Name == name {
			return m.Content, true
		}
	}
	return "", false
}

// wellFormed reports the first XML syntax error in data, if any.
func wellFormed(data []byte) error {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = true
	for {
		if _, err := d.Token(); err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}
	}
}

// viewport returns the size declared by a page's viewport meta and the image
// it shows.
func viewport(data []byte) (Size, string, error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	var vp Size
	var src string
	found := false
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		} else if err != nil {
			return Size{}, "", err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		attr := func(n string) string {
			for _, a := range se.Attr {
				if a.Name.Local == n {
					return a.Value
				}
			}
			return ""
		}
		switch se.Name.Local {
		case "meta":
			if attr("name") == "viewport" {
				m := viewportRE.FindStringSubmatch(attr("content"))
				if m == nil {
					return Size{}, "", fmt.Errorf("unparseable viewport %q", attr("content"))
				}
				vp.W, _ = strconv.Atoi(m[1])
				vp.H, _ = strconv.Atoi(m[2])
				found = true
			}
		case "img":
			src = attr("src")
		}
	}
	if !found {
		return Size{}, "", errors.New("no viewport meta")
	}
	return vp, src, nil
}

// validatePackage checks an EPUB against the rules this pipeline relies on.
func validatePackage(data []byte, want Expect) []Problem {
	var probs []Problem
	fail := func(code, format string, args ...any) {
		probs = append(probs, Problem{code, fmt.Sprintf(format, args...)})
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		fail("ZIP-001", "not a readable ZIP archive: %v", err)
		return probs
	}

	// OCF: mimetype first, stored, no extra field, exact content.
	if len(zr.File) == 0 || zr.File[0].Name != "mimetype" {
		fail("ZIP-002", "mimetype is not the first entry in the archive")
	} else {
		mt := zr.File[0]
		if mt.Method != zip.Store {
			fail("ZIP-003", "mimetype is compressed; it must be stored")
		}
		if len(mt.Extra) != 0 {
			fail("ZIP-004", "mimetype has a %d-byte extra field; none is allowed", len(mt.Extra))
		}
		if mt.Flags&0x8 != 0 {
			fail("ZIP-005", "mimetype uses a data descriptor")
		}
		if b, err := readZip(mt); err != nil || string(b) != "application/epub+zip" {
			fail("ZIP-006", "mimetype content is not exactly application/epub+zip")
		}
	}

	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	read := func(name string) ([]byte, bool) {
		f, ok := files[name]
		if !ok {
			return nil, false
		}
		b, err := readZip(f)
		return b, err == nil
	}

	// Every XML file must be well-formed.
	for _, f := range zr.File {
		switch path.Ext(f.Name) {
		case ".xml", ".opf", ".xhtml", ".html":
			if b, ok := read(f.Name); !ok {
				fail("XML-001", "%s: unreadable", f.Name)
			} else if err := wellFormed(b); err != nil {
				fail("XML-001", "%s: not well-formed: %v", f.Name, err)
			}
		}
	}

	// container.xml names the package document.
	cont, ok := read("META-INF/container.xml")
	if !ok {
		fail("OCF-001", "META-INF/container.xml is missing")
		return probs
	}
	var c struct {
		Rootfiles []struct {
			FullPath string `xml:"full-path,attr"`
		} `xml:"rootfiles>rootfile"`
	}
	if err := xml.Unmarshal(cont, &c); err != nil || len(c.Rootfiles) == 0 {
		fail("OCF-002", "container.xml names no rootfile")
		return probs
	}
	opfPath := c.Rootfiles[0].FullPath
	opfData, ok := read(opfPath)
	if !ok {
		fail("OCF-003", "package document %s is missing", opfPath)
		return probs
	}
	var opf opfPackage
	if err := xml.Unmarshal(opfData, &opf); err != nil {
		fail("OPF-001", "package document does not parse: %v", err)
		return probs
	}
	base := path.Dir(opfPath)

	// Metadata.
	idOK := false
	for _, id := range opf.Metadata.Identifiers {
		if id.ID == opf.UniqueID && strings.TrimSpace(id.Value) != "" {
			idOK = true
		}
	}
	if !idOK {
		fail("OPF-002", "no dc:identifier matches unique-identifier %q", opf.UniqueID)
	}
	if len(opf.Metadata.Titles) == 0 || strings.TrimSpace(opf.Metadata.Titles[0]) == "" {
		fail("OPF-003", "dc:title is missing or empty")
	}
	if len(opf.Metadata.Languages) == 0 || !validLang(opf.Metadata.Languages[0]) {
		fail("OPF-004", "dc:language is missing or not a well-formed BCP 47 tag")
	}
	if v, ok := opf.meta("dcterms:modified"); !ok || !modifiedRE.MatchString(v) {
		fail("OPF-005", "dcterms:modified must be exactly CCYY-MM-DDThh:mm:ssZ, got %q", v)
	}

	// The exact-string trap: whitespace here silently makes the book reflowable.
	if v, _ := opf.meta("rendition:layout"); v != "pre-paginated" {
		fail("OPF-006", "rendition:layout is %q, not exactly \"pre-paginated\"", v)
	}
	if v, _ := opf.meta("rendition:orientation"); v != "landscape" && v != "portrait" && v != "auto" {
		fail("OPF-007", "rendition:orientation %q is not landscape, portrait or auto", v)
	}

	// Manifest.
	byID := map[string]opfItem{}
	listed := map[string]bool{}
	navs, covers := 0, 0
	for _, it := range opf.Items {
		if _, dup := byID[it.ID]; dup {
			fail("OPF-008", "duplicate manifest id %q", it.ID)
		}
		byID[it.ID] = it
		full := path.Join(base, it.Href)
		listed[full] = true
		if _, ok := files[full]; !ok {
			fail("OPF-009", "manifest item %s is not in the archive", it.Href)
		}
		for _, p := range strings.Fields(it.Properties) {
			switch p {
			case "nav":
				navs++
			case "cover-image":
				covers++
			}
		}
	}
	for name := range files {
		// The package document describes the manifest; it is not listed in it.
		if name != "mimetype" && name != opfPath && !strings.HasPrefix(name, "META-INF/") && !listed[name] {
			fail("OPF-010", "%s is in the archive but not in the manifest", name)
		}
	}
	if navs != 1 {
		fail("OPF-011", "expected exactly one nav document, found %d", navs)
	}
	if covers != 1 {
		fail("OPF-012", "expected exactly one cover-image, found %d", covers)
	}

	// Spine: one fixed page per PDF page.
	for _, ref := range opf.Spine {
		if _, ok := byID[ref.IDRef]; !ok {
			fail("OPF-013", "spine references unknown manifest id %q", ref.IDRef)
		}
	}
	if want.Pages > 0 && len(opf.Spine) != want.Pages {
		fail("PAG-001", "spine has %d pages but the PDF has %d", len(opf.Spine), want.Pages)
	}

	if want.Canvas != nil {
		wantRes := fmt.Sprintf("%dx%d", want.Canvas.W, want.Canvas.H)
		if v, _ := opf.named("original-resolution"); v != wantRes {
			fail("OPF-014", "original-resolution is %q, expected %q", v, wantRes)
		}
	}

	// Every page: viewport, image, and the two agree.
	for i, ref := range opf.Spine {
		it, ok := byID[ref.IDRef]
		if !ok {
			continue
		}
		xhtmlPath := path.Join(base, it.Href)
		page, ok := read(xhtmlPath)
		if !ok {
			continue
		}
		vp, src, err := viewport(page)
		if err != nil {
			fail("PAG-002", "page %d: %v", i+1, err)
			continue
		}
		if want.Canvas != nil && vp != *want.Canvas {
			fail("PAG-003", "page %d viewport %dx%d differs from the canvas %dx%d", i+1, vp.W, vp.H, want.Canvas.W, want.Canvas.H)
		}
		imgPath := path.Join(path.Dir(xhtmlPath), src)
		img, ok := read(imgPath)
		if !ok {
			fail("IMG-001", "page %d image %s is missing", i+1, src)
			continue
		}
		m, err := jpeg.Decode(bytes.NewReader(img))
		if err != nil {
			fail("IMG-002", "page %d image does not decode: %v", i+1, err)
			continue
		}
		if b := m.Bounds(); b.Dx() != vp.W || b.Dy() != vp.H {
			fail("IMG-003", "page %d image is %dx%d but its viewport is %dx%d", i+1, b.Dx(), b.Dy(), vp.W, vp.H)
		}
		if want.Grayscale && m.ColorModel() != color.GrayModel {
			fail("IMG-004", "page %d image is colour, expected greyscale", i+1)
		}
	}
	return probs
}

func readZip(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// Epubcheck is the outcome of the optional external check.
type Epubcheck struct {
	Ran      bool
	Passed   bool
	Summary  string   // the "Messages:" line
	Problems []string // FATAL/ERROR/WARNING lines
}

// runEpubcheck runs epubcheck when it is on PATH. A missing checker is not an
// error: it is an independent second opinion, not a dependency.
func runEpubcheck(ctx context.Context, file string) Epubcheck {
	bin, err := exec.LookPath("epubcheck")
	if err != nil {
		return Epubcheck{}
	}
	out, err := exec.CommandContext(ctx, bin, file).CombinedOutput()
	res := Epubcheck{Ran: true, Passed: err == nil}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "Messages:"):
			res.Summary = line
		case strings.HasPrefix(line, "FATAL"), strings.HasPrefix(line, "ERROR"), strings.HasPrefix(line, "WARNING"):
			res.Problems = append(res.Problems, line)
		}
	}
	return res
}

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

	"github.com/abdulwaheedsyed/leafbind/internal/check"
)

// Problem is one validation finding: an epubcheck message from the built-in
// checks, or one of Leafbind's own stricter rules, whose codes start LB-.
type Problem struct {
	Code     string
	Severity string // FATAL, ERROR or WARNING
	Path     string
	Msg      string
}

// String formats a problem the way epubcheck prints one.
func (p Problem) String() string {
	loc := p.Path
	if loc == "" {
		loc = "(container)"
	}
	return fmt.Sprintf("%s(%s): %s: %s", p.Severity, p.Code, loc, p.Msg)
}

// Fails reports whether the problem makes the book invalid. As in epubcheck,
// warnings do not.
func (p Problem) Fails() bool { return p.Severity == "ERROR" || p.Severity == "FATAL" }

// Expect is what the validator checks the package against.
type Expect struct {
	Pages     int
	Canvas    *Size // nil in mixed mode
	Grayscale bool
}

var viewportRE = regexp.MustCompile(`^\s*width\s*=\s*(\d+)\s*,\s*height\s*=\s*(\d+)\s*$`)

func validLang(s string) bool { return check.LanguageTagError(s) == "" }

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
	Metadata struct {
		Metas []opfMeta `xml:"meta"`
	} `xml:"metadata"`
	Items []opfItem `xml:"manifest>item"`
	Spine []struct {
		IDRef string `xml:"idref,attr"`
	} `xml:"spine>itemref"`
}

func (p *opfPackage) named(name string) (string, bool) {
	for _, m := range p.Metadata.Metas {
		if m.Name == name {
			return m.Content, true
		}
	}
	return "", false
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

// validatePackage checks an EPUB. It runs the built-in equivalent of
// epubcheck, then Leafbind's own rules: some stricter than epubcheck, where
// less forgiving reading systems may trip, and some that compare the book
// with the PDF it came from.
func validatePackage(data []byte, want Expect) []Problem {
	var probs []Problem
	fatal := false
	for _, m := range check.EPUB(data) {
		if m.Severity < check.Warning {
			continue // usage and informational notes, which epubcheck only prints on request
		}
		probs = append(probs, Problem{m.ID, m.Severity.String(), m.Path, m.Text})
		fatal = fatal || m.Severity == check.Fatal
	}
	if fatal {
		return probs // the package could not be read far enough for anything else
	}
	fail := func(code, path, format string, args ...any) {
		probs = append(probs, Problem{code, "ERROR", path, fmt.Sprintf(format, args...)})
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return probs
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

	// The container specification requires what epubcheck does not check.
	if mt := zr.File[0]; mt.Name == "mimetype" {
		if mt.Method != zip.Store {
			fail("LB-001", "mimetype", "the mimetype file is compressed; OCF requires it stored")
		}
		if mt.Flags&0x8 != 0 {
			fail("LB-002", "mimetype", "the mimetype file uses a ZIP data descriptor")
		}
	}

	const opfPath = "OEBPS/content.opf"
	opfData, ok := read(opfPath)
	if !ok {
		return probs
	}
	var opf opfPackage
	if xml.Unmarshal(opfData, &opf) != nil {
		return probs
	}

	// EPUB 3.3 asks reading systems to trim these values, and epubcheck
	// accepts surrounding whitespace, but not every parser trims.
	for _, m := range opf.Metadata.Metas {
		switch m.Property {
		case "rendition:layout", "rendition:orientation", "rendition:spread", "dcterms:modified":
			if m.Value != strings.TrimSpace(m.Value) {
				fail("LB-003", opfPath, "the %s value has surrounding whitespace", m.Property)
			}
		}
	}

	byID := map[string]opfItem{}
	listed := map[string]bool{}
	for _, it := range opf.Items {
		byID[it.ID] = it
		listed[path.Join(path.Dir(opfPath), it.Href)] = true
	}
	for name := range files {
		if name != "mimetype" && name != opfPath && !strings.HasPrefix(name, "META-INF/") && !listed[name] {
			fail("LB-004", name, "the file is in the archive but not in the manifest")
		}
	}

	if want.Pages > 0 && len(opf.Spine) != want.Pages {
		fail("LB-010", opfPath, "the spine has %d pages but the PDF has %d", len(opf.Spine), want.Pages)
	}
	if want.Canvas != nil {
		wantRes := fmt.Sprintf("%dx%d", want.Canvas.W, want.Canvas.H)
		if v, _ := opf.named("original-resolution"); v != wantRes {
			fail("LB-011", opfPath, "original-resolution is %q, expected %q", v, wantRes)
		}
	}

	// Every page: its viewport, its image, and the two agree.
	for i, ref := range opf.Spine {
		it, ok := byID[ref.IDRef]
		if !ok {
			continue
		}
		xhtmlPath := path.Join(path.Dir(opfPath), it.Href)
		page, ok := read(xhtmlPath)
		if !ok {
			continue
		}
		vp, src, err := viewport(page)
		if err != nil {
			continue // epubcheck's viewport rules have reported it
		}
		if want.Canvas != nil && vp != *want.Canvas {
			fail("LB-012", xhtmlPath, "page %d viewport %dx%d differs from the canvas %dx%d", i+1, vp.W, vp.H, want.Canvas.W, want.Canvas.H)
		}
		imgPath := path.Join(path.Dir(xhtmlPath), src)
		img, ok := read(imgPath)
		if !ok {
			continue
		}
		m, err := jpeg.Decode(bytes.NewReader(img))
		if err != nil {
			fail("LB-015", imgPath, "page %d image does not decode: %v", i+1, err)
			continue
		}
		if b := m.Bounds(); b.Dx() != vp.W || b.Dy() != vp.H {
			fail("LB-013", imgPath, "page %d image is %dx%d but its viewport is %dx%d", i+1, b.Dx(), b.Dy(), vp.W, vp.H)
		}
		if want.Grayscale && m.ColorModel() != color.GrayModel {
			fail("LB-014", imgPath, "page %d image is colour, expected greyscale", i+1)
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

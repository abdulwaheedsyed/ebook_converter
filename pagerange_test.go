package main

import (
	"bytes"
	"errors"
	"image/jpeg"
	"reflect"
	"strings"
	"testing"
)

func TestSelectPages(t *testing.T) {
	cases := []struct {
		spec string
		want []int
	}{
		{"", []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}},
		{"3", []int{2}},
		{"2-4, 7", []int{1, 2, 3, 6}},
		{"8-", []int{7, 8, 9}},
		{"-2", []int{0, 1}},
		{"5,1-2;5 2", []int{0, 1, 4}}, // PDF order, each page once
	}
	for _, c := range cases {
		spans, err := parsePages(c.spec)
		if err != nil {
			t.Fatalf("%q: %v", c.spec, err)
		}
		got, err := selectPages(spans, 10)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: got %v, %v; want %v", c.spec, got, err, c.want)
		}
	}
	for _, bad := range []string{"0", "a", "5-2", "-", "1-x", "2--3"} {
		if _, err := parsePages(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	for _, beyond := range []string{"11", "9-12"} {
		spans, _ := parsePages(beyond)
		if _, err := selectPages(spans, 10); err == nil {
			t.Errorf("%q was accepted for a 10-page PDF", beyond)
		}
	}
}

// A page range converts only those pages: the cover is the first of them,
// bookmarks to pages left out are dropped, and the page list keeps the
// PDF's page numbers.
func TestConvertPageRange(t *testing.T) {
	pages := []testPage{{W: 400, H: 600}, {W: 600, H: 400}, {W: 400, H: 600}, {W: 400, H: 600}, {W: 400, H: 600}, {W: 400, H: 600}}
	pdf := makePDFWith(pages, testPDF{Outline: []testBookmark{
		{Title: "Front", Page: 0}, {Title: "Two", Page: 1}, {Title: "Four", Page: 3}, {Title: "Five", Page: 4},
	}})
	_, data := convertBytes(t, pdf, "36", "--pages", "2-3,5")
	if got := len(pageImages(t, data)); got != 3 {
		t.Fatalf("book has %d pages, want 3", got)
	}
	nav := string(zipFile(t, data, "OEBPS/nav.xhtml"))
	toc := nav[:strings.Index(nav, `epub:type="page-list"`)]
	if strings.Contains(toc, "Front") || strings.Contains(toc, "Four") ||
		!strings.Contains(toc, `page-001.xhtml">Two</a>`) || !strings.Contains(toc, `page-003.xhtml">Five</a>`) {
		t.Errorf("toc does not follow the range:\n%s", toc)
	}
	for _, want := range []string{`page-001.xhtml">2</a>`, `page-002.xhtml">3</a>`, `page-003.xhtml">5</a>`} {
		if !strings.Contains(nav, want) {
			t.Errorf("page list is missing %s", want)
		}
	}
	// The cover is PDF page 2, the landscape one.
	if opf := string(zipFile(t, data, "OEBPS/content.opf")); !strings.Contains(opf, `properties="cover-image"`) {
		t.Error("no cover image")
	}
}

func TestConvertEncrypted(t *testing.T) {
	if testing.Short() {
		t.Skip("starts the PDF engine; skipped with -short")
	}
	pdf := makePDFWith([]testPage{{W: 400, H: 600, Bar: true}, {W: 400, H: 600}}, testPDF{Password: "s3cret"})
	for _, c := range []struct {
		password string
		want     error
	}{{"", errPasswordNeeded}, {"wrong", errPasswordWrong}} {
		_, err := convertErr(t, pdf, "--password", c.password)
		if !errors.Is(err, c.want) {
			t.Errorf("password %q: got %v, want %v", c.password, err, c.want)
		}
	}
	_, data := convertBytes(t, pdf, "36", "--password", "s3cret")
	if got := len(pageImages(t, data)); got != 2 {
		t.Errorf("book has %d pages, want 2", got)
	}
	// The first page's black bar survives decryption.
	m, err := jpeg.Decode(bytes.NewReader(zipFile(t, data, "OEBPS/images/page-001.jpg")))
	if err != nil {
		t.Fatal(err)
	}
	darkest := uint32(0xffff)
	b := m.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, _, _, _ := m.At(x, y).RGBA()
			darkest = min(darkest, r)
		}
	}
	if darkest > 0x3000 {
		t.Errorf("the decrypted page is blank (darkest %#x)", darkest)
	}
}

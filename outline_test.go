package main

import (
	"archive/zip"
	"bytes"
	"reflect"
	"strings"
	"testing"
)

var fourPages = []testPage{{W: 400, H: 600}, {W: 400, H: 600}, {W: 400, H: 600}, {W: 400, H: 600}}

// The table of contents follows the PDF's bookmarks, whichever way they
// name their page, and the page list carries its page labels.
func TestOutlineTOC(t *testing.T) {
	pdf := makePDFWith(fourPages, testPDF{
		Outline: []testBookmark{
			{Title: "Preface", Page: 0},
			{Title: "Chapter 1", Page: 1, Action: true, Kids: []testBookmark{
				{Title: "Section 1.1", Page: 2},
				{Title: "A web link", Page: -1},
			}},
			{Title: "  Tabs\\tand   spaces ", Page: 3},
		},
		Labels: "0 << /S /r >> 2 << /S /D >>",
	})
	_, data := convertBytes(t, pdf, "36")
	nav := string(zipFile(t, data, "OEBPS/nav.xhtml"))
	toc := nav[strings.Index(nav, `epub:type="toc"`):strings.Index(nav, `epub:type="page-list"`)]
	for _, want := range []string{
		`<li><a href="text/page-001.xhtml">Preface</a></li>`,
		"<li><a href=\"text/page-002.xhtml\">Chapter 1</a>\n<ol>\n<li><a href=\"text/page-003.xhtml\">Section 1.1</a></li>\n</ol>\n</li>",
		`<li><a href="text/page-004.xhtml">Tabs and spaces</a></li>`,
	} {
		if !strings.Contains(toc, want) {
			t.Errorf("toc is missing %q:\n%s", want, toc)
		}
	}
	if strings.Contains(toc, "web link") {
		t.Error("a bookmark that leaves the document is in the toc")
	}
	for _, want := range []string{">i</a>", ">ii</a>", `page-003.xhtml">1</a>`, `page-004.xhtml">2</a>`} {
		if !strings.Contains(nav[strings.Index(nav, `epub:type="page-list"`):], want) {
			t.Errorf("page list is missing %q", want)
		}
	}
}

// --toc pages ignores the bookmarks.
func TestOutlineIgnored(t *testing.T) {
	pdf := makePDFWith(fourPages, testPDF{Outline: []testBookmark{{Title: "Preface", Page: 0}}})
	_, data := convertBytes(t, pdf, "36", "--toc", "pages")
	nav := string(zipFile(t, data, "OEBPS/nav.xhtml"))
	if strings.Contains(nav, "Preface") || !strings.Contains(nav, `page-004.xhtml">4</a>`) {
		t.Errorf("toc should list pages:\n%s", nav)
	}
}

// An outline that loops back on itself must not hang the converter; the
// book falls back to one entry per page.
func TestOutlineLoop(t *testing.T) {
	pdf := makePDFWith(fourPages, testPDF{
		Outline: []testBookmark{{Title: "One", Page: 0}, {Title: "Two", Page: 1}},
		Loop:    true,
	})
	_, data := convertBytes(t, pdf, "36")
	nav := string(zipFile(t, data, "OEBPS/nav.xhtml"))
	if strings.Contains(nav, ">One<") || !strings.Contains(nav, `page-003.xhtml">3</a>`) {
		t.Errorf("a looping outline should fall back to pages:\n%s", nav)
	}
}

func TestTidyOutline(t *testing.T) {
	got := tidyOutline([]TOCEntry{
		{Title: "Gone", Page: -1},
		{Title: "Part", Page: -1, Children: []TOCEntry{{Title: "Beyond", Page: 99}, {Title: "Ch\x01 1", Page: 2}}},
		{Title: " ", Page: 4},
	}, func(p int) int {
		if p < 10 {
			return p
		}
		return -1
	})
	want := []TOCEntry{
		{Title: "Part", Page: 2, Children: []TOCEntry{{Title: "Ch 1", Page: 2}}},
		{Title: "5", Page: 4},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

// The preview reads the finished book back: its pages, labels and contents.
func TestReadPreview(t *testing.T) {
	pdf := makePDFWith([]testPage{{W: 400, H: 600}, {W: 600, H: 400}, {W: 400, H: 600}}, testPDF{
		Outline: []testBookmark{{Title: "Start", Page: 0, Kids: []testBookmark{{Title: "Wide", Page: 1}}}},
		Labels:  "0 << /S /A >>",
	})
	_, data := convertBytes(t, pdf, "36", "--rtl", "--mixed")
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	b, err := readPreview(zr)
	if err != nil {
		t.Fatal(err)
	}
	if b.Direction != "rtl" || b.Title != "in" || len(b.Pages) != 3 {
		t.Fatalf("got %+v", b)
	}
	if p := b.Pages[1]; p.W <= p.H || p.Label != "B" {
		t.Errorf("page 2 is %+v, want a landscape page labelled B", p)
	}
	want := []previewEntry{{Title: "Start", Page: 0, Children: []previewEntry{{Title: "Wide", Page: 1}}}}
	if !reflect.DeepEqual(b.TOC, want) {
		t.Errorf("toc %+v, want %+v", b.TOC, want)
	}
	img, typ, err := previewImage(zr, b, 2)
	if err != nil || typ != "image/jpeg" || len(img) == 0 {
		t.Errorf("page 3 image: %d bytes, %s, %v", len(img), typ, err)
	}
}

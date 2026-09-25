package main

// The table of contents, from the PDF's outline (its bookmarks).

import (
	"errors"
	"strconv"
	"strings"
	"unicode"

	"github.com/klippa-app/go-pdfium/enums"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/responses"
)

// TOCEntry is one entry of the table of contents.
type TOCEntry struct {
	Title    string
	Page     int // index of the page it opens; -1 before tidying when it has none
	Children []TOCEntry
}

// Table of contents sources.
const (
	tocBookmarks = "bookmarks" // the PDF's outline, or one entry per page without one
	tocPages     = "pages"     // one entry per page
)

// An outline is a linked structure the PDF supplies, and PDFium leaves it to
// the caller to notice when one loops back on itself. Both limits are far
// beyond any real book; an outline that reaches them is treated as damaged.
const (
	maxOutlineEntries = 10000
	maxOutlineDepth   = 32
)

var errOutlineDamaged = errors.New("the outline is too large or loops back on itself")

// outline reads the document's bookmarks and returns them tidied for a
// table of contents: every entry has a title and a page in 0..pages-1.
// A document without bookmarks has an empty outline.
func (w *worker) outline(pages int) ([]TOCEntry, error) {
	count := 0
	var walk func(parent *references.FPDF_BOOKMARK, depth int) ([]TOCEntry, error)
	walk = func(parent *references.FPDF_BOOKMARK, depth int) ([]TOCEntry, error) {
		if depth > maxOutlineDepth {
			return nil, errOutlineDamaged
		}
		res, err := w.inst.FPDFBookmark_GetFirstChild(&requests.FPDFBookmark_GetFirstChild{Document: w.doc, Bookmark: parent})
		if err != nil {
			return nil, err
		}
		var out []TOCEntry
		for bm := res.Bookmark; bm != nil; {
			if count++; count > maxOutlineEntries {
				return nil, errOutlineDamaged
			}
			e := TOCEntry{Page: w.bookmarkPage(*bm)}
			if t, err := w.inst.FPDFBookmark_GetTitle(&requests.FPDFBookmark_GetTitle{Bookmark: *bm}); err == nil {
				e.Title = t.Title
			}
			if e.Children, err = walk(bm, depth+1); err != nil {
				return nil, err
			}
			out = append(out, e)
			next, err := w.inst.FPDFBookmark_GetNextSibling(&requests.FPDFBookmark_GetNextSibling{Document: w.doc, Bookmark: *bm})
			if err != nil {
				return nil, err
			}
			bm = next.Bookmark
		}
		return out, nil
	}
	raw, err := walk(nil, 0)
	if err != nil {
		return nil, err
	}

	// go-pdfium's FPDFBookmark_GetDest asks PDFium for the wrong thing and
	// never finds a destination, but GetBookmarks reads destinations
	// correctly. It has no guard against loops, so it is called only now
	// that the walk has shown the outline to be finite.
	if all, err := w.inst.GetBookmarks(&requests.GetBookmarks{Document: w.doc}); err == nil {
		addDestinations(raw, all.Bookmarks)
	}
	return tidyOutline(raw, pages), nil
}

// addDestinations fills in the pages of entries whose bookmark names its
// destination directly, from GetBookmarks's reading of the same outline.
func addDestinations(entries []TOCEntry, marks []responses.GetBookmarksBookmark) {
	if len(entries) != len(marks) {
		return // not the same outline; leave it alone
	}
	for i := range entries {
		if entries[i].Page < 0 && marks[i].DestInfo != nil {
			entries[i].Page = marks[i].DestInfo.PageIndex
		}
		addDestinations(entries[i].Children, marks[i].Children)
	}
}

// bookmarkPage returns the page a bookmark's go-to action opens, or -1.
func (w *worker) bookmarkPage(bm references.FPDF_BOOKMARK) int {
	a, err := w.inst.FPDFBookmark_GetAction(&requests.FPDFBookmark_GetAction{Bookmark: bm})
	if err != nil || a.Action == nil {
		return -1
	}
	t, err := w.inst.FPDFAction_GetType(&requests.FPDFAction_GetType{Action: *a.Action})
	if err != nil || t.Type != enums.FPDF_ACTION_ACTION_GOTO {
		return -1
	}
	d, err := w.inst.FPDFAction_GetDest(&requests.FPDFAction_GetDest{Document: w.doc, Action: *a.Action})
	if err != nil || d.Dest == nil {
		return -1
	}
	p, err := w.inst.FPDFDest_GetDestPageIndex(&requests.FPDFDest_GetDestPageIndex{Document: w.doc, Dest: *d.Dest})
	if err != nil {
		return -1
	}
	return p.Index
}

// tidyOutline makes an outline fit for a navigation document. An entry
// without a page in the book opens its first child's page instead, or is
// dropped when it has no children; an entry without a title is named after
// its page.
func tidyOutline(entries []TOCEntry, pages int) []TOCEntry {
	var out []TOCEntry
	for _, e := range entries {
		e.Children = tidyOutline(e.Children, pages)
		if e.Page < 0 || e.Page >= pages {
			if len(e.Children) == 0 {
				continue
			}
			e.Page = e.Children[0].Page
		}
		if e.Title = cleanTitle(e.Title); e.Title == "" {
			e.Title = strconv.Itoa(e.Page + 1)
		}
		out = append(out, e)
	}
	return out
}

// cleanTitle collapses white space and removes characters XML cannot carry,
// such as the control characters some PDF producers leave in bookmarks.
func cleanTitle(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r):
			return ' '
		case r < 0x20, r == 0xFFFE, r == 0xFFFF, r >= 0xD800 && r <= 0xDFFF, unicode.Is(unicode.Cc, r):
			return -1
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// countEntries counts the entries of an outline, at every level.
func countEntries(entries []TOCEntry) int {
	n := len(entries)
	for _, e := range entries {
		n += countEntries(e.Children)
	}
	return n
}

// pageLabels returns the page labels the PDF defines, such as "iv" for a
// preface page, or nil when it defines none.
func (w *worker) pageLabels(pages int) []string {
	labels := make([]string, pages)
	found := false
	for i := range pages {
		res, err := w.inst.FPDF_GetPageLabel(&requests.FPDF_GetPageLabel{Document: w.doc, Page: i})
		if err == nil {
			labels[i] = cleanTitle(res.Label)
		}
		if labels[i] == "" {
			labels[i] = strconv.Itoa(i + 1)
		} else {
			found = true
		}
	}
	if !found {
		return nil
	}
	return labels
}

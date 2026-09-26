package main

// Page ranges: converting part of a PDF.

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// pageSpan is one part of a page range, in 1-based page numbers. to is 0
// for a span that runs to the last page.
type pageSpan struct{ from, to int }

// parsePages reads a page range such as "1-20, 25, 30-" or "-10". Numbers
// are the PDF's own page positions, counting from 1. An empty range means
// every page.
func parsePages(spec string) ([]pageSpan, error) {
	var spans []pageSpan
	for _, part := range strings.FieldsFunc(spec, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		num := func(s string) (int, error) {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 {
				return 0, fmt.Errorf("%q is not a page number", s)
			}
			return n, nil
		}
		a, b, isSpan := strings.Cut(part, "-")
		var sp pageSpan
		var err error
		switch {
		case !isSpan:
			if sp.from, err = num(a); err != nil {
				return nil, err
			}
			sp.to = sp.from
		case a == "" && b == "":
			return nil, fmt.Errorf("%q is not a page range", part)
		default:
			sp.from = 1
			if a != "" {
				if sp.from, err = num(a); err != nil {
					return nil, err
				}
			}
			if b != "" {
				if sp.to, err = num(b); err != nil {
					return nil, err
				}
				if sp.to < sp.from {
					return nil, fmt.Errorf("%q runs backwards", part)
				}
			}
		}
		spans = append(spans, sp)
	}
	return spans, nil
}

// selectPages turns a range into the 0-based indices of the pages it
// covers, in the PDF's order and each once. No spans selects every page.
func selectPages(spans []pageSpan, total int) ([]int, error) {
	if len(spans) == 0 {
		return allPages(total), nil
	}
	seen := make([]bool, total)
	for _, sp := range spans {
		to := sp.to
		if to == 0 {
			to = total
		}
		if sp.from > total {
			return nil, fmt.Errorf("the PDF has %d pages, so the range cannot start at page %d", total, sp.from)
		}
		if to > total {
			return nil, fmt.Errorf("the PDF has %d pages, so the range cannot run to page %d", total, to)
		}
		for p := sp.from; p <= to; p++ {
			seen[p-1] = true
		}
	}
	var sel []int
	for i, ok := range seen {
		if ok {
			sel = append(sel, i)
		}
	}
	return sel, nil
}

func allPages(n int) []int {
	sel := make([]int, n)
	for i := range sel {
		sel[i] = i
	}
	return sel
}

// slotOf maps a PDF page index to its position in the book, or -1 for a
// page the book leaves out.
func slotOf(sel []int) func(int) int {
	return func(p int) int {
		if i, ok := slices.BinarySearch(sel, p); ok {
			return i
		}
		return -1
	}
}

package main

// Canvas selection and orientation.
//
// Kindle picks a single canvas per book. Pages whose viewport does not match
// it get distorted to fit, so every page is scaled onto one shared canvas.

// Size is a width and height in pixels.
type Size struct{ W, H int }

// Orientation reports landscape, portrait or square.
func (s Size) Orientation() string {
	switch {
	case s.W > s.H:
		return "landscape"
	case s.H > s.W:
		return "portrait"
	default:
		return "square"
	}
}

// ceilDiv is integer division rounding up, for positive operands.
func ceilDiv(a, b int) int { return (a + b - 1) / b }

// chooseCanvas returns the one canvas every page is fitted onto.
//
// It starts from the most common page size, so a book whose pages differ by a
// few pixels is not normalised to an outlier. It then grows, keeping that
// aspect ratio, until it contains the largest page without shrinking it, and
// finally caps the long edge at maxEdge (0 means no cap).
func chooseCanvas(pages []Size, maxEdge int) Size {
	if len(pages) == 0 {
		return Size{}
	}

	counts := make(map[Size]int, 4)
	for _, p := range pages {
		counts[p]++
	}

	// Ties break toward the larger area, then the wider page, so the result
	// never depends on map iteration order.
	var mode Size
	best := -1
	for s, n := range counts {
		switch {
		case n > best,
			n == best && s.W*s.H > mode.W*mode.H,
			n == best && s.W*s.H == mode.W*mode.H && s.W > mode.W:
			mode, best = s, n
		}
	}

	// Compare every page against the ORIGINAL modal aspect ratio. Comparing
	// against the canvas while it grows makes each page compound the last.
	aw, ah := mode.W, mode.H
	needW := aw
	for _, p := range pages {
		w := max(p.W, ceilDiv(p.H*aw, ah))
		needW = max(needW, w)
	}
	c := Size{needW, ceilDiv(needW*ah, aw)}

	if long := max(c.W, c.H); maxEdge > 0 && long > maxEdge {
		c.W = c.W * maxEdge / long
		c.H = c.H * maxEdge / long
	}

	// Kindle dislikes odd viewport dimensions in fixed layout.
	c.W = max(2, c.W-c.W%2)
	c.H = max(2, c.H-c.H%2)
	return c
}

// Orientation carries the three spellings the package needs.
type Orientation struct {
	Book      string // what is reported and requested
	Rendition string // EPUB rendition:orientation: landscape|portrait|auto
	Lock      string // Kindle orientation-lock: landscape|portrait|none
}

// bookOrientation derives orientation from the canvas rather than page 1, so
// a portrait cover on a landscape deck does not lock the book to portrait.
func bookOrientation(canvas Size, override string, mixed bool) Orientation {
	book := override
	if book == "" {
		switch {
		case mixed:
			book = "auto" // let the device decide per page
		case canvas.W > canvas.H:
			book = "landscape"
		case canvas.H > canvas.W:
			book = "portrait"
		default:
			book = "auto"
		}
	}

	// rendition:orientation has no "none"; Kindle's orientation-lock has no
	// "auto". Each maps onto the other's neutral value.
	o := Orientation{Book: book, Rendition: book, Lock: book}
	if o.Rendition == "none" {
		o.Rendition = "auto"
	}
	if o.Lock == "auto" {
		o.Lock = "none"
	}
	return o
}

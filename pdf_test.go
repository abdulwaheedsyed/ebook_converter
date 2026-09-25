package main

import (
	"bytes"
	"fmt"
	"strings"
)

// testPage describes one page of a synthetic PDF.
type testPage struct {
	W, H   float64 // MediaBox in points
	Rotate int     // /Rotate
	BG     *RGB    // page background; nil leaves it white
	Bar    bool    // a black bar across the top third, as stand-in "text"
	Text   bool    // a line of real text
	// Scan, when set, makes the page a single greyscale image of this many
	// pixels, drawn over the whole page, like a scanned book.
	Scan *Size
}

// makePDF writes a minimal, valid PDF with exact cross-reference offsets, so
// tests never depend on real documents.
func makePDF(pages []testPage) []byte {
	var b bytes.Buffer
	var offsets []int
	obj := func(body string) {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", len(offsets), body)
	}

	b.WriteString("%PDF-1.4\n")
	kids := make([]string, len(pages))
	for i := range pages {
		kids[i] = fmt.Sprintf("%d 0 R", 3+3*i)
	}
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	obj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pages)))

	for i, p := range pages {
		var c strings.Builder
		if p.BG != nil {
			fmt.Fprintf(&c, "%.4f %.4f %.4f rg 0 0 %g %g re f\n",
				float64(p.BG.R)/255, float64(p.BG.G)/255, float64(p.BG.B)/255, p.W, p.H)
		}
		if p.Bar {
			fmt.Fprintf(&c, "0 0 0 rg %g %g %g %g re f\n", p.W*0.1, p.H*0.7, p.W*0.8, p.H*0.1)
		}
		if p.Scan != nil {
			fmt.Fprintf(&c, "q %g 0 0 %g 0 0 cm /Im0 Do Q\n", p.W, p.H)
		}
		if p.Text {
			fmt.Fprintf(&c, "BT /F0 12 Tf 20 20 Td (Hello) Tj ET\n")
		}
		res := "<< >>"
		switch {
		case p.Scan != nil:
			res = fmt.Sprintf("<< /XObject << /Im0 %d 0 R >> >>", 5+3*i)
		case p.Text:
			res = fmt.Sprintf("<< /Font << /F0 %d 0 R >> >>", 5+3*i)
		}
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %g %g] /Rotate %d /Resources %s /Contents %d 0 R >>",
			p.W, p.H, p.Rotate, res, 4+3*i))
		obj(fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", c.Len(), c.String()))
		switch {
		case p.Scan != nil:
			px := bytes.Repeat([]byte{200}, p.Scan.W*p.Scan.H)
			obj(fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceGray /BitsPerComponent 8 /Length %d >>\nstream\n%s\nendstream",
				p.Scan.W, p.Scan.H, len(px), px))
		case p.Text:
			obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
		default:
			obj("<< >>") // keep object numbering regular
		}
	}

	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, o := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	return b.Bytes()
}

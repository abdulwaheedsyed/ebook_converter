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
		kids[i] = fmt.Sprintf("%d 0 R", 3+2*i)
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
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %g %g] /Rotate %d /Resources << >> /Contents %d 0 R >>",
			p.W, p.H, p.Rotate, 4+2*i))
		obj(fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", c.Len(), c.String()))
	}

	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, o := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	return b.Bytes()
}

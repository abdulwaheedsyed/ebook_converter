package main

import (
	"bytes"
	"crypto/md5"
	"crypto/rc4"
	"encoding/binary"
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

// testBookmark is an outline entry of a synthetic PDF.
type testBookmark struct {
	Title  string
	Page   int  // target page; -1 for a link that leaves the document
	Action bool // reach the page through a GoTo action instead of /Dest
	Kids   []testBookmark
}

// testPDF adds document-level structure to makePDF's pages.
type testPDF struct {
	Outline []testBookmark
	Loop    bool   // the last top-level bookmark's /Next points back to the first
	Labels  string // a /PageLabels /Nums array body, e.g. "0 << /S /r >> 2 << /S /D >>"
	// Password encrypts the document with the standard security handler,
	// revision 2 (40-bit RC4), with this as the password needed to open it.
	Password string
}

// pdfPadding pads passwords, from the PDF specification (7.6.3.3).
var pdfPadding = []byte("\x28\xBF\x4E\x5E\x4E\x75\x8A\x41\x64\x00\x4E\x56\xFF\xFA\x01\x08" +
	"\x2E\x2E\x00\xB6\xD0\x68\x3E\x80\x2F\x0C\xA9\xFE\x64\x53\x69\x7A")

func padPassword(pw string) []byte {
	return append([]byte(pw), pdfPadding...)[:32]
}

func rc4Crypt(key, data []byte) []byte {
	c, _ := rc4.NewCipher(key)
	out := make([]byte, len(data))
	c.XORKeyStream(out, data)
	return out
}

// pdfEncryption holds the file key and the /Encrypt entries of a document
// encrypted by revision 2 of the standard security handler.
type pdfEncryption struct {
	key   []byte
	o, u  []byte
	id    []byte
	perms int32
}

func newPDFEncryption(password string) *pdfEncryption {
	e := &pdfEncryption{id: []byte("leafbind-test-id"), perms: -4}
	// Algorithm 3: the owner entry, with the owner password the same as the user's.
	ok := md5.Sum(padPassword(password))
	e.o = rc4Crypt(ok[:5], padPassword(password))
	// Algorithm 2: the file key.
	h := md5.New()
	h.Write(padPassword(password))
	h.Write(e.o)
	binary.Write(h, binary.LittleEndian, e.perms)
	h.Write(e.id)
	e.key = h.Sum(nil)[:5]
	// Algorithm 4: the user entry.
	e.u = rc4Crypt(e.key, pdfPadding)
	return e
}

// crypt encrypts an object's stream data with that object's key.
func (e *pdfEncryption) crypt(num int, data []byte) []byte {
	h := md5.New()
	h.Write(e.key)
	h.Write([]byte{byte(num), byte(num >> 8), byte(num >> 16), 0, 0})
	return rc4Crypt(h.Sum(nil)[:10], data)
}

// makePDF writes a minimal, valid PDF with exact cross-reference offsets, so
// tests never depend on real documents.
func makePDF(pages []testPage) []byte { return makePDFWith(pages, testPDF{}) }

func makePDFWith(pages []testPage, doc testPDF) []byte {
	var b bytes.Buffer
	var offsets []int
	obj := func(body string) {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", len(offsets), body)
	}
	var enc *pdfEncryption
	if doc.Password != "" {
		enc = newPDFEncryption(doc.Password)
	}
	// stream writes a stream object, encrypted when the document is.
	stream := func(dict string, data []byte) {
		if enc != nil {
			data = enc.crypt(len(offsets)+1, data)
		}
		obj(fmt.Sprintf("<< %s /Length %d >>\nstream\n%s\nendstream", dict, len(data), data))
	}

	b.WriteString("%PDF-1.4\n")
	kids := make([]string, len(pages))
	for i := range pages {
		kids[i] = fmt.Sprintf("%d 0 R", 3+3*i)
	}
	// Outline objects follow the pages; number them up front so the
	// catalogue can point at them.
	next := 3 + 3*len(pages)
	var outline []string
	catalog := ""
	if len(doc.Outline) > 0 {
		root := next
		next++
		var place func(list []testBookmark, parent int) (first, last, count int)
		bodies := map[int]string{}
		place = func(list []testBookmark, parent int) (int, int, int) {
			nums := make([]int, len(list))
			for i := range list {
				nums[i] = next
				next++
			}
			count := len(list)
			for i, tb := range list {
				d := fmt.Sprintf("<< /Title (%s) /Parent %d 0 R", tb.Title, parent)
				if i > 0 {
					d += fmt.Sprintf(" /Prev %d 0 R", nums[i-1])
				}
				if i < len(list)-1 {
					d += fmt.Sprintf(" /Next %d 0 R", nums[i+1])
				} else if doc.Loop && parent == root {
					d += fmt.Sprintf(" /Next %d 0 R", nums[0])
				}
				switch {
				case tb.Page < 0:
					d += " /A << /S /URI /URI (https://example.com/) >>"
				case tb.Action:
					d += fmt.Sprintf(" /A << /S /GoTo /D [%d 0 R /Fit] >>", 3+3*tb.Page)
				default:
					d += fmt.Sprintf(" /Dest [%d 0 R /Fit]", 3+3*tb.Page)
				}
				if len(tb.Kids) > 0 {
					f, l, c := place(tb.Kids, nums[i])
					d += fmt.Sprintf(" /First %d 0 R /Last %d 0 R /Count %d", f, l, c)
					count += c
				}
				bodies[nums[i]] = d + " >>"
			}
			return nums[0], nums[len(nums)-1], count
		}
		f, l, c := place(doc.Outline, root)
		bodies[root] = fmt.Sprintf("<< /Type /Outlines /First %d 0 R /Last %d 0 R /Count %d >>", f, l, c)
		for n := root; n < next; n++ {
			outline = append(outline, bodies[n])
		}
		catalog += fmt.Sprintf(" /Outlines %d 0 R", root)
	}
	if doc.Labels != "" {
		catalog += " /PageLabels << /Nums [" + doc.Labels + "] >>"
	}
	obj("<< /Type /Catalog /Pages 2 0 R" + catalog + " >>")
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
		stream("", []byte(c.String()))
		switch {
		case p.Scan != nil:
			px := bytes.Repeat([]byte{200}, p.Scan.W*p.Scan.H)
			stream(fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceGray /BitsPerComponent 8",
				p.Scan.W, p.Scan.H), px)
		case p.Text:
			obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
		default:
			obj("<< >>") // keep object numbering regular
		}
	}

	for _, o := range outline {
		obj(o)
	}

	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, o := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	trailer := ""
	if enc != nil {
		trailer = fmt.Sprintf(" /Encrypt << /Filter /Standard /V 1 /R 2 /O <%x> /U <%x> /P %d >> /ID [<%x> <%x>]",
			enc.o, enc.u, enc.perms, enc.id, enc.id)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R%s >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, trailer, xref)
	return b.Bytes()
}

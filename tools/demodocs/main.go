// Command demodocs writes the sample PDFs used for Leafbind's screenshots:
// a slide deck, a novel, and a scanned book, so the screenshots show neutral
// content that anyone can regenerate.
//
//	go run ./tools/demodocs -o /tmp/demo
//
// The novel is the opening of Lewis Carroll's "Alice's Adventures in
// Wonderland" (1865), which is in the public domain. The deck and the
// letters are written for the purpose, about fictional people and places.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"log"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goitalic"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

func main() {
	out := flag.String("o", "demo", "output folder")
	flag.Parse()
	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatal(err)
	}
	for name, doc := range map[string]*pdf{
		"Quarterly Review.pdf":                 deck(),
		"Alice's Adventures in Wonderland.pdf": novel(),
		"Letters from the Coast (scanned).pdf": letters(),
	} {
		if err := os.WriteFile(filepath.Join(*out, name), doc.bytes(), 0o644); err != nil {
			log.Fatal(err)
		}
		fmt.Println("wrote", name)
	}
}

// ----- a minimal PDF writer -----

type pdf struct {
	objs   []string // object bodies, numbered from 1
	pages  []int
	marks  []bookmark // the outline, one level deep
	labels string     // a /PageLabels number tree's /Nums, or ""
	fonts  int        // object number of the font resource dictionary
}

func newPDF() *pdf {
	p := &pdf{}
	p.add("") // 1: catalog, filled in by bytes
	p.add("") // 2: page tree
	var f []string
	for i, name := range []string{"Helvetica", "Helvetica-Bold", "Times-Roman", "Times-Italic", "Times-Bold"} {
		n := p.add(fmt.Sprintf("<< /Type /Font /Subtype /Type1 /BaseFont /%s /Encoding /WinAnsiEncoding >>", name))
		f = append(f, fmt.Sprintf("/F%d %d 0 R", i+1, n))
	}
	p.fonts = p.add("<< " + strings.Join(f, " ") + " >>")
	return p
}

func (p *pdf) add(body string) int {
	p.objs = append(p.objs, body)
	return len(p.objs)
}

// page adds a page of w x h points drawing content, with optional images.
func (p *pdf) page(w, h float64, content string, images map[string]int) {
	xo := ""
	if len(images) > 0 {
		var parts []string
		for name, n := range images {
			parts = append(parts, fmt.Sprintf("/%s %d 0 R", name, n))
		}
		xo = " /XObject << " + strings.Join(parts, " ") + " >>"
	}
	c := p.add(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))
	p.pages = append(p.pages, p.add(fmt.Sprintf(
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %g %g] /Resources << /Font %d 0 R%s >> /Contents %d 0 R >>",
		w, h, p.fonts, xo, c)))
}

func (p *pdf) jpegImage(img image.Image, gray bool) int {
	var b bytes.Buffer
	jpeg.Encode(&b, img, &jpeg.Options{Quality: 85})
	cs := "/DeviceRGB"
	if gray {
		cs = "/DeviceGray"
	}
	r := img.Bounds()
	return p.add(fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace %s /BitsPerComponent 8 /Filter /DCTDecode /Length %d >>\nstream\n%s\nendstream",
		r.Dx(), r.Dy(), cs, b.Len(), b.String()))
}

func (p *pdf) bytes() []byte {
	kids := make([]string, len(p.pages))
	for i, n := range p.pages {
		kids[i] = fmt.Sprintf("%d 0 R", n)
	}
	catalog := ""
	if len(p.marks) > 0 {
		root := p.add("")
		first := len(p.objs) + 1
		for i, m := range p.marks {
			d := fmt.Sprintf("<< /Title %s /Parent %d 0 R /Dest [%d 0 R /Fit]", utf16Text(m.title), root, p.pages[m.page])
			if i > 0 {
				d += fmt.Sprintf(" /Prev %d 0 R", first+i-1)
			}
			if i < len(p.marks)-1 {
				d += fmt.Sprintf(" /Next %d 0 R", first+i+1)
			}
			p.add(d + " >>")
		}
		p.objs[root-1] = fmt.Sprintf("<< /Type /Outlines /First %d 0 R /Last %d 0 R /Count %d >>", first, first+len(p.marks)-1, len(p.marks))
		catalog += fmt.Sprintf(" /Outlines %d 0 R /PageMode /UseOutlines", root)
	}
	if p.labels != "" {
		catalog += " /PageLabels << /Nums [" + p.labels + "] >>"
	}
	p.objs[0] = "<< /Type /Catalog /Pages 2 0 R" + catalog + " >>"
	p.objs[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(p.pages))

	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(p.objs))
	for i, body := range p.objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(p.objs)+1)
	for _, o := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(p.objs)+1, xref)
	return b.Bytes()
}

// bookmark is an outline entry that opens a page.
type bookmark struct {
	title string
	page  int
}

// utf16Text is a PDF text string in UTF-16BE, which holds any character.
func utf16Text(s string) string {
	var b strings.Builder
	b.WriteString("<FEFF")
	for _, u := range utf16.Encode([]rune(s)) {
		fmt.Fprintf(&b, "%04X", u)
	}
	return b.String() + ">"
}

// draw builds a page's content stream.
type draw struct{ strings.Builder }

func (d *draw) rgb(hex uint32) string {
	return fmt.Sprintf("%.3f %.3f %.3f", float64(hex>>16&0xff)/255, float64(hex>>8&0xff)/255, float64(hex&0xff)/255)
}

func (d *draw) rect(x, y, w, h float64, fill uint32) {
	fmt.Fprintf(d, "%s rg %g %g %g %g re f\n", d.rgb(fill), x, y, w, h)
}

// text draws one line; font is 1 Helvetica, 2 Helvetica-Bold, 3 Times,
// 4 Times-Italic, 5 Times-Bold.
func (d *draw) text(font int, size, x, y float64, fill uint32, s string) {
	fmt.Fprintf(d, "BT %s rg /F%d %g Tf %g %g Td (%s) Tj ET\n", d.rgb(fill), font, size, x, y, escape(s))
}

func escape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, "(", `\(`, ")", `\)`, "‘", "\x91", "’", "\x92", "“", "\x93", "”", "\x94", "—", "\x97", "é", "\xe9", "·", "\xb7")
	return r.Replace(s)
}

// wrap breaks text into lines of about width points, estimating glyph width
// as a fraction of the font size, which is close enough for sample text.
func wrap(s string, size, width, em float64) []string {
	var lines []string
	line := ""
	for _, w := range strings.Fields(s) {
		try := strings.TrimSpace(line + " " + w)
		if float64(len([]rune(try)))*size*em > width && line != "" {
			lines = append(lines, line)
			line = w
			continue
		}
		line = try
	}
	return append(lines, line)
}

// ----- the slide deck -----

func deck() *pdf {
	const W, H = 960, 540
	const slate, ink, muted, teal, amber, paper = 0x1E293B, 0x0F172A, 0x64748B, 0x0F766E, 0xD97706, 0xF8FAFC
	p := newPDF()

	title := func(heading string) *draw {
		d := &draw{}
		d.rect(0, 0, W, H, paper)
		d.rect(0, H-96, W, 96, slate)
		d.rect(0, H-100, W, 4, amber)
		d.text(2, 32, 56, H-62, 0xFFFFFF, heading)
		d.text(1, 13, 56, 28, muted, "Harbor Lane Coffee Co.  ·  Third quarter review")
		return d
	}
	bullets := func(d *draw, items []string) {
		y := float64(H - 170)
		for _, it := range items {
			d.rect(64, y+6, 8, 8, teal)
			d.text(1, 24, 88, y, ink, it)
			y -= 58
		}
	}

	// Cover.
	d := &draw{}
	d.rect(0, 0, W, H, slate)
	d.rect(56, 250, 120, 6, amber)
	d.text(2, 58, 56, 290, 0xFFFFFF, "Quarterly Review")
	d.text(1, 24, 56, 205, 0xCBD5E1, "Harbor Lane Coffee Co.  ·  Third quarter")
	p.page(W, H, d.String(), nil)

	d = title("Highlights")
	bullets(d, []string{
		"Revenue up 12% on the same quarter last year",
		"Two new cafés opened, in Riverside and Old Town",
		"Subscription orders passed 4,000 a month",
		"Waste sent to landfill down by a third",
	})
	p.page(W, H, d.String(), nil)

	d = title("Revenue by quarter")
	vals := []float64{1.62, 1.71, 1.80, 1.94, 2.17}
	labels := []string{"Q3 last year", "Q4", "Q1", "Q2", "Q3"}
	for i, v := range vals {
		x := 120 + float64(i)*150
		h := v * 150
		col := uint32(0x94A3B8)
		if i == len(vals)-1 {
			col = teal
		}
		d.rect(x, 80, 90, h, col)
		d.text(2, 18, x+14, 90+h, ink, fmt.Sprintf("$%.2fm", v))
		d.text(1, 15, x+4, 56, muted, labels[i])
	}
	p.page(W, H, d.String(), nil)

	d = title("Where sales come from")
	for i, row := range []struct {
		name string
		pct  float64
		col  uint32
	}{{"Cafés", 58, teal}, {"Online", 27, amber}, {"Wholesale", 15, 0x94A3B8}} {
		y := float64(H - 200 - i*90)
		d.text(2, 22, 64, y+14, ink, row.name)
		d.rect(240, y, row.pct*10, 44, row.col)
		d.text(2, 22, 252+row.pct*10, y+14, ink, fmt.Sprintf("%.0f%%", row.pct))
	}
	p.page(W, H, d.String(), nil)

	d = title("Next quarter")
	bullets(d, []string{
		"Open a third café, on the harbour front",
		"Launch a seasonal blend in November",
		"Move wholesale deliveries to electric vans",
		"Hire and train twelve new baristas",
	})
	p.page(W, H, d.String(), nil)

	d = &draw{}
	d.rect(0, 0, W, H, slate)
	d.text(2, 54, 56, 280, 0xFFFFFF, "Thank you")
	d.text(1, 22, 56, 230, 0xCBD5E1, "Questions and ideas are welcome.")
	p.page(W, H, d.String(), nil)
	return p
}

// ----- the novel -----

var alice = []string{
	"Alice was beginning to get very tired of sitting by her sister on the bank, and of having nothing to do: once or twice she had peeped into the book her sister was reading, but it had no pictures or conversations in it, “and what is the use of a book,” thought Alice “without pictures or conversations?”",
	"So she was considering in her own mind (as well as she could, for the hot day made her feel very sleepy and stupid), whether the pleasure of making a daisy-chain would be worth the trouble of getting up and picking the daisies, when suddenly a White Rabbit with pink eyes ran close by her.",
	"There was nothing so very remarkable in that; nor did Alice think it so very much out of the way to hear the Rabbit say to itself, “Oh dear! Oh dear! I shall be late!” (when she thought it over afterwards, it occurred to her that she ought to have wondered at this, but at the time it all seemed quite natural); but when the Rabbit actually took a watch out of its waistcoat-pocket, and looked at it, and then hurried on, Alice started to her feet, for it flashed across her mind that she had never before seen a rabbit with either a waistcoat-pocket, or a watch to take out of it, and burning with curiosity, she ran across the field after it, and fortunately was just in time to see it pop down a large rabbit-hole under the hedge.",
	"In another moment down went Alice after it, never once considering how in the world she was to get out again.",
	"The rabbit-hole went straight on like a tunnel for some way, and then dipped suddenly down, so suddenly that Alice had not a moment to think about stopping herself before she found herself falling down a very deep well.",
	"Either the well was very deep, or she fell very slowly, for she had plenty of time as she went down to look about her and to wonder what was going to happen next. First, she tried to look down and make out what she was coming to, but it was too dark to see anything; then she looked at the sides of the well, and noticed that they were filled with cupboards and book-shelves; here and there she saw maps and pictures hung upon pegs.",
}

var chapters = [][2]string{
	{"I", "Down the Rabbit-Hole"}, {"II", "The Pool of Tears"}, {"III", "A Caucus-Race and a Long Tale"},
	{"IV", "The Rabbit Sends in a Little Bill"}, {"V", "Advice from a Caterpillar"}, {"VI", "Pig and Pepper"},
	{"VII", "A Mad Tea-Party"}, {"VIII", "The Queen’s Croquet-Ground"}, {"IX", "The Mock Turtle’s Story"},
	{"X", "The Lobster Quadrille"}, {"XI", "Who Stole the Tarts?"}, {"XII", "Alice’s Evidence"},
}

func novel() *pdf {
	const W, H = 396, 612
	const green, cream, ink = 0x14532D, 0xFEF3C7, 0x1C1917
	p := newPDF()

	d := &draw{}
	d.rect(0, 0, W, H, green)
	d.rect(36, 36, W-72, H-72, 0x166534)
	d.rect(44, 44, W-88, H-88, green)
	d.text(5, 34, 62, 420, cream, "Alice’s")
	d.text(5, 34, 62, 378, cream, "Adventures in")
	d.text(5, 34, 62, 336, cream, "Wonderland")
	d.rect(62, 300, 90, 2, cream)
	d.text(4, 18, 62, 262, cream, "Lewis Carroll")
	p.page(W, H, d.String(), nil)

	// Enough pages that a screenshot can catch the conversion in progress,
	// with the book's twelve chapters spread over them and bookmarked.
	p.marks = append(p.marks, bookmark{"Cover", 0})
	p.labels = "0 << /P (Cover) >> 1 << /S /D >>"
	for n := 1; n <= 300; n++ {
		d := &draw{}
		d.rect(0, 0, W, H, 0xFFFFFF)
		y := float64(H - 72)
		if (n-1)%25 == 0 {
			ch := chapters[(n-1)/25]
			d.text(5, 18, 54, y, ink, "Chapter "+ch[0])
			d.text(4, 14, 54, y-24, ink, ch[1])
			y -= 64
			p.marks = append(p.marks, bookmark{"Chapter " + ch[0] + ". " + ch[1], n})
		}
		for _, para := range alice {
			for i, line := range wrap(para, 11, W-108, 0.40) {
				x := 54.0
				if i == 0 {
					x += 14
				}
				d.text(3, 11, x, y, ink, line)
				y -= 15
				if y < 72 {
					break
				}
			}
			y -= 6
			if y < 72 {
				break
			}
		}
		d.text(3, 9, W/2-6, 40, 0x57534E, fmt.Sprint(n))
		p.page(W, H, d.String(), nil)
	}
	return p
}

// ----- the scanned letters -----

var letterText = [][]string{
	{"Dear Margaret,", "The weather turned this morning, and the boats stayed in the harbour for the first time since we arrived. I walked along the sea wall instead, as far as the old lighthouse, and counted eleven kinds of gull before I lost count and gave up.", "The keeper's cottage is empty now, but someone still paints the door every spring. It is the brightest blue on the whole coast."},
	{"We found the bookshop you told us about, tucked behind the chandler's. The owner remembered your father, or said he did, and sold us a tide table from the year you were born.", "It rained all afternoon, so we read in the window seat and watched the ferry come and go. Thomas has started keeping a diary of it: times, passengers, the colour of the funnel."},
	{"On Thursday the fog came in so thickly that the church bell rang every quarter of an hour, all night, for the fishing boats. I found it strangely comforting.", "In the morning the whole bay was silver and perfectly still. We took the small boat out to the island and ate our lunch among the ruins of the chapel."},
	{"Thomas asks whether you still have the brass telescope. He would like to look at the stars from the headland before we leave, and the one at the inn is hopelessly bent.", "We sail for home on the ninth. I will bring you a jar of the heather honey, and all the news, and far too many shells."},
	{"With love from us both,", "Eleanor", "P.S. The lighthouse door is blue again. They painted it on Sunday."},
}

func letters() *pdf {
	const W, H = 360, 540 // points: 5 x 7.5 inches
	const ppi = 150
	px, py := W*ppi/72, H*ppi/72
	reg, _ := opentype.Parse(goregular.TTF)
	ita, _ := opentype.Parse(goitalic.TTF)
	face := func(f *opentype.Font, pt float64) font.Face {
		fc, _ := opentype.NewFace(f, &opentype.FaceOptions{Size: pt, DPI: ppi, Hinting: font.HintingFull})
		return fc
	}
	body, sign := face(reg, 10.5), face(ita, 12)
	r := rand.New(rand.NewPCG(7, 11))

	p := newPDF()
	for n, paras := range letterText {
		// Warm, uneven paper, as a flatbed scanner sees it.
		img := image.NewRGBA(image.Rect(0, 0, px, py))
		for y := range py {
			for x := range px {
				shade := 1 - 0.035*float64(x)/float64(px) - 0.02*float64(y)/float64(py)
				noise := r.Float64()*6 - 3
				img.Set(x, y, color.RGBA{
					uint8(min(255, 239*shade+noise)), uint8(min(255, 231*shade+noise)), uint8(min(255, 214*shade+noise)), 255,
				})
			}
		}
		dr := &font.Drawer{Dst: img, Src: image.NewUniform(color.RGBA{45, 38, 30, 255}), Face: body}
		y := 120
		for i, para := range paras {
			fc := body
			if n == len(letterText)-1 && i == 1 {
				fc = sign
			}
			dr.Face = fc
			for _, line := range wrap(para, 10.5*ppi/72, float64(px-190), 0.5) {
				dr.Dot = fixed.P(95, y)
				dr.DrawString(line)
				y += 34
			}
			y += 18
		}
		dr.Face = body
		dr.Dot = fixed.P(px/2-8, py-70)
		dr.DrawString(fmt.Sprint(n + 1))
		im := p.jpegImage(img, false)
		p.page(W, H, fmt.Sprintf("q %d 0 0 %d 0 0 cm /Im0 Do Q", W, H), map[string]int{"Im0": im})
	}
	return p
}

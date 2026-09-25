package main

// A baseline JPEG encoder with Huffman tables optimised per image.
//
// Go's image/jpeg always uses the example Huffman tables from Annex K.3 of the
// JPEG specification. Building tables from each image's own symbol statistics
// instead is lossless -- the decoded pixels are identical -- and typically
// makes the file about 10% smaller. That is what libjpeg's optimize_coding
// does, and what this encoder does: one pass gathers statistics, a second
// encodes.
//
// Output is baseline sequential DCT, 8-bit, the most widely supported form.
// Greyscale images are one component; colour is YCbCr with 4:2:0 chroma
// subsampling, like image/jpeg.

import (
	"bufio"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"io"
	"math"
	"math/bits"
)

// zigzag[i] is the natural-order index of the i-th coefficient in zig-zag
// order.
var zigzag = [64]int{
	0, 1, 8, 16, 9, 2, 3, 10,
	17, 24, 32, 25, 18, 11, 4, 5,
	12, 19, 26, 33, 40, 48, 41, 34,
	27, 20, 13, 6, 7, 14, 21, 28,
	35, 42, 49, 56, 57, 50, 43, 36,
	29, 22, 15, 23, 30, 37, 44, 51,
	58, 59, 52, 45, 38, 31, 39, 46,
	53, 60, 61, 54, 47, 55, 62, 63,
}

// Quantisation tables from Annex K.1, in zig-zag order.
var baseQuant = [2][64]uint8{
	{ // luminance
		16, 11, 12, 14, 12, 10, 16, 14, 13, 14, 18, 17, 16, 19, 24, 40,
		26, 24, 22, 22, 24, 49, 35, 37, 29, 40, 58, 51, 61, 60, 57, 51,
		56, 55, 64, 72, 92, 78, 64, 68, 87, 69, 55, 56, 80, 109, 81, 87,
		95, 98, 103, 104, 103, 62, 77, 113, 121, 112, 100, 120, 92, 101, 103, 99,
	},
	{ // chrominance
		17, 18, 18, 24, 21, 24, 47, 26, 26, 47, 99, 66, 56, 66, 99, 99,
		99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99,
		99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99,
		99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99,
	},
}

// quantTables scales the base tables for a quality from 1 to 100 with the
// IJG formula, the same one image/jpeg and libjpeg use.
func quantTables(quality int) [2][64]uint8 {
	quality = min(max(quality, 1), 100)
	scale := 200 - 2*quality
	if quality < 50 {
		scale = 5000 / quality
	}
	var q [2][64]uint8
	for t := range q {
		for i, v := range baseQuant[t] {
			q[t][i] = uint8(min(max((int(v)*scale+50)/100, 1), 255))
		}
	}
	return q
}

// dctCos[u][x] = C(u)/2 * cos((2x+1)uπ/16), so a 2-D DCT is two passes of a
// plain matrix product.
var dctCos = func() (m [8][8]float64) {
	for u := range 8 {
		c := 0.5
		if u == 0 {
			c = 0.5 / math.Sqrt2
		}
		for x := range 8 {
			m[u][x] = c * math.Cos(float64(2*x+1)*float64(u)*math.Pi/16)
		}
	}
	return m
}()

// fdctQuant transforms one 8x8 block of level-shifted samples (natural order)
// and writes its quantised coefficients to out in zig-zag order.
func fdctQuant(in *[64]float64, q *[64]uint8, out []int16) {
	var tmp [64]float64
	for y := range 8 { // rows
		row := in[8*y : 8*y+8]
		for u := range 8 {
			c := &dctCos[u]
			tmp[8*y+u] = c[0]*row[0] + c[1]*row[1] + c[2]*row[2] + c[3]*row[3] +
				c[4]*row[4] + c[5]*row[5] + c[6]*row[6] + c[7]*row[7]
		}
	}
	for zi, n := range zigzag { // columns, straight into zig-zag order
		v, u := n/8, n%8
		c := &dctCos[v]
		f := c[0]*tmp[u] + c[1]*tmp[8+u] + c[2]*tmp[16+u] + c[3]*tmp[24+u] +
			c[4]*tmp[32+u] + c[5]*tmp[40+u] + c[6]*tmp[48+u] + c[7]*tmp[56+u]
		// Baseline JPEG limits AC magnitudes to 1023 (size category 10); the
		// float transform can reach 1024 at quality 100.
		lim := 1023.0
		if zi == 0 {
			lim = 2047
		}
		out[zi] = int16(max(-lim, min(lim, math.Round(f/float64(q[zi])))))
	}
}

// scan holds every quantised block in the order it is written.
type scan struct {
	gray   bool
	w, h   int
	coef   []int16 // 64 per block, zig-zag order
	table  []uint8 // per block: 0 luminance, 1 chrominance
	colour []uint8 // per block: component index 0, 1, 2
}

// transform converts, subsamples and quantises the whole image. Blocks at the
// right and bottom edges repeat the last row and column, as image/jpeg does.
func transform(m image.Image, q *[2][64]uint8) *scan {
	b := m.Bounds()
	s := &scan{w: b.Dx(), h: b.Dy()}
	var blk [64]float64
	add := func(t, c uint8) []int16 {
		s.coef = append(s.coef, make([]int16, 64)...)
		s.table = append(s.table, t)
		s.colour = append(s.colour, c)
		return s.coef[len(s.coef)-64:]
	}

	if g, ok := m.(*image.Gray); ok {
		s.gray = true
		bw, bh := (s.w+7)/8, (s.h+7)/8
		s.coef = make([]int16, 0, 64*bw*bh)
		for by := range bh {
			for bx := range bw {
				for j := range 8 {
					y := min(by*8+j, s.h-1)
					row := g.Pix[g.PixOffset(b.Min.X, b.Min.Y+y):]
					for i := range 8 {
						blk[8*j+i] = float64(row[min(bx*8+i, s.w-1)]) - 128
					}
				}
				fdctQuant(&blk, &q[0], add(0, 0))
			}
		}
		return s
	}

	rgba, ok := m.(*image.RGBA)
	if !ok {
		rgba = image.NewRGBA(image.Rect(0, 0, s.w, s.h))
		draw.Draw(rgba, rgba.Bounds(), m, b.Min, draw.Src)
		b = rgba.Bounds()
	}

	// Convert once to full-resolution Y, Cb, Cr planes.
	n := s.w * s.h
	yp, cbp, crp := make([]uint8, n), make([]uint8, n), make([]uint8, n)
	for y := range s.h {
		row := rgba.Pix[rgba.PixOffset(b.Min.X, b.Min.Y+y):]
		for x := range s.w {
			p := row[4*x : 4*x+3 : 4*x+3]
			yp[y*s.w+x], cbp[y*s.w+x], crp[y*s.w+x] = color.RGBToYCbCr(p[0], p[1], p[2])
		}
	}
	at := func(p []uint8, x, y int) int { return int(p[min(y, s.h-1)*s.w+min(x, s.w-1)]) }

	mw, mh := (s.w+15)/16, (s.h+15)/16
	s.coef = make([]int16, 0, 64*6*mw*mh)
	var lastDC int16
	for my := range mh {
		for mx := range mw {
			for k := range 4 { // four luminance blocks, row-major within the MCU
				ox, oy := mx*16+(k&1)*8, my*16+(k>>1)*8
				out := add(0, 0)
				if ox >= s.w || oy >= s.h {
					// A dummy block: it completes the MCU but lies wholly
					// outside the image, so no decoder shows it. Repeating
					// the previous DC with no AC terms codes it in two
					// symbols, as libjpeg does.
					out[0] = lastDC
					continue
				}
				for j := range 8 {
					for i := range 8 {
						blk[8*j+i] = float64(at(yp, ox+i, oy+j)) - 128
					}
				}
				fdctQuant(&blk, &q[0], out)
				lastDC = out[0]
			}
			for c, p := range [][]uint8{cbp, crp} { // each chroma sample averages 2x2
				for j := range 8 {
					for i := range 8 {
						x, y := mx*16+2*i, my*16+2*j
						sum := at(p, x, y) + at(p, x+1, y) + at(p, x, y+1) + at(p, x+1, y+1)
						blk[8*j+i] = float64((sum+2)>>2) - 128
					}
				}
				fdctQuant(&blk, &q[1], add(1, uint8(c+1)))
			}
		}
	}
	return s
}

// category is the number of bits needed for v's magnitude.
func category(v int) int {
	if v < 0 {
		v = -v
	}
	return bits.Len(uint(v))
}

// walk visits every Huffman symbol of the scan, in order, as its table
// (0 DC luma, 1 AC luma, 2 DC chroma, 3 AC chroma), the symbol, and the
// additional bits that follow it.
func (s *scan) walk(visit func(table int, sym uint8, extra int, nExtra int)) {
	var prev [3]int
	for blk := range len(s.table) {
		c := s.coef[64*blk : 64*blk+64]
		t, comp := int(s.table[blk]), s.colour[blk]

		diff := int(c[0]) - prev[comp]
		prev[comp] = int(c[0])
		n := category(diff)
		visit(2*t, uint8(n), diff, n)

		run := 0
		for k := 1; k < 64; k++ {
			v := int(c[k])
			if v == 0 {
				run++
				continue
			}
			for run > 15 {
				visit(2*t+1, 0xF0, 0, 0) // ZRL: sixteen zeros
				run -= 16
			}
			n := category(v)
			visit(2*t+1, uint8(run<<4|n), v, n)
			run = 0
		}
		if run > 0 {
			visit(2*t+1, 0x00, 0, 0) // EOB
		}
	}
}

// huffTable is a table as written to DHT, with its encoding look-up.
type huffTable struct {
	counts [16]uint8 // number of codes of each length 1..16
	values []uint8   // symbols in code order
	code   [256]uint16
	size   [256]uint8
}

// buildCodes assigns canonical codes (Annex C) from counts and values.
func (h *huffTable) buildCodes() {
	code, k := uint16(0), 0
	for l := range 16 {
		for range h.counts[l] {
			h.code[h.values[k]] = code
			h.size[h.values[k]] = uint8(l + 1)
			code++
			k++
		}
		code <<= 1
	}
}

// optimalTable builds a length-limited Huffman table from symbol frequencies
// following Annex K.2 of the JPEG specification, which reserves the all-ones
// code and limits codes to 16 bits.
func optimalTable(freq *[256]int64) *huffTable {
	var f [257]int64
	copy(f[:], freq[:])
	f[256] = 1 // reserved code point; guarantees no code is all ones

	var codeSize [257]int
	var others [257]int
	for i := range others {
		others[i] = -1
	}
	for {
		// The two least frequent live symbols; ties go to the larger value.
		c1, c2 := -1, -1
		for i := range f {
			if f[i] == 0 {
				continue
			}
			if c1 < 0 || f[i] <= f[c1] {
				c2, c1 = c1, i
			} else if c2 < 0 || f[i] <= f[c2] {
				c2 = i
			}
		}
		if c2 < 0 {
			break
		}
		f[c1] += f[c2]
		f[c2] = 0
		for codeSize[c1]++; others[c1] >= 0; codeSize[c1]++ {
			c1 = others[c1]
		}
		others[c1] = c2
		for codeSize[c2]++; others[c2] >= 0; codeSize[c2]++ {
			c2 = others[c2]
		}
	}

	var count [33]int
	for _, n := range codeSize {
		if n > 0 {
			count[n]++
		}
	}
	// Limit to 16 bits (Figure K.3): take two codes from the longest length,
	// add one a level up, and move a shorter code down to pair with it.
	for i := 32; i > 16; i-- {
		for count[i] > 0 {
			j := i - 2
			for count[j] == 0 {
				j--
			}
			count[i] -= 2
			count[i-1]++
			count[j+1] += 2
			count[j]--
		}
	}
	// Drop the reserved code point from the longest remaining length.
	i := 16
	for count[i] == 0 {
		i--
	}
	count[i]--

	h := &huffTable{}
	for l := 1; l <= 16; l++ {
		h.counts[l-1] = uint8(count[l])
	}
	// Symbols in order of their original code length, then value (Figure
	// K.4), excluding the reserved one.
	for l := 1; l <= 32; l++ {
		for s := range 256 {
			if codeSize[s] == l {
				h.values = append(h.values, uint8(s))
			}
		}
	}
	h.buildCodes()
	return h
}

// Example tables from Annex K.3, used when optimisation is off.
var standardSpec = [4]struct {
	counts [16]uint8
	values []uint8
}{
	{[16]uint8{0, 1, 5, 1, 1, 1, 1, 1, 1}, []uint8{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}},
	{[16]uint8{0, 2, 1, 3, 3, 2, 4, 3, 5, 5, 4, 4, 0, 0, 1, 125}, []uint8{
		0x01, 0x02, 0x03, 0x00, 0x04, 0x11, 0x05, 0x12, 0x21, 0x31, 0x41, 0x06, 0x13, 0x51, 0x61, 0x07,
		0x22, 0x71, 0x14, 0x32, 0x81, 0x91, 0xa1, 0x08, 0x23, 0x42, 0xb1, 0xc1, 0x15, 0x52, 0xd1, 0xf0,
		0x24, 0x33, 0x62, 0x72, 0x82, 0x09, 0x0a, 0x16, 0x17, 0x18, 0x19, 0x1a, 0x25, 0x26, 0x27, 0x28,
		0x29, 0x2a, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x3a, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48, 0x49,
		0x4a, 0x53, 0x54, 0x55, 0x56, 0x57, 0x58, 0x59, 0x5a, 0x63, 0x64, 0x65, 0x66, 0x67, 0x68, 0x69,
		0x6a, 0x73, 0x74, 0x75, 0x76, 0x77, 0x78, 0x79, 0x7a, 0x83, 0x84, 0x85, 0x86, 0x87, 0x88, 0x89,
		0x8a, 0x92, 0x93, 0x94, 0x95, 0x96, 0x97, 0x98, 0x99, 0x9a, 0xa2, 0xa3, 0xa4, 0xa5, 0xa6, 0xa7,
		0xa8, 0xa9, 0xaa, 0xb2, 0xb3, 0xb4, 0xb5, 0xb6, 0xb7, 0xb8, 0xb9, 0xba, 0xc2, 0xc3, 0xc4, 0xc5,
		0xc6, 0xc7, 0xc8, 0xc9, 0xca, 0xd2, 0xd3, 0xd4, 0xd5, 0xd6, 0xd7, 0xd8, 0xd9, 0xda, 0xe1, 0xe2,
		0xe3, 0xe4, 0xe5, 0xe6, 0xe7, 0xe8, 0xe9, 0xea, 0xf1, 0xf2, 0xf3, 0xf4, 0xf5, 0xf6, 0xf7, 0xf8,
		0xf9, 0xfa}},
	{[16]uint8{0, 3, 1, 1, 1, 1, 1, 1, 1, 1, 1}, []uint8{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}},
	{[16]uint8{0, 2, 1, 2, 4, 4, 3, 4, 7, 5, 4, 4, 0, 1, 2, 119}, []uint8{
		0x00, 0x01, 0x02, 0x03, 0x11, 0x04, 0x05, 0x21, 0x31, 0x06, 0x12, 0x41, 0x51, 0x07, 0x61, 0x71,
		0x13, 0x22, 0x32, 0x81, 0x08, 0x14, 0x42, 0x91, 0xa1, 0xb1, 0xc1, 0x09, 0x23, 0x33, 0x52, 0xf0,
		0x15, 0x62, 0x72, 0xd1, 0x0a, 0x16, 0x24, 0x34, 0xe1, 0x25, 0xf1, 0x17, 0x18, 0x19, 0x1a, 0x26,
		0x27, 0x28, 0x29, 0x2a, 0x35, 0x36, 0x37, 0x38, 0x39, 0x3a, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48,
		0x49, 0x4a, 0x53, 0x54, 0x55, 0x56, 0x57, 0x58, 0x59, 0x5a, 0x63, 0x64, 0x65, 0x66, 0x67, 0x68,
		0x69, 0x6a, 0x73, 0x74, 0x75, 0x76, 0x77, 0x78, 0x79, 0x7a, 0x82, 0x83, 0x84, 0x85, 0x86, 0x87,
		0x88, 0x89, 0x8a, 0x92, 0x93, 0x94, 0x95, 0x96, 0x97, 0x98, 0x99, 0x9a, 0xa2, 0xa3, 0xa4, 0xa5,
		0xa6, 0xa7, 0xa8, 0xa9, 0xaa, 0xb2, 0xb3, 0xb4, 0xb5, 0xb6, 0xb7, 0xb8, 0xb9, 0xba, 0xc2, 0xc3,
		0xc4, 0xc5, 0xc6, 0xc7, 0xc8, 0xc9, 0xca, 0xd2, 0xd3, 0xd4, 0xd5, 0xd6, 0xd7, 0xd8, 0xd9, 0xda,
		0xe2, 0xe3, 0xe4, 0xe5, 0xe6, 0xe7, 0xe8, 0xe9, 0xea, 0xf2, 0xf3, 0xf4, 0xf5, 0xf6, 0xf7, 0xf8,
		0xf9, 0xfa}},
}

func standardTable(i int) *huffTable {
	h := &huffTable{counts: standardSpec[i].counts, values: standardSpec[i].values}
	h.buildCodes()
	return h
}

// bitWriter packs Huffman codes MSB first and stuffs a zero byte after every
// 0xFF, as entropy-coded data requires.
type bitWriter struct {
	w    *bufio.Writer
	acc  uint64
	n    uint
	fail error
}

func (b *bitWriter) put(v uint32, n uint) {
	b.acc = b.acc<<n | uint64(v)&(1<<n-1)
	b.n += n
	for b.n >= 8 {
		b.n -= 8
		c := byte(b.acc >> b.n)
		b.w.WriteByte(c)
		if c == 0xFF {
			b.w.WriteByte(0)
		}
	}
}

func (b *bitWriter) flush() {
	if b.n > 0 {
		b.put(0x7F, 8-b.n) // pad the final byte with ones
	}
}

// encodeJPEG writes m as a baseline JPEG with optimised Huffman tables.
func encodeJPEG(w io.Writer, m image.Image, quality int) error {
	return encodeJPEGWith(w, m, quality, true)
}

func encodeJPEGWith(w io.Writer, m image.Image, quality int, optimise bool) error {
	b := m.Bounds()
	if b.Dx() < 1 || b.Dy() < 1 {
		return errors.New("jpeg: empty image")
	}
	if b.Dx() >= 1<<16 || b.Dy() >= 1<<16 {
		return errors.New("jpeg: image is too large to encode")
	}
	q := quantTables(quality)
	s := transform(m, &q)

	used := 4
	if s.gray {
		used = 2
	}
	var tables [4]*huffTable
	if optimise {
		var freq [4][256]int64
		s.walk(func(t int, sym uint8, _, _ int) { freq[t][sym]++ })
		for t := range used {
			tables[t] = optimalTable(&freq[t])
		}
	} else {
		for t := range used {
			tables[t] = standardTable(t)
		}
	}

	bw := bufio.NewWriterSize(w, 64<<10)
	seg := func(marker byte, body ...[]byte) {
		n := 2
		for _, p := range body {
			n += len(p)
		}
		bw.Write([]byte{0xFF, marker, byte(n >> 8), byte(n)})
		for _, p := range body {
			bw.Write(p)
		}
	}

	bw.Write([]byte{0xFF, 0xD8}) // SOI
	// JFIF APP0: version 1.01, square pixels, no thumbnail.
	seg(0xE0, []byte("JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00"))

	nq := 2
	if s.gray {
		nq = 1
	}
	var dqt []byte
	for t := range nq {
		dqt = append(dqt, byte(t))
		dqt = append(dqt, q[t][:]...)
	}
	seg(0xDB, dqt)

	size := []byte{8, byte(s.h >> 8), byte(s.h), byte(s.w >> 8), byte(s.w)}
	if s.gray {
		seg(0xC0, size, []byte{1, 1, 0x11, 0})
	} else {
		seg(0xC0, size, []byte{3, 1, 0x22, 0, 2, 0x11, 1, 3, 0x11, 1})
	}

	var dht []byte
	for t := range used {
		dht = append(dht, "\x00\x10\x01\x11"[t])
		dht = append(dht, tables[t].counts[:]...)
		dht = append(dht, tables[t].values...)
	}
	seg(0xC4, dht)

	if s.gray {
		seg(0xDA, []byte{1, 1, 0x00, 0, 63, 0})
	} else {
		seg(0xDA, []byte{3, 1, 0x00, 2, 0x11, 3, 0x11, 0, 63, 0})
	}

	out := &bitWriter{w: bw}
	s.walk(func(t int, sym uint8, extra, n int) {
		h := tables[t]
		out.put(uint32(h.code[sym]), uint(h.size[sym]))
		if n > 0 {
			if extra < 0 {
				extra-- // one's complement form for negative values
			}
			out.put(uint32(extra), uint(n))
		}
	})
	out.flush()

	bw.Write([]byte{0xFF, 0xD9}) // EOI
	return bw.Flush()
}

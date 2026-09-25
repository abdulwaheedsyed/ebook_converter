package main

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"math/rand/v2"
	"sort"
	"testing"
)

// testImages covers flat areas, gradients, noise, sharp text-like edges, and
// sizes that are not multiples of the 8- and 16-pixel block sizes.
func testImages() map[string]image.Image {
	r := rand.New(rand.NewPCG(1, 2))
	imgs := map[string]image.Image{}

	text := func(w, h int) *image.Gray {
		g := image.NewGray(image.Rect(0, 0, w, h))
		for i := range g.Pix {
			g.Pix[i] = 250
		}
		for y := 10; y+12 < h; y += 22 { // lines of glyph-like strokes
			for x := 8; x+6 < w; x += 7 + r.IntN(5) {
				for dy := range 12 {
					for dx := range 2 + r.IntN(3) {
						g.SetGray(x+dx, y+dy, color.Gray{uint8(r.IntN(40))})
					}
				}
			}
		}
		return g
	}
	imgs["text 600x400"] = text(600, 400)
	imgs["text odd 333x217"] = text(333, 217)

	grad := image.NewGray(image.Rect(0, 0, 256, 100))
	for y := range 100 {
		for x := range 256 {
			grad.SetGray(x, y, color.Gray{uint8(x)})
		}
	}
	imgs["gradient"] = grad

	noise := image.NewGray(image.Rect(0, 0, 97, 61))
	for i := range noise.Pix {
		noise.Pix[i] = uint8(r.IntN(256))
	}
	imgs["noise"] = noise

	flat := image.NewGray(image.Rect(0, 0, 40, 40))
	for i := range flat.Pix {
		flat.Pix[i] = 200
	}
	imgs["flat"] = flat
	imgs["1x1"] = image.NewGray(image.Rect(0, 0, 1, 1))

	colour := image.NewRGBA(image.Rect(0, 0, 301, 199))
	for y := range 199 {
		for x := range 301 {
			c := color.RGBA{uint8(x), uint8(y), 128, 255}
			if (x/20+y/20)%3 == 0 {
				c = color.RGBA{0xCC, 0, 0, 255} // red "highlight" blocks
			}
			colour.SetRGBA(x, y, c)
		}
	}
	imgs["colour 301x199"] = colour

	cnoise := image.NewRGBA(image.Rect(0, 0, 50, 35))
	for i := range cnoise.Pix {
		cnoise.Pix[i] = uint8(r.IntN(256))
		if i%4 == 3 {
			cnoise.Pix[i] = 255
		}
	}
	imgs["colour noise"] = cnoise
	imgs["colour 1x1"] = image.NewRGBA(image.Rect(0, 0, 1, 1))

	// A sub-image whose bounds do not start at the origin.
	imgs["sub-image"] = text(200, 200).SubImage(image.Rect(13, 21, 170, 190))
	return imgs
}

func encode(t *testing.T, m image.Image, q int, optimise bool) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := encodeJPEGWith(&b, m, q, optimise); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func decode(t *testing.T, data []byte) image.Image {
	t.Helper()
	m, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decoding: %v", err)
	}
	return m
}

func samePixels(a, b image.Image) bool {
	if a.Bounds() != b.Bounds() {
		return false
	}
	for y := a.Bounds().Min.Y; y < a.Bounds().Max.Y; y++ {
		for x := a.Bounds().Min.X; x < a.Bounds().Max.X; x++ {
			if a.At(x, y) != b.At(x, y) {
				return false
			}
		}
	}
	return true
}

// The optimisation is purely in the entropy coding, so it must not change a
// single decoded pixel, and it must never make a file larger.
func TestOptimisedHuffmanIsLossless(t *testing.T) {
	for name, m := range testImages() {
		for _, q := range []int{1, 50, 75, 92, 100} {
			std := encode(t, m, q, false)
			opt := encode(t, m, q, true)
			if !samePixels(decode(t, std), decode(t, opt)) {
				t.Errorf("%s q%d: optimised tables changed the decoded pixels", name, q)
			}
			if len(opt) > len(std) {
				t.Errorf("%s q%d: optimised %d bytes > standard %d bytes", name, q, len(opt), len(std))
			}
		}
	}
}

func psnr(a, b image.Image) float64 {
	var se float64
	var n int
	for y := a.Bounds().Min.Y; y < a.Bounds().Max.Y; y++ {
		for x := a.Bounds().Min.X; x < a.Bounds().Max.X; x++ {
			ar, ag, ab, _ := a.At(x, y).RGBA()
			br, bg, bb, _ := b.At(x-a.Bounds().Min.X+b.Bounds().Min.X, y-a.Bounds().Min.Y+b.Bounds().Min.Y).RGBA()
			for _, d := range []float64{float64(ar>>8) - float64(br>>8), float64(ag>>8) - float64(bg>>8), float64(ab>>8) - float64(bb>>8)} {
				se += d * d
				n++
			}
		}
	}
	if se == 0 {
		return math.Inf(1)
	}
	return 10 * math.Log10(255*255/(se/float64(n)))
}

// Against image/jpeg at the same quality: at least as faithful, and smaller.
func TestBeatsStandardLibrary(t *testing.T) {
	for name, m := range testImages() {
		if m.Bounds().Dx() < 32 {
			continue // too small for size comparisons to mean anything
		}
		var lib bytes.Buffer
		if err := jpeg.Encode(&lib, m, &jpeg.Options{Quality: 92}); err != nil {
			t.Fatal(err)
		}
		ours := encode(t, m, 92, true)
		pl, po := psnr(m, decode(t, lib.Bytes())), psnr(m, decode(t, ours))
		if po < pl-0.05 {
			t.Errorf("%s: PSNR %.2f dB, image/jpeg %.2f dB", name, po, pl)
		}
		if len(ours) >= lib.Len() && name != "noise" && name != "colour noise" {
			t.Errorf("%s: %d bytes, not smaller than image/jpeg's %d", name, len(ours), lib.Len())
		}
		t.Logf("%-16s %6d -> %6d bytes (%+.1f%%), PSNR %.2f vs %.2f dB", name, lib.Len(), len(ours),
			100*float64(len(ours)-lib.Len())/float64(lib.Len()), po, pl)
	}
}

func TestEncodesGreyAsOneComponent(t *testing.T) {
	m := decode(t, encode(t, testImages()["text 600x400"], 92, true))
	if _, ok := m.(*image.Gray); !ok {
		t.Errorf("greyscale input decoded as %T, want *image.Gray", m)
	}
}

// Code lengths must fit in 16 bits, leave the all-ones code unused, and give
// every symbol that occurs a prefix-free code.
func TestOptimalTableValid(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	cases := map[string]*[256]int64{}
	add := func(name string, f func(i int) int64) {
		var fr [256]int64
		for i := range fr {
			fr[i] = f(i)
		}
		cases[name] = &fr
	}
	add("single symbol", func(i int) int64 { return map[bool]int64{true: 9}[i == 7] })
	add("two symbols", func(i int) int64 { return map[bool]int64{true: 5}[i == 0 || i == 200] })
	add("uniform", func(int) int64 { return 1 })
	add("random", func(int) int64 { return r.Int64N(1000) })
	// Fibonacci frequencies give a maximally skewed tree, far deeper than 16.
	fib := [256]int64{1, 1}
	for i := 2; i < 40; i++ {
		fib[i] = fib[i-1] + fib[i-2]
	}
	add("fibonacci", func(i int) int64 { return fib[i] })

	for name, freq := range cases {
		h := optimalTable(freq)
		total, kraft := 0, 0.0
		for l, c := range h.counts {
			total += int(c)
			kraft += float64(c) / float64(uint(1)<<(l+1))
		}
		if total != len(h.values) {
			t.Errorf("%s: %d codes for %d values", name, total, len(h.values))
		}
		if kraft >= 1 {
			t.Errorf("%s: Kraft sum %v uses the reserved all-ones code", name, kraft)
		}
		type cw struct {
			code uint16
			size uint8
		}
		var codes []cw
		for s, f := range freq {
			if f == 0 {
				continue
			}
			if h.size[s] == 0 || h.size[s] > 16 {
				t.Errorf("%s: symbol %d has code length %d", name, s, h.size[s])
			}
			codes = append(codes, cw{h.code[s], h.size[s]})
		}
		sort.Slice(codes, func(i, j int) bool { return codes[i].size < codes[j].size })
		for i := range codes {
			for j := i + 1; j < len(codes); j++ {
				if codes[j].code>>(codes[j].size-codes[i].size) == codes[i].code {
					t.Errorf("%s: code %b is a prefix of %b", name, codes[i].code, codes[j].code)
				}
			}
		}
	}
}

func TestRejectsBadSizes(t *testing.T) {
	var b bytes.Buffer
	if err := encodeJPEG(&b, image.NewGray(image.Rect(0, 0, 0, 5)), 90); err == nil {
		t.Error("expected an error for an empty image")
	}
}

// Luminance blocks that lie wholly outside a colour image only complete the
// last MCU. They must be coded as cheaply as possible: the previous DC and no
// AC terms, rather than replicated edge pixels.
func TestDummyBlocksAreMinimal(t *testing.T) {
	m := testImages()["colour noise"] // 50x35: the last luma column and row are dummies
	q := quantTables(92)
	s := transform(m, &q)
	w, h := m.Bounds().Dx(), m.Bounds().Dy()
	mw := (w + 15) / 16
	dummies, lastDC := 0, int16(0)
	for blk := range len(s.table) {
		if s.table[blk] != 0 {
			continue
		}
		// Recover this luma block's position from its place in the scan.
		mcu, k := blk/6, blk%6
		ox, oy := (mcu%mw)*16+(k&1)*8, (mcu/mw)*16+(k>>1)*8
		c := s.coef[64*blk : 64*blk+64]
		if ox >= w || oy >= h {
			dummies++
			if c[0] != lastDC {
				t.Errorf("dummy block at %d,%d has DC %d, want the previous %d", ox, oy, c[0], lastDC)
			}
			for _, v := range c[1:] {
				if v != 0 {
					t.Fatalf("dummy block at %d,%d has AC terms", ox, oy)
				}
			}
		}
		lastDC = c[0]
	}
	if dummies == 0 {
		t.Fatal("expected dummy blocks for a 50x35 image")
	}
}

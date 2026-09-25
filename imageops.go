package main

// Page image processing: fit, pad, greyscale, background flattening.
//
// Order matters: scale to fit, pad to the canvas, convert colour, flatten.

import (
	"fmt"
	"image"
	"image/color"

	"golang.org/x/image/draw"
)

// RGB is an opaque 8-bit colour.
type RGB struct{ R, G, B uint8 }

func (c RGB) Hex() string { return fmt.Sprintf("#%02X%02X%02X", c.R, c.G, c.B) }

var white = RGB{255, 255, 255}

// luma is Rec.709 luma on the gamma-encoded values, which preserves perceived
// lightness. Converting in linear light instead drags mid tones much darker
// (an orange header bar goes from 170 to 111), which costs legibility on
// e-ink.
func luma(r, g, b uint8) uint8 {
	return uint8((2126*uint32(r) + 7152*uint32(g) + 722*uint32(b) + 5000) / 10000)
}

func at(img *image.RGBA, x, y int) RGB {
	i := img.PixOffset(x, y)
	p := img.Pix[i : i+3 : i+3]
	return RGB{p[0], p[1], p[2]}
}

// modeColour returns the most common colour in r and how many pixels had it.
// Ties go to the lighter colour so the result is deterministic.
func modeColour(img *image.RGBA, r image.Rectangle) (RGB, int) {
	r = r.Intersect(img.Bounds())
	counts := make(map[RGB]int)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			counts[at(img, x, y)]++
		}
	}
	return pickMode(counts)
}

func pickMode(counts map[RGB]int) (RGB, int) {
	best, n := white, -1
	for c, k := range counts {
		if k > n || (k == n && lumaOf(c) > lumaOf(best)) {
			best, n = c, k
		}
	}
	return best, max(n, 0)
}

func lumaOf(c RGB) uint8 { return luma(c.R, c.G, c.B) }

// dominantColour samples a 100x100 nearest-neighbour grid, which never invents
// a colour that is not already in the page, and returns the most common colour
// with its coverage as a whole percentage.
func dominantColour(img *image.RGBA) (RGB, int) {
	b := img.Bounds()
	const n = 100
	counts := make(map[RGB]int)
	for j := range n {
		y := b.Min.Y + (2*j+1)*b.Dy()/(2*n)
		for i := range n {
			x := b.Min.X + (2*i+1)*b.Dx()/(2*n)
			counts[at(img, x, y)]++
		}
	}
	c, k := pickMode(counts)
	return c, k * 100 / (n * n)
}

// padColour picks the letterbox colour from the edge that will actually be
// padded, inset slightly. The outermost pixel is unreliable: a PDF media box
// often has a stray one-pixel white edge.
func padColour(img *image.RGBA, canvas Size) RGB {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()

	// Cross-multiply to compare aspect ratios in integers.
	switch lhs, rhs := w*canvas.H, h*canvas.W; {
	case lhs < rhs: // relatively taller: fits by height, bars left and right
		in := w/50 + 1
		c, _ := modeColour(img, image.Rect(in, in, in+1, h-in).Add(b.Min))
		return c
	case lhs > rhs: // relatively wider: fits by width, bars top and bottom
		in := h/50 + 1
		c, _ := modeColour(img, image.Rect(in, in, w-in, in+1).Add(b.Min))
		return c
	default: // same aspect ratio: nothing is padded
		return white
	}
}

// fitSize is the largest size with src's aspect ratio that fits in box,
// rounded to the nearest pixel.
func fitSize(src, box Size) Size {
	sx := float64(box.W) / float64(src.W)
	sy := float64(box.H) / float64(src.H)
	s := min(sx, sy)
	return Size{
		max(1, min(box.W, int(float64(src.W)*s+0.5))),
		max(1, min(box.H, int(float64(src.H)*s+0.5))),
	}
}

// fitAndPad scales src to fit inside canvas with its aspect ratio intact, then
// pads it out to exactly canvas, centred. Nothing is cropped or stretched.
func fitAndPad(src *image.RGBA, canvas Size, pad RGB) *image.RGBA {
	b := src.Bounds()
	if b.Dx() == canvas.W && b.Dy() == canvas.H && b.Min == (image.Point{}) {
		return src
	}

	dst := image.NewRGBA(image.Rect(0, 0, canvas.W, canvas.H))
	fill := color.RGBA{pad.R, pad.G, pad.B, 255}
	draw.Draw(dst, dst.Bounds(), &image.Uniform{fill}, image.Point{}, draw.Src)

	fit := fitSize(Size{b.Dx(), b.Dy()}, canvas)
	off := image.Pt((canvas.W-fit.W)/2, (canvas.H-fit.H)/2)
	r := image.Rectangle{off, off.Add(image.Pt(fit.W, fit.H))}

	if fit.W == b.Dx() && fit.H == b.Dy() {
		draw.Draw(dst, r, src, b.Min, draw.Src)
	} else {
		draw.CatmullRom.Scale(dst, r, src, b, draw.Src, nil)
	}
	return dst
}

// toGray converts to 8-bit greyscale with Rec.709 luma.
func toGray(src *image.RGBA) *image.Gray {
	b := src.Bounds()
	dst := image.NewGray(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := range b.Dy() {
		s := src.Pix[src.PixOffset(b.Min.X, b.Min.Y+y):]
		d := dst.Pix[y*dst.Stride : y*dst.Stride+b.Dx()]
		for x := range d {
			d[x] = luma(s[4*x], s[4*x+1], s[4*x+2])
		}
	}
	return dst
}

// Flatten describes how a tinted background is forced to white.
type Flatten struct {
	Active bool
	Colour RGB // detected background
	Grey   uint8
	Cover  int // percent of the page
}

// detectFlatten decides whether the dominant colour really is a background:
// it must cover at least a quarter of the page and be light enough that
// whitening it makes sense. Dark themes are left alone, because "removing"
// that background would mean inverting the page.
func detectFlatten(src *image.RGBA) Flatten {
	c, cover := dominantColour(src)
	g := lumaOf(c)
	return Flatten{
		Active: cover >= 25 && g >= 120 && g <= 250,
		Colour: c, Grey: g, Cover: cover,
	}
}

// whitePoint is the flattening threshold: 8 levels below the background
// grey, as a whole percent.
// Pixels strictly above it become white; everything else is untouched.
func (f Flatten) whitePoint() int { return (int(f.Grey) - 8) * 100 / 255 }

// flattenGray applies the white point to a greyscale page. On a single
// channel this is exact and leaves every tone below it bit-for-bit unchanged.
func flattenGray(img *image.Gray, f Flatten) {
	pct := f.whitePoint()
	for i, v := range img.Pix {
		if int(v)*100 > pct*255 {
			img.Pix[i] = 255
		}
	}
}

// flattenRGB whitens pixels near the background colour. Thresholding each
// channel separately would shift the hue of a tinted background (pale blue
// grey turns cyan), so this matches the colour itself: pixels whose RMS
// channel distance from the background is within 15% become white.
func flattenRGB(img *image.RGBA, f Flatten) {
	fuzz := 0.15 * 255
	limit := int(3 * fuzz * fuzz)
	bg := f.Colour
	for i := 0; i+3 < len(img.Pix); i += 4 {
		dr := int(img.Pix[i]) - int(bg.R)
		dg := int(img.Pix[i+1]) - int(bg.G)
		db := int(img.Pix[i+2]) - int(bg.B)
		if dr*dr+dg*dg+db*db <= limit {
			img.Pix[i], img.Pix[i+1], img.Pix[i+2] = 255, 255, 255
		}
	}
}

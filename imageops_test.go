package main

import (
	"image"
	"image/color"
	"testing"
)

func TestLuma(t *testing.T) {
	// Rec.709 luma on gamma-encoded values.
	cases := []struct {
		c    RGB
		want uint8
	}{
		{RGB{0xCC, 0x00, 0x00}, 43},  // red highlighted text
		{RGB{0xE8, 0xA3, 0x3D}, 170}, // orange header bar
		{RGB{0x1F, 0x6F, 0xB5}, 99},  // blue
		{RGB{0x1E, 0x84, 0x49}, 106}, // green
		{RGB{0xD0, 0xE0, 0xE3}, 221}, // pale blue-grey page tint (220.8)
		{RGB{0, 0, 0}, 0},
		{RGB{255, 255, 255}, 255},
	}
	for _, c := range cases {
		if got := lumaOf(c.c); got != c.want {
			t.Errorf("luma(%s) = %d, want %d", c.c.Hex(), got, c.want)
		}
	}
}

func TestFitSizePreservesAspect(t *testing.T) {
	cases := []struct{ src, box, want Size }{
		{Size{3514, 2025}, Size{2560, 1366}, Size{2370, 1366}},
		{Size{3795, 2025}, Size{2560, 1366}, Size{2560, 1366}},
		{Size{1000, 500}, Size{400, 400}, Size{400, 200}},
		{Size{500, 1000}, Size{400, 400}, Size{200, 400}},
	}
	for _, c := range cases {
		if got := fitSize(c.src, c.box); got != c.want {
			t.Errorf("fitSize(%v, %v) = %v, want %v", c.src, c.box, got, c.want)
		}
	}
}

func solid(w, h int, c RGB) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, 255
	}
	return img
}

func TestFitAndPadLetterboxes(t *testing.T) {
	grey := RGB{200, 200, 200}
	src := solid(300, 300, RGB{0, 0, 0})
	out := fitAndPad(src, Size{400, 300}, grey)
	if b := out.Bounds(); b.Dx() != 400 || b.Dy() != 300 {
		t.Fatalf("output is %v, want 400x300", b)
	}
	// 300x300 centred in 400x300 leaves 50px bars left and right.
	if c := at(out, 10, 150); c != grey {
		t.Errorf("left bar is %v, want pad colour %v", c, grey)
	}
	if c := at(out, 390, 150); c != grey {
		t.Errorf("right bar is %v, want pad colour %v", c, grey)
	}
	if c := at(out, 200, 150); c != (RGB{}) {
		t.Errorf("centre is %v, want the page content", c)
	}
}

func TestPadColourSkipsStrayEdge(t *testing.T) {
	// Background tint with a one-pixel white frame, as PDF media boxes often
	// have. The sampled colour must be the tint, not the frame.
	tint := RGB{0xD0, 0xE0, 0xE3}
	img := solid(300, 600, tint)
	for x := range 300 {
		img.Set(x, 0, color.White)
		img.Set(x, 599, color.White)
	}
	for y := range 600 {
		img.Set(0, y, color.White)
		img.Set(299, y, color.White)
	}
	// Portrait page on a landscape canvas: bars go left and right.
	if got := padColour(img, Size{800, 600}); got != tint {
		t.Errorf("padColour = %s, want %s", got.Hex(), tint.Hex())
	}
}

func TestFlattenGrayKeepsText(t *testing.T) {
	tint := RGB{0xD0, 0xE0, 0xE3}
	img := solid(200, 200, tint)
	// Dark "text" block covering well under a quarter of the page.
	for y := 80; y < 120; y++ {
		for x := 20; x < 180; x++ {
			img.Set(x, y, color.RGBA{30, 30, 30, 255})
		}
	}
	f := detectFlatten(img)
	if !f.Active || f.Colour != tint {
		t.Fatalf("detectFlatten = %+v, want the tint detected as background", f)
	}
	g := toGray(img)
	flattenGray(g, f)
	if v := g.GrayAt(5, 5).Y; v != 255 {
		t.Errorf("background became %d, want 255", v)
	}
	if v := g.GrayAt(100, 100).Y; v != 30 {
		t.Errorf("text became %d, want it untouched at 30", v)
	}
}

func TestFlattenSkipsWhiteAndDark(t *testing.T) {
	if f := detectFlatten(solid(100, 100, white)); f.Active {
		t.Error("an already white page should not be flattened")
	}
	if f := detectFlatten(solid(100, 100, RGB{20, 20, 40})); f.Active {
		t.Error("a dark theme should not be flattened")
	}
}

func TestFlattenRGBKeepsHue(t *testing.T) {
	tint := RGB{0xD0, 0xE0, 0xE3}
	img := solid(10, 10, tint)
	img.Set(5, 5, color.RGBA{0xCC, 0, 0, 255})
	flattenRGB(img, Flatten{Active: true, Colour: tint})
	if c := at(img, 0, 0); c != white {
		t.Errorf("background became %s, want white (not a tinted cyan)", c.Hex())
	}
	if c := at(img, 5, 5); c != (RGB{0xCC, 0, 0}) {
		t.Errorf("red text became %s, want it untouched", c.Hex())
	}
}

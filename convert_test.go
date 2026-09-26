package main

import (
	"archive/zip"
	"bytes"
	"context"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// convertPDF runs the whole pipeline on a synthetic PDF at a low DPI.
func convertPDF(t *testing.T, pages []testPage, extra ...string) (Options, []byte) {
	t.Helper()
	return convertPDFAt(t, pages, "36", extra...)
}

func convertPDFAt(t *testing.T, pages []testPage, dpi string, extra ...string) (Options, []byte) {
	t.Helper()
	return convertBytes(t, makePDF(pages), dpi, extra...)
}

func convertBytes(t *testing.T, pdf []byte, dpi string, extra ...string) (Options, []byte) {
	t.Helper()
	if testing.Short() {
		t.Skip("starts the PDF engine; skipped with -short")
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "in.pdf")
	out := filepath.Join(dir, "out.epub")
	if err := os.WriteFile(in, pdf, 0o644); err != nil {
		t.Fatal(err)
	}
	// Low DPI keeps the test fast; --no-epubcheck keeps it independent of
	// what happens to be installed.
	args := append([]string{"--dpi", dpi, "--no-epubcheck", "--jobs", "2", "--max-edge", "0"}, extra...)
	o, err := parseArgs(append(args, in, out))
	if err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	ok, err := convert(context.Background(), o, &log)
	if err != nil {
		t.Fatalf("convert: %v\n%s", err, log.String())
	}
	if !ok {
		t.Fatalf("validation failed:\n%s", log.String())
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return o, data
}

// convertErr converts a PDF and returns the error, for failures that are
// expected.
func convertErr(t *testing.T, pdf []byte, extra ...string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	in, out := filepath.Join(dir, "in.pdf"), filepath.Join(dir, "out.epub")
	if err := os.WriteFile(in, pdf, 0o644); err != nil {
		t.Fatal(err)
	}
	args := append([]string{"--dpi", "36", "--no-epubcheck", "--jobs", "1"}, extra...)
	o, err := parseArgs(append(args, in, out))
	if err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	_, err = convert(context.Background(), o, &log)
	return log.String(), err
}

func zipFile(t *testing.T, data []byte, name string) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if f.Name == name {
			b, err := readZip(f)
			if err != nil {
				t.Fatal(err)
			}
			return b
		}
	}
	t.Fatalf("%s not in archive", name)
	return nil
}

func pageImages(t *testing.T, data []byte) []Size {
	t.Helper()
	zr, _ := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	var sizes []Size
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "OEBPS/images/") {
			b, _ := readZip(f)
			cfg, err := jpeg.DecodeConfig(bytes.NewReader(b))
			if err != nil {
				t.Fatalf("%s: %v", f.Name, err)
			}
			sizes = append(sizes, Size{cfg.Width, cfg.Height})
		}
	}
	return sizes
}

func TestConvertLandscape(t *testing.T) {
	_, data := convertPDF(t, []testPage{{W: 960, H: 540}, {W: 960, H: 540}, {W: 960, H: 540}})
	opf := string(zipFile(t, data, "OEBPS/content.opf"))
	for _, want := range []string{
		`<meta property="rendition:layout">pre-paginated</meta>`,
		`<meta property="rendition:orientation">landscape</meta>`,
		`<meta name="orientation-lock" content="landscape"/>`,
		`<meta name="original-resolution" content="480x270"/>`,
		`properties="cover-image"`,
		`page-progression-direction="ltr"`,
	} {
		if !strings.Contains(opf, want) {
			t.Errorf("content.opf is missing %s", want)
		}
	}
}

// A portrait MediaBox with /Rotate 90 displays as landscape and must be
// treated as landscape.
func TestConvertRotatedPage(t *testing.T) {
	_, data := convertPDF(t, []testPage{{W: 540, H: 960, Rotate: 90}, {W: 540, H: 960, Rotate: 90}})
	for _, s := range pageImages(t, data) {
		if s != (Size{480, 270}) {
			t.Errorf("rotated page is %v, want 480x270 landscape", s)
		}
	}
	if opf := string(zipFile(t, data, "OEBPS/content.opf")); !strings.Contains(opf, `content="landscape"`) {
		t.Error("rotated pages were not locked to landscape")
	}
}

// Pages of differing widths all land on one canvas.
func TestConvertVaryingSizesShareCanvas(t *testing.T) {
	_, data := convertPDF(t, []testPage{{W: 960, H: 540}, {W: 960, H: 540}, {W: 960, H: 540}, {W: 900, H: 540}, {W: 880, H: 540}})
	sizes := pageImages(t, data)
	if len(sizes) != 5 {
		t.Fatalf("got %d pages, want 5", len(sizes))
	}
	for _, s := range sizes {
		if s != sizes[0] {
			t.Fatalf("pages have differing sizes: %v", sizes)
		}
	}
}

func TestConvertMixedKeepsPageSizes(t *testing.T) {
	_, data := convertPDF(t, []testPage{{W: 960, H: 540}, {W: 540, H: 960}}, "--mixed")
	sizes := pageImages(t, data)
	if len(sizes) != 2 || sizes[0] != (Size{480, 270}) || sizes[1] != (Size{270, 480}) {
		t.Errorf("mixed mode sizes = %v, want [480x270 270x480]", sizes)
	}
}

func TestConvertGrayscaleFlattensTint(t *testing.T) {
	tint := RGB{0xD0, 0xE0, 0xE3}
	_, data := convertPDF(t, []testPage{{W: 612, H: 792, BG: &tint, Bar: true}}, "--grayscale", "--flatten-bg")
	img, err := jpeg.Decode(bytes.NewReader(zipFile(t, data, "OEBPS/images/page-001.jpg")))
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	corner, _, _, _ := img.At(b.Min.X+5, b.Max.Y-5).RGBA()
	bar, _, _, _ := img.At(b.Dx()/2, b.Dy()*25/100).RGBA()
	if corner>>8 < 250 {
		t.Errorf("background is %d after flattening, want white", corner>>8)
	}
	if bar>>8 > 40 {
		t.Errorf("black bar is %d after flattening, want it kept dark", bar>>8)
	}
}

func TestConvertRejectsNonPDF(t *testing.T) {
	if testing.Short() {
		t.Skip("starts the PDF engine; skipped with -short")
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "in.pdf")
	os.WriteFile(in, []byte("not a pdf"), 0o644)
	o, _ := parseArgs([]string{"--no-epubcheck", in, filepath.Join(dir, "out.epub")})
	if _, err := convert(context.Background(), o, io.Discard); err == nil {
		t.Fatal("expected an error for a file that is not a PDF")
	}
	if _, err := os.Stat(filepath.Join(dir, "out.epub")); err == nil {
		t.Error("no output should be written when conversion fails")
	}
}

// A PDF whose pages are each one full-page image is treated as a scan, and
// automatic resolution renders it at the images' own resolution.
func TestAutoDPIUsesScanResolution(t *testing.T) {
	// 612 x 792 pt (US Letter) holding 1275 x 1650 px images is 150 ppi.
	scan := &Size{1275, 1650}
	pages := []testPage{{W: 612, H: 792, Scan: scan}, {W: 612, H: 792, Scan: scan}, {W: 612, H: 792, Scan: scan}}
	_, data := convertPDFAt(t, pages, "auto")
	// 1275 x 1650 at 150 DPI; the canvas rounds odd sizes down to even.
	for _, s := range pageImages(t, data) {
		if s != (Size{1274, 1650}) {
			t.Errorf("scan rendered at %v, want 1274x1650 (150 DPI)", s)
		}
	}
}

// Pages with text are not scans, so automatic resolution uses the default.
func TestAutoDPIKeepsDefaultForText(t *testing.T) {
	_, data := convertPDFAt(t, []testPage{{W: 612, H: 792, Text: true}}, "auto")
	want := Size{1530, 1980} // 612 x 792 pt at 180 DPI
	for _, s := range pageImages(t, data) {
		if s != want {
			t.Errorf("text page rendered at %v, want %v", s, want)
		}
	}
}

// Covers are often scanned finer than the body. The body's resolution wins.
func TestAutoDPIIgnoresFinerCovers(t *testing.T) {
	cover, body := &Size{2550, 3300}, &Size{1275, 1650} // 300 and 150 ppi
	pages := []testPage{{W: 612, H: 792, Scan: cover}}
	for range 6 {
		pages = append(pages, testPage{W: 612, H: 792, Scan: body})
	}
	pages = append(pages, testPage{W: 612, H: 792, Scan: cover})
	_, data := convertPDFAt(t, pages, "auto")
	for _, s := range pageImages(t, data) {
		if s != (Size{1274, 1650}) {
			t.Fatalf("rendered at %v, want the body's 150 DPI (1274x1650)", s)
		}
	}
}

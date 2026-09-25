package main

// PDF rendering with PDFium compiled to WebAssembly.
//
// There is no PDF rasteriser in Go's standard library, and one that renders
// embedded Nastaleeq and Arabic fonts correctly is far too large to write.
// go-pdfium ships PDFium as a .wasm module that runs inside this process on
// wazero, a pure-Go WebAssembly runtime. There is no cgo and no system
// library, so the binary stays static and cross-compiles.

import (
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"math"
	"sort"
	"time"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/enums"
	pdferr "github.com/klippa-app/go-pdfium/errors"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
	"github.com/tetratelabs/wazero"
)

// engine is a pool of PDFium instances. Starting it compiles the WebAssembly
// module, which takes a few seconds; instances made from it afterwards are
// cheap. So the GUI keeps one engine for its whole life, while every document
// still gets fresh instances and nothing carries over between books.
type engine struct {
	pool    pdfium.Pool
	workers int
}

func newEngine(ctx context.Context, workers int) (*engine, error) {
	pool, err := webassembly.Init(webassembly.Config{
		Context:  ctx,
		MinIdle:  1,
		MaxIdle:  workers,
		MaxTotal: workers,

		// Mount nothing. go-pdfium mounts the whole host filesystem into the
		// sandbox by default; the PDF is passed in memory, so it needs none.
		FSConfig: wazero.NewFSConfig(),
		Stdout:   io.Discard,
		Stderr:   io.Discard,
	})
	if err != nil {
		return nil, fmt.Errorf("starting PDF engine: %w", err)
	}
	return &engine{pool: pool, workers: workers}, nil
}

func (e *engine) Close() error { return e.pool.Close() }

// worker is one PDFium instance with the document open. Instances are not
// safe for concurrent use, so each goroutine owns one.
type worker struct {
	inst pdfium.Pdfium
	doc  references.FPDF_DOCUMENT
}

// newWorker takes an instance from the pool and opens pdf in it. Close
// returns the instance to the pool.
func (e *engine) newWorker(pdf []byte) (*worker, error) {
	inst, err := e.pool.GetInstance(5 * time.Minute)
	if err != nil {
		return nil, fmt.Errorf("starting PDF engine instance: %w", err)
	}
	doc, err := inst.OpenDocument(&requests.OpenDocument{File: &pdf})
	if err != nil {
		inst.Close()
		return nil, describeOpenError(err)
	}
	return &worker{inst: inst, doc: doc.Document}, nil
}

func describeOpenError(err error) error {
	switch {
	case errors.Is(err, pdferr.ErrPassword):
		return errors.New("the PDF is password protected")
	case errors.Is(err, pdferr.ErrFormat):
		return errors.New("the file is not a valid PDF, or is damaged")
	case errors.Is(err, pdferr.ErrSecurity):
		return errors.New("the PDF uses an unsupported security handler")
	}
	return fmt.Errorf("opening PDF: %w", err)
}

func (w *worker) Close() {
	w.inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: w.doc})
	w.inst.Close()
}

func (w *worker) page(i int) requests.Page {
	return requests.Page{ByIndex: &requests.PageByIndex{Document: w.doc, Index: i}}
}

func (w *worker) pageCount() (int, error) {
	res, err := w.inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: w.doc})
	if err != nil {
		return 0, fmt.Errorf("counting pages: %w", err)
	}
	return res.PageCount, nil
}

// pageSize asks PDFium for the pixel size it will render page i at. Using
// PDFium's own calculation rather than repeating it guarantees the canvas is
// sized from exactly what gets rendered. The size includes any /Rotate.
func (w *worker) pageSize(i, dpi int) (Size, error) {
	res, err := w.inst.GetPageSizeInPixels(&requests.GetPageSizeInPixels{Page: w.page(i), DPI: dpi})
	if err != nil {
		return Size{}, fmt.Errorf("page %d: measuring: %w", i+1, err)
	}
	return Size{res.Width, res.Height}, nil
}

// render draws page i at dpi and returns an image owned by Go.
func (w *worker) render(i, dpi int) (*image.RGBA, error) {
	res, err := w.inst.RenderPageInDPI(&requests.RenderPageInDPI{DPI: dpi, Page: w.page(i)})
	if err != nil {
		return nil, fmt.Errorf("page %d: rendering: %w", i+1, err)
	}
	defer res.Cleanup()

	// The returned Pix is a view into the WebAssembly heap, not a copy. It is
	// invalidated by Cleanup and can be moved by the next call that grows the
	// heap, so copy it out before doing anything else.
	src := res.Result.Image
	img := &image.RGBA{
		Pix:    make([]byte, len(src.Pix)),
		Stride: src.Stride,
		Rect:   src.Rect,
	}
	copy(img.Pix, src.Pix)
	return img, nil
}

// scanPPI reports whether the document looks like a scan and, if so, the
// native resolution of its page images. A page counts as scanned when it
// holds exactly one image, covering at least 90% of the page, and no text.
// Nine pages spread through the book are sampled, and all must be scans. The
// resolution is their median: covers are often scanned finer than the body,
// and should not decide the resolution of every page.
func (w *worker) scanPPI(pages int) (int, bool) {
	const samples = 9
	var idx []int
	if pages <= samples {
		for i := range pages {
			idx = append(idx, i)
		}
	} else {
		for k := range samples {
			idx = append(idx, k*(pages-1)/(samples-1))
		}
	}

	var ppis []float64
	for _, i := range idx {
		ppi, ok := w.pageScanPPI(i)
		if !ok {
			return 0, false
		}
		ppis = append(ppis, ppi)
	}
	sort.Float64s(ppis)
	med := ppis[len(ppis)/2]
	return int(math.Round(min(max(med, 72), 600))), true
}

func (w *worker) pageScanPPI(i int) (float64, bool) {
	page := w.page(i)
	cnt, err := w.inst.FPDFPage_CountObjects(&requests.FPDFPage_CountObjects{Page: page})
	if err != nil || cnt.Count == 0 {
		return 0, false
	}
	var img references.FPDF_PAGEOBJECT
	images := 0
	for k := range cnt.Count {
		obj, err := w.inst.FPDFPage_GetObject(&requests.FPDFPage_GetObject{Page: page, Index: k})
		if err != nil {
			return 0, false
		}
		typ, err := w.inst.FPDFPageObj_GetType(&requests.FPDFPageObj_GetType{PageObject: obj.PageObject})
		if err != nil {
			return 0, false
		}
		switch typ.Type {
		case enums.FPDF_PAGEOBJ_TEXT:
			return 0, false
		case enums.FPDF_PAGEOBJ_IMAGE:
			images++
			img = obj.PageObject
		}
	}
	if images != 1 {
		return 0, false
	}

	size, err := w.inst.FPDF_GetPageSizeByIndexF(&requests.FPDF_GetPageSizeByIndexF{Document: w.doc, Index: i})
	if err != nil {
		return 0, false
	}
	bounds, err := w.inst.FPDFPageObj_GetBounds(&requests.FPDFPageObj_GetBounds{PageObject: img})
	if err != nil {
		return 0, false
	}
	bw, bh := float64(bounds.Right-bounds.Left), float64(bounds.Top-bounds.Bottom)
	pw, ph := float64(size.Size.Width), float64(size.Size.Height)
	if bw <= 0 || bh <= 0 || bw*bh < 0.9*pw*ph {
		return 0, false
	}

	px, err := w.inst.FPDFImageObj_GetImagePixelSize(&requests.FPDFImageObj_GetImagePixelSize{ImageObject: img})
	if err != nil || px.Width == 0 || px.Height == 0 {
		return 0, false
	}
	// The image may be drawn rotated; pair its sides with the drawn sides
	// the way that gives the most consistent resolution.
	wIn, hIn := bw/72, bh/72
	a1, a2 := float64(px.Width)/wIn, float64(px.Height)/hIn
	b1, b2 := float64(px.Width)/hIn, float64(px.Height)/wIn
	if math.Abs(a1-a2) <= math.Abs(b1-b2) {
		return (a1 + a2) / 2, true
	}
	return (b1 + b2) / 2, true
}

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
	"time"

	"github.com/klippa-app/go-pdfium"
	pdferr "github.com/klippa-app/go-pdfium/errors"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
	"github.com/tetratelabs/wazero"
)

type renderer struct {
	pool pdfium.Pool
	pdf  []byte
}

func newRenderer(ctx context.Context, pdf []byte, workers int) (*renderer, error) {
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
	return &renderer{pool: pool, pdf: pdf}, nil
}

func (r *renderer) Close() error { return r.pool.Close() }

// worker is one PDFium instance with the document open. Instances are not
// safe for concurrent use, so each goroutine owns one.
type worker struct {
	inst pdfium.Pdfium
	doc  references.FPDF_DOCUMENT
}

func (r *renderer) newWorker() (*worker, error) {
	inst, err := r.pool.GetInstance(5 * time.Minute)
	if err != nil {
		return nil, fmt.Errorf("starting PDF engine instance: %w", err)
	}
	doc, err := inst.OpenDocument(&requests.OpenDocument{File: &r.pdf})
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

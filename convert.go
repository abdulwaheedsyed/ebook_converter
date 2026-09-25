package main

// The conversion pipeline, independent of how it is presented. The command
// line and the GUI both drive it and render its progress events their own way.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/image/draw"
)

// Stages reported while converting, in order.
const (
	stageMeasuring  = "measuring"
	stageRendering  = "rendering"
	stagePackaging  = "packaging"
	stageValidating = "validating"
	stageEpubcheck  = "epubcheck"
)

// Plan is what the converter decided once every page was measured.
type Plan struct {
	DPI    int  // resolution rendered at
	Scan   bool // the PDF is a scan and DPI is its native resolution
	Pages  int
	Tally  map[string]int // pages by orientation
	Canvas Size
	Orient Orientation
	Mixed  bool
}

// Event reports progress. Plan is set once, on the first rendering event.
type Event struct {
	Stage       string
	Done, Total int
	Plan        *Plan
}

// PageInfo describes one converted page.
type PageInfo struct {
	Source Size   // as rendered
	Size   Size   // as encoded
	Orient string // of the source page
}

// Result describes a finished conversion.
type Result struct {
	Plan
	PageInfo  []PageInfo
	Bytes     int
	Flattened int
	Elapsed   time.Duration
	Cover     []byte // small JPEG of the first page, for previews
	TOC       int    // entries taken from the PDF's outline; 0 lists every page

	Validated bool      // built-in validation ran
	Problems  []Problem // built-in findings
	Epubcheck Epubcheck
	Passed    bool // everything that ran passed
}

// convertBook converts o.Input to o.Output. It returns an error when no EPUB
// could be written; a written EPUB that fails validation is reported in the
// Result, and the file is kept so it can be inspected.
func convertBook(ctx context.Context, eng *engine, o Options, report func(Event)) (*Result, error) {
	start := time.Now()
	if report == nil {
		report = func(Event) {}
	}

	pdf, err := os.ReadFile(o.Input)
	if err != nil {
		return nil, err
	}

	report(Event{Stage: stageMeasuring})
	first, err := eng.newWorker(pdf)
	if err != nil {
		return nil, err
	}
	n, err := first.pageCount()
	if err != nil {
		first.Close()
		return nil, err
	}
	if n == 0 {
		first.Close()
		return nil, errors.New("the PDF has no pages")
	}

	plan := Plan{Pages: n, Tally: map[string]int{}, Mixed: o.Mixed, DPI: o.DPI}
	if o.DPI == dpiAuto {
		plan.DPI = defaultDPI
		if ppi, ok := first.scanPPI(n); ok {
			plan.DPI, plan.Scan = ppi, true
		}
	}
	o.DPI = plan.DPI

	// Measure every page before rendering any, so the canvas is known up
	// front and only the encoded pages need to be held in memory.
	sizes := make([]Size, n)
	for i := range n {
		if sizes[i], err = first.pageSize(i, o.DPI); err != nil {
			first.Close()
			return nil, err
		}
		plan.Tally[sizes[i].Orientation()]++
		// Measuring loads and parses each page, which takes a while on a
		// long book, so it reports progress too.
		report(Event{Stage: stageMeasuring, Done: i + 1, Total: n})
	}
	// A damaged outline is not worth failing the book over; it falls back
	// to one entry per page.
	var toc []TOCEntry
	if o.TOC == tocBookmarks {
		toc, _ = first.outline(n)
	}
	labels := first.pageLabels(n)

	plan.Canvas = chooseCanvas(sizes, o.MaxEdge)
	plan.Orient = bookOrientation(plan.Canvas, o.Orient, o.Mixed)
	report(Event{Stage: stageRendering, Total: n, Plan: &plan})

	pages, flattened, err := buildPages(ctx, eng, pdf, first, sizes, plan.Canvas, o, func(done int) {
		report(Event{Stage: stageRendering, Done: done, Total: n})
	})
	if err != nil {
		return nil, err
	}

	report(Event{Stage: stagePackaging})
	book := &Book{
		Title:     o.Title,
		Lang:      o.Lang,
		Direction: o.Direction,
		Orient:    plan.Orient,
		Canvas:    plan.Canvas,
		Mixed:     o.Mixed,
		Pages:     pages,
		TOC:       toc,
		Labels:    labels,
		Modified:  time.Now(),
		ID:        newUUID(),
	}
	data, err := writeAtomically(o.Output, book)
	if err != nil {
		return nil, err
	}

	res := &Result{
		Plan:      plan,
		Bytes:     len(data),
		Flattened: flattened,
		Cover:     pages[0].Thumb,
		TOC:       countEntries(toc),
		Passed:    true,
	}
	for _, p := range pages {
		res.PageInfo = append(res.PageInfo, PageInfo{p.Source, p.Size, p.Orient})
	}

	if o.Validate {
		report(Event{Stage: stageValidating})
		want := Expect{Pages: n, Grayscale: o.Grayscale}
		if !o.Mixed {
			want.Canvas = &plan.Canvas
		}
		res.Validated = true
		res.Problems = validatePackage(data, want)
		for _, p := range res.Problems {
			if p.Fails() {
				res.Passed = false
			}
		}

		if o.Epubcheck {
			report(Event{Stage: stageEpubcheck})
			res.Epubcheck = runEpubcheck(ctx, o.Output)
			if res.Epubcheck.Ran && !res.Epubcheck.Passed {
				res.Passed = false
			}
		}
	}
	res.Elapsed = time.Since(start)
	return res, nil
}

// buildPages renders, processes and encodes every page in parallel. first is
// an open worker, which becomes one of them.
func buildPages(ctx context.Context, eng *engine, pdf []byte, first *worker, sizes []Size, canvas Size, o Options, tick func(done int)) ([]Page, int, error) {
	n := len(sizes)
	pages := make([]Page, n)
	var flattened, done atomic.Int64
	var tickMu sync.Mutex

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	next := make(chan int)
	go func() {
		defer close(next)
		for i := range n {
			select {
			case next <- i:
			case <-ctx.Done():
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for k := range min(o.Jobs, n) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := first
			if k > 0 {
				var err error
				if w, err = eng.newWorker(pdf); err != nil {
					cancel(err)
					return
				}
			}
			defer w.Close()
			for i := range next {
				p, flat, err := buildPage(w, i, sizes[i], canvas, o)
				if err != nil {
					cancel(err)
					return
				}
				pages[i] = p
				if flat {
					flattened.Add(1)
				}
				tickMu.Lock() // keep reports in order and never concurrent
				tick(int(done.Add(1)))
				tickMu.Unlock()
			}
		}()
	}
	wg.Wait()

	if err := context.Cause(ctx); err != nil {
		return nil, 0, err
	}
	return pages, int(flattened.Load()), nil
}

func buildPage(w *worker, i int, size, canvas Size, o Options) (Page, bool, error) {
	img, err := w.render(i, o.DPI)
	if err != nil {
		return Page{}, false, err
	}
	if got := (Size{img.Rect.Dx(), img.Rect.Dy()}); got != size {
		return Page{}, false, fmt.Errorf("page %d rendered at %dx%d but measured %dx%d", i+1, got.W, got.H, size.W, size.H)
	}

	target := canvas
	if o.Mixed {
		target = size
	}

	// Detection and pad colour both look at the page as rendered, before it
	// is scaled, so neither is skewed by resampling.
	var fl Flatten
	if o.FlattenBG {
		fl = detectFlatten(img)
	}
	fitted := fitAndPad(img, target, padColour(img, target))

	var enc image.Image = fitted
	if o.Grayscale {
		g := toGray(fitted)
		if fl.Active {
			flattenGray(g, fl)
		}
		enc = g
	} else if fl.Active {
		flattenRGB(fitted, fl)
	}

	var buf bytes.Buffer
	if err := encodeJPEG(&buf, enc, o.Quality); err != nil {
		return Page{}, false, fmt.Errorf("page %d: encoding: %w", i+1, err)
	}
	p := Page{JPEG: buf.Bytes(), Size: target, Source: size, Orient: size.Orientation()}
	if i == 0 {
		p.Thumb = thumbnail(enc, 360)
	}
	return p, fl.Active, nil
}

// thumbnail is a small JPEG of m, at most maxW pixels wide.
func thumbnail(m image.Image, maxW int) []byte {
	b := m.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > maxW {
		w, h = maxW, max(1, h*maxW/w)
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), m, b, draw.Src, nil)
	var buf bytes.Buffer
	if encodeJPEG(&buf, dst, 82) != nil {
		return nil
	}
	return buf.Bytes()
}

// writeAtomically writes the EPUB next to its destination and renames it into
// place, so an interrupted run never leaves a half-written book behind or
// destroys the previous one. It returns the bytes written.
func writeAtomically(dst string, b *Book) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeEPUB(&buf, b); err != nil {
		return nil, fmt.Errorf("packaging: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".leafbind-*.tmp")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename

	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

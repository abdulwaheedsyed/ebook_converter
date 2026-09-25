// Command ebook_converter turns a PDF into a Kindle-compatible fixed-layout
// EPUB 3. Every page becomes an image on one shared canvas, so the original
// typesetting survives exactly. It suits scanned books and slide decks, where
// reflowing the text is not an option.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	o, err := parseArgs(args)
	switch {
	case errors.Is(err, errHelp):
		fmt.Fprint(stdout, usageText)
		return 0
	case errors.Is(err, errVersion):
		fmt.Fprintln(stdout, "ebook_converter", version)
		return 0
	case err != nil:
		fmt.Fprintf(stderr, "error: %v\n\n%s", err, usageText)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ok, err := convert(ctx, o, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if !ok {
		return 1
	}
	return 0
}

// convert runs the whole pipeline. It returns false when the EPUB was written
// but failed validation; the file is kept so it can be inspected.
func convert(ctx context.Context, o Options, out io.Writer) (bool, error) {
	start := time.Now()
	say := func(format string, args ...any) { fmt.Fprintf(out, format, args...) }

	pdf, err := os.ReadFile(o.Input)
	if err != nil {
		return false, err
	}

	say("Input       : %s\n", o.Input)
	say("Starting PDF engine...\n")

	r, err := newRenderer(ctx, pdf, o.Jobs)
	if err != nil {
		return false, err
	}
	defer r.Close()

	first, err := r.newWorker()
	if err != nil {
		return false, err
	}

	n, err := first.pageCount()
	if err != nil {
		first.Close()
		return false, err
	}
	if n == 0 {
		first.Close()
		return false, errors.New("the PDF has no pages")
	}

	// Measure every page before rendering any, so the canvas is known up
	// front and only the encoded pages need to be held in memory.
	sizes := make([]Size, n)
	tally := map[string]int{}
	for i := range n {
		if sizes[i], err = first.pageSize(i, o.DPI); err != nil {
			first.Close()
			return false, err
		}
		tally[sizes[i].Orientation()]++
	}

	canvas := chooseCanvas(sizes, o.MaxEdge)
	orient := bookOrientation(canvas, o.Orient, o.Mixed)

	colour := "sRGB"
	if o.Grayscale {
		colour = "greyscale"
	}
	mode := "normalised (one canvas)"
	if o.Mixed {
		mode = "mixed (per-page canvas)"
	}
	say("Pages       : %d (%d landscape, %d portrait, %d square)\n",
		n, tally["landscape"], tally["portrait"], tally["square"])
	say("Resolution  : %d DPI\n", o.DPI)
	if !o.Mixed {
		say("Canvas      : %d x %d\n", canvas.W, canvas.H)
	}
	say("Orientation : %s\n", orient.Book)
	say("Colour      : %s\n", colour)
	say("Layout      : %s\n", mode)

	pages, flattened, err := buildPages(ctx, r, first, sizes, canvas, o, out)
	if err != nil {
		return false, err
	}

	book := &Book{
		Title:     o.Title,
		Lang:      o.Lang,
		Direction: o.Direction,
		Orient:    orient,
		Canvas:    canvas,
		Mixed:     o.Mixed,
		Pages:     pages,
		Modified:  time.Now(),
		ID:        newUUID(),
	}
	data, err := writeAtomically(o.Output, book)
	if err != nil {
		return false, err
	}

	say("\nOutput      : %s\n", o.Output)
	say("Size        : %s\n", humanBytes(len(data)))
	if o.FlattenBG {
		say("Background  : %d of %d pages flattened to white\n", flattened, n)
	}
	say("Time        : %s\n", time.Since(start).Round(100*time.Millisecond))

	if !o.Validate {
		say("Validation  : skipped\n")
		return true, nil
	}

	want := Expect{Pages: n, Grayscale: o.Grayscale}
	if !o.Mixed {
		want.Canvas = &canvas
	}
	probs := validatePackage(data, want)
	passed := len(probs) == 0
	if passed {
		say("Validation  : passed\n")
	} else {
		say("Validation  : FAILED\n")
		lines := make([]string, len(probs))
		for i, p := range probs {
			lines[i] = p.String()
		}
		reportProblems(out, lines)
	}

	if o.Epubcheck {
		ec := runEpubcheck(ctx, o.Output)
		switch {
		case !ec.Ran:
			say("epubcheck   : not installed, skipped\n")
		case ec.Passed:
			say("epubcheck   : passed (%s)\n", ec.Summary)
		default:
			passed = false
			say("epubcheck   : FAILED (%s)\n", ec.Summary)
			reportProblems(out, ec.Problems)
		}
	}

	if !passed {
		say("\nThe EPUB was kept so it can be inspected.\n")
	}
	return passed, nil
}

// buildPages renders, processes and encodes every page in parallel.
func buildPages(ctx context.Context, r *renderer, first *worker, sizes []Size, canvas Size, o Options, out io.Writer) ([]Page, int, error) {
	n := len(sizes)
	pages := make([]Page, n)
	var flattened, done atomic.Int64

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

	progress := isTerminal(out)
	var outMu sync.Mutex
	tick := func() {
		d := done.Add(1)
		if progress {
			outMu.Lock()
			fmt.Fprintf(out, "\rBuilding    : %d/%d", d, n)
			outMu.Unlock()
		}
	}

	var wg sync.WaitGroup
	for k := range min(o.Jobs, n) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := first
			if k > 0 {
				var err error
				if w, err = r.newWorker(); err != nil {
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
				tick()
			}
		}()
	}
	wg.Wait()
	if progress {
		fmt.Fprintln(out)
	}

	if err := context.Cause(ctx); err != nil {
		return nil, 0, err
	}
	if o.Verbose {
		w := len(fmt.Sprint(n))
		for i, p := range pages {
			fmt.Fprintf(out, "Page %0*d   : %dx%d %s -> %dx%d\n", w, i+1,
				p.Source.W, p.Source.H, p.Orient, p.Size.W, p.Size.H)
		}
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
	if err := jpeg.Encode(&buf, enc, &jpeg.Options{Quality: o.Quality}); err != nil {
		return Page{}, false, fmt.Errorf("page %d: encoding: %w", i+1, err)
	}
	return Page{JPEG: buf.Bytes(), Size: target, Source: size, Orient: size.Orientation()}, fl.Active, nil
}

// writeAtomically writes the EPUB next to its destination and renames it into
// place, so an interrupted run never leaves a half-written book behind or
// destroys the previous one. It returns the bytes written.
func writeAtomically(dst string, b *Book) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeEPUB(&buf, b); err != nil {
		return nil, fmt.Errorf("packaging: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".ebook_converter-*.tmp")
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

// reportProblems prints a tally by code and the first few in full. One bad
// attribute usually repeats on every page, and printing all of them buries
// the distinct problems.
func reportProblems(out io.Writer, lines []string) {
	byCode := map[string]int{}
	for _, l := range lines {
		code := l
		if i := bytes.IndexAny([]byte(l), ":("); i > 0 {
			code = l[:i]
		}
		byCode[code]++
	}
	codes := make([]string, 0, len(byCode))
	for c := range byCode {
		codes = append(codes, c)
	}
	sort.Slice(codes, func(i, j int) bool {
		if byCode[codes[i]] != byCode[codes[j]] {
			return byCode[codes[i]] > byCode[codes[j]]
		}
		return codes[i] < codes[j]
	})
	fmt.Fprintln(out, "  Problems by code:")
	for _, c := range codes {
		fmt.Fprintf(out, "    %5d  %s\n", byCode[c], c)
	}
	fmt.Fprintln(out, "  First few in full:")
	for _, l := range lines[:min(5, len(lines))] {
		if len(l) > 200 {
			l = l[:200] + "..."
		}
		fmt.Fprintf(out, "    %s\n", l)
	}
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func humanBytes(n int) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := int64(n) / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

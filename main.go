// Command leafbind turns a PDF into a Kindle-compatible fixed-layout
// EPUB 3. Every page becomes an image on one shared canvas, so the original
// typesetting survives exactly. It suits scanned books and slide decks, where
// reflowing the text is not an option.
//
// Run without arguments it opens a graphical interface; with arguments it is
// a command-line tool.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
	"syscall"
	"time"
)

// version is set at build time with -ldflags "-X main.version=...", as the
// release builds do. A binary built by "go install" carries its module
// version in its build information instead, and init picks that up.
var version = "dev"

func init() {
	if bi, ok := debug.ReadBuildInfo(); ok {
		version = resolveVersion(version, bi.Main.Version)
	}
}

// resolveVersion prefers a version set at link time, then the module version
// Go recorded, then "dev". Go records "(devel)" when it knows none.
func resolveVersion(linked, module string) string {
	switch {
	case linked != "" && linked != "dev":
		return linked
	case module != "" && module != "(devel)":
		return module
	}
	return "dev"
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if g, ok := guiRequested(args); ok {
		if err := runGUI(ctx, g, stdout); err != nil {
			reportGUIError(stderr, err)
			return 1
		}
		return 0
	}

	o, err := parseArgs(args)
	switch {
	case errors.Is(err, errHelp):
		fmt.Fprint(stdout, usageText)
		return 0
	case errors.Is(err, errVersion):
		fmt.Fprintln(stdout, "leafbind", version)
		return 0
	case errors.Is(err, errLicenses):
		fmt.Fprint(stdout, licenseText, "\n", noticesText)
		return 0
	case err != nil:
		fmt.Fprintf(stderr, "error: %v\n\n%s", err, usageText)
		return 2
	}

	if o.Check != nil {
		return runCheck(o.Check, stdout, stderr)
	}

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

// guiRequested reports whether to open the GUI: when there are no arguments,
// as when the program is double-clicked, or when --gui is given. macOS passes
// a -psn_ process serial number to apps launched from Finder on some versions;
// that counts as no arguments.
func guiRequested(args []string) (guiOptions, bool) {
	var g guiOptions
	gui := true
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "-psn_"):
		case a == "--gui" || a == "-gui":
		case a == "--no-browser" || a == "-no-browser":
			g.NoBrowser = true
		default:
			gui = false
		}
	}
	if len(args) > 0 && !gui {
		return g, false
	}
	return g, true
}

// convert runs one conversion for the command line and prints its progress.
// It returns false when the EPUB was written but failed validation.
func convert(ctx context.Context, o Options, out io.Writer) (bool, error) {
	say := func(format string, args ...any) { fmt.Fprintf(out, format, args...) }
	say("Input       : %s\n", o.Input)
	say("Starting PDF engine...\n")

	eng, err := newEngine(ctx, o.Jobs)
	if err != nil {
		return false, err
	}
	defer eng.Close()

	progress := isTerminal(out)
	rendering := false
	res, err := convertBook(ctx, eng, o, func(e Event) {
		if p := e.Plan; p != nil {
			say("Pages       : %d (%d landscape, %d portrait, %d square)\n",
				p.Pages, p.Tally["landscape"], p.Tally["portrait"], p.Tally["square"])
			if p.Scan {
				say("Resolution  : %d DPI (scan, native resolution)\n", p.DPI)
			} else {
				say("Resolution  : %d DPI\n", p.DPI)
			}
			if !o.Mixed {
				say("Canvas      : %d x %d\n", p.Canvas.W, p.Canvas.H)
			}
			say("Orientation : %s\n", p.Orient.Book)
			say("Colour      : %s\n", map[bool]string{true: "greyscale", false: "sRGB"}[o.Grayscale])
			say("Layout      : %s\n", map[bool]string{true: "mixed (per-page canvas)", false: "normalised (one canvas)"}[o.Mixed])
		}
		if e.Stage == stageRendering && e.Done > 0 {
			rendering = true
			if progress {
				say("\rBuilding    : %d/%d", e.Done, e.Total)
			}
		}
		if e.Stage == stagePackaging && rendering && progress {
			say("\n")
		}
	})
	if err != nil {
		return false, err
	}

	if o.Verbose {
		w := len(fmt.Sprint(res.Pages))
		for i, p := range res.PageInfo {
			say("Page %0*d   : %dx%d %s -> %dx%d\n", w, i+1, p.Source.W, p.Source.H, p.Orient, p.Size.W, p.Size.H)
		}
	}
	say("\nOutput      : %s\n", o.Output)
	say("Size        : %s\n", humanBytes(res.Bytes))
	if o.FlattenBG {
		say("Background  : %d of %d pages flattened to white\n", res.Flattened, res.Pages)
	}
	say("Time        : %s\n", res.Elapsed.Round(100*time.Millisecond))

	if !res.Validated {
		say("Validation  : skipped\n")
		return true, nil
	}
	builtinPassed := true
	lines := make([]string, len(res.Problems))
	for i, p := range res.Problems {
		lines[i] = p.String()
		builtinPassed = builtinPassed && !p.Fails()
	}
	switch {
	case len(lines) == 0:
		say("Validation  : passed\n")
	case builtinPassed:
		say("Validation  : passed, with warnings\n")
		reportProblems(out, lines)
	default:
		say("Validation  : FAILED\n")
		reportProblems(out, lines)
	}
	if o.Epubcheck {
		switch ec := res.Epubcheck; {
		case !ec.Ran:
			say("epubcheck   : not installed, skipped\n")
		case ec.Passed:
			say("epubcheck   : passed (%s)\n", ec.Summary)
		default:
			say("epubcheck   : FAILED (%s)\n", ec.Summary)
			reportProblems(out, ec.Problems)
		}
	}
	if !res.Passed {
		say("\nThe EPUB was kept so it can be inspected.\n")
	}
	return res.Passed, nil
}

// problemLabel matches the SEVERITY(CODE) that starts a finding, in the
// format both epubcheck and the built-in checks print.
var problemLabel = regexp.MustCompile(`^[A-Z]+\([A-Z]{2,3}[-_][0-9]{3}[a-z]?\)`)

// problemCounts tallies findings by severity and code, most frequent first.
func problemCounts(lines []string) ([]string, map[string]int) {
	byCode := map[string]int{}
	for _, l := range lines {
		code := problemLabel.FindString(l)
		if code == "" {
			code, _, _ = strings.Cut(l, ":")
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
	return codes, byCode
}

// reportProblems prints a tally by code and the first few in full. One bad
// attribute usually repeats on every page, and printing all of them buries
// the distinct problems.
func reportProblems(out io.Writer, lines []string) {
	codes, byCode := problemCounts(lines)
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

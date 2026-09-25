package main

// Command-line parsing.
//
// Go's flag package stops at the first positional argument. Options are
// accepted anywhere on the command line here, in --name value, --name=value
// and -name forms, so this is a small parser of its own.

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Options is a parsed command line.
type Options struct {
	Input, Output string

	DPI       int
	MaxEdge   int
	Quality   int
	Grayscale bool
	FlattenBG bool
	Title     string
	Lang      string
	Direction string
	Orient    string // "" means detect
	Mixed     bool
	Validate  bool
	Epubcheck bool
	Jobs      int
	Verbose   bool

	Check []string // --check: EPUBs to validate instead of converting
}

func defaultOptions() Options {
	return Options{
		DPI:       dpiAuto,
		MaxEdge:   2560,
		Quality:   92,
		Lang:      "en",
		Direction: "ltr",
		Validate:  true,
		Epubcheck: true,
		Jobs:      min(runtime.NumCPU(), 6),
	}
}

// dpiAuto selects the resolution per document: a scan's native resolution,
// otherwise defaultDPI.
const (
	dpiAuto    = 0
	defaultDPI = 180
)

var (
	errHelp     = errors.New("help requested")
	errVersion  = errors.New("version requested")
	errLicenses = errors.New("licenses requested")
)

const usageText = `Convert a PDF into a Kindle-compatible fixed-layout EPUB 3.

Usage:
  leafbind [options] input.pdf output.epub   convert from the command line
  leafbind                                   open the graphical interface
  leafbind --check book.epub...              validate EPUBs

Run without arguments, or double-click it, to use the graphical interface.
--gui opens it explicitly; --no-browser prints its address instead of
opening a window.

Options:
  --dpi N|auto       Render resolution; auto uses a scan's native
                     resolution, otherwise 180       (default auto)
  --max-edge N       Cap the longest canvas edge     (default 2560, 0 = no cap)
  --quality N        JPEG quality 1-100              (default 92)
  --grayscale        8-bit greyscale for e-ink       (also --greyscale, --mono)
  --flatten-bg       Tinted page background -> white (also --white-bg)
  --title TEXT       Book title                      (default: PDF file name)
  --lang CODE        BCP 47 language code            (default en)
  --rtl              Right-to-left reading order     (default is LTR; --ltr)
  --orientation X    Force portrait, landscape, auto or none
  --mixed            Keep per-page canvases instead of one shared canvas
  --jobs N           Pages rendered in parallel      (default: CPUs, max 6)
  --no-validate      Skip all validation
  --no-epubcheck     Skip the external epubcheck even when it is installed
  --check            Validate the EPUBs given, with the built-in checks
  -v, --verbose      Print one line per page
  -h, --help         Show this help
  --version          Show the version
  --licenses         Show the license and third-party notices

Exit status: 0 success, 1 conversion or validation failure, 2 usage error.
`

func parseArgs(args []string) (Options, error) {
	o := defaultOptions()
	var pos []string

	type spec struct {
		value bool // takes a value
		set   func(string) error
	}
	intFlag := func(dst *int, lo, hi int, name string) spec {
		return spec{true, func(v string) error {
			n, err := strconv.Atoi(v)
			if err != nil || n < lo || n > hi {
				return fmt.Errorf("--%s must be a whole number from %d to %d, got %q", name, lo, hi, v)
			}
			*dst = n
			return nil
		}}
	}
	boolFlag := func(set func()) spec {
		return spec{false, func(string) error { set(); return nil }}
	}
	grey := boolFlag(func() { o.Grayscale = true })
	flat := boolFlag(func() { o.FlattenBG = true })

	flags := map[string]spec{
		"dpi": {true, func(v string) error {
			if v == "auto" {
				o.DPI = dpiAuto
				return nil
			}
			n, err := strconv.Atoi(v)
			if err != nil || n < 18 || n > 1200 {
				return fmt.Errorf("--dpi must be auto or a whole number from 18 to 1200, got %q", v)
			}
			o.DPI = n
			return nil
		}},
		"max-edge":           intFlag(&o.MaxEdge, 0, 20000, "max-edge"),
		"quality":            intFlag(&o.Quality, 1, 100, "quality"),
		"jobs":               intFlag(&o.Jobs, 1, 64, "jobs"),
		"grayscale":          grey,
		"greyscale":          grey,
		"mono":               grey,
		"flatten-bg":         flat,
		"white-bg":           flat,
		"flatten-background": flat,
		"ltr":                boolFlag(func() { o.Direction = "ltr" }),
		"rtl":                boolFlag(func() { o.Direction = "rtl" }),
		"mixed":              boolFlag(func() { o.Mixed = true }),
		"no-validate":        boolFlag(func() { o.Validate = false }),
		"no-epubcheck":       boolFlag(func() { o.Epubcheck = false }),
		"verbose":            boolFlag(func() { o.Verbose = true }),
		"check":              boolFlag(func() { o.Check = []string{} }),
		"v":                  boolFlag(func() { o.Verbose = true }),
		"title":              {true, func(v string) error { o.Title = v; return nil }},
		"lang": {true, func(v string) error {
			if !validLang(v) {
				return fmt.Errorf("--lang %q is not a well-formed BCP 47 language tag (for example ur, ar, en, ur-Latn)", v)
			}
			o.Lang = v
			return nil
		}},
		"orientation": {true, func(v string) error {
			switch v {
			case "portrait", "landscape", "auto", "none":
				o.Orient = v
				return nil
			}
			return fmt.Errorf("--orientation must be portrait, landscape, auto or none, got %q", v)
		}},
	}

	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if len(a) < 2 || a[0] != '-' {
			pos = append(pos, a)
			continue
		}
		name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		switch name {
		case "h", "help":
			return o, errHelp
		case "version":
			return o, errVersion
		case "licenses":
			return o, errLicenses
		}
		s, ok := flags[name]
		if !ok {
			return o, fmt.Errorf("unknown option %s", a)
		}
		if !s.value {
			if hasVal {
				return o, fmt.Errorf("option --%s takes no value", name)
			}
			s.set("")
			continue
		}
		if !hasVal {
			if i+1 >= len(args) {
				return o, fmt.Errorf("option --%s needs a value", name)
			}
			i++
			val = args[i]
		}
		if err := s.set(val); err != nil {
			return o, err
		}
	}

	if o.Check != nil {
		if len(pos) == 0 {
			return o, errors.New("--check needs at least one EPUB")
		}
		o.Check = pos
		return o, nil
	}
	if len(pos) != 2 {
		return o, fmt.Errorf("expected an input PDF and an output EPUB, got %d argument(s)", len(pos))
	}
	o.Input, o.Output = pos[0], pos[1]

	if in, err := filepath.Abs(o.Input); err == nil {
		if out, err := filepath.Abs(o.Output); err == nil && in == out {
			return o, errors.New("the output would overwrite the input PDF")
		}
	}
	if o.Title == "" {
		base := filepath.Base(o.Input)
		o.Title = strings.TrimSuffix(base, filepath.Ext(base))
	}
	return o, nil
}
